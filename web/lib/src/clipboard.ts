// Clipboard helpers: the two router-held slots (window.wash.clipboard* and
// window.wash.selection*) and the browser's system clipboard. The model is
// docs/CLIPBOARD.md:
//
//   - the clipboard is what was deliberately copied (Ctrl+C, a menu's Copy);
//     Ctrl+V and a menu's Paste paste it;
//   - the selection is what was last highlighted; middle-click pastes it.
//     It never touches the system clipboard.
//
// The wash clipboard and each box's system clipboard are kept in step by
// "last change wins" (resolvePaste): each browser remembers the system
// text it last saw, so a paste can tell whether the system clipboard
// changed outside wash since (adopt it) or not (the wash clipboard is
// newer — paste that).
//
// Two capability tiers:
//
// Secure context (HTTPS or localhost): navigator.clipboard has both
// halves. Copies mirror out via writeText; a menu Paste can read.
//
// Insecure context (plain HTTP on the LAN): navigator.clipboard is
// undefined. Writing OUT still works via execCommand('copy') inside a
// user gesture; reading IN works only through a real paste (Ctrl+V),
// whose event carries the text.

// ---- what this browser last saw on the system clipboard ----

const SEEN_KEY = 'wash.clipboard.systemSeen';
// Memory copy for when localStorage is unavailable (private mode, tests).
let seenFallback: string | null = null;

/** systemSeen is the system clipboard text this browser last saw: what wash
 *  last wrote there, or what a paste last read. Shared by the tabs of one
 *  browser (localStorage), since they share one system clipboard. null when
 *  nothing has been seen. */
export function systemSeen(): string | null {
  try {
    const v = globalThis.localStorage?.getItem(SEEN_KEY);
    if (v !== undefined) return v;
  } catch { /* storage refused */ }
  return seenFallback;
}

/** markSystemSeen records text as what the system clipboard now holds. */
export function markSystemSeen(text: string): void {
  seenFallback = text;
  try {
    globalThis.localStorage?.setItem(SEEN_KEY, text);
  } catch { /* storage refused or full: the memory copy stands */ }
}

/** resolvePaste is the "last change wins" rule. sys is the system clipboard
 *  text a paste carries. If it differs from what this browser last saw
 *  there, something outside wash copied it since: it becomes the wash
 *  clipboard and is what to paste. Otherwise the wash clipboard is at least
 *  as new — copied in wash, perhaps on another box — so paste that.
 *  Returns the text to paste. */
export function resolvePaste(sys: string): string {
  const wash = window.wash.clipboardPeek();
  if (sys && sys !== systemSeen()) {
    markSystemSeen(sys);
    if (sys !== wash.text) window.wash.clipboardSetText(sys);
    return sys;
  }
  // Only text pastes: the files clipboard (fm) or an image holds nothing a
  // text paste can use, so the system text stands.
  if (wash.text && isTextMime(wash.mime)) return wash.text;
  return sys;
}

function isTextMime(mime: string): boolean {
  return mime === '' || mime.startsWith('text/');
}

// ---- writing the system clipboard ----

// systemCopyText best-effort writes text to the SYSTEM clipboard.
// Must be called from within a user-gesture handler (click, mouseup,
// keydown) — Chrome rejects execCommand('copy') outside one. Returns
// whether the write was accepted (optimistically, for the async API).
//
// The textarea is stamped data-wash-clipboard-mirror so the shell's
// copy-event mirror ignores the synthetic copy this fires (the wash
// clipboard was the source; echoing it back would be a no-op set).
export function systemCopyText(text: string): boolean {
  if (navigator.clipboard?.writeText) {
    void navigator.clipboard.writeText(text).then(() => markSystemSeen(text), () => { /* permission denied — best effort */ });
    return true;
  }
  const ok = execCommandCopy(text);
  if (ok) markSystemSeen(text);
  return ok;
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
    return navigator.clipboard.writeText(text).then(() => { markSystemSeen(text); return true; }, () => false);
  }
  const ok = execCommandCopy(text);
  if (ok) markSystemSeen(text);
  return Promise.resolve(ok);
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

// ---- the clipboard ----

// washCopyText is THE copy entry point for app code: stores text in
// the wash clipboard (so every app, tab and box sees it) and mirrors
// it to the system clipboard while the user gesture is still live.
export function washCopyText(text: string): void {
  if (!text) return;
  window.wash.clipboardSetText(text);
  systemCopyText(text);
}

// systemReadText best-effort reads the SYSTEM clipboard. Resolves null
// when it can't be read — the API is missing (insecure context, Firefox
// <125) or the user denied clipboard-read — so the caller knows to fall
// back to the wash clipboard rather than treat it as empty.
function systemReadText(): Promise<string | null> {
  const c = navigator.clipboard;
  if (typeof c?.readText !== 'function') return Promise.resolve(null);
  return c.readText().catch(() => null);
}

// washPasteText is THE programmatic paste entry point for app code (menu
// Paste, the terminal's api.paste). A native paste (Ctrl+V) carries its
// own text and goes through resolvePaste directly; this is for the paths
// that have to read. When the system clipboard is readable the "last
// change wins" rule picks between it and the wash clipboard; when it
// isn't, the wash clipboard is what there is.
export async function washPasteText(): Promise<string> {
  const sys = await systemReadText();
  if (sys !== null) return resolvePaste(sys);
  const wash = window.wash.clipboardPeek();
  return wash.text && isTextMime(wash.mime) ? wash.text : await window.wash.clipboardGetText();
}

// ---- the selection ----

/** washSetSelection records text as the last thing highlighted. Never
 *  touches the system clipboard. Repeats of the current value are dropped,
 *  so a caller can report on every mouseup. */
export function washSetSelection(text: string): void {
  if (!text || text === window.wash.selectionPeek().text) return;
  window.wash.selectionSetText(text);
}

/** washSelectionText is what a paste-selection pastes. */
export function washSelectionText(): string {
  return window.wash.selectionPeek().text;
}

// PASTE_SELECTION_EVENT is dispatched by the shell at the element a paste-
// selection gesture landed on (middle-click, Alt+right-click, left+right,
// or a menu's Paste selection). detail.text is the selection. A surface
// with its own way to insert (the terminal, the editor) handles it and
// calls preventDefault; otherwise the shell inserts into a text field.
export const PASTE_SELECTION_EVENT = 'wash:paste-selection';

export type PasteSelectionEvent = CustomEvent<{ text: string }>;

/** pasteSelectionAt dispatches a paste-selection at target, the same path
 *  the gestures take. Returns whether a surface took it. */
export function pasteSelectionAt(target: EventTarget, text = washSelectionText()): boolean {
  if (!text) return false;
  const ev: PasteSelectionEvent = new CustomEvent(PASTE_SELECTION_EVENT, {
    bubbles: true,
    composed: true,
    cancelable: true,
    detail: { text },
  });
  return !target.dispatchEvent(ev);
}
