// Dropping things onto the agent composer (docs/Review-findings.md,
// 2026-09-08 sweep: the placeholder promised "drop a file from wash-fm…"
// and nothing handled it).
//
// Two kinds of drop, one insertion point — the caret:
//
//   - a wash drag (fm's rows, edit's tabs) carries absolute paths; each
//     becomes an `@<path>` reference the agent can read through the fs
//     capability, quoted when it has whitespace;
//   - an OS file drop carries File objects with no path the page can
//     name, so a text file under the cap is attached INLINE as a fenced
//     block headed by its name. Anything else is skipped and said so.
//
// Framework-free: the decisions live here under node:test, and the
// Solid half in agent-session.tsx only wires events to them.

/**
 * WASH_PATHS_MIME is fm's drag payload type. It MUST equal
 * @wash/fs-client's DRAG_MIME (web/fs-client/src/dnd.ts) — restated here
 * because @wash/ui does not depend on that package, and pinned by
 * agent-compose-drop.test.ts, which imports both and fails on drift.
 */
export const WASH_PATHS_MIME = 'application/x-wash-paths';

/** MAX_ATTACH_BYTES bounds a file attached inline. A prompt is context;
 *  a megabyte pasted into it is a bill, and past this size a path
 *  reference the agent reads on demand is the better answer anyway. */
export const MAX_ATTACH_BYTES = 256 * 1024;

/** The slice of DataTransfer the decisions read — narrowed so tests can
 *  hand in a plain object. */
export interface DropData {
  readonly types: readonly string[];
  getData(format: string): string;
  readonly files?: ArrayLike<File>;
}

/** A file's identity, minus the bytes — what isTextLike needs. */
export interface FileLike {
  name: string;
  type: string;
  size: number;
}

/** washPathsFrom returns the absolute paths a wash drag carries, or []
 *  for any other drag. Malformed payloads are ignored, not thrown. */
export function washPathsFrom(dt: DropData | null | undefined): string[] {
  if (!dt || !dt.types.includes(WASH_PATHS_MIME)) return [];
  try {
    const arr = JSON.parse(dt.getData(WASH_PATHS_MIME));
    if (Array.isArray(arr)) return arr.filter((s): s is string => typeof s === 'string' && s !== '');
  } catch {
    /* not ours */
  }
  return [];
}

/** acceptsDrop is the dragover gate: wash paths, or OS files. A text
 *  drag from another page, say, is left to the browser's default. */
export function acceptsDrop(dt: DropData | null | undefined): boolean {
  if (!dt) return false;
  return dt.types.includes(WASH_PATHS_MIME) || dt.types.includes('Files');
}

/** pathRef is one path as the composer writes it: `@/abs/path`, quoted
 *  when whitespace would otherwise split it. */
export function pathRef(path: string): string {
  return /\s/.test(path) ? `@"${path.replace(/"/g, '\\"')}"` : `@${path}`;
}

/** pathRefs joins several, space-separated. */
export function pathRefs(paths: readonly string[]): string {
  return paths.map(pathRef).join(' ');
}

/** insertAt splices `insert` over [start, end) of `text`, padding with a
 *  space on either side when the neighbour is not already whitespace, and
 *  returns the new text with where the caret should land (after it). */
export function insertAt(text: string, start: number, end: number, insert: string): { text: string; caret: number } {
  const s = Math.max(0, Math.min(start, text.length));
  const e = Math.max(s, Math.min(end, text.length));
  const before = text.slice(0, s);
  const after = text.slice(e);
  const padL = before !== '' && !/\s$/.test(before) && !/^\s/.test(insert) ? ' ' : '';
  const padR = after !== '' && !/^\s/.test(after) && !/\s$/.test(insert) ? ' ' : '';
  const mid = padL + insert + padR;
  return { text: before + mid + after, caret: before.length + mid.length };
}

// Extensions we treat as text when the browser reports no useful MIME
// type — which it does for most source files.
const TEXT_EXTS = new Set([
  'txt', 'md', 'markdown', 'rst', 'log', 'csv', 'tsv', 'json', 'jsonl', 'yaml', 'yml', 'toml', 'ini', 'cfg',
  'conf', 'env', 'xml', 'html', 'htm', 'css', 'scss', 'svg', 'js', 'mjs', 'cjs', 'jsx', 'ts', 'tsx', 'go',
  'rs', 'py', 'rb', 'sh', 'bash', 'zsh', 'fish', 'c', 'h', 'cc', 'cpp', 'hpp', 'java', 'kt', 'swift', 'sql',
  'lua', 'php', 'pl', 'r', 'scala', 'dart', 'ex', 'exs', 'erl', 'hs', 'ml', 'nix', 'mk', 'makefile', 'diff',
  'patch', 'gitignore', 'dockerfile', 'proto', 'graphql', 'gql', 'vue', 'svelte', 'astro',
]);

/** ext is the lower-cased extension, or the whole lower-cased name for a
 *  bare one (Makefile, Dockerfile). */
function ext(name: string): string {
  const base = name.split('/').pop() ?? name;
  const i = base.lastIndexOf('.');
  return (i > 0 ? base.slice(i + 1) : base).toLowerCase();
}

/** isTextLike decides whether an OS file gets attached inline: a text
 *  MIME type or a known source/text extension, under the size cap. */
export function isTextLike(f: FileLike): boolean {
  if (f.size > MAX_ATTACH_BYTES) return false;
  const t = (f.type || '').toLowerCase();
  if (t.startsWith('text/')) return true;
  if (t === 'application/json' || t === 'application/xml' || t === 'application/x-sh' || t === 'application/javascript' || t === 'application/typescript' || t === 'image/svg+xml') return true;
  if (t !== '' && !t.startsWith('application/octet-stream')) return false;
  return TEXT_EXTS.has(ext(f.name));
}

/** fenceLang is the code-fence language for a file name, for highlighting
 *  and for the agent's benefit; '' when there is nothing sensible. */
export function fenceLang(name: string): string {
  const e = ext(name);
  const bare = !(name.split('/').pop() ?? name).includes('.');
  const map: Record<string, string> = {
    mjs: 'js', cjs: 'js', jsx: 'jsx', tsx: 'tsx', yml: 'yaml', md: 'markdown', markdown: 'markdown',
    sh: 'bash', zsh: 'bash', bash: 'bash', py: 'python', rb: 'ruby', rs: 'rust', kt: 'kotlin', hs: 'haskell',
    ex: 'elixir', exs: 'elixir', pl: 'perl', htm: 'html', makefile: 'makefile', dockerfile: 'dockerfile',
    txt: '', log: '', gitignore: '', env: '',
  };
  if (e in map) return map[e];
  // A bare name (README, LICENSE) is not an extension.
  if (bare) return '';
  return /^[a-z0-9+-]{1,12}$/.test(e) ? e : '';
}

/** fencedAttachment is what an inline-attached file becomes in the
 *  prompt: its name on a line, then a fenced block. The fence is longer
 *  than any run of backticks in the content, so a Markdown file with its
 *  own fences cannot close ours early. */
export function fencedAttachment(name: string, content: string): string {
  const longest = Math.max(2, ...(content.match(/`+/g) ?? []).map((m) => m.length));
  const fence = '`'.repeat(longest + 1);
  const body = content.endsWith('\n') ? content : content + '\n';
  return `${name}:\n${fence}${fenceLang(name)}\n${body}${fence}`;
}

/** readTextFile reads a File as UTF-8 text. FileReader rather than
 *  File.text(), which jsdom lacks and older engines did too. */
export function readTextFile(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(typeof r.result === 'string' ? r.result : '');
    r.onerror = () => reject(r.error ?? new Error('read failed'));
    r.readAsText(file);
  });
}

/** describeSkipped is the one-line note for files a drop did not attach. */
export function describeSkipped(names: readonly string[]): string {
  if (names.length === 0) return '';
  const cap = `${Math.round(MAX_ATTACH_BYTES / 1024)} KB`;
  const list = names.length <= 3 ? names.join(', ') : `${names.slice(0, 3).join(', ')} and ${names.length - 3} more`;
  return `Not attached (only text files under ${cap} are): ${list}`;
}
