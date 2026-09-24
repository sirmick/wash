// Tests for the panel loader's completion rule.
// Run with: cd web/shell && npx tsx --test src/panels.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  loadSettingsPanel, handlePanelReadOK, pushPanelBytes, finishPanel, setPanelImporter,
} from './panels.ts';

// A blob URL cannot be imported outside a browser, so the DOM step is
// stubbed; what these tests are about is WHEN it fires.
let imported: Uint8Array[][] = [];
setPanelImporter(async (chunks) => { imported.push(chunks); });

// A panel module that defines nothing — enough to blob-import.
const MODULE = new TextEncoder().encode('export const ok = 1;\n');

let appSeq = 0;
const freshApp = () => `com.wash.panel-test-${++appSeq}`;

function startLoad(): { appID: string; reqID: number; promise: Promise<void> } {
  const appID = freshApp();
  let reqID = 0;
  const promise = loadSettingsPanel((msg) => {
    reqID = (msg as { req_id: number }).req_id;
  }, appID);
  // Swallow rejections until each test asserts on them.
  promise.catch(() => {});
  return { appID, reqID, promise };
}

// The point of the change: the module imports when the promised bytes
// have arrived, WITHOUT the Unbind. That is what lets panel data ride a
// lower-priority lane than the control frames around it — an Unbind
// overtaking the data can no longer truncate the transfer.
test('the panel imports on byte count, before any unbind', async () => {
  const { reqID, promise } = startLoad();
  handlePanelReadOK({ req_id: reqID, channel_id: 4001, size: MODULE.byteLength });
  assert.equal(pushPanelBytes(4001, MODULE), true);
  await promise; // resolves with no finishPanel call at all
});

// Split across frames, as a 32 KB-chunked transfer arrives.
test('chunks accumulate until the promised size is reached', async () => {
  const { reqID, promise } = startLoad();
  handlePanelReadOK({ req_id: reqID, channel_id: 4002, size: MODULE.byteLength });
  const half = MODULE.byteLength >> 1;
  pushPanelBytes(4002, MODULE.slice(0, half));
  let settled = false;
  promise.then(() => { settled = true; }, () => { settled = true; });
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(settled, false, 'imported before every byte arrived');
  pushPanelBytes(4002, MODULE.slice(half));
  await promise;
});

// A stream that ends early must reject rather than leave the caller on a
// promise that can never settle — the job finishPanel keeps now that it
// is no longer the completion path. This is only safe because the router
// sends the Unbind on the panel data's own lane, so it cannot arrive
// before the bytes; when it rode a faster lane it arrived first and tore
// down every panel load.
test('an unbind before the bytes are in rejects the load', async () => {
  const { reqID, promise } = startLoad();
  handlePanelReadOK({ req_id: reqID, channel_id: 4003, size: MODULE.byteLength + 999 });
  pushPanelBytes(4003, MODULE);
  finishPanel(4003);
  await assert.rejects(promise, /panel stream ended after/);
});

// The unbind that follows a completed transfer is the normal case and
// must do nothing at all.
test('the unbind after a completed panel is a no-op', async () => {
  const { reqID, promise } = startLoad();
  handlePanelReadOK({ req_id: reqID, channel_id: 4004, size: MODULE.byteLength });
  pushPanelBytes(4004, MODULE);
  await promise;
  finishPanel(4004); // must not throw, must not reject anything
});

// Bytes for a channel nobody is waiting on are not ours to consume.
test('bytes for an unknown channel are not consumed', () => {
  assert.equal(pushPanelBytes(9999, MODULE), false);
});
