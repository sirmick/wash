// Pure kernel for the tree's type-to-filter: which visible rows survive a
// query, and where the query matches inside a name (for highlighting).
// No framework imports; the component owns the filter box + the row
// rendering and just applies these.
//
// The filter narrows the CURRENT folder only — rows that are direct
// children of `dir` are kept when their name contains the query
// (case-insensitive substring); rows deeper under `dir` follow their
// top-level ancestor (an expanded matching folder keeps its children);
// rows outside `dir` (the ancestors and their siblings, which give the
// tree its shape) are untouched.

export interface FilterableRow {
  path: string;
}

function childUnder(dir: string, p: string): string | null {
  const prefix = dir === '/' ? '/' : dir + '/';
  if (!p.startsWith(prefix) || p === dir) return null;
  const rest = p.slice(prefix.length);
  const i = rest.indexOf('/');
  return i < 0 ? rest : rest.slice(0, i);
}

export function normalizeQuery(query: string): string {
  return query.trim().toLowerCase();
}

export function nameMatches(name: string, query: string): boolean {
  const q = normalizeQuery(query);
  return q === '' || name.toLowerCase().includes(q);
}

export function filterRows<R extends FilterableRow>(rows: readonly R[], dir: string, query: string): R[] {
  const q = normalizeQuery(query);
  if (q === '') return rows.slice();
  return rows.filter((r) => {
    const top = childUnder(dir, r.path);
    if (top == null) return true;
    return top.toLowerCase().includes(q);
  });
}

// matchRanges returns the non-overlapping [start, end) spans where the
// query occurs in name, case-insensitively, in order. Empty for an empty
// query or no match.
export function matchRanges(name: string, query: string): Array<[number, number]> {
  const q = normalizeQuery(query);
  if (q === '') return [];
  const hay = name.toLowerCase();
  const out: Array<[number, number]> = [];
  let from = 0;
  for (;;) {
    const i = hay.indexOf(q, from);
    if (i < 0) break;
    out.push([i, i + q.length]);
    from = i + q.length;
  }
  return out;
}

// splitByRanges turns a name + its match ranges into alternating plain /
// matched segments, so a renderer can wrap the matched ones.
export function splitByRanges(name: string, ranges: Array<[number, number]>): Array<{ text: string; match: boolean }> {
  const out: Array<{ text: string; match: boolean }> = [];
  let pos = 0;
  for (const [s, e] of ranges) {
    if (s > pos) out.push({ text: name.slice(pos, s), match: false });
    out.push({ text: name.slice(s, e), match: true });
    pos = e;
  }
  if (pos < name.length) out.push({ text: name.slice(pos), match: false });
  return out;
}

// isTypeToFilterKey reports whether a keydown should open the filter box:
// a single printable character with no Ctrl/Meta/Alt. Space is excluded
// (it toggles the cursor row).
export function isTypeToFilterKey(key: string, mods: { ctrl: boolean; meta: boolean; alt: boolean }): boolean {
  if (mods.ctrl || mods.meta || mods.alt) return false;
  if (key.length !== 1) return false;
  return key !== ' ';
}
