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

import { For, Show, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Plus, X } from 'lucide-solid';
import { Menu, MenuItem, Terminal, TERM_DEFAULT_FONT_ID, TERM_DEFAULT_FONT_SIZE, defineWashApp, tokens } from '@wash/ui';
import type { TermModes, TerminalAPI } from '@wash/ui';

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

// Color tags a tab can carry, picked from the per-tab right-click
// menu. Stored by id (not hex) so the swatch tracks the token if the
// palette shifts; the same accent hues the launcher/badges use, so a
// tagged terminal reads in wash's one accent language.
interface TagColor {
  id: string;
  label: string;
  value: string;
}
const TAG_COLORS: TagColor[] = [
  { id: 'red', label: 'Red', value: tokens.accentRed },
  { id: 'amber', label: 'Amber', value: tokens.accentAmber },
  { id: 'green', label: 'Green', value: tokens.accentGreen },
  { id: 'cyan', label: 'Cyan', value: tokens.accentCyan },
  { id: 'blue', label: 'Blue', value: tokens.accentBlue },
  { id: 'violet', label: 'Violet', value: tokens.accentViolet },
];
const colorHex = (id?: string): string | undefined =>
  id ? TAG_COLORS.find((c) => c.id === id)?.value : undefined;

interface TabMeta {
  channelID: number;
  shell: string;
  // pending: restored from saved state and waiting for the BE's
  // `sessions` reply before the xterm mounts — the reply carries the
  // pty's current cols/rows so the scrollback replay renders at the
  // grid it was emitted for. Cleared by reconcile() (or its timeout
  // fallback, so a hung BE can't leave blank tabs forever).
  pending?: boolean;
  // init: the grid to open the restored xterm at (from `sessions`).
  init?: { cols: number; rows: number };
  // modes: last tracked terminal-mode state (alt-screen, bracketed
  // paste, mouse, …) — persisted so a reattach can re-seed modes
  // whose set-sequences scrolled out of the 256KB replay window.
  // Mutated in place (not reactive) — only read at persist/mount.
  modes?: TermModes;
}

// The on-the-wire/saved schema uses snake_case to match the rest of
// wash's JSON conventions.
interface PersistedTabRow {
  channel_id: number;
  shell: string;
  modes?: TermModes;
  // color: tag color id (see TAG_COLORS), or absent for untagged.
  color?: string;
}

// One row of the BE's `sessions` reply (list_sessions).
interface SessionRow {
  channel_id: number;
  shell?: string;
  cols?: number;
  rows?: number;
}

interface PersistedState {
  tabs?: PersistedTabRow[];
  active?: number;
  // Font choice is window-wide: every tab in this window shares it.
  font_id?: string;
  font_size?: number;
}

// TAB_BAR_HEIGHT — 32 (was 28) leaves 4px of breathing room above the
// tab buttons; the bar's padding-top puts it there. Without the gap
// the tabs render flush against the window titlebar and read as one
// flat block rather than a row of pickable controls.
const TAB_BAR_HEIGHT = 32;

const App: Component<{ instance: string; host: HTMLElement; origin: string }> = (props) => {
  const [tabs, setTabs] = createSignal<TabMeta[]>([]);
  const [active, setActive] = createSignal(0);
  // Window-wide font choice, driven into every <Terminal>. The
  // right-click menu reports changes back here so they persist and
  // apply across all tabs at once.
  const [fontId, setFontId] = createSignal(TERM_DEFAULT_FONT_ID);
  const [fontSize, setFontSize] = createSignal(TERM_DEFAULT_FONT_SIZE);

  // Per-tab color tag, keyed by channel id. Kept OUT of the TabMeta
  // objects on purpose: the term-host <For> below is keyed by object
  // identity, so replacing a tab's object to change its color would
  // remount its xterm and wipe scrollback (the same hazard reconcile()
  // guards against). A side map stays reactive without touching the
  // tab objects. Persisted alongside each tab row.
  const [tagColors, setTagColors] = createSignal<Map<number, string>>(new Map());
  // Per-tab right-click menu (color picker): the tab it targets and
  // the viewport coords to open at, or null when closed.
  const [ctxMenu, setCtxMenu] = createSignal<{ id: number; x: number; y: number } | null>(null);
  // Drag-to-reorder state: the tab being dragged and the tab it would
  // drop in front of (for the insertion cue). Both null when idle.
  const [dragId, setDragId] = createSignal<number | null>(null);
  const [dropTarget, setDropTarget] = createSignal<number | null>(null);

  // Imperative <Terminal> handles keyed by channel id. Populated
  // by each <Terminal>'s onReady callback; dropped on tab close.
  const apis = new Map<number, TerminalAPI>();
  // Last reported size per channel so resize messages have a value
  // even when called outside a fit() tick.
  const sizes = new Map<number, { cols: number; rows: number }>();

  const send = (m: unknown) => window.wash.sendAppMsg(props.instance, m);

  // Reconcile/persist timers (cleared on unmount).
  let pendingFallback: ReturnType<typeof setTimeout> | undefined;
  let modesTimer: ReturnType<typeof setTimeout> | undefined;

  // ---- tab lifecycle ----

  const addTab = (channelID: number, shellPath: string, extra?: Partial<TabMeta>) => {
    if (tabs().some((t) => t.channelID === channelID)) return;
    setTabs([...tabs(), { channelID, shell: shellPath, ...extra }]);
    setActive(channelID);
    persist();
    // xterm setup happens in the per-tab onMount below.
  };

  const removeTab = (channelID: number) => {
    apis.delete(channelID);
    sizes.delete(channelID);
    if (tagColors().has(channelID)) {
      const next = new Map(tagColors());
      next.delete(channelID);
      setTagColors(next);
    }
    const remaining = tabs().filter((t) => t.channelID !== channelID);
    setTabs(remaining);
    if (active() === channelID) {
      setActive(remaining[0]?.channelID ?? 0);
    }
    persist();
  };

  // ---- color tag (per tab, persisted) ----

  // setTabColor writes the side map, not the tab object, so the
  // tagged tab's xterm stays mounted. colorId undefined clears the tag.
  const setTabColor = (channelID: number, colorId?: string) => {
    const next = new Map(tagColors());
    if (colorId) next.set(channelID, colorId);
    else next.delete(channelID);
    setTagColors(next);
    setCtxMenu(null);
    persist();
  };

  // ---- drag to reorder (persisted) ----

  // moveTab drops the dragged tab in front of `beforeID`. It reuses the
  // existing tab objects (filter + splice keep references intact), so
  // the term-host <For> sees the same identities and no xterm remounts.
  const moveTab = (fromID: number, beforeID: number) => {
    if (fromID === beforeID) return;
    const cur = tabs();
    const dragged = cur.find((t) => t.channelID === fromID);
    if (!dragged) return;
    const rest = cur.filter((t) => t.channelID !== fromID);
    const at = rest.findIndex((t) => t.channelID === beforeID);
    if (at < 0) return;
    rest.splice(at, 0, dragged);
    setTabs(rest);
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

  // ---- font choice (window-wide, persisted) ----

  const changeFontId = (id: string) => {
    if (fontId() === id) return;
    setFontId(id);
    persist();
  };
  const changeFontSize = (px: number) => {
    if (fontSize() === px) return;
    setFontSize(px);
    persist();
  };

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
      case 'sessions':
        reconcile((m.sessions ?? []) as SessionRow[]);
        return;
    }
  };

  // reconcile aligns the restored tab list with the BE's live pty
  // set (the list_sessions reply). Restored state can be stale in
  // both directions: a pty that exited while the browser was
  // detached (its tab_closed was dropped — the router doesn't
  // buffer app_msgs for detached shells) leaves a dead tab, and a
  // save that never flushed can miss a live one. The reply also
  // carries each pty's current grid, which unblocks the pending
  // (not-yet-mounted) restored tabs at the right initial size.
  const reconcile = (rows: SessionRow[]) => {
    if (pendingFallback) {
      clearTimeout(pendingFallback);
      pendingFallback = undefined;
    }
    const live = new Map(rows.map((r) => [Number(r.channel_id), r]));
    const wasActive = active();
    for (const t of tabs()) {
      if (!live.has(t.channelID)) removeTab(t.channelID);
    }
    // Unblock pending tabs with their pty's grid. Only pending tabs
    // get fresh objects — replacing a mounted tab's object would
    // remount its xterm and wipe the live buffer.
    setTabs(
      tabs().map((t) => {
        if (!t.pending) return t;
        const r = live.get(t.channelID);
        const cols = Number(r?.cols ?? 0);
        const rws = Number(r?.rows ?? 0);
        return {
          ...t,
          pending: false,
          init: cols > 1 && rws > 1 ? { cols, rows: rws } : undefined,
        };
      }),
    );
    for (const [id, r] of live) {
      if (!tabs().some((t) => t.channelID === id)) {
        const cols = Number(r.cols ?? 0);
        const rws = Number(r.rows ?? 0);
        addTab(id, String(r.shell ?? 'shell'), {
          init: cols > 1 && rws > 1 ? { cols, rows: rws } : undefined,
        });
      }
    }
    // addTab steals activation; put it back if the original
    // active tab is still alive.
    if (wasActive && live.has(wasActive) && active() !== wasActive) {
      setActive(wasActive);
      persist();
    }
  };

  // ---- state persistence ----

  const persist = () => {
    if (!props.instance) return;
    const state: PersistedState = {
      tabs: tabs().map((t) => ({
        channel_id: t.channelID,
        shell: t.shell,
        modes: t.modes,
        color: tagColors().get(t.channelID),
      })),
      active: active() || undefined,
      font_id: fontId(),
      font_size: fontSize(),
    };
    send({ kind: 'save_state', state });
  };

  // onTabModes records a tab's tracked terminal-mode state and
  // persists it debounced — mode flips arrive in bursts (app start,
  // alt-screen enter/exit) and each persist is a router round-trip.
  const onTabModes = (tab: TabMeta, m: TermModes) => {
    tab.modes = m;
    if (modesTimer) clearTimeout(modesTimer);
    modesTimer = setTimeout(persist, 500);
  };

  const restoreFrom = (s: PersistedState) => {
    if (s.font_id) setFontId(s.font_id);
    if (s.font_size) setFontSize(s.font_size);
    // The restored list may be stale (ptys that died while the
    // browser was detached); ask the BE for the live set and
    // reconcile when the `sessions` reply lands. Restored tabs stay
    // pending (no xterm) until then — the reply carries the grid the
    // replay must render at. The fallback unblocks them at container
    // size if the reply never comes, so a hung BE degrades to the
    // old behaviour instead of blank tabs.
    send({ kind: 'list_sessions' });
    if (!s.tabs?.length) return;
    const tags = new Map<number, string>();
    for (const t of s.tabs) {
      addTab(Number(t.channel_id), t.shell, { pending: true, modes: t.modes });
      if (t.color) tags.set(Number(t.channel_id), t.color);
    }
    if (tags.size) setTagColors(tags);
    if (s.active && tabs().some((t) => t.channelID === s.active)) {
      setActive(s.active);
    }
    pendingFallback = setTimeout(() => {
      pendingFallback = undefined;
      if (tabs().some((t) => t.pending)) {
        setTabs(tabs().map((t) => (t.pending ? { ...t, pending: false } : t)));
      }
    }, 2000);
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
      if (pendingFallback) clearTimeout(pendingFallback);
      if (modesTimer) clearTimeout(modesTimer);
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
            // Tag hue for the top strip: a tagged tab shows its color
            // always (active or not); untagged falls back to the blue
            // active-accent / transparent idle pair.
            const tagHex = () => colorHex(tagColors().get(tab.channelID));
            const isDragging = () => dragId() === tab.channelID;
            const isDropBefore = () => dropTarget() === tab.channelID && dragId() !== tab.channelID;
            return (
              <button
                type="button"
                draggable={true}
                data-testid={`term-tab-${tab.channelID}`}
                style={{
                  background: isActive() ? tokens.bgRowSelected : 'transparent',
                  color: tokens.fg,
                  border: 'none',
                  'border-top': isActive()
                    ? `2px solid ${tagHex() ?? tokens.accentBlue}`
                    : tagHex()
                      ? `2px solid ${tagHex()}`
                      : '2px solid transparent',
                  // Rounded only on top — the bottom meets the bar's
                  // border-bottom flush, matching browser-tab idiom.
                  'border-radius': `${tokens.radiusLg}px ${tokens.radiusLg}px 0 0`,
                  padding: '0 6px 0 10px',
                  cursor: 'pointer',
                  font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
                  display: 'flex',
                  'align-items': 'center',
                  gap: '8px',
                  'max-width': '200px',
                  // Dim while dragged; left rule marks the drop slot.
                  opacity: isDragging() ? 0.4 : 1,
                  'box-shadow': isDropBefore() ? `inset 3px 0 0 ${tokens.accentBlue}` : undefined,
                }}
                onClick={() => activate(tab.channelID)}
                onContextMenu={(ev) => {
                  ev.preventDefault();
                  setCtxMenu({ id: tab.channelID, x: ev.clientX, y: ev.clientY });
                }}
                onDragStart={(ev) => {
                  setDragId(tab.channelID);
                  if (ev.dataTransfer) {
                    ev.dataTransfer.effectAllowed = 'move';
                    // Some browsers refuse to start a drag without data.
                    ev.dataTransfer.setData('text/plain', String(tab.channelID));
                  }
                }}
                onDragEnd={() => {
                  setDragId(null);
                  setDropTarget(null);
                }}
                onDragEnter={(ev) => {
                  if (dragId() === null || dragId() === tab.channelID) return;
                  ev.preventDefault();
                  setDropTarget(tab.channelID);
                }}
                onDragOver={(ev) => {
                  if (dragId() === null || dragId() === tab.channelID) return;
                  ev.preventDefault();
                  if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'move';
                }}
                onDrop={(ev) => {
                  const from = dragId();
                  if (from === null) return;
                  ev.preventDefault();
                  moveTab(from, tab.channelID);
                  setDragId(null);
                  setDropTarget(null);
                }}
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
      <Show when={ctxMenu()}>
        {(menu) => (
          <Menu x={menu().x} y={menu().y} data-testid="term-tab-ctx" onDismiss={() => setCtxMenu(null)}>
            <MenuItem
              label="No color"
              data-testid="term-tag-none"
              icon={<span style={swatchStyle('transparent', true)} />}
              onClick={() => setTabColor(menu().id, undefined)}
            />
            <For each={TAG_COLORS}>
              {(c) => (
                <MenuItem
                  label={c.label}
                  data-testid={`term-tag-${c.id}`}
                  icon={<span style={swatchStyle(c.value)} />}
                  onClick={() => setTabColor(menu().id, c.id)}
                />
              )}
            </For>
          </Menu>
        )}
      </Show>
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
                {/* Pending tabs (restored, awaiting the `sessions`
                    reply) mount no xterm yet: reconcile() replaces
                    the tab object, and <For> re-renders this row
                    with the pty's grid in tab.init. */}
                {!tab.pending && <Terminal
                  channelId={tab.channelID}
                  origin={props.origin}
                  customKeyHandler={onTermKey}
                  fontId={fontId()}
                  fontSize={fontSize()}
                  onFontIdChange={changeFontId}
                  onFontSizeChange={changeFontSize}
                  initialCols={tab.init?.cols}
                  initialRows={tab.init?.rows}
                  initialModes={tab.modes}
                  onModesChanged={(m) => onTabModes(tab, m)}
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
                />}
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

// swatchStyle — the color dot shown beside each entry in the tag menu.
// hollow renders the outlined "No color" chip.
function swatchStyle(color: string, hollow = false): JSX.CSSProperties {
  return {
    width: '12px',
    height: '12px',
    'border-radius': '50%',
    background: hollow ? 'transparent' : color,
    border: `1px solid ${hollow ? tokens.borderMenu : color}`,
    'box-sizing': 'border-box',
  };
}

const tabBarStyle: JSX.CSSProperties = {
  height: `${TAB_BAR_HEIGHT}px`,
  background: tokens.bgWindow,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  display: 'flex',
  'align-items': 'stretch',
  gap: '2px',
  // padding-top creates the gap above the tabs; tabs round into the
  // border-bottom line, matching how browser tabs sit on a bar.
  padding: '4px 4px 0',
  'overflow-x': 'auto',
  'overflow-y': 'hidden',
  font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
  'flex-shrink': 0,
};

const addBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fg,
  border: 'none',
  padding: '0 10px',
  cursor: 'pointer',
  font: `14px ${tokens.fontMono}`,
  opacity: 0.8,
};

// ---- custom element ----

defineWashApp('wash-app-term', (props) => <App {...props} />, {
  // background stays true black — the terminal canvas, not chrome.
  style: `display:flex;flex-direction:column;width:100%;height:100%;box-sizing:border-box;background:#000;color:${tokens.fg};overflow:hidden`,
});
