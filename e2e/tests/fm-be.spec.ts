// fm BE-only tests. No browser — we spawn fm via the router's
// control socket and exercise every mutation op (rename, delete,
// create_file, create_dir, write, read) by sending app_msgs and
// awaiting matched replies via the request_id correlation that the
// router and BE both honor.
//
// Goal: prove that the app_msg surface works in isolation, so when
// a FE test later fails we know the wiring failure is in the FE
// half and not the BE.

import { test, expect } from '../fixtures/router';
import { existsSync, readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';

// Every test in this file wants the fm sandbox active so a wayward
// path can't escape into the user's tree.
test.use({ routerOpts: { fmRoot: true } });

// launchFm spins up an fm instance via the control socket and
// returns its instance id. Each test starts from a clean instance
// to keep state simple.
async function launchFm(router: import('../fixtures/router').RouterHandle): Promise<string> {
  const resp = await router.controlRequest({ t: 'launch', app_id: 'com.wash.fm' });
  expect(resp.t).toBe('launched');
  expect(typeof resp.instance_id).toBe('string');
  return resp.instance_id as string;
}

test.describe('fm BE app_msg surface', () => {
  test('create_file → write → read → delete round-trip', async ({ router }) => {
    const inst = await launchFm(router);
    const target = join(router.fmRoot, 'created.txt');

    const c1 = await router.sendAppMsg(inst, { kind: 'create_file', id: 'r1', path: target });
    expect(c1.kind).toBe('create_file_ok');
    expect(existsSync(target)).toBe(true);

    const w1 = await router.sendAppMsg(inst, {
      kind: 'write',
      id: 'r2',
      path: target,
      content: 'payload one\n',
    });
    expect(w1.kind).toBe('write_ok');
    expect(w1.bytes).toBe('payload one\n'.length);
    expect(readFileSync(target, 'utf8')).toBe('payload one\n');

    const r1 = await router.sendAppMsg(inst, { kind: 'read', id: 'r3', path: target });
    expect(r1.kind).toBe('read_ok');
    expect(r1.binary).toBe(false);
    expect(r1.content).toBe('payload one\n');
    expect(r1.size).toBe('payload one\n'.length);

    const d1 = await router.sendAppMsg(inst, { kind: 'delete', id: 'r4', path: target });
    expect(d1.kind).toBe('delete_ok');
    expect(existsSync(target)).toBe(false);
  });

  test('create_dir then rename then list reflects the change', async ({ router }) => {
    const inst = await launchFm(router);
    const original = join(router.fmRoot, 'mydir');
    const renamed = join(router.fmRoot, 'yourdir');

    const c = await router.sendAppMsg(inst, { kind: 'create_dir', id: 'c', path: original });
    expect(c.kind).toBe('create_dir_ok');
    expect(existsSync(original)).toBe(true);

    const r = await router.sendAppMsg(inst, { kind: 'rename', id: 'r', from: original, to: renamed });
    expect(r.kind).toBe('rename_ok');
    expect(existsSync(original)).toBe(false);
    expect(existsSync(renamed)).toBe(true);

    const l = await router.sendAppMsg(inst, { kind: 'list', id: 'l', path: router.fmRoot });
    expect(l.kind).toBe('list_ok');
    const entries = (l.entries as Array<{ name: string; type: string }>) ?? [];
    expect(entries.some((e) => e.name === 'yourdir' && e.type === 'dir')).toBe(true);
    expect(entries.some((e) => e.name === 'mydir')).toBe(false);
  });

  test('delete refuses non-empty directory (not_empty), accepts empty', async ({ router }) => {
    const inst = await launchFm(router);
    const parent = join(router.fmRoot, 'nest');
    const child = join(parent, 'file.txt');
    mkdirSync(parent);
    writeFileSync(child, 'x\n');

    const refuse = await router.sendAppMsg(inst, { kind: 'delete', id: 'r1', path: parent });
    expect(refuse.kind).toBe('delete_err');
    expect(refuse.code).toBe('not_empty');
    expect(existsSync(parent)).toBe(true);

    // Remove the child first, then the now-empty parent.
    const dchild = await router.sendAppMsg(inst, { kind: 'delete', id: 'r2', path: child });
    expect(dchild.kind).toBe('delete_ok');
    const dparent = await router.sendAppMsg(inst, { kind: 'delete', id: 'r3', path: parent });
    expect(dparent.kind).toBe('delete_ok');
    expect(existsSync(parent)).toBe(false);
  });

  test('rename refuses overwrite of existing destination', async ({ router }) => {
    const inst = await launchFm(router);
    const a = join(router.fmRoot, 'a.txt');
    const b = join(router.fmRoot, 'b.txt');
    writeFileSync(a, 'A\n');
    writeFileSync(b, 'B\n');

    const r = await router.sendAppMsg(inst, { kind: 'rename', id: 'r1', from: a, to: b });
    expect(r.kind).toBe('rename_err');
    expect(r.code).toBe('exists');
    expect(readFileSync(a, 'utf8')).toBe('A\n');
    expect(readFileSync(b, 'utf8')).toBe('B\n');
  });

  test('create_file refuses to clobber existing file (exists)', async ({ router }) => {
    const inst = await launchFm(router);
    const target = join(router.fmRoot, 'fixed.txt');
    writeFileSync(target, 'already here\n');

    const r = await router.sendAppMsg(inst, { kind: 'create_file', id: 'r1', path: target });
    expect(r.kind).toBe('create_file_err');
    expect(r.code).toBe('exists');
    expect(readFileSync(target, 'utf8')).toBe('already here\n');
  });

  test('write is atomic — temp file is not left behind', async ({ router }) => {
    const inst = await launchFm(router);
    const target = join(router.fmRoot, 'atomic.txt');
    writeFileSync(target, 'old\n');

    const w = await router.sendAppMsg(inst, {
      kind: 'write',
      id: 'r1',
      path: target,
      content: 'new\n',
    });
    expect(w.kind).toBe('write_ok');
    expect(readFileSync(target, 'utf8')).toBe('new\n');

    // List the directory and ensure no .wash-fm.tmp.* sibling
    // survived — proves the rename happened and the temp was
    // cleaned up.
    const l = await router.sendAppMsg(inst, { kind: 'list', id: 'r2', path: router.fmRoot });
    expect(l.kind).toBe('list_ok');
    const entries = (l.entries as Array<{ name: string }>) ?? [];
    expect(entries.some((e) => e.name.startsWith('.wash-fm.tmp.'))).toBe(false);
  });

  test('outside_root: every op refuses paths above the sandbox', async ({ router }) => {
    const inst = await launchFm(router);
    const outside = '/tmp/wash-fm-outside-must-not-exist.txt';

    for (const op of [
      { kind: 'list', id: 'o-list', path: outside },
      { kind: 'read', id: 'o-read', path: outside },
      { kind: 'create_file', id: 'o-cf', path: outside },
      { kind: 'create_dir', id: 'o-cd', path: outside },
      { kind: 'delete', id: 'o-del', path: outside },
      { kind: 'write', id: 'o-wr', path: outside, content: 'x' },
    ]) {
      const r = await router.sendAppMsg(inst, op);
      expect(r.kind).toBe(`${op.kind}_err`);
      expect(r.code).toBe('outside_root');
    }
    // rename — both ends checked; outside `to` must reject even if
    // `from` is inside the root.
    const inside = join(router.fmRoot, 'safe.txt');
    writeFileSync(inside, 'safe\n');
    const r = await router.sendAppMsg(inst, {
      kind: 'rename', id: 'o-mv', from: inside, to: outside,
    });
    expect(r.kind).toBe('rename_err');
    expect(r.code).toBe('outside_root');
    expect(existsSync(outside)).toBe(false);
    expect(readFileSync(inside, 'utf8')).toBe('safe\n');
  });

  test('delete refuses the sandbox root itself (forbidden)', async ({ router }) => {
    const inst = await launchFm(router);
    const r = await router.sendAppMsg(inst, { kind: 'delete', id: 'r1', path: router.fmRoot });
    expect(r.kind).toBe('delete_err');
    expect(r.code).toBe('forbidden');
    expect(existsSync(router.fmRoot)).toBe(true);
  });

  test('request_id is required to await a reply (precedent)', async ({ router }) => {
    const inst = await launchFm(router);
    // No `id` → sendAppMsg fires-and-forgets via the control
    // socket; we only get back the {t:"msg.ok"} ack. The reply
    // from the BE is still emitted (the FE/shell would see it)
    // but the test harness has no way to correlate it. This is
    // by design: tests opt into the await by providing an id.
    const target = join(router.fmRoot, 'no-id.txt');
    const noAwait = await router.sendAppMsg(inst, { kind: 'create_file', path: target });
    expect(noAwait).toEqual({}); // {} maps to msg.ok in the fixture
    // Filesystem still got updated even without correlation.
    expect(existsSync(target)).toBe(true);
  });
});
