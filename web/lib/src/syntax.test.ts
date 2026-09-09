// Tests for the transcript's highlighter (syntax.ts).
//
// The invariant that matters most is not which token is which colour: it
// is that the runs concatenate back to exactly the input. A highlighter
// that drops or duplicates a character silently corrupts what the copy
// button then hands you.

import test from 'node:test';
import assert from 'node:assert/strict';

import { diffFiles, diffSpans, highlightSpans, syntaxLang, MAX_HIGHLIGHT_BYTES } from './syntax.ts';

const joined = (code: string, lang: string) =>
  highlightSpans(code, lang)
    .map((s) => s.text)
    .join('');

test('fence info strings normalise to a grammar, or to nothing', () => {
  assert.equal(syntaxLang('TypeScript'), 'typescript');
  assert.equal(syntaxLang('  Go  '), 'go');
  assert.equal(syntaxLang('c++'), 'c++');
  assert.equal(syntaxLang('js {highlight=1}'), 'js');
  assert.equal(syntaxLang('diff'), 'diff');
  assert.equal(syntaxLang('patch'), 'diff');
  // No grammar: render plain rather than guess.
  assert.equal(syntaxLang('shell-session'), '');
  assert.equal(syntaxLang(''), '');
  assert.equal(syntaxLang('text'), '');
});

test('spans concatenate back to the input, whatever the language', () => {
  const cases: [string, string][] = [
    ['const x = 1; // hi\nfunction f() { return "s"; }\n', 'ts'],
    ['package main\n\nimport "fmt"\n\nfunc main() { fmt.Println(1) }\n', 'go'],
    ['def f(a):\n    return a + 1  # comment\n', 'python'],
    ['{"a": [1, 2, null], "b": "x"}\n', 'json'],
    ['a:\n  - b\n', 'yaml'],
    ['#!/bin/sh\necho hi\n', ''],
  ];
  for (const [code, lang] of cases) {
    assert.equal(joined(code, syntaxLang(lang) || lang), code, lang || 'plain');
  }
});

test('a known language actually produces roles, an unknown one does not', () => {
  const ts = highlightSpans('const x = "s"; // c\n', 'ts');
  const roles = new Set(ts.map((s) => s.role).filter(Boolean));
  assert.ok(roles.has('keyword'), 'const should be a keyword');
  assert.ok(roles.has('string'), '"s" should be a string');
  assert.ok(roles.has('comment'), '// c should be a comment');

  const plain = highlightSpans('const x = "s";\n', '');
  assert.equal(plain.length, 1);
  assert.equal(plain[0].role, undefined);
});

test('a block past the cap renders plain rather than parsing', () => {
  const huge = 'const a = 1;\n'.repeat(Math.ceil(MAX_HIGHLIGHT_BYTES / 12) + 1);
  assert.ok(huge.length > MAX_HIGHLIGHT_BYTES);
  const spans = highlightSpans(huge, 'ts');
  assert.equal(spans.length, 1);
  assert.equal(spans[0].role, undefined);
});

test('a unified diff colours by line and keeps every byte', () => {
  const diff = ['--- a/x.go', '+++ b/x.go', '@@ -1,2 +1,2 @@', ' keep', '-old', '+new', ''].join('\n');
  const spans = diffSpans(diff);
  assert.equal(spans.map((s) => s.text).join(''), diff);
  const byRole = (r: string) => spans.filter((s) => s.role === r).map((s) => s.text.trim());
  assert.deepEqual(byRole('add'), ['+new']);
  assert.deepEqual(byRole('del'), ['-old']);
  assert.deepEqual(byRole('hunk'), ['@@ -1,2 +1,2 @@']);
  assert.deepEqual(byRole('meta'), ['--- a/x.go', '+++ b/x.go']);
  // A context line is not coloured — otherwise the whole diff is.
  assert.equal(spans.find((s) => s.text.startsWith(' keep'))?.role, undefined);
});

test('highlightSpans routes diff through diffSpans', () => {
  const spans = highlightSpans('-a\n+b\n', 'diff');
  assert.deepEqual(spans.map((s) => s.role), ['del', 'add']);
});

test('diffFiles names what a diff touches, without the a/ b/ prefixes', () => {
  assert.deepEqual(diffFiles('--- a/w/x.go\n+++ b/w/x.go\n@@ -1 +1 @@\n-a\n+b\n'), ['/w/x.go']);
  // A created file: /dev/null is not a file anyone can open.
  assert.deepEqual(diffFiles('--- /dev/null\n+++ b/w/new.go\n'), ['/w/new.go']);
});
