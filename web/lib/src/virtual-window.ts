// Pure windowing math for VirtualGrid — kept in its own (JSX-free) module so
// it's importable from node --test without a JSX transform. See virtual-grid.tsx.

// VirtualWindow is the computed slice of a virtualized grid: which rows/items
// to mount and the spacer height.
export interface VirtualWindow {
  cols: number;
  rowH: number;
  totalRows: number;
  totalH: number;
  startRow: number;
  endRow: number;
  startIndex: number;
  endIndex: number;
}

export interface VirtualWindowInput {
  count: number;
  tileWidth: number;
  tileHeight: number;
  gap: number;
  overscan: number;
  vw: number;
  vh: number;
  scrollTop: number;
}

export function computeVirtualWindow(o: VirtualWindowInput): VirtualWindow {
  // Column count from the content-box width (>=1 even before measurement).
  const cols = Math.max(1, Math.floor((o.vw + o.gap) / (o.tileWidth + o.gap)));
  const rowH = o.tileHeight + o.gap;
  const totalRows = Math.ceil(o.count / cols);
  // Spacer height: full grid minus the trailing gap (none after the last row).
  const totalH = Math.max(0, totalRows * rowH - o.gap);
  const startRow = Math.max(0, Math.floor(o.scrollTop / rowH) - o.overscan);
  const endRow = Math.min(totalRows, Math.ceil((o.scrollTop + o.vh) / rowH) + o.overscan);
  return { cols, rowH, totalRows, totalH, startRow, endRow, startIndex: startRow * cols, endIndex: endRow * cols };
}
