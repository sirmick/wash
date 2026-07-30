// analyzePaste — the smart-paste decision kernel (docs/AGENT_TERM.md §10,
// issue #19 item 3).
//
// Text copied out of an AI chat (or a rendered man page, or a blog) arrives
// broken in two distinct ways, and they need opposite treatment:
//
//   1. **Smuggled junk** — a leading "$ " prompt marker, curly quotes where
//      the shell needs straight ones, a non-breaking space that renders
//      identically to a space and produces "command not found", zero-width
//      characters, stray ``` fences. Mechanical, always safe to remove, and
//      invisible either way: cleaning it silently is strictly better than
//      making the user look at it.
//   2. **Wrap artifacts** — the chat soft-wrapped one long command, and the
//      copy turned each visual line into a real newline, so a shell runs N
//      broken commands instead of one. Repairing this means CHANGING
//      STRUCTURE, which is exactly the kind of thing a paste filter must
//      never do behind the user's back — so it is offered, not applied.
//
// Hence the verdict: `as-is` (nothing found), `clean` (junk only, safe to
// apply silently), or `ask` (structure would change, or it's multi-line and
// the user should see what we propose).
//
// This file is pure and has no imports. The tests are the specification —
// wrap detection is a pile of heuristics, and the only honest way to pin a
// heuristic down is a table of "this is a wrapped command" / "this is a real
// script" cases that must never swap places.

/** What analyzePaste found. Counts drive the overlay's summary line. */
export interface PasteIssue {
  kind:
    | 'prompt-marker'
    | 'curly-quotes'
    | 'nbsp'
    | 'zero-width'
    | 'code-fence'
    | 'crlf'
    | 'wrapped'
    | 'executes-immediately';
  /** how many occurrences (lines, for structural kinds) */
  count: number;
  /** one-line human summary for the overlay */
  label: string;
}

export interface PasteAnalysis {
  /**
   * as-is  — nothing to do; paste the original.
   * clean  — junk only, nothing structural: safe to apply without asking.
   * ask    — we would change structure, or it's multi-line: show the user.
   */
  verdict: 'as-is' | 'clean' | 'ask';
  /** the repaired text (=== original when verdict is as-is) */
  cleaned: string;
  original: string;
  issues: PasteIssue[];
  /** true when lines were joined (a structural change) */
  wrapped: boolean;
  /** line count of the ORIGINAL paste */
  lines: number;
}

export interface PasteOptions {
  /**
   * Whether the receiving program has bracketed paste on (DEC 2004). When
   * it is OFF and the paste is multi-line, the shell will execute each line
   * the instant it arrives — the paste-jacking case, and the one time a
   * multi-line paste deserves a warning regardless of how clean it is.
   * Undefined = unknown, no warning.
   */
  bracketedPaste?: boolean;
}

// Characters that render as a space but aren't one. U+00A0 is the famous
// one (it is why "command not found" happens to text that looks perfect);
// the rest come from typographic renderers.
const SPACE_LOOKALIKES = /[   -   　]/g;
// Zero-width and BOM: invisible, and shells choke on them.
const ZERO_WIDTH = /[​‌‍⁠﻿]/g;
const CURLY_DOUBLE = /[“”„‟″]/g;
const CURLY_SINGLE = /[‘’‚‛′]/g;
// A prompt marker a human copied along with the command. Only "$" and "%"
// — "#" is a comment character, and stripping it would change meaning.
const PROMPT_MARKER = /^\s*[$%] /;
const CODE_FENCE = /^\s*```[\w+-]*\s*$/;

// Line starts that mean "this is a real script, leave its structure alone".
const SHELL_STRUCTURE =
  /^\s*(if|then|else|elif|fi|for|while|until|do|done|case|esac|function|return|export|local|#|&&|\|\||\||;|\}|\{|\)|fi;|EOF)\b/;

// wrapMinWidth is the shortest line length we'll believe is a wrap column.
// Below this, "all lines are about the same length" is a coincidence — a
// three-line script of short commands would qualify otherwise.
const wrapMinWidth = 40;
// wrapSlack is how far short of the wrap column a wrapped line may fall.
// Word wrap breaks before the word that didn't fit, so lines land a little
// short; more than this and the "lines are clustered" signal is gone.
const wrapSlack = 12;
// wrapTailMin: the remainder line of a real wrap carries a meaningful chunk
// of the command. A 61-char line followed by "ls" is two commands that
// happen to sit next to each other, and joining them would be a disaster —
// so a very short tail vetoes the whole diagnosis. False negatives (a real
// wrap ending in "-v" we leave alone) are the safe direction.
const wrapTailMinChars = 6;
const wrapTailMinRatio = 0.12;
// Characters that can continue a token across a mid-token break. "-" is
// deliberately absent: a line starting with a flag is a new token, which
// means the newline replaced a space.
const TOKEN_CONTINUES = /^[A-Za-z0-9._/~%+&?#=:]/;
const TOKEN_ENDS = /[A-Za-z0-9._/~%+&?#=:-]$/;

/**
 * analyzePaste inspects clipboard text bound for a terminal and returns
 * what it would do about it. It never returns a `cleaned` that changes
 * structure without also returning verdict `ask`.
 */
export function analyzePaste(text: string, opts: PasteOptions = {}): PasteAnalysis {
  const original = text;
  const issues: PasteIssue[] = [];
  let s = text;

  // --- CRLF first: everything below counts lines. ---
  if (s.includes('\r\n')) {
    const count = (s.match(/\r\n/g) ?? []).length;
    s = s.replace(/\r\n/g, '\n');
    issues.push({ kind: 'crlf', count, label: 'Windows line endings' });
  }
  s = s.replace(/\r/g, '\n');

  // --- invisible junk ---
  const nbspCount = (s.match(SPACE_LOOKALIKES) ?? []).length;
  if (nbspCount > 0) {
    s = s.replace(SPACE_LOOKALIKES, ' ');
    issues.push({
      kind: 'nbsp',
      count: nbspCount,
      label: nbspCount === 1 ? 'a non-breaking space' : `${nbspCount} non-breaking spaces`,
    });
  }
  const zwCount = (s.match(ZERO_WIDTH) ?? []).length;
  if (zwCount > 0) {
    s = s.replace(ZERO_WIDTH, '');
    issues.push({
      kind: 'zero-width',
      count: zwCount,
      label: zwCount === 1 ? 'a zero-width character' : `${zwCount} zero-width characters`,
    });
  }
  const curlyCount = (s.match(CURLY_DOUBLE) ?? []).length + (s.match(CURLY_SINGLE) ?? []).length;
  if (curlyCount > 0) {
    s = s.replace(CURLY_DOUBLE, '"').replace(CURLY_SINGLE, "'");
    issues.push({
      kind: 'curly-quotes',
      count: curlyCount,
      label: curlyCount === 1 ? 'a curly quote' : `${curlyCount} curly quotes`,
    });
  }

  // --- line-shaped junk: fences and prompt markers ---
  let lines = s.split('\n');
  const fenceCount = lines.filter((l) => CODE_FENCE.test(l)).length;
  if (fenceCount > 0) {
    lines = lines.filter((l) => !CODE_FENCE.test(l));
    // A fence pair usually leaves a trailing empty line behind.
    while (lines.length > 1 && lines[lines.length - 1].trim() === '') lines.pop();
    issues.push({
      kind: 'code-fence',
      count: fenceCount,
      label: fenceCount === 1 ? 'a code-fence line' : `${fenceCount} code-fence lines`,
    });
  }
  const promptCount = countPromptMarkers(lines);
  if (promptCount > 0) {
    lines = lines.map((l) => (PROMPT_MARKER.test(l) ? l.replace(PROMPT_MARKER, '') : l));
    issues.push({
      kind: 'prompt-marker',
      count: promptCount,
      label: promptCount === 1 ? 'a copied “$ ” prompt' : `${promptCount} copied “$ ” prompts`,
    });
  }

  // --- structural: one command that got soft-wrapped ---
  let wrapped = false;
  const joined = joinWrapped(lines);
  if (joined !== null) {
    lines = [joined];
    wrapped = true;
    issues.push({ kind: 'wrapped', count: 1, label: 'one command split across lines' });
  }

  s = lines.join('\n');

  // --- the paste-jacking warning, on the text as it will actually arrive ---
  const finalLines = s.split('\n').filter((l) => l.trim() !== '').length;
  if (finalLines > 1 && opts.bracketedPaste === false) {
    issues.push({
      kind: 'executes-immediately',
      count: finalLines,
      label: 'the shell will run each line immediately',
    });
  }

  const originalLines = original.split(/\r\n|\r|\n/).length;
  return {
    verdict: decide(original, s, issues, wrapped, originalLines),
    cleaned: s,
    original,
    issues,
    wrapped,
    lines: originalLines,
  };
}

// decide is the UX rule from §10: silence for the invisible stuff, a
// question for anything that changes shape or that the user should see
// before it hits a shell.
function decide(
  original: string,
  cleaned: string,
  issues: PasteIssue[],
  wrapped: boolean,
  originalLines: number,
): PasteAnalysis['verdict'] {
  if (issues.length === 0) return 'as-is';
  // A structural repair is never silent.
  if (wrapped) return 'ask';
  // Neither is a warning — there is nothing to "apply" for it.
  if (issues.some((i) => i.kind === 'executes-immediately')) return 'ask';
  // Single line, junk only: cleaning it is never wrong, and showing an
  // overlay for an invisible character is worse than fixing it.
  if (originalLines === 1) return 'clean';
  // Multi-line junk: mechanically safe, but the user is pasting something
  // with structure into a shell — let them look.
  return cleaned === original ? 'as-is' : 'ask';
}

// countPromptMarkers reports how many lines carry a copied prompt, but only
// when the block is consistently prompt-prefixed (every non-empty line) or
// it is a single line. A "$ " on SOME lines of a script is more likely to
// be real syntax than a prompt.
function countPromptMarkers(lines: string[]): number {
  const nonEmpty = lines.filter((l) => l.trim() !== '');
  if (nonEmpty.length === 0) return 0;
  const marked = nonEmpty.filter((l) => PROMPT_MARKER.test(l));
  if (marked.length === 0) return 0;
  if (marked.length === nonEmpty.length) return marked.length;
  return nonEmpty.length === 1 ? marked.length : 0;
}

/**
 * joinWrapped returns the single command these lines were before a chat
 * soft-wrapped them, or null if they look like genuine multi-line text.
 *
 * The fingerprint (§10), all of which must hold:
 *
 *   - 2+ non-empty lines and no blank line inside (a wrapped command has no
 *     paragraph breaks);
 *   - no shell structure at any line start, and no trailing continuation
 *     (`\`) or operator (`&&`, `|`, `;`) — those are real multi-line syntax
 *     that already works when pasted;
 *   - every line but the last is clustered near the longest line, i.e.
 *     there is a wrap column;
 *   - the last line is no longer than that column.
 *
 * Joining rule: a line that stops exactly AT the column was cut mid-token,
 * so it rejoins with nothing; one that stops short of it was word-wrapped
 * (the newline replaced a space), so it rejoins with a space.
 */
export function joinWrapped(lines: string[]): string | null {
  // Trailing empty lines are an artifact of the copy, not structure.
  const body = [...lines];
  while (body.length > 0 && body[body.length - 1].trim() === '') body.pop();
  if (body.length < 2) return null;
  // A blank line inside means paragraphs, not wrapping.
  if (body.some((l) => l.trim() === '')) return null;
  for (const l of body) {
    if (SHELL_STRUCTURE.test(l)) return null;
    const t = l.trimEnd();
    if (t.endsWith('\\')) return null;
    if (/(&&|\|\||\||;|\{|\}|then|do)$/.test(t)) return null;
  }
  const lens = body.map((l) => l.length);
  // The wrap column is set by the lines that were actually wrapped — the
  // last line is the leftover and says nothing about the width.
  const head = lens.slice(0, -1);
  const col = Math.max(...head);
  const tail = lens[lens.length - 1];
  if (col < wrapMinWidth) return null;
  if (Math.min(...head) < col - wrapSlack) return null;
  // A line LONGER than the column can't have come from wrapping at it.
  if (tail > col) return null;
  if (tail < Math.max(wrapTailMinChars, Math.floor(col * wrapTailMinRatio))) return null;

  let out = body[0];
  for (let i = 1; i < body.length; i++) {
    const prev = out;
    const prevLen = body[i - 1].length;
    const next = body[i];
    if (prev.endsWith(' ') || next.startsWith(' ')) {
      // The space survived the copy; nothing to restore.
      out = prev + next;
    } else if (prevLen === col && TOKEN_CONTINUES.test(next) && TOKEN_ENDS.test(prev)) {
      // The line stopped exactly at the column and both sides look like one
      // token: the renderer cut mid-token, so the newline replaced nothing.
      out = prev + next;
    } else {
      // Word wrap: the newline replaced the space before this word.
      out = prev + ' ' + next;
    }
  }
  return out;
}
