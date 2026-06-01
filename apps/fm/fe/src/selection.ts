// Pure decision kernel for the file-list multi-selection — extracted from
// the row-click handler so the shift-range / ctrl-toggle / plain-select
// semantics can be unit-tested directly instead of only through the
// (slow, full-stack) bulkops/fm-shortcuts e2e specs. No framework imports;
// the component keeps the side effects (preview, status, focus) and just
// applies the {selection, anchor} this returns.
//
// fm-only for now, so it lives in the consumer rather than @wash/fs-client
// (no second consumer yet — see feedback_no_premature_service).

export interface SelectionState {
  /** The set of selected row paths. */
  selection: Set<string>;
  /** The most-recently-clicked path; the pivot for shift-range. */
  anchor: string | null;
}

export interface ClickMods {
  shift: boolean;
  ctrlOrMeta: boolean;
}

// Compute the next multi-selection from a row click.
//   plain      → select just the clicked row; anchor moves to it
//   ctrl/cmd   → toggle the clicked row in/out; anchor moves to it
//   shift      → range from the anchor to the clicked row (inclusive);
//                anchor is preserved so the range can keep growing.
//                With no usable anchor it degrades to a single select.
// `rows` is the visible row paths in display order (only used for shift).
export function nextSelection(
  prev: SelectionState,
  rowPath: string,
  rows: string[],
  mods: ClickMods,
): SelectionState {
  if (mods.shift && prev.anchor) {
    const a = rows.indexOf(prev.anchor);
    const b = rows.indexOf(rowPath);
    if (a >= 0 && b >= 0) {
      const [lo, hi] = a < b ? [a, b] : [b, a];
      return { selection: new Set(rows.slice(lo, hi + 1)), anchor: prev.anchor };
    }
    // Anchor or target fell out of the visible list — single-select but
    // leave the anchor put (matches the prior inline behaviour).
    return { selection: new Set([rowPath]), anchor: prev.anchor };
  }
  if (mods.ctrlOrMeta) {
    const selection = new Set(prev.selection);
    if (selection.has(rowPath)) selection.delete(rowPath);
    else selection.add(rowPath);
    return { selection, anchor: rowPath };
  }
  return { selection: new Set([rowPath]), anchor: rowPath };
}
