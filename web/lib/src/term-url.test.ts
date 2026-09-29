import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { TERM_URL_RE } from './term-url.ts';

function found(line: string): string | null {
  const m = line.match(TERM_URL_RE);
  return m ? m[0] : null;
}

// Each of these is a whole URL: clicking it must open exactly this, not a
// prefix of it. The characters in the second group are the ones the
// addon's own pattern stopped at, which is the bug this file exists for.
const whole = [
  'http://x.io/search?q=wash&page=2',
  'http://x.io/a?b=1#frag',
  'http://x.io/p?q=a%20b',
  'http://x.io/p?a=1&b=2,3',
  'http://x.io/p?ids[]=1&ids[]=2',
  'https://x.io/a_b-c~d+e',
  // Previously truncated:
  'http://x.io/p?q=(paren)',
  'http://x.io/p?q=a!b',
  'http://x.io/p?q=a*b',
  "http://x.io/p?q=it's",
  'http://maps.x.io/@1,2,3z/data=!3m1!4b1',
  'https://en.wikipedia.org/wiki/Foo_(bar)',
];

for (const url of whole) {
  test(`keeps the whole URL: ${url}`, () => {
    assert.equal(found(url), url);
  });
}

// The other half: a URL sitting in prose or inside brackets must not eat
// the punctuation around it. Trimming too little is as wrong as trimming
// too much — it opens a 404 instead of the page.
const inContext: Array<[string, string]> = [
  ['see http://x.io/a. Next', 'http://x.io/a'],
  ['(http://x.io/a)', 'http://x.io/a'],
  ['"http://x.io/a"', 'http://x.io/a'],
  ["'http://x.io/a'", 'http://x.io/a'],
  ['<http://x.io/a>', 'http://x.io/a'],
  ['[http://x.io/a]', 'http://x.io/a'],
  ['http://x.io/a, then', 'http://x.io/a'],
  ['http://x.io/a!', 'http://x.io/a'],
  ['ask http://x.io/a?', 'http://x.io/a'],
];

for (const [line, want] of inContext) {
  test(`stops at the prose boundary: ${JSON.stringify(line)}`, () => {
    assert.equal(found(line), want);
  });
}

test('a balanced group is kept but an unbalanced one ends the match', () => {
  assert.equal(found('http://x.io/a(b)c'), 'http://x.io/a(b)c');
  assert.equal(found('http://x.io/a(b'), 'http://x.io/a');
});

test('non-URL text has no link', () => {
  assert.equal(found('just some output'), null);
  assert.equal(found('ftp://x.io/a'), null);
});

test('carries no g flag — the addon appends its own and a doubled flag throws', () => {
  assert.equal(TERM_URL_RE.flags.includes('g'), false);
  assert.doesNotThrow(() => new RegExp(TERM_URL_RE.source, TERM_URL_RE.flags + 'g'));
});
