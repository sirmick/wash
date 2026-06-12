// Clipboard helpers bridging the wash-internal clipboard (router-held,
// window.wash.clipboard*) and the browser's system clipboard.
//
// The asymmetry these helpers encode: wash is usually served over
// plain HTTP on the LAN, an insecure context where navigator.clipboard
// is undefined. Writing OUT to the system clipboard still works via
// execCommand('copy') inside a user gesture; reading IN is impossible
// programmatically — system text only enters wash through real paste
// gestures (Ctrl+V / browser-menu paste), whose events carry the text.
//
// Where navigator.clipboard exists (localhost today, HTTPS later) the
// async API is preferred automatically.

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

// washPasteText resolves the wash clipboard's current text. Plain
// wrapper today; the indirection point for richer mimes later.
export function washPasteText(): Promise<string> {
  return window.wash.clipboardGetText();
}
