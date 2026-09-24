// Indentation detection and save-time cleanups (docs/Review-findings.md
// P2 → edit). Pure decisions, unit-tested under node:test; main.tsx
// feeds detectIndent the file on open and runs normalizeForSave in
// saveTab before the eol conversion.

export interface Indent {
  unit: 'spaces' | 'tabs';
  /** Column width of one level; for tabs, the tab display width. */
  width: number;
}

export const DEFAULT_INDENT: Indent = { unit: 'spaces', width: 2 };

/** Widths a space-indented file is assumed to use. */
const CANDIDATE_WIDTHS = [2, 4, 3, 8];

/**
 * detectIndent guesses how `text` is indented. Lines that start with a
 * tab vote for tabs; lines that start with spaces vote for spaces, with
 * the width taken from the most common positive step between
 * consecutive indented lines (falling back to the smallest indent seen).
 * Returns null when nothing is indented, so the caller can apply its
 * preference instead of a guess.
 *
 * Only the first `maxLines` lines are read: a guess does not get better
 * past a few thousand lines, and a 4 MiB log is the file this must not
 * choke on.
 */
export function detectIndent(text: string, maxLines = 4000): Indent | null {
  let tabLines = 0;
  let spaceLines = 0;
  const steps = new Map<number, number>();
  let minIndent = Infinity;
  let prev = 0;
  let pos = 0;
  let lines = 0;
  while (pos < text.length && lines < maxLines) {
    let end = text.indexOf('\n', pos);
    if (end < 0) end = text.length;
    lines++;
    const line = text.slice(pos, end);
    pos = end + 1;
    if (line.length === 0 || line.trim().length === 0) continue;
    if (line[0] === '\t') {
      tabLines++;
      continue;
    }
    if (line[0] !== ' ') {
      prev = 0;
      continue;
    }
    let n = 0;
    while (n < line.length && line[n] === ' ') n++;
    // A run of spaces inside a tab-indented block (alignment) is not a
    // vote for spaces.
    if (n < line.length && line[n] === '\t') { tabLines++; continue; }
    spaceLines++;
    if (n < minIndent) minIndent = n;
    const d = Math.abs(n - prev);
    if (d > 0) steps.set(d, (steps.get(d) ?? 0) + 1);
    prev = n;
  }
  if (tabLines === 0 && spaceLines === 0) return null;
  if (tabLines > spaceLines) return { unit: 'tabs', width: 4 };
  let best = 0;
  let bestCount = 0;
  for (const w of CANDIDATE_WIDTHS) {
    const c = steps.get(w) ?? 0;
    if (c > bestCount) { best = w; bestCount = c; }
  }
  if (best === 0) {
    // No clean step (one indented line, say): the shallowest indent is
    // the only evidence there is, if it is a plausible width.
    best = CANDIDATE_WIDTHS.includes(minIndent) ? minIndent : DEFAULT_INDENT.width;
  }
  return { unit: 'spaces', width: best };
}

/** indentString is what one level inserts under `indent`. */
export function indentString(indent: Indent): string {
  return indent.unit === 'tabs' ? '\t' : ' '.repeat(Math.max(1, indent.width));
}

/** indentLabel is the status-bar wording: "Spaces: 2" / "Tab". */
export function indentLabel(indent: Indent): string {
  return indent.unit === 'tabs' ? 'Tab' : `Spaces: ${indent.width}`;
}

export interface SaveCleanup {
  /** Strip spaces and tabs before every line break and at the end. */
  trimTrailing?: boolean;
  /** Make sure the text ends with exactly one newline (if non-empty). */
  finalNewline?: boolean;
}

/**
 * normalizeForSave applies the save-time cleanups to LF text (the buffer
 * form — eol conversion happens after). An empty buffer stays empty:
 * ensuring a final newline must not turn an empty file into "\n".
 */
export function normalizeForSave(text: string, opts: SaveCleanup): string {
  let out = text;
  if (opts.trimTrailing) out = out.replace(/[ \t]+(?=\n|$)/g, '');
  if (opts.finalNewline && out.length > 0) {
    out = out.replace(/\n*$/, '\n');
  }
  return out;
}
