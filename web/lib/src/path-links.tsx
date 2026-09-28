// File references in an agent transcript, as links.
//
// An agent names files all the time — `apps/edit/be/app.go:42`, "see
// internal/fs/fs.go", a tool row's path — and the next thing anyone does is
// go and look. This finds the path-shaped tokens in rendered text and asks
// the host which of them are files; only those become links, so a word that
// merely looks like a path ("e.g.", "1.2.3", a URL) stays text.
//
// The host owns both questions. `probe` goes to a backend that resolves the
// tokens against the session's folder (internal/pathlink: at or below it,
// regular files only), and `open` shows the hit — in the Agent window's own
// editor, or in the editor the agent tab lives in.
//
// Answers are cached per token: a streaming message re-renders on every
// chunk, and asking again each time would be one backend round trip per
// chunk. A miss is remembered only briefly, because the file an agent is
// talking about is often one it is about to write.

import { For, Show, createContext, createEffect, createMemo, createSignal, onCleanup, useContext } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';

/** A token the host confirmed names a file. */
export interface PathHit {
  token: string;
  path: string;
  line?: number;
  col?: number;
}

/** What a transcript's host supplies to make file references links. */
export interface PathLinks {
  /** Which of these tokens are files. Unknown ones are simply absent. */
  probe: (tokens: string[]) => Promise<PathHit[]>;
  /** Show the file a link names. */
  open: (hit: PathHit) => void;
}

/** One path-shaped token and where it sits in the text it came from. */
export interface PathToken {
  start: number;
  end: number;
  token: string;
}

// A miss is re-asked after this long: long enough that a streaming reply
// does not re-probe the same word chunk after chunk, short enough that a
// file the agent has since written becomes a link on the next render.
const MISS_TTL_MS = 10_000;

// Per probe; the backend caps the same number (pathlink.MaxProbe).
const PROBE_BATCH = 64;

// Runs of characters a path can be made of. Brackets, quotes and commas
// end one, so "(see a/b.go)," yields a/b.go.
const WORD_RE = /[^\s`'"<>()[\]{}|,;*]+/g;
const SCHEME_RE = /^[a-z][a-z0-9+.-]*:\/\//i;
const POSITION_RE = /(:\d+){1,2}$/;

/** looksLikePath is the cheap shape test that decides what is worth a
 *  probe: something with a slash in it, or a file name with an extension,
 *  optionally followed by :line or :line:col. `bare` also admits a single
 *  word with neither — right for an inline code span, which an agent uses
 *  for `Makefile` as readily as for `main.go`, and wrong for prose. */
export function looksLikePath(tok: string, bare = false): boolean {
  if (tok.length < 2 || tok.length > 512) return false;
  if (SCHEME_RE.test(tok) || tok.startsWith('www.')) return false;
  const path = tok.replace(POSITION_RE, '');
  if (!path || /\s/.test(path)) return false;
  if (path.includes('/')) return /[^/]$/.test(path);
  if (!/^[\w.@+-]+$/.test(path) || !/[A-Za-z_]/.test(path)) return false;
  return bare || /\.[A-Za-z0-9]{1,12}$/.test(path);
}

/** pathTokens finds the path-shaped tokens in a run of prose. Sentence
 *  punctuation after one is not part of it: "edit a/b.go." links a/b.go. */
export function pathTokens(text: string): PathToken[] {
  const out: PathToken[] = [];
  for (const m of text.matchAll(WORD_RE)) {
    const token = m[0].replace(/[.:!?]+$/, '');
    if (looksLikePath(token)) out.push({ start: m.index!, end: m.index! + token.length, token });
  }
  return out;
}

/** codeToken is the whole of an inline code span when that is one
 *  path-shaped word, else nothing: `go test ./...` is a command, not a
 *  file. */
export function codeToken(text: string): PathToken[] {
  const token = text.trim();
  if (!looksLikePath(token, true)) return [];
  const start = text.indexOf(token);
  return [{ start, end: start + token.length, token }];
}

/** A PathLinks with the cache and batching every render needs. */
export interface PathResolver {
  resolve: (tokens: string[]) => Promise<Map<string, PathHit>>;
  open: (hit: PathHit) => void;
}

/** pathResolver wraps a host's PathLinks: tokens asked for in the same
 *  tick go to the backend as one probe, and answers are reused. */
export function pathResolver(links: PathLinks): PathResolver {
  const known = new Map<string, { hit: PathHit | null; at: number }>();
  const inflight = new Map<string, Promise<void>>();
  let batch: string[] = [];
  let flush: Promise<void> | null = null;

  const fresh = (t: string) => {
    const k = known.get(t);
    return !!k && (!!k.hit || Date.now() - k.at < MISS_TTL_MS);
  };
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
        const now = Date.now();
        for (const a of asked) {
          known.set(a, { hit: byToken.get(a) ?? null, at: now });
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
      await Promise.all(want.filter((t) => !fresh(t)).map((t) => inflight.get(t) ?? enqueue(t)));
      const out = new Map<string, PathHit>();
      for (const t of want) {
        const hit = known.get(t)?.hit;
        if (hit) out.set(t, hit);
      }
      return out;
    },
    open: (hit) => links.open(hit),
  };
}

const PathLinksContext = createContext<PathResolver | undefined>();

/** Makes file references under it links. Without one, text stays text. */
export const PathLinksProvider: Component<{ resolver?: PathResolver; children: JSX.Element }> = (p) => (
  <PathLinksContext.Provider value={p.resolver}>{p.children}</PathLinksContext.Provider>
);

export const usePathLinks = () => useContext(PathLinksContext);

/** useHits resolves the tokens a render found, dropping answers for a
 *  render that has since been replaced. */
export function useHits(found: () => PathToken[]): () => Map<string, PathHit> {
  const r = usePathLinks();
  const [hits, setHits] = createSignal<Map<string, PathHit>>(new Map());
  createEffect(() => {
    const toks = found();
    if (!r || toks.length === 0) return;
    let live = true;
    onCleanup(() => { live = false; });
    void r.resolve(toks.map((t) => t.token)).then((m) => { if (live) setHits(m); });
  });
  return hits;
}

/** The title a link carries: where it goes, with the line when it has one. */
export const hitTitle = (h: PathHit) =>
  `Open ${h.path}${h.line ? `:${h.line}${h.col ? `:${h.col}` : ''}` : ''} in the editor`;

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

/** PathText renders text with the file references in it as links. `code`
 *  treats it as an inline code span: one token or none. */
export const PathText: Component<{ text: string; code?: boolean }> = (p) => {
  const linked = !!usePathLinks();
  const found = createMemo(() => (linked ? (p.code ? codeToken(p.text) : pathTokens(p.text)) : []));
  const hits = useHits(found);
  const segments = createMemo(() => {
    const out: { text: string; hit?: PathHit }[] = [];
    let at = 0;
    for (const f of found()) {
      const hit = hits().get(f.token);
      if (!hit) continue;
      if (f.start > at) out.push({ text: p.text.slice(at, f.start) });
      out.push({ text: p.text.slice(f.start, f.end), hit });
      at = f.end;
    }
    if (at < p.text.length) out.push({ text: p.text.slice(at) });
    return out;
  });
  return (
    <For each={segments()}>
      {(s) => (
        <Show when={s.hit} fallback={<>{s.text}</>}>
          <PathLink hit={s.hit!}>{s.text}</PathLink>
        </Show>
      )}
    </For>
  );
};
