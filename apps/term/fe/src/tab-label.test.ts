// Tab-label derivation (tab-label.ts): the strip must show what differs
// between tabs, not the "user@host: " every one of them shares.

import { test } from 'node:test';
import assert from 'node:assert/strict';

import { TAB_LABEL_MAX, fullTabLabel, tabLabelFor } from './tab-label.ts';

test('no title: the shell basename', () => {
  assert.equal(tabLabelFor(undefined, '/bin/bash'), 'bash');
  assert.equal(tabLabelFor('   ', '/usr/bin/zsh'), 'zsh');
  assert.equal(fullTabLabel('', '/bin/sh'), 'sh');
});

test('the user@host prefix is stripped, with or without a space', () => {
  assert.equal(tabLabelFor('mick@ai: ~/wash', '/bin/bash'), '~/wash');
  assert.equal(tabLabelFor('mick@ai:~/wash', '/bin/bash'), '~/wash');
  assert.equal(tabLabelFor('root@ai: /etc', '/bin/bash'), '/etc');
  // Home alone is still a label.
  assert.equal(tabLabelFor('mick@ai: ~', '/bin/bash'), '~');
});

test('a bare user@host (nothing after it) is left as it is', () => {
  // An ssh session titled by the remote prompt with no path.
  assert.equal(tabLabelFor('mick@remote', '/bin/bash'), 'mick@remote');
  assert.equal(tabLabelFor('mick@remote: ', '/bin/bash'), 'mick@remote:');
});

test('the old degenerate case: the path survives, not the prefix', () => {
  // This used to be "mick@ai: ~/…" for every tab in the window.
  assert.equal(tabLabelFor('mick@ai: ~/wash/branches/p1-term', '/bin/bash'), '~/wash/branches/p1-term');
});

test('a path that does not fit keeps its last segments behind …/', () => {
  const got = tabLabelFor('mick@ai: ~/wash/branches/p1-term/apps/term/fe/src', '/bin/bash');
  assert.equal(got, '…/apps/term/fe/src');
  assert.ok(got.length <= TAB_LABEL_MAX);
  // Two tabs deep in different trees stay tellable apart.
  const a = tabLabelFor('mick@ai: /home/mick/src/project-one/internal/router', '/bin/bash');
  const b = tabLabelFor('mick@ai: /home/mick/src/project-two/internal/router', '/bin/bash');
  // "…/project-one/internal/router" would be 29 chars, so the project
  // segment does not make it and the two read alike — the tooltip
  // carries the rest. What matters is that neither says "mick@ai: /h…".
  assert.equal(a, '…/internal/router');
  assert.equal(b, '…/internal/router');
});

test('a single over-long segment is cut from its front', () => {
  const got = tabLabelFor('root@ai: /etc/systemd/system/multi-user.target.wants', '/bin/bash');
  assert.equal(got.length, TAB_LABEL_MAX);
  assert.ok(got.startsWith('…'));
  assert.ok(got.endsWith('target.wants'));
});

test('exactly the limit is not truncated', () => {
  const s = 'x'.repeat(TAB_LABEL_MAX);
  assert.equal(tabLabelFor(s, '/bin/bash'), s);
});

test('a title with no path is cut from the end (the program name leads)', () => {
  const got = tabLabelFor('vim some-extremely-long-file-name.tsx', '/bin/bash');
  assert.equal(got, 'vim some-extremely-long…');
  assert.equal(got.length, TAB_LABEL_MAX);
});

test('a trailing slash does not produce an empty last segment', () => {
  assert.equal(tabLabelFor('mick@ai: ~/wash/branches/p1-term/apps/term/fe/', '/bin/bash'), '…/p1-term/apps/term/fe');
});

test('max is a parameter', () => {
  assert.equal(tabLabelFor('mick@ai: ~/a/b/c/d', '/bin/bash', 6), '…/c/d');
});
