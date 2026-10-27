// ClipboardWidget is the sidebar's Clipboard pane (docs/CLIPBOARD.md): the
// two router-held slots, the break glass for when the browser won't reach
// this computer's clipboard on its own, and the bindings.
//
//   - Clipboard: what was copied (Ctrl+C, a menu's Copy) — text, an image,
//     or fm's files — with where it came from. "Copy to this computer"
//     pushes it out (the click supplies the gesture the browser demands);
//     the import box is the way in where it can't be read (plain HTTP):
//     click it and Ctrl+V.
//   - Selection: what was last highlighted, pasted by middle-click.
//   - Status: one line, only when something is wrong.
//   - Shortcuts: folded, from the same table the menus' hints use.
//
// Quiet by design: no auto-expand on clipboard changes (every copy would
// pop the section open), no badge.

import type { Component, JSX } from 'solid-js';
import { For, Show, createSignal, onCleanup, onMount } from 'solid-js';
import { Button, CLIPBOARD_BINDINGS, Input, TERMINAL_BINDINGS, markSystemSeen, systemCopyTextChecked, tokens } from '@wash/ui';

const PATHS_MIME = 'application/x-wash-paths';

// ago is a coarse age: "just now", "4 min ago", "2 h ago".
function ago(at: number | undefined, now: number): string {
  if (!at) return '';
  const s = Math.max(0, Math.round((now - at) / 1000));
  if (s < 30) return 'just now';
  if (s < 3600) return `${Math.max(1, Math.round(s / 60))} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
}

// describeFiles reads fm's files clipboard ({op, paths}).
function describeFiles(text: string): string {
  try {
    const { op, paths } = JSON.parse(text) as { op?: string; paths?: string[] };
    const n = paths?.length ?? 0;
    const dir = n ? paths![0].replace(/\/[^/]*\/?$/, '') || '/' : '';
    return `${n} file${n === 1 ? '' : 's'} · ${op === 'cut' ? 'cut' : 'copied'}${dir ? ` · from ${dir}` : ''}`;
  } catch {
    return 'files';
  }
}

function sizeLabel(n: number | undefined): string {
  if (!n) return '';
  return n < 1024 * 1024 ? `${Math.max(1, Math.round(n / 1024))} KB` : `${(n / 1024 / 1024).toFixed(1)} MB`;
}

export const ClipboardWidget: Component = () => {
  const [clip, setClip] = createSignal<WashClipEntry>(window.wash.clipboardPeek());
  const [sel, setSel] = createSignal<WashClipEntry>(window.wash.selectionPeek());
  const [image, setImage] = createSignal<string | null>(null);
  const [now, setNow] = createSignal(Date.now());
  const [showKeys, setShowKeys] = createSignal(false);
  // Transient "✓ copied/imported" feedback; cleared after 1.5s.
  const [flash, setFlash] = createSignal('');
  let flashTimer: ReturnType<typeof setTimeout> | undefined;
  const showFlash = (msg: string) => {
    setFlash(msg);
    clearTimeout(flashTimer);
    flashTimer = setTimeout(() => setFlash(''), 1500);
  };

  // An image's bytes are not pushed with the change notice; fetch them for
  // the thumbnail. The object URL is replaced, and revoked, on every change.
  const showImage = (c: WashClipEntry) => {
    const prev = image();
    if (prev) URL.revokeObjectURL(prev);
    setImage(null);
    if (!c.mime.startsWith('image/')) return;
    void window.wash.clipboardGet().then((full) => {
      if (full.at !== clip().at || !full.data) return;
      setImage(URL.createObjectURL(new Blob([full.data as Uint8Array<ArrayBuffer>], { type: full.mime })));
    });
  };

  // pristine latches false on the first live change. The mount-time fetch
  // is an async round-trip; under load it can resolve AFTER the user has
  // already pasted into the import box, so it must not clobber that newer
  // value with the one read at mount.
  let pristine = true;
  const takeClip = (c: WashClipEntry) => {
    pristine = false;
    setClip(c);
    showImage(c);
  };

  onMount(() => {
    void window.wash.clipboardGet().then((c) => {
      if (!pristine) return;
      const { data: _data, ...entry } = c;
      setClip(entry);
      showImage(entry);
    });
    const unsubClip = window.wash.onClipboardChanged(takeClip);
    const unsubSel = window.wash.onSelectionChanged(setSel);
    // The ages tick over while the pane is open.
    const tick = setInterval(() => setNow(Date.now()), 30_000);
    onCleanup(() => {
      unsubClip();
      unsubSel();
      clearInterval(tick);
      clearTimeout(flashTimer);
      const url = image();
      if (url) URL.revokeObjectURL(url);
    });
  });

  // Insecure origin (plain HTTP on the LAN): navigator.clipboard is
  // undefined, so a menu Paste can't read this computer's clipboard and
  // changes from elsewhere can't be written out. Static per page load.
  const insecure = !window.isSecureContext;
  // Latched when a copy attempt actually fails (execCommand refused, or
  // clipboard-write permission denied); cleared by the next success.
  const [copyBlocked, setCopyBlocked] = createSignal(false);

  const isText = (c: WashClipEntry) => c.mime === '' || c.mime.startsWith('text/');

  const copyToComputer = () => {
    const t = clip().text;
    if (!t || !isText(clip())) return;
    void systemCopyTextChecked(t).then((ok) => {
      setCopyBlocked(!ok);
      showFlash(ok ? 'copied to this computer' : 'copy blocked');
    });
  };

  const onImportPaste = (ev: ClipboardEvent) => {
    ev.preventDefault();
    const t = ev.clipboardData?.getData('text/plain') ?? '';
    if (!t) return;
    // This is this computer's clipboard, read in full: the rule's memory
    // of it is current now.
    markSystemSeen(t);
    window.wash.clipboardSetText(t);
    takeClip(window.wash.clipboardPeek());
    showFlash('imported');
  };

  const selectionToClipboard = () => {
    const t = sel().text;
    if (!t) return;
    window.wash.clipboardSetText(t);
    takeClip(window.wash.clipboardPeek());
    void systemCopyTextChecked(t).then((ok) => setCopyBlocked(!ok));
    showFlash('selection copied');
  };

  const clearClipboard = () => {
    window.wash.clipboardSetText('');
    takeClip(window.wash.clipboardPeek());
  };
  const clearSelection = () => {
    window.wash.selectionSetText('');
    setSel(window.wash.selectionPeek());
  };

  const previewStyle = (empty: boolean): JSX.CSSProperties => ({
    font: tokens.type.monoSm,
    color: empty ? tokens.fgDim : tokens.fg,
    background: tokens.bgInset,
    border: `1px solid ${tokens.borderMenu}`,
    'border-radius': `${tokens.radiusSm}`,
    padding: '6px 8px',
    'max-height': '72px',
    overflow: 'hidden',
    'white-space': 'pre-wrap',
    'word-break': 'break-all',
  });
  const headStyle: JSX.CSSProperties = {
    display: 'flex',
    'justify-content': 'space-between',
    'align-items': 'baseline',
    gap: '6px',
    font: tokens.type.textSm,
    color: tokens.fgDim,
  };
  const labelStyle: JSX.CSSProperties = { color: tokens.fg, 'font-weight': '600' };
  const metaStyle: JSX.CSSProperties = { overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' };
  // Override layer atop <Button variant="ghost"> (transparent fill, border,
  // radius, cursor, fg) to keep the compact small-text footprint.
  const btnStyle: JSX.CSSProperties = { font: tokens.type.textSm, padding: '3px 8px' };
  // Override layer atop <Input> (sunken bgInset fill, radius) — the dashed
  // border + dim text mark this as the deliberate paste-import affordance.
  const importStyle: JSX.CSSProperties = {
    flex: 1,
    color: tokens.fgDim,
    border: `1px dashed ${tokens.borderMenu}`,
    padding: '3px 8px',
    font: tokens.type.textSm,
    'min-width': '0',
  };
  const rowStyle: JSX.CSSProperties = { display: 'flex', gap: '6px', 'align-items': 'center' };

  // Preview truncated for display; the slots themselves are untouched.
  const textPreview = (t: string) => (t.length > 280 ? `${t.slice(0, 280)}…` : t);
  const clipPreview = () => {
    const c = clip();
    if (c.mime === PATHS_MIME) return describeFiles(c.text);
    if (c.mime.startsWith('image/')) return image() ? '' : `image · ${sizeLabel(c.size)}`;
    if (!c.text) return '(empty)';
    return textPreview(c.text);
  };
  const meta = (c: WashClipEntry) => [c.source, ago(c.at, now())].filter(Boolean).join(' · ');

  const keyRow = (b: { keys: string; does: string }) => (
    <div style={{ display: 'flex', gap: '8px', font: tokens.type.textSm }}>
      <span style={{ color: tokens.fg, 'min-width': '0', flex: '0 0 46%' }}>{b.keys}</span>
      <span style={{ color: tokens.fgDim }}>{b.does}</span>
    </div>
  );

  return (
    <div data-testid="clipboard-widget" style={{ display: 'flex', 'flex-direction': 'column', gap: '6px' }}>
      <div style={headStyle}>
        <span style={labelStyle}>Clipboard</span>
        <span data-testid="clipboard-source" style={metaStyle}>{meta(clip())}</span>
      </div>
      <div data-testid="clipboard-preview" style={previewStyle(!clip().text && !clip().size)}>
        <Show when={image()} fallback={clipPreview()}>
          {(url) => <img data-testid="clipboard-image" src={url()} alt="clipboard image" style={{ 'max-width': '100%', 'max-height': '60px', display: 'block' }} />}
        </Show>
      </div>
      <div style={rowStyle}>
        <Button
          variant="ghost"
          data-testid="clipboard-copy-system"
          title="Write the wash clipboard to this computer's clipboard"
          disabled={!clip().text || !isText(clip())}
          onClick={copyToComputer}
          style={btnStyle}
        >
          Copy to this computer
        </Button>
        <Button variant="ghost" data-testid="clipboard-clear" title="Empty the wash clipboard" onClick={clearClipboard} style={btnStyle}>
          Clear
        </Button>
      </div>
      <Input
        data-testid="clipboard-import"
        // The shell's paste handling leaves clipboard helpers alone: this
        // box imports exactly what was pasted.
        data-wash-clipboard-mirror="1"
        placeholder="Click, then Ctrl+V, to bring this computer's clipboard in"
        title="Import this computer's clipboard into wash: click here, then paste"
        value=""
        onPaste={onImportPaste}
        onInput={(ev) => { ev.currentTarget.value = ''; }}
        style={importStyle}
      />

      <div style={{ ...headStyle, 'margin-top': '4px' }}>
        <span style={labelStyle} title="What you last highlighted. Middle-click, Alt+right-click or left+right pastes it.">Selection</span>
        <span data-testid="selection-source" style={metaStyle}>{meta(sel())}</span>
      </div>
      <div data-testid="selection-preview" style={previewStyle(!sel().text)}>
        {sel().text ? textPreview(sel().text) : '(nothing highlighted)'}
      </div>
      <div style={rowStyle}>
        <Button variant="ghost" data-testid="selection-copy" title="Put the selection on the clipboard" disabled={!sel().text} onClick={selectionToClipboard} style={btnStyle}>
          Copy to clipboard
        </Button>
        <Button variant="ghost" data-testid="selection-clear" title="Forget the selection" disabled={!sel().text} onClick={clearSelection} style={btnStyle}>
          Clear
        </Button>
      </div>

      <Show when={flash()}>
        <div data-testid="clipboard-flash" style={{ font: tokens.type.textSm, color: tokens.fgDim }}>
          ✓ {flash()}
        </div>
      </Show>
      <Show when={copyBlocked() || insecure}>
        <div data-testid="clipboard-warning" style={{ font: tokens.type.textSm, color: tokens.fgWarning }}>
          {copyBlocked()
            ? '⚠ the browser blocked writing this computer\'s clipboard — allow clipboard access for this site'
            : '⚠ no HTTPS — Ctrl+C and Ctrl+V still work; menus can\'t read this computer\'s clipboard, use the box above'}
        </div>
      </Show>

      <Button
        variant="ghost"
        data-testid="clipboard-shortcuts-toggle"
        onClick={() => setShowKeys((v) => !v)}
        style={{ ...btnStyle, 'align-self': 'flex-start', 'margin-top': '2px' }}
      >
        {showKeys() ? '▾' : '▸'} Shortcuts
      </Button>
      <Show when={showKeys()}>
        <div data-testid="clipboard-shortcuts" style={{ display: 'flex', 'flex-direction': 'column', gap: '3px' }}>
          <For each={CLIPBOARD_BINDINGS}>{keyRow}</For>
          <div style={{ font: tokens.type.textSm, color: tokens.fgDim, 'margin-top': '4px' }}>In a terminal</div>
          <For each={TERMINAL_BINDINGS}>{keyRow}</For>
        </div>
      </Show>
    </div>
  );
};
