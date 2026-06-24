// Shared xterm.js wrapper. Owns: xterm + FitAddon construction,
// raw-channel I/O wiring, pre-mount byte buffer, ResizeObserver
// refit, live font (family/size) application, and a right-click
// copy/paste context menu. Consumers own: tab/pane orchestration,
// focus policy, keyboard shortcuts (passed in), backend messaging,
// the font choice (driven in via the fontId/fontSize props — the
// picker now lives in the consumer's menubar, not this menu), and
// the OSC window title (surfaced via onTitle).
//
// xterm and addon-fit are externalized to the shared vendor bundle
// (web/shell/build-vendor.mjs); consumers' vite configs already
// include both names in `rollupOptions.external`.

import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import type { ITheme } from '@xterm/xterm';
import { Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component } from 'solid-js';

import { tokens } from './tokens';
import { washAssetUrl } from './assets';
import { Menu, MenuItem } from './menu';
import { washCopyText, washPasteText } from './clipboard';
import { washAppearance, onAppearanceChange } from './packs';

// Terminal palettes by pack appearance. The component uses these when no
// explicit `theme` prop is given, and flips live when the pack changes.
// Dark is the canonical Solarized Dark; light is a clean WHITE terminal
// (not cream) with an ANSI set tuned to read on white.
export const TERM_THEME_DARK: ITheme = {
  // Dead-black background like an out-of-the-box Linux terminal, with the
  // GNOME-Terminal "Tango" ANSI set — the familiar default, readable on
  // pure black (Solarized's #002b36 base read as washed-out grey and left
  // a visible bar where a #000 container showed through underneath).
  background: '#000000', foreground: '#d0d0d0',
  cursor: '#d0d0d0', cursorAccent: '#000000', selectionBackground: '#3a3a3a',
  black: '#000000', red: '#cc0000', green: '#4e9a06', yellow: '#c4a000',
  blue: '#3465a4', magenta: '#75507b', cyan: '#06989a', white: '#d3d7cf',
  brightBlack: '#555753', brightRed: '#ef2929', brightGreen: '#8ae234', brightYellow: '#fce94f',
  brightBlue: '#729fcf', brightMagenta: '#ad7fa8', brightCyan: '#34e2e2', brightWhite: '#eeeeec',
};
export const TERM_THEME_LIGHT: ITheme = {
  background: '#ffffff', foreground: '#2b2b2b',
  cursor: '#2b2b2b', cursorAccent: '#ffffff', selectionBackground: '#cfe0ff',
  black: '#2b2b2b', red: '#c0341d', green: '#1d7a37', yellow: '#8a6d00',
  blue: '#1c5fd6', magenta: '#9426a8', cyan: '#0a7e8c', white: '#cfcfcf',
  brightBlack: '#6b6b6b', brightRed: '#e0402a', brightGreen: '#2a9a4a', brightYellow: '#a88500',
  brightBlue: '#3a7bff', brightMagenta: '#b944cc', brightCyan: '#1aa0b0', brightWhite: '#000000',
};
const termThemeFor = (a: 'light' | 'dark'): ITheme => (a === 'light' ? TERM_THEME_LIGHT : TERM_THEME_DARK);

// Double-click word selection: treat a "word" as an identifier run
// ([A-Za-z0-9_]) and break on every other symbol, so double-clicking
// `bin` in /usr/local/bin selects just that segment. xterm's default
// only breaks on whitespace and a few brackets, so it would grab the
// whole path. This is the full ASCII punctuation set MINUS '_' (kept a
// word char so snake_case identifiers stay one word).
const TERM_WORD_SEPARATORS = ' !"#$%&\'()*+,-./:;<=>?@[\\]^`{|}~';

// isMac gates the copy keybinding: on macOS Ctrl+C must always stay
// SIGINT and Cmd (meta)+C copies; on every other platform Ctrl+C copies
// when there's a selection and otherwise falls through as SIGINT.
const isMac =
  typeof navigator !== 'undefined' &&
  /Mac|iPhone|iPad|iPod/.test(
    (navigator as unknown as { userAgentData?: { platform?: string } }).userAgentData?.platform ||
      navigator.platform ||
      '',
  );

// Concrete monospace fallback shown while a bundled woff2 loads. MUST be a
// real font stack, never a CSS var() — xterm measures cell width on a
// canvas, which can't resolve var(), so a var() string yields the wrong
// cell width (wide letter-spacing) on first load.
const TERM_FALLBACK_STACK = 'ui-monospace, Menlo, Consolas, "DejaVu Sans Mono", monospace';

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
  { id: 'system', label: 'System Mono', stack: TERM_FALLBACK_STACK },
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

// TermModes is the terminal-mode state a reattaching consumer
// persists and re-seeds. The 256KB scrollback replay only carries the
// byte TAIL of a session, so mode-setting sequences emitted once at
// app start (alt-screen 1049, bracketed paste 2004, mouse 1000/1002/
// 1006, application cursor keys 1, cursor visibility 25, …) can fall
// outside the window — reattaching into a long-running vim/htop then
// renders into the wrong buffer with dead mouse/paste handling.
//
// Tracked from the byte stream via xterm's own parser hooks (no
// bespoke VT parsing) and reported through onModesChanged; fed back
// on remount via initialModes, which is written into the fresh xterm
// BEFORE the replay so alt-screen content paints into the right
// buffer. SGR/charset state is deliberately NOT tracked — apps
// re-emit it with every drawing batch, so it is always in-window.
export interface TermModes {
  // DECSET/DECRST private modes by number: true = set (h), false =
  // explicitly reset (l). Untouched modes are absent (default state).
  // JSON round-trips turn the keys into strings; consumers treat the
  // index type as cosmetic.
  dec: Record<number, boolean>;
  // ESC = (application keypad, true) / ESC > (normal, false).
  keypad?: boolean;
}

// modesToSeq synthesizes the escape sequence that restores m on a
// fresh terminal. Numeric order keeps the output deterministic.
function modesToSeq(m: TermModes): string {
  let s = '';
  const ns = Object.keys(m.dec)
    .map(Number)
    .filter((n) => Number.isFinite(n) && n > 0)
    .sort((a, b) => a - b);
  for (const n of ns) s += `\x1b[?${n}${m.dec[n] ? 'h' : 'l'}`;
  if (m.keypad !== undefined) s += m.keypad ? '\x1b=' : '\x1b>';
  return s;
}

// Bound on distinct tracked mode numbers — a guardrail against a
// hostile/buggy stream growing the persisted blob without limit.
const MAX_TRACKED_MODES = 64;

export interface TerminalAPI {
  focus: () => void;
  fit: () => void;
  write: (data: string | Uint8Array) => void;
  cols: () => number;
  rows: () => number;
  xterm: () => XTerm | null;
  modes: () => TermModes;
  // Imperative edit actions, so window chrome (e.g. the term app's
  // menubar) can drive copy/paste/selection without re-deriving the
  // clipboard wiring. copySelection mirrors the selection to the wash +
  // system clipboards and returns whether there was anything to copy.
  copySelection: () => boolean;
  paste: () => void;
  selectAll: () => void;
  clearScreen: () => void;
  hasSelection: () => boolean;
}

export interface TerminalProps {
  // channelId opts the component into the raw-channel I/O path:
  // bytes from openRawChannel(channelId) get written into xterm,
  // and term.onData sends out via writeRaw(channelId). Omit (or
  // pass 0) to manage I/O externally — push output via api.write()
  // from onReady, and receive input via onInput.
  channelId?: number;
  // origin routes the pty raw channel to the owning host's connection
  // (docs/REMOTE.md §4) — pass the app's props.origin so a terminal on a
  // remote host isn't mis-routed to the local router. Defaults to local.
  origin?: string;
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
  // appearanceOverride pins the xterm palette to dark or light, ignoring
  // the desktop pack's appearance. Leave unset to follow the pack live
  // (the default). `theme` still wins over this when both are given.
  appearanceOverride?: 'dark' | 'light';
  customKeyHandler?: (ev: KeyboardEvent) => boolean;
  onResize?: (cols: number, rows: number) => void;
  onReady?: (api: TerminalAPI) => void;
  // onTitle fires with the terminal's OSC window title (ESC]0;…/ESC]2;…)
  // whenever the running program sets it — used by the term app to
  // label the tab. Empty string when the program clears the title.
  onTitle?: (title: string) => void;
  // contextMenu enables the right-click Copy/Paste menu (default on).
  contextMenu?: boolean;
  // initialCols/initialRows open the fresh xterm at a specific grid
  // instead of fitting to the container — set them to the pty's
  // current size on reattach so the scrollback replay renders at the
  // width it was emitted for. After the replay drains, the component
  // fits to the container and xterm reflows soft-wrapped lines.
  // Read once at mount.
  initialCols?: number;
  initialRows?: number;
  // initialModes re-seeds persisted terminal-mode state (see
  // TermModes) into the fresh xterm before any replay bytes. Read
  // once at mount.
  initialModes?: TermModes;
  // onModesChanged fires whenever the tracked mode state changes;
  // consumers persist it (debounced) for the next remount.
  onModesChanged?: (m: TermModes) => void;
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
  const origin = props.origin ?? 'local';
  const unsub = channelId > 0
    ? window.wash.openRawChannelFor(origin, channelId, writeOrBuffer)
    : () => {};

  const encoder = new TextEncoder();

  const reportResize = () => {
    if (!term || !props.onResize) return;
    props.onResize(term.cols, term.rows);
  };

  // holdFits suppresses every fit while a restored session's replay
  // is still parsing — xterm.write is async, and a ResizeObserver
  // tick (or font effect) landing mid-drain would refit to the
  // container before the replay rendered at the pty's old grid.
  // Cleared by the drain's write-callback in onMount.
  let holdFits = false;

  // Skip fit when the host is too small to address — the consumer
  // typically toggles tabs via display:none, which sends a 0×0
  // ResizeObserver tick. FitAddon would resize xterm to its 2×1
  // minimum and reflow the buffer through that tiny grid; growing
  // the host back never fully restores the lost wrap state, so
  // scrollback ends up effectively wiped on the next activation.
  // Skipping degenerate sizes keeps the buffer intact across
  // hide/show cycles.
  const safeFit = () => {
    if (!fit || holdFits) return;
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

  // pasteWash inserts the wash clipboard at the cursor through
  // xterm's paste path (bracketed-paste aware, CR-normalized).
  const pasteWash = () => {
    void washPasteText().then((text) => {
      if (text && term) term.paste(text);
    });
  };

  // ---- terminal-mode tracking (see TermModes) ----

  const decModes: Record<number, boolean> = {};
  let keypad: boolean | undefined;
  const emitModes = () => props.onModesChanged?.({ dec: { ...decModes }, keypad });
  // noteDec records CSI ? … h/l. Always returns false so xterm's own
  // handler still runs — we observe the stream, never consume it.
  const noteDec = (params: (number | number[])[], on: boolean): boolean => {
    let changed = false;
    for (const p of params) {
      const n = Array.isArray(p) ? p[0] : p;
      if (typeof n !== 'number' || n <= 0) continue;
      if (decModes[n] === on) continue;
      if (!(n in decModes) && Object.keys(decModes).length >= MAX_TRACKED_MODES) continue;
      decModes[n] = on;
      changed = true;
    }
    if (changed) emitModes();
    return false;
  };
  const noteKeypad = (on: boolean): boolean => {
    if (keypad !== on) {
      keypad = on;
      emitModes();
    }
    return false;
  };

  // Resync (docs/PTY_ROBUST.md, Fix B): the router suppressed live output
  // while this FE was behind (to avoid shipping a torn stream), and now
  // asks us to reset before it replays a realigned scrollback snapshot.
  // Reset the xterm to a clean state and re-seed the modes we've tracked
  // from the byte stream, so the snapshot bytes that arrive next (on the
  // same channel, in WS order) render correctly instead of being
  // concatenated mid-stream and leaving the terminal in a wrong mode.
  const onResync = () => {
    if (!term) return;
    term.reset();
    const seq = modesToSeq({ dec: { ...decModes }, keypad });
    if (seq) term.write(seq);
  };
  const unsubResync = channelId > 0
    ? window.wash.subscribeResyncFor(origin, channelId, onResync)
    : () => {};

  onMount(() => {
    const initialFamily = props.fontFamily ?? fontById(props.fontId).stack;
    // A restored session opens at the pty's existing grid so the
    // replay renders correctly; a fresh one fits to the container.
    const restoredGrid =
      (props.initialCols ?? 0) > 1 && (props.initialRows ?? 0) > 1
        ? { cols: props.initialCols, rows: props.initialRows }
        : undefined;
    term = new XTerm({
      ...restoredGrid,
      fontFamily: initialFamily,
      fontSize: effectiveSize(),
      theme: props.theme ?? termThemeFor(props.appearanceOverride ?? washAppearance()),
      cursorBlink: true,
      allowProposedApi: true,
      wordSeparator: TERM_WORD_SEPARATORS,
    });
    // Theme resolution: an explicit `theme` prop wins; otherwise an
    // appearanceOverride (set by the menubar's Theme menu) pins dark/light;
    // otherwise follow the desktop pack's appearance live. A signal bridges
    // the pack subscription so the effect re-runs on a pack change OR an
    // override change.
    if (!props.theme) {
      const [packAppearance, setPackAppearance] = createSignal(washAppearance());
      onCleanup(onAppearanceChange((a) => setPackAppearance(a)));
      createEffect(() => {
        if (term) term.options.theme = termThemeFor(props.appearanceOverride ?? packAppearance());
      });
    }
    // Clipboard keys are component-level so every terminal in wash
    // behaves the same; the consumer's customKeyHandler runs after.
    // Ctrl+Shift+C copies the selection; Ctrl+Shift+V pastes the wash
    // clipboard (plain Ctrl+V stays the browser's native system-clipboard
    // paste, which xterm receives as a paste event).
    //
    // Plain copy without Shift is platform-split: on macOS Cmd+C copies
    // (Ctrl+C is left alone as SIGINT); elsewhere Ctrl+C copies WHEN a
    // selection exists and otherwise falls through to xterm as SIGINT.
    // After a PC Ctrl+C copy we clear the selection so a second Ctrl+C
    // interrupts (matching Windows Terminal / GNOME-on-selection).
    term.attachCustomKeyEventHandler((ev: KeyboardEvent) => {
      if (ev.type === 'keydown' && (ev.key === 'C' || ev.key === 'c')) {
        if (ev.ctrlKey && ev.shiftKey) {
          const sel = term?.getSelection();
          if (sel) {
            washCopyText(sel);
            return false;
          }
        } else if (isMac && ev.metaKey && !ev.ctrlKey) {
          const sel = term?.getSelection();
          if (sel) {
            washCopyText(sel);
            return false;
          }
        } else if (!isMac && ev.ctrlKey && !ev.shiftKey && !ev.altKey && !ev.metaKey) {
          const sel = term?.getSelection();
          if (sel) {
            washCopyText(sel);
            term?.clearSelection();
            return false;
          }
          // No selection: let Ctrl+C through as SIGINT.
        }
      }
      if (ev.type === 'keydown' && ev.ctrlKey && ev.shiftKey && (ev.key === 'V' || ev.key === 'v')) {
        pasteWash();
        return false;
      }
      return props.customKeyHandler ? props.customKeyHandler(ev) : true;
    });
    if (props.onModesChanged || props.initialModes) {
      term.parser.registerCsiHandler({ prefix: '?', final: 'h' }, (p) => noteDec(p, true));
      term.parser.registerCsiHandler({ prefix: '?', final: 'l' }, (p) => noteDec(p, false));
      term.parser.registerEscHandler({ final: '=' }, () => noteKeypad(true));
      term.parser.registerEscHandler({ final: '>' }, () => noteKeypad(false));
    }
    fit = new FitAddon();
    term.loadAddon(fit);
    term.open(hostEl);
    term.onData((s) => {
      const bytes = encoder.encode(s);
      if (channelId > 0) {
        window.wash.writeRawFor(origin, channelId, bytes);
      } else {
        props.onInput?.(bytes);
      }
    });
    // OSC window title (set by the shell's PROMPT_COMMAND, vim, ssh, …)
    // bubbles to the consumer so it can label the tab.
    if (props.onTitle) term.onTitleChange((t) => props.onTitle?.(t));

    // PuTTY-style select = copy: xterm keeps its own selection model
    // (not a DOM selection), so on selection-end we push the selected
    // text into the wash clipboard + mirror it to the system clipboard
    // while the mouseup gesture is live. LEFT button only — a right-click
    // opens the context menu (Copy/Paste + font controls) and must NOT
    // re-copy the live selection, or right-click-to-Paste would clobber
    // the clipboard with the selection right before reading it.
    const onMouseUp = (ev: MouseEvent) => {
      if (ev.button !== 0) return;
      const sel = term?.getSelection();
      if (sel) washCopyText(sel);
    };
    hostEl.addEventListener('mouseup', onMouseUp);
    onCleanup(() => {
      hostEl.removeEventListener('mouseup', onMouseUp);
    });

    // Test hook: expose the live Terminal on the host element so
    // playwright specs can read the buffer without traversing
    // framework internals.
    (hostEl as unknown as { __washTerm: XTerm }).__washTerm = term;

    // Defer fit + flush by one frame: xterm measures the host in
    // open() but the layout pass that gives the host its real size
    // hasn't run yet on first mount. requestAnimationFrame fires
    // after layout, so fit() sees the true cell count.
    //
    // Order depends on the mount kind. Fresh session: fit first so
    // initial output paints into the real viewport, not the 80×24
    // default. Restored session (initialCols/Rows set): drain the
    // replay FIRST, at the pty's old grid — the bytes hard-wrap at
    // that width — then fit; xterm reflows soft-wrapped lines to the
    // live container. Persisted modes are re-seeded ahead of either
    // drain so alt-screen replay content lands in the right buffer.
    const restored = !!restoredGrid;
    holdFits = restored;
    requestAnimationFrame(() => {
      if (props.initialModes) {
        const seq = modesToSeq(props.initialModes);
        if (seq) term!.write(seq);
      }
      if (!restored) safeFit();
      const drain = pending ?? [];
      pending = null;
      const finish = () => {
        if (restored) {
          holdFits = false;
          safeFit();
        }
        reportResize();
        props.onReady?.({
          focus: () => term?.focus(),
          fit: () => { safeFit(); reportResize(); },
          write: (data) => term?.write(data),
          cols: () => term?.cols ?? 0,
          rows: () => term?.rows ?? 0,
          xterm: () => term,
          modes: () => ({ dec: { ...decModes }, keypad }),
          copySelection: () => {
            const sel = term?.getSelection();
            if (!sel) return false;
            washCopyText(sel);
            return true;
          },
          paste: () => pasteWash(),
          selectAll: () => term?.selectAll(),
          clearScreen: () => term?.clear(),
          hasSelection: () => !!term?.hasSelection(),
        });
      };
      if (restored && drain.length) {
        // xterm.write parses asynchronously; the reflow fit must not
        // run until the replay has rendered at the old grid, so it
        // rides the final chunk's write-callback.
        for (let i = 0; i < drain.length - 1; i++) term!.write(drain[i]);
        term!.write(drain[drain.length - 1], finish);
      } else {
        for (const b of drain) term!.write(b);
        finish();
      }
    });

    const ro = new ResizeObserver(() => {
      safeFit();
      reportResize();
    });
    ro.observe(hostEl);

    onCleanup(() => {
      ro.disconnect();
      unsub();
      unsubResync();
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
    applyFamily(TERM_FALLBACK_STACK);
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

  // Menu Copy/Paste ride the wash clipboard, not navigator.clipboard:
  // on the plain-HTTP LAN origin wash ships on, navigator.clipboard is
  // undefined and would silently no-op. washCopyText still mirrors to
  // the system clipboard while the click gesture is live; pasteWash
  // goes through term.paste(), so the text rides the same path as
  // typed input and honors bracketed-paste mode.
  const doCopy = () => {
    const sel = term?.getSelection() ?? '';
    if (sel) washCopyText(sel);
    setMenu(null);
  };

  const doPaste = () => {
    term?.focus();
    pasteWash();
    setMenu(null);
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
          </Menu>
        )}
      </Show>
    </>
  );
};

