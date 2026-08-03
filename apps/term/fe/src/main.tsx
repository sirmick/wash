// wash-app-term: tabbed xterm.js wrapper with split panes. One floating
// window hosts a TREE of tab groups (docs/TERM_LAYOUT.md): each leaf group
// has its own tab strip and controls, each tab is a separate raw channel +
// Terminal instance, and an unsplit window is a single group — which is
// exactly what wash-term was before splits existed.
//
// Layout is computed rects, not nested DOM: every terminal host is a flat,
// absolutely-positioned child of the stage for its whole life, and a layout
// change writes only left/top/width/height. Reparenting a mounted xterm
// loses its buffer, so the flat host list is load-bearing, not a style
// choice (docs/TERM_LAYOUT.md §2). The tree lives in layout.ts as a pure
// kernel; this file is its renderer and command surface.
//
// xterm construction and raw-channel wiring live in @wash/ui's
// <Terminal>. This file owns the tab orchestration (open, close,
// switch, split, persist, keyboard shortcuts) and forwards an imperative
// handle from each <Terminal> via onReady so tab activation can
// trigger focus/fit.

import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Check, Columns2, Globe, Plus, Rows2, ShieldAlert, User, X } from 'lucide-solid';
import {
  Button,
  Menu, MenuItem, MenuSeparator, Terminal,
  TERM_DEFAULT_FONT_ID, TERM_DEFAULT_FONT_SIZE, TERM_FONTS,
  TERM_MIN_FONT_SIZE, TERM_MAX_FONT_SIZE, TERM_THEMES, themeById,
  defineWashApp, tokens, WASH_SCROLL_CLASS,
} from '@wash/ui';
import type { PasteAnalysis, TermModes, TerminalAPI } from '@wash/ui';
import { analyzePaste } from '@wash/ui';
import { PasteOverlay } from './PasteOverlay';
import {
  DEFAULT_GUTTER, ROOT,
  addTab as treeAddTab, canSplit, channels as treeChannels, closeTab as treeCloseTab,
  focusNeighbor, fromPersisted, groupAt, groupPaths, layout as layoutTree,
  moveTabBefore, pathOfChannel, pruneToChannels, setActiveTab, singleGroup,
  splitGroup, toPersisted,
} from './layout';
import type { Dir, FocusDir, LayoutNode, PersistedNode, PlacedGroup, Rect } from './layout';

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

// TabStatus is the BE's per-tab `tab_status` poll (≈1Hz, sent only on
// change): what the tab's foreground program is running as. Drives the
// per-tab badge and the bottom status line. Ephemeral — never persisted.
interface TabStatus {
  state: 'user' | 'root' | 'ssh';
  user: string; // login name (for the "user" state)
  host: string; // short box name, e.g. "ai"
  target: string; // ssh destination host (for the "ssh" state)
}

// AgentStatus is the BE's per-tab `agent_status` push (docs/AGENT_TERM.md
// §5): a coding agent detected in this tab, and what it's doing. Drives the
// tab's state dot and the "· claude working 4m" clause in the status line.
// Ephemeral — never persisted, re-seeded by the BE after a reattach.
interface AgentStatus {
  agent: string; // slug: "claude", "codex", …
  // running: detected in the foreground but not reporting (tier T0, or an
  // agent that has started but isn't in a turn). The other three come
  // from the agent's own hooks.
  state: 'running' | 'working' | 'needs-input' | 'done';
  // startedAt: local clock anchor for the elapsed counter, derived once
  // from the BE's since_ms so the FE can tick without further messages.
  startedAt: number;
  sessionId: string;
  reason: string; // qualifies needs-input: "permission" | "idle"
}

const AGENT_STATES = ['running', 'working', 'needs-input', 'done'] as const;

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

// Menubar menus, in bar order.
type MenuId = 'edit' | 'tab' | 'split' | 'theme' | 'font' | 'paste';

// SmartPaste is the window-wide policy for the paste filter
// (docs/AGENT_TERM.md §10):
//   ask    — clean the invisible junk silently, ask before changing structure
//   always — apply the repair without asking
//   off    — don't analyze at all; paste exactly what was copied
type SmartPaste = 'ask' | 'always' | 'off';

interface PersistedState {
  // v2 carries a layout tree; a blob without it is v1 (one group, in the
  // saved tab order) and migrates on restore. See docs/TERM_LAYOUT.md §7.
  v?: number;
  tabs?: PersistedTabRow[];
  layout?: PersistedNode;
  // v1 only: the single bar's active tab. v2 keeps activation per group.
  active?: number;
  // Font choice is window-wide: every tab in this window shares it.
  font_id?: string;
  font_size?: number;
  // Pinned terminal palette id (TERM_THEMES); absent = follow the pack.
  theme_id?: string;
  // Legacy: the old binary palette override, read on restore and
  // migrated to theme_id. No longer written.
  appearance?: 'dark' | 'light';
  smart_paste?: SmartPaste;
}

// STRIP_HEIGHT — every group carries its own tab strip, so this is paid
// once per pane. 26px is the compromise the mock settled on: tall enough
// for a tab with a badge and the control icons, slim enough that a
// three-way split doesn't eat a fifth of the window. (The old single bar
// was 32 with a 4px gap above; there is no window titlebar to separate
// from any more once strips sit inside the stage.)
const STRIP_HEIGHT = 26;
// Divider thickness between sibling panes.
const GUTTER = DEFAULT_GUTTER;

// Tab labels cap here (chars) before ellipsis — a shell sets the OSC
// title to "user@host: /long/cwd", which would otherwise stretch the tab.
const TAB_LABEL_MAX = 12;

const App: Component<{ instance: string; host: HTMLElement; origin: string }> = (props) => {
  // tabs is the channel INVENTORY — one entry per live pty, in no
  // particular order. Placement lives in the tree; these objects only carry
  // per-channel facts (shell, restore grid, modes). The terminal-host <For>
  // below is keyed by these objects, so they are never replaced except by
  // reconcile()'s pending→live promotion.
  const [tabs, setTabs] = createSignal<TabMeta[]>([]);
  // The layout tree (docs/TERM_LAYOUT.md §3) and the focused group's path.
  // Every placement question goes through these two.
  const [tree, setTree] = createSignal<LayoutNode>(singleGroup([]));
  const [focusPath, setFocusPath] = createSignal<string>(ROOT);
  // Live stage size, from a ResizeObserver on the pane container. Rects are
  // computed in px, so the layout has to be recomputed when the window
  // resizes; each <Terminal> then refits itself off its own observer.
  const [stage, setStage] = createSignal<{ w: number; h: number }>({ w: 0, h: 0 });

  // placement is the whole render contract: where every group and divider
  // goes. Recomputed when the tree or the stage changes, nothing else.
  const placement = createMemo(() => {
    const s = stage();
    return layoutTree(tree(), { x: 0, y: 0, w: s.w, h: s.h }, { gutter: GUTTER, strip: STRIP_HEIGHT });
  });
  // Group paths are stable strings, so <For> reuses strip rows across a
  // relayout instead of rebuilding them on every resize tick.
  const placedPaths = createMemo(() => placement().groups.map((g) => g.path));
  const placedAt = (path: string): PlacedGroup | undefined =>
    placement().groups.find((g) => g.path === path);

  // focusedGroup falls back to the first group when the focused path went
  // stale (its group collapsed) — there is always exactly one focus.
  const focusedGroup = (): PlacedGroup | undefined =>
    placedAt(focusPath()) ?? placement().groups[0];

  // active is the focused group's visible tab — the channel every
  // window-level surface (status bar, Edit menu, paste) talks about.
  const active = (): number => focusedGroup()?.group.active ?? 0;

  // A pending split: the BE round-trip for a new tab is asynchronous, so a
  // split records where the tab should land and applies it when tab_opened
  // arrives. FIFO, so two fast Ctrl+Shift+D presses land in order.
  let splitIntents: Array<{ path: string; dir: Dir }> = [];
  // Window-wide font choice, driven into every <Terminal>. The
  // right-click menu reports changes back here so they persist and
  // apply across all tabs at once.
  const [fontId, setFontId] = createSignal(TERM_DEFAULT_FONT_ID);
  const [fontSize, setFontSize] = createSignal(TERM_DEFAULT_FONT_SIZE);
  // Window-wide terminal palette: undefined follows the desktop pack
  // appearance (default); a TERM_THEMES id pins a named palette (Dark,
  // Solarized Dark, Dracula, …). Set via the Theme menu; persisted like
  // the font choice.
  const [themeId, setThemeId] = createSignal<string | undefined>(undefined);

  // Smart paste (docs/AGENT_TERM.md §10): window-wide policy, and the
  // pending analysis while its preview overlay is open. resolve is the
  // Terminal's beforePaste promise — exactly one of the three buttons
  // settles it, and dismissing counts as Cancel.
  const [smartPaste, setSmartPaste] = createSignal<SmartPaste>('ask');
  const [pendingPaste, setPendingPaste] = createSignal<{
    analysis: PasteAnalysis;
    resolve: (text: string | null) => void;
  } | null>(null);

  // Menubar: which top menu is open and the viewport anchor (the clicked
  // button's bottom-left) to paint it at.
  const [openMenu, setOpenMenu] = createSignal<MenuId | null>(null);
  const [menuAnchor, setMenuAnchor] = createSignal<{ x: number; y: number }>({ x: 0, y: 0 });

  // Per-tab live OSC title (set by the program via ESC]0;…), keyed by
  // channel id. Kept in a side map (like tagColors) so updating a title
  // doesn't replace the TabMeta object and remount the xterm. The tab
  // shows this over the shell name, truncated; empty/absent = fall back
  // to the shell basename. Ephemeral: not persisted.
  const [tabTitles, setTabTitles] = createSignal<Map<number, string>>(new Map());
  // Per-tab user badge/status from the BE poll (see TabStatus).
  const [tabStatus, setTabStatus] = createSignal<Map<number, TabStatus>>(new Map());
  // Per-tab agent status (see AgentStatus). Same side-map discipline as
  // tabStatus/tagColors — the term-host <For> is keyed by object identity,
  // so anything that changes per tab lives OUTSIDE the TabMeta objects or
  // the xterm remounts and scrollback is lost.
  const [agentStatus, setAgentStatus] = createSignal<Map<number, AgentStatus>>(new Map());
  // Coarse clock for the agent elapsed counter ("working 4m"). Ticks once
  // a second and only while some tab has an agent, so an ordinary terminal
  // window costs nothing.
  const [now, setNow] = createSignal(Date.now());

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
  // The pane container. Its size drives every rect; each <Terminal> then
  // refits off its OWN observer once its host's rect changes, so this
  // observer never has to talk to xterm.
  let stageEl: HTMLDivElement | undefined;

  const send = (m: unknown) => window.wash.sendAppMsg(props.instance, m);

  // Reconcile/persist timers (cleared on unmount).
  let pendingFallback: ReturnType<typeof setTimeout> | undefined;
  let modesTimer: ReturnType<typeof setTimeout> | undefined;

  // ---- tab lifecycle ----

  // addTab records the channel and places it in the tree: into the group a
  // pending split named, else into the focused group. A tab that arrives
  // for a split takes focus in its new pane, which is what "split right"
  // means — you end up typing in the new one.
  const addTab = (channelID: number, shellPath: string, extra?: Partial<TabMeta>) => {
    if (tabs().some((t) => t.channelID === channelID)) return;
    setTabs([...tabs(), { channelID, shell: shellPath, ...extra }]);

    const intent = splitIntents.shift();
    const next = intent && groupAt(tree(), intent.path)
      ? splitGroup(tree(), intent.path, intent.dir, channelID)
      : treeAddTab(tree(), focusPath(), channelID);
    setTree(next);
    const landed = pathOfChannel(next, channelID);
    if (landed !== undefined) setFocusPath(landed);
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
    if (tabTitles().has(channelID)) {
      const next = new Map(tabTitles());
      next.delete(channelID);
      setTabTitles(next);
    }
    if (tabStatus().has(channelID)) {
      const next = new Map(tabStatus());
      next.delete(channelID);
      setTabStatus(next);
    }
    if (agentStatus().has(channelID)) {
      const next = new Map(agentStatus());
      next.delete(channelID);
      setAgentStatus(next);
    }
    const remaining = tabs().filter((t) => t.channelID !== channelID);
    setTabs(remaining);
    // The tree decides what happens to the pane: the group activates a
    // neighbouring tab, or — if that was its last — collapses and hands its
    // space back to its siblings.
    const next = treeCloseTab(tree(), channelID);
    setTree(next);
    refocusAfterCollapse(next);
    persist();
  };

  // adoptOrphans places any inventoried channel the tree doesn't hold. It
  // covers two real cases: a restore where the saved tree and the saved tab
  // list disagree, and a tab opened by another surface entirely (agentd's
  // exec_tab, docs/AGENT_TERM.md §13).
  const adoptOrphans = () => {
    let next = tree();
    let changed = false;
    for (const t of tabs()) {
      if (pathOfChannel(next, t.channelID) !== undefined) continue;
      next = treeAddTab(next, focusPath(), t.channelID);
      changed = true;
    }
    if (changed) setTree(next);
  };

  // refocusAfterCollapse keeps the focus on a group that still exists.
  // Paths move when the tree changes, so this is by path validity, not by
  // remembering an object.
  const refocusAfterCollapse = (next: LayoutNode) => {
    if (groupAt(next, focusPath())) return;
    setFocusPath(groupPaths(next)[0] ?? ROOT);
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

  // moveTab drops the dragged tab in front of `beforeID` — a reorder inside
  // one strip, or a move between groups when the strips differ. The tab
  // INVENTORY is untouched either way: only the tree changes, so the
  // terminal's DOM node never moves and its buffer is never at risk.
  const moveTab = (fromID: number, beforeID: number) => {
    if (fromID === beforeID) return;
    const next = moveTabBefore(tree(), fromID, beforeID);
    if (next === tree()) return;
    setTree(next);
    const landed = pathOfChannel(next, fromID);
    if (landed !== undefined) setFocusPath(landed);
    refocusAfterCollapse(next);
    persist();
  };

  // activate makes a channel the visible tab of its group AND focuses that
  // group — clicking a tab in an unfocused pane moves you there.
  const activate = (channelID: number) => {
    const path = pathOfChannel(tree(), channelID);
    if (path === undefined) return;
    const already = active() === channelID && focusPath() === path;
    if (!already) {
      setTree(setActiveTab(tree(), channelID));
      setFocusPath(path);
      persist();
    }
    requestAnimationFrame(() => {
      const api = apis.get(channelID);
      if (api) {
        api.fit();
        api.focus();
      }
    });
  };

  // focusGroup moves the focus ring without changing which tab is visible.
  const focusGroup = (path: string) => {
    if (focusPath() === path || !groupAt(tree(), path)) return;
    setFocusPath(path);
    persist();
    apis.get(groupAt(tree(), path)!.active)?.focus();
  };

  const sendResize = (channelID: number, cols: number, rows: number) => {
    send({ kind: 'resize', channel_id: channelID, cols, rows });
  };

  const openNewTab = () => send({ kind: 'new_tab' });
  const requestCloseTab = (channelID: number) => {
    if (channelID) send({ kind: 'close_tab', channel_id: channelID });
  };

  // ---- split commands (docs/TERM_LAYOUT.md §5) ----
  //
  // The strip buttons, the Split menu and the keybindings all land here, so
  // "split the focused pane" has exactly one implementation.

  // splitFocused asks the BE for a pty and records where its tab should go.
  // A split that would leave either half unusable is refused outright rather
  // than opening a pane you can't read — canSplit measures the pane the user
  // is actually looking at, so the answer changes with the window size.
  const splitAt = (path: string, dir: Dir) => {
    const g = placedAt(path);
    if (!g || !canSplit(g.rect, dir, { gutter: GUTTER })) return;
    focusGroup(path);
    splitIntents.push({ path: g.path, dir });
    openNewTab();
  };
  const splitFocused = (dir: Dir) => splitAt(focusedGroup()?.path ?? ROOT, dir);

  // openNewTabIn puts the next tab in a named group — the `+` on a strip
  // means "another tab HERE", not "another tab wherever the focus is".
  const openNewTabIn = (path: string) => {
    focusGroup(path);
    openNewTab();
  };

  const canSplitFocused = (dir: Dir): boolean => {
    const g = focusedGroup();
    return !!g && canSplit(g.rect, dir, { gutter: GUTTER });
  };

  const paneCount = (): number => placement().groups.length;

  // focusDir moves the focus ring geometrically — the pane under the arrow,
  // which in a nested tree is routinely not the tree-order neighbour.
  const focusDir = (dir: FocusDir) => {
    const from = focusedGroup();
    if (!from) return;
    const to = focusNeighbor(placement().groups, from.path, dir);
    if (to !== undefined) focusGroup(to);
  };

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

  // ---- theme (window-wide, persisted) ----

  // changeTheme pins a named palette by id, or undefined to follow the
  // desktop pack. The live switch reaches the mounted xterm via the
  // Terminal's `theme` prop effect — no remount.
  const changeTheme = (id: string | undefined) => {
    setThemeId(id);
    persist();
  };

  // ---- menubar ----

  // activeApi is the imperative handle of the focused tab's terminal, or
  // undefined while a tab is still pending (no xterm mounted yet).
  const activeApi = (): TerminalAPI | undefined => apis.get(active());
  // openMenuFor toggles the named top menu, anchoring it under the button.
  const openMenuFor = (id: MenuId, ev: MouseEvent) => {
    if (openMenu() === id) { setOpenMenu(null); return; }
    const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    setMenuAnchor({ x: r.left, y: r.bottom + 2 });
    setOpenMenu(id);
  };
  const closeMenu = () => setOpenMenu(null);
  // run wraps a menu action so the menu closes before it fires (and focus
  // returns to the terminal, so a following paste/type lands in the pty).
  const run = (fn: () => void) => () => { closeMenu(); fn(); activeApi()?.focus(); };

  // stepFontSize nudges the window-wide font size within the supported
  // range. Used by the Font menu's −/+ stepper; deliberately does NOT
  // close the menu, so the user can step several times.
  const stepFontSize = (delta: number) => {
    const next = Math.max(TERM_MIN_FONT_SIZE, Math.min(TERM_MAX_FONT_SIZE, fontSize() + delta));
    changeFontSize(next);
  };

  // ---- smart paste ----

  // beforePaste is the filter every paste path in the terminal component
  // funnels through (§10). It resolves with the text to send, or null to
  // send nothing. The three outcomes:
  //   off / nothing found → the original, untouched
  //   junk only, one line → cleaned, silently (an invisible character is
  //                         not worth a dialog)
  //   structure or multi-line → the overlay, and the user picks
  const beforePaste = (text: string, ctx: { bracketedPaste?: boolean }): Promise<string | null> => {
    if (smartPaste() === 'off') return Promise.resolve(text);
    const analysis = analyzePaste(text, { bracketedPaste: ctx.bracketedPaste });
    if (analysis.verdict === 'as-is') return Promise.resolve(text);
    if (analysis.verdict === 'clean' || smartPaste() === 'always') {
      return Promise.resolve(analysis.cleaned);
    }
    // Only one preview at a time: a second paste while the overlay is open
    // cancels the first rather than stacking dialogs.
    const prev = pendingPaste();
    if (prev) {
      prev.resolve(null);
      setPendingPaste(null);
    }
    return new Promise<string | null>((resolve) => {
      setPendingPaste({ analysis, resolve });
    });
  };

  // settlePaste answers the open preview and closes it. Focus goes back to
  // the terminal so the next keystroke lands in the pty, not the chrome.
  const settlePaste = (text: string | null) => {
    const p = pendingPaste();
    setPendingPaste(null);
    p?.resolve(text);
    activeApi()?.focus();
  };

  const changeSmartPaste = (mode: SmartPaste) => {
    if (smartPaste() === mode) return;
    setSmartPaste(mode);
    persist();
  };

  // ---- tab title (live OSC title, ephemeral) ----

  const setTabTitle = (channelID: number, title: string) => {
    const t = title.trim();
    const cur = tabTitles().get(channelID) ?? '';
    if (t === cur) return;
    const next = new Map(tabTitles());
    if (t) next.set(channelID, t);
    else next.delete(channelID);
    setTabTitles(next);
  };

  // fullLabel is the untruncated tab label (OSC title, else shell
  // basename) — also used as the button's hover tooltip. tabLabel caps
  // it to TAB_LABEL_MAX chars with an ellipsis so a long "user@host: cwd"
  // title can't blow out the tab width.
  const fullLabel = (tab: TabMeta): string =>
    (tabTitles().get(tab.channelID) ?? '').trim() || shortShellName(tab.shell);
  const tabLabel = (tab: TabMeta): string => {
    const s = fullLabel(tab);
    return s.length > TAB_LABEL_MAX ? s.slice(0, TAB_LABEL_MAX - 1) + '…' : s;
  };

  // ---- per-tab user badge + status line ----

  // statusBadge is the small icon shown in a tab and in the status bar:
  // red shield = root, blue globe = ssh, muted user = normal. color
  // overrides the default (e.g. white on the red root status bar).
  const statusBadge = (s: TabStatus | undefined, color?: string): JSX.Element => {
    if (!s) return null;
    if (s.state === 'root') return <ShieldAlert size={12} color={color ?? tokens.accentRed} />;
    if (s.state === 'ssh') return <Globe size={12} color={color ?? tokens.accentBlue} />;
    return <User size={12} color={color} style={{ opacity: 0.55 }} />;
  };

  const activeStatus = (): TabStatus | undefined => tabStatus().get(active());
  const isRootActive = (): boolean => activeStatus()?.state === 'root';

  // ---- agent status (tab dot + status-line clause) ----

  // agentDot is the small filled circle beside the user badge: blue while
  // the agent works, amber when it wants the human, green when it's done,
  // muted grey for "running but not reporting" (tier T0, no hooks). It is
  // the whole of M1's visible surface, so it carries the state in a data
  // attribute for e2e to assert on.
  const agentDot = (a: AgentStatus | undefined, testid: string): JSX.Element => {
    if (!a) return null;
    return (
      <span
        data-testid={testid}
        data-agent={a.agent}
        data-agent-state={a.state}
        title={agentTitle(a)}
        style={{
          width: '7px',
          height: '7px',
          'border-radius': '50%',
          background: agentColor(a.state),
          'flex-shrink': 0,
          display: 'inline-block',
        }}
      />
    );
  };

  const agentTitle = (a: AgentStatus): string => {
    const what = a.state === 'needs-input' && a.reason
      ? `needs input (${a.reason})`
      : a.state;
    return `${a.agent} ${what} · ${elapsed(a.startedAt)}`;
  };

  // agentText is the status-line clause appended after the shell sentence:
  // "bash as mick on ai · claude working 4m".
  const agentText = (a: AgentStatus | undefined): string => {
    if (!a) return '';
    const what = a.state === 'needs-input' ? 'needs input' : a.state;
    return `· ${a.agent} ${what} ${elapsed(a.startedAt)}`;
  };

  // elapsed renders a duration the way a glanceable status line wants it:
  // seconds under a minute, then minutes, then hours. now() makes it live.
  const elapsed = (startedAt: number): string => {
    const secs = Math.max(0, Math.floor((now() - startedAt) / 1000));
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m`;
    return `${Math.floor(secs / 3600)}h`;
  };

  const activeAgent = (): AgentStatus | undefined => agentStatus().get(active());

  // statusText composes the bottom-bar sentence for the active tab:
  //   ssh  → "ssh to ‘xyz’"
  //   root → "bash as root on ai"
  //   user → "bash as mick on ai"
  const statusText = (): string => {
    const tab = tabs().find((t) => t.channelID === active());
    if (!tab) return '';
    const shell = shortShellName(tab.shell);
    const s = activeStatus();
    if (!s) return shell;
    if (s.state === 'ssh') return s.target ? `ssh to ‘${s.target}’` : 'ssh session';
    const who = s.state === 'root' ? 'root' : s.user || 'user';
    return s.host ? `${shell} as ${who} on ${s.host}` : `${shell} as ${who}`;
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
      case 'tab_status': {
        const id = Number(m.channel_id);
        const state = String(m.state);
        const next = new Map(tabStatus());
        next.set(id, {
          state: state === 'root' || state === 'ssh' ? state : 'user',
          user: String(m.user ?? ''),
          host: String(m.host ?? ''),
          target: String(m.target ?? ''),
        });
        setTabStatus(next);
        return;
      }
      case 'agent_status': {
        const id = Number(m.channel_id);
        const state = String(m.state ?? '');
        const next = new Map(agentStatus());
        if (!AGENT_STATES.includes(state as AgentStatus['state'])) {
          // Empty state = "no agent in this tab any more" (the agent
          // exited, or its SessionEnd hook fired).
          next.delete(id);
        } else {
          next.set(id, {
            agent: String(m.agent ?? 'agent'),
            state: state as AgentStatus['state'],
            // since_ms is how long the BE has held this state; anchor the
            // local clock to it so the counter keeps running between
            // messages (they only arrive on change).
            startedAt: Date.now() - Math.max(0, Number(m.since_ms ?? 0)),
            sessionId: String(m.session_id ?? ''),
            reason: String(m.reason ?? ''),
          });
        }
        setAgentStatus(next);
        return;
      }
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
    const wasFocused = focusPath();
    for (const t of tabs()) {
      if (!live.has(t.channelID)) removeTab(t.channelID);
    }
    // Prune the tree to what the BE still has. removeTab above already
    // walked the live tabs, but a saved tree can name channels that never
    // made it into the inventory at all — this is the one place that
    // guarantees no pane refers to a dead pty.
    const pruned = pruneToChannels(tree(), new Set(live.keys()));
    if (pruned !== tree()) {
      setTree(pruned);
      refocusAfterCollapse(pruned);
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
    // addTab steals focus and activation; put both back if the tab that
    // had them is still alive.
    if (wasActive && live.has(wasActive)) {
      const path = pathOfChannel(tree(), wasActive);
      if (path !== undefined) {
        setTree(setActiveTab(tree(), wasActive));
        setFocusPath(groupAt(tree(), wasFocused) ? wasFocused : path);
        persist();
      }
    }
  };

  // ---- state persistence ----

  const persist = () => {
    if (!props.instance) return;
    const state: PersistedState = {
      v: 2,
      tabs: tabs().map((t) => ({
        channel_id: t.channelID,
        shell: t.shell,
        modes: t.modes,
        color: tagColors().get(t.channelID),
      })),
      layout: toPersisted(tree()),
      font_id: fontId(),
      font_size: fontSize(),
      theme_id: themeId(),
      smart_paste: smartPaste(),
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
    // theme_id is the current field; fall back to the legacy `appearance`
    // ('dark'/'light' map 1:1 to the same-named theme ids) so windows
    // saved before named themes keep their palette.
    if (s.theme_id) setThemeId(s.theme_id);
    else if (s.appearance) setThemeId(s.appearance);
    if (s.smart_paste === 'ask' || s.smart_paste === 'always' || s.smart_paste === 'off') {
      setSmartPaste(s.smart_paste);
    }
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
    // Placement: a v2 blob restores its tree; a v1 blob (no layout, just an
    // ordered tab list and one active id) migrates to a single group, which
    // is the same window it was saved from. Either way the tree is then
    // pruned to what the BE actually still has, in reconcile().
    const restored = s.layout ? fromPersisted(s.layout) : undefined;
    const known = new Set(tabs().map((t) => t.channelID));
    if (restored && treeChannels(restored).every((c) => known.has(c))) {
      setTree(restored);
    } else {
      setTree(singleGroup(s.tabs.map((t) => Number(t.channel_id)), s.active));
    }
    setFocusPath(groupPaths(tree())[0] ?? ROOT);
    // A tab in the inventory that the tree doesn't place would be a mounted
    // terminal with nowhere to draw. Adopt any straggler rather than
    // trusting the two halves of the blob to agree.
    adoptOrphans();
    pendingFallback = setTimeout(() => {
      pendingFallback = undefined;
      if (tabs().some((t) => t.pending)) {
        setTabs(tabs().map((t) => (t.pending ? { ...t, pending: false } : t)));
      }
    }, 2000);
  };

  // ---- keyboard shortcuts ----

  // Ctrl+Shift+<letter> has no distinct control code, so none of these are
  // stolen from the shell (or from an agent running in it). Returning false
  // keeps the event out of the pty entirely.
  const onTermKey = (ev: KeyboardEvent): boolean => {
    if (ev.type !== 'keydown') return true;
    if (ev.ctrlKey && ev.shiftKey) {
      const k = ev.key.toLowerCase();
      if (k === 't') { openNewTab(); return false; }
      // Closes the TAB; when it is the last one in its group the pane goes
      // with it and the tree hands the space back (docs/TERM_LAYOUT.md §5).
      if (k === 'w') { requestCloseTab(active()); return false; }
      if (k === 'd') { splitFocused('row'); return false; }
      if (k === 'e') { splitFocused('col'); return false; }
      const arrows: Record<string, FocusDir> = {
        arrowleft: 'left', arrowright: 'right', arrowup: 'up', arrowdown: 'down',
      };
      const dir = arrows[k];
      if (dir) { ev.preventDefault(); focusDir(dir); return false; }
    }
    if (ev.ctrlKey && ev.key === 'Tab') {
      ev.preventDefault();
      cycleTabs(ev.shiftKey ? -1 : 1);
      return false;
    }
    return true;
  };

  // cycleTabs walks the FOCUSED GROUP's strip — Ctrl+Tab is a tab gesture,
  // not a pane gesture (that is Ctrl+Shift+arrows).
  const cycleTabs = (dir: number) => {
    const ids = focusedGroup()?.group.tabs ?? [];
    if (ids.length < 2) return;
    const i = ids.indexOf(active());
    if (i < 0) return;
    activate(ids[(i + dir + ids.length) % ids.length]);
  };

  // ---- tab button (one per tab, inside its group's strip) ----

  // tabButton renders a strip entry. Identity is the CHANNEL id, and the
  // group is passed in rather than looked up, so a click activates the tab
  // in the right pane even while another one holds focus.
  const tabButton = (channelID: number, path: string): JSX.Element => {
    const tab = () => tabs().find((t) => t.channelID === channelID);
    const isActive = () => groupAt(tree(), path)?.active === channelID;
    // Tag hue for the top strip: a tagged tab shows its color
    // always (active or not); untagged falls back to the blue
    // active-accent / transparent idle pair.
    const tagHex = () => colorHex(tagColors().get(channelID));
    const isDragging = () => dragId() === channelID;
    const isDropBefore = () => dropTarget() === channelID && dragId() !== channelID;
    return (
      <Show when={tab()}>
        <button
          type="button"
          draggable={true}
          data-testid={`term-tab-${channelID}`}
          style={{
            background: isActive() ? tokens.bgRowSelected : 'transparent',
            color: tokens.fg,
            border: 'none',
            'border-top': isActive()
              ? `2px solid ${tagHex() ?? tokens.accentBlue}`
              : tagHex()
                ? `2px solid ${tagHex()}`
                : '2px solid transparent',
            // Rounded only on top — the bottom meets the strip's
            // border-bottom flush, matching browser-tab idiom.
            'border-radius': `${tokens.radiusLg} ${tokens.radiusLg} 0 0`,
            padding: '0 4px 0 8px',
            cursor: 'pointer',
            font: tokens.type.monoMd,
            display: 'flex',
            'align-items': 'center',
            gap: '6px',
            'max-width': '200px',
            'flex-shrink': 0,
            // Dim while dragged; left rule marks the drop slot.
            opacity: isDragging() ? 0.4 : 1,
            'box-shadow': isDropBefore() ? `inset 3px 0 0 ${tokens.accentBlue}` : undefined,
          }}
          onClick={() => activate(channelID)}
          onContextMenu={(ev) => {
            ev.preventDefault();
            setCtxMenu({ id: channelID, x: ev.clientX, y: ev.clientY });
          }}
          onDragStart={(ev) => {
            setDragId(channelID);
            if (ev.dataTransfer) {
              ev.dataTransfer.effectAllowed = 'move';
              // Some browsers refuse to start a drag without data.
              ev.dataTransfer.setData('text/plain', String(channelID));
            }
          }}
          onDragEnd={() => {
            setDragId(null);
            setDropTarget(null);
          }}
          onDragEnter={(ev) => {
            if (dragId() === null || dragId() === channelID) return;
            ev.preventDefault();
            setDropTarget(channelID);
          }}
          onDragOver={(ev) => {
            if (dragId() === null || dragId() === channelID) return;
            ev.preventDefault();
            if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'move';
          }}
          onDrop={(ev) => {
            const from = dragId();
            if (from === null) return;
            ev.preventDefault();
            moveTab(from, channelID);
            setDragId(null);
            setDropTarget(null);
          }}
        >
          <span
            data-testid={`term-tab-badge-${channelID}`}
            style={{ display: 'inline-flex', 'align-items': 'center', gap: '5px', 'flex-shrink': 0 }}
          >
            {statusBadge(tabStatus().get(channelID))}
            {agentDot(agentStatus().get(channelID), `term-tab-agent-${channelID}`)}
          </span>
          <span
            title={fullLabel(tab()!)}
            style={{
              overflow: 'hidden',
              'text-overflow': 'ellipsis',
              'white-space': 'nowrap',
            }}
          >
            {tabLabel(tab()!)}
          </span>
          <span
            data-testid={`term-tab-close-${channelID}`}
            style={{
              opacity: 0.6,
              cursor: 'pointer',
              padding: '0 2px',
              display: 'inline-flex',
              'align-items': 'center',
            }}
            onClick={(ev) => {
              ev.stopPropagation();
              requestCloseTab(channelID);
            }}
          >
            <X size={12} />
          </span>
        </button>
      </Show>
    );
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

    // Stage size → rects. Seeded synchronously so the first paint has a
    // real layout rather than a 0×0 one (which would leave every pane
    // hidden until the first observer tick).
    const measure = () => {
      if (!stageEl) return;
      const r = stageEl.getBoundingClientRect();
      const w = Math.round(r.width);
      const h = Math.round(r.height);
      const cur = stage();
      if (cur.w === w && cur.h === h) return;
      setStage({ w, h });
    };
    measure();
    const ro = new ResizeObserver(measure);
    if (stageEl) ro.observe(stageEl);

    // Elapsed-time ticker for the agent clause. Runs only while a tab
    // actually has an agent — createEffect re-evaluates when the agent map
    // changes, so an ordinary terminal never holds an interval.
    let tick: ReturnType<typeof setInterval> | undefined;
    createEffect(() => {
      const wanted = agentStatus().size > 0;
      if (wanted && tick === undefined) {
        setNow(Date.now());
        tick = setInterval(() => setNow(Date.now()), 1000);
      } else if (!wanted && tick !== undefined) {
        clearInterval(tick);
        tick = undefined;
      }
    });

    onCleanup(() => {
      ro.disconnect();
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('wash:state', onState);
      if (pendingFallback) clearTimeout(pendingFallback);
      if (modesTimer) clearTimeout(modesTimer);
      if (tick !== undefined) clearInterval(tick);
      apis.clear();
      sizes.clear();
    });
  });

  return (
    <>
      <div data-testid="term-menubar" style={menuBarStyle}>
        <button
          type="button"
          data-testid="term-menu-edit-btn"
          style={menuBarBtnStyle(openMenu() === 'edit')}
          onClick={(ev) => openMenuFor('edit', ev)}
        >
          Edit
        </button>
        <button
          type="button"
          data-testid="term-menu-tab-btn"
          style={menuBarBtnStyle(openMenu() === 'tab')}
          onClick={(ev) => openMenuFor('tab', ev)}
        >
          Tab
        </button>
        <button
          type="button"
          data-testid="term-menu-split-btn"
          style={menuBarBtnStyle(openMenu() === 'split')}
          onClick={(ev) => openMenuFor('split', ev)}
        >
          Split
        </button>
        <button
          type="button"
          data-testid="term-menu-theme-btn"
          style={menuBarBtnStyle(openMenu() === 'theme')}
          onClick={(ev) => openMenuFor('theme', ev)}
        >
          Theme
        </button>
        <button
          type="button"
          data-testid="term-menu-paste-btn"
          style={menuBarBtnStyle(openMenu() === 'paste')}
          onClick={(ev) => openMenuFor('paste', ev)}
        >
          Paste
        </button>
        <button
          type="button"
          data-testid="term-menu-font-btn"
          style={menuBarBtnStyle(openMenu() === 'font')}
          onClick={(ev) => openMenuFor('font', ev)}
        >
          Font
        </button>
      </div>
      <Show when={openMenu() === 'edit'}>
        <Menu x={menuAnchor().x} y={menuAnchor().y} data-testid="term-menu-edit" onDismiss={closeMenu}>
          <MenuItem label="Copy" data-testid="term-menu-copy" onClick={run(() => activeApi()?.copySelection())} />
          <MenuItem label="Paste" data-testid="term-menu-paste" onClick={run(() => activeApi()?.paste())} />
          <MenuItem label="Select All" data-testid="term-menu-selectall" onClick={run(() => activeApi()?.selectAll())} />
          <MenuSeparator />
          <MenuItem label="Clear" data-testid="term-menu-clear" onClick={run(() => activeApi()?.clearScreen())} />
        </Menu>
      </Show>
      <Show when={openMenu() === 'tab'}>
        <Menu x={menuAnchor().x} y={menuAnchor().y} data-testid="term-menu-tab" onDismiss={closeMenu}>
          <MenuItem label="New Tab" data-testid="term-menu-newtab" onClick={run(openNewTab)} />
          <MenuItem label="Close Tab" data-testid="term-menu-closetab" onClick={run(() => requestCloseTab(active()))} />
          <MenuSeparator />
          <MenuItem
            label="No color"
            data-testid="term-menu-tag-none"
            icon={<span style={swatchStyle('transparent', true)} />}
            onClick={run(() => setTabColor(active(), undefined))}
          />
          <For each={TAG_COLORS}>
            {(c) => (
              <MenuItem
                label={c.label}
                data-testid={`term-menu-tag-${c.id}`}
                icon={<span style={swatchStyle(c.value)} />}
                onClick={run(() => setTabColor(active(), c.id))}
              />
            )}
          </For>
        </Menu>
      </Show>
      <Show when={openMenu() === 'split'}>
        <Menu x={menuAnchor().x} y={menuAnchor().y} data-testid="term-menu-split" onDismiss={closeMenu}>
          {/* Disabled means "this pane is too small to halve", which is
              why the split items grey out as a window shrinks. */}
          <MenuItem
            label="Split Right"
            data-testid="term-menu-split-right"
            trailing={<span style={shortcutStyle}>Ctrl+Shift+D</span>}
            disabled={!canSplitFocused('row')}
            onClick={run(() => splitFocused('row'))}
          />
          <MenuItem
            label="Split Down"
            data-testid="term-menu-split-down"
            trailing={<span style={shortcutStyle}>Ctrl+Shift+E</span>}
            disabled={!canSplitFocused('col')}
            onClick={run(() => splitFocused('col'))}
          />
          <MenuSeparator />
          <MenuItem
            label="Next Pane"
            data-testid="term-menu-next-pane"
            trailing={<span style={shortcutStyle}>Ctrl+Shift+→</span>}
            disabled={paneCount() < 2}
            onClick={run(() => focusDir('right'))}
          />
          <MenuItem
            label="Close Pane"
            data-testid="term-menu-close-pane"
            trailing={<span style={shortcutStyle}>Ctrl+Shift+W</span>}
            disabled={paneCount() < 2}
            onClick={run(() => requestCloseTab(active()))}
          />
        </Menu>
      </Show>
      <Show when={openMenu() === 'theme'}>
        <Menu x={menuAnchor().x} y={menuAnchor().y} data-testid="term-menu-theme" onDismiss={closeMenu}>
          <MenuItem
            label="Follow desktop"
            data-testid="term-menu-theme-auto"
            trailing={themeId() === undefined ? <Check size={12} /> : undefined}
            onClick={run(() => changeTheme(undefined))}
          />
          <MenuSeparator />
          <For each={TERM_THEMES}>
            {(t) => (
              <MenuItem
                label={t.label}
                data-testid={`term-menu-theme-${t.id}`}
                trailing={themeId() === t.id ? <Check size={12} /> : undefined}
                onClick={run(() => changeTheme(t.id))}
              />
            )}
          </For>
        </Menu>
      </Show>
      <Show when={openMenu() === 'paste'}>
        <Menu x={menuAnchor().x} y={menuAnchor().y} data-testid="term-menu-paste" onDismiss={closeMenu}>
          {/* Smart paste policy (§10). "ask" is the default: silent for the
              invisible fixes, a preview for anything structural. */}
          <MenuItem
            label="Smart paste: ask"
            data-testid="term-menu-paste-ask"
            trailing={smartPaste() === 'ask' ? <Check size={12} /> : undefined}
            onClick={run(() => changeSmartPaste('ask'))}
          />
          <MenuItem
            label="Smart paste: always"
            data-testid="term-menu-paste-always"
            trailing={smartPaste() === 'always' ? <Check size={12} /> : undefined}
            onClick={run(() => changeSmartPaste('always'))}
          />
          <MenuItem
            label="Smart paste: off"
            data-testid="term-menu-paste-off"
            trailing={smartPaste() === 'off' ? <Check size={12} /> : undefined}
            onClick={run(() => changeSmartPaste('off'))}
          />
        </Menu>
      </Show>
      <Show when={openMenu() === 'font'}>
        <Menu x={menuAnchor().x} y={menuAnchor().y} data-testid="term-menu-font" onDismiss={closeMenu}>
          {/* Size stepper: clicks stay inside the menu so it doesn't
              close, letting the user step several times and watch the
              live terminal reflow. */}
          <div style={sizeRowStyle}>
            <span style={{ flex: 1 }}>Size</span>
            <Button variant="icon" data-testid="term-menu-size-dec" style={stepBtnStyle} onClick={() => stepFontSize(-1)}>−</Button>
            <span data-testid="term-menu-size-val" style={sizeValStyle}>{fontSize()}</span>
            <Button variant="icon" data-testid="term-menu-size-inc" style={stepBtnStyle} onClick={() => stepFontSize(1)}>+</Button>
          </div>
          <MenuSeparator />
          <For each={TERM_FONTS}>
            {(f) => (
              <MenuItem
                label={f.label}
                data-testid={`term-menu-font-${f.id}`}
                trailing={fontId() === f.id ? <Check size={12} /> : undefined}
                onClick={run(() => changeFontId(f.id))}
              />
            )}
          </For>
        </Menu>
      </Show>
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
      {/* The stage. Everything below is positioned from placement():
          terminal hosts first (flat siblings, keyed by channel id, NEVER
          reparented — docs/TERM_LAYOUT.md §2), then one strip per group,
          then the dividers. Two independent <For>s over the same tree. */}
      <div
        data-testid="term-stage"
        ref={(el) => { stageEl = el; }}
        style={{ flex: 1, position: 'relative', 'min-height': 0, overflow: 'hidden' }}
      >
        <For each={tabs()}>
          {(tab) => {
            let hostEl: HTMLDivElement | undefined;
            // The rect this channel occupies: its group's content box when
            // it is that group's visible tab, hidden otherwise. Only the
            // style changes — the element itself never moves.
            const place = () => {
              const g = placement().groups.find((pg) => pg.group.tabs.includes(tab.channelID));
              return g && g.group.active === tab.channelID ? g.content : undefined;
            };
            return (
              <div
                data-testid="term-host"
                data-channel={tab.channelID}
                style={{
                  position: 'absolute',
                  display: place() ? 'block' : 'none',
                  left: `${place()?.x ?? 0}px`,
                  top: `${place()?.y ?? 0}px`,
                  width: `${place()?.w ?? 0}px`,
                  height: `${place()?.h ?? 0}px`,
                }}
                ref={(el) => { hostEl = el; }}
                onMouseDown={() => {
                  const path = pathOfChannel(tree(), tab.channelID);
                  if (path !== undefined) focusGroup(path);
                }}
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
                  theme={themeById(themeId())?.theme}
                  onTitle={(t) => setTabTitle(tab.channelID, t)}
                  initialCols={tab.init?.cols}
                  initialRows={tab.init?.rows}
                  initialModes={tab.modes}
                  onModesChanged={(m) => onTabModes(tab, m)}
                  beforePaste={beforePaste}
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
        {/* Group strips. Keyed by path (a stable string), so a relayout
            repositions rows instead of rebuilding them. */}
        <For each={placedPaths()}>
          {(path) => {
            const place = () => placedAt(path);
            const group = () => place()?.group;
            const focused = () => focusPath() === path;
            return (
              <Show when={place()}>
                <div
                  data-testid="term-tabbar"
                  data-path={path}
                  data-focused={focused() ? 'true' : 'false'}
                  class={WASH_SCROLL_CLASS}
                  style={{
                    ...stripStyle,
                    left: `${place()!.rect.x}px`,
                    top: `${place()!.rect.y}px`,
                    width: `${place()!.rect.w}px`,
                    // The focused pane is ringed rather than bordered: a
                    // border would resize the content box and refit the
                    // grid underneath it (docs/TERM_LAYOUT.md §6).
                    'box-shadow': focused() && paneCount() > 1
                      ? `inset 0 2px 0 ${tokens.accentBlue}`
                      : undefined,
                  }}
                  onMouseDown={() => focusGroup(path)}
                >
                  <For each={group()!.tabs}>{(id) => tabButton(id, path)}</For>
                  <span style={{ flex: 1, 'min-width': '4px' }} />
                  <Button
                    variant="icon"
                    data-testid="term-new-tab"
                    title="New tab (Ctrl+Shift+T)"
                    style={ctlBtnStyle}
                    onClick={() => openNewTabIn(path)}
                  >
                    <Plus size={14} />
                  </Button>
                  <Button
                    variant="icon"
                    data-testid="term-split-right"
                    title="Split right (Ctrl+Shift+D)"
                    style={ctlBtnStyle}
                    disabled={!place() || !canSplit(place()!.rect, 'row', { gutter: GUTTER })}
                    onClick={() => splitAt(path, 'row')}
                  >
                    <Columns2 size={13} />
                  </Button>
                  <Button
                    variant="icon"
                    data-testid="term-split-down"
                    title="Split down (Ctrl+Shift+E)"
                    style={ctlBtnStyle}
                    disabled={!place() || !canSplit(place()!.rect, 'col', { gutter: GUTTER })}
                    onClick={() => splitAt(path, 'col')}
                  >
                    <Rows2 size={13} />
                  </Button>
                </div>
              </Show>
            );
          }}
        </For>
        {/* Dividers. Static rules in M1; M2 makes them draggable (the
            kernel's resizeSplit is already there and tested). */}
        <For each={placement().dividers}>
          {(d) => (
            <div
              data-testid="term-divider"
              data-dir={d.dir}
              style={{
                position: 'absolute',
                left: `${d.rect.x}px`,
                top: `${d.rect.y}px`,
                width: `${d.rect.w}px`,
                height: `${d.rect.h}px`,
                background: tokens.borderMenu,
              }}
            />
          )}
        </For>
      </div>
      <Show when={pendingPaste()}>
        {(p) => (
          <PasteOverlay
            analysis={p().analysis}
            onCleaned={() => settlePaste(p().analysis.cleaned)}
            onAsIs={() => settlePaste(p().analysis.original)}
            onCancel={() => settlePaste(null)}
          />
        )}
      </Show>
      <div
        data-testid="term-statusbar"
        style={{
          display: 'flex',
          'align-items': 'center',
          gap: '6px',
          height: '20px',
          padding: '0 8px',
          'flex-shrink': 0,
          'border-top': `1px solid ${tokens.borderMenu}`,
          // Splash of red when the active tab is root — a standing
          // "you are root here" cue, not just the badge.
          background: isRootActive() ? tokens.accentRed : tokens.bgMenu,
          color: isRootActive() ? '#ffffff' : tokens.fg,
          font: tokens.type.textMd,
          'white-space': 'nowrap',
          overflow: 'hidden',
          'user-select': 'none',
        }}
      >
        <span style={{ display: 'inline-flex', 'align-items': 'center', 'flex-shrink': 0 }}>
          {statusBadge(activeStatus(), isRootActive() ? '#ffffff' : undefined)}
        </span>
        <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis' }}>{statusText()}</span>
        <Show when={activeAgent()}>
          {(a) => (
            <span
              data-testid="term-status-agent"
              data-agent-state={a().state}
              style={{
                display: 'inline-flex',
                'align-items': 'center',
                gap: '5px',
                'flex-shrink': 0,
                // The red root bar owns the whole line's colour; elsewhere
                // the clause carries its own state hue.
                color: isRootActive() ? '#ffffff' : agentColor(a().state),
              }}
            >
              {agentText(a())}
            </span>
          )}
        </Show>
      </div>
    </>
  );
};

// ---- helpers / styles ----

// agentColor maps an agent state to its dot hue (docs/AGENT_TERM.md §5):
// blue working / amber needs-input / green done, with a muted dot for a
// T0-detected agent that isn't reporting state.
function agentColor(state: AgentStatus['state']): string {
  switch (state) {
    case 'working': return tokens.accentBlue;
    case 'needs-input': return tokens.accentAmber;
    case 'done': return tokens.accentGreen;
    default: return tokens.fgMuted;
  }
}

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

const menuBarStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  background: tokens.bgMenu,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  'min-height': '24px',
  'flex-shrink': 0,
  'user-select': 'none',
};

function menuBarBtnStyle(active: boolean): JSX.CSSProperties {
  return {
    background: active ? tokens.bgRowSelected : 'transparent',
    color: tokens.fg,
    border: 'none',
    padding: '2px 10px',
    height: '24px',
    cursor: 'pointer',
    font: tokens.type.textMd,
  };
}

// One strip per group, positioned absolutely from the group's rect. The
// left/top/width come from placement(); everything else is fixed.
const stripStyle: JSX.CSSProperties = {
  position: 'absolute',
  height: `${STRIP_HEIGHT}px`,
  background: tokens.bgWindow,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  display: 'flex',
  'align-items': 'stretch',
  gap: '2px',
  // padding-top creates the gap above the tabs; tabs round into the
  // border-bottom line, matching how browser tabs sit on a bar.
  padding: '3px 3px 0',
  'overflow-x': 'auto',
  'overflow-y': 'hidden',
  font: tokens.type.monoMd,
  'box-sizing': 'border-box',
};

// Layout override on top of <Button variant="icon"> (transparent chrome base)
// for the per-strip controls: new tab, split right, split down.
const ctlBtnStyle: JSX.CSSProperties = {
  opacity: 0.8,
  'flex-shrink': 0,
  padding: '0 3px',
};

// Shortcut hint in the Split menu's trailing slot.
const shortcutStyle: JSX.CSSProperties = {
  color: tokens.fgDim,
  'font-size': '11px',
  'padding-left': '18px',
};

// Font menu size-stepper row (moved here from the terminal's old
// right-click menu).
const sizeRowStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '6px',
  padding: '4px 14px',
  color: tokens.fg,
  font: tokens.type.textMd,
};

// Layout/fill override on top of <Button variant="icon"> (the icon base
// supplies the flex-centering, cursor and radius; this carries the filled
// look and the compact 22px square footprint).
const stepBtnStyle: JSX.CSSProperties = {
  width: '22px',
  height: '22px',
  background: tokens.bgRowSelected,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}`,
  'font-size': '14px',
  'line-height': '1',
};

const sizeValStyle: JSX.CSSProperties = {
  'min-width': '24px',
  'text-align': 'center',
  'font-variant-numeric': 'tabular-nums',
};

// ---- custom element ----

defineWashApp('wash-app-term', (props) => <App {...props} />, {
  // Surface follows the pack (near-black on dark packs, cream on light)
  // so the chrome behind the xterm canvas matches the terminal theme.
  style: `display:flex;flex-direction:column;width:100%;height:100%;box-sizing:border-box;background:${tokens.bgInset};color:${tokens.fg};overflow:hidden`,
});
