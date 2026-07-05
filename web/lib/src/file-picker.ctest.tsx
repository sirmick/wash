// Component test (Tier B) for the FilePicker's path-bar navigation:
// Back (history trail), Up (strictly one level up the tree), "/"
// (filesystem root) and "~" (BE default start / home). Issue #17's
// symptom — Up refusing to climb above $HOME — was a BE-side rewrite
// of "/" (covered in internal/sdk/filepicker_test.go); this tier
// pins the FE contract: which paths the picker actually requests and
// how the buttons drive them. The full FE↔BE round trip stays in
// e2e/tests/file-picker.spec.ts.

import { test, expect, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@solidjs/testing-library';
import { FilePicker } from './file-picker.tsx';

afterEach(cleanup);

const HOME = '/home/mick';

// In-memory directory tree served by the fake host BE. Mirrors the
// wire shape of fs.list_ok entries; empty-path requests resolve to
// HOME the way sdk.EnableFilePicker's default-start path does for an
// unconfined host.
const TREE: Record<string, string[]> = {
  '/': ['home'],
  '/home': ['mick'],
  '/home/mick': ['docs'],
  '/home/mick/docs': [],
};

// Raw `path` field of every fs.list request the picker sent, in
// order — lets tests assert the wire contract (e.g. Home sends "").
let listed: string[] = [];

const dirEntry = (name: string) => ({ name, type: 'dir', size: 0, mod_unix: 0 });

// installWash wires window.wash.sendAppMsg to answer fs.* against
// TREE, replying asynchronously on the host element the way the real
// BE round trip does.
const installWash = (host: HTMLElement) => {
  listed = [];
  (window as unknown as { wash: unknown }).wash = {
    sendAppMsg: (_inst: string, data: { kind: string; id?: string; path?: string }) => {
      const { kind, id, path } = data;
      let reply: Record<string, unknown> | null = null;
      if (kind === 'fs.list') {
        listed.push(path ?? '');
        const p = path === '' ? HOME : (path ?? '');
        reply = TREE[p]
          ? { kind: 'fs.list_ok', id, path: p, entries: TREE[p].map(dirEntry), truncated: false }
          : { kind: 'fs.list_err', id, code: 'not_found', msg: `no such dir: ${p}` };
      } else if (kind === 'fs.root') {
        reply = { kind: 'fs.root_ok', id, root: '' };
      }
      // fs.watch / fs.unwatch are fire-and-forget for the picker.
      if (!reply) return;
      const r = reply;
      queueMicrotask(() => host.dispatchEvent(new CustomEvent('wash:msg', { detail: r })));
    },
  };
};

let host: HTMLElement;
beforeEach(() => {
  host = document.createElement('div');
  document.body.appendChild(host);
  installWash(host);
});

const mountPicker = (start = HOME) =>
  render(() => (
    <FilePicker
      open={true}
      mode="open"
      host={host}
      hostInstanceID="i-test"
      start={start}
      onConfirm={() => {}}
      onCancel={() => {}}
      data-testid="picker"
    />
  ));

const pathValue = (getByTestId: (id: string) => HTMLElement) =>
  (getByTestId('fp-path') as HTMLInputElement).value;

test('Up climbs one level at a time all the way to /', async () => {
  const { getByTestId } = mountPicker();
  await waitFor(() => expect(pathValue(getByTestId)).toBe(HOME));

  fireEvent.click(getByTestId('fp-up'));
  await waitFor(() => expect(pathValue(getByTestId)).toBe('/home'));

  fireEvent.click(getByTestId('fp-up'));
  await waitFor(() => expect(pathValue(getByTestId)).toBe('/'));
  expect(listed).toContain('/');

  // At the root, Up is a no-op — no further fs.list goes out.
  const sent = listed.length;
  fireEvent.click(getByTestId('fp-up'));
  expect(listed.length).toBe(sent);
  expect(pathValue(getByTestId)).toBe('/');
});

test('"/" button jumps straight to the filesystem root', async () => {
  const { getByTestId } = mountPicker();
  await waitFor(() => expect(pathValue(getByTestId)).toBe(HOME));

  fireEvent.click(getByTestId('fp-root'));
  await waitFor(() => expect(pathValue(getByTestId)).toBe('/'));
  expect(listed[listed.length - 1]).toBe('/');
});

test('"~" button requests the BE default start (empty path)', async () => {
  const { getByTestId } = mountPicker();
  await waitFor(() => expect(pathValue(getByTestId)).toBe(HOME));

  fireEvent.click(getByTestId('fp-root'));
  await waitFor(() => expect(pathValue(getByTestId)).toBe('/'));

  fireEvent.click(getByTestId('fp-home'));
  await waitFor(() => expect(pathValue(getByTestId)).toBe(HOME));
  // Home is expressed as the empty path on the wire — the BE, not
  // the FE, decides where "home" is (sandbox root when confined).
  expect(listed[listed.length - 1]).toBe('');
});

test('Back retraces committed navigations and disables when the trail is empty', async () => {
  const { getByTestId } = mountPicker();
  // The path bar is prefilled from props.start, so wait for the
  // initial listing (the docs row) rather than the input value.
  await waitFor(() => getByTestId('fp-entry-docs'));

  // Nothing to go back to yet.
  const back = getByTestId('fp-back') as HTMLButtonElement;
  expect(back.disabled).toBe(true);

  // HOME → docs (double-click) → / (root button).
  fireEvent.dblClick(getByTestId('fp-entry-docs'));
  await waitFor(() => expect(pathValue(getByTestId)).toBe(`${HOME}/docs`));
  fireEvent.click(getByTestId('fp-root'));
  await waitFor(() => expect(pathValue(getByTestId)).toBe('/'));
  expect(back.disabled).toBe(false);

  // Back walks the trail in reverse: docs, then HOME.
  fireEvent.click(back);
  await waitFor(() => expect(pathValue(getByTestId)).toBe(`${HOME}/docs`));
  fireEvent.click(back);
  await waitFor(() => expect(pathValue(getByTestId)).toBe(HOME));
  expect(back.disabled).toBe(true);
});
