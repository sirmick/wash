// The shell's document-wide clipboard handling (docs/CLIPBOARD.md):
//
//   - native copy/cut anywhere becomes the wash clipboard;
//   - a native paste goes through resolvePaste ("last change wins"), so a
//     Ctrl+V on this box can paste what was copied in wash on another;
//   - highlighting anywhere becomes the wash selection;
//   - the paste-selection gestures: middle-click, Alt+right-click, and
//     left+right pressed together.
//
// The terminal applies these rules itself (xterm keeps its own selection and
// paste path), so its surface is skipped here except where noted, and so is
// wash-display, whose input belongs to the guest app.

import {
  markSystemSeen,
  pasteSelectionAt,
  resolvePaste,
  washSelectionText,
  washSetSelection,
} from '@wash/ui';
import { insertText, type TextField } from './text-menu.tsx';

// CHORD_MS is how close together left and right must go down to count as
// one middle-click (X11's Emulate3Buttons uses 50ms).
export const CHORD_MS = 60;

const TEXT_INPUT_TYPES = new Set(['', 'text', 'search', 'url', 'tel', 'email', 'number']);

function el(t: EventTarget | null): HTMLElement | null {
  return t instanceof Element ? (t as HTMLElement) : null;
}

const inTerminal = (t: HTMLElement | null) => !!t?.closest?.('.xterm');
const inDisplay = (t: HTMLElement | null) => !!t?.closest?.('wash-app-display');
const mirrorHelper = (t: HTMLElement | null) => !!t?.closest?.('[data-wash-clipboard-mirror]');

// editableField resolves the text field an event landed in, if it can take
// text. Password fields take a paste but never give a selection.
function editableField(t: HTMLElement | null): TextField | null {
  const f = t?.closest?.('input, textarea') as TextField | null;
  if (!f || f.disabled || f.readOnly) return null;
  if (f.tagName === 'INPUT' && !TEXT_INPUT_TYPES.has((f as HTMLInputElement).type) && (f as HTMLInputElement).type !== 'password') return null;
  return f;
}

function editableHost(t: HTMLElement | null): HTMLElement | null {
  const h = t?.closest?.('[contenteditable=""], [contenteditable="true"]') as HTMLElement | null;
  return h?.isContentEditable ? h : null;
}

// selectedText reads what is highlighted for the event's target: a field's
// own range (Firefox keeps field selections out of the document selection),
// else the document selection. Password fields give nothing: their content
// must not reach a slot every app can read.
export function selectedText(t: HTMLElement | null): string {
  const f = (t?.closest?.('input, textarea') ?? document.activeElement) as HTMLInputElement | HTMLTextAreaElement | null;
  if (f && (f.tagName === 'INPUT' || f.tagName === 'TEXTAREA')) {
    if ((f as HTMLInputElement).type === 'password') return '';
    const { selectionStart: s, selectionEnd: e } = f;
    if (s != null && e != null && e > s) return f.value.slice(s, e);
    if (f === t?.closest?.('input, textarea')) return '';
  }
  return String(document.getSelection() ?? '');
}

// Synthetic pastes this module dispatches into rich editors, so the paste
// listener below doesn't run the rule on them a second time.
const ownPastes = new WeakSet<Event>();

// insertAt puts text where a paste at t would have gone: a text field via
// the field's undo-friendly insert; a rich editor (CodeMirror, ProseMirror)
// through a synthetic paste, so its own paste pipeline runs; else nothing.
export function insertAt(t: HTMLElement | null, text: string): boolean {
  const f = editableField(t);
  if (f) {
    insertText(f, text);
    return true;
  }
  const host = editableHost(t);
  if (!host) return false;
  host.focus();
  const dt = new DataTransfer();
  dt.setData('text/plain', text);
  const ev = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true });
  ownPastes.add(ev);
  if (host.dispatchEvent(ev)) document.execCommand('insertText', false, text);
  return true;
}

// pasteTarget: whether a paste-selection gesture at t has somewhere to go —
// a terminal, a text field, a rich editor. Elsewhere the buttons keep their
// own meaning (a middle-click closing a tab, an app's right-click menu).
function pasteTarget(t: HTMLElement | null): boolean {
  return inTerminal(t) || !!editableField(t) || !!editableHost(t);
}

// pasteSelection runs a paste-selection at t: a surface that inserts its own
// way (the terminal, the editor) takes the event; otherwise a field.
function pasteSelection(t: HTMLElement | null): void {
  const text = washSelectionText();
  if (!text || !t) return;
  if (pasteSelectionAt(t, text)) return;
  insertAt(t, text);
}

export function installClipboardGestures(): () => void {
  const now = () => performance.now();
  // A middle-click on Linux also fires the browser's own paste of the
  // desktop's primary selection; the wash selection replaces it.
  let suppressPasteUntil = 0;
  // After a chord, the menu and click its two buttons would otherwise raise.
  let suppressCtxUntil = 0;
  let suppressClickUntil = 0;
  const downAt = [-Infinity, -Infinity, -Infinity];
  // A plain right-click, held for CHORD_MS in case left joins it.
  let heldCtx: { timer: ReturnType<typeof setTimeout>; target: EventTarget; init: MouseEventInit } | null = null;
  const replayed = new WeakSet<Event>();

  const swallow = (ev: Event) => {
    ev.preventDefault();
    ev.stopImmediatePropagation();
  };

  const onCopy = (ev: ClipboardEvent) => {
    const t = el(ev.target);
    if (mirrorHelper(t)) return;
    const sel = selectedText(t);
    if (!sel) return;
    window.wash.clipboardSetText(sel);
    // The native copy goes on to write the system clipboard.
    markSystemSeen(sel);
  };

  const onPaste = (ev: ClipboardEvent) => {
    if (ownPastes.has(ev)) return;
    const t = el(ev.target);
    if (mirrorHelper(t)) return;
    if (now() < suppressPasteUntil) {
      swallow(ev);
      return;
    }
    if (inTerminal(t) || inDisplay(t)) return;
    const sys = ev.clipboardData?.getData('text/plain') ?? '';
    if (!sys) return;
    const text = resolvePaste(sys);
    if (text === sys) return;
    swallow(ev);
    insertAt(t, text);
  };

  // Highlighting: report on the gestures that make a selection — a mouse
  // release, or a key that extends one. Not on selectionchange, which also
  // fires for selections code makes (a rename field selecting the name).
  const reportSelection = (t: HTMLElement | null) => {
    if (!t || inTerminal(t) || inDisplay(t)) return;
    // After the browser has settled the selection for this gesture.
    setTimeout(() => {
      const text = selectedText(t);
      if (text) washSetSelection(text);
    }, 0);
  };
  const onMouseUp = (ev: MouseEvent) => {
    if (ev.button === 0) reportSelection(el(ev.target));
  };
  const onKeyUp = (ev: KeyboardEvent) => {
    const extends_ = ev.shiftKey || ((ev.ctrlKey || ev.metaKey) && (ev.key === 'a' || ev.key === 'A'));
    if (extends_) reportSelection(el(ev.target) ?? (document.activeElement as HTMLElement | null));
  };

  const chord = (ev: MouseEvent) => {
    swallow(ev);
    if (heldCtx) {
      clearTimeout(heldCtx.timer);
      heldCtx = null;
    }
    suppressCtxUntil = now() + 1000;
    suppressClickUntil = now() + 1000;
    pasteSelection(el(ev.target));
  };

  // mousedown, not pointerdown: a second button pressed while one is held
  // raises pointermove under Pointer Events, so a chord never shows there.
  const onMouseDown = (ev: MouseEvent) => {
    const t = el(ev.target);
    if (inDisplay(t)) return;
    const at = now();
    if (ev.button >= 0 && ev.button < 3) downAt[ev.button] = at;
    if (!pasteTarget(t)) return;
    if (ev.button === 1) {
      swallow(ev);
      suppressPasteUntil = at + 1000;
      pasteSelection(t);
      return;
    }
    if (ev.button === 0 || ev.button === 2) {
      const other = ev.button === 0 ? 2 : 0;
      if ((ev.buttons & 3) === 3 && at - downAt[other] < CHORD_MS) chord(ev);
    }
  };

  const onAuxClick = (ev: MouseEvent) => {
    if (ev.button === 1 && now() < suppressPasteUntil) swallow(ev);
  };
  const onClick = (ev: MouseEvent) => {
    if (now() < suppressClickUntil) {
      suppressClickUntil = 0;
      swallow(ev);
    }
  };

  const onContextMenu = (ev: MouseEvent) => {
    if (replayed.has(ev)) return;
    const t = el(ev.target);
    if (inDisplay(t)) return;
    if (now() < suppressCtxUntil) {
      swallow(ev);
      return;
    }
    // Shift is the browser's menu (text-menu.tsx yieldToBrowserMenu).
    if (ev.shiftKey) return;
    if (ev.altKey && pasteTarget(t)) {
      swallow(ev);
      pasteSelection(t);
      return;
    }
    // A plain right-click: Linux and macOS raise this as the right button
    // goes down, before a left press could make it a chord. Hold it that
    // long, then give it to whichever wash menu is there.
    if (!(ev.buttons & 2)) return; // raised on release (Windows): no chord to wait for
    // Only where a chord pastes; elsewhere the menu (the app's, or the
    // browser's where there is none) opens at once.
    if (!pasteTarget(t)) return;
    swallow(ev);
    const init: MouseEventInit = {
      bubbles: true, cancelable: true, composed: true,
      clientX: ev.clientX, clientY: ev.clientY, screenX: ev.screenX, screenY: ev.screenY,
      button: 2, buttons: ev.buttons,
      ctrlKey: ev.ctrlKey, metaKey: ev.metaKey,
    };
    const target = ev.target as EventTarget;
    heldCtx = {
      target,
      init,
      timer: setTimeout(() => {
        heldCtx = null;
        const replay = new MouseEvent('contextmenu', init);
        replayed.add(replay);
        target.dispatchEvent(replay);
      }, CHORD_MS),
    };
  };

  const opts = { capture: true } as const;
  document.addEventListener('copy', onCopy, opts);
  document.addEventListener('cut', onCopy, opts);
  document.addEventListener('paste', onPaste, opts);
  document.addEventListener('mouseup', onMouseUp, opts);
  document.addEventListener('keyup', onKeyUp, opts);
  document.addEventListener('mousedown', onMouseDown, opts);
  document.addEventListener('auxclick', onAuxClick, opts);
  document.addEventListener('click', onClick, opts);
  document.addEventListener('contextmenu', onContextMenu, opts);
  return () => {
    document.removeEventListener('copy', onCopy, opts);
    document.removeEventListener('cut', onCopy, opts);
    document.removeEventListener('paste', onPaste, opts);
    document.removeEventListener('mouseup', onMouseUp, opts);
    document.removeEventListener('keyup', onKeyUp, opts);
    document.removeEventListener('mousedown', onMouseDown, opts);
    document.removeEventListener('auxclick', onAuxClick, opts);
    document.removeEventListener('click', onClick, opts);
    document.removeEventListener('contextmenu', onContextMenu, opts);
  };
}
