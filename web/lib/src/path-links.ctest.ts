import { test, expect } from 'vitest';
import { codeToken, looksLikePath, pathResolver, pathTokens } from './path-links';

test('looksLikePath takes paths and file names, not prose or URLs', () => {
  for (const yes of ['a/b.go', './run.sh', '/etc/hosts', 'main.go', 'main.go:42', 'x/y.ts:3:9', '.env.local', '~/notes.md']) {
    expect(looksLikePath(yes), yes).toBe(true);
  }
  for (const no of ['hello', '1.2.3', 'http://x.io/a.go', 'www.example.com', 'a/', '42', 'Makefile']) {
    expect(looksLikePath(no), no).toBe(false);
  }
  // A code span may be a bare file name.
  expect(looksLikePath('Makefile', true)).toBe(true);
});

test('pathTokens drops the punctuation around a path', () => {
  const text = 'Edit (a/b.go), then c.ts:12. Done.';
  expect(pathTokens(text).map((t) => [t.token, text.slice(t.start, t.end)])).toEqual([
    ['a/b.go', 'a/b.go'],
    ['c.ts:12', 'c.ts:12'],
  ]);
});

test('codeToken is one word or nothing', () => {
  expect(codeToken(' main.go ').map((t) => t.token)).toEqual(['main.go']);
  expect(codeToken('go test ./...')).toEqual([]);
});

test('pathResolver batches a tick into one probe and reuses the answers', async () => {
  const probes: string[][] = [];
  const r = pathResolver({
    probe: async (toks) => {
      probes.push(toks);
      return toks.filter((t) => t !== 'no.go').map((t) => ({ token: t, path: '/w/' + t }));
    },
    open: () => {},
  });
  const [a, b] = await Promise.all([r.resolve(['a.go', 'no.go']), r.resolve(['b.go', 'a.go'])]);
  expect(probes).toEqual([['a.go', 'no.go', 'b.go']]);
  expect([...a.keys()]).toEqual(['a.go']);
  expect([...b.keys()].sort()).toEqual(['a.go', 'b.go']);
  await r.resolve(['a.go', 'no.go']);
  expect(probes.length).toBe(1);
});
