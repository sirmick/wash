// Component test (Tier B): mounts WifiDialog in jsdom and drives the manual
// form — PSK gating by security, the 8–63 length rule, and the onConnect args.

import { test, expect, afterEach, vi } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { WifiDialog } from './WifiDialog.tsx';

afterEach(cleanup);

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
