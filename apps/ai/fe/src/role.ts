// One bundle, two surfaces: which one a mounted element is.
//
// The element's tag is the only answer that survives a browser reload — a
// BE message sent once at startup is not replayed to a remounted FE.

/**
 * isManagerElement: the Agents manager mounts as wash-app-agents, or, for a
 * remote host, under an origin-suffixed tag (wash-app-agents-<host>). The
 * controller's wash-app-ai can never match.
 */
export function isManagerElement(tagName: string): boolean {
  const t = tagName.toLowerCase();
  return t === 'wash-app-agents' || t.startsWith('wash-app-agents-');
}
