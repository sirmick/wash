// The specification of smart paste (docs/AGENT_TERM.md §10). Wrap
// detection is heuristics all the way down, so these tables ARE the
// contract: the "leave it alone" cases matter more than the "fix it" ones,
// because a filter that mangles a real script is worse than no filter.

import test from 'node:test';
import assert from 'node:assert/strict';
import { analyzePaste, joinWrapped } from './paste-analyze.ts';

// ---- nothing to do ----

test('clean text is passed through untouched', () => {
  for (const s of [
    'ls -la',
    'git commit -m "fix the thing"',
    "echo 'single quotes'",
    'make test\nmake e2e-test',
    '',
    '   ',
  ]) {
    const a = analyzePaste(s);
    assert.equal(a.verdict, 'as-is', `verdict for ${JSON.stringify(s)}`);
    assert.equal(a.cleaned, s);
    assert.deepEqual(a.issues, []);
  }
});

// ---- invisible junk: cleaned, and cleaned silently when it's one line ----

test('a non-breaking space is fixed silently — the classic "command not found"', () => {
  const a = analyzePaste('git status');
  assert.equal(a.verdict, 'clean');
  assert.equal(a.cleaned, 'git status');
  assert.equal(a.issues[0].kind, 'nbsp');
});

test('zero-width characters are removed', () => {
  const a = analyzePaste('npm​ run‍ build');
  assert.equal(a.verdict, 'clean');
  assert.equal(a.cleaned, 'npm run build');
  assert.equal(a.issues[0].kind, 'zero-width');
});

test('curly quotes become straight ones', () => {
  const a = analyzePaste('git commit -m “fix the thing”');
  assert.equal(a.verdict, 'clean');
  assert.equal(a.cleaned, 'git commit -m "fix the thing"');
  const a2 = analyzePaste('echo ‘hi’');
  assert.equal(a2.cleaned, "echo 'hi'");
});

test('a copied shell prompt is stripped', () => {
  assert.equal(analyzePaste('$ ls -la').cleaned, 'ls -la');
  assert.equal(analyzePaste('% ls -la').cleaned, 'ls -la');
  // …but only when the whole block has one, so real syntax survives.
  const mixed = analyzePaste('echo $ dollars\n$ ls');
  assert.ok(!mixed.issues.some((i) => i.kind === 'prompt-marker'), 'stripped a non-prompt $');
  const all = analyzePaste('$ cd /tmp\n$ ls');
  assert.equal(all.cleaned, 'cd /tmp\nls');
  assert.equal(all.verdict, 'ask', 'multi-line changes are shown, not applied');
});

test('a "#" is never treated as a prompt — it is a comment', () => {
  const a = analyzePaste('# not a prompt');
  assert.equal(a.verdict, 'as-is');
  assert.equal(a.cleaned, '# not a prompt');
});

test('code fences are dropped', () => {
  const a = analyzePaste('```bash\nmake test\n```');
  assert.equal(a.cleaned, 'make test');
  assert.ok(a.issues.some((i) => i.kind === 'code-fence'));
});

test('CRLF is normalized', () => {
  const a = analyzePaste('one\r\ntwo');
  assert.equal(a.cleaned, 'one\ntwo');
  assert.ok(a.issues.some((i) => i.kind === 'crlf'));
});

// ---- wrap artifacts: the structural case, always offered not applied ----

test('a soft-wrapped command is rejoined and offered, never applied silently', () => {
  const wrapped =
    'docker run --rm -it --name my-container --volume /home/mick/data:/data\n' +
    '--env MODE=production --network host ghcr.io/example/image:latest';
  const a = analyzePaste(wrapped);
  assert.equal(a.verdict, 'ask');
  assert.equal(a.wrapped, true);
  assert.equal(
    a.cleaned,
    'docker run --rm -it --name my-container --volume /home/mick/data:/data ' +
      '--env MODE=production --network host ghcr.io/example/image:latest',
  );
});

test('a mid-token break rejoins with no space', () => {
  // The first line stops exactly at the wrap column, mid-path.
  const col = 'https://example.com/some/very/long/path/that/wraps/in/the/mid';
  const rest = 'dle-of-a-token.tar.gz';
  const a = analyzePaste(`curl -fsSL ${col}\n${rest}`);
  assert.equal(a.wrapped, true, 'not detected as wrapped');
  assert.ok(a.cleaned.includes('middle-of-a-token.tar.gz'), a.cleaned);
});

test('three wrapped lines all rejoin', () => {
  const a = analyzePaste(
    'ffmpeg -i input.mkv -c:v libx264 -preset slow -crf 18 -c:a aac\n' +
      '-b:a 192k -movflags +faststart -vf scale=1920:-2 -metadata\n' +
      'title=Example output.mp4',
  );
  assert.equal(a.wrapped, true);
  assert.equal(a.cleaned.split('\n').length, 1);
});

// ---- the cases that must NEVER be joined ----

test('genuine multi-line scripts keep their structure', () => {
  const scripts = [
    // Shell structure at line starts.
    'if [ -f /etc/passwd ]; then\n  echo yes\nfi',
    'for f in *.go; do\n  gofmt -w "$f"\ndone',
    // Explicit continuations already work when pasted.
    'docker run --rm -it \\\n  --name my-container-with-a-long-name \\\n  image:latest',
    // Operators at line ends are real syntax.
    'make build-the-whole-project-here &&\nmake test-everything-now',
    'cat /var/log/syslog |\ngrep -i error',
    // A comment line.
    'make test\n# then the slow one\nmake e2e-test',
    // Paragraph break: not one command.
    'first-command --with-a-reasonably-long-line-here\n\nsecond-command --also-long-enough',
    // Short lines: "same length" is a coincidence.
    'ls\ncd /tmp\npwd',
    // A real two-command sequence where the lines happen to differ a lot.
    'cd /home/mick/wash/branches/agent-term-m5/e2e/tests/fixtures\nls',
  ];
  for (const s of scripts) {
    const a = analyzePaste(s);
    assert.equal(a.wrapped, false, `wrongly joined:\n${s}\n→ ${a.cleaned}`);
    assert.equal(a.cleaned.split('\n').length, s.replace(/\\\n/g, '\\\n').split('\n').length,
      `line count changed for:\n${s}`);
  }
});

test('joinWrapped is the whole structural decision, and says no by default', () => {
  assert.equal(joinWrapped([]), null);
  assert.equal(joinWrapped(['one line']), null);
  assert.equal(joinWrapped(['short', 'short']), null);
  // Clustered but under the minimum believable wrap column.
  assert.equal(joinWrapped(['aaaaaaaaaa bbbb', 'cccccccccc dddd']), null);
  // Last line longer than the column can't be a wrap.
  assert.equal(
    joinWrapped([
      'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
      'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    ]),
    null,
  );
});

// ---- combinations, which is what a real chat paste looks like ----

test('a fenced, prompted, curly-quoted block is cleaned and shown', () => {
  const a = analyzePaste('```sh\n$ git commit -m “wip”\n$ git push\n```');
  assert.equal(a.verdict, 'ask');
  assert.equal(a.cleaned, 'git commit -m "wip"\ngit push');
  const kinds = a.issues.map((i) => i.kind).sort();
  assert.deepEqual(kinds, ['code-fence', 'curly-quotes', 'nbsp', 'prompt-marker'].sort());
});

test('junk inside a wrapped command is fixed and the command rejoined', () => {
  const a = analyzePaste(
    'kubectl apply --filename /home/mick/deploy/production/manifest .yaml\n' +
      '--namespace production --context prod-cluster-east --wait',
  );
  assert.equal(a.wrapped, true);
  assert.ok(!a.cleaned.includes(' '), 'nbsp survived');
  assert.equal(a.cleaned.split('\n').length, 1);
});

// ---- the paste-jacking warning ----

test('a multi-line paste into a shell without bracketed paste is called out', () => {
  const a = analyzePaste('make build\nmake test', { bracketedPaste: false });
  assert.equal(a.verdict, 'ask');
  assert.ok(a.issues.some((i) => i.kind === 'executes-immediately'));
  // With bracketed paste on, the same text is unremarkable.
  const b = analyzePaste('make build\nmake test', { bracketedPaste: true });
  assert.equal(b.verdict, 'as-is');
  // Unknown mode: no warning invented.
  const c = analyzePaste('make build\nmake test');
  assert.equal(c.verdict, 'as-is');
});

test('a single-line paste is never a paste-jacking warning', () => {
  const a = analyzePaste('ls -la', { bracketedPaste: false });
  assert.equal(a.verdict, 'as-is');
});

// ---- invariants ----

test('cleaned never changes structure unless the verdict asks', () => {
  const samples = [
    'ls -la',
    'git status',
    '$ ls',
    '```\nls\n```',
    'one\r\ntwo',
    'if x; then\n echo\nfi',
    'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bbbb\ncccc dddd',
    'make build\nmake test',
  ];
  for (const s of samples) {
    const a = analyzePaste(s, { bracketedPaste: true });
    if (a.verdict !== 'ask') {
      const before = a.original.replace(/\r\n/g, '\n').split('\n').filter((l) => l.trim() !== '').length;
      const after = a.cleaned.split('\n').filter((l) => l.trim() !== '').length;
      assert.equal(after, before, `structure changed without asking:\n${s}\n→ ${a.cleaned}`);
    }
  }
});

test('analysis is idempotent — cleaning cleaned text finds nothing', () => {
  const samples = [
    'git status',
    '$ ls -la',
    '```sh\nmake test\n```',
    'docker run --rm -it --name my-container --volume /data:/data\n--env MODE=production image:latest',
  ];
  for (const s of samples) {
    const once = analyzePaste(s).cleaned;
    const twice = analyzePaste(once);
    assert.equal(twice.cleaned, once, `not idempotent for ${JSON.stringify(s)}`);
    assert.equal(twice.verdict, 'as-is', `second pass still wants changes for ${JSON.stringify(s)}`);
  }
});

test('the original is always preserved for "paste as-is"', () => {
  const s = '$ git status';
  const a = analyzePaste(s);
  assert.equal(a.original, s);
  assert.notEqual(a.cleaned, s);
});

test('pathological input neither throws nor grows', () => {
  const big = 'x'.repeat(200_000);
  const a = analyzePaste(big + '\n' + big);
  assert.ok(a.cleaned.length <= (big.length + 1) * 2);
  assert.doesNotThrow(() => analyzePaste(' [31m\n ​'));
  assert.doesNotThrow(() => analyzePaste('\n'.repeat(10_000)));
});
