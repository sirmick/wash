// ClipboardWidget surfaces the wash clipboard (router-held, shared by
// every app) in the sidebar. Three jobs:
//   - make the clipboard visible: preview of the current text, so
//     "what will right-click paste?" is never a mystery;
//   - "Copy to system": push the wash clipboard out to the OS
//     clipboard — the button click supplies the user gesture the
//     browser demands for a programmatic system-clipboard write;
//   - paste-import box: the deliberate inbound path. On a plain-HTTP
//     origin the system clipboard can't be read programmatically, so
//     the user clicks the box and Ctrl+V's — the paste event carries
//     the text, which we fold into the wash clipboard.
//
// Quiet by design: no auto-expand on clipboard changes (every copy
// would pop the section open), no badge. The one standing exception:
// a warning line when the system bridge is degraded — insecure origin
// (no navigator.clipboard) or the browser refusing the write.

import type { Component, JSX } from 'solid-js';
import { Show, createSignal, onCleanup, onMount } from 'solid-js';
import { systemCopyTextChecked, tokens } from '@wash/ui';

export const ClipboardWidget: Component = () => {
  const [text, setText] = createSignal('');
  // Transient "✓ copied/imported" feedback; cleared after 1.5s.
  const [flash, setFlash] = createSignal('');
  let flashTimer: ReturnType<typeof setTimeout> | undefined;
  const showFlash = (msg: string) => {
    setFlash(msg);
    clearTimeout(flashTimer);
    flashTimer = setTimeout(() => setFlash(''), 1500);
  };

  onMount(() => {
    void window.wash.clipboardGetText().then(setText);
    const unsub = window.wash.onClipboardChanged((c) => setText(c.text));
    onCleanup(() => {
      unsub();
      clearTimeout(flashTimer);
    });
  });

  // Insecure origin (plain HTTP on the LAN): navigator.clipboard is
  // undefined, so outbound copy is execCommand best-effort and inbound
  // is paste-gesture only. Static per page load.
  const insecure = !window.isSecureContext;
  // Latched when a copy attempt actually fails (execCommand refused,
  // or clipboard-write permission denied on a secure origin); cleared
  // by the next success.
  const [copyBlocked, setCopyBlocked] = createSignal(false);

  const copyToSystem = () => {
    const t = text();
    if (!t) return;
    void systemCopyTextChecked(t).then((ok) => {
      setCopyBlocked(!ok);
      showFlash(ok ? 'copied to system' : 'copy blocked');
    });
  };

  const onImportPaste = (ev: ClipboardEvent) => {
    ev.preventDefault();
    const t = ev.clipboardData?.getData('text/plain') ?? '';
    if (!t) return;
    window.wash.clipboardSetText(t);
    setText(t);
    showFlash('imported');
  };

  const previewStyle: JSX.CSSProperties = {
    font: `11px ${tokens.fontMono}`,
    color: text() ? tokens.fg : tokens.fgDim,
    background: tokens.bgInset,
    border: `1px solid ${tokens.borderMenu}`,
    'border-radius': `${tokens.radiusSm}px`,
    padding: '6px 8px',
    'max-height': '72px',
    overflow: 'hidden',
    'white-space': 'pre-wrap',
    'word-break': 'break-all',
  };

  const btnStyle: JSX.CSSProperties = {
    background: 'transparent',
    color: tokens.fg,
    border: `1px solid ${tokens.borderMenu}`,
    'border-radius': `${tokens.radiusSm}px`,
    padding: '3px 8px',
    cursor: 'pointer',
    font: `11px ${tokens.fontSans}`,
  };

  const importStyle: JSX.CSSProperties = {
    flex: 1,
    background: tokens.bgInset,
    color: tokens.fgDim,
    border: `1px dashed ${tokens.borderMenu}`,
    'border-radius': `${tokens.radiusSm}px`,
    padding: '3px 8px',
    font: `11px ${tokens.fontSans}`,
    'min-width': '0',
  };

  // Preview truncated for display; the clipboard itself is untouched.
  const preview = () => {
    const t = text();
    if (!t) return '(empty)';
    return t.length > 280 ? `${t.slice(0, 280)}…` : t;
  };

  return (
    <div data-testid="clipboard-widget" style={{ display: 'flex', 'flex-direction': 'column', gap: '6px' }}>
      <div data-testid="clipboard-preview" style={previewStyle}>{preview()}</div>
      <div style={{ display: 'flex', gap: '6px', 'align-items': 'center' }}>
        <button
          type="button"
          data-testid="clipboard-copy-system"
          title="Write the wash clipboard to this machine's clipboard"
          onClick={copyToSystem}
          style={btnStyle}
        >
          Copy to system
        </button>
        <input
          data-testid="clipboard-import"
          placeholder="Click + Ctrl+V to import"
          title="Import this machine's clipboard into wash: click here, then paste"
          value=""
          onPaste={onImportPaste}
          onInput={(ev) => { ev.currentTarget.value = ''; }}
          style={importStyle}
        />
      </div>
      <Show when={flash()}>
        <div data-testid="clipboard-flash" style={{ font: `11px ${tokens.fontSans}`, color: tokens.fgDim }}>
          ✓ {flash()}
        </div>
      </Show>
      <Show when={copyBlocked() || insecure}>
        <div
          data-testid="clipboard-warning"
          style={{ font: `11px ${tokens.fontSans}`, color: tokens.fgWarning }}
        >
          {copyBlocked()
            ? '⚠ system copy blocked by the browser'
            : '⚠ no HTTPS — system copy is best-effort; import only via the paste box'}
        </div>
      </Show>
    </div>
  );
};
