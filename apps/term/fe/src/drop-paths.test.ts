// Dropping paths onto a terminal pane (drop-paths.ts). The quoting is the
// part worth testing: a path that reaches the shell wrong is either a
// broken command or, with the wrong character in the name, a different one.

import { test } from 'node:test';
import assert from 'node:assert/strict';

import { WASH_PATHS_MIME, acceptsDrop, dropText, pathsFrom, shellQuote } from './drop-paths.ts';

const dt = (types: string[], data: Record<string, string>) => ({
  types,
  getData: (f: string) => data[f] ?? '',
});

test('the drag MIME is the one fm actually sets', () => {
  // Pinned by value rather than by import: @wash/ui's barrel is not
  // resolvable from a bare node:test run. web/fs-client/src/dnd.ts and
  // web/lib/src/agent-compose-drop.ts hold the other two copies.
  assert.equal(WASH_PATHS_MIME, 'application/x-wash-paths');
});

test('an ordinary path is left bare', () => {
  assert.equal(shellQuote('/etc/hosts'), '/etc/hosts');
  assert.equal(shellQuote('/home/u/a-b_c.2/x+y@z'), '/home/u/a-b_c.2/x+y@z');
});

test('anything the shell would act on is single-quoted', () => {
  assert.equal(shellQuote('/home/u/My Docs'), "'/home/u/My Docs'");
  assert.equal(shellQuote('/tmp/$HOME'), "'/tmp/$HOME'");
  assert.equal(shellQuote('/tmp/a;rm -rf b'), "'/tmp/a;rm -rf b'");
  assert.equal(shellQuote('/tmp/`id`'), "'/tmp/`id`'");
  assert.equal(shellQuote('/tmp/a*b'), "'/tmp/a*b'");
  assert.equal(shellQuote(''), "''");
});

test("a single quote in the name leaves the quotes and comes back", () => {
  // '\'' — close, escaped quote, reopen. This is the only escape a
  // single-quoted shell word has.
  assert.equal(shellQuote("/tmp/it's"), "'/tmp/it'\\''s'");
});

test('fm’s JSON payload wins, in order', () => {
  const d = dt([WASH_PATHS_MIME, 'text/plain'], {
    [WASH_PATHS_MIME]: JSON.stringify(['/a', '/b c']),
    'text/plain': '/ignored',
  });
  assert.deepEqual(pathsFrom(d), ['/a', '/b c']);
  assert.equal(acceptsDrop(d), true);
});

test('a malformed wash payload yields nothing rather than throwing', () => {
  const d = dt([WASH_PATHS_MIME], { [WASH_PATHS_MIME]: '{not json' });
  assert.deepEqual(pathsFrom(d), []);
  assert.equal(acceptsDrop(d), false);
});

test('newline-joined absolute paths are accepted from text/plain', () => {
  const d = dt(['text/plain'], { 'text/plain': '/a\n/b c\n' });
  assert.deepEqual(pathsFrom(d), ['/a', '/b c']);
});

test('prose is not a path list — that drop stays an ordinary paste', () => {
  assert.deepEqual(pathsFrom(dt(['text/plain'], { 'text/plain': 'hello there' })), []);
  // One non-path line disqualifies the whole drop: a half-quoted mixture
  // would be worse than leaving it to the plain text path.
  assert.deepEqual(pathsFrom(dt(['text/plain'], { 'text/plain': '/a\nnope' })), []);
});

test('nothing at all', () => {
  assert.deepEqual(pathsFrom(null), []);
  assert.deepEqual(pathsFrom(dt(['Files'], {})), []);
  assert.equal(acceptsDrop(undefined), false);
});

test('the pasted text is space-separated, trailing space, never a newline', () => {
  assert.equal(dropText(['/a', '/b c']), "/a '/b c' ");
  assert.equal(dropText([]), '');
  assert.ok(!dropText(['/a']).includes('\n'));
});
