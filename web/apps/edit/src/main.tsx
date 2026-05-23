// wash-app-edit: text editor. Three-pane layout — sidebar (dir
// tree) | editor area (CodeMirror, tabs above) | status bar.
//
// This file is the skeleton: layout + tree state + sidebar
// rendering. CodeMirror is wired in the next commit. Until then
// the editor area shows a placeholder for the currently-selected
// file.
//
// Architecture note: every fs op is a single in-process call
// through the BE (cmd/wash-edit calls internal/fs.List / Read /
// Write directly). The picker is the only place that touches
// cross-app routing, via the SDK helper.

import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import { createStore, produce } from 'solid-js/store';
import type { Component, JSX } from 'solid-js';
import { FilePicker, Menu, MenuItem, MenuSeparator, Splitter, StatusBar, defineWashApp, tokens } from '@wash/ui';
import { EditorSelection, EditorState, Compartment } from '@codemirror/state';
import {
  EditorView,
  crosshairCursor,
  dropCursor,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
  placeholder,
  rectangularSelection,
} from '@codemirror/view';
import { defaultKeymap, history, historyKeymap, indentWithTab, redo, undo } from '@codemirror/commands';
import { getSearchQuery, highlightSelectionMatches, openSearchPanel, searchKeymap, searchPanelOpen, SearchQuery, setSearchQuery, search } from '@codemirror/search';
import { unifiedMergeView } from '@codemirror/merge';
import {
  autocompletion,
  closeBrackets,
  closeBracketsKeymap,
  completionKeymap,
} from '@codemirror/autocomplete';
import {
  HighlightStyle,
  bracketMatching,
  foldGutter,
  foldKeymap,
  indentOnInput,
  syntaxHighlighting,
} from '@codemirror/language';
import { tags as t } from '@lezer/highlight';
import { javascript } from '@codemirror/lang-javascript';
import { json } from '@codemirror/lang-json';
import { markdown } from '@codemirror/lang-markdown';
// xterm + addon-fit are externalized to /vendor/xterm.js. The vendor
// bundle auto-injects the xterm CSS, so no manual <style> shim here.
import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import {
  Check,
  ChevronDown,
  ChevronRight,
  File as FileIcon,
  Folder as FolderIcon,
  Link2,
} from 'lucide-solid';

interface Entry {
  name: string;
  type: 'dir' | 'file' | 'symlink' | 'other';
  size: number;
  mod_unix: number;
  // link_to / link_err carry the symlink target as returned by
  // the BE (internal/fs.Entry). Used by the double-click handler
  // to follow links — same affordance fm has.
  link_to?: string;
  link_err?: string;
}

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

// PersistedState is what we write to SaveState and read back on
// mount. Only saved tabs (with a real path) are persisted —
// Untitled buffers are ephemeral, lost on reload. Cursor/scroll
// per tab can be added later; for v1 the file list + active +
// splitter are enough.
interface PersistedTab {
  // Empty/missing path means an Untitled buffer; content + display_name
  // are required for those (no on-disk source to read back).
  path?: string;
  display_name?: string;
  content?: string;
  selection?: { anchor: number; head: number };
  scroll?: number;
}

interface PersistedState {
  // Legacy fields — older blobs may only have these. Still written
  // for one-way back-compat; new restores prefer `tabs` when present.
  paths?: string[];
  active?: string;
  split_pct?: number;
  // Terminal pane geometry. term_open toggles the panel; edit_pct
  // is the editor row's vertical share when the panel is visible.
  // Terminal tabs themselves aren't persisted — PTYs die with the
  // editor process; restoring a "terminal tab" would be a fresh
  // shell anyway.
  term_open?: boolean;
  edit_pct?: number;
  // Find/replace panel — open state + query so a reload returns
  // the user to the exact same search context. Empty `search`
  // string means no active query (CM6's default).
  find_open?: boolean;
  find_query?: {
    search: string;
    replace?: string;
    case_sensitive?: boolean;
    regexp?: boolean;
    whole_word?: boolean;
    literal?: boolean;
  };
  // Full per-tab snapshot: includes Untitled buffer contents +
  // cursor selection + scroll. Wins over `paths` on restore.
  tabs?: PersistedTab[];
  active_idx?: number;
}

// TermTab is one terminal session. Local id is assigned eagerly;
// the channelID arrives from the BE on term.opened. Bytes that
// land before the FE has mounted xterm are queued in `pending`
// and flushed at mount time so we never lose initial output.
interface TermTab {
  id: string;
  channelID: number;
  title: string;
  // Map state, not class members — xterm is imperative so we
  // keep references outside Solid's reactive system. Filled in
  // by mountTerm.
}

// Tab is one open file (or one Untitled buffer). Path is "" for
// Untitled tabs that haven't been saved yet — they'll trigger the
// Save dialog on first Ctrl+S. baseline is the on-disk content the
// last write produced; the editor compares against it to set the
// dirty flag. state is the captured CM EditorState the last time
// this tab was the active one — we restore it on tab switch so
// each tab carries its own undo history, cursor, scroll.
interface Tab {
  id: string;
  path: string;
  displayName: string;
  baseline: string;
  state: EditorState | null;
  binary: boolean;
  // Last-seen vertical scroll for this tab; captured at tab-switch.
  // Restored on switch back so each tab keeps its scroll position
  // alongside its EditorState (which already holds cursor/undo).
  scrollTop?: number;
  // When set, this tab is a unified-diff view. The doc is the
  // "new" side (typically the currently-active file at diff time);
  // `diff.otherContent` is the "original" side passed to
  // unifiedMergeView. Diff tabs are read-only by convention — the
  // user clicks Accept/Reject on each chunk to mutate.
  diff?: { otherPath: string; otherDisplayName: string; otherContent: string };
}

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  // ---- reactive state ----

  // listings is a directory cache; key = absolute path. The tree
  // walks this on render and asks the BE to populate any expanded
  // path it hasn't seen yet.
  const [listings, setListings] = createStore<Record<string, Entry[]>>({});
  const [expanded, setExpanded] = createStore<Record<string, true>>({});
  const [root, setRoot] = createSignal('');
  const [selectedPath, setSelectedPath] = createSignal('');
  const [splitPct, setSplitPct] = createSignal(25);

  // tabs / activeID drive the editor pane. tabs is ordered; the
  // tab bar renders left-to-right. activeID points at one of them
  // (or '' when no tabs are open). dirtyIDs is a derived signal
  // that tracks which tabs have unsaved changes.
  const [tabs, setTabs] = createSignal<Tab[]>([]);
  const [activeID, setActiveID] = createSignal('');
  const [dirtyIDs, setDirtyIDs] = createSignal<Set<string>>(new Set());
  // picker drives the FilePicker overlay. null = closed; otherwise
  // mode + savePayload (for Save-As, the tab whose content we'll
  // write once the user picks a destination).
  const [picker, setPicker] = createSignal<
    | null
    | { mode: 'open' }
    | { mode: 'save'; tabID: string; suggestedName: string }
  >(null);

  // Terminal pane state. termTabs is ordered; activeTermID points
  // at one of them (or '' when no terminals). termOpen toggles the
  // panel visibility (Ctrl+` / menu). editPct is the editor row's
  // vertical share when the terminal panel is visible.
  const [termTabs, setTermTabs] = createSignal<TermTab[]>([]);
  const [activeTermID, setActiveTermID] = createSignal('');
  const [termOpen, setTermOpen] = createSignal(false);
  const [editPct, setEditPct] = createSignal(70);
  // Pending term.open requests waiting for term.opened so we can
  // pair channelID with the FE's local term id. Keyed by reply id.
  const pendingTermOpens = new Map<string, string>(); // reply id -> local id
  // Per-channel state: xterm + fit + bytes-queue + cleanup. We
  // keep this outside Solid's reactive system because xterm is
  // imperative and a Map is the cleanest fit. Map keyed by
  // channelID; created at term.opened; populated at mount.
  type TermRefs = {
    xterm: XTerm | null;
    fit: FitAddon | null;
    unsub?: () => void;
    pending: Uint8Array[];
  };
  const termRefs = new Map<number, TermRefs>();
  let nextTermLocalID = 0;

  // openMenu is the open dropdown's id ('' = none). It's set when
  // the user clicks a menubar button; menubarOffsets stores each
  // button's x,y so the Menu component knows where to drop.
  const [openMenu, setOpenMenu] = createSignal<'' | 'file' | 'edit' | 'syntax' | 'terminal'>('');
  const [menuAnchor, setMenuAnchor] = createSignal<{ x: number; y: number }>({ x: 0, y: 0 });
  // Per-active-tab language override. Null = derive from path.
  const [langOverride, setLangOverride] = createSignal<string | null>(null);
  // Word-wrap toggle. Recompiled into the langCompartment so we
  // don't need a second compartment for it.
  const [wordWrap, setWordWrap] = createSignal(false);

  // Sidebar drag/drop. dropTargetPath drives the visual highlight
  // on the hovered folder row ('' = no target = drop lands in
  // root). dropMenu is non-null while the alt-drop overlay is up.
  // renaming holds the inline-edit draft when the user picks
  // Rename from the alt-menu.
  const [dropTargetPath, setDropTargetPath] = createSignal('');
  const [dropMenu, setDropMenu] = createSignal<
    | null
    | { x: number; y: number; src: string; destDir: string }
  >(null);
  const [renaming, setRenaming] = createSignal<{ path: string; draft: string } | null>(null);

  // ctxMenu drives the right-click context menu on sidebar rows.
  // Same pattern as fm: right-clicking implicitly selects the row
  // so the menu's actions operate on what was clicked, not a
  // stale selection elsewhere.
  const [ctxMenu, setCtxMenu] = createSignal<
    | null
    | { x: number; y: number; entry: Entry; path: string }
  >(null);
  // untitledCounter — monotonically increasing index for naming
  // fresh Untitled-N buffers. Resets only on app remount.
  let untitledCounter = 0;

  const activeTab = (): Tab | undefined => {
    const id = activeID();
    return tabs().find((t) => t.id === id);
  };

  // Per-request id counter for the message correlator. Each list /
  // read / write gets a fresh id so we can pair the reply.
  let nextReqID = 0;
  const pendingReplies = new Map<string, (m: BEMessage) => void>();

  // ---- BE I/O ----

  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  const sendWithReply = (req: Record<string, unknown>, timeoutMs = 5000): Promise<BEMessage> => {
    nextReqID += 1;
    const id = `e-${nextReqID}`;
    return new Promise((resolve) => {
      const timer = window.setTimeout(() => {
        if (pendingReplies.delete(id)) {
          resolve({ kind: 'timeout_err', id, code: 'timeout', msg: `no reply within ${timeoutMs}ms` });
        }
      }, timeoutMs);
      pendingReplies.set(id, (m) => {
        window.clearTimeout(timer);
        resolve(m);
      });
      send({ ...req, id });
    });
  };

  const loadDir = async (path: string) => {
    const reply = await sendWithReply({ kind: 'list', path });
    if (reply.kind === 'list_ok') {
      const abs = String(reply.path);
      const entries = (reply.entries as Entry[]) ?? [];
      setListings(abs, entries);
      if (!root()) {
        setRoot(abs);
        // Watch the root the first time we see it — the sidebar
        // always shows root-level entries, so we always want
        // fresh data there. Other dirs subscribe on expand.
        sendFsWatch(abs);
      }
    }
  };

  // openInTab focuses an existing tab for `path`, or reads the
  // file and creates a fresh tab if there isn't one. Same tab
  // can't appear twice — opening twice converges on a single tab.
  const openInTab = async (path: string) => {
    captureActiveState();
    const existing = tabs().find((t) => t.path === path);
    if (existing) {
      setActiveID(existing.id);
      return;
    }
    const reply = await sendWithReply({ kind: 'read', path });
    if (reply.kind !== 'read_ok') return;
    const binary = !!reply.binary;
    const content = binary ? '' : String(reply.content ?? '');
    const tab: Tab = {
      id: path,
      path,
      displayName: baseName(path) || path,
      baseline: content,
      state: null,
      binary,
    };
    setTabs([...tabs(), tab]);
    setActiveID(tab.id);
  };

  // openDiffTab reads `otherPath` from disk and creates a tab that
  // diffs it against the active tab's current content (the "new"
  // side). The tab's doc is the new content; unifiedMergeView
  // overlays inline diff markers showing changes vs `otherPath`.
  // No-op if there is no active file tab to diff against.
  const openDiffTab = async (otherPath: string) => {
    const cur = activeTab();
    if (!cur || !editorView) return;
    // Use the live editor content for the "new" side so unsaved
    // edits show up in the diff. baseline is what's on disk.
    const newContent = editorView.state.doc.toString();
    const reply = await sendWithReply({ kind: 'read', path: otherPath });
    if (reply.kind !== 'read_ok' || reply.binary) return;
    const otherContent = String(reply.content ?? '');
    captureActiveState();
    const id = `diff-${otherPath}-vs-${cur.path || cur.displayName}`;
    const existing = tabs().find((t) => t.id === id);
    if (existing) {
      setActiveID(existing.id);
      return;
    }
    const otherName = baseName(otherPath) || otherPath;
    const curName = cur.path ? (baseName(cur.path) || cur.path) : cur.displayName;
    const tab: Tab = {
      id,
      path: '',
      displayName: `Diff: ${otherName} ↔ ${curName}`,
      baseline: newContent,
      state: null,
      binary: false,
      diff: { otherPath, otherDisplayName: otherName, otherContent },
    };
    setTabs([...tabs(), tab]);
    setActiveID(tab.id);
  };

  // newUntitled creates a fresh empty buffer. The path stays ""
  // until the user saves it via the FilePicker.
  const newUntitled = () => {
    captureActiveState();
    untitledCounter += 1;
    const id = `untitled-${untitledCounter}`;
    const tab: Tab = {
      id,
      path: '',
      displayName: `Untitled-${untitledCounter}`,
      baseline: '',
      state: null,
      binary: false,
    };
    setTabs([...tabs(), tab]);
    setActiveID(tab.id);
  };

  // closeTab drops the tab and picks a sensible neighbor for the
  // new active. Dirty-state confirmation will be added when the
  // app gets a "really close?" dialog; for now closes are silent.
  const closeTab = (id: string) => {
    const cur = tabs();
    const idx = cur.findIndex((t) => t.id === id);
    if (idx < 0) return;
    const next = cur.slice(0, idx).concat(cur.slice(idx + 1));
    setTabs(next);
    setDirtyIDs((s) => {
      if (!s.has(id)) return s;
      const out = new Set(s);
      out.delete(id);
      return out;
    });
    if (activeID() === id) {
      const neighbor = next[idx] ?? next[idx - 1];
      setActiveID(neighbor ? neighbor.id : '');
    }
  };

  // saveActive writes the active tab's current content to disk.
  // If the tab is Untitled (no path yet) it routes through the
  // FilePicker in save mode and writes once the user picks one.
  const saveActive = async () => {
    const t = activeTab();
    if (!t || !editorView) return;
    if (!t.path) {
      setPicker({ mode: 'save', tabID: t.id, suggestedName: t.displayName });
      return;
    }
    const content = editorView.state.doc.toString();
    const reply = await sendWithReply({ kind: 'write', path: t.path, content });
    if (reply.kind === 'write_ok') {
      // Refresh baseline + clear dirty marker. The tab's path may
      // have changed if write canonicalized it (filepath.Clean).
      setTabs(tabs().map((x) => x.id === t.id ? { ...x, baseline: content, path: String(reply.path ?? x.path) } : x));
      setDirtyIDs((s) => {
        if (!s.has(t.id)) return s;
        const out = new Set(s);
        out.delete(t.id);
        return out;
      });
    }
  };

  // saveAsActive forces the picker open for the active tab, no
  // matter whether it already has a path. Bound to Ctrl+Shift+S.
  const saveAsActive = () => {
    const t = activeTab();
    if (!t) return;
    setPicker({
      mode: 'save',
      tabID: t.id,
      suggestedName: baseName(t.path) || t.displayName,
    });
  };

  // pickerConfirm dispatches the picker's chosen path. In open
  // mode we just route through openInTab. In save mode we write
  // the source tab's current doc to the chosen path, then
  // canonicalize the tab (path, displayName, baseline).
  const pickerConfirm = async (chosen: string) => {
    const cur = picker();
    setPicker(null);
    if (!cur) return;
    if (cur.mode === 'open') {
      void openInTab(chosen);
      return;
    }
    if (!editorView) return;
    const src = tabs().find((t) => t.id === cur.tabID);
    if (!src) return;
    const content = src.id === activeID() ? editorView.state.doc.toString() : (src.state ? src.state.doc.toString() : src.baseline);
    const reply = await sendWithReply({ kind: 'write', path: chosen, content });
    if (reply.kind !== 'write_ok') return;
    const newPath = String(reply.path ?? chosen);
    // Update the tab: new id (the path), new display name, fresh
    // baseline. If another tab already pointed at newPath, drop
    // it — converging on a single tab per path matches openInTab.
    const dupeIdx = tabs().findIndex((t) => t.path === newPath && t.id !== src.id);
    const updated = tabs()
      .filter((_, i) => i !== dupeIdx)
      .map((x) => x.id === src.id
        ? { ...x, id: newPath, path: newPath, displayName: baseName(newPath) || newPath, baseline: content }
        : x);
    setTabs(updated);
    setActiveID(newPath);
    setDirtyIDs((s) => {
      if (!s.has(src.id)) return s;
      const out = new Set(s);
      out.delete(src.id);
      return out;
    });
  };

  // ---- state persistence ----

  // persist is debounced so a flurry of tab switches doesn't slam
  // the router. The state blob is small (handful of paths) so
  // the cost is negligible per call; the debounce just collapses
  // bursts.
  let persistTimer: number | null = null;
  const persist = () => {
    if (!props.instance) return;
    if (persistTimer != null) window.clearTimeout(persistTimer);
    persistTimer = window.setTimeout(() => {
      persistTimer = null;
      const tabsNow = tabs();
      const activeNow = activeID();
      const tabList: PersistedTab[] = [];
      let activeIdx = -1;
      tabsNow.forEach((t, i) => {
        if (t.id === activeNow) activeIdx = i;
        // For the active tab, the freshest selection/scroll/content
        // lives in editorView; cached t.state is whatever was last
        // captured on tab-switch.
        const isActive = t.id === activeNow;
        const liveState = isActive ? editorView?.state : t.state;
        const scrollTop = isActive ? editorView?.scrollDOM.scrollTop : t.scrollTop;
        const pt: PersistedTab = {};
        if (t.path) {
          pt.path = t.path;
        } else {
          pt.display_name = t.displayName;
          pt.content = liveState ? liveState.doc.toString() : t.baseline;
        }
        if (liveState) {
          const sel = liveState.selection.main;
          if (sel.anchor !== 0 || sel.head !== 0) {
            pt.selection = { anchor: sel.anchor, head: sel.head };
          }
        }
        if (scrollTop && scrollTop > 0) pt.scroll = scrollTop;
        tabList.push(pt);
      });
      const state: PersistedState = {
        // Legacy fields, written for older clients that don't read `tabs`.
        paths: tabsNow.filter((t) => t.path).map((t) => t.path),
        active: activeTab()?.path || undefined,
        split_pct: splitPct(),
        term_open: termOpen(),
        edit_pct: editPct(),
        tabs: tabList,
        active_idx: activeIdx >= 0 ? activeIdx : undefined,
      };
      if (editorView) {
        const q = getSearchQuery(editorView.state);
        state.find_open = searchPanelOpen(editorView.state);
        // Only persist a query if there's something to restore.
        // CM6's default-constructed SearchQuery has search="".
        if (q.search) {
          state.find_query = {
            search: q.search,
            replace: q.replace || undefined,
            case_sensitive: q.caseSensitive || undefined,
            regexp: q.regexp || undefined,
            whole_word: q.wholeWord || undefined,
            literal: q.literal || undefined,
          };
        }
      }
      send({ kind: 'save_state', state });
    }, 250);
  };

  const restoreFrom = async (s: PersistedState) => {
    if (typeof s.split_pct === 'number') {
      setSplitPct(Math.max(15, Math.min(85, s.split_pct)));
    }
    if (typeof s.edit_pct === 'number') {
      setEditPct(Math.max(20, Math.min(90, s.edit_pct)));
    }
    // Restore term_open without auto-spawning a terminal — the
    // user can recreate via Ctrl+Shift+` if they want one. PTYs
    // don't survive process exit so re-spawning silently would
    // surprise them.
    if (typeof s.term_open === 'boolean') setTermOpen(s.term_open);
    // Prefer the richer `tabs` array (cursor/scroll/untitled
    // content); fall back to the legacy `paths` list for older
    // saved blobs that pre-date this shape.
    if (s.tabs && s.tabs.length > 0) {
      for (const pt of s.tabs) {
        if (pt.path) {
          await openInTab(pt.path);
          const tab = tabs().find((x) => x.path === pt.path);
          if (tab && (pt.selection || pt.scroll)) {
            const fresh = EditorState.create({
              doc: tab.baseline,
              extensions: baseExtensions(),
              selection: pt.selection
                ? EditorSelection.single(pt.selection.anchor, pt.selection.head)
                : undefined,
            });
            setTabs(tabs().map((x) => x.id === tab.id ? { ...x, state: fresh, scrollTop: pt.scroll } : x));
          }
        } else {
          // Untitled — reconstruct the buffer in place. untitledCounter
          // bumps so a subsequent New keeps a distinct name even when
          // a saved Untitled-N is back on screen.
          untitledCounter += 1;
          const id = `untitled-${untitledCounter}`;
          const content = pt.content || '';
          const fresh = EditorState.create({
            doc: content,
            extensions: baseExtensions(),
            selection: pt.selection
              ? EditorSelection.single(pt.selection.anchor, pt.selection.head)
              : undefined,
          });
          const tab: Tab = {
            id,
            path: '',
            displayName: pt.display_name || `Untitled-${untitledCounter}`,
            baseline: '',
            state: fresh,
            binary: false,
            scrollTop: pt.scroll,
          };
          setTabs([...tabs(), tab]);
        }
      }
      if (typeof s.active_idx === 'number') {
        const tab = tabs()[s.active_idx];
        if (tab) setActiveID(tab.id);
      }
    } else if (s.paths && s.paths.length > 0) {
      for (const p of s.paths) {
        await openInTab(p);
      }
      if (s.active) {
        const t = tabs().find((x) => x.path === s.active);
        if (t) setActiveID(t.id);
      }
    }
    // Scroll for the active tab is applied after the createEffect
    // below has run editorView.setState(t.state); a microtask is
    // late enough that scrollDOM has the new doc laid out.
    queueMicrotask(() => {
      const t = activeTab();
      if (t?.scrollTop && editorView) editorView.scrollDOM.scrollTop = t.scrollTop;
    });
    // Find/replace restore happens after tabs so the editor view is
    // mounted with the active document. Query first (so an open
    // panel paints with the right input), then panel.
    if (editorView) {
      if (s.find_query?.search) {
        editorView.dispatch({
          effects: setSearchQuery.of(new SearchQuery({
            search: s.find_query.search,
            replace: s.find_query.replace || '',
            caseSensitive: !!s.find_query.case_sensitive,
            regexp: !!s.find_query.regexp,
            wholeWord: !!s.find_query.whole_word,
            literal: !!s.find_query.literal,
          })),
        });
      }
      if (s.find_open) openSearchPanel(editorView);
    }
  };

  // ---- menu commands ----
  //
  // Most menu items either call into CodeMirror via its command
  // API or replay one of the keyboard handlers we already wired
  // (saveActive, newUntitled, etc). We always focus the editor
  // before commanding so the command lands in the right view.

  const cmdUndo = () => {
    if (!editorView) return;
    editorView.focus();
    undo(editorView);
  };
  const cmdRedo = () => {
    if (!editorView) return;
    editorView.focus();
    redo(editorView);
  };
  const cmdFind = () => {
    if (!editorView) return;
    editorView.focus();
    openSearchPanel(editorView);
  };
  // Cut/Copy/Paste route through document.execCommand, which is
  // technically deprecated but is the only path that lets a menu
  // click drive the clipboard (programmatic Clipboard API needs a
  // user gesture on the menu item — it has one, but Permissions
  // around 'clipboard-write' are inconsistent across browsers).
  // CodeMirror's own keybindings remain the recommended path.
  const cmdClipboard = (op: 'cut' | 'copy' | 'paste') => {
    if (!editorView) return;
    editorView.focus();
    try { document.execCommand(op); } catch { /* ignore — best effort */ }
  };

  const setLang = (k: string | null) => {
    setLangOverride(k);
    editorView?.focus();
  };
  const toggleWrap = () => {
    setWordWrap(!wordWrap());
    editorView?.focus();
  };

  // ---- menu bar plumbing ----
  //
  // openMenuFor toggles a menu open against its trigger button.
  // The Menu component owns dismissal (click-outside via document
  // listener), so we just toggle openMenu signal and set the
  // anchor coordinates relative to the host element so the menu
  // hangs below the button regardless of where the window is.

  const openMenuFor = (id: 'file' | 'edit' | 'syntax' | 'terminal', ev: MouseEvent) => {
    if (openMenu() === id) {
      setOpenMenu('');
      return;
    }
    const btn = ev.currentTarget as HTMLElement;
    const btnRect = btn.getBoundingClientRect();
    const hostRect = props.host.getBoundingClientRect();
    setMenuAnchor({
      x: btnRect.left - hostRect.left,
      y: btnRect.bottom - hostRect.top + 2,
    });
    setOpenMenu(id);
  };
  const closeMenu = () => setOpenMenu('');
  // run wraps a menu-item action so the menu closes before the
  // action fires — focuses look right (no menu flashing during
  // CM dispatch).
  const run = (fn: () => void) => () => { closeMenu(); fn(); };

  // captureActiveState snapshots the live CM state into the
  // outgoing tab right before a switch. Without this, switching
  // away from a tab loses its undo history and cursor.
  const captureActiveState = () => {
    const t = activeTab();
    if (!t || !editorView) return;
    const scrollTop = editorView.scrollDOM.scrollTop;
    if (t.state === editorView.state && t.scrollTop === scrollTop) return;
    setTabs(tabs().map((x) => x.id === t.id ? { ...x, state: editorView!.state, scrollTop } : x));
  };

  const handleBE = (m: BEMessage) => {
    // ---- headless control commands ----
    //
    // External drivers (tests, other apps) send these via app_msg
    // to drive the editor without keyboard/mouse synthesis. The BE
    // forwards anything kind=cmd.* straight to the FE; the FE does
    // the actual UI work below.
    if (m.kind === 'cmd.open_file') {
      const path = String(m.path ?? '');
      if (path) void openInTab(path);
      return;
    }
    if (m.kind === 'cmd.set_root') {
      const path = String(m.path ?? '');
      if (!path) return;
      setRoot(path);
      setListings({});
      setExpanded({});
      void loadDir(path);
      sendFsWatch(path);
      return;
    }
    if (m.kind === 'cmd.open_diff') {
      const other = String(m.other ?? m.path ?? '');
      if (!other) return;
      // Optional: caller can supply `against` to set the active
      // tab first. Otherwise diff uses whatever tab is active.
      const against = String(m.against ?? '');
      (async () => {
        if (against) {
          await openInTab(against);
        }
        await openDiffTab(other);
      })();
      return;
    }
    // fs.watch_event arrives unsolicited when a subscribed dir
    // sees a change. We refresh both the parent of the changed
    // path (the watch reports children, so the parent is the
    // watched dir) AND the changed path itself if it happens to
    // be a tracked dir — covers "the watched dir got deleted, my
    // grandparent's watcher saw it" cases. scheduleRefresh
    // no-ops for paths we don't have listings for.
    if (m.kind === 'fs.watch_event') {
      const evPath = String(m.path ?? '');
      if (!evPath) return;
      scheduleRefresh(parentPath(evPath));
      scheduleRefresh(evPath);
      return;
    }
    // Terminal lifecycle messages: term.opened pairs the
    // server-assigned channel with a pending local term tab;
    // term.closed cleans up state when a PTY ends (user typed
    // exit, or wash-edit BE killed it on close).
    if (m.kind === 'term.opened') {
      const replyID = String(m.id ?? '');
      const localID = pendingTermOpens.get(replyID);
      if (!localID) return;
      pendingTermOpens.delete(replyID);
      const channelID = Number(m.channel_id ?? 0);
      if (!channelID) return;
      setTermTabs(termTabs().map((t) => t.id === localID ? { ...t, channelID } : t));
      // Initialize refs so mountTerm can queue bytes that arrive
      // before the xterm host element is in the DOM.
      termRefs.set(channelID, { xterm: null, fit: null, pending: [] });
      // Subscribe to bytes immediately so we don't lose anything
      // between term.opened and the host element being created.
      const unsub = window.wash.openRawChannel(channelID, (bytes) => {
        const ref = termRefs.get(channelID);
        if (!ref) return;
        if (ref.xterm) ref.xterm.write(bytes);
        else ref.pending.push(bytes);
      });
      const ref = termRefs.get(channelID)!;
      ref.unsub = unsub;
      // Mount xterm into the host element. Solid has already
      // rendered the host since termOpen()+termTabs() were set
      // before the BE replied; queueMicrotask defers one tick so
      // a freshly-toggled-open panel has its DOM in place.
      queueMicrotask(() => {
        const hostEl = props.host.querySelector(`[data-testid="edit-term-host-${localID}"]`) as HTMLDivElement | null;
        if (hostEl) mountTerm(channelID, hostEl);
      });
      return;
    }
    if (m.kind === 'term.closed') {
      const channelID = Number(m.channel_id ?? 0);
      if (!channelID) return;
      const tab = termTabs().find((t) => t.channelID === channelID);
      const ref = termRefs.get(channelID);
      if (ref) {
        ref.unsub?.();
        ref.xterm?.dispose();
        termRefs.delete(channelID);
      }
      if (tab) {
        setTermTabs(termTabs().filter((t) => t.id !== tab.id));
        if (activeTermID() === tab.id) {
          const remaining = termTabs().filter((t) => t.id !== tab.id);
          setActiveTermID(remaining[0]?.id ?? '');
        }
      }
      return;
    }
    const replyID = typeof m.id === 'string' ? m.id : undefined;
    if (replyID && pendingReplies.has(replyID)) {
      const resolver = pendingReplies.get(replyID)!;
      pendingReplies.delete(replyID);
      resolver(m);
    }
  };

  // ---- terminal pane ops ----

  // openNewTerm asks the BE to spawn a new shell + PTY. The reply
  // (term.opened) carries the channel_id; until then we have a
  // placeholder tab without a channel. We send `cols`/`rows` from
  // the host's current size as a reasonable initial guess; the
  // FitAddon will refine after the xterm element mounts.
  const openNewTerm = () => {
    setTermOpen(true);
    nextTermLocalID += 1;
    const localID = `t-${nextTermLocalID}`;
    const replyID = `to-${nextTermLocalID}`;
    pendingTermOpens.set(replyID, localID);
    setTermTabs([...termTabs(), { id: localID, channelID: 0, title: `Terminal ${nextTermLocalID}` }]);
    setActiveTermID(localID);
    send({ kind: 'term.open', id: replyID, cols: 80, rows: 24 });
  };

  const closeTerm = (id: string) => {
    const tab = termTabs().find((t) => t.id === id);
    if (!tab) return;
    if (tab.channelID) {
      // BE will fire term.closed once the pty winds down; handleBE
      // handles the tab removal there to keep the path single.
      send({ kind: 'term.close', channel_id: tab.channelID });
    } else {
      // No channel yet — local-only cleanup.
      setTermTabs(termTabs().filter((t) => t.id !== tab.id));
      if (activeTermID() === tab.id) setActiveTermID('');
    }
  };

  const toggleTermPanel = () => {
    setTermOpen(!termOpen());
    if (termOpen() && termTabs().length === 0) openNewTerm();
  };

  // mountTerm wires xterm into a host div for the given channel.
  // Bytes queued before mount are flushed at the end so initial
  // shell output isn't dropped if the user opens then quickly
  // switches tabs.
  const mountTerm = (channelID: number, host: HTMLDivElement) => {
    const ref = termRefs.get(channelID);
    if (!ref) return;
    const term = new XTerm({
      fontFamily: tokens.fontMono,
      fontSize: 13,
      theme: { background: '#000000' },
      cursorBlink: true,
      allowProposedApi: true,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host);
    ref.xterm = term;
    ref.fit = fit;
    // Expose the live Terminal on the host element so e2e tests
    // can read the buffer without going through internal refs.
    // Same pattern wash-term uses.
    (host as unknown as { __washTerm: XTerm }).__washTerm = term;
    const encoder = new TextEncoder();
    term.onData((s) => window.wash.writeRaw(channelID, encoder.encode(s)));
    // Flush bytes that arrived between term.opened and mount.
    for (const b of ref.pending) term.write(b);
    ref.pending = [];
    // Initial fit + announce size after layout settles.
    requestAnimationFrame(() => {
      fit.fit();
      term.focus();
      send({
        kind: 'term.resize',
        channel_id: channelID,
        cols: term.cols,
        rows: term.rows,
      });
    });
  };

  // ---- sidebar drag / drop / rename / delete ----
  //
  // Single-file scope: drag a row, drop on a folder = move. Hold
  // Alt during the drop to pop a menu (Move / Copy / Rename /
  // Delete). Move uses the editor BE's rename op (in-process fast
  // path); Copy + recursive Delete go through wash-bulk for the
  // queueing + progress + conflict-prompt UX. Cross-window drags
  // (fm → editor or vice versa) work for free because both apps
  // use the same MIME constant.
  //
  // fs.watch refreshes the affected dirs on completion, so we
  // don't need to manually re-list after rename/delete.

  const DRAG_MIME = 'application/x-wash-paths';

  const readDragPaths = (ev: DragEvent): string[] => {
    if (!ev.dataTransfer) return [];
    const json = ev.dataTransfer.getData(DRAG_MIME);
    if (!json) return [];
    try {
      const arr = JSON.parse(json);
      if (Array.isArray(arr)) return arr.filter((s) => typeof s === 'string');
    } catch {
      /* ignore */
    }
    return [];
  };

  const onRowDragStart = (ev: DragEvent, p: string) => {
    if (!ev.dataTransfer) return;
    ev.dataTransfer.effectAllowed = 'copyMove';
    // JSON array with one path keeps the wire format identical to
    // fm's multi-select payload — future-proofs cross-window drops.
    ev.dataTransfer.setData(DRAG_MIME, JSON.stringify([p]));
    ev.dataTransfer.setData('text/plain', p);
  };
  const onRowDragEnd = () => setDropTargetPath('');

  const onRowDragOver = (ev: DragEvent, rowPath: string) => {
    if (!ev.dataTransfer || !ev.dataTransfer.types.includes(DRAG_MIME)) return;
    ev.preventDefault();
    ev.stopPropagation();
    ev.dataTransfer.dropEffect = ev.altKey ? 'copy' : 'move';
    if (dropTargetPath() !== rowPath) setDropTargetPath(rowPath);
  };
  const onRowDrop = (ev: DragEvent, rowPath: string) => {
    const paths = readDragPaths(ev);
    if (paths.length === 0) return;
    ev.preventDefault();
    ev.stopPropagation();
    setDropTargetPath('');
    if (ev.altKey) {
      const rect = props.host.getBoundingClientRect();
      setDropMenu({
        x: ev.clientX - rect.left + 8,
        y: ev.clientY - rect.top + 8,
        src: paths[0],
        destDir: rowPath,
      });
      return;
    }
    void commitMove(paths[0], rowPath);
  };

  const onListDragOver = (ev: DragEvent) => {
    if (!ev.dataTransfer || !ev.dataTransfer.types.includes(DRAG_MIME)) return;
    ev.preventDefault();
    ev.dataTransfer.dropEffect = ev.altKey ? 'copy' : 'move';
    if (dropTargetPath() !== '') setDropTargetPath('');
  };
  const onListDrop = (ev: DragEvent) => {
    const paths = readDragPaths(ev);
    if (paths.length === 0) return;
    ev.preventDefault();
    setDropTargetPath('');
    // Empty-pane drop lands in the user's notion of "current dir"
    // — mirrors fm's dirOfSelection, so dropping while a folder
    // is selected drops INTO that folder rather than always the
    // project root.
    const dest = dirOfSelection();
    if (!dest) return;
    if (ev.altKey) {
      const rect = props.host.getBoundingClientRect();
      setDropMenu({
        x: ev.clientX - rect.left + 8,
        y: ev.clientY - rect.top + 8,
        src: paths[0],
        destDir: dest,
      });
      return;
    }
    void commitMove(paths[0], dest);
  };

  // commitMove uses the editor BE's rename op — single-path,
  // in-process, fast. fm-direct semantics: same-parent drops are
  // silent no-ops, dropping a dir onto itself / a descendant is
  // refused.
  const commitMove = async (src: string, destDir: string) => {
    if (!src || !destDir) return;
    if (parentPath(src) === destDir) return;
    if (destDir === src || destDir.startsWith(src + '/')) return;
    const to = joinPath(destDir, baseName(src));
    await sendWithReply({ kind: 'rename', from: src, to });
    // fs.watch on the parents catches up automatically.
  };

  // commitCopy always routes through wash-bulk. Copy has no
  // in-process fast path because recursive directory copies need
  // queueing + progress + Replace prompts that the bulk-ops UI
  // already provides.
  const commitCopy = (src: string, destDir: string) => {
    if (!src || !destDir) return;
    if (destDir === src || destDir.startsWith(src + '/')) return;
    window.wash.sendAppMsgTo(
      { app_id: 'com.wash.bulk' },
      { kind: 'enqueue', op: 'copy', paths: [src], dest: destDir },
    );
  };

  // commitDelete tries the editor BE's fast-path single-file
  // delete first. If the path is a non-empty dir, the BE returns
  // not_empty and we re-route through wash-bulk for the recursive
  // walk + queued progress.
  const commitDelete = async (path: string) => {
    if (!path) return;
    const reply = await sendWithReply({ kind: 'delete', path });
    if (reply.kind === 'delete_err' && (reply as { code?: string }).code === 'not_empty') {
      window.wash.sendAppMsgTo(
        { app_id: 'com.wash.bulk' },
        { kind: 'enqueue', op: 'delete', paths: [path] },
      );
    }
  };

  // Inline rename — the same flow fm has. Picking Rename from
  // the alt-drop menu pops an inline input on the row at the
  // dragged path; Enter commits, Escape cancels.
  const startRename = (p: string) => {
    setRenaming({ path: p, draft: baseName(p) });
  };
  const cancelRename = () => setRenaming(null);
  const commitRenameDraft = async () => {
    const r = renaming();
    if (!r) return;
    const draft = r.draft.trim();
    setRenaming(null);
    if (draft === '' || draft === baseName(r.path)) return;
    if (draft.includes('/')) return;
    const to = joinPath(parentPath(r.path), draft);
    await sendWithReply({ kind: 'rename', from: r.path, to });
  };

  const openInFm = () => {
    send({ kind: 'spawn', app_id: 'com.wash.fm' });
  };

  // ---- tree ops + fs.watch ----
  //
  // watching is the set of dirs we currently hold an fs.watch on.
  // Every expand subscribes (idempotent BE-side), every collapse
  // releases. The root gets watched at boot via loadDir's first
  // success path. onCleanup tears every remaining sub down so a
  // closed editor window doesn't strand watchers in the BE.
  //
  // refreshTimers is the per-dir debounce: fsnotify can fire
  // multiple events for one logical save (write + chmod); 100ms
  // collapses bursts without making the tree feel stale.

  const watching = new Set<string>();
  const refreshTimers = new Map<string, number>();

  const sendFsWatch = (p: string) => {
    if (!p || watching.has(p)) return;
    watching.add(p);
    send({ kind: 'fs.watch', path: p });
  };
  const sendFsUnwatch = (p: string) => {
    if (!p || !watching.has(p)) return;
    watching.delete(p);
    send({ kind: 'fs.unwatch', path: p });
  };

  const scheduleRefresh = (dir: string) => {
    if (!listings[dir]) return;
    const prev = refreshTimers.get(dir);
    if (prev != null) window.clearTimeout(prev);
    const tok = window.setTimeout(() => {
      refreshTimers.delete(dir);
      // Only re-list if we still care about this dir — closing the
      // editor or collapsing the parent could have happened during
      // the debounce.
      if (listings[dir]) void loadDir(dir);
    }, 100);
    refreshTimers.set(dir, tok);
  };

  const toggleExpand = (path: string) => {
    if (expanded[path]) {
      // Collapsing — also collapse + unwatch the whole subtree
      // so a deep tree doesn't strand watches when the user
      // closes the top. The trade-off is that re-expanding shows
      // the children collapsed again, but that beats leaking.
      const prefix = path === '/' ? '/' : path + '/';
      setExpanded(produce((s) => {
        for (const k of Object.keys(s)) {
          if (k === path || k.startsWith(prefix)) delete s[k];
        }
      }));
      for (const w of Array.from(watching)) {
        if (w === path || w.startsWith(prefix)) sendFsUnwatch(w);
      }
    } else {
      setExpanded(path, true);
      if (!listings[path]) void loadDir(path);
      sendFsWatch(path);
    }
  };

  // visibleRows flattens the tree into render-able rows: { entry,
  // path, depth }. Folders come before files at each level.
  const visibleRows = createMemo<Array<{ entry: Entry; path: string; depth: number }>>(() => {
    const rows: Array<{ entry: Entry; path: string; depth: number }> = [];
    const walk = (parent: string, depth: number) => {
      const entries = listings[parent];
      if (!entries) return;
      const sorted = entries.slice().sort((a, b) => {
        if (a.type === 'dir' && b.type !== 'dir') return -1;
        if (a.type !== 'dir' && b.type === 'dir') return 1;
        return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
      });
      for (const e of sorted) {
        if (e.name.startsWith('.')) continue;
        const child = joinPath(parent, e.name);
        rows.push({ entry: e, path: child, depth });
        if (e.type === 'dir' && expanded[child]) {
          walk(child, depth + 1);
        }
      }
    };
    const r = root();
    if (r) walk(r, 0);
    return rows;
  });

  // ---- row click semantics ----
  //
  // Single click = SELECT (matches fm). Double click = ACT —
  // expand folder / open file / follow symlink. The split exists
  // so right-click context-menu actions land on a clean target
  // without the row also opening underneath them.

  const onRowClick = (row: { entry: Entry; path: string }) => {
    setSelectedPath(row.path);
  };

  const onRowDblClick = (row: { entry: Entry; path: string }) => {
    if (row.entry.type === 'dir') {
      toggleExpand(row.path);
      return;
    }
    if (row.entry.type === 'symlink') {
      followSymlink(row.entry, row.path);
      return;
    }
    if (row.entry.type === 'file') {
      void openInTab(row.path);
    }
  };

  // followSymlink resolves the link target (absolute or relative
  // to the link's parent) and decides what to do with it: a dir
  // gets expanded and selected; a file opens in a tab. Same logic
  // fm uses for navigation.
  const followSymlink = (e: Entry, p: string) => {
    if (!e.link_to) return;
    const target = e.link_to.startsWith('/')
      ? e.link_to
      : joinPath(parentPath(p), e.link_to);
    setSelectedPath(target);
    // We don't know the target's type without statting. Try as
    // both: openInTab is a no-op for dirs (the read returns
    // is_dir and openInTab silently exits) and loadDir is a
    // no-op for files. One of them lands.
    void openInTab(target);
    if (!listings[target]) {
      void loadDir(target);
    }
    setExpanded(target, true);
  };

  // dirOfSelection picks the target directory for an empty-pane
  // drop or a "create file here" action. Order matches fm's
  // pattern: single folder selected → that folder; single file
  // selected → its parent; nothing → root.
  const dirOfSelection = (): string => {
    const p = selectedPath();
    if (!p) return root();
    const par = parentPath(p);
    const entries = listings[par];
    const entry = entries?.find((x) => x.name === baseName(p));
    if (entry?.type === 'dir') return p;
    return par || root();
  };

  // ---- context menu ----

  const openCtxMenu = (ev: MouseEvent, entry: Entry, p: string) => {
    ev.preventDefault();
    setSelectedPath(p);
    const rect = props.host.getBoundingClientRect();
    setCtxMenu({ x: ev.clientX - rect.left, y: ev.clientY - rect.top, entry, path: p });
  };
  const closeCtxMenu = () => setCtxMenu(null);

  // ctxCopyPath drops the selected row's path on the host
  // clipboard so the user can paste it elsewhere (terminal,
  // chat, etc). Mirrors fm's "Copy path" item.
  const ctxCopyPath = async (p: string) => {
    try {
      await navigator.clipboard.writeText(p);
    } catch {
      /* ignore — best effort, no UX feedback needed in v1 */
    }
  };

  // ---- CodeMirror ----
  //
  // The view is created once in onMount against a mount div ref;
  // subsequent file opens dispatch a transaction that swaps the
  // doc + the language extension (via a Compartment so we can
  // reconfigure language without rebuilding the whole state).
  //
  // Editing tracks dirty state in step 6; for now the editor is
  // free-typing but unsaved changes don't go anywhere.

  let editorMountEl!: HTMLDivElement;
  let editorView: EditorView | undefined;
  const langCompartment = new Compartment();

  // dirtyListener compares the live doc against the active tab's
  // baseline after every doc-changing transaction. The Set update
  // is cheap (constant-time membership check) so this is fine on
  // every keystroke.
  const dirtyListener = EditorView.updateListener.of((u) => {
    if (!u.docChanged) return;
    const t = activeTab();
    if (!t) return;
    const text = u.state.doc.toString();
    const isDirty = text !== t.baseline;
    setDirtyIDs((s) => {
      const has = s.has(t.id);
      if (has === isDirty) return s;
      const out = new Set(s);
      if (isDirty) out.add(t.id);
      else out.delete(t.id);
      return out;
    });
  });

  // searchListener triggers state persist when the find panel
  // opens / closes or its query changes. The persist() debounce
  // coalesces rapid keystrokes inside the search field.
  const searchListener = EditorView.updateListener.of((u) => {
    const openChanged = searchPanelOpen(u.startState) !== searchPanelOpen(u.state);
    const queryChanged = !getSearchQuery(u.startState).eq(getSearchQuery(u.state));
    if (openChanged || queryChanged) persist();
  });

  // selectionListener persists when the cursor / selection moves
  // (arrow keys, click, search-jump). docChanged also counts
  // because doc edits move the cursor — but the existing dirty
  // tracking already triggers persist via the reactive effect on
  // tabs(). Just selectionSet is enough here.
  const selectionListener = EditorView.updateListener.of((u) => {
    if (u.selectionSet && !u.docChanged) persist();
  });

  const baseExtensions = () => [
    // Display
    lineNumbers(),
    foldGutter(),
    highlightActiveLine(),
    highlightActiveLineGutter(),
    highlightSpecialChars(),
    highlightSelectionMatches(),
    placeholder('Empty file'),
    dropCursor(),

    // Selection / multi-cursor / Alt-drag block selection.
    EditorState.allowMultipleSelections.of(true),
    rectangularSelection(),
    crosshairCursor(),

    // Editing helpers — bracket pairs auto-close, indents
    // propagate on Enter, language-aware bracket matching.
    closeBrackets(),
    bracketMatching(),
    indentOnInput(),

    // History + search.
    history(),
    search(),

    // Autocomplete: word completions from the buffer for free;
    // language packs (lang-javascript, lang-html, …) layer
    // semantic completions on top when active.
    autocompletion(),

    // Syntax highlighting — wash-tuned palette in washHighlightStyle.
    syntaxHighlighting(washHighlightStyle, { fallback: true }),
    langCompartment.of([]),
    dirtyListener,
    searchListener,
    selectionListener,
    EditorView.domEventHandlers({
      // Scroll fires fast while wheeling — persist() is debounced
      // 250ms so the wire stays quiet. We read scroll out of the
      // live view at persist-time; no per-event capture needed.
      scroll() { persist(); return false; },
    }),

    keymap.of([
      ...closeBracketsKeymap,
      ...defaultKeymap,
      ...historyKeymap,
      ...searchKeymap,
      ...foldKeymap,
      ...completionKeymap,
      indentWithTab,
    ]),

    EditorView.theme({
      '&': { height: '100%', background: tokens.bgWindow, color: tokens.fg },
      '.cm-scroller': { font: `${tokens.fontSizeBase} ${tokens.fontMono}` },
      '.cm-content': { padding: '8px 0', caretColor: tokens.fg },
      '.cm-cursor': { borderLeftColor: tokens.fg },
      '.cm-activeLine': { backgroundColor: 'rgba(255,255,255,0.04)' },
      '.cm-activeLineGutter': { backgroundColor: 'rgba(255,255,255,0.06)' },
      '.cm-gutters': {
        background: tokens.bgMenu,
        color: tokens.fgDim,
        border: 'none',
        borderRight: `1px solid ${tokens.borderMenu}`,
      },
      '.cm-selectionBackground, ::selection': { background: tokens.bgRowSelected },
      '.cm-focused .cm-selectionBackground, .cm-focused ::selection': { background: tokens.bgRowSelected },

      // Search panel — match wash menus / overlays.
      '.cm-panels': {
        background: tokens.bgWindow,
        color: tokens.fg,
        borderTop: `1px solid ${tokens.borderMenu}`,
        font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
      },
      '.cm-panel.cm-search': {
        padding: '6px 8px',
        background: tokens.bgMenu,
        display: 'flex',
        'flex-wrap': 'wrap',
        gap: '6px',
        'align-items': 'center',
      },
      // All search-panel children share the same height so flex
      // align-items:center on the panel lines their visual centers
      // up. Otherwise input/button/label heights diverge by a few
      // pixels and the labels read as too high.
      '.cm-textfield': {
        background: '#10101a',
        color: tokens.fg,
        border: `1px solid ${tokens.borderMenu}`,
        borderRadius: `${tokens.radiusSm}px`,
        padding: '0 6px',
        height: '22px',
        lineHeight: '20px',
        boxSizing: 'border-box',
        font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
      },
      '.cm-textfield:focus': { outline: 'none', borderColor: tokens.borderFocus },
      '.cm-button': {
        background: 'transparent',
        color: tokens.fg,
        border: `1px solid ${tokens.borderMenu}`,
        borderRadius: `${tokens.radiusSm}px`,
        padding: '0 10px',
        height: '22px',
        boxSizing: 'border-box',
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        lineHeight: 1,
        cursor: 'pointer',
        font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
        backgroundImage: 'none',
      },
      '.cm-button:hover': { background: tokens.bgRowHover },
      '.cm-panel.cm-search [name="close"]': { color: tokens.fg, opacity: 0.6, fontSize: '16px' },

      // Themed checkboxes for the search-panel options (match case,
      // regexp, by word). CM6's default theme shrinks labels to 80%
      // and lays out checkbox + text inline, which throws off both
      // font and vertical centering. Override: full wash font on the
      // label, inline-flex with gap for clean centering, and a
      // Lucide-Check on a wash-bordered box for the checkbox itself.
      '.cm-panel.cm-search label': {
        display: 'inline-flex',
        alignItems: 'center',
        gap: '4px',
        cursor: 'pointer',
        font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
        height: '22px',
        lineHeight: 1,
      },
      '.cm-panel.cm-search input[type="checkbox"]': {
        appearance: 'none',
        '-webkit-appearance': 'none',
        width: '13px',
        height: '13px',
        background: tokens.bgWindow,
        border: `1px solid ${tokens.borderMenu}`,
        borderRadius: `${tokens.radiusSm}px`,
        cursor: 'pointer',
        margin: 0,
        padding: 0,
        backgroundRepeat: 'no-repeat',
        backgroundPosition: 'center',
        backgroundSize: '11px 11px',
        flexShrink: 0,
      },
      '.cm-panel.cm-search input[type="checkbox"]:hover': { borderColor: tokens.borderFocus },
      '.cm-panel.cm-search input[type="checkbox"]:focus': { outline: 'none', borderColor: tokens.borderFocus },
      '.cm-panel.cm-search input[type="checkbox"]:checked': {
        backgroundImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%23eeeeeed9' stroke-width='3' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M20 6 9 17l-5-5'/%3E%3C/svg%3E")`,
      },

      // Match highlights inside the document.
      '.cm-searchMatch': {
        background: 'rgba(180,180,80,0.25)',
        outline: '1px solid rgba(180,180,80,0.5)',
      },
      '.cm-searchMatch-selected': { background: 'rgba(180,180,80,0.5)' },
      '.cm-selectionMatch': { background: 'rgba(120,120,180,0.2)' },

      // Autocomplete popup — match menu styling.
      '.cm-tooltip.cm-tooltip-autocomplete': {
        background: tokens.bgMenu,
        border: `1px solid ${tokens.borderMenu}`,
        borderRadius: `${tokens.radiusMd}px`,
        boxShadow: tokens.shadowMenu,
        color: tokens.fg,
        font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
      },
      '.cm-tooltip.cm-tooltip-autocomplete > ul > li': { padding: '3px 8px' },
      '.cm-tooltip.cm-tooltip-autocomplete > ul > li[aria-selected]': {
        background: tokens.bgRowSelected,
        color: tokens.fg,
      },
    }, { dark: true }),
  ];

  // extensionsForTab layers tab-specific extensions on top of
  // baseExtensions. Today: unifiedMergeView for diff tabs (doc =
  // the "new" side, otherContent = the "original"). Regular tabs
  // get exactly baseExtensions() — same behavior as before.
  const extensionsForTab = (t: Tab | null) => {
    const base = baseExtensions();
    if (t?.diff) return [...base, unifiedMergeView({ original: t.diff.otherContent })];
    return base;
  };

  // washHighlightStyle replaces CM6's defaultHighlightStyle with a
  // palette tuned to wash's dark theme — soft purples for keywords,
  // light greens for strings, warm orange for numbers. Tag set is
  // the @lezer/highlight standard so every existing lang pack
  // (javascript / json / markdown + any future @codemirror/lang-*)
  // hits these styles automatically.
  const washHighlightStyle = HighlightStyle.define([
    { tag: t.keyword, color: '#c084fc' },
    { tag: t.controlKeyword, color: '#c084fc' },
    { tag: t.moduleKeyword, color: '#c084fc' },
    { tag: [t.string, t.special(t.string)], color: '#a3e635' },
    { tag: [t.number, t.bool, t.null, t.atom], color: '#fb923c' },
    { tag: t.comment, color: tokens.fgMuted, fontStyle: 'italic' },
    { tag: t.lineComment, color: tokens.fgMuted, fontStyle: 'italic' },
    { tag: t.blockComment, color: tokens.fgMuted, fontStyle: 'italic' },
    { tag: t.docComment, color: tokens.fgMuted, fontStyle: 'italic' },
    { tag: t.regexp, color: '#22d3ee' },
    { tag: t.escape, color: '#22d3ee' },
    { tag: t.operator, color: '#94a3b8' },
    { tag: t.compareOperator, color: '#94a3b8' },
    { tag: t.logicOperator, color: '#94a3b8' },
    { tag: t.arithmeticOperator, color: '#94a3b8' },
    { tag: t.punctuation, color: '#94a3b8' },
    { tag: t.bracket, color: '#cbd5e1' },
    { tag: t.brace, color: '#cbd5e1' },
    { tag: t.paren, color: '#cbd5e1' },
    { tag: [t.variableName, t.propertyName], color: '#e2e8f0' },
    { tag: [t.function(t.variableName), t.function(t.propertyName)], color: '#60a5fa' },
    { tag: t.typeName, color: '#34d399' },
    { tag: t.className, color: '#34d399' },
    { tag: t.namespace, color: '#34d399' },
    { tag: t.tagName, color: '#f472b6' },
    { tag: t.attributeName, color: '#fbbf24' },
    { tag: t.heading, color: '#c084fc', fontWeight: 'bold' },
    { tag: t.heading1, color: '#c084fc', fontWeight: 'bold' },
    { tag: t.heading2, color: '#c084fc', fontWeight: 'bold' },
    { tag: t.heading3, color: '#c084fc', fontWeight: 'bold' },
    { tag: t.link, color: '#60a5fa', textDecoration: 'underline' },
    { tag: t.url, color: '#60a5fa' },
    { tag: t.emphasis, fontStyle: 'italic' },
    { tag: t.strong, fontWeight: 'bold' },
    { tag: t.strikethrough, textDecoration: 'line-through' },
    { tag: t.meta, color: tokens.fgMuted },
    { tag: t.invalid, color: '#ef4444' },
  ]);

  // langForKey returns a CM6 language pack for an explicit key.
  // Used by both the path-derived default (langForPath) and the
  // Syntax menu's manual override.
  const langForKey = (key: string) => {
    switch (key) {
      case 'javascript':
        return javascript();
      case 'jsx':
        return javascript({ jsx: true });
      case 'typescript':
        return javascript({ typescript: true });
      case 'tsx':
        return javascript({ typescript: true, jsx: true });
      case 'json':
        return json();
      case 'markdown':
        return markdown();
      default:
        return [];
    }
  };

  // langKeyForPath picks a language key from the file extension.
  // Unknown extensions fall through to plain text.
  const langKeyForPath = (path: string): string => {
    const ext = path.toLowerCase().split('.').pop() ?? '';
    switch (ext) {
      case 'js':
      case 'mjs':
      case 'cjs':
        return 'javascript';
      case 'jsx':
        return 'jsx';
      case 'ts':
        return 'typescript';
      case 'tsx':
        return 'tsx';
      case 'json':
        return 'json';
      case 'md':
      case 'markdown':
        return 'markdown';
      default:
        return 'plain';
    }
  };

  // currentLang is what's actually configured in the editor right
  // now: the manual override if set, else the path-derived key.
  const currentLang = (): string => {
    const o = langOverride();
    if (o) return o;
    const t = activeTab();
    return t ? langKeyForPath(t.path) : 'plain';
  };

  // langExtensions builds the compartmented payload — language
  // pack + line-wrap flag. Recompiling both together keeps us to
  // one compartment.
  const langExtensions = () => {
    const ext: any[] = [langForKey(currentLang())];
    if (wordWrap()) ext.push(EditorView.lineWrapping);
    return ext;
  };

  // ---- lifecycle ----

  let bodyEl!: HTMLDivElement;
  let mainEl!: HTMLDivElement;

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail as BEMessage);
    props.host.addEventListener('wash:msg', onMsg);
    const onState = (ev: Event) => {
      const s = (ev as CustomEvent).detail as PersistedState | null;
      if (s) void restoreFrom(s);
    };
    props.host.addEventListener('wash:state', onState);

    // Window-resize → refit every mounted terminal + announce
    // the new size. The active terminal is what the user sees,
    // but background tabs still need fits so they're ready when
    // activated. fit.fit() is cheap on a hidden element.
    const ro = new ResizeObserver(() => {
      for (const [chID, ref] of termRefs) {
        if (!ref.fit || !ref.xterm) continue;
        ref.fit.fit();
        send({ kind: 'term.resize', channel_id: chID, cols: ref.xterm.cols, rows: ref.xterm.rows });
      }
    });
    ro.observe(props.host);

    // Create the EditorView once. Doc + language reconfigure on
    // file open via dispatch + compartment.
    editorView = new EditorView({
      state: EditorState.create({
        doc: '',
        extensions: baseExtensions(),
      }),
      parent: editorMountEl,
    });

    // App-level keyboard shortcuts. We bind on the host element
    // (not document) so they only fire when this editor window is
    // focused. CodeMirror's own keymaps already cover in-editor
    // shortcuts (find, undo, etc.); these handle file-level acts.
    // isTypingInEditor returns true when CodeMirror (or any input
    // / textarea / contenteditable, including the picker's path
    // input and the inline rename) has focus. Used to guard plain
    // keystrokes like F2 / Delete / Escape so they only fire when
    // the sidebar is the active surface — fm's same discipline.
    const isTypingInEditor = (): boolean => {
      const el = document.activeElement as HTMLElement | null;
      if (!el) return false;
      if (editorMountEl && editorMountEl.contains(el)) return true;
      const tag = el.tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA') return true;
      if (el.isContentEditable) return true;
      return false;
    };

    const onKey = (ev: KeyboardEvent) => {
      const cmd = ev.ctrlKey || ev.metaKey;

      // Sidebar-only plain keys: F2, Delete, Backspace, Escape.
      // Guarded so CM's own bindings (Backspace = delete char,
      // Escape = close search panel) still win when CM is focused.
      if (!cmd && !ev.altKey && !isTypingInEditor()) {
        if (ev.key === 'F2' && selectedPath()) {
          ev.preventDefault();
          startRename(selectedPath());
          return;
        }
        if ((ev.key === 'Delete' || ev.key === 'Backspace') && selectedPath()) {
          ev.preventDefault();
          void commitDelete(selectedPath());
          return;
        }
        if (ev.key === 'Escape' && selectedPath()) {
          ev.preventDefault();
          setSelectedPath('');
          return;
        }
        // Enter mimics fm: act on the selected row (open file /
        // expand folder / follow symlink). Same logic as
        // onRowDblClick so behavior is identical to double-click.
        if (ev.key === 'Enter' && selectedPath()) {
          ev.preventDefault();
          const par = parentPath(selectedPath());
          const entry = listings[par]?.find((x) => x.name === baseName(selectedPath()));
          if (entry) onRowDblClick({ entry, path: selectedPath() });
          return;
        }
      }

      if (!cmd) return;
      // Ctrl+S: save active tab.
      if ((ev.key === 's' || ev.key === 'S') && !ev.shiftKey) {
        ev.preventDefault();
        void saveActive();
        return;
      }
      // Ctrl+Shift+S: save-as on active tab.
      if ((ev.key === 's' || ev.key === 'S') && ev.shiftKey) {
        ev.preventDefault();
        saveAsActive();
        return;
      }
      // Ctrl+O: open file via picker.
      if (ev.key === 'o' || ev.key === 'O') {
        ev.preventDefault();
        setPicker({ mode: 'open' });
        return;
      }
      // Ctrl+N: new Untitled buffer.
      if (ev.key === 'n' || ev.key === 'N') {
        ev.preventDefault();
        newUntitled();
        return;
      }
      // Ctrl+W: close active tab.
      if (ev.key === 'w' || ev.key === 'W') {
        ev.preventDefault();
        const id = activeID();
        if (id) closeTab(id);
        return;
      }
      // Ctrl+` (VSCode parity): toggle terminal panel.
      if (ev.key === '`' && !ev.shiftKey) {
        ev.preventDefault();
        toggleTermPanel();
        return;
      }
      // Ctrl+Shift+`: new terminal (opening the panel if closed).
      if (ev.key === '~' || (ev.key === '`' && ev.shiftKey)) {
        ev.preventDefault();
        openNewTerm();
        return;
      }
    };
    props.host.addEventListener('keydown', onKey);
    if (!props.host.hasAttribute('tabindex')) props.host.setAttribute('tabindex', '0');

    // Boot with a list of "/" — the BE's Confine downshifts to
    // the sandbox root automatically when one is configured.
    void loadDir('/');
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('wash:state', onState);
      props.host.removeEventListener('keydown', onKey);
      ro.disconnect();
      // Release every active fs.watch so the BE doesn't strand
      // them after the editor window closes. Idempotent BE-side,
      // so we don't need to know which subs survived the run.
      for (const p of Array.from(watching)) {
        send({ kind: 'fs.unwatch', path: p });
      }
      watching.clear();
      for (const t of refreshTimers.values()) window.clearTimeout(t);
      refreshTimers.clear();
      // Dispose every live terminal — pty cleanup is on the BE
      // side via the channel close path, but we still want to
      // drop xterm DOM + listeners.
      for (const ref of termRefs.values()) {
        ref.unsub?.();
        ref.xterm?.dispose();
      }
      termRefs.clear();
      editorView?.destroy();
      editorView = undefined;
    });
  });

  // Trigger persist whenever the persisted slice changes. The
  // debounce inside persist() coalesces tab-switch bursts.
  createEffect(() => {
    // Track the dependencies explicitly so Solid re-runs only on
    // these.
    tabs();
    activeID();
    splitPct();
    termOpen();
    editPct();
    persist();
  });

  // When the active tab changes, swap CM's whole state. We
  // capture-then-restore each tab's EditorState so undo, cursor,
  // and scroll are per-tab. First activation of a tab seeds a
  // fresh state from its baseline + the language matching its
  // path; subsequent activations reuse the captured state.
  //
  // Language override clears on tab switch — it's a "treat THIS
  // tab as X" rather than a permanent setting.
  let lastTabID = '';
  createEffect(() => {
    const id = activeID();
    if (!editorView) return;
    if (id !== lastTabID) {
      lastTabID = id;
      setLangOverride(null);
    }
    const t = tabs().find((x) => x.id === id);
    if (!t) {
      // No active tab — leave the view empty.
      editorView.setState(EditorState.create({ doc: '', extensions: baseExtensions() }));
      return;
    }
    if (t.state) {
      editorView.setState(t.state);
    } else {
      const fresh = EditorState.create({
        doc: t.baseline,
        extensions: extensionsForTab(t),
      });
      editorView.setState(fresh);
    }
    editorView.dispatch({ effects: langCompartment.reconfigure(langExtensions()) });
    editorView.focus();
  });

  // Reactively reconfigure the language compartment whenever the
  // override or wordWrap change. Tab-switch already handles its
  // own reconfigure above.
  createEffect(() => {
    langOverride();
    wordWrap();
    if (!editorView) return;
    editorView.dispatch({ effects: langCompartment.reconfigure(langExtensions()) });
  });

  // ---- render ----

  return (
    <>
      {/* menu bar */}
      <div data-testid="edit-menubar" style={menuBarStyle}>
        <MenuBarButton id="file" label="File" active={openMenu() === 'file'} onClick={openMenuFor} />
        <MenuBarButton id="edit" label="Edit" active={openMenu() === 'edit'} onClick={openMenuFor} />
        <MenuBarButton id="syntax" label="Syntax" active={openMenu() === 'syntax'} onClick={openMenuFor} />
        <MenuBarButton id="terminal" label="Terminal" active={openMenu() === 'terminal'} onClick={openMenuFor} />
        <Show when={openMenu() === 'file'}>
          <Menu x={menuAnchor().x} y={menuAnchor().y} onDismiss={closeMenu} data-testid="edit-menu-file">
            <MenuItem label="New" trailing={<kbd style={kbdStyle}>Ctrl+N</kbd>} onClick={run(newUntitled)} data-testid="edit-menu-new" />
            <MenuItem label="Open…" trailing={<kbd style={kbdStyle}>Ctrl+O</kbd>} onClick={run(() => setPicker({ mode: 'open' }))} data-testid="edit-menu-open" />
            <MenuSeparator />
            <MenuItem label="Save" trailing={<kbd style={kbdStyle}>Ctrl+S</kbd>} disabled={!activeTab()} onClick={run(() => void saveActive())} data-testid="edit-menu-save" />
            <MenuItem label="Save As…" trailing={<kbd style={kbdStyle}>Ctrl+Shift+S</kbd>} disabled={!activeTab()} onClick={run(saveAsActive)} data-testid="edit-menu-save-as" />
            <MenuSeparator />
            <MenuItem label="Close Tab" trailing={<kbd style={kbdStyle}>Ctrl+W</kbd>} disabled={!activeTab()} onClick={run(() => closeTab(activeID()))} data-testid="edit-menu-close-tab" />
          </Menu>
        </Show>
        <Show when={openMenu() === 'edit'}>
          <Menu x={menuAnchor().x} y={menuAnchor().y} onDismiss={closeMenu} data-testid="edit-menu-edit">
            <MenuItem label="Undo" trailing={<kbd style={kbdStyle}>Ctrl+Z</kbd>} onClick={run(cmdUndo)} data-testid="edit-menu-undo" />
            <MenuItem label="Redo" trailing={<kbd style={kbdStyle}>Ctrl+Shift+Z</kbd>} onClick={run(cmdRedo)} data-testid="edit-menu-redo" />
            <MenuSeparator />
            <MenuItem label="Cut" trailing={<kbd style={kbdStyle}>Ctrl+X</kbd>} onClick={run(() => cmdClipboard('cut'))} data-testid="edit-menu-cut" />
            <MenuItem label="Copy" trailing={<kbd style={kbdStyle}>Ctrl+C</kbd>} onClick={run(() => cmdClipboard('copy'))} data-testid="edit-menu-copy" />
            <MenuItem label="Paste" trailing={<kbd style={kbdStyle}>Ctrl+V</kbd>} onClick={run(() => cmdClipboard('paste'))} data-testid="edit-menu-paste" />
            <MenuSeparator />
            <MenuItem label="Find" trailing={<kbd style={kbdStyle}>Ctrl+F</kbd>} onClick={run(cmdFind)} data-testid="edit-menu-find" />
            <MenuItem label="Find & Replace" trailing={<kbd style={kbdStyle}>Ctrl+H</kbd>} onClick={run(cmdFind)} data-testid="edit-menu-replace" />
          </Menu>
        </Show>
        <Show when={openMenu() === 'terminal'}>
          <Menu x={menuAnchor().x} y={menuAnchor().y} onDismiss={closeMenu} data-testid="edit-menu-terminal">
            <MenuItem
              label="New Terminal"
              trailing={<kbd style={kbdStyle}>Ctrl+Shift+`</kbd>}
              onClick={run(openNewTerm)}
              data-testid="edit-menu-term-new"
            />
            <MenuItem
              label="Close Terminal"
              disabled={!activeTermID()}
              onClick={run(() => closeTerm(activeTermID()))}
              data-testid="edit-menu-term-close"
            />
            <MenuSeparator />
            <MenuItem
              label={termOpen() ? 'Hide Panel' : 'Show Panel'}
              trailing={<kbd style={kbdStyle}>Ctrl+`</kbd>}
              onClick={run(toggleTermPanel)}
              data-testid="edit-menu-term-toggle"
            />
          </Menu>
        </Show>
        <Show when={openMenu() === 'syntax'}>
          <Menu x={menuAnchor().x} y={menuAnchor().y} onDismiss={closeMenu} data-testid="edit-menu-syntax">
            <For each={langChoices}>
              {(l) => (
                <MenuItem
                  label={l.label}
                  trailing={currentLang() === l.key ? <span style={menuCheckStyle}><Check size={12} /></span> : undefined}
                  onClick={run(() => setLang(l.key === langKeyForPath(activeTab()?.path ?? '') ? null : l.key))}
                  data-testid={`edit-menu-lang-${l.key}`}
                />
              )}
            </For>
            <MenuSeparator />
            <MenuItem
              label="Word Wrap"
              trailing={wordWrap() ? <span style={menuCheckStyle}><Check size={12} /></span> : undefined}
              onClick={run(toggleWrap)}
              data-testid="edit-menu-wrap"
            />
          </Menu>
        </Show>
      </div>

      <div
        ref={mainEl!}
        style={{
          display: 'grid',
          // Vertical split: editor row on top, optional terminal
          // pane on the bottom. When the pane is closed the whole
          // cell is the editor.
          'grid-template-rows': termOpen()
            ? `${editPct()}% 4px ${100 - editPct()}%`
            : '1fr',
          overflow: 'hidden',
          'border-bottom': `1px solid ${tokens.borderWindow}`,
        }}
      >
      <div
        ref={bodyEl!}
        style={{ ...bodyStyle, 'grid-template-columns': `${splitPct()}% 4px 1fr` }}
      >
        {/* sidebar */}
        <div data-testid="edit-sidebar" style={sidebarStyle}>
          <div style={sidebarHeaderStyle}>
            <span style={{
              flex: 1,
              overflow: 'hidden',
              'text-overflow': 'ellipsis',
              'white-space': 'nowrap',
            }}>{root() || 'loading…'}</span>
            <button
              type="button"
              data-testid="edit-open-in-fm"
              title="Open in fm"
              onClick={openInFm}
              style={sidebarHeaderBtnStyle}
            >
              <FolderIcon size={12} />
            </button>
          </div>
          <div
            style={sidebarListStyle}
            onDragOver={onListDragOver}
            onDrop={onListDrop}
          >
            <For each={visibleRows()}>
              {(row) => {
                const sel = () => selectedPath() === row.path;
                const isExpanded = () => !!expanded[row.path];
                return (
                  <div
                    data-testid={`edit-entry-${row.entry.name}`}
                    data-type={row.entry.type}
                    data-selected={sel() ? 'true' : undefined}
                    data-drop-target={dropTargetPath() === row.path ? 'true' : undefined}
                    draggable="true"
                    onDragStart={(ev) => onRowDragStart(ev, row.path)}
                    onDragEnd={onRowDragEnd}
                    onDragOver={row.entry.type === 'dir' ? (ev) => onRowDragOver(ev, row.path) : undefined}
                    onDrop={row.entry.type === 'dir' ? (ev) => onRowDrop(ev, row.path) : undefined}
                    style={rowStyleDropAware(sel(), dropTargetPath() === row.path, row.depth)}
                    onClick={() => {
                      if (renaming()?.path === row.path) return;
                      onRowClick(row);
                    }}
                    onDblClick={() => {
                      if (renaming()?.path === row.path) return;
                      onRowDblClick(row);
                    }}
                    onContextMenu={(ev) => openCtxMenu(ev, row.entry, row.path)}
                  >
                    {/* chevron + icon + name — same visual contract as
                        wash-fm's TreeRow: 12px chevron slot, 14px icon
                        slot, lucide-solid glyphs. Empty chevron slot
                        keeps file rows' icons aligned with folders'. */}
                    <span
                      data-testid={`edit-chevron-${row.entry.name}`}
                      style={{
                        width: '12px',
                        display: 'inline-flex',
                        'align-items': 'center',
                        'justify-content': 'center',
                        opacity: 0.6,
                        'flex-shrink': 0,
                        cursor: row.entry.type === 'dir' ? 'pointer' : 'default',
                      }}
                      onClick={(ev) => {
                        if (row.entry.type === 'dir') {
                          ev.stopPropagation();
                          toggleExpand(row.path);
                        }
                      }}
                    >
                      <Show when={row.entry.type === 'dir'}>
                        {isExpanded() ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                      </Show>
                    </span>
                    <span style={{
                      width: '14px',
                      display: 'inline-flex',
                      'align-items': 'center',
                      'justify-content': 'center',
                      opacity: 0.8,
                      'flex-shrink': 0,
                    }}>
                      <EntryIcon type={row.entry.type} />
                    </span>
                    <span style={{
                      flex: 1,
                      overflow: 'hidden',
                      'text-overflow': 'ellipsis',
                      'white-space': 'nowrap',
                    }}>
                      <Show
                        when={renaming()?.path === row.path}
                        fallback={row.entry.name}
                      >
                        <input
                          data-testid="edit-rename-input"
                          ref={(el) => setTimeout(() => { el.focus(); el.select(); }, 0)}
                          type="text"
                          value={renaming()!.draft}
                          onInput={(e) => {
                            const r = renaming();
                            if (r) setRenaming({ ...r, draft: e.currentTarget.value });
                          }}
                          onClick={(e) => e.stopPropagation()}
                          onKeyDown={(e) => {
                            e.stopPropagation();
                            if (e.key === 'Enter') {
                              e.preventDefault();
                              void commitRenameDraft();
                            } else if (e.key === 'Escape') {
                              e.preventDefault();
                              cancelRename();
                            }
                          }}
                          onBlur={() => void commitRenameDraft()}
                          style={renameInputStyle}
                        />
                      </Show>
                    </span>
                  </div>
                );
              }}
            </For>
          </div>
        </div>

        <Splitter container={bodyEl} onChange={setSplitPct} data-testid="edit-splitter" />

        {/* editor area — tab bar above, CodeMirror below */}
        <div data-testid="edit-pane" style={editorPaneStyle}>
          {/* tab bar */}
          <div data-testid="edit-tabs" style={tabBarStyle}>
            <For each={tabs()}>
              {(t) => {
                const isActive = () => activeID() === t.id;
                const isDirty = () => dirtyIDs().has(t.id);
                return (
                  <div
                    data-testid={`edit-tab-${t.id}`}
                    data-active={isActive() ? 'true' : undefined}
                    data-dirty={isDirty() ? 'true' : undefined}
                    onClick={() => { captureActiveState(); setActiveID(t.id); }}
                    style={tabStyle(isActive())}
                  >
                    <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis' }}>
                      {t.displayName}
                    </span>
                    <span
                      data-testid={`edit-tab-close-${t.id}`}
                      onClick={(ev) => { ev.stopPropagation(); closeTab(t.id); }}
                      style={tabCloseStyle}
                      title="Close (Ctrl+W)"
                    >
                      <Show when={isDirty()} fallback="×">●</Show>
                    </span>
                  </div>
                );
              }}
            </For>
          </div>

          {/* CM mount + placeholder overlays */}
          <div style={editorBodyStyle}>
            <div
              ref={editorMountEl!}
              data-testid="edit-cm"
              style={{ position: 'absolute', inset: 0 }}
            />
            <Show when={!activeTab() || activeTab()?.binary}>
              <div data-testid="edit-placeholder" style={placeholderOverlayStyle}>
                <Show when={!activeTab()}>
                  Pick a file from the sidebar, or Ctrl+N for an empty buffer.
                </Show>
                <Show when={activeTab()?.binary}>
                  <div>{activeTab()?.path}</div>
                  <div style={{ color: tokens.fgDim, 'margin-top': '6px' }}>
                    Binary file — not displayed.
                  </div>
                </Show>
              </div>
            </Show>
          </div>
        </div>
      </div>

      {/* terminal pane (toggleable) */}
      <Show when={termOpen()}>
        <Splitter
          orientation="horizontal"
          container={mainEl}
          min={20}
          max={90}
          onChange={setEditPct}
          onCommit={persist}
          data-testid="edit-vsplit"
        />
        <div data-testid="edit-term-pane" style={termPaneStyle}>
          {/* tab bar */}
          <div data-testid="edit-term-tabs" style={termTabBarStyle}>
            <For each={termTabs()}>
              {(t) => {
                const isActive = () => activeTermID() === t.id;
                return (
                  <div
                    data-testid={`edit-term-tab-${t.id}`}
                    data-active={isActive() ? 'true' : undefined}
                    onClick={() => setActiveTermID(t.id)}
                    style={tabStyle(isActive())}
                  >
                    <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis' }}>
                      {t.title}
                    </span>
                    <span
                      data-testid={`edit-term-tab-close-${t.id}`}
                      onClick={(ev) => { ev.stopPropagation(); closeTerm(t.id); }}
                      style={tabCloseStyle}
                      title="Close terminal"
                    >
                      ×
                    </span>
                  </div>
                );
              }}
            </For>
            <button
              type="button"
              data-testid="edit-term-new"
              onClick={openNewTerm}
              style={termNewBtnStyle}
              title="New Terminal (Ctrl+Shift+`)"
            >
              +
            </button>
          </div>
          {/* terminal hosts — one DOM element per channel, only
              the active one is visible. We keep them mounted (not
              just rendered behind a Show) so xterm doesn't lose
              its host element on tab switch. */}
          <div style={termBodyStyle}>
            <For each={termTabs()}>
              {(t) => (
                <div
                  data-testid={`edit-term-host-${t.id}`}
                  style={{
                    position: 'absolute',
                    inset: 0,
                    display: activeTermID() === t.id ? 'block' : 'none',
                  }}
                />
              )}
            </For>
          </div>
        </div>
      </Show>
      </div>

      <StatusBar data-testid="edit-status">
        <Show when={activeTab()} fallback={`${tabs().length} tabs`}>
          <span>
            {activeTab()!.path || activeTab()!.displayName}
          </span>
          <Show when={dirtyIDs().has(activeTab()!.id)}>
            <span style={{ 'margin-left': '8px', color: tokens.fgDim }}>· modified</span>
          </Show>
          <Show when={activeTab()!.binary}>
            <span style={{ 'margin-left': '8px', color: tokens.fgDim }}>· binary</span>
          </Show>
        </Show>
      </StatusBar>

      <FilePicker
        open={picker() !== null}
        mode={picker()?.mode ?? 'open'}
        host={props.host}
        hostInstanceID={props.instance}
        defaultName={picker()?.mode === 'save' ? (picker() as { suggestedName: string }).suggestedName : undefined}
        onConfirm={(p) => void pickerConfirm(p)}
        onCancel={() => setPicker(null)}
        data-testid="edit-picker"
      />

      {/* right-click context menu — fires on row right-click. */}
      <Show when={ctxMenu()}>
        <Menu
          x={ctxMenu()!.x}
          y={ctxMenu()!.y}
          onDismiss={closeCtxMenu}
          data-testid="edit-ctx-menu"
        >
          <MenuItem
            label={ctxMenu()!.entry.type === 'dir' ? 'Expand' : 'Open'}
            onClick={() => {
              const c = ctxMenu()!;
              closeCtxMenu();
              onRowDblClick({ entry: c.entry, path: c.path });
            }}
            data-testid="edit-ctx-open"
          />
          <MenuItem
            label="Copy path"
            onClick={() => {
              const c = ctxMenu()!;
              closeCtxMenu();
              void ctxCopyPath(c.path);
            }}
            data-testid="edit-ctx-copy-path"
          />
          {/* Diff against the currently-active file. Only shown when
              there's an active tab whose path differs from the
              right-clicked file (no point diffing a file to itself). */}
          <Show when={
            ctxMenu()!.entry.type === 'file'
            && activeTab()
            && (activeTab()!.path || activeTab()!.displayName)
            && activeTab()!.path !== ctxMenu()!.path
          }>
            <MenuItem
              label={`Diff to ${activeTab()!.path ? (baseName(activeTab()!.path) || activeTab()!.path) : activeTab()!.displayName}`}
              onClick={() => {
                const c = ctxMenu()!;
                closeCtxMenu();
                void openDiffTab(c.path);
              }}
              data-testid="edit-ctx-diff"
            />
          </Show>
          <MenuSeparator />
          <MenuItem
            label="Rename"
            onClick={() => {
              const c = ctxMenu()!;
              closeCtxMenu();
              startRename(c.path);
            }}
            data-testid="edit-ctx-rename"
          />
          <MenuItem
            label="Delete"
            onClick={() => {
              const c = ctxMenu()!;
              closeCtxMenu();
              void commitDelete(c.path);
            }}
            data-testid="edit-ctx-delete"
          />
        </Menu>
      </Show>

      {/* alt-drop menu — appears when the user drops with Alt held.
          Move + Copy use the drop target; Rename + Delete operate
          on the dragged path. */}
      <Show when={dropMenu()}>
        <Menu
          x={dropMenu()!.x}
          y={dropMenu()!.y}
          onDismiss={() => setDropMenu(null)}
          data-testid="edit-drop-menu"
        >
          <MenuItem
            label="Move here"
            onClick={() => {
              const d = dropMenu()!;
              setDropMenu(null);
              void commitMove(d.src, d.destDir);
            }}
            data-testid="edit-drop-move"
          />
          <MenuItem
            label="Copy here"
            onClick={() => {
              const d = dropMenu()!;
              setDropMenu(null);
              commitCopy(d.src, d.destDir);
            }}
            data-testid="edit-drop-copy"
          />
          <MenuSeparator />
          <MenuItem
            label="Rename"
            onClick={() => {
              const d = dropMenu()!;
              setDropMenu(null);
              startRename(d.src);
            }}
            data-testid="edit-drop-rename"
          />
          <MenuItem
            label="Delete"
            onClick={() => {
              const d = dropMenu()!;
              setDropMenu(null);
              void commitDelete(d.src);
            }}
            data-testid="edit-drop-delete"
          />
        </Menu>
      </Show>
    </>
  );
};

// EntryIcon picks the lucide glyph for a given entry type. Mirrors
// the helper of the same name in wash-fm so the sidebar tree looks
// identical to fm's: folder, file, symlink, or fallback file.
const EntryIcon: Component<{ type: Entry['type'] }> = (props) => {
  switch (props.type) {
    case 'dir':
      return <FolderIcon size={12} />;
    case 'symlink':
      return <Link2 size={12} />;
    case 'file':
      return <FileIcon size={12} />;
    default:
      return <FileIcon size={12} />;
  }
};

// ---- menu bar pieces ----

// langChoices is the Syntax menu list. Adding a new language pack
// is two lines: import the package, append a row here, and the
// menu picks it up. langForKey resolves the actual extension.
const langChoices = [
  { key: 'plain', label: 'Plain Text' },
  { key: 'javascript', label: 'JavaScript' },
  { key: 'jsx', label: 'JavaScript (JSX)' },
  { key: 'typescript', label: 'TypeScript' },
  { key: 'tsx', label: 'TypeScript (TSX)' },
  { key: 'json', label: 'JSON' },
  { key: 'markdown', label: 'Markdown' },
];

type MenuID = 'file' | 'edit' | 'syntax' | 'terminal';

const MenuBarButton: Component<{
  id: MenuID;
  label: string;
  active: boolean;
  onClick: (id: MenuID, ev: MouseEvent) => void;
}> = (props) => {
  return (
    <button
      type="button"
      data-testid={`edit-menubar-${props.id}`}
      onClick={(ev) => props.onClick(props.id, ev)}
      style={menuBarButtonStyle(props.active)}
    >
      {props.label}
    </button>
  );
};

// ---- helpers ----

function joinPath(parent: string, name: string): string {
  if (parent.endsWith('/')) return parent + name;
  return parent + '/' + name;
}

function baseName(p: string): string {
  const i = p.lastIndexOf('/');
  return i < 0 ? p : p.slice(i + 1);
}

function parentPath(p: string): string {
  if (!p || p === '/') return '/';
  const i = p.lastIndexOf('/');
  if (i <= 0) return '/';
  return p.slice(0, i);
}

// ---- styles ----

const bodyStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-rows': '1fr',
  overflow: 'hidden',
  height: '100%',
  'border-bottom': `1px solid ${tokens.borderWindow}`,
};

const menuBarStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  background: tokens.bgMenu,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  'min-height': '24px',
  'flex-shrink': 0,
  position: 'relative',
  'user-select': 'none',
};

function menuBarButtonStyle(active: boolean): JSX.CSSProperties {
  return {
    background: active ? tokens.bgRowSelected : 'transparent',
    color: tokens.fg,
    border: 'none',
    padding: '2px 10px',
    height: '24px',
    cursor: 'pointer',
    font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
  };
}

const kbdStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  color: tokens.fgMuted,
  background: 'transparent',
  padding: '0 4px',
};

// menuCheckStyle wraps the lucide Check icon used as the "active"
// indicator on toggleable menu items (Syntax language picker,
// Word Wrap). Same color as the menu item's text so the active
// state reads as confirmation rather than competing emphasis.
const menuCheckStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  color: tokens.fg,
  opacity: 0.85,
};

const termPaneStyle: JSX.CSSProperties = {
  background: '#000',
  display: 'flex',
  'flex-direction': 'column',
  overflow: 'hidden',
};

const termTabBarStyle: JSX.CSSProperties = {
  display: 'flex',
  background: tokens.bgMenu,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  'min-height': '26px',
  'flex-shrink': 0,
};

const termBodyStyle: JSX.CSSProperties = {
  flex: 1,
  position: 'relative',
  overflow: 'hidden',
  background: '#000',
};

const termNewBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fgMuted,
  border: 'none',
  padding: '0 10px',
  height: '26px',
  cursor: 'pointer',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
};

const sidebarStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  background: tokens.bgMenu,
  overflow: 'hidden',
};

const sidebarHeaderStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '6px',
  padding: '4px 6px 4px 10px',
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
  color: tokens.fgMuted,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
};

const sidebarHeaderBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fgMuted,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}px`,
  width: '22px',
  height: '22px',
  display: 'inline-flex',
  'align-items': 'center',
  'justify-content': 'center',
  cursor: 'pointer',
  'flex-shrink': 0,
};

const renameInputStyle: JSX.CSSProperties = {
  width: '100%',
  background: '#10101a',
  color: tokens.fg,
  border: `1px solid ${tokens.borderFocus}`,
  'border-radius': `${tokens.radiusSm}px`,
  padding: '0 4px',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  outline: 'none',
};

const sidebarListStyle: JSX.CSSProperties = {
  flex: 1,
  overflow: 'auto',
  padding: '4px 0',
};

function rowStyleDropAware(selected: boolean, dropTarget: boolean, depth: number): JSX.CSSProperties {
  // Drop-target highlight takes precedence over selected so the
  // user always sees where the drop will land. Same colour scheme
  // wash-fm uses for visual consistency between the two trees.
  const base = rowStyle(selected, depth);
  if (!dropTarget) return base;
  return {
    ...base,
    background: '#2a3a5a',
    outline: '1px solid #4a6ab0',
  };
}

function rowStyle(selected: boolean, depth: number): JSX.CSSProperties {
  // Match wash-fm's TreeRow visual: 4px gap between chevron / icon /
  // name, depth-indented via padding-left, 3px row vertical pad.
  return {
    display: 'flex',
    'align-items': 'center',
    gap: '4px',
    padding: `3px 8px 3px ${8 + depth * 12}px`,
    background: selected ? tokens.bgRowSelected : 'transparent',
    color: tokens.fg,
    cursor: 'pointer',
    'user-select': 'none',
    font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  };
}

const editorPaneStyle: JSX.CSSProperties = {
  background: tokens.bgWindow,
  overflow: 'hidden',
  display: 'flex',
  'flex-direction': 'column',
};

const editorBodyStyle: JSX.CSSProperties = {
  flex: 1,
  position: 'relative',
  overflow: 'hidden',
};

const tabBarStyle: JSX.CSSProperties = {
  display: 'flex',
  overflow: 'auto',
  background: tokens.bgMenu,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  'min-height': '26px',
  'flex-shrink': 0,
};

function tabStyle(active: boolean): JSX.CSSProperties {
  return {
    display: 'flex',
    'align-items': 'center',
    gap: '6px',
    padding: '0 8px',
    height: '26px',
    'border-right': `1px solid ${tokens.borderMenu}`,
    // Rounded only on top so the tab visually sits on the bar's
    // border-bottom — matches wash-term's tab styling.
    'border-radius': '6px 6px 0 0',
    background: active ? tokens.bgWindow : 'transparent',
    color: active ? tokens.fg : tokens.fgMuted,
    cursor: 'pointer',
    font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
    'user-select': 'none',
    'max-width': '200px',
    overflow: 'hidden',
    'white-space': 'nowrap',
    'flex-shrink': 0,
  };
}

const tabCloseStyle: JSX.CSSProperties = {
  width: '14px',
  height: '14px',
  display: 'inline-flex',
  'align-items': 'center',
  'justify-content': 'center',
  'border-radius': '2px',
  font: '11px ui-monospace,Menlo,Consolas,monospace',
  color: tokens.fgMuted,
};

const placeholderOverlayStyle: JSX.CSSProperties = {
  position: 'absolute',
  inset: 0,
  display: 'flex',
  'flex-direction': 'column',
  'align-items': 'center',
  'justify-content': 'center',
  background: tokens.bgWindow,
  color: tokens.fgMuted,
  font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
  'pointer-events': 'none',
};

// ---- custom element ----

// Grid: menubar (auto) | body (1fr) | status bar (auto). The
// menubar row carries `position: relative` so the absolute-
// positioned <Menu> dropdowns inside it anchor relative to it
// rather than escaping to the closest positioned ancestor.
defineWashApp('wash-app-edit', (props) => <App {...props} />, {
  style: `display:grid;grid-template-rows:auto 1fr auto;height:100%;background:${tokens.bgWindow};color:${tokens.fg};overflow:hidden;position:relative`,
});
