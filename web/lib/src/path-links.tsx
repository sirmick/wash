// Links in an agent transcript: web addresses, and the files it names.
//
// An agent names files all the time — `apps/edit/be/app.go:42`, "see
// internal/fs/fs.go", a tool row's path — and the next thing anyone does is
// go and look. The same goes for the URL it says to open. This finds both in
// any text a transcript shows — prose, bold, a code span, a fenced block, a
// tool's output — with one scanner, so a link does not depend on how the
// agent happened to dress it. (It used to: URLs were found only in plain
// prose and paths in a code span only when the span was one word, which is
// how `**https://host:8090/**` and `make -C executor/server` stayed text.)
//
// A web address is a link on its shape, http(s) only. A path is a link only
// when the host says so: `probe` goes to a backend that resolves the tokens
// against the session's folder (internal/pathlink), and `open` shows the
// hit — in the Agent window's own editor, or in the editor the agent tab
// lives in. So a word that merely looks like a path ("e.g.", "1.2.3",
// "and/or") stays text.
//
// Answers are cached per token: a streaming message re-renders on every
// chunk, and asking again each time would be one backend round trip per
// chunk. A miss is remembered only until a tool that may write finishes
// (forget), because the file an agent is talking about is often one it is
// about to write.

import { For, Show, createContext, createEffect, createMemo, createSignal, onCleanup, untrack, useContext } from 'solid-js';
import type { Accessor, Component, JSX } from 'solid-js';
import { tokens } from './tokens';
import { highlightSpans, syntaxLang } from './syntax';
import type { SyntaxSpan } from './syntax';

/** A token the host confirmed names a file. */
export interface PathHit {
  token: string;
  path: string;
  line?: number;
  col?: number;
  /** A folder: it opens in the file manager, not the editor. */
  dir?: boolean;
}

/** What a transcript's host supplies to make file references links. */
export interface PathLinks {
  /** Which of these tokens are files. Unknown ones are simply absent. */
  probe: (tokens: string[]) => Promise<PathHit[]>;
  /** Show the file a link names. */
  open: (hit: PathHit) => void;
}

/** One link-shaped token and where it sits in the text it came from: a
 *  web address (`url`), or a path for the host to vouch for. */
export interface PathToken {
  start: number;
  end: number;
  token: string;
  url?: boolean;
}

// Per probe; the backend caps the same number (pathlink.MaxProbe).
const PROBE_BATCH = 64;

// Distinct paths one text asks about. A fenced log or a tool's output can
// hold thousands of path-shaped words; past this many nobody is clicking
// through them, and each one is a stat on the host.
const MAX_PATHS = 256;

// Runs of characters a path can be made of. Brackets, quotes and commas
// end one, so "(see a/b.go)," yields a/b.go.
const WORD_RE = /[^\s`'"<>()[\]{}|,;*]+/g;
const SCHEME_RE = /^[a-z][a-z0-9+.-]*:\/\//i;
// Where in the file: :line, :line:col, a :first-last range, or the
// #L42 / #L42-L60 anchor a forge link uses. internal/pathlink.Split reads
// the same shapes.
const POSITION_RE = /(?::\d+(?:-\d+|:\d+)?|#L\d+(?:C\d+)?(?:-L?\d+(?:C\d+)?)?)$/;
// A shell assignment or an option glued to its value: the path is what
// follows the `=` (XDG_RUNTIME_DIR=/run/user/1000, --system=~/a.img).
const ASSIGN_RE = /^-{0,2}[A-Za-z_][\w.-]*=/;
const URL_RE = /https?:\/\/[^\s<>"'`]+/g;

/** isWebURL says whether an href is one a click may open: http(s) only. */
export function isWebURL(href: string): boolean {
  return /^https?:\/\/\S+$/i.test(href.trim());
}

/**
 * trimURL takes the prose punctuation off the end of a URL found in text:
 * the full stop of a sentence, a comma, and a closing bracket the URL did
 * not open — "(see https://x.y/z)" names https://x.y/z. The `*` and `_`
 * are a bold or italic the URL sits in when the Markdown did not take it.
 */
export function trimURL(url: string): string {
  for (;;) {
    const last = url[url.length - 1];
    if ('.,;:!?*_'.includes(last)) { url = url.slice(0, -1); continue; }
    if (last === ')' && (url.match(/\(/g)?.length ?? 0) < (url.match(/\)/g)?.length ?? 0)) { url = url.slice(0, -1); continue; }
    if (last === ']' && (url.match(/\[/g)?.length ?? 0) < (url.match(/]/g)?.length ?? 0)) { url = url.slice(0, -1); continue; }
    return url;
  }
}

/** looksLikePath is the cheap shape test that decides what is worth a
 *  probe: something with a slash in it (a folder's trailing one too), or a
 *  file name with an extension, optionally followed by a position.
 *  `bare` also admits a single word with neither — right for an inline
 *  code span, which an agent uses for `Makefile` as readily as for
 *  `main.go`, and wrong for prose. */
export function looksLikePath(tok: string, bare = false): boolean {
  if (tok.length < 2 || tok.length > 512) return false;
  if (SCHEME_RE.test(tok) || tok.startsWith('www.')) return false;
  const path = tok.replace(POSITION_RE, '');
  if (!path || /\s/.test(path)) return false;
  if (path.includes('/')) return /\w/.test(path);
  if (!/^[\w.@+-]+$/.test(path) || !/[A-Za-z_]/.test(path)) return false;
  return bare || /\.[A-Za-z0-9]{1,12}$/.test(path);
}

/** urlTokens finds the web addresses in text. */
export function urlTokens(text: string): PathToken[] {
  const out: PathToken[] = [];
  for (const m of text.matchAll(URL_RE)) {
    const token = trimURL(m[0]);
    if (isWebURL(token)) out.push({ start: m.index!, end: m.index! + token.length, token, url: true });
  }
  return out;
}

/** pathTokens finds the path-shaped tokens in a run of prose. Sentence
 *  punctuation after one is not part of it: "edit a/b.go." links a/b.go. */
export function pathTokens(text: string): PathToken[] {
  const out: PathToken[] = [];
  for (const m of text.matchAll(WORD_RE)) {
    let start = m.index!;
    let token = m[0].replace(/[.:!?]+$/, '');
    const assign = ASSIGN_RE.exec(token);
    if (assign && token.length > assign[0].length) {
      start += assign[0].length;
      token = token.slice(assign[0].length);
    }
    if (looksLikePath(token)) out.push({ start, end: start + token.length, token });
  }
  return out;
}

/** codeToken reads an inline code span: the whole span when it is one
 *  path-shaped word (`Makefile` counts), else the paths among its words —
 *  `python3 .run/hunt.py` names .run/hunt.py, `go test ./...` nothing. */
export function codeToken(text: string): PathToken[] {
  const token = text.trim();
  if (ASSIGN_RE.test(token) || !looksLikePath(token, true)) return pathTokens(text);
  const start = text.indexOf(token);
  return [{ start, end: start + token.length, token }];
}

/** linkTokens is every link a piece of text could hold, in order: its web
 *  addresses, and the path-shaped tokens that are not part of one. `code`
 *  reads it as an inline code span (codeToken); `paths` false leaves the
 *  paths out, for text with no host to vouch for them. */
export function linkTokens(text: string, opts: { code?: boolean; paths?: boolean } = {}): PathToken[] {
  const urls = urlTokens(text);
  if (opts.paths === false) return urls;
  const seen = new Set<string>();
  const paths = (opts.code ? codeToken(text) : pathTokens(text)).filter((p) => {
    if (urls.some((u) => p.start < u.end && u.start < p.end)) return false;
    if (!seen.has(p.token)) {
      if (seen.size >= MAX_PATHS) return false;
      seen.add(p.token);
    }
    return true;
  });
  return [...urls, ...paths].sort((a, b) => a.start - b.start);
}

/** A PathLinks with the cache and batching every render needs. */
export interface PathResolver {
  resolve: (tokens: string[]) => Promise<Map<string, PathHit>>;
  open: (hit: PathHit) => void;
  /** Drops the remembered misses, so every render asks about them again:
   *  called when a tool that may have written a file finishes. */
  forget: () => void;
  /** Bumped by forget; a render that reads it re-resolves. */
  epoch: Accessor<number>;
}

/** pathResolver wraps a host's PathLinks: tokens asked for in the same
 *  tick go to the backend as one probe, and answers are reused. A hit is
 *  kept; a miss until the next forget. */
export function pathResolver(links: PathLinks): PathResolver {
  const known = new Map<string, PathHit | null>();
  const inflight = new Map<string, Promise<void>>();
  const [epoch, setEpoch] = createSignal(0);
  let batch: string[] = [];
  let flush: Promise<void> | null = null;

  const enqueue = (t: string): Promise<void> => {
    batch.push(t);
    if (!flush) {
      flush = Promise.resolve().then(async () => {
        const asked = batch;
        batch = [];
        flush = null;
        const chunks: string[][] = [];
        for (let i = 0; i < asked.length; i += PROBE_BATCH) chunks.push(asked.slice(i, i + PROBE_BATCH));
        const hits = (await Promise.all(chunks.map((c) => links.probe(c).catch(() => [] as PathHit[])))).flat();
        const byToken = new Map(hits.map((h) => [h.token, h]));
        for (const a of asked) {
          known.set(a, byToken.get(a) ?? null);
          inflight.delete(a);
        }
      });
    }
    inflight.set(t, flush);
    return flush;
  };

  return {
    async resolve(toks) {
      const want = [...new Set(toks)];
      await Promise.all(want.filter((t) => !known.has(t)).map((t) => inflight.get(t) ?? enqueue(t)));
      const out = new Map<string, PathHit>();
      for (const t of want) {
        const hit = known.get(t);
        if (hit) out.set(t, hit);
      }
      return out;
    },
    open: (hit) => links.open(hit),
    forget() {
      for (const [t, hit] of known) if (!hit) known.delete(t);
      setEpoch(epoch() + 1);
    },
    epoch,
  };
}

const PathLinksContext = createContext<PathResolver | undefined>();

/** Makes file references under it links. Without one, text stays text. */
export const PathLinksProvider: Component<{ resolver?: PathResolver; children: JSX.Element }> = (p) => (
  <PathLinksContext.Provider value={p.resolver}>{p.children}</PathLinksContext.Provider>
);

export const usePathLinks = () => useContext(PathLinksContext);

const sameHits = (a: Map<string, PathHit>, b: Map<string, PathHit>) =>
  a.size === b.size && [...a].every(([t, h]) => b.get(t) === h);

/** useProbe resolves the path tokens a render found (web addresses need no
 *  host and are skipped), dropping answers for a render that has since
 *  been replaced. `settled` is whether the host has answered for them. */
export function useProbe(found: () => PathToken[]): { hits: Accessor<Map<string, PathHit>>; settled: Accessor<boolean> } {
  const r = usePathLinks();
  const [hits, setHits] = createSignal<Map<string, PathHit>>(new Map());
  const [settled, setSettled] = createSignal(false);
  createEffect(() => {
    const toks = found().filter((t) => !t.url);
    r?.epoch();
    if (!r || toks.length === 0) return;
    let live = true;
    onCleanup(() => { live = false; });
    void r.resolve(toks.map((t) => t.token)).then((m) => {
      if (!live) return;
      // A forget re-asks everything a render shows; most answers come back
      // the same, and an unchanged map must not re-render the text.
      if (!sameHits(untrack(hits), m)) setHits(m);
      setSettled(true);
    });
  });
  return { hits, settled };
}

/** useHits is useProbe's answers alone. */
export const useHits = (found: () => PathToken[]) => useProbe(found).hits;

/** The title a link carries: where it goes, with the line when it has one. */
export const hitTitle = (h: PathHit) =>
  h.dir ? `Open ${h.path} in the file manager` : `Open ${h.path}${h.line ? `:${h.line}${h.col ? `:${h.col}` : ''}` : ''} in the editor`;

const linkStyle: JSX.CSSProperties = {
  color: tokens.accentBlue,
  cursor: 'pointer',
  'text-decoration': 'underline',
  'text-decoration-style': 'dotted',
  'text-underline-offset': '2px',
};

/** PathLink is one clickable file reference. */
export const PathLink: Component<{ hit: PathHit; children: JSX.Element }> = (p) => {
  const r = usePathLinks();
  const open = (ev: Event) => {
    ev.preventDefault();
    ev.stopPropagation();
    r?.open(p.hit);
  };
  return (
    <span
      role="link"
      tabindex={0}
      data-wash-hit="subtle"
      data-testid="agent-path-link"
      data-path={p.hit.path}
      title={hitTitle(p.hit)}
      style={linkStyle}
      onClick={open}
      onKeyDown={(ev) => { if (ev.key === 'Enter') open(ev); }}
    >
      {p.children}
    </span>
  );
};

/** A web link: a real anchor, opened in a new tab with no opener and no
 *  referrer. Only http(s) gets here (isWebURL), so a javascript: or data:
 *  href from model output is never something a click runs. The target is
 *  on the title, so a label that says one thing and points at another can
 *  be checked before the click. */
export const WebLink: Component<{ href: string; children?: JSX.Element }> = (p) => (
  <a data-wash-hit href={p.href} title={p.href} target="_blank" rel="noopener noreferrer"
    // A click must not also reach a row that folds out on click.
    onClick={(ev) => ev.stopPropagation()}
    style={{ color: tokens.accentBlue, 'overflow-wrap': 'anywhere' }}>
    {p.children ?? p.href}
  </a>
);

/** A live link: a web address, or a path with the hit the host gave it. */
interface Live extends PathToken {
  hit?: PathHit;
}

/** useLinks finds the links in text and keeps the live ones, in order:
 *  every web address, and the paths the host vouched for. */
function useLinks(text: () => string, code?: boolean): Accessor<Live[]> {
  const linked = !!usePathLinks();
  const found = createMemo(() => linkTokens(text(), { code, paths: linked }));
  const hits = useHits(found);
  return createMemo(() => {
    const out: Live[] = [];
    for (const t of found()) {
      if (t.url) out.push(t);
      else if (hits().has(t.token)) out.push({ ...t, hit: hits().get(t.token) });
    }
    return out;
  });
}

const LiveLink: Component<{ link: Live; children: JSX.Element }> = (p) => (
  <Show when={p.link.hit} fallback={<WebLink href={p.link.token}>{p.children}</WebLink>}>
    <PathLink hit={p.link.hit!}>{p.children}</PathLink>
  </Show>
);

/** LinkText renders text with the links in it — web addresses, and the
 *  files the host can open. `code` reads it as an inline code span. */
export const LinkText: Component<{ text: string; code?: boolean }> = (p) => {
  const live = useLinks(() => p.text, p.code);
  const segments = createMemo(() => {
    const out: { text: string; link?: Live }[] = [];
    let at = 0;
    for (const t of live()) {
      if (t.start > at) out.push({ text: p.text.slice(at, t.start) });
      out.push({ text: p.text.slice(t.start, t.end), link: t });
      at = t.end;
    }
    if (at < p.text.length) out.push({ text: p.text.slice(at) });
    return out;
  });
  return (
    <For each={segments()}>
      {(s) => (
        <Show when={s.link} fallback={<>{s.text}</>}>
          <LiveLink link={s.link!}>{s.text}</LiveLink>
        </Show>
      )}
    </For>
  );
};

/** A run of highlighted code, and the link it belongs to if any. */
export interface CodeRun {
  link?: Live;
  spans: SyntaxSpan[];
}

/** codeRuns cuts highlighted spans at the links' edges and groups them: a
 *  link keeps its syntax colours inside it, and the runs still concatenate
 *  back to exactly the code. */
export function codeRuns(spans: SyntaxSpan[], links: Live[]): CodeRun[] {
  const out: CodeRun[] = [];
  const push = (text: string, role: SyntaxSpan['role'], link?: Live) => {
    if (!text) return;
    const last = out[out.length - 1];
    const span = role ? { text, role } : { text };
    if (last && last.link === link) last.spans.push(span);
    else out.push({ link, spans: [span] });
  };
  let at = 0;
  let li = 0;
  for (const s of spans) {
    const end = at + s.text.length;
    let pos = at;
    while (pos < end) {
      while (li < links.length && links[li].end <= pos) li++;
      const l = links[li];
      if (!l || l.start >= end) { push(s.text.slice(pos - at), s.role); pos = end; break; }
      if (l.start > pos) { push(s.text.slice(pos - at, l.start - at), s.role); pos = l.start; continue; }
      const stop = Math.min(end, l.end);
      push(s.text.slice(pos - at, stop - at), s.role, l);
      pos = stop;
    }
    at = end;
  }
  return out;
}

/** LinkedCode is a fenced block's code, highlighted, with its links live:
 *  a URL to fetch or a file named in a command is as clickable there as in
 *  prose. `color` maps a span's role to its colour. */
export const LinkedCode: Component<{ code: string; lang?: string; color: (role: NonNullable<SyntaxSpan['role']>) => string }> = (p) => {
  const live = useLinks(() => p.code);
  const runs = createMemo(() => codeRuns(highlightSpans(p.code, syntaxLang(p.lang ?? '')), live()));
  const Spans: Component<{ spans: SyntaxSpan[] }> = (q) => (
    <For each={q.spans}>
      {(s) => (
        <Show when={s.role} fallback={<>{s.text}</>}>
          <span style={{ color: p.color(s.role!) }}>{s.text}</span>
        </Show>
      )}
    </For>
  );
  return (
    <For each={runs()}>
      {(r) => (
        <Show when={r.link} fallback={<Spans spans={r.spans} />}>
          <LiveLink link={r.link!}><Spans spans={r.spans} /></LiveLink>
        </Show>
      )}
    </For>
  );
};
