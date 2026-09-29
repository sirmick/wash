// The wash context menu for text fields — Cut / Copy / Paste / Select all
// on any <input> or <textarea>, in place of the browser's own menu.
//
// It lives in the SHELL, on one document-level listener, rather than in a
// component apps opt into: there are ~85 raw <input>/<textarea> across the
// apps and only about a third go through @wash/ui's <Input>, so a component
// would have covered the minority and drifted from there. Apps are light DOM
// (defineWashApp renders into the element itself; nothing in the repo calls
// attachShadow), so every field's contextmenu reaches document — the same
// property the shell's copy/cut/paste clipboard mirrors already rely on.
//
// BUBBLE phase, and skipped when the event is already defaultPrevented, so an
// app that wants its own menu on a field simply handles it first and wins.
// That is the opposite choice from the clipboard mirrors (capture, because
// xterm stops propagation on its textarea) and is deliberate: a mirror must
// see everything, whereas a menu should yield to a more specific one.
//
// Not shown for:
//   - xterm's helper textarea (.xterm) — the terminal has its own menu, with
//     PuTTY-style plain-right-click behaviour this would preempt.
//   - <wash-app-display> — right-click there belongs to the VM guest, which
//     is why that surface suppresses the native menu itself.
//   - non-text inputs (checkbox, range, file, colour …) — nothing to edit.
// Content inside an <iframe> (the ingress frames) never reaches us at all:
// its contextmenu does not cross the boundary, so the embedded app keeps its
// own menu without any check here.

import { Show, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component } from 'solid-js';
import { Menu, MenuItem, MenuSeparator, washCopyText, washPasteText } from '@wash/ui';

export type TextField = HTMLInputElement | HTMLTextAreaElement;

// The <input> types that hold editable text. An empty type is "text".
const TEXT_INPUT_TYPES = new Set(['', 'text', 'search', 'url', 'tel', 'email', 'password', 'number']);

// fieldFromEvent resolves the text field a contextmenu landed in, or null
// when this menu should stay out of the way.
export function fieldFromEvent(ev: MouseEvent): TextField | null {
  if (ev.defaultPrevented) return null;
  const t = ev.target as HTMLElement | null;
  if (!t?.closest) return null;
  if (t.closest('.xterm, wash-app-display')) return null;
  const el = t.closest('input, textarea') as TextField | null;
  if (!el || el.disabled) return null;
  if (el.tagName === 'INPUT' && !TEXT_INPUT_TYPES.has((el as HTMLInputElement).type)) return null;
  return el;
}

// insertText replaces the field's selection, preferring execCommand so the
// edit joins the field's native undo stack and fires `input` on its own —
// which controlled Solid fields need to see. The manual splice is the
// fallback for where execCommand is refused; it dispatches `input` itself.
function insertText(el: TextField, text: string): void {
  el.focus();
  if (document.execCommand('insertText', false, text)) return;
  const s = el.selectionStart ?? el.value.length;
  const e = el.selectionEnd ?? s;
  el.value = el.value.slice(0, s) + text + el.value.slice(e);
  const caret = s + text.length;
  el.setSelectionRange(caret, caret);
  el.dispatchEvent(new Event('input', { bubbles: true }));
}

// deleteSelection removes the selected range, same undo/`input` reasoning.
function deleteSelection(el: TextField, start: number, end: number): void {
  el.focus();
  el.setSelectionRange(start, end);
  if (document.execCommand('delete')) return;
  el.value = el.value.slice(0, start) + el.value.slice(end);
  el.setSelectionRange(start, start);
  el.dispatchEvent(new Event('input', { bubbles: true }));
}

export const TextFieldMenu: Component<{
  x: number;
  y: number;
  el: TextField;
  onDismiss: () => void;
}> = (props) => {
  // The selection is read ONCE, as the menu opens. Pressing a menu item
  // moves focus out of the field and the browser drops its selection, so by
  // the time a handler runs there is nothing left to read — every action
  // below restores this range first.
  const el = props.el;
  const start = el.selectionStart ?? 0;
  const end = el.selectionEnd ?? 0;
  const selected = el.value.slice(start, end);
  const readOnly = el.readOnly;
  // A password field's contents must not reach a clipboard every app can
  // read — the shell already refuses to mirror them (nativeCopySelection).
  // Pasting INTO one is fine, and is the case that matters.
  const secret = el.tagName === 'INPUT' && (el as HTMLInputElement).type === 'password';
  const canCopy = !!selected && !secret;

  const restore = () => {
    el.focus();
    try {
      el.setSelectionRange(start, end);
    } catch {
      // A field whose type stopped supporting selection between open and
      // click; focus alone is still the right outcome.
    }
  };
  const done = (fn: () => void) => {
    props.onDismiss();
    fn();
  };

  // Escape closes and hands the field back. Menu itself has no key
  // handling, and a menu that can only be dismissed by clicking elsewhere
  // costs the caret position the user was working at.
  onMount(() => {
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key !== 'Escape') return;
      ev.preventDefault();
      done(restore);
    };
    document.addEventListener('keydown', onKey, true);
    onCleanup(() => document.removeEventListener('keydown', onKey, true));
  });

  return (
    <Menu x={props.x} y={props.y} onDismiss={() => done(restore)} data-testid="text-context-menu">
      <MenuItem
        label="Cut"
        disabled={!canCopy || readOnly}
        data-testid="text-ctx-cut"
        onClick={() => done(() => {
          washCopyText(selected);
          deleteSelection(el, start, end);
        })}
      />
      <MenuItem
        label="Copy"
        disabled={!canCopy}
        data-testid="text-ctx-copy"
        onClick={() => done(() => {
          washCopyText(selected);
          restore();
        })}
      />
      <MenuItem
        label="Paste"
        disabled={readOnly}
        data-testid="text-ctx-paste"
        onClick={() => done(() => {
          restore();
          // Async: the system clipboard read may need a round trip, and on
          // an insecure origin falls back to the wash clipboard.
          void washPasteText().then((text) => {
            if (text) insertText(el, text);
          });
        })}
      />
      <MenuSeparator />
      <MenuItem
        label="Select all"
        disabled={!el.value}
        data-testid="text-ctx-select-all"
        onClick={() => done(() => {
          el.focus();
          el.select();
        })}
      />
    </Menu>
  );
};

// TextMenuLayer is the shell-side host: one listener, one menu at a time.
export const TextMenuLayer: Component = () => {
  const [open, setOpen] = createSignal<{ x: number; y: number; el: TextField } | null>(null);
  onMount(() => {
    const onCtx = (ev: MouseEvent) => {
      const el = fieldFromEvent(ev);
      if (!el) return;
      ev.preventDefault();
      setOpen({ x: ev.clientX, y: ev.clientY, el });
    };
    document.addEventListener('contextmenu', onCtx);
    onCleanup(() => document.removeEventListener('contextmenu', onCtx));
  });
  return (
    <Show when={open()}>
      {(o) => <TextFieldMenu x={o().x} y={o().y} el={o().el} onDismiss={() => setOpen(null)} />}
    </Show>
  );
};
