// Drag-and-drop payload parsing + drop-accept logic. Framework-free
// (no DOM events, no Solid) so it's unit-testable with node:test and
// reusable by the other apps that hand-roll the same drag protocol
// (edit). Extracted verbatim from fm's App() closure. The thin DOM
// event handlers (preventDefault / stopPropagation / setDropTargetPath)
// stay in App and call into these.
//
// Wire protocol: a drag carries a JSON array of absolute source paths
// under application/x-wash-paths (plural). A single drag from an
// unselected row carries one path; dragging a row that's part of a
// multi-selection carries every selected path. text/plain is a friendly
// newline-joined fallback for drops onto non-wash targets.

export const DRAG_MIME = 'application/x-wash-paths';

// The minimal slice of DataTransfer we touch — narrowed to an interface
// so tests can pass a plain object instead of a real DragEvent. The
// DOM's DataTransfer is structurally assignable to this.
export interface DragData {
  getData(format: string): string;
  readonly types: readonly string[];
}

// readDragPaths pulls our JSON-array payload out of a drag's
// DataTransfer. Returns [] for a missing transfer, a drag that doesn't
// carry our MIME (text drops from other apps, etc.), or malformed JSON —
// so non-wash drags are ignored cleanly. Non-string array members are
// filtered out defensively.
export function readDragPaths(dt: DragData | null | undefined): string[] {
  if (!dt) return [];
  const json = dt.getData(DRAG_MIME);
  if (!json) return [];
  try {
    const arr = JSON.parse(json);
    if (Array.isArray(arr)) return arr.filter((s) => typeof s === 'string');
  } catch {
    /* ignore */
  }
  return [];
}

// hasWashDrag reports whether a drag carries our payload — the
// drop-accept gate. Callers preventDefault (mark a valid drop target)
// only when this is true.
export function hasWashDrag(dt: DragData | null | undefined): boolean {
  return !!dt && dt.types.includes(DRAG_MIME);
}

// dropEffectFor maps the alt/option modifier to the OS cursor hint:
// Alt-drag is a copy gesture (+copy badge), otherwise a move. Assigned
// to dataTransfer.dropEffect during dragover so Mac users get feedback
// that the modifier is doing something.
export function dropEffectFor(altKey: boolean): 'copy' | 'move' {
  return altKey ? 'copy' : 'move';
}

// dragPayload builds the source-path list for a dragstart: the whole
// current selection if the dragged row is part of it, else just that
// row. Drop handlers branch on length>1 to route through bulk-ops.
export function dragPayload(rowPath: string, selection: ReadonlySet<string>): string[] {
  return selection.has(rowPath) ? Array.from(selection) : [rowPath];
}
