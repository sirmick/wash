// wash-app-fm: single-tree + preview file manager.
// - Left pane: hierarchical tree, rooted at /, auto-expanded to $HOME
//   on first list_ok. Click triangle to toggle; click row name to
//   select. Selecting a folder also auto-expands it.
// - Right pane: file preview (text or "binary").
// - Below preview: collapsible info section (perms, size, mtime,
//   symlink target if relevant).
// - Toolbar: Home, Back, Up, editable path (Enter to navigate),
//   Reload, New file/folder, Upload files/folder, Sort dropdown.
// - Right-click on a row: Open · Copy path · Show info.
// - Mutations: rename/delete/create/symlink + drag-move/copy.
// - Upload: OS files in via the toolbar pickers or an external
//   drag-drop (recursive for folders); bytes stream over the bus
//   (be/upload.go), progress shows in the wash-bulk sidebar widget.
//
// Solid drives the rendering — state mutations automatically re-run
// just the views that read them. No more "I changed a field but
// forgot to re-render" bugs.

import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount, untrack } from 'solid-js';
import { createStore, produce } from 'solid-js/store';
import type { Component, JSX } from 'solid-js';
import { ConfirmDialog, Menu, MenuItem, MenuSeparator, Overlay, Splitter, StatusBar, defineWashApp, tokens } from '@wash/ui';
import {
  baseName, formatDate, humanSize, joinPath, octalPerm, parentPath, ancestorChain,
  createBus,
  createWatch,
  DRAG_MIME, dragPayload, dropEffectFor, hasWashDrag, readDragPaths,
  flattenTree,
  withReplacePrompt as runReplaceFlow,
  entriesFromDataTransfer, entriesFromFileList, planUpload,
  readBlobChunks, encodeRecordHeader, uploadEndMarker,
  type UploadItem,
} from '@wash/fs-client';
import {
  type NavHistory, emptyHistory, initAt, pushPath, back, forward, at,
} from './nav-history.ts';
import {
  type ClipboardState, parseClipboardState, planPaste,
} from './clipboard.ts';
import { nextSelection } from './selection.ts';
import {
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  ArrowUpDown,
  Check,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  File as FileIcon,
  FilePlus,
  Folder as FolderIcon,
  FolderPlus,
  Home as HomeIcon,
  Link2,
  PanelRightClose,
  PanelRightOpen,
  Pencil,
  RotateCw,
  Square,
  Trash2,
  Upload,
  FolderUp,
} from 'lucide-solid';

interface PersistedState {
  path?: string;
  expanded?: string[];
  sort_key?: SortKey;
  sort_desc?: boolean;
  show_hidden?: boolean;
  info_open?: boolean;
  // preview_w: fixed pixel width of the right preview/info dock.
  // preview_open: whether the dock is shown at all. (Replaces the old
  // split_pct percentage model — see the previewW/previewOpen signals.)
  preview_w?: number;
  preview_open?: boolean;
}

interface Entry {
  name: string;
  type: 'dir' | 'file' | 'symlink' | 'other';
  size: number;
  mod_unix: number;
  created_unix: number;
  perm: string;
  mode: number;
  uid: number;
  gid: number;
  owner?: string;
  group?: string;
  link_to?: string;
  link_err?: string;
}

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

// One flattened tree row, as produced by flattenTree() and rendered by
// the <For> in the body. Kept as a named type so the identity-stabilising
// memo and the raw memo share a signature.
type VisibleRow = { entry: Entry; path: string; depth: number; childCount?: number };

type SortKey = 'name' | 'mtime' | 'ctime' | 'size' | 'type';

type MenuState =
  | { kind: 'sort'; left: number; top: number }
  | { kind: 'context'; left: number; top: number; entry: Entry; path: string }
  | null;

const HOME_FALLBACK = '/';

// Upload send-buffer backpressure thresholds. writeRaw queues bytes
// into the single shell WebSocket's send buffer; awaitUploadDrain
// pauses the producer once more than UPLOAD_SEND_HWM is buffered and
// resumes once it drains below UPLOAD_SEND_LWM. Keeping the buffer
// small is what lets a sidebar cancel (an interactive control frame)
// reach the BE without waiting behind the whole file's bytes.
const UPLOAD_SEND_HWM = 1 << 20; // 1 MiB
const UPLOAD_SEND_LWM = 256 * 1024; // 256 KiB

// How long to wait for the BE's terminal upload_done AFTER we've sent the
// end marker (or a cancel closed the channel). This bounds only the
// finalize step — NOT the byte stream, which for a large/slow OS-folder
// drop can legitimately run for minutes. A flat timeout spanning the
// whole transfer would falsely declare a slow-but-healthy upload "done"
// mid-stream.
const UPLOAD_FINALIZE_MS = 30_000;

// How often the streaming loop hands the event loop a real macrotask so
// incoming control messages (a sidebar cancel) and user input get
// processed mid-stream. awaitUploadDrain only yields a macrotask when the
// send buffer FILLS — for a bulk upload of many small files it never
// does, so without a periodic yield the producer loop runs microtask-only
// and starves the WS message pump (cancel unseen) and the UI (the cancel
// button can't even be clicked) until the whole job is queued. Time-
// sliced so a huge file count doesn't pay a macrotask per file.
const UPLOAD_YIELD_MS = 50;

// awaitUploadDrain blocks until the shell socket's send buffer falls
// below the low-water mark (or the upload is cancelled). Returns
// immediately when the buffer is already under the high-water mark, so
// it's cheap to call after every chunk. Transports without a
// bufferedAmount (virtio) report 0 and never block.
async function awaitUploadDrain(origin: string, cancelled: () => boolean): Promise<void> {
  if (window.wash.rawBufferedAmountFor(origin) < UPLOAD_SEND_HWM) return;
  while (window.wash.rawBufferedAmountFor(origin) > UPLOAD_SEND_LWM && !cancelled()) {
    await new Promise((r) => setTimeout(r, 15));
  }
}

const App: Component<{ instance: string; host: HTMLElement; origin: string }> = (props) => {
  // ---- reactive state ----
  const [path, setPath] = createSignal('');
  const [selectedEntry, setSelectedEntry] = createSignal<Entry | null>(null);
  const [listings, setListings] = createStore<Record<string, Entry[]>>({});
  const [expanded, setExpanded] = createStore<Record<string, true>>({});
  // Back/forward history. The push/back/forward index arithmetic lives
  // in ./nav-history.ts (unit-tested); this signal just holds the state
  // and the handlers below apply the reducer's results.
  const [navHistory, setNavHistory] = createSignal<NavHistory>(emptyHistory());
  const [sortKey, setSortKey] = createSignal<SortKey>('name');
  const [sortDesc, setSortDesc] = createSignal(false);
  const [showHidden, setShowHidden] = createSignal(false);
  const [infoOpen, setInfoOpen] = createSignal(false);
  // The preview/info dock is a FIXED-WIDTH right column (previewW px),
  // not a percentage — so the tree absorbs every extra pixel as the
  // window grows and a wide window no longer starves filenames.
  // previewOpen toggles the whole dock (toolbar button + Ctrl/Cmd+I).
  // Both persist.
  const [previewW, setPreviewW] = createSignal(PREVIEW_DEFAULT_W);
  const [previewOpen, setPreviewOpen] = createSignal(true);
  let bodyEl!: HTMLDivElement;
  // bodyW tracks the body grid's width (ResizeObserver in onMount) so we
  // can clamp the dock to leave the tree at least TREE_MIN_W and derive
  // the tree's own width for responsive column density.
  const [bodyW, setBodyW] = createSignal(0);
  // effPreviewW: the dock width actually applied — previewW, but never so
  // wide it pushes the tree below TREE_MIN_W on a small window.
  const effPreviewW = createMemo(() => {
    const bw = bodyW();
    if (bw <= 0) return previewW();
    return Math.min(previewW(), Math.max(PREVIEW_MIN_W, bw - TREE_MIN_W - SPLITTER_W));
  });
  // gridCols: body template. Dock hidden → the tree is the only column.
  const gridCols = createMemo(() =>
    previewOpen() ? `1fr ${SPLITTER_W}px ${effPreviewW()}px` : '1fr',
  );
  // treeW: width available to the tree, for the column-density breakpoints.
  const treeW = createMemo(() => {
    const bw = bodyW();
    if (bw <= 0) return 0;
    return previewOpen() ? Math.max(0, bw - effPreviewW() - SPLITTER_W) : bw;
  });
  // cols: which tree columns currently fit (see colsFor).
  const cols = createMemo<ColCfg>(() => colsFor(treeW()));
  // onSplitChange converts the Splitter's divider-position percent into a
  // dock pixel width (the dock is everything right of the divider) and
  // clamps it so neither pane starves.
  const onSplitChange = (pct: number) => {
    const bw = bodyW() || bodyEl?.clientWidth || 0;
    if (bw <= 0) return;
    const px = Math.round(bw * (1 - pct / 100));
    const max = Math.max(PREVIEW_MIN_W, bw - TREE_MIN_W - SPLITTER_W);
    setPreviewW(Math.max(PREVIEW_MIN_W, Math.min(px, max)));
  };
  const togglePreview = () => {
    setPreviewOpen(!previewOpen());
    persist();
  };
  const [home, setHome] = createSignal(HOME_FALLBACK);
  const [rootInitialized, setRootInitialized] = createSignal(false);
  const [pathInputValue, setPathInputValue] = createSignal('');
  const [previewContent, setPreviewContent] = createSignal<{ binary: boolean; size: number; text: string; truncated: boolean } | null>(null);
  // statusOverride is set by transient one-shot messages (drop, error,
  // clipboard feedback). While non-null it wins over the auto-derived
  // visible-entry count. Navigation / clicks clear it so the auto
  // status resumes. The `kind` drives styling — errors render red at
  // full opacity so they read as failure, not as casual chatter.
  type StatusOverride = { kind: 'error' | 'info'; text: string };
  const [statusOverride, setStatusOverrideRaw] = createSignal<StatusOverride | null>(null);
  // Default helper: setStatusOverride(string) is an error; nulls clear.
  // Most failure paths predate the kind distinction and stay one-arg.
  const setStatusOverride = (text: string | null) => {
    setStatusOverrideRaw(text == null ? null : { kind: 'error', text });
  };
  const setStatusInfo = (text: string) => {
    setStatusOverrideRaw({ kind: 'info', text });
  };
  const [menu, setMenu] = createSignal<MenuState>(null);

  // Autocomplete
  const [completeMatches, setCompleteMatches] = createSignal<string[]>([]);
  const [completeIdx, setCompleteIdx] = createSignal(-1);
  const [completeOpen, setCompleteOpen] = createSignal(false);

  // Mutation state. Each represents a transient FE-only UI mode:
  //   renaming  — a row is showing an inline editable name input
  //   pendingNew — a synthetic row at the top of `parent` is awaiting
  //                the user's name input for create_file / create_dir
  //   confirmDelete — modal overlay asking the user to confirm
  // Each clears on commit / cancel / Escape.
  const [renaming, setRenaming] = createSignal<{ path: string; draft: string } | null>(null);
  const [pendingNew, setPendingNew] = createSignal<{ parent: string; kind: 'file' | 'folder'; draft: string } | null>(null);
  const [confirmDelete, setConfirmDelete] = createSignal<{ path: string | string[]; name: string } | null>(null);
  // replaceConfirm holds the in-flight Replace prompt. The
  // resolver returns whether the user clicked Replace (true) or
  // Cancel (false), unblocking the pending withReplacePrompt
  // continuation. Only one prompt is active at a time.
  const [replaceConfirm, setReplaceConfirm] = createSignal<{
    dst: string;
    entry: Entry | null;
    resolve: (replace: boolean) => void;
  } | null>(null);

  // Multi-selection state. `selection` is the set of paths the user
  // has selected via plain-click / Ctrl-click / Shift-click; actions
  // (Delete, future Copy/Move) operate on this set when its size
  // exceeds 1. `selectionAnchor` is the most-recently-clicked path
  // used as one end of a Shift-click range.
  const [selection, setSelection] = createSignal<Set<string>>(new Set());
  let selectionAnchor: string | null = null;

  // All selection writes route through applySelection so each carries a
  // `reason` label. The label is a breadcrumb the selection invariant
  // (checkSelectionInvariant, defined once visibleRows exists) reports
  // when it catches a divergence — it names the most recent explicit
  // selection write, which helps tell "this click left a ghost" apart
  // from "a background listing change orphaned an older selection".
  let lastSelectionWrite = 'init';
  const applySelection = (next: Set<string>, reason: string) => {
    lastSelectionWrite = reason;
    setSelection(next);
  };

  // Files clipboard — mirrors the router clipboard's
  // application/x-wash-paths slot, kept in sync by the BE pushing
  // clipboard_files_state on every change (and at fm startup). Two
  // fm windows therefore share one clipboard: cut in window A,
  // paste in window B works naturally.
  const [filesClipboard, setFilesClipboard] = createSignal<ClipboardState | null>(null);

  // Refs / latched state (no reactivity needed)
  let pendingNav: string | null = null;
  let completePartial = '';
  let completeTimer: number | null = null;
  // (no manual click-timer state — we lean on native dblclick.)
  let pathInputEl!: HTMLInputElement;
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  // Request/reply correlation + timeout live in ./bus.ts (unit-tested).
  // sendWithReply is kept as an alias so the call sites below read the
  // same; handleBE consults bus.tryResolve for echoed ids.
  const bus = createBus(send);
  const sendWithReply = bus.request;

  // fs.watch bookkeeping (the watched-paths dedup set) + the fs_event
  // refresh debounce live in ./watch.ts (unit-tested). shouldRefresh
  // gates a re-list on the dir still being listed+expanded so a stale
  // event can't resurrect a row the user just collapsed; the timer is
  // re-checked at fire time for the same reason. refresh = re-list.
  const fsWatch = createWatch({
    send,
    refresh: (dir) => invalidateAndList(dir),
    shouldRefresh: (dir) => !!listings[dir] && !!expanded[dir],
  });

  // askReplace shows the Replace confirm overlay for `dst` and
  // resolves with the user's choice. Used by withReplacePrompt
  // below; the overlay reads the matching listings entry for
  // type/size display.
  const askReplace = (dst: string): Promise<boolean> => {
    const par = parentPath(dst);
    const name = baseName(dst);
    const entry = listings[par]?.find((e) => e.name === name) ?? null;
    return new Promise<boolean>((resolve) => {
      setReplaceConfirm({ dst, entry, resolve });
    });
  };

  // withReplacePrompt is the conflict-retry flow (request → `*_err
  // exists` → confirm → retry with replace:true → cancelled). The logic
  // lives in @wash/fs-client's replace-flow.ts (unit-tested with a fake
  // bus + confirm); this adapter injects fm's bus + the askReplace
  // overlay so the 3 call sites read unchanged.
  const withReplacePrompt = (
    req: Record<string, unknown>,
    destPath: string,
    errKind: string,
  ): Promise<BEMessage> =>
    runReplaceFlow({ request: sendWithReply, confirm: askReplace }, req, destPath, errKind);

  // ---- BE comms ----

  const sendList = (p: string) => {
    pendingNav = p;
    send({ kind: 'list', path: p });
  };
  const sendRead = (p: string) => {
    setPreviewContent({ binary: false, size: 0, text: 'loading…', truncated: false });
    send({ kind: 'read', path: p });
  };

  // expandDir marks a path as visually expanded AND asks the BE to
  // watch it. collapseDir is the inverse. Use these instead of
  // setExpanded() directly so the watch state stays in sync with
  // the tree — they're the only places that touch fsWatch. The
  // watch/unwatch dedup + refresh debounce live in ./watch.ts.
  const expandDir = (p: string) => {
    if (expanded[p]) return;
    setExpanded(p, true);
    fsWatch.watch(p);
  };

  const collapseDir = (p: string) => {
    if (!expanded[p]) return;
    setExpanded(produce((s) => { delete s[p]; }));
    fsWatch.unwatch(p);
  };

  const handleBE = (m: BEMessage) => {
    // If the BE echoes an id we issued via sendWithReply, the bus
    // resolves the matching promise and we stop. The remaining
    // branches handle uncorrelated push messages (list/read/complete/
    // fs_event).
    if (bus.tryResolve(m)) return;
    switch (m.kind) {
      case 'list_ok': {
        const p = String(m.path);
        const entries = m.entries as Entry[];
        setListings(p, entries);
        expandDir(p);
        if (!rootInitialized()) {
          setRootInitialized(true);
          setHome(p);
          // Only adopt this path as the current location if the user
          // hasn't already navigated. Otherwise the late initial
          // list_ok would stomp a navigation that ran while the
          // request was in flight. The path-input value is gated
          // separately on the input being untouched, so a user who
          // typed but hasn't hit Enter yet doesn't lose their entry.
          if (!path()) {
            setPath(p);
            setNavHistory(initAt(p));
            if (!pathInputValue()) setPathInputValue(p);
          }
          setSelectedEntry(findEntry(path() || p));
          expandPath(path() || p);
        } else if (parentPath(path()) === p) {
          // Parent listing just arrived — refresh the selection's
          // entry metadata (info pane, etc.) which was stale while
          // we were navigating with no parent listing in hand.
          const fresh = findEntry(path());
          if (fresh) setSelectedEntry(fresh);
        }
        pendingNav = null;
        return;
      }
      case 'list_err':
        // outside_root is expected in sandbox mode when expandPath
        // probes ancestors above WASH_FM_ROOT. Don't pollute the
        // status bar with that — it's the BE doing its job.
        if (m.code !== 'outside_root') {
          setStatusOverride(`error: ${String(m.msg)}`);
        }
        pendingNav = null;
        return;
      case 'read_ok': {
        const r = m as unknown as { binary: boolean; size: number; content: string; truncated: boolean };
        setPreviewContent({ binary: r.binary, size: r.size, text: r.content, truncated: r.truncated });
        return;
      }
      case 'read_err':
        setPreviewContent({ binary: false, size: 0, text: `error: ${String(m.msg)}`, truncated: false });
        return;
      case 'complete_ok': {
        const partial = String(m.partial);
        const matches = (m.matches as string[]) ?? [];
        if (partial !== completePartial) return;
        setCompleteMatches(matches);
        setCompleteIdx(matches.length > 0 ? 0 : -1);
        setCompleteOpen(matches.length > 0);
        return;
      }
      case 'fs_event': {
        // A watched dir saw a change. The watched dir is the
        // PARENT of m.path (fsnotify names the file that
        // changed). If the watched dir ITSELF was removed,
        // m.path is the dir — schedule both paths so the parent
        // listing (which now lacks that dir) refreshes too.
        const evtPath = String(m.path);
        fsWatch.scheduleRefresh(parentPath(evtPath));
        fsWatch.scheduleRefresh(evtPath);
        return;
      }
      case 'clipboard_files_state': {
        setFilesClipboard(parseClipboardState(m.op, m.paths));
        return;
      }
      case 'upload_channel': {
        // BE opened the raw channel for this upload — hand its id to the
        // awaiting streamer.
        const id = String(m.upload_id);
        const resolve = pendingUploadChannels.get(id);
        if (resolve) {
          pendingUploadChannels.delete(id);
          resolve(Number(m.channel_id));
        }
        return;
      }
      case 'upload_done': {
        // Terminal status from the BE reader. Resolve the done waiter,
        // and unblock any still-pending channel waiter (e.g. the BE
        // failed before the channel opened) with the -1 sentinel.
        const id = String(m.upload_id);
        const chResolve = pendingUploadChannels.get(id);
        if (chResolve) {
          pendingUploadChannels.delete(id);
          chResolve(-1);
        }
        const doneResolve = pendingUploadDone.get(id);
        if (doneResolve) {
          pendingUploadDone.delete(id);
          doneResolve(String(m.status));
        }
        return;
      }
      case 'upload_cancelled': {
        // A sidebar cancel was relayed (bulk → fm). Flag the id so the
        // streaming loop stops writing.
        cancelledUploads.add(String(m.upload_id));
        return;
      }
      case 'download_channel': {
        // BE opened the raw channel for this download — hand its id to
        // the awaiting receiver so it can subscribe for bytes.
        const id = String(m.download_id);
        const resolve = pendingDownloadChannels.get(id);
        if (resolve) {
          pendingDownloadChannels.delete(id);
          resolve(Number(m.channel_id));
        }
        return;
      }
      case 'download_done': {
        // Terminal status: all bytes are flushed, finalize the save.
        // Also unblock a still-pending channel waiter (BE failed before
        // the channel opened) with the -1 sentinel.
        const id = String(m.download_id);
        const chResolve = pendingDownloadChannels.get(id);
        if (chResolve) {
          pendingDownloadChannels.delete(id);
          chResolve(-1);
        }
        const doneResolve = pendingDownloadDone.get(id);
        if (doneResolve) {
          pendingDownloadDone.delete(id);
          doneResolve(String(m.status));
        }
        return;
      }
    }
  };

  // ---- navigation ----

  const findEntry = (p: string): Entry | null => {
    const par = parentPath(p);
    const name = baseName(p);
    const entries = listings[par];
    if (!entries) return null;
    return entries.find((e) => e.name === name) ?? null;
  };

  const expandPath = (p: string) => {
    // ancestorChain(p) = ['/', '/a', '/a/b', …] (pure, in paths.ts);
    // this loop applies the side effects per dir.
    const chain = ancestorChain(p);
    for (let i = 0; i < chain.length; i++) {
      const acc = chain[i];
      // Don't sendList the last segment if it's a file/symlink —
      // BE's list errors with "not a directory" and the status bar
      // would carry that user-visible error. The leaf-file case is
      // legitimate when the user clicks a file row; we already
      // sendRead for that elsewhere. (i > 0 so the tree root is never
      // treated as a leaf file.)
      if (i === chain.length - 1 && i > 0) {
        const entry = findEntry(acc);
        if (entry && entry.type !== 'dir') return;
      }
      expandDir(acc);
      if (!listings[acc]) sendList(acc);
    }
  };

  const selectPath = (p: string, pushHistory: boolean) => {
    setStatusOverride(null);
    // Navigation resets the multi-selection. Without this the
    // "Home → drag file to empty pane" path would target the
    // still-selected previous folder rather than the current
    // location (dirOfSelection prefers the selection over path()).
    applySelection(new Set(), 'navigate');
    selectionAnchor = null;

    // Eager state commit: path + input + history move now, before
    // any BE round-trip. We commit intent immediately rather than
    // queueing the navigation until the parent listing loads, because
    // a single-slot queue is easy to lose under fs_event-driven
    // re-lists or concurrent navigations (path bar shows the new path
    // but the tree sticks on the old one). visibleRows lights up as
    // listings arrive.
    const entry = findEntry(p);  // may be null if par isn't listed
    setPath(p);
    setSelectedEntry(entry);
    setPathInputValue(p);
    if (pushHistory) {
      setNavHistory(pushPath(navHistory(), p));
    }

    // expandPath issues sendList for every ancestor we don't have a
    // listing for, including p itself (unless entry is a known
    // non-dir, in which case it stops at the parent). For a file
    // target we still want a preview, so trigger sendRead too.
    expandPath(p);
    if (entry?.type === 'file' || entry?.type === 'symlink') {
      sendRead(p);
    }
    persist();
  };

  // onRowClick threads click+modifier semantics through the tree.
  //   plain click  → select the row. For files we ALSO update path
  //                  (so the path bar reflects the file) and load
  //                  a preview. For folders we DON'T touch path —
  //                  doing so makes the path bar read like the
  //                  user navigated into the folder, even though
  //                  it was just a select. Use double-click to
  //                  actually navigate in.
  //   Ctrl/Cmd-click → toggle path in/out of selection
  //   Shift-click  → range-select from anchor to path
  // Selection drives action targets (Delete, Ctrl+V paste-dest,
  // etc.) — `path()` is reserved for the explicit "where am I"
  // cursor that only moves on double-click or path-bar navigation.
  const onRowClick = (rowPath: string, entry: Entry, ev: MouseEvent) => {
    const focusForFile = (p: string) => {
      // For a file, single click DOES update path + preview —
      // there's no "navigate into a file" so this is the normal
      // file-selection feedback users expect.
      setPath(p);
      setPathInputValue(p);
    };
    // The set/anchor decision is the pure kernel (selection.ts, unit-
    // tested); the component owns the side effects below. A plain click
    // additionally clears any status override and previews the file;
    // shift/ctrl clicks do neither (they're building a multi-selection).
    const isPlain = !ev.shiftKey && !(ev.ctrlKey || ev.metaKey);
    const result = nextSelection(
      { selection: selection(), anchor: selectionAnchor },
      rowPath,
      visibleRows().map((r) => r.path),
      { shift: ev.shiftKey, ctrlOrMeta: ev.ctrlKey || ev.metaKey },
    );
    applySelection(result.selection, 'row-click');
    selectionAnchor = result.anchor;
    setSelectedEntry(entry);
    if (isPlain) setStatusOverride(null);
    if (entry.type === 'file') {
      focusForFile(rowPath);
      if (isPlain) sendRead(rowPath);
    }
  };

  const navigateTo = (p: string) => selectPath(p || '/', true);
  const goHome = () => navigateTo(home());
  const goBack = () => {
    const move = back(navHistory());
    if (move) {
      setNavHistory(at(navHistory(), move.idx));
      selectPath(move.path, false);
    }
  };
  const goForward = () => {
    const move = forward(navHistory());
    if (move) {
      setNavHistory(at(navHistory(), move.idx));
      selectPath(move.path, false);
    }
  };
  const goUp = () => {
    const p = path();
    if (!p) return;
    // Don't walk above the tree root. In production this is "/"
    // and the parentPath("/") === "/" check below would already
    // no-op; in sandbox mode the tree root is WASH_FM_ROOT and
    // going above it gets rejected by the BE as outside_root,
    // leaving navigation stuck in pendingSelectAfter — so we
    // short-circuit here.
    if (p === treeRoot()) return;
    const par = parentPath(p);
    if (par !== p) navigateTo(par);
  };

  const invalidateAndList = (p: string) => {
    setListings(produce((s) => { delete s[p]; }));
    sendList(p);
  };

  const toggleExpand = (p: string) => {
    if (expanded[p]) {
      collapseDir(p);
    } else {
      expandDir(p);
      if (!listings[p]) sendList(p);
    }
    persist();
  };

  const toggleInfo = () => {
    setInfoOpen(!infoOpen());
    persist();
  };

  const followSymlink = (e: Entry, p: string) => {
    if (!e.link_to) return;
    const target = e.link_to.startsWith('/') ? e.link_to : joinPath(parentPath(p), e.link_to);
    navigateTo(target);
  };

  // ---- mutations: rename, delete, create_file, create_dir ----
  //
  // Each helper drives a small FE state machine: open an input UI,
  // collect the user's name/confirm, send the request with id, and
  // on success invalidate the affected directory listing so the
  // tree re-renders fresh entries.

  // dirOfSelection picks the target directory for actions that
  // need a "where to put the thing" (Ctrl+V paste, New File / New
  // Folder, list-pane drop). It prefers the SELECTION over path()
  // because plain-clicking a folder now only selects it — path()
  // doesn't follow. Order:
  //   - single folder selected → that folder
  //   - single file selected   → that file's parent
  //   - empty / multi-select   → path() ("where am I" cursor) or
  //                              the file's parent / home() fallback
  const dirOfSelection = (): string => {
    const sel = selection();
    if (sel.size === 1) {
      const p = Array.from(sel)[0];
      if (listings[p]) return p;
      const entry = findEntry(p);
      if (entry?.type === 'dir') return p;
      return parentPath(p);
    }
    const p = path();
    if (!p) return home();
    if (listings[p]) return p;
    const entry = findEntry(p);
    if (entry?.type === 'dir') return p;
    return parentPath(p);
  };

  // startRename swaps the selected row into inline-edit mode. The
  // user types a new name; Enter commits, Escape cancels.
  const startRename = (p: string) => {
    const name = baseName(p);
    setRenaming({ path: p, draft: name });
  };

  const cancelRename = () => setRenaming(null);

  const commitRename = async () => {
    const r = renaming();
    if (!r) return;
    const draft = r.draft.trim();
    if (draft === '' || draft === baseName(r.path)) {
      setRenaming(null);
      return;
    }
    if (draft.includes('/')) {
      setStatusOverride(`rename: name cannot contain '/'`);
      setRenaming(null);
      return;
    }
    const parent = parentPath(r.path);
    const to = joinPath(parent, draft);
    setRenaming(null);
    const reply = await withReplacePrompt(
      { kind: 'rename', from: r.path, to },
      to,
      'rename_err',
    );
    if (reply.kind === 'rename_ok') {
      invalidateAndList(parent);
      setPath(to);
      setPathInputValue(to);
    } else if (reply.kind === 'cancelled') {
      // User dismissed the Replace prompt — silent no-op.
    } else if (reply.kind === 'rename_err' && reply.code === 'not_empty_dir') {
      setStatusOverride(`rename: ${String(reply.msg)}`);
    } else {
      setStatusOverride(`rename: ${String(reply.msg ?? reply.code ?? 'failed')}`);
    }
  };

  // startNewFile / startNewFolder render a synthetic row at the top
  // of the current dir's listing with a focused input. commitNew
  // creates the entry; cancelNew discards.
  const startNewFile = () => setPendingNew({ parent: dirOfSelection(), kind: 'file', draft: '' });
  const startNewFolder = () => setPendingNew({ parent: dirOfSelection(), kind: 'folder', draft: '' });
  const cancelNew = () => setPendingNew(null);

  const commitNew = async () => {
    const n = pendingNew();
    if (!n) return;
    const draft = n.draft.trim();
    if (draft === '') {
      setPendingNew(null);
      return;
    }
    if (draft.includes('/')) {
      setStatusOverride(`create: name cannot contain '/'`);
      setPendingNew(null);
      return;
    }
    const target = joinPath(n.parent, draft);
    const kind = n.kind === 'file' ? 'create_file' : 'create_dir';
    setPendingNew(null);
    const reply = await sendWithReply({ kind, path: target });
    const okKind = kind + '_ok';
    if (reply.kind === okKind) {
      // Refresh the parent so the new entry appears and select it.
      invalidateAndList(n.parent);
      setPath(target);
      setPathInputValue(target);
    } else {
      setStatusOverride(`${kind}: ${String(reply.msg ?? reply.code ?? 'failed')}`);
    }
  };

  // requestDelete opens the confirm overlay. The user clicks Delete
  // in the overlay to actually proceed. Path is the single path for
  // single-row context-menu Delete; when the user multi-selects 2+
  // rows, requestBulkDelete is invoked instead and the overlay
  // carries the whole selection.
  const requestDelete = (p: string) => {
    if (!p) return;
    setConfirmDelete({ path: p, name: baseName(p) });
  };

  const requestBulkDelete = (paths: string[]) => {
    if (paths.length === 0) return;
    setConfirmDelete({
      path: paths,
      name: paths.length === 1 ? baseName(paths[0]) : `${paths.length} items`,
    });
  };

  const cancelDelete = () => setConfirmDelete(null);

  // dispatchBulkDelete hands `paths` to wash-bulk via the sentinel
  // address. The router spawns the singleton on demand if it's not
  // running; bulk-ops then walks and removes recursively. fs.watch
  // in fm picks up the changes in real time so the tree clears as
  // the job progresses.
  const dispatchBulkDelete = (paths: string[]) => {
    const recipient: { app_id: string } = { app_id: 'com.wash.bulk' };
    window.wash.sendAppMsgTo(recipient, {
      kind: 'enqueue',
      op: 'delete',
      paths,
    });
    applySelection(new Set(), 'bulk-delete-clear');
    setSelectedEntry(null);
  };

  const performDelete = async () => {
    const d = confirmDelete();
    if (!d) return;
    setConfirmDelete(null);
    // Multi-path delete → straight to bulk-ops.
    if (Array.isArray(d.path)) {
      dispatchBulkDelete(d.path);
      return;
    }
    // Single-path delete: try fm direct first. If the target is a
    // non-empty dir the BE returns `not_empty`; we transparently
    // re-route through bulk-ops so the user gets the recursive
    // delete without a separate UI step.
    const target = d.path;
    const reply = await sendWithReply({ kind: 'delete', path: target });
    if (reply.kind === 'delete_ok') {
      const par = parentPath(target);
      invalidateAndList(par);
      setPath(par);
      setPathInputValue(par);
      setSelectedEntry(null);
      setPreviewContent(null);
    } else if (reply.kind === 'delete_err' && reply.code === 'not_empty') {
      dispatchBulkDelete([target]);
    } else {
      setStatusOverride(`delete: ${String(reply.msg ?? reply.code ?? 'failed')}`);
    }
  };

  // commitChmod / commitChown wire the Info section's inline-edit
  // commits to the BE. fs.watch fires on CHMOD/CHOWN events, so we
  // don't need to manually refresh the listing — but we do need to
  // be patient: the watch debounces for 100ms and the BE's reply
  // race the event. Status carries any error verbatim.
  const commitChmod = async (target: string, modeText: string) => {
    const cleaned = modeText.trim();
    if (cleaned === '') return;
    const reply = await sendWithReply({ kind: 'chmod', path: target, mode: cleaned });
    if (reply.kind === 'chmod_ok') {
      // Refresh the parent so the entry's mode/perm display
      // updates without waiting for fs.watch.
      invalidateAndList(parentPath(target));
    } else {
      setStatusOverride(`chmod: ${String(reply.msg ?? reply.code ?? 'failed')}`);
    }
  };

  const commitChown = async (target: string, field: 'owner' | 'group', value: string) => {
    const cleaned = value.trim();
    if (cleaned === '') return;
    const req: Record<string, unknown> = { kind: 'chown', path: target };
    req[field] = cleaned;
    const reply = await sendWithReply(req);
    if (reply.kind === 'chown_ok') {
      invalidateAndList(parentPath(target));
    } else {
      setStatusOverride(`chown: ${String(reply.msg ?? reply.code ?? 'failed')}`);
    }
  };

  // ---- autocomplete ----

  const onPathInput = (value: string) => {
    setPathInputValue(value);
    if (completeTimer != null) window.clearTimeout(completeTimer);
    completeTimer = window.setTimeout(() => {
      completePartial = value;
      send({ kind: 'complete', partial: value });
    }, 120);
  };

  const closeCompleteDropdown = () => {
    setCompleteOpen(false);
    setCompleteMatches([]);
    setCompleteIdx(-1);
  };

  const pickCompletion = (idx: number, alsoNavigate: boolean) => {
    const matches = completeMatches();
    if (idx < 0 || idx >= matches.length) return;
    const pick = matches[idx];
    setPathInputValue(pick);
    closeCompleteDropdown();
    if (alsoNavigate) {
      navigateTo(pick.endsWith('/') ? pick.slice(0, -1) : pick);
    }
  };

  const onPathKey = (ev: KeyboardEvent) => {
    if (completeOpen() && completeMatches().length > 0) {
      const matches = completeMatches();
      if (ev.key === 'ArrowDown') {
        ev.preventDefault();
        setCompleteIdx((completeIdx() + 1) % matches.length);
        return;
      }
      if (ev.key === 'ArrowUp') {
        ev.preventDefault();
        setCompleteIdx((completeIdx() - 1 + matches.length) % matches.length);
        return;
      }
      if (ev.key === 'Tab') {
        ev.preventDefault();
        pickCompletion(completeIdx(), false);
        return;
      }
      if (ev.key === 'Escape') {
        ev.preventDefault();
        closeCompleteDropdown();
        return;
      }
      if (ev.key === 'Enter') {
        ev.preventDefault();
        pickCompletion(completeIdx(), true);
        return;
      }
    } else if (ev.key === 'Enter') {
      ev.preventDefault();
      navigateTo(pathInputValue());
    } else if (ev.key === 'Escape') {
      closeCompleteDropdown();
    }
  };

  // ---- drag-and-drop ----
  //
  // Move semantics only in v1 (per docs/ARCHITECTURE.md and the
  // [[wash-fm-dnd-plan]] memory). Drop on a folder row → move into
  // that folder. Drop on the empty list pane → move into the
  // currently-selected dir. Drop on a file row is rejected by the
  // browser (no preventDefault → no drop allowed). Copy needs a
  // dedicated BE op + bulk-ops semantics; that lives in a future
  // app.
  //
  // Cross-window works for free: the target window's fm BE owns
  // the rename syscall, and both windows learn of the change via
  // fs.watch — source row vanishes, dest row appears, no explicit
  // coordination protocol needed.

  // dropTargetPath is the path of the folder row currently being
  // hovered as a drop target, or "" if no row is targeted (drop
  // would land in the list pane = current dir). TreeRows read it
  // to render their dropTarget highlight.
  const [dropTargetPath, setDropTargetPath] = createSignal('');

  // dropMenu, when non-null, renders a small overlay at the drop
  // position offering Move / Copy / Symlink. Set when the user
  // held Alt during the drop; cleared when they pick an option
  // or click outside. (Why Alt, not right-mouse? Firefox and
  // Chromium both gate HTML5 dragstart to the left button —
  // right-button never initiates a drag in the first place, so a
  // modifier on the left-button drag is the only mechanism that
  // works in both.) srcs is the full set of dragged paths;
  // length>1 means we route through bulk-ops for everything.
  const [dropMenu, setDropMenu] = createSignal<{ x: number; y: number; srcs: string[]; targetDir: string } | null>(null);

  // uploadDropActive highlights the list pane while an EXTERNAL (OS)
  // file drag hovers over it — distinct from the per-row dropTarget
  // highlight used for internal moves. Cleared on drop / dragleave.
  const [uploadDropActive, setUploadDropActive] = createSignal(false);

  // uploadConflict drives the pre-flight "N files already exist"
  // overlay. Like askReplace it resolves a promise with the user's
  // choice — 'replace' (overwrite), 'skip' (only new files), or null
  // (cancel the whole upload).
  const [uploadConflict, setUploadConflict] =
    createSignal<{ existing: number; total: number; resolve: (p: 'replace' | 'skip' | null) => void } | null>(null);

  // cancelledUploads holds upload_ids the BE told us to stop (a
  // sidebar cancel relayed through wash-bulk). Non-reactive — the
  // streaming loop just polls it. See the upload_cancelled BE message.
  const cancelledUploads = new Set<string>();

  // The byte stream rides a raw wash channel the BE opens after
  // upload_begin; these one-shot maps bridge the async BE pushes
  // (upload_channel carries the channel id, upload_done the terminal
  // status) back to the awaiting runUpload promise, keyed by upload_id.
  const pendingUploadChannels = new Map<string, (channelID: number) => void>();
  const pendingUploadDone = new Map<string, (status: string) => void>();

  // Download egress mirrors upload's async handshake: the BE opens a raw
  // channel and pushes its id (download_channel), streams the file/zip
  // bytes, then signals completion (download_done). Keyed by download_id.
  const pendingDownloadChannels = new Map<string, (channelID: number) => void>();
  const pendingDownloadDone = new Map<string, (status: string) => void>();

  // Hidden native inputs that back the toolbar Upload buttons — the
  // only way to reach OS files from the browser. dirInputEl gets the
  // webkitdirectory attribute in onMount (no clean JSX typing for it).
  let fileInputEl!: HTMLInputElement;
  let dirInputEl!: HTMLInputElement;

  // Drag payload parsing + drop-accept logic (DRAG_MIME, readDragPaths,
  // dragPayload, hasWashDrag, dropEffectFor) live in ./dnd.ts
  // (framework-free, unit-tested). The thin DOM handlers below own only
  // the event-plumbing (preventDefault/stopPropagation) and the Solid
  // dropTargetPath signal.
  const onDragStart = (ev: DragEvent, p: string) => {
    if (!ev.dataTransfer) return;
    // effectAllowed = 'copyMove' (not 'move') so the browser
    // accepts Alt/Option-drag as a copy gesture on macOS — when
    // the user holds Option, the OS shifts the drag cursor to
    // the "+ copy" badge, and the drop event still fires with
    // altKey=true. With 'move' alone, some browsers reject
    // Option-drag entirely. We don't include 'link' because
    // create-symlink lives behind the Alt menu, not the cursor
    // badge.
    ev.dataTransfer.effectAllowed = 'copyMove';
    const paths = dragPayload(p, selection());
    ev.dataTransfer.setData(DRAG_MIME, JSON.stringify(paths));
    // text/plain is a friendly fallback for drops onto non-wash
    // targets (terminals, editors). Newline-joined matches what
    // most apps expect for multi-path copy.
    ev.dataTransfer.setData('text/plain', paths.join('\n'));
  };

  const onDragEnd = () => {
    setDropTargetPath('');
  };

  // onRowDragOver is wired on directory rows only. preventDefault
  // tells the browser "this is a valid drop target," and
  // stopPropagation keeps the list-pane onDragOver from also
  // claiming the event (which would clear dropTargetPath).
  // dropEffect mirrors the alt/option modifier so the OS cursor
  // shows the right hint (+copy when Option held, regular move
  // otherwise) — without this, Mac users get no feedback that
  // the modifier is actually doing something.
  const onRowDragOver = (ev: DragEvent, rowPath: string) => {
    // External OS file drag → mark the folder row as an upload target.
    if (isExternalFileDrag(ev.dataTransfer)) {
      ev.preventDefault();
      ev.stopPropagation();
      ev.dataTransfer!.dropEffect = 'copy';
      if (dropTargetPath() !== rowPath) setDropTargetPath(rowPath);
      return;
    }
    if (!hasWashDrag(ev.dataTransfer)) return;
    ev.preventDefault();
    ev.stopPropagation();
    ev.dataTransfer!.dropEffect = dropEffectFor(ev.altKey);
    if (dropTargetPath() !== rowPath) setDropTargetPath(rowPath);
  };

  const onRowDrop = (ev: DragEvent, rowPath: string) => {
    if (isExternalFileDrag(ev.dataTransfer)) {
      ev.preventDefault();
      ev.stopPropagation();
      setDropTargetPath('');
      setUploadDropActive(false);
      collectFromDrop(ev.dataTransfer!, rowPath);
      return;
    }
    const paths = readDragPaths(ev.dataTransfer);
    if (paths.length === 0) return;
    ev.preventDefault();
    ev.stopPropagation();
    setDropTargetPath('');
    if (ev.altKey) {
      setDropMenu({ x: ev.clientX + 8, y: ev.clientY + 8, srcs: paths, targetDir: rowPath });
      return;
    }
    handleMoveDrop(paths, rowPath);
  };

  const onListDragOver = (ev: DragEvent) => {
    // External OS file drag → highlight the whole pane (drop lands in
    // the current dir).
    if (isExternalFileDrag(ev.dataTransfer)) {
      ev.preventDefault();
      ev.dataTransfer!.dropEffect = 'copy';
      if (!uploadDropActive()) setUploadDropActive(true);
      if (dropTargetPath() !== '') setDropTargetPath('');
      return;
    }
    if (!hasWashDrag(ev.dataTransfer)) return;
    ev.preventDefault();
    ev.dataTransfer!.dropEffect = dropEffectFor(ev.altKey);
    if (dropTargetPath() !== '') setDropTargetPath('');
  };

  const onListDragLeave = (ev: DragEvent) => {
    // Only clear when the pointer actually left the pane (dragleave
    // also fires when crossing into child rows).
    const to = ev.relatedTarget as Node | null;
    if (!to || !(ev.currentTarget as HTMLElement).contains(to)) {
      setUploadDropActive(false);
      // An OS-file drag has no dragend in our app (the source is the
      // OS), so a folder-row highlight set by onRowDragOver would
      // otherwise stick after the drag leaves the window. Internal
      // drags clear it via onDragEnd; clear it here for external ones.
      setDropTargetPath('');
    }
  };

  const onListDrop = (ev: DragEvent) => {
    setUploadDropActive(false);
    if (isExternalFileDrag(ev.dataTransfer)) {
      ev.preventDefault();
      setDropTargetPath('');
      collectFromDrop(ev.dataTransfer!, dirOfSelection());
      return;
    }
    const paths = readDragPaths(ev.dataTransfer);
    if (paths.length === 0) return;
    ev.preventDefault();
    setDropTargetPath('');
    if (ev.altKey) {
      setDropMenu({ x: ev.clientX + 8, y: ev.clientY + 8, srcs: paths, targetDir: dirOfSelection() });
      return;
    }
    handleMoveDrop(paths, dirOfSelection());
  };

  // handleMoveDrop routes a plain (no-alt) drop: single path uses
  // fm-direct rename (fast, Replace prompt on conflict); multi
  // paths always go through bulk-ops where Replace All / Skip All
  // exist. Caller has already filtered to ≥1 path.
  const handleMoveDrop = (paths: string[], targetDir: string) => {
    if (paths.length === 1) {
      void commitMove(paths[0], targetDir);
      return;
    }
    dispatchBulkMove(paths, targetDir);
  };

  const dispatchBulkMove = (paths: string[], targetDir: string) => {
    if (!targetDir) return;
    // Apply the same guards commitMove enforces for the single-file
    // path, so a multi-select drop can't enqueue an invalid bulk job:
    //   - moving a folder into itself or its own descendant (would
    //     error in bulk-ops; reject up front with the same message).
    //   - same-parent drops are no-ops, not moves — drop them silently
    //     rather than queuing a redundant rename.
    for (const src of paths) {
      if (targetDir === src || targetDir.startsWith(src + '/')) {
        setStatusOverride('move: cannot move a folder into itself');
        return;
      }
    }
    const movable = paths.filter((src) => parentPath(src) !== targetDir);
    if (movable.length === 0) return; // everything already there
    window.wash.sendAppMsgTo(
      { app_id: 'com.wash.bulk' },
      { kind: 'enqueue', op: 'move', paths: movable, dest: targetDir },
    );
    // Source paths are about to vanish — drop them from the
    // selection so the status bar doesn't keep claiming "N
    // selected" for paths that no longer exist.
    applySelection(new Set(), 'bulk-move-clear');
    selectionAnchor = null;
  };

  // ---- upload: OS files → confined fs ----
  //
  // Two entry points feed the same runUpload pipeline: an external OS
  // file/folder drag dropped onto the tree, and the toolbar Upload
  // buttons (a <input type=file [webkitdirectory]> picker). The bytes
  // stream over fm's own bus (upload_begin/chunk/end in be/upload.go);
  // progress surfaces in the wash-bulk sidebar widget as an external
  // job. Conflicts are resolved once, up front, via uploadConflict.

  // isExternalFileDrag distinguishes an OS file drag (carries the
  // synthetic "Files" type, NOT our wash MIME) from an internal
  // row drag. Internal drags are handled by the move/copy path.
  const isExternalFileDrag = (dt: DataTransfer | null): boolean =>
    !!dt && !hasWashDrag(dt) && Array.from(dt.types).includes('Files');

  // askUploadPolicy shows the conflict overlay and resolves the chosen
  // policy (or null to cancel). There is a SINGLE uploadConflict signal /
  // overlay, so two uploads that both hit conflicts (e.g. two OS drops in
  // quick succession) must SERIALIZE their prompts: we chain on
  // promptTail so the second overlay only appears once the first is
  // answered. Without this the second setUploadConflict clobbered the
  // first, and the first runUpload's await never resolved — it hung,
  // silently, forever, and that upload simply never happened.
  let promptTail: Promise<unknown> = Promise.resolve();
  const askUploadPolicy = (existing: number, total: number): Promise<'replace' | 'skip' | null> => {
    const shown = promptTail.then(
      () => new Promise<'replace' | 'skip' | null>((resolve) => setUploadConflict({ existing, total, resolve })),
    );
    promptTail = shown.catch(() => {});
    return shown;
  };

  // onUploadFailure surfaces an unexpected upload rejection (a bus
  // timeout on upload_check/begin, a writeRaw on a torn-down channel,
  // etc.) as a status line instead of an unhandled promise rejection —
  // both runUpload entry points (drop + picker) route their tails here.
  const onUploadFailure = (e: unknown) =>
    setStatusOverride(`upload: ${e instanceof Error ? e.message : 'failed'}`);

  // collectFromDrop snapshots the drop's entries (recursing into
  // dropped folders) and uploads them into targetDir. Called
  // synchronously from the drop handler so webkitGetAsEntry is read
  // while the DataTransfer is still alive.
  const collectFromDrop = (dt: DataTransfer, targetDir: string) => {
    void entriesFromDataTransfer(dt.items).then((items) => runUpload(items, targetDir)).catch(onUploadFailure);
  };

  // onUploadInput handles a picker selection. The folder picker
  // populates webkitRelativePath; the file picker leaves it empty.
  const onUploadInput = (el: HTMLInputElement) => {
    const files = el.files;
    if (!files || files.length === 0) return;
    const items = entriesFromFileList(files);
    el.value = ''; // allow re-picking the same file later
    void runUpload(items, dirOfSelection()).catch(onUploadFailure);
  };

  // waitFor builds a one-shot promise registered in `map` under id,
  // with a timeout fallback so a lost BE push can't hang the upload.
  const waitFor = <T,>(map: Map<string, (v: T) => void>, id: string, ms: number, onTimeout: T): Promise<T> =>
    new Promise<T>((resolve) => {
      let settled = false;
      const done = (v: T) => {
        if (settled) return;
        settled = true;
        map.delete(id);
        resolve(v);
      };
      map.set(id, done);
      setTimeout(() => done(onTimeout), ms);
    });

  // runUpload is the pipeline: pre-flight conflict check → optional
  // prompt → begin → stream each file's bytes over the raw channel →
  // wait for the BE's done push. Progress + cancellation live in
  // wash-bulk; here we only pump bytes and, on completion, refresh the
  // destination listing so the new rows show.
  const runUpload = async (items: UploadItem[], destDir: string) => {
    if (items.length === 0 || !destDir) return;
    const chk = await sendWithReply({ kind: 'upload_check', dest: destDir, rels: items.map((i) => i.relPath) });
    if (chk.kind === 'upload_check_err') {
      setStatusOverride(`upload: ${String(chk.msg ?? chk.code ?? 'failed')}`);
      return;
    }
    const conflicts = new Set<string>((chk.conflicts as string[]) ?? []);
    let policy: 'replace' | 'skip' = 'replace';
    if (conflicts.size > 0) {
      const choice = await askUploadPolicy(conflicts.size, items.length);
      if (!choice) return; // cancelled
      policy = choice;
    }
    const plan = planUpload(items, conflicts, policy);
    if (plan.entries.length === 0) {
      setStatusOverride('upload: nothing new to upload');
      return;
    }
    const begin = await sendWithReply({
      kind: 'upload_begin',
      dest: destDir,
      total_bytes: plan.totalBytes,
      policy,
      labels: plan.labels,
    });
    if (begin.kind !== 'upload_begin_ok') {
      setStatusOverride(`upload: ${String(begin.msg ?? begin.code ?? 'failed')}`);
      return;
    }
    const uploadID = String(begin.upload_id);
    // The BE opens the raw channel asynchronously and pushes its id.
    const channelID = await waitFor(pendingUploadChannels, uploadID, 15_000, -1);
    try {
      if (channelID >= 0) {
        // Register the done waiter BEFORE streaming so we never miss an
        // early terminal push — but with NO timeout spanning the stream
        // (a large/slow OS-folder drop can run for minutes). It's a bare
        // one-shot resolver; the finalize window is bounded after the end
        // marker below. finally unregisters it either way.
        const donePromise = new Promise<string>((resolve) => pendingUploadDone.set(uploadID, resolve));
        // breatheIfDue hands the event loop a real macrotask at most once
        // per UPLOAD_YIELD_MS so a sidebar cancel (and UI input) is seen
        // mid-stream even when the send buffer never fills — the
        // many-small-files case awaitUploadDrain alone doesn't cover.
        let lastYield = Date.now();
        const breatheIfDue = async () => {
          if (Date.now() - lastYield < UPLOAD_YIELD_MS) return;
          await new Promise((r) => setTimeout(r, 0));
          lastYield = Date.now();
        };
        stream: for (const it of plan.entries) {
          await breatheIfDue();
          if (cancelledUploads.has(uploadID)) break;
          window.wash.writeRawFor(props.origin, channelID, encodeRecordHeader(it.relPath, it.file.size));
          for await (const chunk of readBlobChunks(it.file)) {
            window.wash.writeRawFor(props.origin, channelID, chunk);
            // Backpressure: writeRaw queues into the single shell
            // socket's send buffer. Without pacing, the whole file
            // lands there at once and head-of-line blocks the cancel
            // control frame behind megabytes of data — so a sidebar
            // cancel never reaches the BE until the upload has already
            // drained. Keep the buffer small so the (interactive)
            // cancel frame jumps ahead and this loop's own cancel
            // check actually fires mid-stream.
            await awaitUploadDrain(props.origin, () => cancelledUploads.has(uploadID));
            await breatheIfDue();
            if (cancelledUploads.has(uploadID)) break stream;
          }
        }
        if (!cancelledUploads.has(uploadID)) {
          window.wash.writeRawFor(props.origin, channelID, uploadEndMarker());
        }
        // Bound ONLY the finalize: the BE emits upload_done shortly after
        // it reads the end marker (or after a cancel closes the channel).
        // If that push is lost, stop waiting after UPLOAD_FINALIZE_MS so
        // the listing still refreshes — but the long stream above was
        // never on the clock.
        await Promise.race([donePromise, new Promise((r) => setTimeout(r, UPLOAD_FINALIZE_MS))]);
      }
    } finally {
      cancelledUploads.delete(uploadID);
      pendingUploadChannels.delete(uploadID);
      pendingUploadDone.delete(uploadID);
      // Reveal the new rows without waiting for fs.watch (which never
      // fires for a still-collapsed destination folder).
      expandDir(destDir);
      invalidateAndList(destDir);
    }
  };

  // runDownload asks the BE to stream one-or-more confined paths back to
  // the browser. A lone file comes as-is; a directory or a multi-select
  // arrives as a single zip. We open the raw channel the BE announces,
  // concatenate every chunk, and on download_done synthesize an <a
  // download> click to drop it in the browser's downloads. The terminal
  // toast (Download ready / failed) is fired BE-side.
  const runDownload = async (paths: string[]) => {
    if (paths.length === 0) return;
    const begin = await sendWithReply({ kind: 'download_begin', paths });
    if (begin.kind !== 'download_begin_ok') {
      setStatusOverride(`download: ${String(begin.msg ?? begin.code ?? 'failed')}`);
      return;
    }
    const downloadID = String(begin.download_id);
    const filename = String(begin.filename);
    const channelID = await waitFor(pendingDownloadChannels, downloadID, 15_000, -1);
    if (channelID < 0) {
      setStatusOverride('download: channel never opened');
      return;
    }
    const chunks: Uint8Array[] = [];
    const unsubscribe = window.wash.openRawChannel(channelID, (bytes) => {
      // Copy: the shell may reuse the backing buffer after the callback.
      chunks.push(bytes.slice());
    });
    // No timeout spanning the stream — a big zip can run a while. The BE
    // emits download_done right after the last frame flushes.
    const donePromise = new Promise<string>((resolve) => pendingDownloadDone.set(downloadID, resolve));
    try {
      const status = await donePromise;
      if (status === 'done') {
        saveBytes(filename, chunks);
      } else {
        setStatusOverride(`download: ${filename} failed`);
      }
    } finally {
      unsubscribe();
      pendingDownloadChannels.delete(downloadID);
      pendingDownloadDone.delete(downloadID);
    }
  };

  // saveBytes drops a Blob into the browser's downloads under name via a
  // synthetic anchor click. data-testid on the anchor lets the e2e assert
  // the trigger fired without depending on the OS download chrome.
  const saveBytes = (name: string, chunks: Uint8Array[]) => {
    const blob = new Blob(chunks as BlobPart[], { type: 'application/octet-stream' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    a.dataset.testid = 'fm-download-anchor';
    a.dataset.name = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    // Revoke after a tick so the navigation/save has picked up the URL.
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
  };

  // commitSymlink creates a symlink at targetDir/basename(src)
  // pointing at src. Same trivial-skip rules as commitMove except
  // we allow same-parent (linking next to the original is a fine
  // use case: shorter handy alias for a path).
  //
  // On success, immediately expand + invalidate the target dir so
  // the new symlink row shows without waiting for fs.watch (which
  // only fires for dirs the user had already expanded — for a
  // brand-new drop into a collapsed folder, that's never).
  const commitSymlink = async (src: string, targetDir: string) => {
    if (!src || !targetDir) return;
    const linkPath = joinPath(targetDir, baseName(src));
    if (linkPath === src) {
      // No-op: would create a circular self-link. Refuse politely.
      setStatusOverride('symlink: link path equals target');
      return;
    }
    const reply = await withReplacePrompt(
      { kind: 'symlink', target: src, link_path: linkPath },
      linkPath,
      'symlink_err',
    );
    if (reply.kind === 'symlink_ok') {
      expandDir(targetDir);
      invalidateAndList(targetDir);
    } else if (reply.kind === 'cancelled') {
      // user dismissed Replace prompt — silent no-op.
    } else if (reply.kind === 'symlink_err' && reply.code === 'not_empty_dir') {
      setStatusOverride(`symlink: ${String(reply.msg)}`);
    } else {
      setStatusOverride(`symlink: ${String(reply.msg ?? reply.code ?? 'failed')}`);
    }
  };

  // pickSelectionPaths returns the current selection (if non-empty)
  // or the focused row's path as a one-element list. Used by
  // shortcuts so Ctrl+C without an explicit multi-select still
  // operates on the currently-focused row.
  const pickSelectionPaths = (): string[] => {
    const sel = selection();
    if (sel.size > 0) return Array.from(sel);
    const p = path();
    return p ? [p] : [];
  };

  // putFilesOnClipboard tells the BE to set the router clipboard
  // with `op` + `paths`. The BE echoes back a clipboard_files_state
  // event which updates filesClipboard reactively — so the status
  // bar reflects the cut/copy without us tracking it FE-locally.
  const putFilesOnClipboard = (op: 'copy' | 'cut', paths: string[]) => {
    if (paths.length === 0) return;
    send({ kind: 'clipboard_files_set', op, paths });
  };

  // pasteFilesClipboard reads the current files-clipboard state
  // (mirrored from BE) and dispatches the matching bulk-ops job.
  // For "cut", we clear the clipboard after dispatching so a
  // second paste doesn't try to re-move already-moved paths.
  const pasteFilesClipboard = () => {
    const plan = planPaste(filesClipboard(), dirOfSelection());
    if (!plan) return;
    window.wash.sendAppMsgTo(
      { app_id: 'com.wash.bulk' },
      { kind: 'enqueue', op: plan.op, paths: plan.paths, dest: plan.dest },
    );
    if (plan.clearAfter) {
      // Clear the clipboard so a second Ctrl+V doesn't try to
      // re-move paths that no longer exist at the source.
      send({ kind: 'clipboard_files_set', op: 'copy', paths: [] });
      setFilesClipboard(null);
    }
  };

  // commitBulkCopy enqueues a recursive copy job in wash-bulk via
  // the singleton sentinel. We deliberately route ALL copies
  // through bulk-ops (even a single file): copy can be slow on
  // large dirs and the queue UI is the right place to surface
  // progress — fm has no business owning that affordance.
  // fs.watch in fm picks up the new entry in the target dir as
  // bulk-ops creates files. Accepts an array so multi-select
  // alt-drag-Copy works in one job.
  const commitBulkCopy = (srcs: string[], targetDir: string) => {
    if (srcs.length === 0 || !targetDir) return;
    for (const src of srcs) {
      if (targetDir === src || targetDir.startsWith(src + '/')) {
        setStatusOverride('copy: cannot copy a folder into itself');
        return;
      }
    }
    window.wash.sendAppMsgTo(
      { app_id: 'com.wash.bulk' },
      { kind: 'enqueue', op: 'copy', paths: srcs, dest: targetDir },
    );
  };

  // commitMove is the actual move operation. Skips trivial cases
  // (same-parent drop, dropping onto self or a descendant) before
  // sending the rename, then lets fs.watch drive the refresh.
  const commitMove = async (src: string, targetDir: string) => {
    if (!src || !targetDir) return;
    const srcParent = parentPath(src);
    if (srcParent === targetDir) return; // already there — silent no-op
    // Refuse moving a dir into itself or its own descendant — the BE
    // would error with EINVAL, but a friendly early reject saves a
    // round trip and gives the user immediate visible feedback.
    if (targetDir === src || targetDir.startsWith(src + '/')) {
      setStatusOverride('move: cannot move a folder into itself');
      return;
    }
    const dest = joinPath(targetDir, baseName(src));
    const reply = await withReplacePrompt(
      { kind: 'rename', from: src, to: dest },
      dest,
      'rename_err',
    );
    if (reply.kind === 'rename_ok') {
      // Refresh both ends ourselves rather than waiting for
      // fs.watch — for a drop into a collapsed target dir, the
      // watch never fires (we only subscribe to expanded dirs).
      // Expanding the target also makes the moved file visible.
      invalidateAndList(srcParent);
      expandDir(targetDir);
      invalidateAndList(targetDir);
      if (path() === src) {
        setPath(dest);
        setPathInputValue(dest);
      }
    } else if (reply.kind === 'cancelled') {
      // user dismissed Replace prompt — silent no-op.
    } else if (reply.kind === 'rename_err' && reply.code === 'not_empty_dir') {
      setStatusOverride(`move: ${String(reply.msg)}`);
    } else {
      setStatusOverride(`move: ${String(reply.msg ?? reply.code ?? 'failed')}`);
    }
  };

  // Body splitter: drag-to-resize the fixed-width preview/info dock.
  // The Splitter primitive (@wash/ui) owns the gesture; onSplitChange
  // turns its divider percent into the dock's pixel width, and we own
  // the body grid template (gridCols) and persistence.

  // ---- state persistence ----

  const persist = () => {
    if (!props.instance) return;
    const s: PersistedState = {
      path: path() || undefined,
      expanded: Object.keys(expanded),
      sort_key: sortKey(),
      sort_desc: sortDesc(),
      show_hidden: showHidden(),
      info_open: infoOpen(),
      preview_w: previewW(),
      preview_open: previewOpen(),
    };
    send({ kind: 'save_state', state: s });
  };

  const restoreFrom = (s: PersistedState) => {
    if (s.sort_key) setSortKey(s.sort_key);
    if (typeof s.sort_desc === 'boolean') setSortDesc(s.sort_desc);
    if (typeof s.show_hidden === 'boolean') setShowHidden(s.show_hidden);
    if (typeof s.info_open === 'boolean') setInfoOpen(s.info_open);
    if (typeof s.preview_w === 'number') setPreviewW(Math.max(PREVIEW_MIN_W, Math.round(s.preview_w)));
    if (typeof s.preview_open === 'boolean') setPreviewOpen(s.preview_open);
    if (s.expanded) {
      for (const p of s.expanded) expandDir(p);
    }
    if (s.path) selectPath(s.path, false);
    else send({ kind: 'request_initial' });
  };

  // ---- listings sort/filter ----

  // treeRoot is the SHALLOWEST ancestor of path() that the FE has
  // successfully listed — i.e. the highest reachable directory in
  // the current confinement. In production fm lists "/" so
  // treeRoot is "/"; in WASH_FM_ROOT mode "/" returns
  // outside_root, so the loop stops at the sandbox root (the
  // first ancestor with a listing). Crucially we do NOT pick the
  // deepest listed ancestor — that would re-root the tree every
  // time the user clicked a file in an already-expanded subdir,
  // making the rest of the tree disappear. The user explicitly
  // expands subfolders by double-click / chevron; the tree stays
  // anchored at the highest reachable point and they navigate via
  // expansion + the path bar.
  const treeRoot = createMemo<string>(() => {
    if (listings['/']) return '/';
    const target = path() || home();
    if (!target) return '/';
    let acc = '/';
    const parts = target.split('/').filter(Boolean);
    for (const part of parts) {
      acc = acc === '/' ? '/' + part : acc + '/' + part;
      if (listings[acc]) return acc;
    }
    return '/';
  });

  // Flatten the visible tree into {entry, path, depth, childCount} rows
  // — Solid's <For> renders the list; toggling expand triggers a fresh
  // computation here automatically. The walk + in-flight fallback bridge
  // live in @wash/fs-client's flattenTree (pure, unit-tested); this memo
  // reads the reactive sources (sort signals, treeRoot, path) and passes
  // the listings/expanded store proxies straight in — flattenTree runs
  // synchronously here, so its listings[p]/expanded[x] reads stay tracked.
  const flatRows = createMemo<VisibleRow[]>(() =>
    flattenTree<Entry>({
      listings,
      expanded,
      sort: { key: sortKey(), desc: sortDesc(), showHidden: showHidden() },
      start: treeRoot(),
      cur: path(),
    }),
  );

  // Identity-stabilising layer. flattenTree returns brand-new wrapper +
  // entry objects on every recompute, and a plain <For> keys by object
  // reference — so without this, ANY re-list (even a no-op fs.watch
  // refresh that produces value-identical entries) tears down and
  // rebuilds every row's DOM. A click then races the rebuild: the row
  // is "detached from the DOM" mid-click. That's the source of the
  // fm/clipboard full-suite flakes. Here we reuse the previous row
  // object for a path whose visible content is unchanged, so <For>
  // keeps those rows' DOM and only genuinely-changed rows re-render.
  let prevRows = new Map<string, { row: VisibleRow; sig: string }>();
  const visibleRows = createMemo<VisibleRow[]>(() => {
    const next = new Map<string, { row: VisibleRow; sig: string }>();
    const out = flatRows().map((row) => {
      const sig = JSON.stringify(row);
      const prior = prevRows.get(row.path);
      const stable = prior && prior.sig === sig ? prior.row : row;
      next.set(row.path, { row: stable, sig });
      return stable;
    });
    prevRows = next;
    return out;
  });

  // visibleCount is the total entries visible right now. Updates
  // automatically as folders expand/collapse.
  const visibleCount = createMemo(() => visibleRows().length);

  // ---- selection invariant (observe-only, always on) ----
  //
  // `selection` is a free-floating set of paths that nothing reconciles
  // against the listing, so a change underneath a live selection — an
  // fs.watch delete/rename from another window, a finished bulk job, a
  // sort / hidden-files toggle, or collapsing a selected row's parent —
  // can leave "ghost" paths in the set: still counted by the status bar
  // and still handed to delete/drag/copy, but no longer a real row.
  //
  // STRICT check: every selected path must be a currently-VISIBLE row.
  // We only LOG a violation (no auto-prune) so the trigger isn't masked
  // while we hunt the root cause. The log carries lastSelectionWrite as
  // a breadcrumb. Deduped by the ghost signature so a persistent ghost
  // logs once per change, not on every reactive tick.
  let lastGhostSig = '';
  createEffect(() => {
    const sel = selection();
    const visible = new Set(visibleRows().map((r) => r.path));
    const ghosts: string[] = [];
    for (const p of sel) if (!visible.has(p)) ghosts.push(p);
    const sig = ghosts.length ? ghosts.slice().sort().join('\n') : '';
    if (sig === lastGhostSig) return;
    lastGhostSig = sig;
    if (ghosts.length === 0) return;
    // untrack path() so the cursor moving doesn't re-run this check.
    console.error(
      '[fm] selection invariant: selected paths are not in the visible list',
      {
        lastSelectionWrite,
        ghosts,
        ghostCount: ghosts.length,
        selectionSize: sel.size,
        visibleCount: visible.size,
        cursor: untrack(() => path()),
      },
    );
  });

  // statusBar text — derived. While statusOverride is set (drop /
  // error / clipboard feedback), show it; otherwise the live
  // visible-entry count. Errors render in red at full opacity so
  // failures (e.g. "move: cross device link") read as failures and
  // not as casual chatter alongside the entry count.
  const statusBar = createMemo<JSX.Element>(() => {
    const override = statusOverride();
    if (override) {
      if (override.kind === 'error') {
        return (
          <span
            data-status-kind="error"
            style={{ color: tokens.borderDanger, opacity: 1 }}
          >
            {override.text}
          </span>
        );
      }
      return override.text;
    }
    if (!rootInitialized()) return 'loading…';
    const sel = selection().size;
    if (sel > 1) return `${sel} of ${visibleCount()} selected`;
    return `${visibleCount()} entries`;
  });

  // ---- menus ----

  const closeMenu = () => setMenu(null);

  // Menu coords are viewport-relative — the Menu component portals
  // out of the window slot (which has overflow:auto and would clip
  // a menu painted at the edge) and renders with position:fixed.
  const openSortMenu = (ev: MouseEvent) => {
    const rect = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    setMenu({ kind: 'sort', left: rect.right - 160, top: rect.bottom + 4 });
  };

  const openContextMenu = (ev: MouseEvent, entry: Entry, p: string) => {
    ev.preventDefault();
    setSelectedEntry(entry);
    setPath(p);
    setPathInputValue(p);
    // Native FM convention: right-clicking an unselected row
    // implicitly replaces the selection with just that row, so the
    // menu action operates on what was clicked, not on a stale
    // selection elsewhere.
    if (!selection().has(p)) applySelection(new Set([p]), 'context-menu-select');
    if (entry.type === 'file') sendRead(p);
    setMenu({ kind: 'context', left: ev.clientX, top: ev.clientY, entry, path: p });
  };

  // ---- lifecycle: events ----

  onMount(() => {
    // webkitdirectory has no clean JSX typing — set it as an attribute
    // so the folder picker recurses (populating webkitRelativePath).
    dirInputEl.setAttribute('webkitdirectory', '');
    dirInputEl.setAttribute('directory', '');
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail as BEMessage);
    const onState = (ev: Event) => {
      const s = (ev as CustomEvent).detail as PersistedState | null;
      if (s) restoreFrom(s);
      else send({ kind: 'request_initial' });
    };
    // Don't fire shortcuts when the user is typing in an input —
    // path bar, rename input, new-file input, info-edit input.
    // The shell's wash-app-fm host has tabindex=0, so plain rows
    // can receive keydown without an input being focused.
    const isTypingFocused = (): boolean => {
      const el = document.activeElement;
      if (!el) return false;
      const tag = el.tagName;
      return tag === 'INPUT' || tag === 'TEXTAREA' || (el as HTMLElement).isContentEditable;
    };

    const onKey = (ev: KeyboardEvent) => {
      if (isTypingFocused()) return;
      const sel = selection();
      const cmd = ev.ctrlKey || ev.metaKey;

      // Plain keys.
      if (!cmd && !ev.altKey) {
        if (ev.key === 'F2' && sel.size === 1) {
          ev.preventDefault();
          startRename(Array.from(sel)[0]);
          return;
        }
        if ((ev.key === 'Delete' || ev.key === 'Backspace') && sel.size > 0) {
          ev.preventDefault();
          const paths = Array.from(sel);
          if (paths.length === 1) requestDelete(paths[0]);
          else requestBulkDelete(paths);
          return;
        }
        if (ev.key === 'Enter' && sel.size === 1) {
          ev.preventDefault();
          selectPath(Array.from(sel)[0], true);
          return;
        }
        if (ev.key === 'Escape' && sel.size > 0) {
          ev.preventDefault();
          applySelection(new Set(), 'escape-clear');
          return;
        }
      }

      // Ctrl/Cmd shortcuts.
      if (cmd && !ev.altKey) {
        if (ev.key === 'a' || ev.key === 'A') {
          // Select-all = every currently-visible row in the tree.
          ev.preventDefault();
          applySelection(new Set(visibleRows().map((r) => r.path)), 'select-all');
          return;
        }
        if ((ev.key === 'N' || ev.key === 'n') && ev.shiftKey) {
          // Ctrl+Shift+N = new folder, matching Chrome's "new
          // incognito window" muscle memory in reverse.
          ev.preventDefault();
          startNewFolder();
          return;
        }
        if (ev.key === 'i' || ev.key === 'I') {
          // Ctrl/Cmd+I = show/hide the preview/info dock (Finder's
          // "Get Info" muscle memory).
          ev.preventDefault();
          togglePreview();
          return;
        }
        // Ctrl+C / Ctrl+X / Ctrl+V — files clipboard. The
        // "copy path text" affordance moved to the right-click
        // context menu's "Copy path" item, freeing Ctrl+C for
        // the native file-clipboard meaning.
        if (ev.key === 'c' || ev.key === 'C') {
          const paths = pickSelectionPaths();
          if (paths.length === 0) return;
          ev.preventDefault();
          putFilesOnClipboard('copy', paths);
          setStatusInfo(`copied ${paths.length} to clipboard`);
          return;
        }
        if (ev.key === 'x' || ev.key === 'X') {
          const paths = pickSelectionPaths();
          if (paths.length === 0) return;
          ev.preventDefault();
          putFilesOnClipboard('cut', paths);
          setStatusInfo(`cut ${paths.length} to clipboard`);
          return;
        }
        if (ev.key === 'v' || ev.key === 'V') {
          const cb = filesClipboard();
          if (!cb || cb.paths.length === 0) return;
          ev.preventDefault();
          pasteFilesClipboard();
          return;
        }
      }
    };
    // Click-outside dismissal is owned by the Menu component
    // itself ([[@wash/ui menu]]); no host-level handler is needed.
    props.host.addEventListener('wash:msg', onMsg);
    props.host.addEventListener('wash:state', onState);
    props.host.addEventListener('keydown', onKey);
    if (!props.host.hasAttribute('tabindex')) props.host.setAttribute('tabindex', '0');
    // Track the body width: the dock clamp (effPreviewW) and the
    // responsive tree columns (cols) both key off it, so they react to
    // window resizes and splitter drags without polling.
    const ro = new ResizeObserver((entries) => {
      for (const e of entries) setBodyW(e.contentRect.width);
    });
    if (bodyEl) {
      setBodyW(bodyEl.clientWidth);
      ro.observe(bodyEl);
    }
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('wash:state', onState);
      props.host.removeEventListener('keydown', onKey);
      ro.disconnect();
    });
  });

  // ---- view ----

  return (
    <>
      {/* toolbar */}
      <div style={toolbarStyle}>
        <button type="button" data-testid="fm-home" title="Home" style={iconBtnStyle} onClick={goHome}>
          <HomeIcon size={14} />
        </button>
        <button type="button" data-testid="fm-back" title="Back" style={iconBtnStyle} onClick={goBack}>
          <ArrowLeft size={14} />
        </button>
        <button type="button" data-testid="fm-forward" title="Forward" style={iconBtnStyle} onClick={goForward}>
          <ArrowRight size={14} />
        </button>
        <button type="button" data-testid="fm-up" title="Up" style={iconBtnStyle} onClick={goUp}>
          <ArrowUp size={14} />
        </button>
        <input
          ref={pathInputEl!}
          type="text"
          data-testid="fm-path"
          spellcheck={false}
          placeholder="path"
          style={pathInputStyle}
          value={pathInputValue()}
          onInput={(e) => onPathInput(e.currentTarget.value)}
          onKeyDown={onPathKey}
          onBlur={() => setTimeout(closeCompleteDropdown, 100)}
        />
        <button
          type="button"
          data-testid="fm-reload"
          title="Reload"
          style={iconBtnStyle}
          onClick={() => { if (path()) invalidateAndList(parentPath(path())); }}
        >
          <RotateCw size={14} />
        </button>
        <button
          type="button"
          data-testid="fm-new-file"
          title="New file"
          style={iconBtnStyle}
          onClick={startNewFile}
        >
          <FilePlus size={14} />
        </button>
        <button
          type="button"
          data-testid="fm-new-folder"
          title="New folder"
          style={iconBtnStyle}
          onClick={startNewFolder}
        >
          <FolderPlus size={14} />
        </button>
        <button
          type="button"
          data-testid="fm-upload"
          title="Upload files"
          style={iconBtnStyle}
          onClick={() => fileInputEl.click()}
        >
          <Upload size={14} />
        </button>
        <button
          type="button"
          data-testid="fm-upload-folder"
          title="Upload folder"
          style={iconBtnStyle}
          onClick={() => dirInputEl.click()}
        >
          <FolderUp size={14} />
        </button>
        <button type="button" data-testid="fm-sort" title="Sort" style={iconBtnStyle} onClick={openSortMenu}>
          <ArrowUpDown size={14} />
        </button>
        <button
          type="button"
          data-testid="fm-toggle-preview"
          aria-pressed={previewOpen()}
          title={previewOpen() ? 'Hide preview (Ctrl+I)' : 'Show preview (Ctrl+I)'}
          style={previewOpen() ? iconBtnStyle : { ...iconBtnStyle, opacity: 0.5 }}
          onClick={togglePreview}
        >
          {previewOpen() ? <PanelRightClose size={14} /> : <PanelRightOpen size={14} />}
        </button>
        {/* Hidden native pickers backing the Upload buttons — the only
            way to read OS files from the browser. */}
        <input
          ref={fileInputEl!}
          type="file"
          multiple
          data-testid="fm-upload-input"
          style={{ display: 'none' }}
          onChange={(e) => onUploadInput(e.currentTarget)}
        />
        <input
          ref={dirInputEl!}
          type="file"
          data-testid="fm-upload-folder-input"
          style={{ display: 'none' }}
          onChange={(e) => onUploadInput(e.currentTarget)}
        />
      </div>

      {/* body: tree + splitter + preview/info */}
      <div
        ref={bodyEl!}
        style={{ ...bodyStyle, 'grid-template-columns': gridCols() }}
      >
        <div
          data-testid="fm-list"
          data-upload-active={uploadDropActive() ? 'true' : undefined}
          // External drag over empty list space → the whole pane is the
          // landing zone (drop = upload into the current dir). A solid
          // accent ring + faint blue wash makes that obvious, matching
          // the per-folder-row drop affordance.
          style={uploadDropActive()
            ? { ...treeStyle, 'box-shadow': `inset 0 0 0 2px ${tokens.borderDropTarget}`, background: tokens.bgDropTarget }
            : treeStyle}
          onDragOver={onListDragOver}
          onDragLeave={onListDragLeave}
          onDrop={onListDrop}
          onClick={(ev) => {
            // Background click clears the selection — native FM
            // convention. Only fire when the click hit the list
            // container itself (not a row that bubbled up).
            if (ev.target === ev.currentTarget && !ev.shiftKey && !ev.ctrlKey && !ev.metaKey) {
              applySelection(new Set(), 'background-clear');
              selectionAnchor = null;
            }
          }}
        >
          <ColumnHeader
            sortKey={sortKey()}
            sortDesc={sortDesc()}
            cols={cols()}
            onSort={(k) => {
              if (sortKey() === k) setSortDesc(!sortDesc());
              else { setSortKey(k); setSortDesc(false); }
              persist();
            }}
          />
          <Show when={pendingNew()}>
            <PendingNewRow
              kind={pendingNew()!.kind}
              parent={pendingNew()!.parent}
              draft={pendingNew()!.draft}
              onInput={(v) => {
                const cur = pendingNew();
                if (cur) setPendingNew({ ...cur, draft: v });
              }}
              onCommit={commitNew}
              onCancel={cancelNew}
            />
          </Show>
          <Show when={rootInitialized() && visibleRows().length === 0 && !pendingNew()}>
            <div
              data-testid="fm-empty"
              style={{
                padding: '20px 16px',
                color: tokens.fgDim,
                'font-style': 'italic',
                'font-size': '13px',
              }}
            >
              (empty folder)
            </div>
          </Show>
          <For each={visibleRows()}>
            {(row) => {
              const isRenaming = () => renaming()?.path === row.path;
              return <TreeRow
                entry={row.entry}
                path={row.path}
                depth={row.depth}
                childCount={row.childCount}
                cols={cols()}
                selected={selection().has(row.path)}
                isCurrent={path() === row.path}
                expanded={!!expanded[row.path]}
                renaming={isRenaming() ? { draft: renaming()!.draft } : null}
                onRenameInput={(v) => {
                  const r = renaming();
                  if (r) setRenaming({ ...r, draft: v });
                }}
                onRenameCommit={commitRename}
                onRenameCancel={cancelRename}
                onClick={(ev) => {
                  if (isRenaming()) return;
                  onRowClick(row.path, row.entry, ev);
                }}
                onDblClick={() => {
                  // Native dblclick — the browser's timing window
                  // avoids catching pairs of intentional single
                  // clicks as double-clicks on slow input. Single-click
                  // navigation never happens here; this is the
                  // only path that calls selectPath for a row.
                  if (row.entry.type === 'symlink') {
                    followSymlink(row.entry, row.path);
                    return;
                  }
                  if (row.entry.type === 'dir') {
                    selectPath(row.path, true);
                    return;
                  }
                }}
                onToggle={() => toggleExpand(row.path)}
                onContextMenu={(ev) => openContextMenu(ev, row.entry, row.path)}
                onDragStart={(ev) => onDragStart(ev, row.path)}
                onDragEnd={onDragEnd}
                isDropTarget={dropTargetPath() === row.path}
                onDragOver={row.entry.type === 'dir' ? (ev) => onRowDragOver(ev, row.path) : undefined}
                onDrop={row.entry.type === 'dir' ? (ev) => onRowDrop(ev, row.path) : undefined}
              />;
            }}
          </For>
        </div>
        <Show when={previewOpen()}>
          <Splitter
            container={bodyEl}
            min={5}
            max={95}
            onChange={onSplitChange}
            onCommit={persist}
            data-testid="fm-splitter"
          />
          <div
            data-testid="fm-preview-dock"
            style={{ display: 'grid', 'grid-template-rows': 'auto 1fr', overflow: 'hidden', 'min-width': 0 }}
          >
            <InfoSection
              open={infoOpen()}
              onToggle={toggleInfo}
              entry={selectedEntry()}
              path={path()}
              onChmod={commitChmod}
              onChown={commitChown}
            />
            <PreviewPane content={previewContent()} />
          </div>
        </Show>
      </div>

      {/* status */}
      <StatusBar data-testid="fm-status">{statusBar()}</StatusBar>

      {/* overlays */}
      <Show when={completeOpen() && completeMatches().length > 0}>
        <AutocompleteDropdown
          host={props.host}
          anchor={pathInputEl}
          matches={completeMatches()}
          idx={completeIdx()}
          onHover={(i) => setCompleteIdx(i)}
          onPick={(i) => pickCompletion(i, true)}
        />
      </Show>

      <Show when={menu()?.kind === 'sort'}>
        <SortMenu
          left={(menu() as { left: number }).left}
          top={(menu() as { top: number }).top}
          sortKey={sortKey()}
          sortDesc={sortDesc()}
          showHidden={showHidden()}
          onDismiss={closeMenu}
          onPick={(k) => {
            if (sortKey() === k) setSortDesc(!sortDesc());
            else { setSortKey(k); setSortDesc(false); }
            closeMenu();
            persist();
          }}
          onToggleHidden={() => {
            setShowHidden(!showHidden());
            closeMenu();
            persist();
          }}
        />
      </Show>

      <Show when={menu()?.kind === 'context'}>
        <ContextMenu
          left={(menu() as { left: number }).left}
          top={(menu() as { top: number }).top}
          entry={(menu() as { entry: Entry }).entry}
          path={(menu() as { path: string }).path}
          onDismiss={closeMenu}
          onOpen={() => {
            const m = menu() as { entry: Entry; path: string };
            closeMenu();
            if (m.entry.type === 'symlink') followSymlink(m.entry, m.path);
            else selectPath(m.path, true);
          }}
          onCopy={() => {
            const m = menu() as { path: string };
            closeMenu();
            send({ kind: 'clipboard_copy_path', path: m.path });
          }}
          downloadLabel={(() => {
            const p = (menu() as { path: string }).path;
            const sel = selection();
            return sel.size >= 2 && sel.has(p) ? `Download ${sel.size} items` : 'Download';
          })()}
          onDownload={() => {
            const m = menu() as { path: string };
            closeMenu();
            // Mirror Delete: a 2+ selection that includes the clicked row
            // downloads the whole set (zipped); otherwise just the row.
            const sel = selection();
            if (sel.size >= 2 && sel.has(m.path)) {
              void runDownload(Array.from(sel));
            } else {
              void runDownload([m.path]);
            }
          }}
          onInfo={() => {
            closeMenu();
            if (!infoOpen()) toggleInfo();
          }}
          onRename={() => {
            const m = menu() as { path: string };
            closeMenu();
            startRename(m.path);
          }}
          onDelete={() => {
            const m = menu() as { path: string };
            closeMenu();
            // If the right-clicked row is part of a 2+ selection,
            // act on the whole selection; otherwise just the row.
            const sel = selection();
            if (sel.size >= 2 && sel.has(m.path)) {
              requestBulkDelete(Array.from(sel));
            } else {
              requestDelete(m.path);
            }
          }}
        />
      </Show>

      <Show when={confirmDelete()}>
        <ConfirmDeleteOverlay
          name={confirmDelete()!.name}
          path={confirmDelete()!.path}
          onCancel={cancelDelete}
          onConfirm={performDelete}
        />
      </Show>

      <Show when={replaceConfirm()}>
        <ReplaceConfirmOverlay
          dst={replaceConfirm()!.dst}
          entry={replaceConfirm()!.entry}
          onConfirm={() => {
            const c = replaceConfirm();
            setReplaceConfirm(null);
            c?.resolve(true);
          }}
          onCancel={() => {
            const c = replaceConfirm();
            setReplaceConfirm(null);
            c?.resolve(false);
          }}
        />
      </Show>

      <Show when={uploadConflict()}>
        <UploadConflictOverlay
          existing={uploadConflict()!.existing}
          total={uploadConflict()!.total}
          onReplace={() => {
            const c = uploadConflict();
            setUploadConflict(null);
            c?.resolve('replace');
          }}
          onSkip={() => {
            const c = uploadConflict();
            setUploadConflict(null);
            c?.resolve('skip');
          }}
          onCancel={() => {
            const c = uploadConflict();
            setUploadConflict(null);
            c?.resolve(null);
          }}
        />
      </Show>

      <Show when={dropMenu()}>
        <DropMenu
          x={dropMenu()!.x}
          y={dropMenu()!.y}
          srcs={dropMenu()!.srcs}
          targetDir={dropMenu()!.targetDir}
          onMove={() => {
            const dm = dropMenu()!;
            setDropMenu(null);
            handleMoveDrop(dm.srcs, dm.targetDir);
          }}
          onCopy={() => {
            const dm = dropMenu()!;
            setDropMenu(null);
            commitBulkCopy(dm.srcs, dm.targetDir);
          }}
          onSymlink={() => {
            const dm = dropMenu()!;
            setDropMenu(null);
            // Single-shot symlink stays fm-direct (Replace prompt
            // per item). For multi, iterate — each goes through
            // its own commitSymlink with its own Replace prompt.
            for (const src of dm.srcs) {
              void commitSymlink(src, dm.targetDir);
            }
          }}
          onCancel={() => setDropMenu(null)}
        />
      </Show>
    </>
  );
};

// ---- sub-components ----

// colsFor (below) defines the responsive column layout shared by
// ColumnHeader and each TreeRow. The metadata columns are tuned so the
// standard human-size (e.g. "999.9 KB") + date strings ("Dec 15 14:32")
// fit without truncating, and drop out as the tree narrows so the Name
// column keeps its width. Column geometry stays fm-specific (don't
// extract to @wash/ui — apps with different shapes shouldn't share it).
const COL_DATE_W = 96;
const COL_SIZE_W = 76;

// Preview/info dock geometry (see the previewW/previewOpen signals).
const PREVIEW_DEFAULT_W = 320; // modest fixed dock — not half the window
const PREVIEW_MIN_W = 220;
const TREE_MIN_W = 240; // the tree never shrinks below this when docked
const SPLITTER_W = 4;

// ColCfg drives responsive tree columns: as the tree narrows we drop the
// metadata columns (Created first, then Modified, then Size) so the Name
// column keeps its width instead of ellipsizing to fit dates. The 0 case
// (width not yet measured) shows everything so the first paint isn't sparse.
interface ColCfg {
  template: string;
  size: boolean;
  mtime: boolean;
  ctime: boolean;
}
function colsFor(w: number): ColCfg {
  if (w === 0 || w >= 560)
    return { template: `1fr ${COL_SIZE_W}px ${COL_DATE_W}px ${COL_DATE_W}px`, size: true, mtime: true, ctime: true };
  if (w >= 440) return { template: `1fr ${COL_SIZE_W}px ${COL_DATE_W}px`, size: true, mtime: true, ctime: false };
  if (w >= 340) return { template: `1fr ${COL_SIZE_W}px`, size: true, mtime: false, ctime: false };
  return { template: '1fr', size: false, mtime: false, ctime: false };
}

// HEADER_ROW_H matches the column-header strip to the InfoSection
// toggle so the two top rows line up across the splitter. The 1px
// border-bottom on each parent adds to this for the visual stripe.
const HEADER_ROW_H = 22;

// ColumnHeader is the sticky header strip above the tree: Name |
// Modified | Created | Size. Clicking a header sorts by that
// column; clicking the active one toggles direction. Stays in
// sync with the sort menu since both drive the same
// sortKey/sortDesc state.
const ColumnHeader: Component<{
  sortKey: SortKey;
  sortDesc: boolean;
  cols: ColCfg;
  onSort: (k: SortKey) => void;
}> = (props) => {
  const arrow = (k: SortKey): JSX.Element => {
    if (props.sortKey !== k) return null;
    return props.sortDesc ? <ChevronDown size={11} /> : <ChevronUp size={11} />;
  };
  const cell = (label: string, k: SortKey, align: 'left' | 'right'): JSX.Element => (
    <button
      type="button"
      data-testid={`fm-header-${k}`}
      onClick={() => props.onSort(k)}
      style={{
        background: 'transparent',
        border: 'none',
        color: tokens.fgMuted,
        font: `11px ${tokens.fontSans}`,
        cursor: 'pointer',
        padding: '0 8px',
        height: `${HEADER_ROW_H}px`,
        'box-sizing': 'border-box',
        display: 'flex',
        'align-items': 'center',
        gap: '4px',
        'justify-content': align === 'right' ? 'flex-end' : 'flex-start',
      }}
    >
      <span>{label}</span>
      {arrow(k)}
    </button>
  );
  return (
    <div
      data-testid="fm-column-header"
      style={{
        position: 'sticky',
        top: 0,
        background: tokens.bgMenu,
        'border-bottom': `1px solid ${tokens.borderMenu}`,
        display: 'grid',
        'grid-template-columns': props.cols.template,
        'z-index': 2,
        'user-select': 'none',
      }}
    >
      {cell('Name', 'name', 'left')}
      <Show when={props.cols.size}>{cell('Size', 'size', 'right')}</Show>
      <Show when={props.cols.mtime}>{cell('Modified', 'mtime', 'right')}</Show>
      <Show when={props.cols.ctime}>{cell('Created', 'ctime', 'right')}</Show>
    </div>
  );
};

const TreeRow: Component<{
  entry: Entry;
  path: string;
  depth: number;
  childCount?: number;
  cols: ColCfg;
  selected: boolean;
  isCurrent: boolean;
  expanded: boolean;
  // When `renaming` is set, the name span becomes a focused input
  // bound to `renaming.draft`. Enter commits, Escape cancels, blur
  // commits. Clicks on the row are suppressed while editing.
  renaming?: { draft: string } | null;
  onRenameInput?: (val: string) => void;
  onRenameCommit?: () => void;
  onRenameCancel?: () => void;
  onClick: (ev: MouseEvent) => void;
  onDblClick?: () => void;
  onToggle: () => void;
  onContextMenu: (ev: MouseEvent) => void;
  onDragStart: (ev: DragEvent) => void;
  onDragEnd?: () => void;
  // Drop-target handlers. Only wired on directory rows; file rows
  // omit them so the browser auto-rejects drops with not-allowed.
  isDropTarget?: boolean;
  onDragOver?: (ev: DragEvent) => void;
  onDrop?: (ev: DragEvent) => void;
}> = (props) => {
  const [hover, setHover] = createSignal(false);
  return (
    <div
      data-testid={`fm-entry-${props.entry.name}`}
      data-type={props.entry.type}
      data-path={props.path}
      data-selected={props.selected ? 'true' : undefined}
      data-drop-target={props.isDropTarget ? 'true' : undefined}
      draggable="true"
      onDragStart={props.onDragStart}
      onDragEnd={props.onDragEnd}
      onDragOver={props.onDragOver}
      onDrop={props.onDrop}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      onClick={(ev) => props.onClick(ev)}
      onDblClick={() => props.onDblClick?.()}
      onContextMenu={props.onContextMenu}
      style={{
        display: 'grid',
        'grid-template-columns': props.cols.template,
        'align-items': 'center',
        padding: '3px 8px',
        background: props.isDropTarget
          ? tokens.bgDropTarget
          : props.selected
          ? tokens.bgRowSelected
          : hover()
          ? tokens.bgRowHover
          : 'transparent',
        color: tokens.fg,
        cursor: 'pointer',
        'user-select': 'none',
        font: `13px ${tokens.fontSans}`,
        // A solid accent ring (inset so it isn't clipped by the row
        // bounds) makes the landing folder pop out unmistakably from a
        // merely-selected row during a drag.
        'box-shadow': props.isDropTarget ? `inset 0 0 0 2px ${tokens.borderDropTarget}` : 'none',
        outline: 'none',
      }}
    >
      {/* name cell — chevron + icon + name, indented by depth */}
      <span style={{
        display: 'flex',
        'align-items': 'center',
        gap: '4px',
        'padding-left': `${props.depth * 12}px`,
        overflow: 'hidden',
      }}>
      <span
        data-testid={`fm-chevron-${props.entry.name}`}
        style={{ width: '12px', display: 'inline-flex', 'align-items': 'center', 'justify-content': 'center', opacity: 0.6, cursor: 'pointer', 'flex-shrink': 0 }}
        onClick={(ev) => {
          if (props.entry.type === 'dir') {
            ev.stopPropagation();
            props.onToggle();
          }
        }}
      >
        <Show when={props.entry.type === 'dir'}>
          {props.expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </Show>
      </span>
      <span style={{ width: '14px', display: 'inline-flex', 'align-items': 'center', 'justify-content': 'center', opacity: 0.8, 'flex-shrink': 0 }}>
        <EntryIcon entry={props.entry} />
      </span>
      <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap', 'font-weight': props.isCurrent ? 'bold' : 'normal' }}>
        <Show
          when={props.renaming}
          fallback={props.entry.name}
        >
          <input
            data-testid="fm-rename-input"
            ref={(el) => setTimeout(() => { el.focus(); el.select(); }, 0)}
            type="text"
            value={props.renaming!.draft}
            onInput={(e) => props.onRenameInput?.(e.currentTarget.value)}
            onClick={(e) => e.stopPropagation()}
            onKeyDown={(e) => {
              e.stopPropagation();
              if (e.key === 'Enter') {
                e.preventDefault();
                props.onRenameCommit?.();
              } else if (e.key === 'Escape') {
                e.preventDefault();
                props.onRenameCancel?.();
              }
            }}
            onBlur={() => props.onRenameCommit?.()}
            style={inlineInputStyle}
          />
        </Show>
      </span>
      </span>
      {/* Size / item count */}
      <Show when={props.cols.size}>
        <span style={cellNumStyle}>
          {props.renaming ? '' : sizeOrCount(props.entry, props.childCount)}
        </span>
      </Show>
      {/* Modified */}
      <Show when={props.cols.mtime}>
        <span style={cellNumStyle}>
          {!props.renaming ? formatDate(props.entry.mod_unix) : ''}
        </span>
      </Show>
      {/* Created */}
      <Show when={props.cols.ctime}>
        <span style={cellNumStyle}>
          {!props.renaming ? formatDate(props.entry.created_unix) : ''}
        </span>
      </Show>
    </div>
  );
};

const cellNumStyle: JSX.CSSProperties = {
  opacity: 0.6,
  font: `11px ${tokens.fontMono}`,
  'text-align': 'right',
  'white-space': 'nowrap',
  overflow: 'hidden',
};

function sizeOrCount(entry: Entry, childCount?: number): string {
  if (entry.type === 'file') return humanSize(entry.size);
  if (entry.type === 'dir') {
    if (childCount === undefined) return '';
    return childCount === 1 ? '1 item' : `${childCount} items`;
  }
  return '';
}

// PendingNewRow — synthetic row rendered above the tree when the
// user has clicked New File / New Folder. It mirrors the look of a
// real row but only carries an input; on commit the BE creates the
// real entry and this row is replaced with the actual TreeRow.
const PendingNewRow: Component<{
  kind: 'file' | 'folder';
  parent: string;
  draft: string;
  onInput: (val: string) => void;
  onCommit: () => void;
  onCancel: () => void;
}> = (props) => {
  return (
    <div
      data-testid={`fm-pending-new-${props.kind}`}
      data-parent={props.parent}
      style={{
        display: 'flex',
        'align-items': 'center',
        gap: '4px',
        padding: '3px 8px 3px 8px',
        background: tokens.bgRowSelected,
        color: tokens.fg,
        font: `13px ${tokens.fontSans}`,
      }}
    >
      <span style={{ width: '12px' }} />
      <span style={{ width: '14px', display: 'inline-flex', 'align-items': 'center', 'justify-content': 'center', opacity: 0.8 }}>
        {props.kind === 'folder' ? <FolderIcon size={12} /> : <FileIcon size={12} />}
      </span>
      <input
        data-testid="fm-pending-new-input"
        ref={(el) => setTimeout(() => el.focus(), 0)}
        type="text"
        value={props.draft}
        placeholder={props.kind === 'folder' ? 'new folder name' : 'new file name'}
        onInput={(e) => props.onInput(e.currentTarget.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            props.onCommit();
          } else if (e.key === 'Escape') {
            e.preventDefault();
            props.onCancel();
          }
        }}
        style={{ ...inlineInputStyle, flex: 1 }}
      />
    </div>
  );
};

const inlineInputStyle: JSX.CSSProperties = {
  background: tokens.bgMenu,
  color: tokens.fg,
  border: `1px solid ${tokens.borderFocus}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '2px 6px',
  font: `12px ${tokens.fontMono}`,
  outline: 'none',
  width: '100%',
  'box-sizing': 'border-box',
};

type PreviewContent = { binary: boolean; size: number; text: string; truncated: boolean };

const PreviewPane: Component<{ content: PreviewContent | null }> = (props) => {
  return (
    <div data-testid="fm-preview" style={previewStyle}>
      <Show when={props.content} fallback="(select a file to preview)">
        {(c) => (
          <>
            {c().binary
              ? `(binary, ${humanSize(c().size)})`
              : c().text + (c().truncated ? `\n\n--- truncated (${humanSize(c().size)} total) ---` : '')}
          </>
        )}
      </Show>
    </div>
  );
};

const InfoSection: Component<{
  open: boolean;
  onToggle: () => void;
  entry: Entry | null;
  path: string;
  onChmod: (path: string, modeText: string) => void;
  onChown: (path: string, field: 'owner' | 'group', value: string) => void;
}> = (props) => {
  // Tracks which field is currently in edit mode. Only one field
  // editable at a time keeps the UX simple — Enter/Escape on the
  // input close it; clicking another field implicitly cancels.
  const [editing, setEditing] = createSignal<'perm' | 'owner' | 'group' | null>(null);
  const [draft, setDraft] = createSignal('');

  const startEdit = (field: 'perm' | 'owner' | 'group', initial: string) => {
    setEditing(field);
    setDraft(initial);
  };

  const cancel = () => setEditing(null);

  const commit = () => {
    const e = props.entry;
    if (!e) {
      setEditing(null);
      return;
    }
    const which = editing();
    const value = draft();
    setEditing(null);
    if (!which) return;
    if (which === 'perm') props.onChmod(props.path, value);
    else props.onChown(props.path, which, value);
  };

  return (
    <div style={{ 'border-bottom': `1px solid ${tokens.borderMenu}`, background: tokens.bgMenu }}>
      <button
        type="button"
        data-testid="fm-info-toggle"
        style={{ ...infoToggleStyle, display: 'flex', 'align-items': 'center', gap: '6px' }}
        onClick={props.onToggle}
      >
        {props.open ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
        <span>Info</span>
      </button>
      <Show when={props.open}>
        <div data-testid="fm-info" style={infoBodyStyle}>
          <Show when={props.entry} fallback="(no selection)">
            {(e) => {
              const entry = e();
              const ownerDisplay = entry.owner ? `${entry.owner} (${entry.uid})` : String(entry.uid);
              const groupDisplay = entry.group ? `${entry.group} (${entry.gid})` : String(entry.gid);
              return (
                <>
                  <StaticRow k="Path" v={props.path} />
                  <StaticRow k="Type" v={entry.type} />
                  <StaticRow k="Size" v={humanSize(entry.size)} />
                  <StaticRow k="Modified" v={new Date(entry.mod_unix * 1000).toLocaleString()} />
                  <EditableRow
                    testid="fm-info-perm"
                    label="Permissions"
                    display={`${entry.perm} (${octalPerm(entry.mode)})`}
                    editing={editing() === 'perm'}
                    draft={editing() === 'perm' ? draft() : ''}
                    placeholder="octal e.g. 0755"
                    onStart={() => startEdit('perm', octalPerm(entry.mode))}
                    onDraft={setDraft}
                    onCommit={commit}
                    onCancel={cancel}
                  />
                  <EditableRow
                    testid="fm-info-owner"
                    label="Owner"
                    display={ownerDisplay}
                    editing={editing() === 'owner'}
                    draft={editing() === 'owner' ? draft() : ''}
                    placeholder="username or uid"
                    onStart={() => startEdit('owner', entry.owner || String(entry.uid))}
                    onDraft={setDraft}
                    onCommit={commit}
                    onCancel={cancel}
                  />
                  <EditableRow
                    testid="fm-info-group"
                    label="Group"
                    display={groupDisplay}
                    editing={editing() === 'group'}
                    draft={editing() === 'group' ? draft() : ''}
                    placeholder="group name or gid"
                    onStart={() => startEdit('group', entry.group || String(entry.gid))}
                    onDraft={setDraft}
                    onCommit={commit}
                    onCancel={cancel}
                  />
                  <Show when={entry.type === 'symlink'}>
                    <StaticRow k="Link target" v={entry.link_to ?? `(${entry.link_err ?? 'unresolved'})`} />
                  </Show>
                </>
              );
            }}
          </Show>
        </div>
      </Show>
    </div>
  );
};

// StaticRow is the non-editable row used for path/type/size/etc.
const StaticRow: Component<{ k: string; v: string }> = (props) => (
  <div style={{ display: 'flex', gap: '10px', padding: '2px 0' }}>
    <span style={{ width: '110px', opacity: 0.6, 'flex-shrink': 0 }}>{props.k}</span>
    <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>{props.v}</span>
  </div>
);

// EditableRow renders a key/value pair where the value swaps into
// an input on click. Enter commits, Escape cancels, blur commits.
// Same UX as inline rename in the tree; consistent for the user.
const EditableRow: Component<{
  testid: string;
  label: string;
  display: string;
  editing: boolean;
  draft: string;
  placeholder: string;
  onStart: () => void;
  onDraft: (val: string) => void;
  onCommit: () => void;
  onCancel: () => void;
}> = (props) => {
  return (
    <div data-testid={props.testid} style={{ display: 'flex', gap: '10px', padding: '2px 0', 'align-items': 'center' }}>
      <span style={{ width: '110px', opacity: 0.6, 'flex-shrink': 0 }}>{props.label}</span>
      <Show
        when={props.editing}
        fallback={
          <span
            data-testid={`${props.testid}-value`}
            onClick={props.onStart}
            style={{
              flex: 1,
              cursor: 'pointer',
              overflow: 'hidden',
              'text-overflow': 'ellipsis',
              'white-space': 'nowrap',
              padding: '1px 4px',
              'border-radius': `${tokens.radiusSm}`,
              'border-bottom': `1px dashed ${tokens.borderMenu}`,
            }}
            title="click to edit"
          >
            {props.display}
          </span>
        }
      >
        <input
          data-testid={`${props.testid}-input`}
          ref={(el) => setTimeout(() => { el.focus(); el.select(); }, 0)}
          type="text"
          value={props.draft}
          placeholder={props.placeholder}
          onInput={(e) => props.onDraft(e.currentTarget.value)}
          onKeyDown={(e) => {
            e.stopPropagation();
            if (e.key === 'Enter') {
              e.preventDefault();
              props.onCommit();
            } else if (e.key === 'Escape') {
              e.preventDefault();
              props.onCancel();
            }
          }}
          onBlur={() => props.onCommit()}
          style={inlineInputStyle}
        />
      </Show>
    </div>
  );
};

const SortMenu: Component<{
  left: number;
  top: number;
  sortKey: SortKey;
  sortDesc: boolean;
  showHidden: boolean;
  onPick: (k: SortKey) => void;
  onToggleHidden: () => void;
  onDismiss: () => void;
}> = (props) => {
  const arrow = (k: SortKey): JSX.Element => {
    if (props.sortKey !== k) return null;
    return props.sortDesc ? <ChevronDown size={12} /> : <ChevronUp size={12} />;
  };
  return (
    <Menu data-testid="fm-sort-menu" x={props.left} y={props.top} onDismiss={props.onDismiss}>
      <MenuItem data-testid="fm-sort-name" label="Name" trailing={arrow('name')} onClick={() => props.onPick('name')} />
      <MenuItem data-testid="fm-sort-mtime" label="Modified" trailing={arrow('mtime')} onClick={() => props.onPick('mtime')} />
      <MenuItem data-testid="fm-sort-ctime" label="Created" trailing={arrow('ctime')} onClick={() => props.onPick('ctime')} />
      <MenuItem data-testid="fm-sort-size" label="Size" trailing={arrow('size')} onClick={() => props.onPick('size')} />
      <MenuItem data-testid="fm-sort-type" label="Type" trailing={arrow('type')} onClick={() => props.onPick('type')} />
      <MenuSeparator />
      <MenuItem
        data-testid="fm-show-hidden"
        label="Show hidden"
        trailing={props.showHidden ? <Check size={12} /> : <Square size={12} />}
        onClick={props.onToggleHidden}
      />
    </Menu>
  );
};

const ContextMenu: Component<{
  left: number;
  top: number;
  entry: Entry;
  path: string;
  onOpen: () => void;
  onCopy: () => void;
  onInfo: () => void;
  onDownload: () => void;
  downloadLabel: string;
  onRename: () => void;
  onDelete: () => void;
  onDismiss: () => void;
}> = (props) => {
  return (
    <Menu data-testid="fm-context-menu" x={props.left} y={props.top} onDismiss={props.onDismiss}>
      <MenuItem data-testid="fm-ctx-open" label="Open" onClick={props.onOpen} />
      <MenuItem data-testid="fm-ctx-copy" label="Copy path" onClick={props.onCopy} />
      <MenuItem data-testid="fm-ctx-download" label={props.downloadLabel} onClick={props.onDownload} />
      <MenuItem data-testid="fm-ctx-info" label="Show info" onClick={props.onInfo} />
      <MenuSeparator />
      <MenuItem data-testid="fm-ctx-rename" label="Rename" onClick={props.onRename} />
      <MenuItem data-testid="fm-ctx-delete" label="Delete" onClick={props.onDelete} />
    </Menu>
  );
};

// ConfirmDeleteOverlay — destructive-delete confirm modal.
const ConfirmDeleteOverlay: Component<{
  name: string;
  path: string;
  onCancel: () => void;
  onConfirm: () => void;
}> = (props) => {
  return (
    <ConfirmDialog
      data-testid="fm-confirm-delete"
      title="Delete?"
      confirmLabel="Delete"
      confirmTestid="fm-confirm-delete-yes"
      cancelTestid="fm-confirm-delete-cancel"
      danger
      onCancel={props.onCancel}
      onConfirm={props.onConfirm}
    >
      <div
        data-testid="fm-confirm-delete-name"
        style={{
          font: `12px ${tokens.fontMono}`,
          opacity: 0.8,
          'word-break': 'break-all',
        }}
      >
        {props.path}
      </div>
    </ConfirmDialog>
  );
};

// ReplaceConfirmOverlay — same modal scaffold as delete-confirm,
// shown when rename / move / symlink would overwrite an existing
// entry. Surfaces the dst path AND the entry's type+size so the
// user knows what they're about to lose; that detail is the
// whole reason the prompt exists.
const ReplaceConfirmOverlay: Component<{
  dst: string;
  entry: Entry | null;
  onCancel: () => void;
  onConfirm: () => void;
}> = (props) => {
  const detail = () => {
    const e = props.entry;
    if (!e) return '';
    if (e.type === 'dir') return 'folder';
    if (e.type === 'symlink') return 'symlink';
    if (e.type === 'file') return `file, ${humanSize(e.size)}`;
    return e.type;
  };
  return (
    <ConfirmDialog
      data-testid="fm-confirm-replace"
      title="Replace?"
      confirmLabel="Replace"
      confirmTestid="fm-confirm-replace-yes"
      cancelTestid="fm-confirm-replace-cancel"
      danger
      onCancel={props.onCancel}
      onConfirm={props.onConfirm}
    >
      <div
        data-testid="fm-confirm-replace-path"
        style={{
          font: `12px ${tokens.fontMono}`,
          opacity: 0.8,
          'word-break': 'break-all',
        }}
      >
        {props.dst}
      </div>
      <Show when={props.entry}>
        <div
          data-testid="fm-confirm-replace-detail"
          style={{ 'font-size': '11px', opacity: 0.6, 'margin-top': '4px' }}
        >
          {detail()}
        </div>
      </Show>
    </ConfirmDialog>
  );
};

// UploadConflictOverlay — the single pre-flight prompt shown when an
// upload would overwrite existing files. Three outcomes: Replace all
// (overwrite), Skip existing (upload only the new files), or Cancel
// (abort the whole upload). Resolving once up front avoids a
// per-file stall mid-transfer.
const UploadConflictOverlay: Component<{
  existing: number;
  total: number;
  onReplace: () => void;
  onSkip: () => void;
  onCancel: () => void;
}> = (props) => {
  const plural = (n: number) => (n === 1 ? '' : 's');
  return (
    <Overlay onDismiss={props.onCancel} data-testid="fm-upload-conflict">
      <div style={{ 'font-weight': 600, 'margin-bottom': '6px' }}>Files already exist</div>
      <div style={{ 'font-size': '12px', opacity: 0.8 }}>
        {props.existing} of {props.total} file{plural(props.total)} already exist at the destination.
      </div>
      <div style={{ display: 'flex', gap: '8px', 'justify-content': 'flex-end', 'margin-top': '14px' }}>
        <button type="button" data-testid="fm-upload-conflict-cancel" onClick={props.onCancel} style={uploadBtnStyle(false)}>
          Cancel
        </button>
        <button type="button" data-testid="fm-upload-conflict-skip" onClick={props.onSkip} style={uploadBtnStyle(false)}>
          Skip existing
        </button>
        <button type="button" data-testid="fm-upload-conflict-replace" onClick={props.onReplace} style={uploadBtnStyle(true)}>
          Replace all
        </button>
      </div>
    </Overlay>
  );
};

// uploadBtnStyle mirrors overlay.tsx's confirmBtnStyle (kept local —
// it's not exported from @wash/ui). danger tints the Replace action.
function uploadBtnStyle(danger: boolean): JSX.CSSProperties {
  return {
    background: danger ? tokens.bgDanger : 'transparent',
    color: tokens.fg,
    border: `1px solid ${danger ? tokens.borderDanger : tokens.borderMenu}`,
    'border-radius': `${tokens.radiusSm}`,
    padding: '5px 12px',
    cursor: 'pointer',
    font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  };
}

// DropMenu — the small context menu that appears when the user
// holds Alt while dropping a drag. Offers Move and Symlink (copy
// lives in the future bulk-ops app per [[wash-fm-dnd-plan]]).
//
// No backdrop overlay. The instrumentation pass revealed that a
// full-host backdrop at z-index 1900 was intercepting every mouse
// event despite the menu's higher z-index 1950 — the wash-app-fm
// host uses `display:grid`, and absolute-positioned children inside
// a grid container don't honor z-index against grid-placed
// siblings the way they do in a normal-flow container. We now use
// a document-level mousedown listener (same pattern the sort and
// context menus use) to close on outside click.
const DropMenu: Component<{
  x: number;
  y: number;
  srcs: string[];
  targetDir: string;
  onMove: () => void;
  onCopy: () => void;
  onSymlink: () => void;
  onCancel: () => void;
}> = (props) => {
  // zIndex 1950 — above the in-flight drop area, below modal
  // confirms. (Tokens names the value as zDropMenu; this matches
  // the historic layering decisions documented in [[wash fm DnD]].)
  return (
    <Menu data-testid="fm-drop-menu" x={props.x} y={props.y} zIndex={1950} onDismiss={props.onCancel}>
      <MenuItem data-testid="fm-drop-move" label="Move here" onClick={props.onMove} />
      <MenuItem data-testid="fm-drop-copy" label="Copy here" onClick={props.onCopy} />
      <MenuItem data-testid="fm-drop-symlink" label="Create symlink here" onClick={props.onSymlink} />
      <MenuSeparator />
      <MenuItem data-testid="fm-drop-cancel" label="Cancel" onClick={props.onCancel} />
    </Menu>
  );
};

const AutocompleteDropdown: Component<{
  host: HTMLElement;
  anchor: HTMLInputElement;
  matches: string[];
  idx: number;
  onHover: (i: number) => void;
  onPick: (i: number) => void;
}> = (props) => {
  const [pos, setPos] = createSignal({ left: 0, top: 0, width: 240 });
  onMount(() => {
    const rect = props.anchor.getBoundingClientRect();
    const my = props.host.getBoundingClientRect();
    setPos({ left: rect.left - my.left, top: rect.bottom - my.top + 2, width: rect.width });
  });
  return (
    <div
      data-testid="fm-complete"
      style={{
        position: 'absolute',
        background: tokens.bgMenu,
        border: `1px solid ${tokens.borderMenu}`,
        'border-radius': `${tokens.radiusMd}`,
        padding: '2px 0',
        'min-width': '240px',
        'max-height': '280px',
        'overflow-y': 'auto',
        'box-shadow': '0 8px 20px rgba(0,0,0,0.5)',
        'z-index': 1500,
        font: `12px ${tokens.fontMono}`,
        left: `${pos().left}px`,
        top: `${pos().top}px`,
        width: `${pos().width}px`,
      }}
    >
      <For each={props.matches}>
        {(match, i) => (
          <div
            data-testid={`fm-complete-${i()}`}
            style={{
              padding: '4px 8px',
              cursor: 'pointer',
              color: tokens.fg,
              background: i() === props.idx ? tokens.bgRowSelected : 'transparent',
              'white-space': 'nowrap',
              overflow: 'hidden',
              'text-overflow': 'ellipsis',
            }}
            onMouseEnter={() => props.onHover(i())}
            onMouseDown={(ev) => {
              ev.preventDefault();
              props.onPick(i());
            }}
          >
            {match}
          </div>
        )}
      </For>
    </div>
  );
};

// ---- styles ----

const toolbarStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '4px',
  padding: '0 8px',
  background: tokens.bgWindow,
};

const iconBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '4px 8px',
  cursor: 'pointer',
  font: `13px ${tokens.fontSans}`,
  'min-width': '30px',
};

const pathInputStyle: JSX.CSSProperties = {
  flex: 1,
  background: tokens.bgMenu,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '4px 8px',
  font: `12px ${tokens.fontSans}`,
  outline: 'none',
};

const bodyStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': '1fr 1fr',
  overflow: 'hidden',
  'border-top': `1px solid ${tokens.borderMenu}`,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
};

const treeStyle: JSX.CSSProperties = {
  overflow: 'auto',
  background: tokens.bgWindow,
  padding: '0 0 4px 0',
};

const previewStyle: JSX.CSSProperties = {
  overflow: 'auto',
  padding: '10px 12px',
  font: `12px ${tokens.fontMono}`,
  'white-space': 'pre',
  background: '#0c0c14',
};

const infoToggleStyle: JSX.CSSProperties = {
  display: 'block',
  width: '100%',
  height: `${HEADER_ROW_H}px`,
  'box-sizing': 'border-box',
  'text-align': 'left',
  background: 'transparent',
  color: tokens.fgMuted,
  border: 'none',
  padding: '0 8px',
  cursor: 'pointer',
  font: `11px ${tokens.fontSans}`,
};

const infoBodyStyle: JSX.CSSProperties = {
  padding: '8px 12px 12px',
  font: `12px ${tokens.fontMono}`,
  'border-top': `1px solid ${tokens.borderMenu}`,
};

// ---- helpers ----

const EntryIcon: Component<{ entry: Entry }> = (props) => {
  switch (props.entry.type) {
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

// Pure path/format helpers (joinPath, parentPath, baseName, humanSize,
// formatDate, octalPerm) live in ./paths.ts — extracted for node:test
// coverage and reuse.

// ---- custom element wrapper ----

defineWashApp('wash-app-fm', (props) => <App {...props} />, {
  style: `display:grid;grid-template-rows:36px 1fr 22px;width:100%;height:100%;background:${tokens.bgMenu};color:${tokens.fg};font:13px ${tokens.fontSans};box-sizing:border-box;position:relative`,
});
