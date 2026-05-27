// wash-app-term: tabbed xterm.js wrapper. One floating window can
// host many PTY tabs; each tab is a separate raw channel + Terminal
// instance. Tab bar at top, terminals stack below with display:none
// on inactive ones so state and scrollback survive switching.
//
// xterm construction and raw-channel wiring live in @wash/ui's
// <Terminal>. This file owns the tab orchestration (open, close,
// switch, persist, keyboard shortcuts) and forwards an imperative
// handle from each <Terminal> via onReady so tab activation can
// trigger focus/fit.

import { For, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Plus, X } from 'lucide-solid';
import { Terminal, defineWashApp } from '@wash/ui';
import type { TerminalAPI } from '@wash/ui';

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

  // Imperative <Terminal> handles keyed by channel id. Populated
  // by each <Terminal>'s onReady callback; dropped on tab close.
  const apis = new Map<number, TerminalAPI>();
  // Last reported size per channel so resize messages have a value
  // even when called outside a fit() tick.
  const sizes = new Map<number, { cols: number; rows: number }>();

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
    apis.delete(channelID);
    sizes.delete(channelID);
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
      const api = apis.get(channelID);
      if (api) {
        api.fit();
        api.focus();
      }
    });
  };

  const sendResize = (channelID: number, cols: number, rows: number) => {
    send({ kind: 'resize', channel_id: channelID, cols, rows });
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
        const api = apis.get(active());
        if (api) api.write('\r\n\x1b[31mwash-term: ' + String(m.msg) + '\x1b[0m\r\n');
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

  // ---- lifecycle ----

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail as BEMessage);
    const onState = (ev: Event) => {
      const s = (ev as CustomEvent).detail as PersistedState | null;
      if (s) restoreFrom(s);
    };
    props.host.addEventListener('wash:msg', onMsg);
    props.host.addEventListener('wash:state', onState);

    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('wash:state', onState);
      apis.clear();
      sizes.clear();
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
          {(tab) => {
            let hostEl: HTMLDivElement | undefined;
            return (
              <div
                data-testid="term-host"
                data-channel={tab.channelID}
                style={{
                  position: 'absolute',
                  inset: 0,
                  display: active() === tab.channelID ? 'block' : 'none',
                }}
                ref={(el) => { hostEl = el; }}
              >
                <Terminal
                  channelId={tab.channelID}
                  customKeyHandler={onTermKey}
                  onReady={(api) => {
                    apis.set(tab.channelID, api);
                    if (active() === tab.channelID) api.focus();
                    // Expose on the term-host div too — e2e tests look
                    // up __washTerm on the testid-bearing element.
                    if (hostEl) (hostEl as unknown as { __washTerm: unknown }).__washTerm = api.xterm();
                  }}
                  onResize={(cols, rows) => {
                    const prev = sizes.get(tab.channelID);
                    if (prev && prev.cols === cols && prev.rows === rows) return;
                    sizes.set(tab.channelID, { cols, rows });
                    sendResize(tab.channelID, cols, rows);
                  }}
                />
              </div>
            );
          }}
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
