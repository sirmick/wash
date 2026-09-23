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
import { AgentSession, Button, ConfirmDialog, FilePicker, FileTree, Input, isDirLike, Menu, MenuItem, MenuSeparator, Overlay, Splitter, StatusBar, Tab, Terminal, defineWashApp, tokens, washCopyText, washPasteText, washAppearance, onAppearanceChange } from '@wash/ui';
import type { InsertedDraft } from '@wash/ui';
import type { AgentAsk, AgentEvent, AgentStatus, TerminalAPI } from '@wash/ui';
import { applyAgentEvent } from '@wash/ui';

// One roster row as agentd publishes it; only the fields this pane reads.
interface AgentRow {
  key: string;
  agent?: string;
  state?: string;
  dir?: string;
  title?: string;
  used?: number;
  size?: number;
  mode?: string;
  modes?: { id: string; name: string; description?: string }[];
  configs?: AgentStatus['configs'];
  commands?: { name: string; description?: string }[];
  yolo?: boolean;
}
import {
  joinPath, baseName, parentPath,
  createBus,
  createWatch,
  DRAG_MIME, readDragPaths, hasWashDrag, dropEffectFor,
  flattenTree,
} from '@wash/fs-client';
import { EditorSelection, EditorState, Compartment, Extension } from '@codemirror/state';
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
import { getSearchQuery, gotoLine, highlightSelectionMatches, openSearchPanel, searchKeymap, searchPanelOpen, SearchQuery, setSearchQuery, search } from '@codemirror/search';
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
  indentUnit,
  syntaxHighlighting,
  StreamLanguage,
} from '@codemirror/language';
import { solarizedDarkStyle, solarizedLightStyle } from '@uiw/codemirror-theme-solarized';
import { javascript } from '@codemirror/lang-javascript';
import { json } from '@codemirror/lang-json';
import { markdown } from '@codemirror/lang-markdown';
import { html } from '@codemirror/lang-html';
import { css } from '@codemirror/lang-css';
import { xml } from '@codemirror/lang-xml';
import { python } from '@codemirror/lang-python';
import { rust } from '@codemirror/lang-rust';
import { go } from '@codemirror/lang-go';
import { cpp } from '@codemirror/lang-cpp';
import { java } from '@codemirror/lang-java';
import { php } from '@codemirror/lang-php';
import { sql } from '@codemirror/lang-sql';
import { yaml } from '@codemirror/lang-yaml';
import { sass } from '@codemirror/lang-sass';
// Legacy modes: parsers for langs that don't have a dedicated CM6
// lang-* pack. StreamLanguage.define adapts them into a LanguageSupport.
import { shell } from '@codemirror/legacy-modes/mode/shell';
import { dockerFile } from '@codemirror/legacy-modes/mode/dockerfile';
import { cmake } from '@codemirror/legacy-modes/mode/cmake';
import { ruby } from '@codemirror/legacy-modes/mode/ruby';
import { lua } from '@codemirror/legacy-modes/mode/lua';
import { perl } from '@codemirror/legacy-modes/mode/perl';
import { toml } from '@codemirror/legacy-modes/mode/toml';
import { properties } from '@codemirror/legacy-modes/mode/properties';
import { nginx } from '@codemirror/legacy-modes/mode/nginx';
import { diff } from '@codemirror/legacy-modes/mode/diff';
// xterm + addon-fit are externalized to /vendor/xterm.js. The vendor
// bundle auto-injects the xterm CSS, so no manual <style> shim here.
import {
  Bold,
  Check,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  Code as CodeIcon,
  File as FileIcon,
  Folder as FolderIcon,
  Heading1,
  Heading2,
  Heading3,
  Image as ImageIcon,
  Italic,
  Link as LinkIcon,
  Link2,
  List,
  ListOrdered,
  ListTodo,
  Lock,
  Minus,
  Quote,
  Strikethrough,
  Table as TableIcon,
  Terminal as TerminalIcon,
} from 'lucide-solid';
import { createWysiwyg, isMarkdownPath, type WysiwygHandle, type WysiwygSearchState } from './wysiwyg';
import { pushRecent, dropRecent, rankFiles } from './quick-open';
import { DEFAULT_INDENT, detectIndent, indentLabel, indentString, normalizeForSave, type Indent } from './indent';

// Prefs is the desktop-wide preference file ($XDG_CONFIG_HOME/wash/
// edit.json), owned by the edit BE (prefs.go). Every key is optional:
// a missing one means the built-in default. Per-window state (tabs,
// cursor, split) is PersistedState, not this.
interface Prefs {
  font_size?: number;
  indent_unit?: 'spaces' | 'tabs';
  indent_width?: number;
  trim_trailing?: boolean;
  final_newline?: boolean;
  recent?: string[];
}

interface Entry {
  name: string;
  type: 'dir' | 'file' | 'symlink' | 'other';
  size: number;
  mod_unix: number;
  // created_unix is part of the BE's fs.Entry (the wire data always has
  // it); declared here so Entry satisfies @wash/fs-client's SortableEntry
  // for flattenTree. edit only name-sorts, so it's otherwise unused.
  created_unix: number;
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
  // 'wysiwyg' on .md tabs reopened in the rich editor; 'source' (or
  // missing) keeps the existing CodeMirror-based behavior. Persisting
  // mode lets us honor a per-tab toggle across reloads.
  mode?: 'source' | 'wysiwyg';
  // Per-tab view settings. Both used to be one window-wide signal that
  // reset on every tab switch and every reload: turning wrap on for a
  // log, or forcing a syntax on an extensionless file, lasted exactly
  // as long as you stayed on that tab.
  wrap?: boolean;
  lang?: string;
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
  // 'pty' is a shell this app opened; 'agent' is a coding-agent session
  // agentd hosts and this pane renders (docs/AGENT_TABS.md). The pane is
  // the same, the body differs — and so does the lifetime: a pty dies with
  // the editor, an agent session outlives it.
  kind?: 'pty' | 'agent';
  // Agent tabs: the agentd session key, once it has started.
  agentKey?: string;
  agentName?: string;
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
  // Why the tab is a placeholder rather than a buffer, when it is one.
  // Such a tab is never written back: a save used to write an empty
  // document over a binary, the truncated prefix over a large file,
  // and U+FFFD over Latin-1. Binary stays as the legacy alias.
  blocked?: Blocked;
  // On-disk size, for the too-large placeholder.
  size?: number;
  // Line endings on disk. The buffer is always LF; see toDisk.
  eol?: Eol;
  // Word wrap for this tab, and a manual syntax override ('' / absent =
  // derive from the path). Per tab because they are properties of what
  // you are looking at, not of the window.
  wrap?: boolean;
  lang?: string | null;
  // The file's mode (or its mount) denies this process a write. Not a
  // `blocked` reason: the buffer is a perfectly good editable buffer,
  // it just cannot go back where it came from, so Ctrl+S routes to
  // Save As instead of failing at the BE.
  readOnlyFile?: boolean;
  // Indentation, detected from the file's own content on open and
  // falling back to the prefs default when there is nothing to detect
  // from. Drives CM's indentUnit + tabSize and the status bar.
  indent?: Indent;
  // The file vanished from disk under the tab (an external rename, rm,
  // git checkout). The buffer is kept; the status bar says so and the
  // next save goes through the picker instead of silently recreating
  // the old path. Cleared when a read finds the file again or when the
  // editor itself re-keys the tab.
  missing?: boolean;
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
  // Editor mode. 'wysiwyg' is the rich markdown editor (TipTap);
  // 'source' is CodeMirror with markdown syntax highlighting. Only
  // .md / .markdown paths default to 'wysiwyg'; everything else is
  // 'source' and the toggle in the View menu is a no-op for them.
  mode: 'source' | 'wysiwyg';
  // wysCache is the last-known markdown for a wysiwyg tab. We snap
  // it on tab-switch out and on dirty-clear so a re-mount of the
  // host element (or a reload) can seed the editor without going
  // back to disk.
  wysCache?: string;
}

type Blocked = 'binary' | 'too_large' | 'encoding';

// blockedOf reads the BE's read-only reason off a read_ok, falling back
// to the legacy binary flag for replies that predate `blocked`.
const blockedOf = (r: Record<string, unknown>): Blocked | undefined => {
  const b = r.blocked;
  if (b === 'binary' || b === 'too_large' || b === 'encoding') return b;
  return r.binary ? 'binary' : undefined;
};

// Line endings. CodeMirror splits on \r\n on load and joins with \n, so
// the buffer is always LF and a CRLF file used to be rewritten as LF on
// its first save — and never went clean, since the raw baseline still
// held the \r bytes. The eol is remembered per tab and re-applied on the
// way out; baselines are kept in buffer form so the dirty compare and the
// changed-on-disk compare see the same bytes the editor does.
type Eol = 'lf' | 'crlf';
const detectEol = (raw: string): Eol => (raw.includes('\r\n') ? 'crlf' : 'lf');
const toBuffer = (raw: string): string => raw.replace(/\r\n/g, '\n');
const toDisk = (text: string, eol: Eol | undefined): string =>
  (eol === 'crlf' ? text.replace(/\r?\n/g, '\r\n') : text);

// readOnlyText is the status-bar / placeholder wording for a blocked tab.
const readOnlyText = (t: Tab): string => {
  switch (t.blocked) {
    case 'binary': return 'binary file — not editable here';
    case 'too_large': return `${formatSize(t.size ?? 0)} is over the editor's ${formatSize(MAX_EDIT_BYTES)} cap — not editable here`;
    case 'encoding': return 'not valid UTF-8 — not editable here (saving would corrupt it)';
    default: return '';
  }
};

// Editor font size, in px: the token default, and the range the zoom
// keys stay inside (below ~8 the gutter stops being legible, above ~40
// a line of code no longer fits).
const DEFAULT_FONT_PX = 13;
const FONT_MIN_PX = 8;
const FONT_MAX_PX = 40;
const clampFont = (px: number): number =>
  Math.max(FONT_MIN_PX, Math.min(FONT_MAX_PX, Math.round(Number.isFinite(px) ? px : DEFAULT_FONT_PX)));

// Mirrors the BE's maxReadBytes; only used for wording.
const MAX_EDIT_BYTES = 4 * 1024 * 1024;
const formatSize = (n: number): string => {
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MiB`;
  if (n >= 1024) return `${Math.round(n / 1024)} KiB`;
  return `${n} B`;
};

const App: Component<{ instance: string; host: HTMLElement; origin: string }> = (props) => {
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
    | { mode: 'directory' }
    | { mode: 'save'; tabID: string; suggestedName: string; start?: string }
  >(null);
  // reloadPrompt drives the "changed on disk" modal. Non-null while a
  // tab with unsaved edits has had its file modified externally; the
  // user chooses Reload (discard edits, load disk content) or Keep.
  // Clean tabs reload silently and never reach this signal.
  // statusError is the status bar's one-line error surface: a failed
  // save, a file that would not open, a read-only tab the user tried to
  // save. Cleared by the next successful save or a tab switch.
  const [statusError, setStatusError] = createSignal<string | null>(null);
  // pendingClose is the Save / Don't save / Cancel prompt for a dirty
  // tab (Ctrl+W, the ×) or for the whole window (the titlebar close,
  // relayed by the BE as close_blocked).
  // 'tabs' is Close All / Close Others: one dialog listing every dirty
  // tab among `ids`, one answer for the lot.
  const [pendingClose, setPendingClose] = createSignal<
    { scope: 'window' } | { scope: 'tab'; tabID: string } | { scope: 'tabs'; ids: string[]; title: string } | null
  >(null);
  // revertPrompt asks before Revert throws away unsaved edits; a clean
  // tab reverts without asking.
  const [revertPrompt, setRevertPrompt] = createSignal<{ tabID: string; displayName: string } | null>(null);
  const [reloadPrompt, setReloadPrompt] = createSignal<
    | null
    | { tabID: string; displayName: string; diskContent: string }
  >(null);

  // Terminal pane state. termTabs is ordered; activeTermID points
  // at one of them (or '' when no terminals). termOpen toggles the
  // panel visibility (Ctrl+` / menu). editPct is the editor row's
  // vertical share when the terminal panel is visible.
  const [termTabs, setTermTabs] = createSignal<TermTab[]>([]);
  const [activeTermID, setActiveTermID] = createSignal('');
  // Per-agent-tab transcript state, keyed by agentd session key. Kept
  // outside TermTab so an arriving event does not replace the tab object
  // and remount the pane.
  const [agentEvents, setAgentEvents] = createSignal<Record<string, AgentEvent[]>>({});
  const [agentRoster, setAgentRoster] = createSignal<{ rows?: AgentRow[]; asks?: AgentAsk[]; adapters?: { id: string; name?: string }[] }>({});
  const [agentMenu, setAgentMenu] = createSignal<{ x: number; y: number } | null>(null);
  const [termOpen, setTermOpen] = createSignal(false);
  const [editPct, setEditPct] = createSignal(70);
  // Pending term.open requests waiting for term.opened so we can
  // pair channelID with the FE's local term id. Keyed by reply id.
  const pendingTermOpens = new Map<string, string>(); // reply id -> local id
  // Imperative <Terminal> handles keyed by channel id; populated by
  // each <Terminal>'s onReady, dropped on tab close.
  const termAPIs = new Map<number, TerminalAPI>();
  let nextTermLocalID = 0;

  // wysHandles is the TipTap editor per markdown-wysiwyg tab. Keyed
  // by tab id; created lazily in the per-tab mount ref callback the
  // first time the tab becomes visible; destroyed in closeTab and
  // on app cleanup. We keep them outside Solid's reactive system
  // because TipTap is imperative and the handle holds a live DOM
  // mount the framework should not re-render.
  const wysHandles = new Map<string, WysiwygHandle>();

  // wysFindOpen shows the find/replace bar over the WYSIWYG editor.
  // CM tabs keep using CodeMirror's own search panel; this bar only
  // exists because TipTap has no built-in equivalent. The bar itself
  // (WysFindBar) owns query/replace text and per-tab re-binding.
  const [wysFindOpen, setWysFindOpen] = createSignal(false);

  // openMenu is the open dropdown's id ('' = none). It's set when
  // the user clicks a menubar button; menubarOffsets stores each
  // button's x,y so the Menu component knows where to drop.
  const [openMenu, setOpenMenu] = createSignal<'' | MenuID | 'recent' | 'indent' | 'eol'>('');
  const [menuAnchor, setMenuAnchor] = createSignal<{ x: number; y: number }>({ x: 0, y: 0 });
  // Per-active-tab language override. Null = derive from path.
  const [langOverride, setLangOverride] = createSignal<string | null>(null);
  // Word-wrap toggle. Recompiled into the langCompartment so we
  // don't need a second compartment for it.
  const [wordWrap, setWordWrap] = createSignal(false);
  // Cursor position for the status bar, 1-based, kept by an update
  // listener (and resynced on tab switch, which setState does not
  // report as a selection change).
  const [cursorPos, setCursorPos] = createSignal<{ line: number; col: number }>({ line: 1, col: 1 });

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
  // textCtxMenu drives the right-click menu over the editor TEXT
  // area (CM + wysiwyg layers) — Cut/Copy/Paste against the wash
  // clipboard. Distinct from ctxMenu, which owns the sidebar rows.
  const [textCtxMenu, setTextCtxMenu] = createSignal<{ x: number; y: number } | null>(null);

  // Desktop-wide preferences, loaded once at boot from the edit BE and
  // patched through setPref. recent() is the shared recent-files list.
  const [prefs, setPrefs] = createSignal<Prefs>({});
  const recent = (): string[] => prefs().recent ?? [];

  // Quick open (Ctrl+P): the palette over the tree root. qoFiles is the
  // BE's recursive listing, relative to root, null while it is loading.
  const [qoOpen, setQoOpen] = createSignal(false);
  const [qoQuery, setQoQuery] = createSignal('');
  const [qoFiles, setQoFiles] = createSignal<string[] | null>(null);
  const [qoTruncated, setQoTruncated] = createSignal(false);
  const [qoSelected, setQoSelected] = createSignal(0);
  // The in-flight find's id; cleared when its reply lands or the
  // palette closes (which cancels it BE-side).
  let qoFindID = '';
  let qoSeq = 0;

  // untitledCounter — monotonically increasing index for naming
  // fresh Untitled-N buffers. Resets only on app remount.
  let untitledCounter = 0;

  const activeTab = (): Tab | undefined => {
    const id = activeID();
    return tabs().find((t) => t.id === id);
  };

  // wysiwygTabIDs is the stable id list for the per-tab TipTap
  // mount divs. Memoized with a length+content equality check so a
  // setTabs that only changes a tab's content (baseline, wysCache,
  // state) doesn't bust the For — which would destroy the mounted
  // contentEditable and orphan the editor instance. The list only
  // changes when a wysiwyg tab is added/removed/reordered or its
  // mode flips.
  const wysiwygTabIDs = createMemo(
    () => tabs().filter((t) => t.mode === 'wysiwyg').map((t) => t.id),
    [],
    { equals: (a, b) => a.length === b.length && a.every((id, i) => id === b[i]) },
  );

  // ---- BE I/O ----

  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);
  // Sessions with a transcript replay in flight (agent.resync); the
  // snapshot clears it.
  const agentResyncPending = new Set<string>();

  // Request/reply correlation + timeout live in @wash/fs-client's bus.ts
  // (unit-tested). idPrefix 'e' mints e-<n> ids (fm uses 'f'); handleBE
  // consults bus.tryResolve for echoed ids. sendWithReply is an alias so
  // the call sites below read unchanged.
  const bus = createBus(send, undefined, 'e');
  const sendWithReply = bus.request;

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
        fsWatch.watch(abs);
      }
    }
  };

  // ---- preferences ----

  const loadPrefs = async () => {
    const reply = await sendWithReply({ kind: 'prefs' });
    if (reply.kind === 'prefs_ok') setPrefs((reply.prefs ?? {}) as Prefs);
  };
  // setPref applies a patch locally at once (the UI must not wait on a
  // disk write) and then adopts whatever the BE merged, so a change made
  // from another window since boot is picked up too.
  const setPref = async (patch: Partial<Prefs>) => {
    setPrefs({ ...prefs(), ...patch });
    const reply = await sendWithReply({ kind: 'prefs_set', patch });
    if (reply.kind === 'prefs_set_ok') setPrefs((reply.prefs ?? {}) as Prefs);
    else setStatusError(`preferences not saved: ${String(reply.msg ?? reply.kind)}`);
  };
  // noteRecent moves `path` to the front of the shared recent list. The
  // BE keeps the list (its own verb, never a prefs patch, so two windows
  // cannot overwrite each other's additions).
  const noteRecent = (path: string) => {
    if (!path) return;
    setPrefs({ ...prefs(), recent: pushRecent(recent(), path) });
    void sendWithReply({ kind: 'recent_add', path }).then((reply) => {
      if (reply.kind === 'recent_add_ok') setPrefs({ ...prefs(), recent: (reply.recent as string[]) ?? [] });
    });
  };
  const forgetRecent = (path: string) => {
    setPrefs({ ...prefs(), recent: dropRecent(recent(), path) });
    void sendWithReply({ kind: 'recent_drop', path });
  };

  // ---- indentation ----
  //
  // Indentation is per tab, detected from the file itself on open
  // (indent.ts) so editing a Go file inserts tabs and a JSON file two
  // spaces without anyone configuring anything. The prefs default is
  // only the fallback for a file with nothing to detect from — a new
  // buffer, or one with no indented line.

  const indentCompartment = new Compartment();
  const prefsIndent = (): Indent => ({
    unit: prefs().indent_unit ?? DEFAULT_INDENT.unit,
    width: prefs().indent_width ?? DEFAULT_INDENT.width,
  });
  const activeIndent = (): Indent => activeTab()?.indent ?? prefsIndent();
  const indentExtensions = () => {
    const ind = activeIndent();
    return [indentUnit.of(indentString(ind)), EditorState.tabSize.of(ind.width)];
  };
  // detectedIndent is what a freshly-read buffer gets.
  const detectedIndent = (text: string): Indent => detectIndent(text) ?? prefsIndent();
  const setTabIndent = (ind: Indent) => {
    const t = activeTab();
    if (!t) return;
    setTabs(tabs().map((x) => (x.id === t.id ? { ...x, indent: ind } : x)));
  };
  // setEol changes what the next save writes. The buffer is always LF,
  // so nothing on screen changes — which is exactly why the tab is
  // marked dirty: the difference is real but invisible until toDisk
  // runs, and an unsaved EOL switch that looked clean would be lost.
  const setEol = (e: Eol) => {
    const t = activeTab();
    if (!t || t.blocked || t.eol === e) return;
    setTabs(tabs().map((x) => (x.id === t.id ? { ...x, eol: e } : x)));
    markTabDirty(t.id, true);
    persist();
  };
  // ---- font zoom ----
  //
  // One size for every editor window on the desktop (it lives in prefs,
  // not in a window's state) — the alternative is discovering that the
  // window you just opened is the one that did not get the change.

  const fontSize = (): number => clampFont(prefs().font_size ?? DEFAULT_FONT_PX);
  let fontSaveTimer: ReturnType<typeof setTimeout> | undefined;
  const setFontSize = (px: number) => {
    const next = clampFont(px);
    if (next === fontSize()) return;
    // Applied locally at once; the file write is debounced because a
    // wheel gesture arrives as a stream of notches and each one would
    // otherwise be a read-merge-write.
    setPrefs({ ...prefs(), font_size: next });
    clearTimeout(fontSaveTimer);
    fontSaveTimer = setTimeout(() => { void setPref({ font_size: next }); }, 400);
  };
  const zoomFont = (delta: number) => setFontSize(fontSize() + delta);
  onCleanup(() => clearTimeout(fontSaveTimer));

  // syncCursor reads the status bar's Ln/Col out of the live view.
  // Called on tab switch: view.setState() does not report a selection
  // change, so the update listener alone would show the old position.
  const syncCursor = () => {
    if (!editorView) {
      setCursorPos({ line: 1, col: 1 });
      return;
    }
    const head = editorView.state.selection.main.head;
    const line = editorView.state.doc.lineAt(head);
    setCursorPos({ line: line.number, col: head - line.from + 1 });
  };

  // ---- quick open ----

  const openQuickOpen = () => {
    setQoQuery('');
    setQoSelected(0);
    setQoOpen(true);
    // A fresh listing per open: the tree changes under a running
    // editor, and 5k entries is a cheap walk next to a stale answer.
    if (root()) {
      qoSeq += 1;
      qoFindID = `qo-${qoSeq}`;
      setQoFiles(null);
      setQoTruncated(false);
      send({ kind: 'find', id: qoFindID, path: root(), limit: 5000 });
    }
  };
  const closeQuickOpen = () => {
    if (!qoOpen()) return;
    setQoOpen(false);
    if (qoFindID) {
      send({ kind: 'find_cancel', id: qoFindID });
      qoFindID = '';
    }
    editorView?.focus();
  };
  // qoResults is what the palette lists: the recent files while the
  // query is empty, else the fuzzy ranking over the tree listing plus
  // any recent file outside it. Rows carry the label shown (relative to
  // root when under it) and the absolute path to open.
  const qoRel = (p: string): string => {
    const r = root();
    return r && p.startsWith(r + '/') ? p.slice(r.length + 1) : p;
  };
  const qoResults = createMemo<{ abs: string; label: string }[]>(() => {
    const q = qoQuery().trim();
    const r = root();
    const recentRows = recent().map((p) => ({ abs: p, label: qoRel(p) }));
    if (!q) return recentRows.slice(0, 50);
    const files = qoFiles() ?? [];
    const seen = new Set(files);
    const cands = [...files, ...recentRows.map((x) => x.label).filter((l) => !seen.has(l))];
    return rankFiles(q, cands, 50).map((l) => ({ abs: l.startsWith('/') ? l : joinPath(r, l), label: l }));
  });
  const qoPick = (row: { abs: string } | undefined) => {
    if (!row) return;
    closeQuickOpen();
    void openInTab(row.abs);
  };
  const onQuickOpenKey = (ev: KeyboardEvent) => {
    // The palette owns the keyboard while it is up; nothing here may
    // reach the tab shortcuts behind it.
    ev.stopPropagation();
    const n = qoResults().length;
    if (ev.key === 'Escape') { ev.preventDefault(); closeQuickOpen(); return; }
    if (ev.key === 'ArrowDown') { ev.preventDefault(); if (n) setQoSelected((qoSelected() + 1) % n); return; }
    if (ev.key === 'ArrowUp') { ev.preventDefault(); if (n) setQoSelected((qoSelected() - 1 + n) % n); return; }
    if (ev.key === 'Enter') { ev.preventDefault(); qoPick(qoResults()[qoSelected()]); return; }
  };

  // openInTab focuses an existing tab for `path`, or reads the
  // file and creates a fresh tab if there isn't one. Same tab
  // can't appear twice — opening twice converges on a single tab.
  const openInTab = async (path: string) => {
    captureActiveState();
    const existing = tabs().find((t) => t.path === path);
    if (existing) {
      setActiveID(existing.id);
      noteRecent(path);
      return;
    }
    const reply = await sendWithReply({ kind: 'read', path });
    if (reply.kind !== 'read_ok') {
      // Permission denied, vanished, a directory: say so where the
      // user is looking instead of silently doing nothing.
      setStatusError(`cannot open ${baseName(path) || path}: ${String(reply.msg ?? reply.kind)}`);
      if ((reply as { code?: string }).code === 'not_found') forgetRecent(path);
      return;
    }
    noteRecent(path);
    const blocked = blockedOf(reply);
    const raw = blocked ? '' : String(reply.content ?? '');
    const tab: Tab = {
      id: path,
      path,
      displayName: baseName(path) || path,
      baseline: toBuffer(raw),
      state: null,
      binary: blocked === 'binary',
      blocked,
      size: typeof reply.size === 'number' ? reply.size : undefined,
      eol: blocked ? undefined : detectEol(raw),
      indent: blocked ? undefined : detectedIndent(toBuffer(raw)),
      readOnlyFile: reply.writable === false,
      mode: !blocked && isMarkdownPath(path) ? 'wysiwyg' : 'source',
    };
    setTabs([...tabs(), tab]);
    setActiveID(tab.id);
    // Watch the file's directory so an external edit (another editor,
    // a build step, git) surfaces as a reload prompt. fileWatch dedups
    // per dir and is independent of the sidebar's fsWatch, whose subs
    // get torn down on tree-collapse.
    fileWatch.watch(parentPath(path));
  };

  // openDiffTab reads `otherPath` from disk and creates a tab that
  // diffs it against the active tab's current content (the "new"
  // side). The tab's doc is the new content; unifiedMergeView
  // overlays inline diff markers showing changes vs `otherPath`.
  // No-op if there is no active file tab to diff against.
  const openDiffTab = async (otherPath: string) => {
    const cur = activeTab();
    if (!cur || !editorView) return;
    // Use the live content for the "new" side so unsaved edits show up
    // in the diff (tabContent reads the wysiwyg handle for markdown
    // tabs, where the CM view holds stale text). baseline is what's on
    // disk.
    const newContent = tabContent(cur);
    const reply = await sendWithReply({ kind: 'read', path: otherPath });
    if (reply.kind !== 'read_ok' || blockedOf(reply)) return;
    const otherContent = toBuffer(String(reply.content ?? ''));
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
      // Diff tabs always use CodeMirror — unifiedMergeView is a CM
      // extension and we'd lose the per-chunk Accept/Reject UX in
      // wysiwyg mode.
      mode: 'source',
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
      // Untitled buffers default to source — no extension to hint
      // at markdown, and the user can toggle via the View menu after
      // typing if they want wysiwyg.
      mode: 'source',
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
    // Release the parent-dir watch if no surviving tab still lives in
    // that directory. Refcounted BE-side, so this is safe even when
    // the sidebar also watches the same dir.
    const closedPath = cur[idx].path;
    if (closedPath) {
      const dir = parentPath(closedPath);
      if (!next.some((t) => t.path && parentPath(t.path) === dir)) {
        fileWatch.unwatch(dir);
      }
    }
    // If this tab had a pending reload prompt, drop it.
    if (reloadPrompt()?.tabID === id) setReloadPrompt(null);
    // Destroy the TipTap editor for this tab if one was created.
    // The ref callback won't fire again on the gone-from-DOM mount,
    // so we'd otherwise leak the editor + its event listeners.
    const h = wysHandles.get(id);
    if (h) {
      h.destroy();
      wysHandles.delete(id);
    }
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
  // tabContent reads the live content for a tab regardless of its
  // editor mode. Wysiwyg pulls markdown from the TipTap handle (or
  // from wysCache if the handle hasn't been mounted yet, e.g. for a
  // tab that's never been activated since reload); source pulls from
  // the live CM view if active, the captured EditorState otherwise.
  const tabContent = (t: Tab): string => {
    if (t.mode === 'wysiwyg') {
      const h = wysHandles.get(t.id);
      if (h) return h.getMarkdown();
      return t.wysCache ?? t.baseline;
    }
    if (t.id === activeID() && editorView) return editorView.state.doc.toString();
    if (t.state) return t.state.doc.toString();
    return t.baseline;
  };

  // applySaveCleanups runs the on-save preferences (trim trailing
  // whitespace, ensure a final newline — both off by default) and
  // returns what should be written. It rewrites the BUFFER as well as
  // the bytes: a cleanup that only touched the file would leave the tab
  // dirty the instant it was saved. The rewrite goes through a normal
  // transaction so it is undoable, on the live view for the active tab
  // and on the captured state for the others (Save All).
  const applySaveCleanups = (t: Tab): string => {
    const content = tabContent(t);
    if (t.mode === 'wysiwyg' || t.blocked) return content;
    const clean = normalizeForSave(content, {
      trimTrailing: !!prefs().trim_trailing,
      finalNewline: !!prefs().final_newline,
    });
    if (clean === content) return content;
    const changes = { from: 0, to: content.length, insert: clean };
    if (t.id === activeID() && editorView) {
      editorView.dispatch({ changes });
    } else if (t.state) {
      const next = t.state.update({ changes }).state;
      setTabs(tabs().map((x) => (x.id === t.id ? { ...x, state: next } : x)));
    }
    return clean;
  };

  // saveTab writes one tab. 'needs_path' means the picker was opened
  // for an Untitled buffer and the write happens in pickerConfirm;
  // 'failed' means the status bar already says why. Callers that go on
  // to close something must stop on anything but 'ok'.
  const saveTab = async (t: Tab): Promise<'ok' | 'failed' | 'needs_path'> => {
    if (t.blocked) {
      setStatusError(`${t.displayName}: ${readOnlyText(t)}`);
      return 'failed';
    }
    if (!t.path) {
      setPicker({ mode: 'save', tabID: t.id, suggestedName: t.displayName });
      return 'needs_path';
    }
    if (t.missing) {
      // The path went away under us. Recreating it silently is how a
      // stale tab resurrects a file someone just renamed or removed;
      // the picker, seeded with the old name in the old folder, makes
      // that an explicit choice (confirming the same path recreates it).
      setPicker({ mode: 'save', tabID: t.id, suggestedName: baseName(t.path), start: parentPath(t.path) });
      return 'needs_path';
    }
    if (t.readOnlyFile) {
      // The mode bits say this write would fail. Offering Save As is
      // the only useful answer, and it is better made before the edit
      // is thrown at the BE and bounced.
      setStatusError(`${t.displayName} is a read-only file — Save As?`);
      setPicker({ mode: 'save', tabID: t.id, suggestedName: baseName(t.path), start: parentPath(t.path) });
      return 'needs_path';
    }
    const content = applySaveCleanups(t);
    const reply = await sendWithReply({ kind: 'write', path: t.path, content: toDisk(content, t.eol) });
    if (reply.kind !== 'write_ok') {
      // The BE also toasts; this is for the eyes already on the editor.
      setStatusError(`save failed: ${String(reply.msg ?? reply.kind)}`);
      return 'failed';
    }
    // Refresh baseline + clear dirty marker. The tab's path may
    // have changed if write canonicalized it (filepath.Clean).
    setTabs(tabs().map((x) => x.id === t.id ? { ...x, baseline: content, path: String(reply.path ?? x.path), wysCache: t.mode === 'wysiwyg' ? content : x.wysCache } : x));
    setDirtyIDs((s) => {
      if (!s.has(t.id)) return s;
      const out = new Set(s);
      out.delete(t.id);
      return out;
    });
    wysHandles.get(t.id)?.markClean();
    setStatusError(null);
    return 'ok';
  };

  const saveActive = async () => {
    const t = activeTab();
    if (t) await saveTab(t);
  };

  // saveAll writes every dirty tab in strip order. An Untitled buffer
  // parks the loop on the picker (saveTab's 'needs_path'); the remaining
  // ids wait in saveAllQueue and pickerConfirm resumes the loop once that
  // buffer has a path, so several untitled buffers are asked for one at
  // a time. Cancelling the picker abandons the rest — the user said no.
  let saveAllQueue: string[] = [];
  const saveAll = async (ids?: string[]) => {
    const order = ids ?? dirtyTabs().map((t) => t.id);
    saveAllQueue = [];
    for (let i = 0; i < order.length; i++) {
      const t = tabs().find((x) => x.id === order[i]);
      if (!t || !dirtyIDs().has(t.id)) continue;
      const r = await saveTab(t);
      if (r === 'needs_path') {
        saveAllQueue = order.slice(i + 1);
        return;
      }
      // A failed write already sits in the status bar; the rest still
      // get their chance rather than being held hostage by one file.
    }
  };
  const resumeSaveAll = () => {
    if (saveAllQueue.length === 0) return;
    const rest = saveAllQueue;
    saveAllQueue = [];
    void saveAll(rest);
  };

  // requestCloseTab is what every close gesture goes through: a clean
  // tab closes at once, a dirty one asks first.
  const requestCloseTab = (id: string) => {
    if (dirtyIDs().has(id)) setPendingClose({ scope: 'tab', tabID: id });
    else closeTab(id);
  };

  // requestCloseTabs is Close All / Close Others: the clean ones go at
  // once when nothing is dirty; otherwise ONE dialog names every dirty
  // tab in the set and one answer settles all of them.
  const requestCloseTabs = (ids: string[], title: string) => {
    if (ids.length === 0) return;
    if (ids.some((id) => dirtyIDs().has(id))) setPendingClose({ scope: 'tabs', ids, title });
    else closeTabs(ids);
  };
  const closeTabs = (ids: string[]) => {
    captureActiveState();
    for (const id of ids) closeTab(id);
  };
  const closeAllTabs = () => requestCloseTabs(tabs().map((t) => t.id), 'Close all tabs?');
  const closeOtherTabs = () => requestCloseTabs(tabs().filter((t) => t.id !== activeID()).map((t) => t.id), 'Close other tabs?');

  // activateTab is the one switch path the keyboard and the strip share:
  // snapshot the outgoing buffer, then move.
  const activateTab = (id: string) => {
    if (!id || id === activeID()) return;
    captureActiveState();
    setActiveID(id);
  };
  // cycleTabs moves `delta` tabs along the strip, wrapping at either end.
  const cycleTabs = (delta: number) => {
    const list = tabs();
    if (list.length < 2) return;
    const idx = list.findIndex((t) => t.id === activeID());
    const next = ((idx < 0 ? 0 : idx) + delta + list.length) % list.length;
    activateTab(list[next].id);
  };
  // jumpToTab picks the n-th tab (1-based); past the end does nothing.
  const jumpToTab = (n: number) => {
    const t = tabs()[n - 1];
    if (t) activateTab(t.id);
  };
  // moveTab re-slots the dragged tab at the target's position: dragging
  // leftwards lands before the target, rightwards after it — so a tab
  // dropped on its neighbour swaps with it either way.
  const moveTab = (dragID: string, targetID: string) => {
    if (dragID === targetID) return;
    const list = tabs();
    const from = list.findIndex((t) => t.id === dragID);
    const to = list.findIndex((t) => t.id === targetID);
    if (from < 0 || to < 0) return;
    const without = list.filter((t) => t.id !== dragID);
    const at = without.findIndex((t) => t.id === targetID) + (from < to ? 1 : 0);
    setTabs([...without.slice(0, at), list[from], ...without.slice(at)]);
  };
  // dragTabID is the tab being dragged along the strip; a strip drag has
  // its own MIME so the sidebar's move-file drops and the pane's
  // drop-to-open ignore it.
  const [dragTabID, setDragTabID] = createSignal<string | null>(null);

  // revertActive reloads the active tab from disk, throwing the buffer
  // away. Asks first when there is something to lose.
  const revertActive = () => {
    const t = activeTab();
    if (!t || !t.path || t.blocked || t.diff) return;
    if (dirtyIDs().has(t.id)) setRevertPrompt({ tabID: t.id, displayName: t.displayName });
    else void doRevert(t.id);
  };
  const doRevert = async (tabID: string) => {
    const t = tabs().find((x) => x.id === tabID);
    if (!t || !t.path) return;
    const reply = await sendWithReply({ kind: 'read', path: t.path });
    if (reply.kind !== 'read_ok' || blockedOf(reply)) {
      setStatusError(`cannot revert ${t.displayName}: ${String(reply.msg ?? reply.kind)}`);
      return;
    }
    const raw = String(reply.content ?? '');
    const eol = detectEol(raw);
    setTabs(tabs().map((x) => x.id === tabID ? { ...x, eol, missing: false } : x));
    applyReload(tabID, toBuffer(raw));
    setStatusError(null);
  };
  const confirmRevert = () => {
    const p = revertPrompt();
    setRevertPrompt(null);
    if (p) void doRevert(p.tabID);
  };

  const dirtyTabs = () => tabs().filter((t) => dirtyIDs().has(t.id));
  // The tabs a close prompt is about — what its list shows and what
  // Save writes.
  const pendingCloseTargets = (): Tab[] => {
    const p = pendingClose();
    if (!p) return [];
    if (p.scope === 'window') return dirtyTabs();
    if (p.scope === 'tab') return tabs().filter((t) => t.id === p.tabID);
    return tabs().filter((t) => p.ids.includes(t.id) && dirtyIDs().has(t.id));
  };

  // The close prompt's three answers. Window scope ends in
  // close_window_confirmed, which the BE turns into the router's
  // confirm_close; tab scope ends in closeTab; tabs scope in closeTabs.
  const discardAndClose = () => {
    const p = pendingClose();
    setPendingClose(null);
    if (!p) return;
    if (p.scope === 'window') send({ kind: 'close_window_confirmed' });
    else if (p.scope === 'tab') closeTab(p.tabID);
    else closeTabs(p.ids);
  };
  const saveAndClose = async () => {
    const p = pendingClose();
    const targets = pendingCloseTargets();
    setPendingClose(null);
    if (!p) return;
    for (const t of targets) {
      const r = await saveTab(t);
      if (r === 'needs_path') {
        // The picker is up for this buffer; the close is abandoned
        // rather than queued behind a dialog that may be cancelled.
        setStatusError(`${t.displayName} needs a path — save it, then close again`);
        return;
      }
      if (r !== 'ok') return;
    }
    if (p.scope === 'window') send({ kind: 'close_window_confirmed' });
    else if (p.scope === 'tab') closeTab(p.tabID);
    else closeTabs(p.ids);
  };

  // saveAsActive forces the picker open for the active tab, no
  // matter whether it already has a path. Bound to Ctrl+Shift+S.
  const saveAsActive = () => {
    const t = activeTab();
    if (!t) return;
    if (t.blocked) {
      setStatusError(`${t.displayName}: ${readOnlyText(t)}`);
      return;
    }
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
  // setTreeRoot re-roots the left sidebar tree at path: reset the
  // listing cache + expansion, load the new root, and move the fs
  // watch. Shared by the BE-driven cmd.set_root and the File →
  // Open Folder… action so both re-root identically.
  const setTreeRoot = (path: string) => {
    if (!path) return;
    setRoot(path);
    setListings({});
    setExpanded({});
    void loadDir(path);
    fsWatch.watch(path);
  };

  const pickerConfirm = async (chosen: string) => {
    const cur = picker();
    setPicker(null);
    if (!cur) return;
    if (cur.mode === 'open') {
      void openInTab(chosen);
      return;
    }
    if (cur.mode === 'directory') {
      // Open Folder…: point the sidebar tree at the chosen directory.
      setTreeRoot(chosen);
      return;
    }
    const src = tabs().find((t) => t.id === cur.tabID);
    if (!src) return;
    const content = tabContent(src);
    const reply = await sendWithReply({ kind: 'write', path: chosen, content: toDisk(content, src.eol) });
    if (reply.kind !== 'write_ok') {
      setStatusError(`save failed: ${String(reply.msg ?? reply.kind)}`);
      saveAllQueue = [];
      return;
    }
    setStatusError(null);
    const newPath = String(reply.path ?? chosen);
    // Save As is the one save that changes a tab's id, which makes it
    // the one save that legitimately re-seeds the editor (the
    // active-tab effect keys on id). So carry the LIVE state across the
    // rename: the captured t.state is a snapshot from the last tab
    // switch, and re-seeding from it would drop everything typed since
    // — the same revert the effect used to cause on every save.
    const isActive = src.id === activeID();
    const liveState = isActive && editorView && src.mode !== 'wysiwyg' ? editorView.state : src.state;
    // Update the tab: new id (the path), new display name, fresh
    // baseline. If another tab already pointed at newPath, drop
    // it — converging on a single tab per path matches openInTab.
    const dupeIdx = tabs().findIndex((t) => t.path === newPath && t.id !== src.id);
    // Captured before the list is rewritten: after setTabs the dropped
    // tab is gone and its id is unrecoverable.
    const dropped = dupeIdx >= 0 ? tabs()[dupeIdx].id : undefined;
    const updated = tabs()
      .filter((_, i) => i !== dupeIdx)
      .map((x) => x.id === src.id
        ? {
          ...x,
          id: newPath,
          path: newPath,
          displayName: baseName(newPath) || newPath,
          baseline: content,
          state: liveState,
          // The write to the new path just succeeded, so whatever the
          // OLD path's mode said no longer applies.
          readOnlyFile: false,
          // The remounted TipTap seeds from wysCache; leaving the
          // pre-save cache would show older text than we just wrote.
          wysCache: x.mode === 'wysiwyg' ? content : x.wysCache,
        }
        : x);
    // The wysiwyg handle map is keyed by tab id, so the rename orphans
    // the old entry: its editor would leak and its onChange would keep
    // writing to a tab id that no longer exists. Retire it and let the
    // remount build a fresh one from wysCache above.
    const oldHandle = wysHandles.get(src.id);
    if (oldHandle && src.id !== newPath) {
      oldHandle.destroy();
      wysHandles.delete(src.id);
    }
    setTabs(updated);
    setActiveID(newPath);
    // Watch the destination dir so the freshly-saved tab tracks
    // external edits just like an opened file.
    fileWatch.watch(parentPath(newPath));
    noteRecent(newPath);
    // Both ids go: the source tab is now clean under its new id, and a
    // duplicate that was dropped above must not leave its marker behind
    // for a tab that no longer exists.
    if (dropped) {
      wysHandles.get(dropped)?.destroy();
      wysHandles.delete(dropped);
    }
    setDirtyIDs((s) => {
      if (!s.has(src.id) && !(dropped && s.has(dropped))) return s;
      const out = new Set(s);
      out.delete(src.id);
      if (dropped) out.delete(dropped);
      return out;
    });
    // A Save All parked on this buffer carries on with the next one.
    resumeSaveAll();
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
        if (t.mode === 'wysiwyg') pt.mode = 'wysiwyg';
        if (t.path) {
          pt.path = t.path;
          // For wysiwyg tabs we persist the in-flight markdown so a
          // reload before save doesn't lose unsaved edits.
          if (t.mode === 'wysiwyg') {
            pt.content = tabContent(t);
          } else if (dirtyIDs().has(t.id)) {
            // Unsaved edits to a SAVED source file: persist the live
            // buffer so a reconnect/remount restores the in-progress
            // text instead of silently reverting to the on-disk version
            // (the reconnect data-loss this guards against). Clean
            // source tabs skip this and re-read disk on restore — smaller
            // blob, identical result. Dirty-buffer content rides the
            // coarser cadence in scheduleContentPersist(), not the
            // per-event 250ms debounce.
            pt.content = liveState ? liveState.doc.toString() : t.baseline;
          }
        } else {
          pt.display_name = t.displayName;
          if (t.mode === 'wysiwyg') pt.content = tabContent(t);
          else pt.content = liveState ? liveState.doc.toString() : t.baseline;
        }
        if (liveState && t.mode === 'source') {
          const sel = liveState.selection.main;
          if (sel.anchor !== 0 || sel.head !== 0) {
            pt.selection = { anchor: sel.anchor, head: sel.head };
          }
        }
        if (scrollTop && scrollTop > 0 && t.mode === 'source') pt.scroll = scrollTop;
        // The live signals are the truth for the ACTIVE tab: toggleWrap
        // / setLang write through to the tab too, but a reconfigure that
        // has not been flushed yet would otherwise be missed.
        const wrap = isActive ? wordWrap() : !!t.wrap;
        const lang = isActive ? langOverride() : (t.lang ?? null);
        if (wrap) pt.wrap = true;
        if (lang) pt.lang = lang;
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

  // Dirty source-buffer content can be large, so we don't ship it on
  // every keystroke. Instead a doc change schedules a persist on a
  // trailing "quiet" timer (fires once typing pauses) plus a hard
  // max-interval cap so a long uninterrupted typing run still
  // checkpoints. Worst-case unsaved-edit loss on a reconnect is one
  // CONTENT_MAX_MS window. persist() itself includes the live buffer
  // for any dirty tab, so we just need to fire it on this cadence.
  const CONTENT_QUIET_MS = 1500;
  const CONTENT_MAX_MS = 5000;
  let contentQuietTimer: number | null = null;
  let contentMaxTimer: number | null = null;
  const flushContentPersist = () => {
    if (contentQuietTimer != null) { window.clearTimeout(contentQuietTimer); contentQuietTimer = null; }
    if (contentMaxTimer != null) { window.clearTimeout(contentMaxTimer); contentMaxTimer = null; }
    persist();
  };
  const scheduleContentPersist = () => {
    if (contentQuietTimer != null) window.clearTimeout(contentQuietTimer);
    contentQuietTimer = window.setTimeout(flushContentPersist, CONTENT_QUIET_MS);
    // Max-interval cap: started once and left running so continuous
    // typing (which keeps resetting the quiet timer) can't starve it.
    if (contentMaxTimer == null) {
      contentMaxTimer = window.setTimeout(flushContentPersist, CONTENT_MAX_MS);
    }
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
          if (tab) {
            const persistedMode: Tab['mode'] | undefined = pt.mode === 'wysiwyg' || pt.mode === 'source' ? pt.mode : undefined;
            // Restore unsaved edits to a saved source file: only dirty
            // source tabs carry live content, so when it's present seed
            // the buffer from it instead of the on-disk baseline. The
            // baseline stays the disk version (dirty detection compares
            // against it), and we re-mark the tab dirty below so the UI
            // and next save match the restored buffer.
            const restoreContent = persistedMode !== 'wysiwyg' && typeof pt.content === 'string';
            const doc = restoreContent ? pt.content! : tab.baseline;
            const fresh = (pt.selection || pt.scroll || restoreContent) ? EditorState.create({
              doc,
              extensions: baseExtensions(),
              selection: pt.selection
                ? EditorSelection.single(pt.selection.anchor, pt.selection.head)
                : undefined,
            }) : null;
            setTabs(tabs().map((x) => x.id === tab.id ? {
              ...x,
              ...(fresh ? { state: fresh } : {}),
              ...(pt.scroll ? { scrollTop: pt.scroll } : {}),
              ...(persistedMode ? { mode: persistedMode } : {}),
              ...(pt.wrap ? { wrap: true } : {}),
              ...(pt.lang ? { lang: pt.lang } : {}),
            } : x));
            if (restoreContent && pt.content !== tab.baseline) {
              setDirtyIDs((s) => {
                if (s.has(tab.id)) return s;
                const out = new Set(s);
                out.add(tab.id);
                return out;
              });
            }
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
            wrap: pt.wrap || undefined,
            lang: pt.lang || undefined,
            mode: pt.mode === 'wysiwyg' ? 'wysiwyg' : 'source',
            wysCache: pt.mode === 'wysiwyg' ? content : undefined,
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
    // WYSIWYG tabs get the TipTap find bar — CM's panel would open
    // against the hidden source view and search the wrong document.
    if (activeTab()?.mode === 'wysiwyg') {
      setWysFindOpen(true);
      return;
    }
    if (!editorView) return;
    editorView.focus();
    openSearchPanel(editorView);
  };
  // cmdReplace is Ctrl+H: the same panel as Find, with the caret in
  // the replace field. CM has no command for that — the panel is one
  // widget with both rows — so the field is focused once it is up.
  const cmdReplace = () => {
    if (activeTab()?.mode === 'wysiwyg') {
      setWysFindOpen(true);
      return;
    }
    if (!editorView) return;
    editorView.focus();
    openSearchPanel(editorView);
    queueMicrotask(() => {
      const el = editorView?.dom.querySelector('.cm-panel.cm-search input[name="replace"]') as HTMLInputElement | null;
      el?.focus();
      el?.select();
    });
  };
  // cmdGotoLine opens CM's line dialog. Only source tabs have line
  // numbers to go to; in WYSIWYG the command says so rather than
  // opening a dialog against the hidden source view.
  const cmdGotoLine = () => {
    if (!editorView || !activeTab()) return;
    if (activeTab()!.mode === 'wysiwyg') {
      setStatusError('go to line needs the source view (Ctrl+Shift+P)');
      return;
    }
    editorView.focus();
    gotoLine(editorView);
  };
  // closeWysFind tears the bar down, drops the highlights, and hands
  // focus back to the document — CM's closeSearchPanel contract.
  const closeWysFind = () => {
    setWysFindOpen(false);
    const h = wysHandles.get(activeID());
    h?.search.clear();
    h?.focus();
  };
  // Cut/Copy/Paste against the active editor model (CM or TipTap),
  // backed by the wash clipboard. Copy/cut mirror to the system
  // clipboard via washCopyText (the menu/context click is the user
  // gesture that allows it); paste reads the wash clipboard — the
  // system clipboard isn't programmatically readable on an insecure
  // origin, so external content enters via Ctrl+V (native paste)
  // which the shell's copy/paste listeners fold into the wash side.
  const selectedEditorText = (): string => {
    const t = activeTab();
    if (!t) return '';
    if (t.mode === 'wysiwyg') {
      const h = wysHandles.get(t.id);
      if (!h) return '';
      const st = h.editor.state;
      return st.doc.textBetween(st.selection.from, st.selection.to, '\n');
    }
    if (!editorView) return '';
    const sel = editorView.state.selection.main;
    return editorView.state.sliceDoc(sel.from, sel.to);
  };
  const cmdCopy = () => {
    const text = selectedEditorText();
    if (text) washCopyText(text);
  };
  const cmdCut = () => {
    const text = selectedEditorText();
    if (!text) return;
    washCopyText(text);
    const t = activeTab();
    if (t?.mode === 'wysiwyg') {
      wysHandles.get(t.id)?.editor.chain().focus().deleteSelection().run();
    } else if (editorView) {
      editorView.focus();
      editorView.dispatch(editorView.state.replaceSelection(''));
    }
  };
  const cmdPaste = () => {
    void washPasteText().then((text) => {
      if (!text) return;
      const t = activeTab();
      if (t?.mode === 'wysiwyg') {
        const h = wysHandles.get(t.id);
        if (!h) return;
        h.editor.commands.focus();
        // pasteText runs ProseMirror's normal paste pipeline, so
        // tiptap-markdown's transformPastedText applies — pasting
        // "# title" gives a heading, same as a native Ctrl+V.
        h.editor.view.pasteText(text);
      } else if (editorView) {
        editorView.focus();
        editorView.dispatch({ ...editorView.state.replaceSelection(text), scrollIntoView: true });
      }
    });
  };

  // toggleWysiwyg flips the active tab's editor mode. .md files that
  // started in wysiwyg can drop to CodeMirror source view; .md files
  // the user moved to source can flip back. Non-markdown tabs accept
  // the call but the toolbar + menu disable it (only the keyboard
  // shortcut would still hit this path; we ignore those silently).
  const toggleWysiwyg = () => {
    const t = activeTab();
    if (!t) return;
    if (!isMarkdownPath(t.path)) return;
    if (t.mode === 'wysiwyg') {
      // Snap the current MD out of the TipTap handle so a switch
      // back later can re-seed without a re-read from disk, and so
      // the CM view shows the user's current draft.
      const md = tabContent(t);
      const h = wysHandles.get(t.id);
      if (h) {
        h.destroy();
        wysHandles.delete(t.id);
      }
      // Seed CM with the live markdown — discard the captured
      // EditorState because it was for whatever doc CM had loaded
      // when the tab was last in source mode (typically the same
      // baseline content; this just rebuilds from the current MD).
      setTabs(tabs().map((x) => x.id === t.id ? { ...x, mode: 'source', wysCache: md, state: null, baseline: x.baseline } : x));
      if (editorView) {
        editorView.setState(EditorState.create({ doc: md, extensions: extensionsForTab({ ...t, mode: 'source' }) }));
        editorView.dispatch({ effects: langCompartment.reconfigure(langExtensions()) });
        editorView.focus();
      }
      // Dirty marker: if the live MD differs from baseline, keep
      // dirty; else clear. Mirrors the CM dirtyListener's contract.
      markTabDirty(t.id, md !== t.baseline);
    } else {
      // Source → wysiwyg. Use CM's live doc as the seed and stash
      // it in wysCache so the mount ref callback finds it. The
      // mount fires next tick when Solid renders the per-tab div.
      const md = tabContent(t);
      setTabs(tabs().map((x) => x.id === t.id ? { ...x, mode: 'wysiwyg', wysCache: md } : x));
    }
  };

  const setLang = (k: string | null) => {
    setLangOverride(k);
    const t = activeTab();
    if (t) setTabs(tabs().map((x) => (x.id === t.id ? { ...x, lang: k } : x)));
    persist();
    editorView?.focus();
  };
  const toggleWrap = () => {
    const next = !wordWrap();
    setWordWrap(next);
    const t = activeTab();
    if (t) setTabs(tabs().map((x) => (x.id === t.id ? { ...x, wrap: next } : x)));
    persist();
    editorView?.focus();
  };

  // ---- menu bar plumbing ----
  //
  // openMenuFor toggles a menu open against its trigger button.
  // The Menu component owns dismissal (click-outside via document
  // listener), so we just toggle openMenu signal and set the
  // anchor coordinates relative to the host element so the menu
  // hangs below the button regardless of where the window is.

  const openMenuFor = (id: MenuID, ev: MouseEvent) => {
    if (openMenu() === id) {
      setOpenMenu('');
      return;
    }
    // Menu paints via Portal with position:fixed, so coords are
    // viewport-space — no host-rect subtraction.
    const btnRect = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    setMenuAnchor({ x: btnRect.left, y: btnRect.bottom + 2 });
    setOpenMenu(id);
  };
  // openStatusMenu drops a menu off a status-bar cell. The status bar
  // is the last row of the window, so the anchor is the cell's TOP edge
  // and Menu's viewport clamp lifts the body above it.
  const openStatusMenu = (id: 'indent' | 'eol' | 'syntax', ev: MouseEvent) => {
    if (openMenu() === id) {
      setOpenMenu('');
      return;
    }
    const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    setMenuAnchor({ x: r.left, y: r.top });
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
    // The router asked to close the window and the BE vetoed on our
    // behalf (WIRE.md §10); answer at once when nothing is unsaved,
    // otherwise ask.
    if (m.kind === 'close_blocked') {
      if (dirtyTabs().length === 0) send({ kind: 'close_window_confirmed' });
      else setPendingClose({ scope: 'window' });
      return;
    }
    if (m.kind === 'cmd.set_root') {
      setTreeRoot(String(m.path ?? ''));
      return;
    }
    // The quick-open listing. Only the find the palette is waiting on
    // counts; a cancelled one's late reply is dropped here.
    if (m.kind === 'find_ok' || m.kind === 'find_err') {
      if (String(m.id ?? '') !== qoFindID) return;
      qoFindID = '';
      if (m.kind === 'find_ok') {
        setQoFiles((m.files as string[]) ?? []);
        setQoTruncated(!!m.truncated);
      } else {
        setQoFiles([]);
        setStatusError(`quick open: ${String(m.msg ?? m.kind)}`);
      }
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
      fsWatch.scheduleRefresh(parentPath(evPath));
      fsWatch.scheduleRefresh(evPath);
      // If the changed path is an open file, reconcile its buffer
      // against the new disk content (silent reload when clean, prompt
      // when there are unsaved edits).
      void maybeReloadFromDisk(evPath);
      return;
    }
    // ---- agent tabs (docs/AGENT_TABS.md) ----
    if (m.kind === 'agent.started') {
      const tab = String(m.tab ?? '');
      const key = String(m.key ?? '');
      setTermTabs(termTabs().map((t) => (t.id === tab ? { ...t, agentKey: key } : t)));
      return;
    }
    if (m.kind === 'agent.start_failed') {
      const tab = String(m.tab ?? '');
      // Name the failure in the tab rather than leaving it "starting…"
      // forever — a failed start carries no session key, which is why
      // the request id exists at all.
      setTermTabs(termTabs().map((t) => (t.id === tab ? { ...t, title: `agent failed: ${String(m.error ?? '')}` } : t)));
      return;
    }
    if (m.kind === 'agent.snapshot') {
      const key = String(m.key ?? '');
      agentResyncPending.delete(key);
      setAgentEvents({ ...agentEvents(), [key]: (m.events ?? []) as AgentEvent[] });
      return;
    }
    if (m.kind === 'agent.event') {
      const key = String(m.key ?? '');
      const ev = m.event as AgentEvent;
      const cur = agentEvents()[key] ?? [];
      // Same seq means the BE updated a row in place (a tool going
      // pending → completed), not a new line; a streamed reply's later
      // chunks are deltas that append. A delta with no base means our
      // copy is behind — ask for the history again, once.
      const r = applyAgentEvent(cur, ev);
      setAgentEvents({ ...agentEvents(), [key]: r.events });
      if (r.gap && !agentResyncPending.has(key)) {
        agentResyncPending.add(key);
        send({ kind: 'agent.resync', key });
      }
      return;
    }
    if (m.kind === 'agent.state') {
      setAgentRoster((m.state ?? {}) as { rows?: AgentRow[]; asks?: AgentAsk[] });
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
      // Setting channelID flips the tab from placeholder to
      // <Terminal channelId={N}>. The component opens the raw
      // channel synchronously on first render; bytes the BE has
      // already sent are still in the router's pendingRaw queue
      // and drain into the component on subscribe.
      setTermTabs(termTabs().map((t) => t.id === localID ? { ...t, channelID } : t));
      return;
    }
    if (m.kind === 'term.closed') {
      const channelID = Number(m.channel_id ?? 0);
      if (!channelID) return;
      const tab = termTabs().find((t) => t.channelID === channelID);
      termAPIs.delete(channelID);
      if (tab) {
        setTermTabs(termTabs().filter((t) => t.id !== tab.id));
        if (activeTermID() === tab.id) {
          const remaining = termTabs().filter((t) => t.id !== tab.id);
          setActiveTermID(remaining[0]?.id ?? '');
        }
      }
      return;
    }
    // Resolve a correlated reply via the bus (uncorrelated pushes were
    // handled by the switch above).
    bus.tryResolve(m);
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

  // openAgentTab starts a coding-agent session in the pane, in the folder
  // the editor already has open — the reason hosting one here beats the
  // standalone Agent app, where the first question is always "which
  // folder". The tab exists immediately so the user sees it starting.
  const openAgentTab = (agentID: string) => {
    setTermOpen(true);
    nextTermLocalID += 1;
    const localID = `t-${nextTermLocalID}`;
    setTermTabs([...termTabs(), {
      id: localID, channelID: 0, kind: 'agent',
      agentName: agentID, title: `${agentID}…`,
    }]);
    setActiveTermID(localID);
    send({ kind: 'agent.start', tab: localID, agent: agentID });
  };

  // ---- send to agent ----
  //
  // The selection (or the whole buffer) as a fenced block with the
  // file's path, dropped into the agent tab's composer as a DRAFT: the
  // point is to type "why is this wrong?" next to it, not to fire the
  // code off on its own.
  //
  // SEAM: the Agent app is growing an `agent_draft` app-message
  // ({kind:'agent_draft', text}) that it inserts into its composer.
  // edit does not go through that path because it HOSTS AgentSession
  // itself — there is no app on the other end of a message — and
  // AgentSession takes no draft prop (web/lib/src/agent-session.tsx is
  // the agent track's). Until it does, the draft is written into the
  // composer's textarea the way a paste would be: set the value through
  // the native setter and fire `input`, which is exactly what the
  // component's own onInput consumes. When AgentSession grows a draft
  // input, this becomes a one-line change.

  // agentDraftFor builds the block: the path (with the line range when
  // it is a selection) above a fence tagged with the tab's language.
  const agentDraftFor = (t: Tab): string => {
    const fence = currentLang() === 'plain' ? '' : currentLang();
    let body = tabContent(t);
    let where = t.path || t.displayName;
    if (t.mode !== 'wysiwyg' && editorView && t.id === activeID()) {
      const sel = editorView.state.selection.main;
      if (!sel.empty) {
        body = editorView.state.sliceDoc(sel.from, sel.to);
        const from = editorView.state.doc.lineAt(sel.from).number;
        const to = editorView.state.doc.lineAt(sel.to).number;
        where += from === to ? `:${from}` : `:${from}-${to}`;
      }
    }
    return `${where}\n\n\`\`\`${fence}\n${body.replace(/\n*$/, '')}\n\`\`\`\n`;
  };

  // Drafts handed to an agent tab's composer, keyed by tab id. The
  // component takes them through its insertDraft prop and appends at the
  // caret, queuing for free when the session has not started: its
  // composer is a controlled input on the signal it writes, so text set
  // before the session exists is simply there when the box goes live.
  // This replaces poking the textarea with execCommand and retrying for
  // half a minute to find out whether it had landed.
  const [agentDrafts, setAgentDrafts] = createSignal<Record<string, InsertedDraft>>({});
  let draftSeq = 0;

  // sendToAgent opens the pane (starting a session if there is none) and
  // lands the draft once the composer exists.
  const sendToAgent = () => {
    const t = activeTab();
    if (!t || t.blocked) {
      setStatusError('nothing to send — this tab has no buffer');
      return;
    }
    const text = agentDraftFor(t);
    let target = termTabs().find((x) => x.id === activeTermID() && x.kind === 'agent')
      ?? termTabs().find((x) => x.kind === 'agent');
    if (!target) {
      const adapter = agentAdapters()[0];
      if (!adapter) {
        setStatusError('no agent installed to send this to');
        return;
      }
      openAgentTab(adapter.id);
      target = termTabs().find((x) => x.kind === 'agent');
    }
    if (!target) return;
    setTermOpen(true);
    setActiveTermID(target.id);
    // seq is what makes the same selection insert twice: the component
    // acts on a value it has not seen before, not on a changed string.
    draftSeq += 1;
    setAgentDrafts({ ...agentDrafts(), [target.id]: { text, seq: draftSeq } });
    setStatusError(null);
  };

  // Adapters agentd found, for the + menu. Empty until the roster push
  // arrives, which is why the menu says so rather than looking broken.
  const agentAdapters = (): { id: string; name?: string }[] => agentRoster().adapters ?? [];

  // The roster row backing an agent tab: its state, context usage, mode,
  // and the agent's own settings. Same shape the Agent app renders.
  const agentStatusFor = (key: string): AgentStatus => {
    const r = (agentRoster().rows ?? []).find((x) => x.key === key);
    return {
      agent: r?.agent, dir: r?.dir, state: r?.state, used: r?.used, size: r?.size,
      title: r?.title, mode: r?.mode, modes: r?.modes, configs: r?.configs,
      commands: r?.commands, yolo: r?.yolo,
    };
  };
  const agentAsksFor = (key: string): AgentAsk[] =>
    (agentRoster().asks ?? []).filter((a) => (a as { row_key?: string }).row_key === key);

  const closeTerm = (id: string) => {
    const tab = termTabs().find((t) => t.id === id);
    if (!tab) return;
    if (tab.kind === 'agent') {
      // The tab goes; the SESSION does not. agentd outlives its hosts —
      // that is what Resume is — so closing a tab detaches rather than
      // killing, and the agent stays on the roster to come back to.
      if (tab.agentKey) send({ kind: 'agent.close', key: tab.agentKey });
      setTermTabs(termTabs().filter((t) => t.id !== tab.id));
      if (activeTermID() === tab.id) {
        const remaining = termTabs().filter((t) => t.id !== tab.id);
        setActiveTermID(remaining[0]?.id ?? '');
      }
      return;
    }
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

  // DRAG_MIME + readDragPaths + the hasWashDrag/dropEffectFor helpers
  // live in @wash/fs-client's dnd.ts (unit-tested). edit carries a
  // single path (no multi-select), so the dragstart payload stays inline.
  const onRowDragStart = (ev: DragEvent, p: string) => {
    if (!ev.dataTransfer) return;
    ev.dataTransfer.effectAllowed = 'copyMove';
    // JSON array with one path keeps the wire format identical to
    // fm's multi-select payload — future-proofs cross-window drops.
    ev.dataTransfer.setData(DRAG_MIME, JSON.stringify([p]));
    ev.dataTransfer.setData('text/plain', p);
  };
  const onRowDragEnd = () => setDropTargetPath('');

  // <FileTree> wires these on every row; only folders are valid move targets,
  // so a drag over a file row early-returns and bubbles to the list container
  // (onListDragOver/onListDrop), matching the old dir-only wiring.
  const onRowDragOver = (ev: DragEvent, rowPath: string, entry?: Entry) => {
    if (entry && entry.type !== 'dir') return;
    if (!hasWashDrag(ev.dataTransfer)) return;
    ev.preventDefault();
    ev.stopPropagation();
    ev.dataTransfer!.dropEffect = dropEffectFor(ev.altKey);
    if (dropTargetPath() !== rowPath) setDropTargetPath(rowPath);
  };
  const onRowDrop = (ev: DragEvent, rowPath: string, entry?: Entry) => {
    if (entry && entry.type !== 'dir') return;
    const paths = readDragPaths(ev.dataTransfer);
    if (paths.length === 0) return;
    ev.preventDefault();
    ev.stopPropagation();
    setDropTargetPath('');
    if (ev.altKey) {
      setDropMenu({
        x: ev.clientX + 8,
        y: ev.clientY + 8,
        src: paths[0],
        destDir: rowPath,
      });
      return;
    }
    void commitMove(paths[0], rowPath);
  };

  const onListDragOver = (ev: DragEvent) => {
    if (!hasWashDrag(ev.dataTransfer)) return;
    ev.preventDefault();
    ev.dataTransfer!.dropEffect = dropEffectFor(ev.altKey);
    if (dropTargetPath() !== '') setDropTargetPath('');
  };
  const onListDrop = (ev: DragEvent) => {
    const paths = readDragPaths(ev.dataTransfer);
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
      setDropMenu({
        x: ev.clientX + 8,
        y: ev.clientY + 8,
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
    const reply = await sendWithReply({ kind: 'rename', from: src, to });
    // fs.watch on the parents catches up automatically; open tabs
    // under the moved path follow it.
    if (reply.kind === 'rename_ok') retargetTabs(String(reply.from ?? src), String(reply.to ?? to));
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
    const reply = await sendWithReply({ kind: 'rename', from: r.path, to });
    if (reply.kind === 'rename_ok') retargetTabs(String(reply.from ?? r.path), String(reply.to ?? to));
  };

  // retargetTabs re-keys every open tab whose file the editor itself just
  // renamed or moved (`from` → `to`; a moved directory carries the tabs
  // under it). Before this the tab stayed on the old path: the watcher
  // saw a delete, the buffer was kept, and the next Ctrl+S recreated the
  // old file next to the renamed one. Buffer state, undo history, dirty
  // flag and wysiwyg content all survive; only the identity changes. The
  // tab id IS the path, so the active-tab effect re-seeds the view from
  // the captured live state exactly as Save As does.
  const retargetTabs = (from: string, to: string) => {
    if (!from || !to || from === to) return;
    const prefix = from + '/';
    const under = (p: string) => p === from || p.startsWith(prefix);
    if (!tabs().some((t) => !!t.path && under(t.path))) return;
    captureActiveState();
    const oldDirs = new Set<string>();
    const newDirs = new Set<string>();
    const moved: Array<[string, string]> = [];
    let nextActive = activeID();
    const updated = tabs().map((t) => {
      if (!t.path || !under(t.path)) return t;
      const newPath = t.path === from ? to : to + t.path.slice(from.length);
      oldDirs.add(parentPath(t.path));
      newDirs.add(parentPath(newPath));
      moved.push([t.id, newPath]);
      if (t.id === activeID()) nextActive = newPath;
      // The TipTap host is keyed by tab id, so the rename remounts it;
      // snapshot the live markdown for the re-seed and retire the old
      // handle so it neither leaks nor keeps writing to a dead id.
      let wysCache = t.wysCache;
      const h = wysHandles.get(t.id);
      if (h) {
        if (t.mode === 'wysiwyg') wysCache = h.getMarkdown();
        h.destroy();
        wysHandles.delete(t.id);
      }
      return { ...t, id: newPath, path: newPath, displayName: baseName(newPath) || newPath, wysCache, missing: false };
    });
    setTabs(updated);
    setDirtyIDs((s) => {
      if (!moved.some(([o]) => s.has(o))) return s;
      const out = new Set(s);
      for (const [o, n] of moved) if (s.has(o)) { out.delete(o); out.add(n); }
      return out;
    });
    const rp = reloadPrompt();
    if (rp && moved.some(([o]) => o === rp.tabID)) setReloadPrompt(null);
    if (nextActive !== activeID()) setActiveID(nextActive);
    for (const d of newDirs) fileWatch.watch(d);
    for (const d of oldDirs) {
      if (!tabs().some((t) => t.path && parentPath(t.path) === d)) fileWatch.unwatch(d);
    }
    persist();
  };

  // revealInFm opens a Files window AT the thing the user is looking
  // at: the active tab's file (fm lists its folder), else the sidebar
  // selection, else the project root. The BE forwards the path as the
  // spawn request's `open`, which the router hands to fm as
  // `--open <path>` — the same launch seam open routing uses for edit.
  const revealInFm = () => {
    const t = activeTab();
    const target = (t?.path && !t.diff ? t.path : '') || selectedPath() || root();
    send({ kind: 'spawn', app_id: 'com.wash.fm', ...(target ? { open: target } : {}) });
  };

  const openFolderIn = (appID: 'com.wash.term' | 'com.wash.fm', folder: string) => {
    if (folder) send({ kind: 'spawn', app_id: appID, open: folder });
  };
  const contextFolder = () => {
    const c = ctxMenu();
    return c ? (isDirLike(c.entry) ? c.path : parentPath(c.path)) : '';
  };
  const openContextFolderIn = (appID: 'com.wash.term' | 'com.wash.fm') => {
    const folder = contextFolder();
    closeCtxMenu();
    openFolderIn(appID, folder);
  };

  // ---- drop-to-open on the editor area ----
  //
  // A drag from fm (or this sidebar) carries application/x-wash-paths.
  // Dropped on the tab strip or the editor body it OPENS each path in a
  // tab. The sidebar tree keeps its move semantics — that is a file
  // tree — but the editor is not a folder, and before this the drop fell
  // through to CodeMirror's default handler, which inserted the drag's
  // text/plain fallback (the path) into the buffer. Both listeners run in
  // the capture phase on the pane so they win over CM's (and TipTap's)
  // own drop handling on the content DOM. An OS file drop carries no
  // path the BE could read, so it is swallowed with a hint rather than
  // letting CM paste the file's bytes into the open buffer.
  const isOsFileDrag = (dt: DataTransfer | null): boolean =>
    !!dt && !hasWashDrag(dt) && Array.from(dt.types).includes('Files');
  const onPaneDragOver = (ev: DragEvent) => {
    const dt = ev.dataTransfer;
    if (!dt) return;
    if (!hasWashDrag(dt) && !isOsFileDrag(dt)) return;
    ev.preventDefault();
    ev.stopPropagation();
    // 'copy', never 'move': the source stays where it is.
    dt.dropEffect = 'copy';
  };
  const onPaneDrop = (ev: DragEvent) => {
    const dt = ev.dataTransfer;
    if (!dt) return;
    if (hasWashDrag(dt)) {
      ev.preventDefault();
      ev.stopPropagation();
      const paths = readDragPaths(dt);
      // Sequential so each open sees the tabs the previous one added.
      void (async () => { for (const p of paths) await openInTab(p); })();
      return;
    }
    if (isOsFileDrag(dt)) {
      ev.preventDefault();
      ev.stopPropagation();
      setStatusError('drag files from Files (fm) to open them');
    }
  };

  // ---- tree ops + fs.watch ----
  //
  // The watched-dirs dedup set + the fs_event refresh debounce live in
  // @wash/fs-client's watch.ts (unit-tested, shared with fm). Every
  // expand subscribes (idempotent BE-side), every collapse releases the
  // subtree (fsWatch.unwatchWhere below); the root gets watched at boot
  // via loadDir's first success. onCleanup tears every remaining sub
  // down so a closed editor doesn't strand watchers in the BE.
  //
  // edit's BE speaks the dotted fs.watch/fs.unwatch kinds, and its
  // refresh guard is just "still listed" (no expanded gate — collapse
  // already unwatches the subtree, so no stale events arrive).
  const fsWatch = createWatch({
    send,
    refresh: (dir) => void loadDir(dir),
    shouldRefresh: (dir) => !!listings[dir],
    watchKind: 'fs.watch',
    unwatchKind: 'fs.unwatch',
  });

  // fileWatch tracks the parent directories of open file tabs, kept
  // separate from the sidebar's fsWatch so tree-collapse (which
  // unwatches whole subtrees) can't strand an open file's watch. It
  // only uses the watch/unwatch dedup — refreshes are handled per-file
  // by maybeReloadFromDisk, so shouldRefresh is a no-op. The BE refMap
  // refcounts per path, so a dir watched by both instances stays alive
  // until both release it and still delivers one event per change.
  const fileWatch = createWatch({
    send,
    refresh: () => {},
    shouldRefresh: () => false,
    watchKind: 'fs.watch',
    unwatchKind: 'fs.unwatch',
  });

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
      fsWatch.unwatchWhere((w) => w === path || w.startsWith(prefix));
    } else {
      setExpanded(path, true);
      if (!listings[path]) void loadDir(path);
      fsWatch.watch(path);
    }
  };

  // visibleRows flattens the tree into render-able rows. The recursive
  // walk lives in @wash/fs-client's flattenTree (unit-tested, shared with
  // fm). edit always sorts name-asc with hidden filtered — exactly the
  // {key:'name', desc:false, showHidden:false} the comparator produces —
  // and has no in-flight fallback bridge, so cur:'' skips it. flattenTree
  // also computes childCount, which edit'\''s rows simply ignore. Passing
  // the store proxies in keeps the memo reactive (synchronous read).
  type EditRow = { entry: Entry; path: string; depth: number };
  const flatRows = createMemo<EditRow[]>(() =>
    flattenTree<Entry>({
      listings,
      expanded,
      sort: { key: 'name', desc: false, showHidden: false },
      start: root(),
      cur: '',
    }),
  );

  // Row-identity stabilisation now lives inside the shared <FileTree>
  // (@wash/ui); edit just feeds it flatRows().

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
    // Symlinks are checked BEFORE the dir test: edit follows a link to its
    // canonical target rather than expanding it in place, so the tree shows
    // where the thing actually lives. isDirLike would short-circuit that.
    if (row.entry.type === 'symlink') {
      followSymlink(row.entry, row.path);
      return;
    }
    if (row.entry.type === 'dir') {
      toggleExpand(row.path);
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
    // The BE resolves the link for us now (Entry.link_type), so this no
    // longer has to fire both barrels and let one miss. Fall back to the
    // old try-both only when the type is unknown — a link listed by an
    // older BE, or one whose target vanished between list and click.
    switch (e.link_type) {
      case 'dir':
        if (!listings[target]) void loadDir(target);
        setExpanded(target, true);
        return;
      case 'file':
        void openInTab(target);
        return;
      default:
        void openInTab(target);
        if (!listings[target]) {
          void loadDir(target);
        }
        setExpanded(target, true);
    }
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
    if (entry && isDirLike(entry)) return p;
    return par || root();
  };

  // ---- context menu ----

  const openCtxMenu = (ev: MouseEvent, entry: Entry, p: string) => {
    ev.preventDefault();
    setSelectedPath(p);
    // Viewport coords — Menu portals to body with position:fixed.
    setCtxMenu({ x: ev.clientX, y: ev.clientY, entry, path: p });
  };
  const closeCtxMenu = () => setCtxMenu(null);

  // ctxCopyPath drops the selected row's path on the wash + system
  // clipboards so the user can paste it anywhere (terminal, chat,
  // etc). Mirrors fm's "Copy path" item — and unlike the old direct
  // navigator.clipboard call, works on insecure origins too and is
  // visible to wash-clipboard consumers (terminal right-click paste).
  const ctxCopyPath = (p: string) => {
    washCopyText(p);
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
  // The tab strip + editor body; drop-to-open listens here (capture).
  let editPaneEl!: HTMLDivElement;
  let editorView: EditorView | undefined;
  const langCompartment = new Compartment();
  // Holds the active syntax-highlight style; reconfigured live when the
  // pack's light/dark appearance changes (see onMount).
  const highlightCompartment = new Compartment();

  // markTabDirty toggles a tab's entry in dirtyIDs without rewriting
  // the whole set when nothing's changing. Same set semantics as the
  // CM-side dirtyListener, exposed so the wysiwyg handle can drive
  // it from TipTap's update event.
  const markTabDirty = (id: string, dirty: boolean) => {
    setDirtyIDs((s) => {
      const has = s.has(id);
      if (has === dirty) return s;
      const out = new Set(s);
      if (dirty) out.add(id); else out.delete(id);
      return out;
    });
  };

  // maybeReloadFromDisk reconciles an open file tab against a fresh
  // read after fileWatch reported its directory changed. Self-saves
  // and chmod/touch noise fall out for free: we only act when the disk
  // content actually differs from the tab's baseline (last-known
  // on-disk bytes). Clean tabs reload silently; dirty tabs raise the
  // reload prompt so the user's unsaved edits aren't clobbered.
  const maybeReloadFromDisk = async (path: string) => {
    if (!path) return;
    const tab = tabs().find((t) => t.path === path);
    // Diff and binary tabs aren't live-editable buffers; leave them.
    if (!tab || tab.diff || tab.blocked) return;
    // Already prompting for this tab — the write+chmod burst that one
    // save fires would otherwise re-read and re-arm repeatedly.
    if (reloadPrompt()?.tabID === tab.id) return;
    const reply = await sendWithReply({ kind: 'read', path });
    // Re-find: the tab may have closed (or been re-keyed by one of our
    // own renames) during the async read.
    const cur = tabs().find((t) => t.id === tab.id);
    if (!cur || cur.path !== path) return;
    // File vanished (an external rename, rm, checkout): keep the buffer
    // but say so, and route the next save through the picker rather
    // than recreating the path behind the user's back (saveTab).
    if (reply.kind === 'read_err' && (reply as { code?: string }).code === 'not_found') {
      if (!cur.missing) setTabs(tabs().map((x) => x.id === cur.id ? { ...x, missing: true } : x));
      return;
    }
    // Unreadable for another reason (perms, became a dir): keep the
    // buffer so the user can still save it back out.
    if (reply.kind !== 'read_ok' || blockedOf(reply)) return;
    if (cur.missing) setTabs(tabs().map((x) => x.id === cur.id ? { ...x, missing: false } : x));
    const raw = String(reply.content ?? '');
    const disk = toBuffer(raw);
    // Track the line endings on disk even when the text did not move:
    // the next save should match what is there now.
    const eol = detectEol(raw);
    if (eol !== cur.eol) setTabs(tabs().map((x) => x.id === cur.id ? { ...x, eol } : x));
    if (disk === cur.baseline) return; // no material change (incl. our own save)
    if (dirtyIDs().has(cur.id)) {
      setReloadPrompt({ tabID: cur.id, displayName: cur.displayName, diskContent: disk });
    } else {
      applyReload(cur.id, disk);
    }
  };

  // applyReload swaps a tab's content to the freshly-read disk version
  // and clears its dirty marker. Mirrors the wysiwyg<->source toggle
  // path: drive the live editor directly for the active CM tab, drop
  // the captured state for the rest so they re-seed from the new
  // baseline on next activation.
  const applyReload = (tabID: string, disk: string) => {
    const t = tabs().find((x) => x.id === tabID);
    if (!t) return;
    if (t.mode === 'wysiwyg') {
      setTabs(tabs().map((x) => x.id === tabID
        ? { ...x, baseline: disk, wysCache: disk, state: null } : x));
      const h = wysHandles.get(tabID);
      if (h) { h.setMarkdown(disk); h.markClean(); }
    } else {
      setTabs(tabs().map((x) => x.id === tabID
        ? { ...x, baseline: disk, state: null, scrollTop: 0 } : x));
      if (tabID === activeID() && editorView) {
        editorView.setState(EditorState.create({
          doc: disk,
          extensions: extensionsForTab({ ...t, baseline: disk }),
        }));
        editorView.dispatch({ effects: langCompartment.reconfigure(langExtensions()) });
      }
    }
    markTabDirty(tabID, false);
    persist();
  };

  // confirmReload — user chose Reload: discard their edits and load the
  // disk version captured when the prompt was raised.
  const confirmReload = () => {
    const p = reloadPrompt();
    setReloadPrompt(null);
    if (p) applyReload(p.tabID, p.diskContent);
  };

  // showReloadDiff — user wants to see the external change before
  // choosing. Keeps the buffer exactly as Keep editing does (disk
  // adopted as the baseline, so the dirty marker means "differs from
  // disk" and the same change does not re-prompt), then opens the
  // diff-tab machinery with the buffer as the new side and the disk
  // version as the original, where chunks can be taken or rejected one
  // at a time before the next save.
  const showReloadDiff = async () => {
    const p = reloadPrompt();
    if (!p) return;
    const t = tabs().find((x) => x.id === p.tabID);
    dismissReload();
    if (!t || !t.path) return;
    await openInTab(t.path);
    await openDiffTab(t.path);
  };

  // dismissReload — user chose Keep editing: hold their buffer but
  // adopt the disk version as the new baseline so (a) we don't re-prompt
  // for the same change and (b) the "modified" indicator now reflects
  // divergence from what's actually on disk. For an active CM tab we
  // capture the live editor state first, so the tabs() update doesn't
  // let the active-tab effect re-seed from baseline and lose the edits.
  const dismissReload = () => {
    const p = reloadPrompt();
    setReloadPrompt(null);
    if (!p) return;
    const isActive = activeID() === p.tabID;
    const liveState = isActive && editorView ? editorView.state : undefined;
    setTabs(tabs().map((x) => x.id === p.tabID
      ? { ...x, baseline: p.diskContent, ...(liveState ? { state: liveState } : {}) }
      : x));
    const t = tabs().find((x) => x.id === p.tabID);
    if (t) markTabDirty(p.tabID, tabContent(t) !== p.diskContent);
  };

  // mountWysiwyg creates the TipTap handle for `tabID` against the
  // freshly-mounted DOM element. Idempotent: the ref callback fires
  // each time Solid hands us a new host node (which only happens on
  // initial mount unless the For-row gets recreated). Initial
  // content comes from the tab's wysCache (set on tab-switch or
  // restore) falling back to its baseline (on-disk markdown).
  const mountWysiwyg = (tabID: string, el: HTMLDivElement) => {
    if (wysHandles.has(tabID)) return;
    const t = tabs().find((x) => x.id === tabID);
    if (!t) return;
    const initial = t.wysCache ?? t.baseline;
    const handle = createWysiwyg({
      parent: el,
      content: initial,
      onChange: (md) => {
        // Cache live markdown so persist() + tabContent() can read
        // it cheaply without going through getMarkdown each time.
        // The tab's baseline is what's on disk; the dirty marker
        // tracks divergence from that baseline.
        const cur = tabs().find((x) => x.id === tabID);
        if (cur) setTabs(tabs().map((x) => x.id === tabID ? { ...x, wysCache: md } : x));
        persist();
      },
      onDirtyChange: (d) => markTabDirty(tabID, d),
    });
    wysHandles.set(tabID, handle);
    if (activeID() === tabID) handle.focus();
  };

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
    // Checkpoint the live buffer on the coarse content cadence. Covers
    // both unsaved edits to a saved file and Untitled/wysiwyg buffers,
    // which pure typing (always docChanged) otherwise wouldn't persist
    // until some non-doc event fired.
    scheduleContentPersist();
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
  // The status bar's Ln/Col. Separate from selectionListener because it
  // also has to follow document changes (typing moves the caret without
  // setting the selection).
  const cursorListener = EditorView.updateListener.of((u) => {
    if (!u.selectionSet && !u.docChanged) return;
    const head = u.state.selection.main.head;
    const line = u.state.doc.lineAt(head);
    setCursorPos({ line: line.number, col: head - line.from + 1 });
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

    // Syntax highlighting — dark/light swapped by pack appearance.
    highlightCompartment.of(highlightFor(washAppearance())),
    langCompartment.of([]),
    indentCompartment.of(indentExtensions()),
    dirtyListener,
    searchListener,
    selectionListener,
    cursorListener,
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
      // CM binds Mod-g to find-next; every other editor binds it to
      // go-to-line, and F3 / Shift+F3 (still in searchKeymap) already
      // cover find-next. CM's own Mod-Alt-g stays bound as well.
      ...searchKeymap.filter((b) => b.key !== 'Mod-g'),
      { key: 'Mod-g', run: gotoLine, scope: 'editor search-panel', preventDefault: true },
      ...foldKeymap,
      ...completionKeymap,
      indentWithTab,
    ]),

    EditorView.theme({
      '&': { height: '100%', background: tokens.bgCanvas, color: tokens.fg },
      // Size through a custom property, not a literal: zooming sets the
      // property on the mount element, where an inline style beats every
      // stylesheet rule. A second CM theme would not — StyleModule
      // PREPENDS new modules, so a compartment reconfigured later ends
      // up EARLIER in the sheet and loses the tie to this shorthand.
      '.cm-scroller': { font: `var(--wash-edit-font-size, ${tokens.fontSizeBase}) ${tokens.fontMono}` },
      '.cm-content': { padding: '8px 0', caretColor: tokens.fg },
      '.cm-cursor': { borderLeftColor: tokens.fg },
      '.cm-activeLine': { backgroundColor: `color-mix(in srgb, ${tokens.fg} 4%, transparent)` },
      '.cm-activeLineGutter': { backgroundColor: `color-mix(in srgb, ${tokens.fg} 6%, transparent)` },
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
        background: tokens.bgInset,
        color: tokens.fg,
        border: `1px solid ${tokens.borderMenu}`,
        borderRadius: `${tokens.radiusSm}`,
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
        borderRadius: `${tokens.radiusSm}`,
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
        borderRadius: `${tokens.radiusSm}`,
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
        borderRadius: `${tokens.radiusMd}`,
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

  // Syntax highlighting uses the off-the-shelf Solarized palettes — the
  // canonical dual dark/light theme: Solarized Dark styles on dark packs,
  // Solarized Light on light packs (Seoul), swapped by appearance via
  // highlightCompartment (see baseExtensions + onMount). The editor chrome
  // stays wash-token-themed, so it tracks the pack alongside.
  const solarizedDarkHL = HighlightStyle.define(solarizedDarkStyle);
  const solarizedLightHL = HighlightStyle.define(solarizedLightStyle);
  const highlightFor = (a: 'light' | 'dark') =>
    syntaxHighlighting(a === 'light' ? solarizedLightHL : solarizedDarkHL, { fallback: true });

  // langForKey returns a CM6 language pack for an explicit key.
  // Used by both the path-derived default (langForPath) and the
  // Syntax menu's manual override.
  // langForKey + langKeyForPath are table-driven from LANGS — one
  // row per syntax owns its label, extensions, and CM6 factory.
  const langForKey = (key: string): Extension | Extension[] => {
    return LANGS_BY_KEY[key]?.lang() ?? [];
  };

  // langKeyForPath picks a syntax from the file's basename or
  // extension. Filenames win over extensions so e.g. "Dockerfile"
  // (no extension) still maps to dockerfile. Unknown → plain.
  const langKeyForPath = (path: string): string => {
    const base = (path.toLowerCase().split('/').pop() ?? '').trim();
    if (!base) return 'plain';
    for (const l of LANGS) {
      if (l.filenames?.includes(base)) return l.key;
    }
    const ext = base.includes('.') ? base.split('.').pop()! : '';
    if (ext) {
      for (const l of LANGS) {
        if (l.extensions?.includes(ext)) return l.key;
      }
    }
    return 'plain';
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

    // <Terminal> components carry their own ResizeObserver against
    // each host div, so a window resize bubbles into per-component
    // refits without a top-level coordinator. Background tabs
    // (display:none) re-fit on next activation via their own RO
    // firing when the size flips back to non-zero.

    // Create the EditorView once. Doc + language reconfigure on
    // file open via dispatch + compartment.
    editorView = new EditorView({
      state: EditorState.create({
        doc: '',
        extensions: baseExtensions(),
      }),
      parent: editorMountEl,
    });

    // Follow the pack: flip the syntax palette dark↔light when the active
    // pack's appearance changes (the editor chrome already tracks the
    // tokens). New states pick the right one at create-time via baseExtensions.
    onCleanup(onAppearanceChange((a) => {
      editorView?.dispatch({ effects: highlightCompartment.reconfigure(highlightFor(a)) });
    }));

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

      // Escape with the WYSIWYG find bar up closes it from anywhere
      // in the window (the bar's inputs handle their own Escape and
      // stop propagation before this fires). Checked ahead of the
      // sidebar Escape-deselect so find-dismiss wins.
      if (ev.key === 'Escape' && !cmd && !ev.altKey && wysFindOpen() && activeTab()?.mode === 'wysiwyg') {
        ev.preventDefault();
        closeWysFind();
        return;
      }

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

      // Tab navigation. Ctrl+Tab / Ctrl+Shift+Tab are what people reach
      // for, but Chromium reserves them in a normal browser tab, so the
      // same Alt alternates the term app binds are here too: Alt+PageDown /
      // Alt+PageUp cycle, Alt+1…9 jump. Matched on ev.code so a non-QWERTY
      // layout gets the same physical keys.
      if (ev.altKey && !cmd && !ev.shiftKey) {
        const code = ev.code;
        if (code === 'PageDown') { ev.preventDefault(); cycleTabs(1); return; }
        if (code === 'PageUp') { ev.preventDefault(); cycleTabs(-1); return; }
        const digit = /^Digit([1-9])$/.exec(code);
        if (digit) { ev.preventDefault(); jumpToTab(Number(digit[1])); return; }
      }
      if (cmd && ev.key === 'Tab' && !ev.altKey) {
        ev.preventDefault();
        cycleTabs(ev.shiftKey ? -1 : 1);
        return;
      }

      if (!cmd) return;
      // The file-level shortcuts act on the tab BEHIND a dialog, so they
      // are off while the picker, a confirm prompt or the sidebar's
      // inline rename owns the keyboard: Ctrl+W typed into the picker's
      // path input used to close the tab under it. Ctrl+` and the rest
      // are left alone — they do not touch the tab.
      const dialogUp = picker() !== null || pendingClose() !== null || reloadPrompt() !== null || renaming() !== null || revertPrompt() !== null || qoOpen();
      const fileKey = ev.key === 's' || ev.key === 'S' || ev.key === 'o' || ev.key === 'O'
        || ev.key === 'n' || ev.key === 'N' || ev.key === 'w' || ev.key === 'W';
      if (dialogUp && fileKey) return;
      // Ctrl+Alt+S: save every dirty tab.
      if ((ev.key === 's' || ev.key === 'S') && ev.altKey) {
        ev.preventDefault();
        void saveAll();
        return;
      }
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
        if (id) requestCloseTab(id);
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
      // Ctrl+F: find. In a WYSIWYG tab that is the TipTap bar. On a
      // source tab CM's searchKeymap already owns Mod-f while the
      // editor is focused, so this only has to cover the case where it
      // is not — clicking the tree and pressing Ctrl+F used to do
      // nothing at all.
      if ((ev.key === 'f' || ev.key === 'F') && !ev.shiftKey && !ev.altKey) {
        if (activeTab()?.mode === 'wysiwyg') {
          ev.preventDefault();
          setWysFindOpen(true);
          return;
        }
        if (!dialogUp && !isTypingInEditor() && activeTab()) {
          ev.preventDefault();
          cmdFind();
        }
        return;
      }
      // Ctrl+H: find and replace. Unlike Ctrl+F this is never CM's —
      // Chromium claims it for History — so it is always intercepted
      // here, including from inside the editor.
      if ((ev.key === 'h' || ev.key === 'H') && !ev.shiftKey && !ev.altKey) {
        ev.preventDefault();
        if (!dialogUp && activeTab()) cmdReplace();
        return;
      }
      // Ctrl+Shift+P: toggle the active tab between WYSIWYG and
      // source view. No-op for non-markdown tabs (toggleWysiwyg
      // bails). Lowercase variant covered by checking either case.
      if ((ev.key === 'p' || ev.key === 'P') && ev.shiftKey) {
        ev.preventDefault();
        toggleWysiwyg();
        return;
      }
      // Ctrl+Shift+Enter: send the selection (or the buffer) to the
      // agent pane as a draft.
      if (ev.key === 'Enter' && ev.shiftKey) {
        ev.preventDefault();
        sendToAgent();
        return;
      }
      // Ctrl+= / Ctrl+- / Ctrl+0: font zoom. Chromium's own page zoom is
      // not the same thing (it scales the whole desktop, chrome and all)
      // and is not reachable from a keydown anyway.
      if (ev.key === '=' || ev.key === '+') { ev.preventDefault(); zoomFont(1); return; }
      if (ev.key === '-' || ev.key === '_') { ev.preventDefault(); zoomFont(-1); return; }
      if (ev.key === '0') { ev.preventDefault(); setFontSize(DEFAULT_FONT_PX); return; }
      // Ctrl+G: go to line. The editor's own keymap covers the focused
      // editor; this is the same command reached from the sidebar or the
      // tab strip, where CM never sees the key.
      if ((ev.key === 'g' || ev.key === 'G') && !ev.shiftKey && !ev.altKey) {
        ev.preventDefault();
        if (!dialogUp && !isTypingInEditor()) cmdGotoLine();
        return;
      }
      // Ctrl+P: quick open (Chromium's Print otherwise).
      if ((ev.key === 'p' || ev.key === 'P') && !ev.shiftKey && !ev.altKey) {
        ev.preventDefault();
        if (!dialogUp) openQuickOpen();
        return;
      }
    };
    // Ctrl+wheel zooms, the other half of the same gesture. Non-passive
    // because the default is the browser's page zoom, which has to be
    // suppressed; bound on the editor pane so wheeling over the sidebar
    // or the terminal is left alone.
    const onWheel = (ev: WheelEvent) => {
      if (!ev.ctrlKey && !ev.metaKey) return;
      ev.preventDefault();
      if (ev.deltaY === 0) return;
      zoomFont(ev.deltaY < 0 ? 1 : -1);
    };
    editPaneEl.addEventListener('wheel', onWheel, { passive: false });
    onCleanup(() => editPaneEl.removeEventListener('wheel', onWheel));

    props.host.addEventListener('keydown', onKey);
    if (!props.host.hasAttribute('tabindex')) props.host.setAttribute('tabindex', '0');
    editPaneEl.addEventListener('dragover', onPaneDragOver, true);
    editPaneEl.addEventListener('drop', onPaneDrop, true);

    // Boot with a list of "/" — the BE's Confine downshifts to
    // the sandbox root automatically when one is configured.
    void loadDir('/');
    void loadPrefs();
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('wash:state', onState);
      props.host.removeEventListener('keydown', onKey);
      editPaneEl.removeEventListener('dragover', onPaneDragOver, true);
      editPaneEl.removeEventListener('drop', onPaneDrop, true);
      // Release every active fs.watch so the BE doesn't strand
      // them after the editor window closes, and clear pending
      // refresh timers. Idempotent BE-side.
      fsWatch.unwatchWhere(() => true);
      fsWatch.dispose();
      fileWatch.unwatchWhere(() => true);
      fileWatch.dispose();
      // <Terminal> components handle their own xterm disposal +
      // raw-channel unsubscribe via onCleanup; just drop the API
      // map so we're not holding references after teardown.
      termAPIs.clear();
      // Tear down every TipTap editor; each holds its own DOM
      // listeners + ProseMirror plumbing.
      for (const h of wysHandles.values()) h.destroy();
      wysHandles.clear();
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
  //
  // ONLY a change of active tab may seed the view. The effect also reads
  // tabs(), so it re-runs on every tab mutation, and seeding on those was
  // a data-loss bug: saving mutates tabs() (new baseline), so Ctrl+S
  // re-entered here and overwrote the live buffer. With no captured state
  // it rebuilt the doc from baseline, discarding the caret, the scroll
  // position and the whole undo history; with one — any tab you have
  // switched away from and back — it re-applied that stale snapshot, so
  // the text you had just saved vanished from the screen while the disk
  // kept it, and the next save wrote the reverted text back over it.
  // While the id is unchanged the view owns its content; the paths that
  // legitimately replace it (applyReload, toggleWysiwyg) drive editorView
  // directly.
  //
  // seededID is whose buffer the view currently holds — null for the
  // empty view. Keyed on the tab actually seeded rather than on
  // activeID() alone, so a tab whose object has not arrived in tabs()
  // yet (session restore sets the id first) still gets seeded when it
  // does, instead of being latched as already handled.
  let seededID: string | null = null;
  createEffect(() => {
    const id = activeID();
    const t = tabs().find((x) => x.id === id);
    if (!editorView) return;
    if (!t) {
      if (seededID === null) return;
      seededID = null;
      setLangOverride(null);
      setWordWrap(false);
      // No active tab — leave the view empty.
      editorView.setState(EditorState.create({ doc: '', extensions: baseExtensions() }));
      return;
    }
    if (seededID === id) return;
    seededID = id;
    // Adopt this tab's own view settings rather than resetting them.
    setLangOverride(t.lang ?? null);
    setWordWrap(!!t.wrap);
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
    // Put the scroll back where captureActiveState left it. The
    // EditorState carries cursor + undo history but not the scroll
    // position, so a switched-away tab used to come back at the top;
    // only session restore (restoreFrom) put it back. Applied in CM's
    // measure write phase rather than a bare microtask: the fresh state's
    // first measure calibrates CM's line-height estimate, and a scrollTop
    // set before that lands in uncalibrated units and drifts by a few
    // percent once the estimate is corrected.
    if (t.scrollTop && t.mode !== 'wysiwyg') {
      const top = t.scrollTop;
      editorView.requestMeasure({
        read: () => null,
        write: (_m, view) => { if (seededID === id) view.scrollDOM.scrollTop = top; },
      });
    }
    // For wysiwyg tabs, hand focus to TipTap instead of CM — CM is
    // hidden under the wysiwyg layer. The handle may not exist yet
    // (mount ref callback runs after this effect on first activation);
    // mountWysiwyg auto-focuses in that case.
    if (t.mode === 'wysiwyg') {
      wysHandles.get(t.id)?.focus();
    } else {
      editorView.focus();
    }
    syncCursor();
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

  // Same for indentation: it changes when the tab changes (each file
  // carries its own), when the indent picker is used, and when another
  // window edits the prefs default under a tab that has no detection of
  // its own to go on.
  createEffect(() => {
    const ind = activeIndent();
    if (!editorView) return;
    editorView.dispatch({
      effects: indentCompartment.reconfigure([
        indentUnit.of(indentString(ind)),
        EditorState.tabSize.of(ind.width),
      ]),
    });
  });

  // ---- render ----

  return (
    <>
      {/* menu bar */}
      <div data-testid="edit-menubar" style={menuBarStyle}>
        <MenuBarButton id="file" label="File" active={openMenu() === 'file'} onClick={openMenuFor} />
        <MenuBarButton id="edit" label="Edit" active={openMenu() === 'edit'} onClick={openMenuFor} />
        <MenuBarButton id="view" label="View" active={openMenu() === 'view'} onClick={openMenuFor} />
        <MenuBarButton id="syntax" label="Syntax" active={openMenu() === 'syntax'} onClick={openMenuFor} />
        <MenuBarButton id="terminal" label="Terminal" active={openMenu() === 'terminal'} onClick={openMenuFor} />
        <Show when={openMenu() === 'file'}>
          <Menu x={menuAnchor().x} y={menuAnchor().y} onDismiss={closeMenu} data-testid="edit-menu-file">
            <MenuItem label="New" trailing={<kbd style={kbdStyle}>Ctrl+N</kbd>} onClick={run(newUntitled)} data-testid="edit-menu-new" />
            <MenuItem label="Open…" trailing={<kbd style={kbdStyle}>Ctrl+O</kbd>} onClick={run(() => setPicker({ mode: 'open' }))} data-testid="edit-menu-open" />
            <MenuItem label="Open Folder…" onClick={run(() => setPicker({ mode: 'directory' }))} data-testid="edit-menu-open-folder" />
            <MenuItem label="Quick Open…" trailing={<kbd style={kbdStyle}>Ctrl+P</kbd>} onClick={run(openQuickOpen)} data-testid="edit-menu-quick-open" />
            <MenuItem label="Open Recent" trailing={<span style={langHintStyle}>▸</span>} disabled={recent().length === 0} onClick={() => setOpenMenu('recent')} data-testid="edit-menu-open-recent" />
            <MenuSeparator />
            <MenuItem label="Open terminal window in project folder" icon={<TerminalIcon size={14} />} title={root()} disabled={!root()} onClick={run(() => openFolderIn('com.wash.term', root()))} data-testid="edit-menu-open-terminal" />
            <MenuItem label="Open file manager in project folder" icon={<FolderIcon size={14} />} title={root()} disabled={!root()} onClick={run(() => openFolderIn('com.wash.fm', root()))} data-testid="edit-menu-open-file-manager" />
            <MenuSeparator />
            <MenuItem label="Save" trailing={<kbd style={kbdStyle}>Ctrl+S</kbd>} disabled={!activeTab()} onClick={run(() => void saveActive())} data-testid="edit-menu-save" />
            <MenuItem label="Save As…" trailing={<kbd style={kbdStyle}>Ctrl+Shift+S</kbd>} disabled={!activeTab()} onClick={run(saveAsActive)} data-testid="edit-menu-save-as" />
            <MenuItem label="Save All" trailing={<kbd style={kbdStyle}>Ctrl+Alt+S</kbd>} disabled={dirtyTabs().length === 0} onClick={run(() => void saveAll())} data-testid="edit-menu-save-all" />
            <MenuItem label="Revert" disabled={!activeTab()?.path || !!activeTab()?.blocked || !!activeTab()?.diff} onClick={run(revertActive)} data-testid="edit-menu-revert" />
            <MenuSeparator />
            <MenuItem label="Close Tab" trailing={<kbd style={kbdStyle}>Ctrl+W</kbd>} disabled={!activeTab()} onClick={run(() => requestCloseTab(activeID()))} data-testid="edit-menu-close-tab" />
            <MenuItem label="Close Others" disabled={tabs().length < 2} onClick={run(closeOtherTabs)} data-testid="edit-menu-close-others" />
            <MenuItem label="Close All" disabled={tabs().length === 0} onClick={run(closeAllTabs)} data-testid="edit-menu-close-all" />
          </Menu>
        </Show>
        <Show when={openMenu() === 'recent'}>
          {/* Open Recent: the shared list, newest first, in place of the
              File menu (Menu has no submenus). */}
          <Menu x={menuAnchor().x} y={menuAnchor().y} onDismiss={closeMenu} data-testid="edit-menu-recent">
            <For each={recent().slice(0, 12)}>
              {(p) => (
                <MenuItem
                  label={baseName(p) || p}
                  trailing={<span style={langHintStyle}>{qoRel(parentPath(p)) || '/'}</span>}
                  onClick={run(() => void openInTab(p))}
                  data-testid={`edit-menu-recent-${p}`}
                />
              )}
            </For>
            <MenuSeparator />
            <MenuItem label="More…" trailing={<kbd style={kbdStyle}>Ctrl+P</kbd>} onClick={run(openQuickOpen)} data-testid="edit-menu-recent-more" />
          </Menu>
        </Show>
        <Show when={openMenu() === 'edit'}>
          <Menu x={menuAnchor().x} y={menuAnchor().y} onDismiss={closeMenu} data-testid="edit-menu-edit">
            <MenuItem label="Undo" trailing={<kbd style={kbdStyle}>Ctrl+Z</kbd>} onClick={run(cmdUndo)} data-testid="edit-menu-undo" />
            <MenuItem label="Redo" trailing={<kbd style={kbdStyle}>Ctrl+Shift+Z</kbd>} onClick={run(cmdRedo)} data-testid="edit-menu-redo" />
            <MenuSeparator />
            <MenuItem label="Cut" trailing={<kbd style={kbdStyle}>Ctrl+X</kbd>} onClick={run(cmdCut)} data-testid="edit-menu-cut" />
            <MenuItem label="Copy" trailing={<kbd style={kbdStyle}>Ctrl+C</kbd>} onClick={run(cmdCopy)} data-testid="edit-menu-copy" />
            <MenuItem label="Paste" trailing={<kbd style={kbdStyle}>Ctrl+V</kbd>} onClick={run(cmdPaste)} data-testid="edit-menu-paste" />
            <MenuSeparator />
            <MenuItem label="Find" trailing={<kbd style={kbdStyle}>Ctrl+F</kbd>} onClick={run(cmdFind)} data-testid="edit-menu-find" />
            <MenuItem label="Find & Replace" trailing={<kbd style={kbdStyle}>Ctrl+H</kbd>} onClick={run(cmdReplace)} data-testid="edit-menu-replace" />
            <MenuSeparator />
            <MenuItem label="Go to Line…" trailing={<kbd style={kbdStyle}>Ctrl+G</kbd>} disabled={!activeTab()} onClick={run(cmdGotoLine)} data-testid="edit-menu-goto-line" />
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
            {/* Agents live in the Terminal menu because they live in the
                terminal PANE. Reachable from here whether or not that pane
                is open — the ✦ button in the tab strip cannot be, and
                needing to open a shell first before you can start an agent
                is a silly gate. */}
            <Show
              when={agentAdapters().length > 0}
              fallback={<MenuItem label="No agents installed" disabled onClick={() => {}} data-testid="edit-menu-agent-none" />}
            >
              <For each={agentAdapters()}>
                {(a) => (
                  <MenuItem
                    label={`New ${a.name || a.id} session`}
                    onClick={run(() => openAgentTab(a.id))}
                    data-testid={`edit-menu-agent-${a.id}`}
                  />
                )}
              </For>
            </Show>
            <MenuSeparator />
            <MenuItem
              label="Send to Agent"
              trailing={<kbd style={kbdStyle}>Ctrl+Shift+Enter</kbd>}
              disabled={!activeTab() || !!activeTab()!.blocked}
              onClick={run(sendToAgent)}
              data-testid="edit-menu-send-agent"
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
        <Show when={openMenu() === 'view'}>
          <Menu x={menuAnchor().x} y={menuAnchor().y} onDismiss={closeMenu} data-testid="edit-menu-view">
            <MenuItem
              label="WYSIWYG"
              trailing={activeTab()?.mode === 'wysiwyg' ? <span style={menuCheckStyle}><Check size={12} /></span> : <kbd style={kbdStyle}>Ctrl+Shift+P</kbd>}
              disabled={!activeTab() || !isMarkdownPath(activeTab()!.path)}
              onClick={run(() => { if (activeTab()?.mode !== 'wysiwyg') toggleWysiwyg(); })}
              data-testid="edit-menu-wysiwyg"
            />
            <MenuItem
              label="Source"
              trailing={activeTab()?.mode === 'source' ? <span style={menuCheckStyle}><Check size={12} /></span> : undefined}
              disabled={!activeTab() || !isMarkdownPath(activeTab()!.path)}
              onClick={run(() => { if (activeTab()?.mode !== 'source') toggleWysiwyg(); })}
              data-testid="edit-menu-source"
            />
            <MenuSeparator />
            {/* On-save cleanups. Desktop-wide (they live in prefs, not
                in the window's state) and off by default: silently
                rewriting somebody's file on save is only welcome when
                they asked for it. */}
            <MenuItem
              label="Trim Trailing Whitespace on Save"
              trailing={prefs().trim_trailing ? <span style={menuCheckStyle}><Check size={12} /></span> : undefined}
              onClick={run(() => void setPref({ trim_trailing: !prefs().trim_trailing }))}
              data-testid="edit-menu-trim-trailing"
            />
            <MenuItem
              label="Ensure Final Newline on Save"
              trailing={prefs().final_newline ? <span style={menuCheckStyle}><Check size={12} /></span> : undefined}
              onClick={run(() => void setPref({ final_newline: !prefs().final_newline }))}
              data-testid="edit-menu-final-newline"
            />
          </Menu>
        </Show>
        <Show when={openMenu() === 'syntax'}>
          <Menu x={menuAnchor().x} y={menuAnchor().y} onDismiss={closeMenu} data-testid="edit-menu-syntax">
            <For each={LANGS}>
              {(l) => {
                // Trailing shows either the check (when this row is
                // the active syntax) or the list of file signals
                // mapped to it — extensions like ".js .mjs" or
                // filename triggers like "Dockerfile". Plays the
                // role of a hint so users learn the mapping.
                const hint = [
                  ...(l.extensions ?? []).map((e) => `.${e}`),
                  ...(l.filenames ?? []),
                ].join(' ');
                return (
                  <MenuItem
                    label={l.label}
                    trailing={
                      currentLang() === l.key
                        ? <span style={menuCheckStyle}><Check size={12} /></span>
                        : hint
                          ? <span style={langHintStyle}>{hint}</span>
                          : undefined
                    }
                    onClick={run(() => setLang(l.key === langKeyForPath(activeTab()?.path ?? '') ? null : l.key))}
                    data-testid={`edit-menu-lang-${l.key}`}
                  />
                );
              }}
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
        <Show when={openMenu() === 'indent'}>
          {/* The indent picker, off the status bar. Choosing a width
              retargets THIS tab; the default is a separate act, because
              one Go file is not a reason to change every new buffer. */}
          <Menu x={menuAnchor().x} y={menuAnchor().y} onDismiss={closeMenu} data-testid="edit-menu-indent">
            <MenuItem
              label="Tab"
              trailing={activeIndent().unit === 'tabs' ? <span style={menuCheckStyle}><Check size={12} /></span> : undefined}
              onClick={run(() => setTabIndent({ unit: 'tabs', width: activeIndent().width }))}
              data-testid="edit-menu-indent-tab"
            />
            <MenuSeparator />
            <For each={[2, 3, 4, 8]}>
              {(w) => (
                <MenuItem
                  label={`Spaces: ${w}`}
                  trailing={activeIndent().unit === 'spaces' && activeIndent().width === w
                    ? <span style={menuCheckStyle}><Check size={12} /></span> : undefined}
                  onClick={run(() => setTabIndent({ unit: 'spaces', width: w }))}
                  data-testid={`edit-menu-indent-spaces-${w}`}
                />
              )}
            </For>
            <MenuSeparator />
            <MenuItem
              label="Detect from Content"
              disabled={!activeTab()}
              onClick={run(() => { const t = activeTab(); if (t) setTabIndent(detectedIndent(tabContent(t))); })}
              data-testid="edit-menu-indent-detect"
            />
            <MenuItem
              label="Use as the Default"
              onClick={run(() => void setPref({ indent_unit: activeIndent().unit, indent_width: activeIndent().width }))}
              data-testid="edit-menu-indent-default"
            />
          </Menu>
        </Show>
        <Show when={openMenu() === 'eol'}>
          <Menu x={menuAnchor().x} y={menuAnchor().y} onDismiss={closeMenu} data-testid="edit-menu-eol">
            <MenuItem
              label="LF"
              trailing={activeTab()?.eol !== 'crlf' ? <span style={menuCheckStyle}><Check size={12} /></span> : <span style={langHintStyle}>Unix</span>}
              onClick={run(() => setEol('lf'))}
              data-testid="edit-menu-eol-lf"
            />
            <MenuItem
              label="CRLF"
              trailing={activeTab()?.eol === 'crlf' ? <span style={menuCheckStyle}><Check size={12} /></span> : <span style={langHintStyle}>Windows</span>}
              onClick={run(() => setEol('crlf'))}
              data-testid="edit-menu-eol-crlf"
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
            <Button
              variant="ghost"
              data-testid="edit-reveal-in-fm"
              title="Reveal in Files"
              onClick={revealInFm}
              style={{
                color: tokens.fgMuted,
                width: '22px',
                height: '22px',
                display: 'inline-flex',
                'align-items': 'center',
                'justify-content': 'center',
                padding: 0,
                'flex-shrink': 0,
              }}
            >
              <FolderIcon size={12} />
            </Button>
          </div>
          <FileTree
            rows={flatRows()}
            testIdPrefix="edit"
            containerStyle={sidebarListStyle}
            onContainerDragOver={onListDragOver}
            onContainerDrop={onListDrop}
            isSelected={(p) => selectedPath() === p}
            isExpanded={(p) => !!expanded[p]}
            isDropTarget={(p) => dropTargetPath() === p}
            renderIcon={(e) => <EntryIcon type={e.type} />}
            scrollTarget={() => selectedPath()}
            onRowClick={(p, e) => {
              if (renaming()?.path === p) return;
              onRowClick({ entry: e, path: p });
            }}
            onRowDblClick={(p, e) => {
              if (renaming()?.path === p) return;
              onRowDblClick({ entry: e, path: p });
            }}
            onToggle={(p) => toggleExpand(p)}
            onRowContextMenu={(ev, e, p) => openCtxMenu(ev, e, p)}
            onRowDragStart={(ev, p) => onRowDragStart(ev, p)}
            onRowDragEnd={onRowDragEnd}
            onRowDragOver={(ev, p, e) => onRowDragOver(ev, p, e)}
            onRowDrop={(ev, p, e) => onRowDrop(ev, p, e)}
            renaming={(p) => (renaming()?.path === p ? { draft: renaming()!.draft } : null)}
            onRenameInput={(v) => {
              const r = renaming();
              if (r) setRenaming({ ...r, draft: v });
            }}
            onRenameCommit={() => void commitRenameDraft()}
            onRenameCancel={cancelRename}
          />
        </div>

        <Splitter container={bodyEl} onChange={setSplitPct} data-testid="edit-splitter" />

        {/* editor area — tab bar above, CodeMirror below */}
        <div ref={editPaneEl!} data-testid="edit-pane" style={editorPaneStyle}>
          {/* tab bar */}
          <div data-testid="edit-tabs" style={tabBarStyle}>
            <For each={tabs()}>
              {(t) => {
                const isActive = () => activeID() === t.id;
                const isDirty = () => dirtyIDs().has(t.id);
                return (
                  <Tab
                    data-testid={`edit-tab-${t.id}`}
                    data-dirty={isDirty() ? 'true' : undefined}
                    active={isActive()}
                    // The strip and the keyboard share one switch path.
                    onClick={() => activateTab(t.id)}
                    onClose={() => requestCloseTab(t.id)}
                    closeTestId={`edit-tab-close-${t.id}`}
                    closeTitle="Close (Ctrl+W)"
                    closeGlyph={<Show when={isDirty()} fallback="×">●</Show>}
                    // Middle-click closes, through the same dirty guard as
                    // the × and Ctrl+W. mousedown is cancelled so the
                    // browser's autoscroll does not start on the way.
                    onMouseDown={(ev) => { if (ev.button === 1) ev.preventDefault(); }}
                    onAuxClick={(ev) => { if (ev.button === 1) { ev.preventDefault(); requestCloseTab(t.id); } }}
                    // Reorder by dragging along the strip. <Tab> spreads the
                    // rest of its props onto its button, and carries the
                    // `dragging` dim itself.
                    draggable={true}
                    dragging={dragTabID() === t.id}
                    onDragStart={(ev) => {
                      if (!ev.dataTransfer) return;
                      ev.dataTransfer.effectAllowed = 'move';
                      ev.dataTransfer.setData(TAB_DRAG_MIME, t.id);
                      setDragTabID(t.id);
                    }}
                    onDragEnd={() => setDragTabID(null)}
                    onDragOver={(ev) => {
                      if (!ev.dataTransfer?.types.includes(TAB_DRAG_MIME)) return;
                      ev.preventDefault();
                      ev.stopPropagation();
                      ev.dataTransfer.dropEffect = 'move';
                    }}
                    onDrop={(ev) => {
                      const src = ev.dataTransfer?.getData(TAB_DRAG_MIME) || dragTabID();
                      if (!src) return;
                      ev.preventDefault();
                      ev.stopPropagation();
                      setDragTabID(null);
                      moveTab(src, t.id);
                    }}
                  >
                    {t.displayName}
                  </Tab>
                );
              }}
            </For>
          </div>

          {/* formatting toolbar — only shown for wysiwyg tabs. Each
              button is a discrete TipTap command on the active tab's
              handle; no-op if the handle isn't mounted yet (rare —
              first paint of a fresh tab). */}
          <Show when={activeTab()?.mode === 'wysiwyg'}>
            <WysiwygToolbar tab={() => activeTab()!} handle={() => wysHandles.get(activeID())} />
          </Show>

          {/* Editor body. Holds three layers stacked at inset:0:
              - CM mount (always present; visible when active tab is
                source — placeholder overlays it for empty/binary).
              - Per-tab wysiwyg mounts (one div per wysiwyg tab,
                display:none for inactive ones — same pattern as the
                terminal pane's xterm hosts).
              - Placeholder overlay for the no-tab / binary states.

              Each editor layer owns its own focus and keyboard; the
              app-level keydown handler still fires because it's on
              props.host above them. */}
          <div
            style={editorBodyStyle}
            onContextMenu={(ev) => {
              // Right-click over the text layers opens the wash
              // Cut/Copy/Paste menu (the native menu's Paste can't
              // read the system clipboard on an insecure origin
              // anyway). No menu without a tab to act on.
              if (!activeTab() || activeTab()?.blocked) return;
              ev.preventDefault();
              setTextCtxMenu({ x: ev.clientX, y: ev.clientY });
            }}
          >
            <div
              ref={editorMountEl!}
              data-testid="edit-cm"
              style={{
                position: 'absolute',
                inset: 0,
                display: activeTab()?.mode === 'wysiwyg' ? 'none' : 'block',
                // What Ctrl+± / Ctrl+wheel move; see the .cm-scroller
                // rule in baseExtensions.
                '--wash-edit-font-size': `${fontSize()}px`,
              }}
            />
            <For each={wysiwygTabIDs()}>
              {(id) => (
                <div
                  ref={(el) => mountWysiwyg(id, el)}
                  data-testid={`edit-wf-host-${id}`}
                  data-tab-id={id}
                  style={{
                    position: 'absolute',
                    inset: 0,
                    display: activeID() === id ? 'block' : 'none',
                    overflow: 'hidden',
                  }}
                />
              )}
            </For>
            <Show when={wysFindOpen() && activeTab()?.mode === 'wysiwyg'}>
              <WysFindBar
                handle={() => wysHandles.get(activeID())}
                onClose={closeWysFind}
              />
            </Show>
            <Show when={!activeTab() || activeTab()?.blocked}>
              <div data-testid="edit-placeholder" style={placeholderOverlayStyle}>
                <Show when={!activeTab()}>
                  Pick a file from the sidebar, or Ctrl+N for an empty buffer.
                </Show>
                <Show when={activeTab()?.blocked}>
                  <div>{activeTab()?.path}</div>
                  <div data-testid="edit-placeholder-reason" style={{ color: tokens.fgDim, 'margin-top': '6px' }}>
                    {readOnlyText(activeTab()!)}
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
        <Show when={agentMenu()}>
          {(at) => (
            <Menu x={at().x} y={at().y} onDismiss={() => setAgentMenu(null)} data-testid="edit-agent-menu">
              <Show
                when={agentAdapters().length > 0}
                fallback={<MenuItem label="No agents installed" disabled onClick={() => {}} />}
              >
                <For each={agentAdapters()}>
                  {(a) => (
                    <MenuItem
                      label={a.name || a.id}
                      data-testid={`edit-agent-start-${a.id}`}
                      onClick={() => { setAgentMenu(null); openAgentTab(a.id); }}
                    />
                  )}
                </For>
              </Show>
            </Menu>
          )}
        </Show>
        <div data-testid="edit-term-pane" style={termPaneStyle}>
          {/* tab bar */}
          <div data-testid="edit-term-tabs" style={termTabBarStyle}>
            <For each={termTabs()}>
              {(t) => {
                const isActive = () => activeTermID() === t.id;
                return (
                  <Tab
                    data-testid={`edit-term-tab-${t.id}`}
                    active={isActive()}
                    mono
                    onClick={() => setActiveTermID(t.id)}
                    onClose={() => closeTerm(t.id)}
                    closeTestId={`edit-term-tab-close-${t.id}`}
                    closeTitle="Close terminal"
                  >
                    {t.title}
                  </Tab>
                );
              }}
            </For>
            <button
              data-wash-hit
              type="button"
              data-testid="edit-term-new"
              onClick={openNewTerm}
              style={termNewBtnStyle}
              title="New Terminal (Ctrl+Shift+`)"
            >
              +
            </button>
            {/* Agents open in the same strip as terminals, deliberately:
                both are a process working on your behalf that you watch
                and interrupt. */}
            <button
              data-wash-hit
              type="button"
              data-testid="edit-agent-new"
              onClick={(ev) => {
                const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
                setAgentMenu({ x: r.left, y: r.bottom });
              }}
              style={termNewBtnStyle}
              title="New agent session in this folder"
            >
              ✦
            </button>
          </div>
          {/* terminal hosts — one DOM element per channel, only
              the active one is visible. We keep them mounted (not
              just rendered behind a Show) so xterm doesn't lose
              its host element on tab switch. */}
          <div style={termBodyStyle}>
            <For each={termTabs()}>
              {(t) => {
                let hostEl: HTMLDivElement | undefined;
                return (
                  <div
                    data-testid={`edit-term-host-${t.id}`}
                    style={{
                      position: 'absolute',
                      inset: 0,
                      display: activeTermID() === t.id ? 'block' : 'none',
                    }}
                    ref={(el) => { hostEl = el; }}
                  >
                    <Show when={t.kind === 'agent'}>
                      {/* The transcript, in the pane the terminals live in.
                          onOpenTool is why hosting an agent HERE is worth
                          doing at all: a tool row naming a file opens that
                          file in the buffer above, which the standalone
                          Agent app cannot do. */}
                      <AgentSession
                        insertDraft={() => agentDrafts()[t.id]}
                        events={() => agentEvents()[t.agentKey ?? ''] ?? []}
                        asks={() => agentAsksFor(t.agentKey ?? '')}
                        status={() => agentStatusFor(t.agentKey ?? '')}
                        placeholder={t.agentKey ? 'Ask the agent…' : 'starting…'}
                        onSend={t.agentKey ? ((text) => send({ kind: 'agent.prompt', key: t.agentKey, text })) : undefined}
                        onAnswer={(id, decision, rule) => send({ kind: 'agent.answer', key: t.agentKey, id, decision, rule: rule ?? '' })}
                        onCancel={() => send({ kind: 'agent.cancel', key: t.agentKey })}
                        onSetMode={(mode) => send({ kind: 'agent.set_mode', key: t.agentKey, mode })}
                        onSetConfig={(id, value) => send({ kind: 'agent.set_config', key: t.agentKey, id, value })}
                        onOpenTool={(e) => {
                          const path = (e.title ?? '').trim();
                          if (path) void openInTab(path.startsWith('/') ? path : joinPath(root(), path));
                        }}
                      />
                    </Show>
                    <Show when={t.kind !== 'agent' && t.channelID > 0}>
                      <Terminal
                        channelId={t.channelID}
                        origin={props.origin}
                        onReady={(api) => {
                          termAPIs.set(t.channelID, api);
                          if (activeTermID() === t.id) api.focus();
                          if (hostEl) (hostEl as unknown as { __washTerm: unknown }).__washTerm = api.xterm();
                        }}
                        onResize={(cols, rows) => {
                          send({ kind: 'term.resize', channel_id: t.channelID, cols, rows });
                        }}
                      />
                    </Show>
                  </div>
                );
              }}
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
          <Show when={activeTab()!.missing}>
            <span data-testid="edit-status-missing" style={{ 'margin-left': '8px', color: tokens.fgDanger }}>· deleted on disk — Ctrl+S saves as…</span>
          </Show>
          <Show when={activeTab()!.readOnlyFile && !activeTab()!.blocked}>
            <span
              data-testid="edit-status-lock"
              title="Read-only file — Ctrl+S offers Save As"
              style={{ 'margin-left': '8px', display: 'inline-flex', 'align-items': 'center', gap: '3px', color: tokens.fgDim }}
            >
              <Lock size={11} /> read-only
            </span>
          </Show>
          <Show when={activeTab()!.blocked}>
            <span data-testid="edit-status-readonly" style={{ 'margin-left': '8px', color: tokens.fgDim }}>· read-only: {readOnlyText(activeTab()!)}</span>
          </Show>
        </Show>
        <Show when={statusError()}>
          <span data-testid="edit-status-error" style={{ 'margin-left': '8px', color: tokens.fgDanger }}>· {statusError()}</span>
        </Show>
        {/* The right-hand cells: everything about the buffer you would
            otherwise have to go looking in a menu for, and each of them
            the way to change it. */}
        <div style={statusCellsStyle}>
          <Show when={activeTab() && !activeTab()!.blocked && activeTab()!.mode === 'source'}>
            <StatusCell
              testid="edit-status-cursor"
              title="Go to line (Ctrl+G)"
              label={`Ln ${cursorPos().line}, Col ${cursorPos().col}`}
              onClick={cmdGotoLine}
            />
            <StatusCell
              testid="edit-status-indent"
              title="Select indentation"
              label={indentLabel(activeIndent())}
              onClick={(ev) => openStatusMenu('indent', ev)}
            />
            <StatusCell
              testid="edit-status-eol"
              title="Select line ending"
              label={activeTab()!.eol === 'crlf' ? 'CRLF' : 'LF'}
              onClick={(ev) => openStatusMenu('eol', ev)}
            />
          </Show>
          <Show when={activeTab()}>
            <StatusCell
              testid="edit-status-lang"
              title="Select language"
              label={LANGS_BY_KEY[currentLang()]?.label ?? 'Plain'}
              onClick={(ev) => openStatusMenu('syntax', ev)}
            />
          </Show>
        </div>
      </StatusBar>

      <Show when={qoOpen()}>
        <QuickOpen
          query={qoQuery()}
          onQuery={(v) => { setQoQuery(v); setQoSelected(0); }}
          rows={qoResults()}
          selected={qoSelected()}
          onHover={setQoSelected}
          onPick={(i) => qoPick(qoResults()[i])}
          onKey={onQuickOpenKey}
          onClose={closeQuickOpen}
          loading={qoFiles() === null && qoQuery().trim() !== ''}
          status={qoFiles() === null
            ? (qoQuery().trim() ? 'listing files…' : 'recent files — type to search the tree')
            : `${qoFiles()!.length}${qoTruncated() ? '+' : ''} files under ${qoRel(root()) || root()}`}
        />
      </Show>

      <FilePicker
        open={picker() !== null}
        mode={picker()?.mode ?? 'open'}
        host={props.host}
        hostInstanceID={props.instance}
        defaultName={picker()?.mode === 'save' ? (picker() as { suggestedName: string }).suggestedName : undefined}
        start={picker()?.mode === 'save' ? (picker() as { start?: string }).start : undefined}
        onConfirm={(p) => void pickerConfirm(p)}
        onCancel={() => { setPicker(null); saveAllQueue = []; }}
        data-testid="edit-picker"
      />

      {/* Changed-on-disk prompt — only raised for tabs with unsaved
          edits; clean tabs reload silently. */}
      <Show when={reloadPrompt()}>
        <ConfirmDialog
          title="File changed on disk"
          confirmLabel="Reload"
          cancelLabel="Keep editing"
          altLabel="Show diff"
          danger
          onConfirm={confirmReload}
          onCancel={dismissReload}
          onAlt={() => void showReloadDiff()}
          data-testid="edit-reload-dialog"
          confirmTestid="edit-reload-confirm"
          cancelTestid="edit-reload-keep"
          altTestid="edit-reload-diff"
        >
          <div style={{ color: tokens.fgDim, 'max-width': '380px', 'line-height': '1.4' }}>
            <strong style={{ color: tokens.fg }}>{reloadPrompt()!.displayName}</strong>{' '}
            was modified outside the editor. Reloading discards your unsaved
            changes; keep editing to preserve them; Show diff keeps them and
            opens them side by side with the disk version.
          </div>
        </ConfirmDialog>
      </Show>

      {/* Save / Don't save / Cancel — raised for a dirty tab close and for
          the window close the BE relays as close_blocked. */}
      <Show when={pendingClose()}>
        {(p) => (
          <ConfirmDialog
            title={p().scope === 'window' ? 'Close the editor?' : p().scope === 'tabs' ? (p() as { title: string }).title : 'Close this tab?'}
            confirmLabel="Save"
            altLabel="Don't save"
            altDanger
            cancelLabel="Cancel"
            onConfirm={() => void saveAndClose()}
            onAlt={discardAndClose}
            onCancel={() => setPendingClose(null)}
            data-testid="edit-close-dialog"
            confirmTestid="edit-close-save"
            altTestid="edit-close-discard"
            cancelTestid="edit-close-cancel"
          >
            <div style={{ color: tokens.fgDim, 'max-width': '380px', 'line-height': '1.4' }}>
              Unsaved changes in:
              <ul style={{ margin: '6px 0 0', padding: '0 0 0 18px', color: tokens.fg }}>
                <For each={pendingCloseTargets()}>
                  {(t) => <li data-testid="edit-close-dialog-item">{t.displayName}</li>}
                </For>
              </ul>
            </div>
          </ConfirmDialog>
        )}
      </Show>

      {/* Revert with unsaved edits: the one place the editor throws work
          away on purpose, so it asks. */}
      <Show when={revertPrompt()}>
        <ConfirmDialog
          title="Revert to the saved version?"
          confirmLabel="Revert"
          cancelLabel="Cancel"
          danger
          onConfirm={confirmRevert}
          onCancel={() => setRevertPrompt(null)}
          data-testid="edit-revert-dialog"
          confirmTestid="edit-revert-confirm"
          cancelTestid="edit-revert-cancel"
        >
          <div style={{ color: tokens.fgDim, 'max-width': '380px', 'line-height': '1.4' }}>
            <strong style={{ color: tokens.fg }}>{revertPrompt()!.displayName}</strong>{' '}
            has unsaved changes. Reverting reloads the file from disk and discards them.
          </div>
        </ConfirmDialog>
      </Show>

      {/* right-click context menu — fires on row right-click. */}
      {/* editor text-area context menu: wash clipboard actions. */}
      <Show when={textCtxMenu()}>
        <Menu
          x={textCtxMenu()!.x}
          y={textCtxMenu()!.y}
          onDismiss={() => setTextCtxMenu(null)}
          data-testid="edit-text-ctx-menu"
        >
          <MenuItem label="Cut" onClick={() => { setTextCtxMenu(null); cmdCut(); }} data-testid="edit-text-ctx-cut" />
          <MenuItem label="Copy" onClick={() => { setTextCtxMenu(null); cmdCopy(); }} data-testid="edit-text-ctx-copy" />
          <MenuItem label="Paste" onClick={() => { setTextCtxMenu(null); cmdPaste(); }} data-testid="edit-text-ctx-paste" />
          <MenuSeparator />
          <MenuItem
            label="Send to Agent"
            trailing={<kbd style={kbdStyle}>Ctrl+Shift+Enter</kbd>}
            onClick={() => { setTextCtxMenu(null); sendToAgent(); }}
            data-testid="edit-text-ctx-send-agent"
          />
        </Menu>
      </Show>

      <Show when={ctxMenu()}>
        <Menu
          x={ctxMenu()!.x}
          y={ctxMenu()!.y}
          onDismiss={closeCtxMenu}
          data-testid="edit-ctx-menu"
        >
          <MenuItem
            label={isDirLike(ctxMenu()!.entry) ? 'Expand' : 'Open'}
            onClick={() => {
              const c = ctxMenu()!;
              closeCtxMenu();
              onRowDblClick({ entry: c.entry, path: c.path });
            }}
            data-testid="edit-ctx-open"
          />
          <MenuItem
            label="Open terminal window in this folder"
            icon={<TerminalIcon size={14} />}
            title={contextFolder()}
            onClick={() => openContextFolderIn('com.wash.term')}
            data-testid="edit-ctx-open-terminal"
          />
          <MenuItem
            label="Open file manager in this folder"
            icon={<FolderIcon size={14} />}
            title={contextFolder()}
            onClick={() => openContextFolderIn('com.wash.fm')}
            data-testid="edit-ctx-open-file-manager"
          />
          <MenuSeparator />
          <MenuItem
            label="Copy path"
            onClick={() => {
              const c = ctxMenu()!;
              closeCtxMenu();
              ctxCopyPath(c.path);
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

// WysiwygToolbar is the formatting strip above the TipTap editor.
// Each button is a TipTap command on the active tab's handle; the
// `tab` + `handle` accessors are reactive so a tab-switch + a
// late-arriving handle (mount ref callback runs the tick after the
// effect) both keep the toolbar pointed at the right editor.
//
// Buttons are dispatch-only — they don't reflect pressed state for
// the cursor's current marks. Adding that needs an editor.on
// ('selectionUpdate') subscription plus a tracked signal; deferred
// until users actually miss the affordance.
const WysiwygToolbar: Component<{ tab: () => Tab; handle: () => WysiwygHandle | undefined }> = (props) => {
  const cmd = (fn: (h: WysiwygHandle) => void) => () => {
    const h = props.handle();
    if (h) fn(h);
  };
  const insertLink = () => {
    const h = props.handle();
    if (!h) return;
    const prev = h.editor.getAttributes('link').href as string | undefined;
    const url = window.prompt('Link URL', prev || 'https://');
    if (url === null) return;
    if (url === '') {
      h.editor.chain().focus().unsetLink().run();
      return;
    }
    h.editor.chain().focus().extendMarkRange('link').setLink({ href: url }).run();
  };
  const insertImage = () => {
    const h = props.handle();
    if (!h) return;
    const src = window.prompt('Image URL or path', '');
    if (!src) return;
    h.editor.chain().focus().setImage({ src }).run();
  };
  const insertTable = () => {
    const h = props.handle();
    if (!h) return;
    h.editor.chain().focus().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run();
  };
  const btn = (id: string, title: string, icon: () => JSX.Element, onClick: () => void) => (
    <Button
      variant="icon"
      data-testid={`edit-wf-${id}`}
      title={title}
      onClick={onClick}
      onMouseDown={(e) => e.preventDefault()}
      style={{ padding: 0, height: '22px' }}
    >
      {icon()}
    </Button>
  );
  return (
    <div data-testid="edit-wf-toolbar" style={toolbarStyle}>
      {btn('bold', 'Bold (Ctrl+B)', () => <Bold size={14} />, cmd((h) => { h.editor.chain().focus().toggleBold().run(); }))}
      {btn('italic', 'Italic (Ctrl+I)', () => <Italic size={14} />, cmd((h) => { h.editor.chain().focus().toggleItalic().run(); }))}
      {btn('strike', 'Strikethrough', () => <Strikethrough size={14} />, cmd((h) => { h.editor.chain().focus().toggleStrike().run(); }))}
      {btn('code', 'Inline code', () => <CodeIcon size={14} />, cmd((h) => { h.editor.chain().focus().toggleCode().run(); }))}
      <div style={toolbarSepStyle} />
      {btn('h1', 'Heading 1', () => <Heading1 size={14} />, cmd((h) => { h.editor.chain().focus().toggleHeading({ level: 1 }).run(); }))}
      {btn('h2', 'Heading 2', () => <Heading2 size={14} />, cmd((h) => { h.editor.chain().focus().toggleHeading({ level: 2 }).run(); }))}
      {btn('h3', 'Heading 3', () => <Heading3 size={14} />, cmd((h) => { h.editor.chain().focus().toggleHeading({ level: 3 }).run(); }))}
      <div style={toolbarSepStyle} />
      {btn('bullet', 'Bullet list', () => <List size={14} />, cmd((h) => { h.editor.chain().focus().toggleBulletList().run(); }))}
      {btn('ordered', 'Ordered list', () => <ListOrdered size={14} />, cmd((h) => { h.editor.chain().focus().toggleOrderedList().run(); }))}
      {btn('task', 'Task list', () => <ListTodo size={14} />, cmd((h) => { h.editor.chain().focus().toggleTaskList().run(); }))}
      <div style={toolbarSepStyle} />
      {btn('quote', 'Blockquote', () => <Quote size={14} />, cmd((h) => { h.editor.chain().focus().toggleBlockquote().run(); }))}
      {btn('codeblock', 'Code block', () => <CodeIcon size={14} />, cmd((h) => { h.editor.chain().focus().toggleCodeBlock().run(); }))}
      {btn('hr', 'Horizontal rule', () => <Minus size={14} />, cmd((h) => { h.editor.chain().focus().setHorizontalRule().run(); }))}
      <div style={toolbarSepStyle} />
      {btn('link', 'Insert / edit link', () => <LinkIcon size={14} />, insertLink)}
      {btn('image', 'Insert image', () => <ImageIcon size={14} />, insertImage)}
      {btn('table', 'Insert 3×3 table', () => <TableIcon size={14} />, insertTable)}
    </div>
  );
};

// WysFindBar is the find/replace overlay for WYSIWYG tabs — the
// TipTap counterpart of CodeMirror's search panel. It owns the query
// and replacement drafts; matching, highlighting, and replacement all
// live in the editor's search plugin behind WysiwygHandle.search.
//
// The `handle` accessor is reactive: switching to another wysiwyg tab
// while the bar is open re-runs the query against the new editor and
// clears the highlights off the old one.
const WysFindBar: Component<{
  handle: () => WysiwygHandle | undefined;
  onClose: () => void;
}> = (props) => {
  const [query, setQuery] = createSignal('');
  const [repl, setRepl] = createSignal('');
  const [hits, setHits] = createSignal<WysiwygSearchState>({ count: 0, current: -1 });
  let findInput!: HTMLInputElement;
  let boundHandle: WysiwygHandle | undefined;

  createEffect(() => {
    const h = props.handle();
    if (h === boundHandle) return;
    boundHandle?.search.clear();
    boundHandle = h;
    if (h && query()) setHits(h.search.set(query()));
  });
  // Live count: the plugin rescans on every doc change, so mirror its
  // state into the display after each transaction (typing in the
  // document with the bar open keeps "n/m" honest).
  createEffect(() => {
    const h = props.handle();
    if (!h) return;
    const refresh = () => setHits(h.search.state());
    h.editor.on('transaction', refresh);
    onCleanup(() => { h.editor.off('transaction', refresh); });
  });
  onMount(() => findInput.focus());
  // Unmount without onClose (tab closed, mode toggled) still drops
  // the highlights. search.clear() is destroy-safe and idempotent.
  onCleanup(() => boundHandle?.search.clear());

  const onQueryInput = (v: string) => {
    setQuery(v);
    const h = props.handle();
    if (h) setHits(h.search.set(v));
  };
  const nav = (dir: 'next' | 'prev') => {
    const h = props.handle();
    if (h) setHits(dir === 'next' ? h.search.next() : h.search.prev());
  };
  const doReplace = (all: boolean) => {
    const h = props.handle();
    if (h) setHits(all ? h.search.replaceAll(repl()) : h.search.replace(repl()));
  };
  // Enter cycles matches (Shift reverses); Escape closes. Handled on
  // the inputs with stopPropagation so the app-level Escape handler
  // doesn't double-fire.
  const onFindKey = (ev: KeyboardEvent) => {
    if (ev.key === 'Enter') { ev.preventDefault(); nav(ev.shiftKey ? 'prev' : 'next'); }
    if (ev.key === 'Escape') { ev.preventDefault(); ev.stopPropagation(); props.onClose(); }
  };
  const onReplKey = (ev: KeyboardEvent) => {
    if (ev.key === 'Enter') { ev.preventDefault(); doReplace(false); }
    if (ev.key === 'Escape') { ev.preventDefault(); ev.stopPropagation(); props.onClose(); }
  };

  return (
    <div data-testid="edit-wf-find" style={findBarStyle}>
      <div style={findRowStyle}>
        <Input
          ref={findInput}
          data-testid="edit-wf-find-input"
          placeholder="Find"
          value={query()}
          onInput={(ev) => onQueryInput(ev.currentTarget.value)}
          onKeyDown={onFindKey}
          style={{ padding: '0 6px', height: '22px', width: '160px', 'box-sizing': 'border-box', font: tokens.type.monoMd }}
        />
        <span data-testid="edit-wf-find-count" style={findCountStyle}>
          {query() ? `${hits().count ? hits().current + 1 : 0}/${hits().count}` : ''}
        </span>
        <Button variant="icon" data-testid="edit-wf-find-prev" onMouseDown={(e) => e.preventDefault()} title="Previous match (Shift+Enter)" onClick={() => nav('prev')} style={{ padding: 0, height: '22px' }}>
          <ChevronUp size={14} />
        </Button>
        <Button variant="icon" data-testid="edit-wf-find-next" onMouseDown={(e) => e.preventDefault()} title="Next match (Enter)" onClick={() => nav('next')} style={{ padding: 0, height: '22px' }}>
          <ChevronDown size={14} />
        </Button>
        <Button variant="icon" data-testid="edit-wf-find-close" onMouseDown={(e) => e.preventDefault()} title="Close (Escape)" onClick={props.onClose} style={{ padding: 0, height: '22px' }}>
          ×
        </Button>
      </div>
      <div style={findRowStyle}>
        <Input
          data-testid="edit-wf-replace-input"
          placeholder="Replace"
          value={repl()}
          onInput={(ev) => setRepl(ev.currentTarget.value)}
          onKeyDown={onReplKey}
          style={{ padding: '0 6px', height: '22px', width: '160px', 'box-sizing': 'border-box', font: tokens.type.monoMd }}
        />
        <button data-wash-hit type="button" data-testid="edit-wf-replace-one" onMouseDown={(e) => e.preventDefault()} title="Replace current match (Enter)" onClick={() => doReplace(false)} style={findButtonStyle}>
          Replace
        </button>
        <button data-wash-hit type="button" data-testid="edit-wf-replace-all" onMouseDown={(e) => e.preventDefault()} title="Replace all matches" onClick={() => doReplace(true)} style={findButtonStyle}>
          All
        </button>
      </div>
    </div>
  );
};

// QuickOpen is the Ctrl+P palette: one input, a ranked list under it.
// Presentation only — the query, ranking and the open live in App, so
// the keyboard contract (onKey) and the rows are the whole interface.
const QuickOpen: Component<{
  query: string;
  onQuery: (v: string) => void;
  rows: { abs: string; label: string }[];
  selected: number;
  onHover: (i: number) => void;
  onPick: (i: number) => void;
  onKey: (ev: KeyboardEvent) => void;
  onClose: () => void;
  loading: boolean;
  status: string;
}> = (props) => {
  let input!: HTMLInputElement;
  onMount(() => input.focus());
  return (
    <Overlay onDismiss={props.onClose} align="top" data-testid="edit-quick-open" innerStyle={{ padding: 0, 'min-width': '480px', 'max-width': '640px', width: '60%' }}>
      <Input
        ref={input}
        data-testid="edit-qo-input"
        placeholder="Open file by name…"
        value={props.query}
        onInput={(ev) => props.onQuery(ev.currentTarget.value)}
        onKeyDown={props.onKey}
        style={{ margin: '10px 12px 6px', padding: '4px 8px', height: '28px', 'box-sizing': 'border-box', font: tokens.type.monoMd }}
      />
      <div data-testid="edit-qo-list" style={{ 'max-height': '40vh', overflow: 'auto', padding: '0 0 6px' }}>
        <For each={props.rows}>
          {(row, i) => (
            <QuickOpenRow
              label={row.label}
              selected={props.selected === i()}
              onHover={() => props.onHover(i())}
              onPick={() => props.onPick(i())}
            />
          )}
        </For>
        <Show when={props.rows.length === 0}>
          <div data-testid="edit-qo-empty" style={{ padding: '6px 16px', color: tokens.fgDim, font: tokens.type.textMd }}>
            {props.loading ? 'listing files…' : props.query ? 'no matches' : 'no recent files yet'}
          </div>
        </Show>
      </div>
      <div data-testid="edit-qo-status" style={{ padding: '4px 12px 6px', 'border-top': `1px solid ${tokens.borderMenu}`, color: tokens.fgDim, font: tokens.type.textSm }}>
        {props.status}
      </div>
    </Overlay>
  );
};

const QuickOpenRow: Component<{ label: string; selected: boolean; onHover: () => void; onPick: () => void }> = (props) => {
  let el!: HTMLButtonElement;
  createEffect(() => {
    if (props.selected) el.scrollIntoView({ block: 'nearest' });
  });
  const dir = () => { const i = props.label.lastIndexOf('/'); return i < 0 ? '' : props.label.slice(0, i); };
  const base = () => { const i = props.label.lastIndexOf('/'); return i < 0 ? props.label : props.label.slice(i + 1); };
  return (
    <button
      type="button"
      ref={el!}
      data-wash-hit="subtle"
      data-testid={`edit-qo-item-${props.label}`}
      data-selected={props.selected ? 'true' : undefined}
      onMouseEnter={props.onHover}
      onMouseDown={(e) => e.preventDefault()}
      onClick={props.onPick}
      style={{
        display: 'flex',
        'align-items': 'baseline',
        gap: '8px',
        width: '100%',
        padding: '4px 16px',
        background: props.selected ? tokens.bgRowSelected : 'transparent',
        color: tokens.fg,
        border: 'none',
        'text-align': 'left',
        cursor: 'pointer',
        font: tokens.type.textMd,
      }}
    >
      <span style={{ 'white-space': 'nowrap' }}>{base()}</span>
      <span style={{ color: tokens.fgDim, font: tokens.type.textSm, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>{dir()}</span>
    </button>
  );
};

// StatusCell is one clickable cell on the right of the status bar:
// the cursor position, the indentation, the line ending, the language.
// Reads as the status text it replaces (no button chrome at rest) and
// takes its hover/press from the interaction layer.
const StatusCell: Component<{
  label: string;
  title: string;
  testid: string;
  onClick: (ev: MouseEvent) => void;
}> = (props) => (
  <button
    type="button"
    data-wash-hit
    data-testid={props.testid}
    title={props.title}
    onClick={(ev) => props.onClick(ev)}
    style={{
      background: 'transparent',
      border: 'none',
      color: tokens.fg,
      font: 'inherit',
      padding: '0 6px',
      height: '18px',
      'border-radius': `${tokens.radiusSm}`,
      'white-space': 'nowrap',
    }}
  >
    {props.label}
  </button>
);

const statusCellsStyle: JSX.CSSProperties = {
  'margin-left': 'auto',
  display: 'flex',
  'align-items': 'center',
  gap: '2px',
  'flex-shrink': 0,
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

// LANGS is the single source of truth for syntax highlighting.
// Every row carries its menu label, the CM6 extension factory, and
// the filename signals (extensions + exact basenames) that map a
// file to it. langForKey / langKeyForPath / langChoices all derive
// from this table — adding a new syntax is one row.
//
// `lang` returns a CM6 Extension. Modern packs (lang-*) are called
// directly; legacy parsers go through StreamLanguage.define here so
// callers stay uniform.
interface LangDef {
  key: string;
  label: string;
  /** File extensions (without leading dot), lowercase. */
  extensions?: string[];
  /** Exact basenames matched case-insensitively (e.g. "dockerfile"). */
  filenames?: string[];
  lang: () => Extension | Extension[];
}

const LANGS: LangDef[] = [
  { key: 'plain', label: 'Plain Text', lang: () => [] },
  { key: 'javascript', label: 'JavaScript', extensions: ['js', 'mjs', 'cjs'], lang: () => javascript() },
  { key: 'jsx', label: 'JavaScript (JSX)', extensions: ['jsx'], lang: () => javascript({ jsx: true }) },
  { key: 'typescript', label: 'TypeScript', extensions: ['ts'], lang: () => javascript({ typescript: true }) },
  { key: 'tsx', label: 'TypeScript (TSX)', extensions: ['tsx'], lang: () => javascript({ typescript: true, jsx: true }) },
  { key: 'json', label: 'JSON', extensions: ['json'], lang: () => json() },
  { key: 'markdown', label: 'Markdown', extensions: ['md', 'markdown'], lang: () => markdown() },
  { key: 'html', label: 'HTML', extensions: ['html', 'htm'], lang: () => html() },
  { key: 'css', label: 'CSS', extensions: ['css'], lang: () => css() },
  { key: 'sass', label: 'Sass / SCSS', extensions: ['sass', 'scss'], lang: () => sass() },
  { key: 'xml', label: 'XML', extensions: ['xml', 'svg'], lang: () => xml() },
  { key: 'yaml', label: 'YAML', extensions: ['yaml', 'yml'], lang: () => yaml() },
  { key: 'toml', label: 'TOML', extensions: ['toml'], lang: () => StreamLanguage.define(toml) },
  { key: 'properties', label: 'INI / Properties', extensions: ['ini', 'conf', 'cfg', 'properties'], lang: () => StreamLanguage.define(properties) },
  { key: 'python', label: 'Python', extensions: ['py', 'pyw'], lang: () => python() },
  { key: 'go', label: 'Go', extensions: ['go'], lang: () => go() },
  { key: 'rust', label: 'Rust', extensions: ['rs'], lang: () => rust() },
  { key: 'cpp', label: 'C / C++', extensions: ['c', 'h', 'cc', 'cpp', 'cxx', 'hpp', 'hh', 'hxx'], lang: () => cpp() },
  { key: 'java', label: 'Java', extensions: ['java'], lang: () => java() },
  { key: 'php', label: 'PHP', extensions: ['php', 'phtml'], lang: () => php() },
  { key: 'ruby', label: 'Ruby', extensions: ['rb', 'rake', 'gemspec'], lang: () => StreamLanguage.define(ruby) },
  { key: 'lua', label: 'Lua', extensions: ['lua'], lang: () => StreamLanguage.define(lua) },
  { key: 'perl', label: 'Perl', extensions: ['pl', 'pm'], lang: () => StreamLanguage.define(perl) },
  { key: 'sql', label: 'SQL', extensions: ['sql'], lang: () => sql() },
  {
    key: 'shell',
    label: 'Shell / Bash',
    extensions: ['sh', 'bash', 'zsh', 'ksh'],
    filenames: ['.bashrc', '.bash_profile', '.profile', '.zshrc'],
    lang: () => StreamLanguage.define(shell),
  },
  { key: 'dockerfile', label: 'Dockerfile', filenames: ['dockerfile'], lang: () => StreamLanguage.define(dockerFile) },
  {
    key: 'cmake',
    label: 'CMake / Makefile',
    filenames: ['makefile', 'gnumakefile', 'cmakelists.txt'],
    lang: () => StreamLanguage.define(cmake),
  },
  { key: 'nginx', label: 'nginx', filenames: ['nginx.conf'], lang: () => StreamLanguage.define(nginx) },
  { key: 'diff', label: 'Diff / Patch', extensions: ['diff', 'patch'], lang: () => StreamLanguage.define(diff) },
];

// Index for O(1) lookup by key in langForKey + the menu.
const LANGS_BY_KEY: Record<string, LangDef> = Object.fromEntries(LANGS.map((l) => [l.key, l]));

// TAB_DRAG_MIME marks a drag that reorders the strip. Distinct from the
// fs-client DRAG_MIME on purpose: a tab is not a file to move or open.
const TAB_DRAG_MIME = 'application/x-wash-edit-tab';

type MenuID = 'file' | 'edit' | 'view' | 'syntax' | 'terminal';

const MenuBarButton: Component<{
  id: MenuID;
  label: string;
  active: boolean;
  onClick: (id: MenuID, ev: MouseEvent) => void;
}> = (props) => {
  return (
    <button
      data-wash-hit
      type="button"
      data-testid={`edit-menubar-${props.id}`}
      onClick={(ev) => props.onClick(props.id, ev)}
      style={menuBarButtonStyle(props.active)}
    >
      {props.label}
    </button>
  );
};

// joinPath / baseName / parentPath now come from @wash/fs-client (imported
// at the top) — identical bodies, removed from here.

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
    font: tokens.type.textMd,
  };
}

const kbdStyle: JSX.CSSProperties = {
  font: tokens.type.monoSm,
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

// langHintStyle renders the file extensions / basenames next to
// each Syntax menu row. Muted + monospace so they read as data
// signals rather than competing with the language label.
const langHintStyle: JSX.CSSProperties = {
  color: tokens.fgDim,
  font: tokens.type.monoSm,
};

const termPaneStyle: JSX.CSSProperties = {
  background: tokens.bgCanvas,
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
  background: tokens.bgCanvas,
};

const termNewBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fgMuted,
  border: 'none',
  padding: '0 10px',
  height: '26px',
  cursor: 'pointer',
  font: tokens.type.textMd,
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
  font: tokens.type.textSm,
  color: tokens.fgMuted,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
};

// The sidebar list IS the shared <FileTree>'s scroll container; this is its
// outer style (flex child of the sidebar). Row look + indentation now live in
// FileTree (@wash/ui).
const sidebarListStyle: JSX.CSSProperties = {
  flex: 1,
  overflow: 'auto',
  padding: '4px 0',
};

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

const toolbarStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  background: tokens.bgMenu,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  padding: '3px 6px',
  gap: '2px',
  'flex-shrink': 0,
  'min-height': '26px',
};

const toolbarSepStyle: JSX.CSSProperties = {
  width: '1px',
  height: '16px',
  background: tokens.borderMenu,
  margin: '0 4px',
};

// Find/replace bar for WYSIWYG tabs — floats top-right over the
// document like VSCode's widget; inputs/buttons sized to match the
// CM search panel's .cm-textfield/.cm-button theme above.
const findBarStyle: JSX.CSSProperties = {
  position: 'absolute',
  top: '8px',
  right: '24px',
  'z-index': 5,
  display: 'flex',
  'flex-direction': 'column',
  gap: '4px',
  padding: '6px 8px',
  background: tokens.bgMenu,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}`,
  'box-shadow': tokens.shadowMenu,
};

const findRowStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '4px',
};

const findCountStyle: JSX.CSSProperties = {
  color: tokens.fgDim,
  font: tokens.type.textMd,
  'min-width': '34px',
  'text-align': 'right',
  'white-space': 'nowrap',
};

const findButtonStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '0 10px',
  height: '22px',
  'box-sizing': 'border-box',
  display: 'inline-flex',
  'align-items': 'center',
  'justify-content': 'center',
  cursor: 'pointer',
  font: tokens.type.textMd,
  'line-height': 1,
};

const tabBarStyle: JSX.CSSProperties = {
  display: 'flex',
  overflow: 'auto',
  background: tokens.bgMenu,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  'min-height': '26px',
  'flex-shrink': 0,
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
  font: tokens.type.monoMd,
  'pointer-events': 'none',
};

// ---- custom element ----

// Grid: menubar (auto) | body (1fr) | status bar (22px). Menus now
// portal to document.body with position:fixed, so the host no
// longer needs to be a positioning ancestor for the menubar
// dropdowns — `position: relative` here is just defense in depth
// for any in-app absolutely-positioned overlays still inside the
// slot (rename inputs, the autocomplete dropdown, etc.).
defineWashApp('wash-app-edit', (props) => <App {...props} />, {
  // Status-bar row pinned to 22px — same as wash-fm — so the chrome
  // is consistent across apps. `auto` collapses to the StatusBar's
  // intrinsic height (~13px), which read as visibly thinner than fm.
  style: `display:grid;grid-template-rows:auto 1fr 22px;height:100%;background:${tokens.bgWindow};color:${tokens.fg};overflow:hidden;position:relative`,
});
