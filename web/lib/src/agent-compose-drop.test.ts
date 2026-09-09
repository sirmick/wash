// The composer's drop decisions, framework-free (agent-compose-drop.ts).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  WASH_PATHS_MIME,
  MAX_ATTACH_BYTES,
  acceptsDrop,
  describeSkipped,
  fenceLang,
  fencedAttachment,
  insertAt,
  isTextLike,
  pathRef,
  pathRefs,
  washPathsFrom,
} from './agent-compose-drop.ts';
import { DRAG_MIME } from '../../fs-client/src/dnd.ts';

// @wash/ui does not depend on @wash/fs-client, so the MIME is restated.
// This is what keeps the two from drifting apart.
test('the wash drag MIME matches @wash/fs-client', () => {
  assert.equal(WASH_PATHS_MIME, DRAG_MIME);
});

const dt = (types: string[], data: Record<string, string> = {}, files: File[] = []) => ({
  types,
  getData: (f: string) => data[f] ?? '',
  files,
});

test('a wash drag yields its paths; anything else yields none', () => {
  assert.deepEqual(washPathsFrom(dt([WASH_PATHS_MIME], { [WASH_PATHS_MIME]: JSON.stringify(['/a/b.go', '/c d/e.md']) })), ['/a/b.go', '/c d/e.md']);
  // Non-strings and empties are filtered; malformed JSON is ignored.
  assert.deepEqual(washPathsFrom(dt([WASH_PATHS_MIME], { [WASH_PATHS_MIME]: JSON.stringify(['/x', 3, '', null]) })), ['/x']);
  assert.deepEqual(washPathsFrom(dt([WASH_PATHS_MIME], { [WASH_PATHS_MIME]: '{not json' })), []);
  assert.deepEqual(washPathsFrom(dt(['text/plain'], { 'text/plain': '/a' })), []);
  assert.deepEqual(washPathsFrom(null), []);
});

test('the dragover gate accepts wash paths and OS files, nothing else', () => {
  assert.equal(acceptsDrop(dt([WASH_PATHS_MIME])), true);
  assert.equal(acceptsDrop(dt(['Files'])), true);
  assert.equal(acceptsDrop(dt(['text/plain', 'text/uri-list'])), false);
  assert.equal(acceptsDrop(undefined), false);
});

test('a path becomes an @reference, quoted when whitespace would split it', () => {
  assert.equal(pathRef('/home/u/wash/main.go'), '@/home/u/wash/main.go');
  assert.equal(pathRef('/home/u/My Docs/a.md'), '@"/home/u/My Docs/a.md"');
  assert.equal(pathRef('/q/"x" y'), '@"/q/\\"x\\" y"');
  assert.equal(pathRefs(['/a', '/b c']), '@/a @"/b c"');
});

test('insertion lands at the caret with a space either side when needed', () => {
  // Empty composer: no padding, caret after.
  assert.deepEqual(insertAt('', 0, 0, '@/a'), { text: '@/a', caret: 3 });
  // Mid-word: padded both sides.
  assert.deepEqual(insertAt('look at this', 7, 7, '@/a'), { text: 'look at @/a this', caret: 11 });
  // After a space and at the end: no padding needed.
  assert.deepEqual(insertAt('look at ', 8, 8, '@/a'), { text: 'look at @/a', caret: 11 });
  // Replaces a selection.
  assert.deepEqual(insertAt('fix THAT please', 4, 8, '@/a'), { text: 'fix @/a please', caret: 7 });
  // Out-of-range caret is clamped rather than thrown.
  assert.deepEqual(insertAt('ab', 99, 99, 'c'), { text: 'ab c', caret: 4 });
});

test('text-likeness: MIME first, then a known extension, under the cap', () => {
  assert.equal(isTextLike({ name: 'notes.txt', type: 'text/plain', size: 10 }), true);
  assert.equal(isTextLike({ name: 'main.go', type: '', size: 10 }), true);
  assert.equal(isTextLike({ name: 'Makefile', type: '', size: 10 }), true);
  assert.equal(isTextLike({ name: 'data.json', type: 'application/json', size: 10 }), true);
  assert.equal(isTextLike({ name: 'pic.png', type: 'image/png', size: 10 }), false);
  assert.equal(isTextLike({ name: 'blob.bin', type: '', size: 10 }), false);
  assert.equal(isTextLike({ name: 'archive.zip', type: 'application/zip', size: 10 }), false);
  assert.equal(isTextLike({ name: 'big.txt', type: 'text/plain', size: MAX_ATTACH_BYTES + 1 }), false);
});

test('an inline attachment is named, fenced, and fence-safe', () => {
  const md = '# Title\n\n```js\nx()\n```\n';
  const out = fencedAttachment('README.md', md);
  // The fence is longer than the content's own, so the inner one cannot
  // close ours early.
  assert.equal(out, 'README.md:\n````markdown\n' + md + '````');
  assert.equal(fencedAttachment('a.go', 'package a'), 'a.go:\n```go\npackage a\n```');
  assert.equal(fenceLang('notes.txt'), '');
  assert.equal(fenceLang('x.tsx'), 'tsx');
  assert.equal(fenceLang('weird.name.with.dots.py'), 'python');
  assert.equal(fenceLang('noext'), '');
});

test('skipped files are named, and capped at three', () => {
  assert.equal(describeSkipped([]), '');
  assert.match(describeSkipped(['a.png']), /^Not attached .*: a\.png$/);
  assert.match(describeSkipped(['a', 'b', 'c', 'd', 'e']), /a, b, c and 2 more$/);
});
