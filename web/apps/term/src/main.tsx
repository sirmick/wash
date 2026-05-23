// wash-app-term: tabbed xterm.js wrapper. One floating window can
// host many PTY tabs; each tab is a separate raw channel + Terminal
// instance. Tab bar at top, terminals stack below with display:none
// on inactive ones so state and scrollback survive switching.
//
// Solid drives the UI but xterm itself is imperative — we hold the
// Terminal/FitAddon references in a plain Map, mount each host via
// <For keyed>, and dispose on unmount.

// xterm + addon-fit are externalized to the shared vendor bundle at
// /vendor/xterm.js (see web/shell/build-vendor.mjs). That bundle
// auto-injects the xterm CSS on first import, so the app no longer
// needs the `?inline` CSS import + manual <style> shim.
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';

import { For, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Plus, X } from 'lucide-solid';
import { defineWashApp } from '@wash/ui';

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

interface TabMeta {
  channelID: number;
  shell: string;
}

// The on-the-wire/saved schema uses snake_case to match the rest of
// wash's JSON conventions.
interface PersistedTabRow {
  channel_id: number;
  shell: string;
}

interface PersistedState {
  tabs?: PersistedTabRow[];
  active?: number;
}

// TAB_BAR_HEIGHT — 32 (was 28) leaves 4px of breathing room above the
// tab buttons; the bar's padding-top puts it there. Without the gap
// the tabs render flush against the window titlebar and read as one
// flat block rather than a row of pickable controls.
const TAB_BAR_HEIGHT = 32;

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [tabs, setTabs] = createSignal<TabMeta[]>([]);
  const [active, setActive] = createSignal(0);

  // xterm instances live outside the reactive system — they're imperative
  // canvas-backed widgets. We key by channel id.
  type TabRef = {
    host: HTMLDivElement;
    term: Terminal;
    fit: FitAddon;
    unsub: () => void;
  };
  const refs = new Map<number, TabRef>();

  const send = (m: unknown) => window.wash.sendAppMsg(props.instance, m);

  // ---- tab lifecycle ----

  const addTab = (channelID: number, shellPath: string) => {
    if (tabs().some((t) => t.channelID === channelID)) return;
    setTabs([...tabs(), { channelID, shell: shellPath }]);
    setActive(channelID);
    persist();
    // xterm setup happens in the per-tab onMount below.
  };

  const removeTab = (channelID: number) => {
    const ref = refs.get(channelID);
    if (ref) {
      ref.unsub();
      ref.term.dispose();
      refs.delete(channelID);
    }
    const remaining = tabs().filter((t) => t.channelID !== channelID);
    setTabs(remaining);
    if (active() === channelID) {
      setActive(remaining[0]?.channelID ?? 0);
    }
    persist();
  };

  const activate = (channelID: number) => {
    if (active() === channelID) return;
    setActive(channelID);
    persist();
    requestAnimationFrame(() => {
      const ref = refs.get(channelID);
      if (ref) {
        ref.fit.fit();
        ref.term.focus();
        sendResize(channelID);
      }
    });
  };

  const sendResize = (channelID: number) => {
    const ref = refs.get(channelID);
    if (!ref) return;
    send({
      kind: 'resize',
      channel_id: channelID,
      cols: ref.term.cols,
      rows: ref.term.rows,
    });
  };

  const openNewTab = () => send({ kind: 'new_tab' });
  const requestCloseTab = (channelID: number) => send({ kind: 'close_tab', channel_id: channelID });

  // ---- BE ----

  const handleBE = (m: BEMessage) => {
    switch (m.kind) {
      case 'tab_opened':
        addTab(Number(m.channel_id), String(m.shell ?? 'shell'));
        return;
      case 'tab_closed':
        removeTab(Number(m.channel_id));
        return;
      case 'tab_error': {
        const ref = refs.get(active());
        if (ref) ref.term.write('\r\n\x1b[31mwash-term: ' + String(m.msg) + '\x1b[0m\r\n');
        return;
      }
    }
  };

  // ---- state persistence ----

  const persist = () => {
    if (!props.instance) return;
    const state: PersistedState = {
      tabs: tabs().map((t) => ({ channel_id: t.channelID, shell: t.shell })),
      active: active() || undefined,
    };
    send({ kind: 'save_state', state });
  };

  const restoreFrom = (s: PersistedState) => {
    if (!s.tabs?.length) return;
    for (const t of s.tabs) addTab(Number(t.channel_id), t.shell);
    if (s.active && tabs().some((t) => t.channelID === s.active)) {
      setActive(s.active);
    }
  };

  // ---- keyboard shortcuts ----

  const onTermKey = (ev: KeyboardEvent): boolean => {
    if (ev.type !== 'keydown') return true;
    if (ev.ctrlKey && ev.shiftKey && (ev.key === 'T' || ev.key === 't')) {
      openNewTab();
      return false;
    }
    if (ev.ctrlKey && ev.shiftKey && (ev.key === 'W' || ev.key === 'w')) {
      if (active()) requestCloseTab(active());
      return false;
    }
    if (ev.ctrlKey && ev.key === 'Tab') {
      ev.preventDefault();
      cycleTabs(ev.shiftKey ? -1 : 1);
      return false;
    }
    return true;
  };

  const cycleTabs = (dir: number) => {
    const ids = tabs().map((t) => t.channelID);
    if (ids.length < 2) return;
    const i = ids.indexOf(active());
    if (i < 0) return;
    activate(ids[(i + dir + ids.length) % ids.length]);
  };

  // ---- per-tab xterm mount ----

  const mountTab = (channelID: number, host: HTMLDivElement) => {
    const term = new Terminal({
      fontFamily: 'ui-monospace,Menlo,Consolas,monospace',
      fontSize: 13,
      theme: { background: '#000000' },
      cursorBlink: true,
      allowProposedApi: true,
    });
    term.attachCustomKeyEventHandler(onTermKey);
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host);
    // Expose the Terminal on the host element so tests (and any
    // debug tooling) can poke at the live buffer without going
    // through internal refs.
    (host as unknown as { __washTerm: Terminal }).__washTerm = term;

    // Buffer incoming bytes until the next animation frame fires.
    // The non-determinism we were chasing: subscribeRaw drains the
    // shell-side pendingRaw queue synchronously, so the bash prompt
    // can land in xterm.write before xterm's renderer has measured
    // the host. xterm internally tracks an 80×24 default until a
    // fit() supersedes it; bytes written before the fit paint into
    // a stale viewport and the rows never repaint. Holding the
    // bytes in `pending` until after the first fit.fit() means the
    // viewport is correct before any output reaches the renderer.
    let pending: Uint8Array[] | null = [];
    const writeOrBuffer = (bytes: Uint8Array) => {
      if (pending) pending.push(bytes);
      else term.write(bytes);
    };
    const unsub = window.wash.openRawChannel(channelID, writeOrBuffer);
    const encoder = new TextEncoder();
    term.onData((data) => window.wash.writeRaw(channelID, encoder.encode(data)));

    refs.set(channelID, { host, term, fit, unsub });

    requestAnimationFrame(() => {
      try { fit.fit(); } catch { /* host still 0×0 — next ResizeObserver wakeup picks it up */ }
      const drain = pending ?? [];
      pending = null;
      for (const b of drain) term.write(b);
      if (active() === channelID) {
        term.focus();
        sendResize(channelID);
      }
    });
  };

  // ---- lifecycle ----

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail as BEMessage);
    const onState = (ev: Event) => {
      const s = (ev as CustomEvent).detail as PersistedState | null;
      if (s) restoreFrom(s);
    };
    props.host.addEventListener('wash:msg', onMsg);
    props.host.addEventListener('wash:state', onState);

    // Window resize → refit the active terminal.
    const ro = new ResizeObserver(() => {
      const ref = refs.get(active());
      if (!ref) return;
      ref.fit.fit();
      sendResize(active());
    });
    ro.observe(props.host);

    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('wash:state', onState);
      ro.disconnect();
      for (const ref of refs.values()) {
        ref.unsub();
        ref.term.dispose();
      }
      refs.clear();
    });
  });

  return (
    <>
      <div data-testid="term-tabbar" style={tabBarStyle}>
        <For each={tabs()}>
          {(tab) => {
            const isActive = () => active() === tab.channelID;
            return (
              <button
                type="button"
                data-testid={`term-tab-${tab.channelID}`}
                style={{
                  background: isActive() ? '#33387a' : 'transparent',
                  color: '#eee',
                  border: 'none',
                  'border-top': isActive() ? '2px solid #66c' : '2px solid transparent',
                  // Rounded only on top — the bottom meets the bar's
                  // border-bottom flush, matching browser-tab idiom.
                  'border-radius': '6px 6px 0 0',
                  padding: '0 6px 0 10px',
                  cursor: 'pointer',
                  font: '12px ui-monospace,Menlo,Consolas,monospace',
                  display: 'flex',
                  'align-items': 'center',
                  gap: '8px',
                  'max-width': '200px',
                }}
                onClick={() => activate(tab.channelID)}
              >
                <span
                  style={{
                    overflow: 'hidden',
                    'text-overflow': 'ellipsis',
                    'white-space': 'nowrap',
                  }}
                >
                  {shortShellName(tab.shell)}
                </span>
                <span
                  data-testid={`term-tab-close-${tab.channelID}`}
                  style={{
                    opacity: 0.6,
                    cursor: 'pointer',
                    padding: '0 4px',
                    display: 'inline-flex',
                    'align-items': 'center',
                  }}
                  onClick={(ev) => {
                    ev.stopPropagation();
                    requestCloseTab(tab.channelID);
                  }}
                >
                  <X size={12} />
                </span>
              </button>
            );
          }}
        </For>
        <button
          type="button"
          data-testid="term-new-tab"
          title="New tab (Ctrl+Shift+T)"
          style={addBtnStyle}
          onClick={openNewTab}
        >
          <Plus size={14} />
        </button>
      </div>
      <div style={{ flex: 1, position: 'relative', 'min-height': 0 }}>
        <For each={tabs()}>
          {(tab) => (
            <div
              data-testid="term-host"
              data-channel={tab.channelID}
              style={{
                position: 'absolute',
                inset: 0,
                display: active() === tab.channelID ? 'block' : 'none',
              }}
              ref={(el) => {
                // Mount xterm once per tab into this host div.
                if (!refs.has(tab.channelID)) mountTab(tab.channelID, el);
              }}
            />
          )}
        </For>
      </div>
    </>
  );
};

// ---- helpers / styles ----

function shortShellName(p: string): string {
  const i = p.lastIndexOf('/');
  return i >= 0 ? p.slice(i + 1) : p;
}

const tabBarStyle: JSX.CSSProperties = {
  height: `${TAB_BAR_HEIGHT}px`,
  background: '#181828',
  'border-bottom': '1px solid #2a2a3a',
  display: 'flex',
  'align-items': 'stretch',
  gap: '2px',
  // padding-top creates the gap above the tabs; tabs round into the
  // border-bottom line, matching how browser tabs sit on a bar.
  padding: '4px 4px 0',
  'overflow-x': 'auto',
  'overflow-y': 'hidden',
  font: '12px ui-monospace,Menlo,Consolas,monospace',
  'flex-shrink': 0,
};

const addBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: '#eee',
  border: 'none',
  padding: '0 10px',
  cursor: 'pointer',
  font: '14px ui-monospace,Menlo,Consolas,monospace',
  opacity: 0.8,
};

// ---- custom element ----

defineWashApp('wash-app-term', (props) => <App {...props} />, {
  style: 'display:flex;flex-direction:column;width:100%;height:100%;box-sizing:border-box;background:#000;color:#eee;overflow:hidden',
});
