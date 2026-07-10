// Clipboard helpers bridging the wash-internal clipboard (router-held,
// window.wash.clipboard*) and the browser's system clipboard.
//
// Two origins, two capability tiers:
//
// Secure context (HTTPS — the default since the TLS fronts landed —
// or localhost): navigator.clipboard has both halves. Copies mirror
// out via writeText; pastes PREFER readText, so text copied anywhere
// (an OS app, code-server's iframe, another browser profile) pastes
// correctly through wash's menus — the wash clipboard is the fallback,
// not a shadow world.
//
// Insecure context (plain HTTP on the LAN): navigator.clipboard is
// undefined. Writing OUT still works via execCommand('copy') inside a
// user gesture; reading IN is impossible programmatically — system
// text only enters wash through real paste gestures (Ctrl+V /
// browser-menu paste), whose events the shell mirrors into the wash
// clipboard.

// systemCopyText best-effort writes text to the SYSTEM clipboard.
// Must be called from within a user-gesture handler (click, mouseup,
// keydown) — Chrome rejects execCommand('copy') outside one. Returns
// whether the write was accepted.
//
// The textarea is stamped data-wash-clipboard-mirror so the shell's
// copy-event mirror ignores the synthetic copy this fires (the wash
// clipboard was the source; echoing it back would be a no-op set).
export function systemCopyText(text: string): boolean {
  if (navigator.clipboard?.writeText) {
    void navigator.clipboard.writeText(text).catch(() => { /* permission denied — best effort */ });
    return true;
  }
  return execCommandCopy(text);
}

// systemCopyTextChecked is systemCopyText that reports the TRUE
// outcome: where systemCopyText optimistically returns true as soon
// as navigator.clipboard exists, this resolves false when the browser
// rejects the write (clipboard-write permission blocked). Same
// user-gesture requirement — the copy is initiated synchronously,
// only the verdict is async. For UI that surfaces copy failures
// (sidebar ClipboardWidget); fire-and-forget callers keep the sync
// variant.
export function systemCopyTextChecked(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    return navigator.clipboard.writeText(text).then(() => true, () => false);
  }
  return Promise.resolve(execCommandCopy(text));
}

function execCommandCopy(text: string): boolean {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('data-wash-clipboard-mirror', '1');
  ta.setAttribute('readonly', '');
  // Off-screen but focusable; position:fixed avoids scroll jumps.
  ta.style.cssText = 'position:fixed;top:-200px;left:0;opacity:0';
  document.body.appendChild(ta);
  const prevActive = document.activeElement as HTMLElement | null;
  ta.select();
  let ok = false;
  try { ok = document.execCommand('copy'); } catch { ok = false; }
  ta.remove();
  prevActive?.focus?.();
  return ok;
}

// washCopyText is THE copy entry point for app code: stores text in
// the wash clipboard (so every app + the sidebar see it) and mirrors
// it to the system clipboard while the user gesture is still live.
export function washCopyText(text: string): void {
  if (!text) return;
  window.wash.clipboardSetText(text);
  systemCopyText(text);
}

// systemReadText best-effort reads the SYSTEM clipboard. Resolves ''
// when the API is missing (insecure context, Firefox <125), the user
// denied the clipboard-read permission, or the clipboard holds no
// text — every failure means "fall back to the wash clipboard".
function systemReadText(): Promise<string> {
  const c = navigator.clipboard;
  if (typeof c?.readText !== 'function') return Promise.resolve('');
  return c.readText().catch(() => '');
}

// washPasteText is THE paste entry point for app code (terminal
// right-click, editor menus). The system clipboard wins when it's
// readable and non-empty: it is the only clipboard that OS apps and
// secure-context embeds (code-server) write to, so preferring it makes
// Ctrl+V and menu-Paste resolve the same text. A successful system
// read is folded back into the wash clipboard so BE consumers (X apps
// via wash-display, the sidebar widget) converge on what was pasted.
// The wash clipboard remains the source of truth whenever the system
// side is unreadable.
export async function washPasteText(): Promise<string> {
  const sys = await systemReadText();
  const washText = await window.wash.clipboardGetText();
  if (sys) {
    if (sys !== washText) window.wash.clipboardSetText(sys);
    return sys;
  }
  return washText;
}
