// Bridge the wash clipboard into a same-origin embedded app (IngressFrame)
// whose browser withheld a working navigator.clipboard.
//
// Context: wash usually runs over plain HTTP on the LAN — an insecure
// context where navigator.clipboard is undefined (see clipboard.ts). The
// shell and every wash app route around that through the wash clipboard,
// but an embedded third-party web app (code-server is consumer #1) uses
// the browser Clipboard API directly. On the insecure origin its
// copy/paste — including the built-in right-click menu — silently fails,
// because navigator.clipboard.readText/writeText simply aren't there (#9).
//
// Because ingress is a SAME-ORIGIN embed (ingress-frame.tsx), the host can
// reach into the iframe's window and define navigator.clipboard on it,
// backed by the wash clipboard. No proxy HTML rewrite, no eval, no script
// injection — so the embedded app's CSP is untouched: we just set an own
// property on the child window's own navigator object.

import { washCopyText, washPasteText } from './clipboard.ts';

// The text half of the DOM Clipboard interface — all Monaco/VS Code (and
// typical web apps) reach for on copy/paste.
interface ClipboardTextApi {
  readText(): Promise<string>;
  writeText(text: string): Promise<void>;
}

// hasWorkingClipboard reports whether nav already exposes the async text
// API — true in a secure context (https / localhost), where we must NOT
// shadow the real system clipboard: the wash apps already mirror to it, so
// the embedded app and wash interoperate through the OS clipboard as-is.
function hasWorkingClipboard(nav: Navigator | undefined): boolean {
  const c = (nav as { clipboard?: Partial<ClipboardTextApi> } | undefined)?.clipboard;
  return typeof c?.readText === 'function' && typeof c?.writeText === 'function';
}

// installIngressClipboardBridge defines navigator.clipboard on the embedded
// window when the browser withheld it (insecure origin), routing
// readText/writeText through the wash clipboard so the embedded app's
// copy/paste behaves like every other wash surface. It is a no-op —
// returns false — when a real clipboard already exists (secure context),
// when the window is cross-origin (property access throws), or when there's
// no window/navigator to patch. Idempotent: safe to call again on reload.
export function installIngressClipboardBridge(win: Window | null | undefined): boolean {
  if (!win) return false;
  try {
    const nav = win.navigator;
    if (!nav || hasWorkingClipboard(nav)) return false;
    const bridge: ClipboardTextApi = {
      readText: () => washPasteText(),
      writeText: (text: string) => {
        washCopyText(text);
        return Promise.resolve();
      },
    };
    Object.defineProperty(nav, 'clipboard', {
      value: bridge,
      configurable: true,
      enumerable: false,
    });
    return true;
  } catch {
    // Cross-origin window, or a navigator that refuses the definition —
    // nothing we can safely do; leave the embed as-is.
    return false;
  }
}
