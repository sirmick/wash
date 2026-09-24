// Pure decision kernel for keyboard row navigation in the fm tree —
// Arrow/Home/End/PageUp/PageDown over the flattened visible rows, plus the
// ArrowRight/ArrowLeft expand/collapse/parent semantics every desktop file
// manager shares. No framework imports: the component feeds it the visible
// rows (path, depth, dir-ness, expansion) and applies the returned decision
// (select / extend / expand / collapse / move) with its own side effects.
//
// fm-only for now, so it lives in the consumer rather than @wash/fs-client
// (no second consumer yet — see feedback_no_premature_service).

export interface NavRow {
  path: string;
  depth: number;
  isDir: boolean;
  expanded: boolean;
}

// Vertical moves. `cursor` is the current cursor path (null when nothing is
// focused); the result is the path the cursor lands on, or null when there
// is nowhere to go (empty list, or already at the edge). With no cursor,
// Down/PageDown/End land on the first row and Up/PageUp/Home on the last —
// so the first arrow press always lights a row.
export type VerticalMove = 'up' | 'down' | 'pageUp' | 'pageDown' | 'home' | 'end';

export function nextRow(rows: NavRow[], cursor: string | null, move: VerticalMove, pageSize: number): string | null {
  if (rows.length === 0) return null;
  const last = rows.length - 1;
  const idx = cursor == null ? -1 : rows.findIndex((r) => r.path === cursor);
  const page = Math.max(1, Math.floor(pageSize));
  let target: number;
  switch (move) {
    case 'home':
      target = 0;
      break;
    case 'end':
      target = last;
      break;
    case 'up':
      target = idx < 0 ? last : idx - 1;
      break;
    case 'down':
      target = idx < 0 ? 0 : idx + 1;
      break;
    case 'pageUp':
      target = idx < 0 ? last : idx - page;
      break;
    case 'pageDown':
      target = idx < 0 ? 0 : idx + page;
      break;
  }
  target = Math.max(0, Math.min(last, target));
  if (idx >= 0 && target === idx) return null;
  return rows[target].path;
}

// Horizontal moves. ArrowRight on a collapsed folder expands it; on an
// expanded folder it steps into the first child (when one is visible);
// on a file it does nothing. ArrowLeft on an expanded folder collapses it;
// otherwise it jumps to the row's parent (the nearest preceding row one
// level up) — or nothing when the row is at the top level.
export type HorizontalDecision =
  | { kind: 'expand'; path: string }
  | { kind: 'collapse'; path: string }
  | { kind: 'move'; path: string }
  | { kind: 'none' };

export function arrowRight(rows: NavRow[], cursor: string | null): HorizontalDecision {
  const idx = cursor == null ? -1 : rows.findIndex((r) => r.path === cursor);
  if (idx < 0) return { kind: 'none' };
  const row = rows[idx];
  if (!row.isDir) return { kind: 'none' };
  if (!row.expanded) return { kind: 'expand', path: row.path };
  const next = rows[idx + 1];
  if (next && next.depth === row.depth + 1) return { kind: 'move', path: next.path };
  return { kind: 'none' };
}

export function arrowLeft(rows: NavRow[], cursor: string | null): HorizontalDecision {
  const idx = cursor == null ? -1 : rows.findIndex((r) => r.path === cursor);
  if (idx < 0) return { kind: 'none' };
  const row = rows[idx];
  if (row.isDir && row.expanded) return { kind: 'collapse', path: row.path };
  for (let i = idx - 1; i >= 0; i--) {
    if (rows[i].depth < row.depth) return { kind: 'move', path: rows[i].path };
  }
  return { kind: 'none' };
}

// pageSizeFor turns the list viewport + one row's height into a page step
// (rows per screen, minus one so the last visible row stays as context).
// Falls back to 10 when nothing has been measured yet.
export function pageSizeFor(viewportPx: number, rowPx: number): number {
  if (viewportPx <= 0 || rowPx <= 0) return 10;
  return Math.max(1, Math.floor(viewportPx / rowPx) - 1);
}
