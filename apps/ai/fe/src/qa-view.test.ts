import { test } from 'node:test';
import assert from 'node:assert/strict';
import { splitQAMarkdown } from './qa-view.ts';

test('threads are cut at their headings, the preamble kept', () => {
  const md = [
    '# Workspace QA', '', 'Questions, decisions and review evidence.',
    '', '## K5 · q1 — Wakeup bound', '', 'Status: **open** · Assigned to: Lead · Revision: 1', '', '### Lead · Question', '', '> How long?',
    '', '## INIT1 · GATE1-notice-two-leases — The deadline notice misses', '', 'Status: **resolved** · Assigned to: Architect · Revision: 4',
    '', 'View truncated; read complete thread events with workspace_get view=qa and thread_id.',
  ].join('\n');
  const s = splitQAMarkdown(md);
  assert.equal(s.preamble, '# Workspace QA\n\nQuestions, decisions and review evidence.');
  assert.equal(s.threads.length, 2);
  assert.deepEqual(s.threads.map((t) => [t.id, t.node, t.title]), [['q1', 'K5', 'Wakeup bound'], ['GATE1-notice-two-leases', 'INIT1', 'The deadline notice misses']]);
  assert.match(s.threads[0].body, /^Status: \*\*open\*\*/);
  assert.match(s.threads[0].body, /> How long\?$/);
  assert.match(s.threads[1].body, /View truncated/, 'a trailing note stays with the last thread');
});

test('a document with no threads is all preamble; an unfamiliar heading still becomes a card', () => {
  assert.deepEqual(splitQAMarkdown('# Workspace QA\n\nNo questions yet.'), { preamble: '# Workspace QA\n\nNo questions yet.', threads: [] });
  const s = splitQAMarkdown('# QA\n\n## Something else\n\ntext');
  assert.deepEqual(s.threads, [{ id: '', node: '', title: 'Something else', body: 'text' }]);
});
