// Dropping files onto a terminal pane (docs/Review-findings.md P2 → term
// "file drop from fm (fm sets newline-joined unquoted text/plain, term has
// no drop handler)").
//
// What a terminal can do with a dropped path is type it: the shell is
// mid-command-line and the paths are its next arguments. So the drop
// becomes a PASTE of the paths, shell-quoted and space-separated, with no
// Enter — the user decides what the command is, and whether to run it.
//
// Pure, so the quoting rules are tested under plain node:test rather than
// through a browser.

/** WASH_PATHS_MIME is fm's drag payload — a JSON array of absolute paths.
 *  Restated rather than imported so this module stays framework- and
 *  package-free; drop-paths.test.ts pins it against @wash/ui's copy. */
export const WASH_PATHS_MIME = 'application/x-wash-paths';

/** The slice of DataTransfer these decisions read. */
export interface DropData {
  readonly types: readonly string[];
  getData(format: string): string;
}

/**
 * shellQuote renders one path as a single shell word.
 *
 * Single quotes, because inside them a POSIX shell expands nothing at all
 * — no $, no backtick, no backslash escape — which is what you want for a
 * filename somebody else chose. A single quote in the name itself is the
 * one character that has to leave the quotes: '\'' closes, escapes, and
 * reopens. Names that need no quoting at all are left bare, because a
 * terminal full of '/etc/hosts' reads badly.
 */
export function shellQuote(path: string): string {
  if (path !== '' && /^[A-Za-z0-9_@%+=:,./-]+$/.test(path)) return path;
  return `'${path.replace(/'/g, `'\\''`)}'`;
}

/**
 * pathsFrom returns the paths a drop carries, in order.
 *
 * fm's own drags carry the JSON array; a drag from anywhere else that
 * happens to be newline-joined absolute paths (the brief's second case,
 * and what fm itself used to set) is accepted from text/plain — but only
 * when EVERY line looks like an absolute path, so dropping a paragraph of
 * prose onto a terminal is left to the ordinary text paste.
 */
export function pathsFrom(dt: DropData | null | undefined): string[] {
  if (!dt) return [];
  if (dt.types.includes(WASH_PATHS_MIME)) {
    try {
      const arr = JSON.parse(dt.getData(WASH_PATHS_MIME));
      if (Array.isArray(arr)) return arr.filter((s): s is string => typeof s === 'string' && s !== '');
    } catch {
      /* not ours after all */
    }
    return [];
  }
  if (!dt.types.includes('text/plain')) return [];
  const lines = dt.getData('text/plain').split('\n').map((l) => l.trim()).filter((l) => l !== '');
  if (!lines.length || !lines.every((l) => l.startsWith('/'))) return [];
  return lines;
}

/** acceptsDrop is the dragover gate. */
export function acceptsDrop(dt: DropData | null | undefined): boolean {
  return pathsFrom(dt).length > 0;
}

/**
 * dropText is what gets pasted: the paths quoted and space-separated, with
 * a trailing space so the next thing typed is a new word — and no newline,
 * because the terminal must never run a command the user did not.
 */
export function dropText(paths: string[]): string {
  if (!paths.length) return '';
  return paths.map(shellQuote).join(' ') + ' ';
}
