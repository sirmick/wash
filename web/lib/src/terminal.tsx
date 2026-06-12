// Shared xterm.js wrapper. Owns: xterm + FitAddon construction,
// raw-channel I/O wiring, pre-mount byte buffer, ResizeObserver
// refit, live font (family/size) application, and a right-click
// copy/paste/font context menu. Consumers own: tab/pane
// orchestration, focus policy, keyboard shortcuts (passed in),
// backend messaging, and persistence of the font choice (fed back
// in via the fontId/fontSize props + onFont* callbacks).
//
// xterm and addon-fit are externalized to the shared vendor bundle
// (web/shell/build-vendor.mjs); consumers' vite configs already
// include both names in `rollupOptions.external`.

import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import type { ITheme } from '@xterm/xterm';
import { For, Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';

import { tokens } from './tokens';
import { washAssetUrl } from './assets';
import { Menu, MenuItem, MenuSeparator } from './menu';

// ---- font registry ----

export interface TermFont {
  id: string;
  label: string;
  // CSS font stack handed to xterm. For bundled fonts the first
  // family is the FontFace we register; the rest are fallbacks shown
  // until the woff2 finishes loading.
  stack: string;
  // family name + woff2 URLs (relative, run through washAssetUrl) for
  // bundled fonts. Omitted for the system stack — nothing to load.
  family?: string;
  url?: string;
  boldUrl?: string;
}

// The curated picker list. System Mono needs no download; the other
// two are bundled woff2 under web/shell/public/fonts (served at
// /fonts/*). xterm's canvas renderer ignores ligatures, so these are
// chosen for their glyph shapes, not Fira/JetBrains ligature sets.
export const TERM_FONTS: TermFont[] = [
  { id: 'system', label: 'System Mono', stack: tokens.fontMono },
  {
    id: 'jetbrains-mono',
    label: 'JetBrains Mono',
    stack: '"JetBrains Mono", ui-monospace, Menlo, Consolas, monospace',
    family: 'JetBrains Mono',
    url: 'fonts/JetBrainsMono-Regular.woff2',
    boldUrl: 'fonts/JetBrainsMono-Bold.woff2',
  },
  {
    id: 'fira-code',
    label: 'Fira Code',
    stack: '"Fira Code", ui-monospace, Menlo, Consolas, monospace',
    family: 'Fira Code',
    url: 'fonts/FiraCode-Regular.woff2',
    boldUrl: 'fonts/FiraCode-Bold.woff2',
  },
];

export const TERM_DEFAULT_FONT_ID = 'system';
export const TERM_DEFAULT_FONT_SIZE = 12;
export const TERM_MIN_FONT_SIZE = 9;
export const TERM_MAX_FONT_SIZE = 20;

export function fontById(id: string | undefined): TermFont {
  return TERM_FONTS.find((f) => f.id === id) ?? TERM_FONTS[0];
}

const clampSize = (px: number) =>
  Math.max(TERM_MIN_FONT_SIZE, Math.min(TERM_MAX_FONT_SIZE, Math.round(px)));

// Module-level so every terminal shares one load per font. The id is
// added optimistically before awaiting to dedupe concurrent loads;
// removed on failure so a later selection can retry.
const loadedFonts = new Set<string>();
async function ensureFontLoaded(f: TermFont): Promise<void> {
  if (!f.url || !f.family || loadedFonts.has(f.id)) return;
  if (typeof document === 'undefined' || !('fonts' in document)) return;
  loadedFonts.add(f.id);
  try {
    const faces = [new FontFace(f.family, `url(${washAssetUrl(f.url)})`, { weight: '400' })];
    if (f.boldUrl) {
      faces.push(new FontFace(f.family, `url(${washAssetUrl(f.boldUrl)})`, { weight: '700' }));
    }
    await Promise.all(faces.map((face) => face.load().then((ld) => document.fonts.add(ld))));
  } catch {
    loadedFonts.delete(f.id);
  }
}

export interface TerminalAPI {
  focus: () => void;
  fit: () => void;
  write: (data: string | Uint8Array) => void;
  cols: () => number;
  rows: () => number;
  xterm: () => XTerm | null;
}

export interface TerminalProps {
  // channelId opts the component into the raw-channel I/O path:
  // bytes from openRawChannel(channelId) get written into xterm,
  // and term.onData sends out via writeRaw(channelId). Omit (or
  // pass 0) to manage I/O externally — push output via api.write()
  // from onReady, and receive input via onInput.
  channelId?: number;
  // onInput fires for every keystroke / paste when the caller is
  // managing I/O externally (no channelId). Ignored when channelId
  // is set — the component routes through window.wash.writeRaw in
  // that case.
  onInput?: (bytes: Uint8Array) => void;
  // fontFamily, when set, is used verbatim as the xterm font stack
  // and disables the bundled-font picker / loader (caller owns the
  // font). Leave unset to drive the family from fontId instead.
  fontFamily?: string;
  // fontId selects one of TERM_FONTS (bundled woff2 are loaded on
  // demand). Defaults to TERM_DEFAULT_FONT_ID.
  fontId?: string;
  fontSize?: number;
  theme?: ITheme;
  customKeyHandler?: (ev: KeyboardEvent) => boolean;
  onResize?: (cols: number, rows: number) => void;
  onReady?: (api: TerminalAPI) => void;
  // contextMenu enables the right-click Copy/Paste menu (default on).
  // The font controls (size stepper + family list) appear in that
  // menu only when onFontIdChange is also supplied, so the consumer
  // can persist the choice.
  contextMenu?: boolean;
  onFontIdChange?: (id: string) => void;
  onFontSizeChange?: (px: number) => void;
}

export const Terminal: Component<TerminalProps> = (props) => {
  let hostEl!: HTMLDivElement;

  let term: XTerm | null = null;
  let fit: FitAddon | null = null;

  // Right-click menu position (null = closed) + cached selection
  // state captured at open time so Copy can grey out with no
  // selection.
  const [menu, setMenu] = createSignal<{ x: number; y: number } | null>(null);
  const [hasSel, setHasSel] = createSignal(false);


  // Subscribe synchronously when in raw-channel mode so bytes the
  // router has already buffered (pendingRaw) flush into our local
  // queue before xterm exists; the queue drains into xterm after
  // first fit() so initial output paints into a correctly-sized
  // viewport rather than xterm's 80×24 default. External-I/O mode
  // is the caller's responsibility — they hold the api.write
  // handle and decide when to feed bytes.
  let pending: Uint8Array[] | null = [];
  const writeOrBuffer = (bytes: Uint8Array) => {
    if (pending) pending.push(bytes);
    else term?.write(bytes);
  };
  const channelId = props.channelId ?? 0;
  const unsub = channelId > 0
    ? window.wash.openRawChannel(channelId, writeOrBuffer)
    : () => {};

  const encoder = new TextEncoder();

  const reportResize = () => {
    if (!term || !props.onResize) return;
    props.onResize(term.cols, term.rows);
  };

  // Skip fit when the host is too small to address — the consumer
  // typically toggles tabs via display:none, which sends a 0×0
  // ResizeObserver tick. FitAddon would resize xterm to its 2×1
  // minimum and reflow the buffer through that tiny grid; growing
  // the host back never fully restores the lost wrap state, so
  // scrollback ends up effectively wiped on the next activation.
  // Skipping degenerate sizes keeps the buffer intact across
  // hide/show cycles.
  const safeFit = () => {
    if (!fit) return;
    const r = hostEl.getBoundingClientRect();
    if (r.width < 10 || r.height < 10) return;
    try { fit.fit(); } catch { /* fit itself rejected; next tick will retry */ }
  };

  const applyFamily = (stack: string) => {
    if (!term || term.options.fontFamily === stack) return;
    term.options.fontFamily = stack;
    safeFit();
    reportResize();
  };

  const effectiveSize = () => clampSize(props.fontSize ?? TERM_DEFAULT_FONT_SIZE);

  onMount(() => {
    const initialFamily = props.fontFamily ?? fontById(props.fontId).stack;
    term = new XTerm({
      fontFamily: initialFamily,
      fontSize: effectiveSize(),
      theme: props.theme ?? { background: '#000000' },
      cursorBlink: true,
      allowProposedApi: true,
    });
    if (props.customKeyHandler) term.attachCustomKeyEventHandler(props.customKeyHandler);
    fit = new FitAddon();
    term.loadAddon(fit);
    term.open(hostEl);
    term.onData((s) => {
      const bytes = encoder.encode(s);
      if (channelId > 0) {
        window.wash.writeRaw(channelId, bytes);
      } else {
        props.onInput?.(bytes);
      }
    });

    // Test hook: expose the live Terminal on the host element so
    // playwright specs can read the buffer without traversing
    // framework internals.
    (hostEl as unknown as { __washTerm: XTerm }).__washTerm = term;

    // Defer fit + flush by one frame: xterm measures the host in
    // open() but the layout pass that gives the host its real size
    // hasn't run yet on first mount. requestAnimationFrame fires
    // after layout, so fit() sees the true cell count.
    requestAnimationFrame(() => {
      safeFit();
      const drain = pending ?? [];
      pending = null;
      for (const b of drain) term!.write(b);
      reportResize();
      props.onReady?.({
        focus: () => term?.focus(),
        fit: () => { safeFit(); reportResize(); },
        write: (data) => term?.write(data),
        cols: () => term?.cols ?? 0,
        rows: () => term?.rows ?? 0,
        xterm: () => term,
      });
    });

    const ro = new ResizeObserver(() => {
      safeFit();
      reportResize();
    });
    ro.observe(hostEl);

    onCleanup(() => {
      ro.disconnect();
      unsub();
      term?.dispose();
      term = null;
      fit = null;
    });
  });

  // Live font-size: re-fit so the cell grid (and the PTY resize it
  // triggers) tracks the new metrics.
  createEffect(() => {
    const size = effectiveSize();
    if (!term || term.options.fontSize === size) return;
    term.options.fontSize = size;
    safeFit();
    reportResize();
  });

  // Live font-family. For a bundled font that isn't loaded yet we
  // first render the plain fallback stack, kick the woff2 load, then
  // swap to the custom-family stack once it resolves — the changed
  // string forces xterm to remeasure with the real glyphs.
  createEffect(() => {
    if (props.fontFamily !== undefined) {
      applyFamily(props.fontFamily);
      return;
    }
    const f = fontById(props.fontId);
    if (!f.family || loadedFonts.has(f.id)) {
      applyFamily(f.stack);
      return;
    }
    applyFamily(tokens.fontMono);
    void ensureFontLoaded(f).then(() => {
      // Guard against a fast re-selection landing here stale.
      if (fontById(props.fontId).id === f.id) applyFamily(f.stack);
    });
  });

  // ---- right-click menu ----

  const onCtx = (ev: MouseEvent) => {
    if (props.contextMenu === false) return;
    ev.preventDefault();
    setHasSel(!!term?.hasSelection());
    setMenu({ x: ev.clientX, y: ev.clientY });
  };

  const doCopy = () => {
    const sel = term?.getSelection() ?? '';
    if (sel) navigator.clipboard?.writeText(sel).catch(() => {});
    setMenu(null);
  };

  const doPaste = () => {
    // term.paste() emits onData, so the text rides the same path as
    // typed input (writeRaw in channel mode, onInput otherwise) and
    // honors bracketed-paste mode.
    navigator.clipboard?.readText().then((t) => { if (t) term?.paste(t); }).catch(() => {});
    setMenu(null);
  };

  const showFontMenu = () => !!props.onFontIdChange;
  const activeFontId = () => props.fontId ?? TERM_DEFAULT_FONT_ID;
  const stepSize = (delta: number) => {
    props.onFontSizeChange?.(clampSize((props.fontSize ?? TERM_DEFAULT_FONT_SIZE) + delta));
  };

  return (
    <>
      <div
        ref={hostEl!}
        style={{ width: '100%', height: '100%' }}
        onContextMenu={onCtx}
      />
      <Show when={menu()}>
        {(m) => (
          <Menu
            x={m().x}
            y={m().y}
            onDismiss={() => setMenu(null)}
            data-testid="term-context-menu"
          >
            <MenuItem
              label="Copy"
              disabled={!hasSel()}
              data-testid="term-ctx-copy"
              onClick={doCopy}
            />
            <MenuItem
              label="Paste"
              data-testid="term-ctx-paste"
              onClick={doPaste}
            />
            <Show when={showFontMenu()}>
              <MenuSeparator />
              <div style={sizeRowStyle}>
                <span style={{ flex: 1 }}>Size</span>
                <button
                  type="button"
                  data-testid="term-ctx-size-dec"
                  style={stepBtnStyle}
                  onClick={() => stepSize(-1)}
                >
                  −
                </button>
                <span data-testid="term-ctx-size-val" style={sizeValStyle}>
                  {effectiveSize()}
                </span>
                <button
                  type="button"
                  data-testid="term-ctx-size-inc"
                  style={stepBtnStyle}
                  onClick={() => stepSize(1)}
                >
                  +
                </button>
              </div>
              <MenuSeparator />
              <For each={TERM_FONTS}>
                {(f) => (
                  <MenuItem
                    label={f.label}
                    data-testid={`term-ctx-font-${f.id}`}
                    trailing={activeFontId() === f.id ? <span style={{ opacity: 0.9 }}>✓</span> : undefined}
                    onClick={() => { props.onFontIdChange?.(f.id); setMenu(null); }}
                  />
                )}
              </For>
            </Show>
          </Menu>
        )}
      </Show>
    </>
  );
};

// ---- menu styles ----

const sizeRowStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '6px',
  padding: '4px 14px',
  color: tokens.fg,
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
};

const stepBtnStyle: JSX.CSSProperties = {
  width: '22px',
  height: '22px',
  display: 'inline-flex',
  'align-items': 'center',
  'justify-content': 'center',
  background: tokens.bgRowSelected,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}px`,
  cursor: 'pointer',
  'font-size': '14px',
  'line-height': '1',
};

const sizeValStyle: JSX.CSSProperties = {
  'min-width': '24px',
  'text-align': 'center',
  'font-variant-numeric': 'tabular-nums',
};
