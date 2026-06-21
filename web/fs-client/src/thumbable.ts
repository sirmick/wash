// Which file extensions internal/thumbs can decode into a thumbnail. This
// MUST track the stdlib decoder registrations in internal/thumbs/thumbs.go
// (jpeg + the blank-imported image/png, image/gif). Formats outside this set
// (webp/svg/…) keep their file-type icon rather than render a broken
// thumbnail. Single source of truth for both fm's grid and the imageview list.

import { extName } from './paths.ts';

export const THUMBABLE_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif']);

// isThumbableName reports whether a file NAME (basename) is one thumbs can
// decode. Callers that also gate on file-vs-dir should AND this with their
// own type check (fm does: entry.type === 'file' && isThumbableName(name)).
export function isThumbableName(name: string): boolean {
  return THUMBABLE_EXTS.has(extName(name));
}
