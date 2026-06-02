// Component test (Tier B): mounts WifiDialog in jsdom and drives the manual
// form — PSK gating by security, the 8–63 length rule, and the onConnect args.

import { test, expect, afterEach, vi } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { WifiDialog, secTagFromAP } from './WifiDialog.tsx';

afterEach(cleanup);

test('secTagFromAP maps the security column to a union tag', () => {
  expect(secTagFromAP('WPA3')).toBe('sae');
  expect(secTagFromAP('WPA2 WPA3')).toBe('sae');
  expect(secTagFromAP('WPA2')).toBe('psk2');
  expect(secTagFromAP('')).toBe('none');
});

test('no AP picker when not live; picker present + click prefills the form when live', () => {
  const aps = [
    { ssid: 'home', signal: 80, security: 'WPA2', in_use: true },
    { ssid: 'cafe', signal: 50, security: '', in_use: false },
  ];
  // not live → no scan section
  const off = render(() => <WifiDialog live={false} busy={false} onConnect={() => {}} onCancel={() => {}} />);
  expect(off.queryByTestId('wifi-scan')).toBeNull();
  cleanup();

  // live → picker present; clicking the open AP prefills SSID + security=none
  const { getByTestId, queryByTestId } = render(() => (
    <WifiDialog live={true} busy={false} aps={aps} onConnect={() => {}} onCancel={() => {}} />
  ));
  expect(queryByTestId('wifi-scan')).toBeTruthy();
  fireEvent.click(getByTestId('ap-cafe'));
  expect((getByTestId('wifi-ssid') as HTMLInputElement).value).toBe('cafe');
  expect((getByTestId('wifi-security') as HTMLSelectElement).value).toBe('none');
  expect(queryByTestId('wifi-psk')).toBeNull(); // open ⇒ no PSK field
});

test('PSK field gates on security; Connect needs a valid PSK; emits the chosen args', () => {
  const onConnect = vi.fn();
  const { getByTestId, queryByTestId } = render(() => (
    <WifiDialog live={false} busy={false} onConnect={onConnect} onCancel={() => {}} />
  ));

  const connect = getByTestId('wifi-connect') as HTMLButtonElement;
  // default security is WPA2-PSK → PSK field present, Connect disabled (no ssid/psk yet)
  expect(queryByTestId('wifi-psk')).toBeTruthy();
  expect(connect.disabled).toBe(true);

  fireEvent.input(getByTestId('wifi-ssid'), { target: { value: 'home' } });
  fireEvent.input(getByTestId('wifi-psk'), { target: { value: 'short' } }); // < 8 chars
  expect(connect.disabled).toBe(true);

  fireEvent.input(getByTestId('wifi-psk'), { target: { value: 'longenough' } });
  expect(connect.disabled).toBe(false);

  fireEvent.click(connect);
  expect(onConnect).toHaveBeenCalledWith('home', 'psk2', 'longenough', false);
});

test('open security hides the PSK field and connects with an empty key', () => {
  const onConnect = vi.fn();
  const { getByTestId, queryByTestId } = render(() => (
    <WifiDialog live={true} busy={false} onConnect={onConnect} onCancel={() => {}} />
  ));

  fireEvent.change(getByTestId('wifi-security'), { target: { value: 'none' } });
  expect(queryByTestId('wifi-psk')).toBeNull();

  fireEvent.input(getByTestId('wifi-ssid'), { target: { value: 'cafe' } });
  const connect = getByTestId('wifi-connect') as HTMLButtonElement;
  expect(connect.disabled).toBe(false);

  fireEvent.click(connect);
  expect(onConnect).toHaveBeenCalledWith('cafe', 'none', '', false);
});
