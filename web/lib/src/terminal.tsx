// Shared xterm.js wrapper. Owns: xterm + FitAddon construction,
// raw-channel I/O wiring, pre-mount byte buffer, ResizeObserver
// refit. Consumers own: tab/pane orchestration, focus policy,
// keyboard shortcuts (passed in), backend messaging.
//
// xterm and addon-fit are externalized to the shared vendor bundle
// (web/shell/build-vendor.mjs); consumers' vite configs already
// include both names in `rollupOptions.external`.

import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import type { ITheme } from '@xterm/xterm';
import { onCleanup, onMount } from 'solid-js';
import type { Component } from 'solid-js';

import { tokens } from './tokens';
import { washCopyText, washPasteText } from './clipboard';

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
  fontFamily?: string;
  fontSize?: number;
  theme?: ITheme;
  customKeyHandler?: (ev: KeyboardEvent) => boolean;
  onResize?: (cols: number, rows: number) => void;
  onReady?: (api: TerminalAPI) => void;
}

export const Terminal: Component<TerminalProps> = (props) => {
  let hostEl!: HTMLDivElement;

  let term: XTerm | null = null;
  let fit: FitAddon | null = null;


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

  // pasteWash inserts the wash clipboard at the cursor through
  // xterm's paste path (bracketed-paste aware, CR-normalized).
  const pasteWash = () => {
    void washPasteText().then((text) => {
      if (text && term) term.paste(text);
    });
  };

  onMount(() => {
    term = new XTerm({
      fontFamily: props.fontFamily ?? tokens.fontMono,
      fontSize: props.fontSize ?? 13,
      theme: props.theme ?? { background: '#000000' },
      cursorBlink: true,
      allowProposedApi: true,
    });
    // Clipboard keys are component-level so every terminal in wash
    // behaves the same; the consumer's customKeyHandler runs after.
    // Ctrl+Shift+C copies the selection (plain Ctrl+C must stay
    // SIGINT); Ctrl+Shift+V pastes the wash clipboard (plain Ctrl+V
    // stays the browser's native system-clipboard paste, which xterm
    // receives as a paste event).
    term.attachCustomKeyEventHandler((ev: KeyboardEvent) => {
      if (ev.type === 'keydown' && ev.ctrlKey && ev.shiftKey) {
        if (ev.key === 'C' || ev.key === 'c') {
          const sel = term?.getSelection();
          if (sel) {
            washCopyText(sel);
            return false;
          }
        }
        if (ev.key === 'V' || ev.key === 'v') {
          pasteWash();
          return false;
        }
      }
      return props.customKeyHandler ? props.customKeyHandler(ev) : true;
    });
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

    // PuTTY-style mouse clipboard. Select = copy: xterm keeps its own
    // selection model (not a DOM selection), so on selection-end we
    // push the selected text into the wash clipboard + mirror it to
    // the system clipboard while the mouseup gesture is live.
    // Right-click = paste, native context menu suppressed — the menu
    // offered nothing usable here anyway (its Paste needs a system
    // clipboard read the insecure context forbids).
    const onMouseUp = () => {
      const sel = term?.getSelection();
      if (sel) washCopyText(sel);
    };
    const onContextMenu = (ev: MouseEvent) => {
      ev.preventDefault();
      // Consumers (edit's text area, future panes) hang their own
      // contextmenu handlers on ancestors — the terminal's paste
      // gesture must not also open those menus.
      ev.stopPropagation();
      term?.focus();
      pasteWash();
    };
    hostEl.addEventListener('mouseup', onMouseUp);
    hostEl.addEventListener('contextmenu', onContextMenu);
    onCleanup(() => {
      hostEl.removeEventListener('mouseup', onMouseUp);
      hostEl.removeEventListener('contextmenu', onContextMenu);
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

  // If the component unmounts before onMount completes (rare but
  // possible under Suspense / fast tab churn), still release the
  // raw-channel subscription.
  onCleanup(() => {
    if (!term && channelId > 0) unsub();
  });

  return <div ref={hostEl!} style={{ width: '100%', height: '100%' }} />;
};
