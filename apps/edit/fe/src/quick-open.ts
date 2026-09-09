// Quick-open ranking and the recent-files list (docs/Review-findings.md
// P2 → edit): the decisions behind the Ctrl+P palette, framework-free so
// they run under node:test. main.tsx wires them to the BE's `find` reply
// and the prefs file.

/** RECENT_CAP is how many recent paths the prefs file keeps. */
export const RECENT_CAP = 20;

/**
 * pushRecent returns `recent` with `path` at the front, deduplicated and
 * capped. The input is not mutated.
 */
export function pushRecent(recent: readonly string[], path: string, cap = RECENT_CAP): string[] {
  if (!path) return recent.slice(0, cap);
  const out = [path];
  for (const p of recent) {
    if (p !== path && p) out.push(p);
    if (out.length >= cap) break;
  }
  return out;
}

/** dropRecent removes `path` (a file that no longer opens) from the list. */
export function dropRecent(recent: readonly string[], path: string): string[] {
  return recent.filter((p) => p !== path);
}

// Boundary characters: a match right after one of these reads as the
// start of a word, which is what people type when they abbreviate.
const isBoundary = (c: string): boolean => c === '/' || c === '_' || c === '-' || c === '.' || c === ' ';

/**
 * fuzzyScore ranks `candidate` against `query` as a case-insensitive
 * subsequence match: null when the query's characters do not all appear
 * in order, otherwise a score where higher is better. Matches at word
 * starts (after / _ - .), consecutive runs, and matches inside the
 * basename score higher; a longer candidate scores a little lower so
 * `main.go` beats `internal/legacy/main_test.go` for "main".
 *
 * An empty query matches everything with a flat score.
 */
export function fuzzyScore(query: string, candidate: string): number | null {
  const q = query.toLowerCase();
  if (q.length === 0) return 0;
  const c = candidate.toLowerCase();
  if (q.length > c.length) return null;
  const baseStart = c.lastIndexOf('/') + 1;
  let score = 0;
  let qi = 0;
  let prevMatch = -2;
  // Greedy left-to-right, but prefer a boundary match for each query
  // char when one is available within the remaining text. Not optimal
  // in the dynamic-programming sense; good enough for a file palette
  // and linear in the candidate.
  for (let ci = 0; ci < c.length && qi < q.length; ci++) {
    if (c[ci] !== q[qi]) continue;
    // Look ahead for a boundary-start occurrence of the same char
    // before the next needed char would run out of room.
    let pick = ci;
    if (!(ci === 0 || isBoundary(c[ci - 1]) || prevMatch === ci - 1)) {
      for (let k = ci + 1; k < c.length - (q.length - qi - 1); k++) {
        if (c[k] === q[qi] && (isBoundary(c[k - 1]) || k === baseStart)) { pick = k; break; }
      }
    }
    ci = pick;
    let gain = 1;
    if (ci === 0 || isBoundary(c[ci - 1])) gain += 4;
    if (ci === baseStart) gain += 4;
    if (prevMatch === ci - 1) gain += 3;
    if (ci >= baseStart) gain += 2;
    score += gain;
    prevMatch = ci;
    qi++;
  }
  if (qi < q.length) return null;
  // Whole-query exact hit on the basename is the strongest signal.
  const base = c.slice(baseStart);
  if (base === q) score += 20;
  else if (base.startsWith(q)) score += 10;
  else if (base.includes(q)) score += 5;
  return score - Math.min(10, c.length / 20);
}

/**
 * rankFiles returns up to `limit` of `files` best matching `query`,
 * best first; ties keep the input order (which the BE lists
 * depth-first, so shallower paths come first). An empty query returns
 * the first `limit` files unranked.
 */
export function rankFiles(query: string, files: readonly string[], limit = 50): string[] {
  if (query.trim() === '') return files.slice(0, limit);
  const scored: { f: string; s: number; i: number }[] = [];
  for (let i = 0; i < files.length; i++) {
    const s = fuzzyScore(query.trim(), files[i]);
    if (s !== null) scored.push({ f: files[i], s, i });
  }
  scored.sort((a, b) => b.s - a.s || a.i - b.i);
  return scored.slice(0, limit).map((x) => x.f);
}
