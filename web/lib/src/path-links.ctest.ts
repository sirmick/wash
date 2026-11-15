import { test, expect } from 'vitest';
import { codeRuns, codeToken, linkTokens, looksLikePath, pathResolver, pathTokens } from './path-links';

test('looksLikePath takes paths and file names, not prose or URLs', () => {
  for (const yes of ['a/b.go', './run.sh', '/etc/hosts', 'main.go', 'main.go:42', 'x/y.ts:3:9', '.env.local', '~/notes.md',
    'executor/', 'a.c:146-148', 'a/b.go#L42', 'a/b.go#L42-L60']) {
    expect(looksLikePath(yes), yes).toBe(true);
  }
  for (const no of ['hello', '1.2.3', 'http://x.io/a.go', 'www.example.com', '/', './', '42', 'Makefile']) {
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

// Shapes from real transcripts (an audit of 120 of them, 2026-11-14) that
// stayed text: about a third of the files an agent named, and the URLs it
// most wanted clicked.
test('the shapes agents write are found', () => {
  const toks = (text: string, code = false) => linkTokens(text, { code }).map((t) => t.token);
  // A folder, written with its slash.
  expect(toks('executor/', true)).toEqual(['executor/']);
  expect(toks('the `docs/executor/` tree moves')).toEqual(['docs/executor/']);
  // Paths among a command's words.
  expect(toks('python3 .run/fax-hunt.py', true)).toEqual(['.run/fax-hunt.py']);
  expect(toks('make -C executor/server test', true)).toEqual(['executor/server']);
  expect(toks('tools/rtlswitch.py FW --topo topo.yaml', true)).toEqual(['tools/rtlswitch.py', 'topo.yaml']);
  // An assignment's value.
  expect(toks('XDG_RUNTIME_DIR=/run/user/1000', true)).toEqual(['/run/user/1000']);
  expect(toks('--system=~/storage/boot.img')).toEqual(['~/storage/boot.img']);
  // Ranges and anchors.
  expect(toks('rtlplayground.c:146-148', true)).toEqual(['rtlplayground.c:146-148']);
  expect(toks('see web/x.tsx#L42-L60.')).toEqual(['web/x.tsx#L42-L60']);
  // URLs in a code span, and in a bold the Markdown left alone.
  expect(toks('http://172.16.16.74:8095/executor-windows-1d2fae3c.zip', true)).toEqual(['http://172.16.16.74:8095/executor-windows-1d2fae3c.zip']);
  expect(toks('Open https://172.16.16.74:12000/.')).toEqual(['https://172.16.16.74:12000/']);
  // A URL is one link, never also the paths inside it.
  expect(linkTokens('curl http://192.168.2.2/login.html').map((t) => [t.token, !!t.url])).toEqual([['http://192.168.2.2/login.html', true]]);
  // No host to vouch for paths: only the URLs.
  expect(linkTokens('a/b.go https://x.io', { paths: false }).map((t) => t.token)).toEqual(['https://x.io']);
});

test('codeRuns cuts highlighting at a link and keeps every character', () => {
  const code = 'curl http://h/x.zip -o out';
  const link = { start: 5, end: 19, token: 'http://h/x.zip', url: true };
  const runs = codeRuns([{ text: 'curl ' }, { text: 'http://h', role: 'string' }, { text: '/x.zip -o out' }], [link]);
  expect(runs.map((r) => [r.spans.map((s) => s.text).join(''), !!r.link])).toEqual([
    ['curl ', false], ['http://h/x.zip', true], [' -o out', false],
  ]);
  expect(runs.flatMap((r) => r.spans.map((s) => s.text)).join('')).toBe(code);
  expect(runs[1].spans[0].role).toBe('string');
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

test('pathResolver asks about a miss again after forget, never a hit', async () => {
  const probes: string[][] = [];
  let made = false;
  const r = pathResolver({
    probe: async (toks) => {
      probes.push(toks);
      return toks.filter((t) => t === 'a.go' || (made && t === 'new.go')).map((t) => ({ token: t, path: '/w/' + t }));
    },
    open: () => {},
  });
  expect([...(await r.resolve(['a.go', 'new.go'])).keys()]).toEqual(['a.go']);
  made = true;
  expect([...(await r.resolve(['a.go', 'new.go'])).keys()]).toEqual(['a.go']);
  const before = r.epoch();
  r.forget();
  expect(r.epoch()).toBe(before + 1);
  expect([...(await r.resolve(['a.go', 'new.go'])).keys()].sort()).toEqual(['a.go', 'new.go']);
  expect(probes).toEqual([['a.go', 'new.go'], ['new.go']]);
});
