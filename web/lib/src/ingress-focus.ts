// Forward "the user interacted with me" from a same-origin embedded app
// (IngressFrame — code-server is consumer #1) up to the wash window
// manager, so clicking the embedded surface raises the window to the
// foreground like every other wash surface does.
//
// Context: a wash window is raised when a `pointerdown` reaches its
// frame (web/shell window.tsx onWindowPointerDown → focusWindow). An
// iframe swallows pointer/focus events — they fire inside the child
// document and never bubble across the frame boundary — so a click on
// the embedded editor never reaches the WM and a backgrounded VS Code
// window stays behind whatever is in front of it (#12).
//
// Because ingress is a SAME-ORIGIN embed (ingress-frame.tsx), the host
// can attach a listener to the child document and re-raise the signal on
// the host side. We listen in the CAPTURE phase so we see the
// interaction regardless of how the embedded app handles or stops it,
// and we never call preventDefault/stopPropagation — the embedded app's
// own click handling is untouched; we only observe.

// forward is invoked once per user interaction inside the embed (a
// pointerdown, or a focus landing in the frame — e.g. tabbing in). The
// host wires this to raise its window.
type ForwardFn = () => void;

// The interactions that mean "the user is now working in this window".
// pointerdown covers mouse/touch/pen; focusin covers keyboard focus
// arriving without a pointer (tab into the frame, programmatic focus).
const FOCUS_EVENTS = ['pointerdown', 'focusin'] as const;

// installIngressFocusBridge attaches capture-phase interaction listeners
// to a same-origin embedded window's document and calls `forward` when
// the user interacts inside it. Returns a cleanup function that detaches
// the listeners. It is a safe no-op — returns a no-op cleanup — when the
// window is cross-origin (document access throws), or when there's no
// window/document to observe. Idempotent per (win, forward): the same
// listener reference is registered, so a repeat call before cleanup does
// not stack duplicate handlers.
export function installIngressFocusBridge(
  win: Window | null | undefined,
  forward: ForwardFn,
): () => void {
  const noop = () => {};
  if (!win) return noop;
  try {
    const doc = win.document;
    if (!doc) return noop;
    const onInteract = () => forward();
    for (const type of FOCUS_EVENTS) {
      doc.addEventListener(type, onInteract, { capture: true });
    }
    return () => {
      for (const type of FOCUS_EVENTS) {
        doc.removeEventListener(type, onInteract, { capture: true });
      }
    };
  } catch {
    // Cross-origin window, or a document that refuses listeners —
    // nothing we can safely do; leave the embed as-is.
    return noop;
  }
}
