// @wash/fs-client — framework-free (no DOM, no Solid) client-side logic
// shared by the file-manager-shaped apps (fm, and edit in Phase 6).
// Everything here is unit-tested with node:test (see the sibling
// *.test.ts files) and bundled into each consumer's dist; it is NOT a
// vendor module (no shared-singleton or large-dep reason to externalize).
//
// Extracted from fm's App() monolith — see docs/FE_REFACTOR_PLAN.md.

export {
  joinPath,
  parentPath,
  baseName,
  ancestorChain,
  humanSize,
  formatDate,
  octalPerm,
} from './paths.ts';

export { createBus } from './bus.ts';
export type { Bus, BEMessage, Clock } from './bus.ts';

// createWatch's WatchOptions takes the same Clock shape as the bus; it
// is re-exported once above (from ./bus.ts) to avoid an ambiguous
// duplicate-name star export.
export { createWatch } from './watch.ts';
export type { Watch, WatchOptions } from './watch.ts';

export {
  DRAG_MIME,
  readDragPaths,
  hasWashDrag,
  dropEffectFor,
  dragPayload,
} from './dnd.ts';
export type { DragData } from './dnd.ts';

export { sortedFiltered } from './sort.ts';
export type { SortKey, SortableEntry, SortOptions } from './sort.ts';

export { withReplacePrompt } from './replace-flow.ts';
export type { ReplaceFlowDeps } from './replace-flow.ts';

export { flattenTree } from './tree.ts';
export type { TreeRow, TreeInput } from './tree.ts';
