// wash-app-fm: single-tree + preview file manager.
// - Left pane: hierarchical tree, rooted at /, auto-expanded to $HOME
//   on first list_ok. Click triangle to toggle; click row name to
//   select. Selecting a folder also auto-expands it.
// - Right pane: file preview (text or "binary").
// - Below preview: collapsible info section (perms, size, mtime,
//   symlink target if relevant).
// - Toolbar: Home, Back, Up, editable path (Enter to navigate),
//   Reload, Sort dropdown (sort key + show-hidden toggle).
// - Right-click on a row: Open · Copy path · Show info.
// v1 is read-only. Future: rename/delete/symlink (v2),
// move/copy (v3).
//
// Solid drives the rendering — state mutations automatically re-run
// just the views that read them. No more "I changed a field but
// forgot to re-render" bugs.

import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import { createStore, produce } from 'solid-js/store';
import type { Component, JSX } from 'solid-js';
import { ConfirmDialog, Menu, MenuItem, MenuSeparator, Splitter, StatusBar, defineWashApp, tokens } from '@wash/ui';
import {
  baseName, formatDate, humanSize, joinPath, octalPerm, parentPath, ancestorChain,
  createBus,
  createWatch,
  DRAG_MIME, dragPayload, dropEffectFor, hasWashDrag, readDragPaths,
  sortedFiltered as sortListing,
  withReplacePrompt as runReplaceFlow,
} from '@wash/fs-client';
import {
  type NavHistory, emptyHistory, initAt, pushPath, back, forward, at,
} from './nav-history.ts';
import {
  type ClipboardState, parseClipboardState, planPaste,
} from './clipboard.ts';
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
  Pencil,
  RotateCw,
  Square,
  Trash2,
} from 'lucide-solid';

interface PersistedState {
  path?: string;
  expanded?: string[];
  sort_key?: SortKey;
  sort_desc?: boolean;
  show_hidden?: boolean;
  info_open?: boolean;
  split_pct?: number;
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

type SortKey = 'name' | 'mtime' | 'ctime' | 'size' | 'type';

type MenuState =
  | { kind: 'sort'; left: number; top: number }
  | { kind: 'context'; left: number; top: number; entry: Entry; path: string }
  | null;

const HOME_FALLBACK = '/';

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
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
  // splitPct is the percentage width of the tree pane in the body
  // grid; the rest (minus the 4px splitter) goes to the preview/info
  // pane. Adjustable via the draggable splitter; persisted.
  const [splitPct, setSplitPct] = createSignal(50);
  let bodyEl!: HTMLDivElement;
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
    setSelection(new Set());
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
    if (ev.shiftKey && selectionAnchor) {
      const rows = visibleRows().map((r) => r.path);
      const a = rows.indexOf(selectionAnchor);
      const b = rows.indexOf(rowPath);
      if (a >= 0 && b >= 0) {
        const [lo, hi] = a < b ? [a, b] : [b, a];
        setSelection(new Set(rows.slice(lo, hi + 1)));
      } else {
        setSelection(new Set([rowPath]));
      }
      setSelectedEntry(entry);
      if (entry.type === 'file') focusForFile(rowPath);
      return;
    }
    if (ev.ctrlKey || ev.metaKey) {
      const next = new Set(selection());
      if (next.has(rowPath)) next.delete(rowPath);
      else next.add(rowPath);
      setSelection(next);
      selectionAnchor = rowPath;
      setSelectedEntry(entry);
      if (entry.type === 'file') focusForFile(rowPath);
      return;
    }
    // Plain click: select + (for files) focus + preview.
    setSelection(new Set([rowPath]));
    selectionAnchor = rowPath;
    setSelectedEntry(entry);
    setStatusOverride(null);
    if (entry.type === 'file') {
      focusForFile(rowPath);
      sendRead(rowPath);
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
    setSelection(new Set());
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
    if (!hasWashDrag(ev.dataTransfer)) return;
    ev.preventDefault();
    ev.stopPropagation();
    ev.dataTransfer!.dropEffect = dropEffectFor(ev.altKey);
    if (dropTargetPath() !== rowPath) setDropTargetPath(rowPath);
  };

  const onRowDrop = (ev: DragEvent, rowPath: string) => {
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
    window.wash.sendAppMsgTo(
      { app_id: 'com.wash.bulk' },
      { kind: 'enqueue', op: 'move', paths, dest: targetDir },
    );
    // Source paths are about to vanish — drop them from the
    // selection so the status bar doesn't keep claiming "N
    // selected" for paths that no longer exist.
    setSelection(new Set());
    selectionAnchor = null;
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

  // Body splitter: drag-to-resize between tree and preview/info
  // panes. The Splitter primitive (@wash/ui) owns the gesture; we
  // own splitPct, the body grid template, and persistence.

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
      split_pct: splitPct(),
    };
    send({ kind: 'save_state', state: s });
  };

  const restoreFrom = (s: PersistedState) => {
    if (s.sort_key) setSortKey(s.sort_key);
    if (typeof s.sort_desc === 'boolean') setSortDesc(s.sort_desc);
    if (typeof s.show_hidden === 'boolean') setShowHidden(s.show_hidden);
    if (typeof s.info_open === 'boolean') setInfoOpen(s.info_open);
    if (typeof s.split_pct === 'number') setSplitPct(Math.max(15, Math.min(85, s.split_pct)));
    if (s.expanded) {
      for (const p of s.expanded) expandDir(p);
    }
    if (s.path) selectPath(s.path, false);
    else send({ kind: 'request_initial' });
  };

  // ---- listings sort/filter ----

  // The comparator + hidden-file filter live in @wash/fs-client's
  // sort.ts (unit-tested). This adapter reads the reactive sort signals
  // so callers (the visibleRows memo) still track them, then delegates.
  const sortedFiltered = (entries: Entry[]): Entry[] =>
    sortListing(entries, { key: sortKey(), desc: sortDesc(), showHidden: showHidden() });

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

  // Flatten the visible tree into a list of {entry, path, depth} rows
  // — Solid's <For> renders the list; toggling expand triggers a fresh
  // computation here automatically.
  const visibleRows = createMemo<Array<{ entry: Entry; path: string; depth: number; childCount?: number }>>(() => {
    const rows: Array<{ entry: Entry; path: string; depth: number; childCount?: number }> = [];
    // `walked` tracks directories the walk has descended into.
    // Used by the fallback below to find ancestors of path() that
    // the main walk couldn't reach (intermediate listing in flight)
    // and avoid double-rendering when the fallback's start equals
    // the main walk's start.
    const walked = new Set<string>();
    const walk = (p: string, depth: number) => {
      const entries = listings[p];
      if (!entries) return;
      walked.add(p);
      for (const e of sortedFiltered(entries)) {
        const childPath = joinPath(p, e.name);
        // For listed dirs, expose the entry count so the row's
        // size column can render "12 items" instead of blank. We
        // count ALL entries (including hidden) — Windows-explorer-
        // style. "show hidden" only affects what's rendered, not
        // the total.
        let childCount: number | undefined;
        if (e.type === 'dir' && listings[childPath]) {
          childCount = listings[childPath].length;
        }
        rows.push({ entry: e, path: childPath, depth, childCount });
        if (e.type === 'dir' && expanded[childPath] && listings[childPath]) {
          walk(childPath, depth + 1);
        }
      }
    };
    const start = treeRoot();
    if (listings[start]) walk(start, 0);

    // Fallback render: if the walk from treeRoot didn't reach
    // path()'s neighbourhood (some intermediate ancestor's
    // list_ok hasn't landed yet), find the highest listed
    // ancestor whose own contents the main walk didn't include
    // and walk from there. This bridges the gap so the user sees
    // the dir they navigated to even while ancestor listings are
    // still in flight. Disappears as soon as the intermediates
    // land and the normal walk reaches the chain.
    //
    // Walk up from path() through ancestors that are listed but
    // the main walk didn't descend into. Stop at the first one
    // that's missing a listing or that the main walk did walk —
    // `fallbackRoot` is the *highest* still-unwalked listed
    // ancestor, which is where the bridge walk should start so
    // descendants render at their natural relative depth.
    const cur = path();
    if (cur) {
      let fallbackRoot = '';
      let p = cur;
      while (p && listings[p] && !walked.has(p)) {
        fallbackRoot = p;
        const par = parentPath(p);
        if (par === p) break;
        p = par;
      }
      if (fallbackRoot) {
        // Use the depth fallbackRoot would have under the main
        // walk — so when ancestors fill in and the main walk
        // reaches it, the rows stay at the same depth instead
        // of shifting. Without this the dom flips depth, which
        // moves the elements vertically and trips Playwright's
        // stability gate on dblclick / click under heavy load.
        const tr = start;
        const segs = (s: string) => s.split('/').filter(Boolean).length;
        const baseDepth = Math.max(0, segs(fallbackRoot) - segs(tr));
        walk(fallbackRoot, baseDepth);
      }
    }
    return rows;
  });

  // visibleCount is the total entries visible right now. Updates
  // automatically as folders expand/collapse.
  const visibleCount = createMemo(() => visibleRows().length);

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
    if (!selection().has(p)) setSelection(new Set([p]));
    if (entry.type === 'file') sendRead(p);
    setMenu({ kind: 'context', left: ev.clientX, top: ev.clientY, entry, path: p });
  };

  // ---- lifecycle: events ----

  onMount(() => {
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
          setSelection(new Set());
          return;
        }
      }

      // Ctrl/Cmd shortcuts.
      if (cmd && !ev.altKey) {
        if (ev.key === 'a' || ev.key === 'A') {
          // Select-all = every currently-visible row in the tree.
          ev.preventDefault();
          setSelection(new Set(visibleRows().map((r) => r.path)));
          return;
        }
        if ((ev.key === 'N' || ev.key === 'n') && ev.shiftKey) {
          // Ctrl+Shift+N = new folder, matching Chrome's "new
          // incognito window" muscle memory in reverse.
          ev.preventDefault();
          startNewFolder();
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
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('wash:state', onState);
      props.host.removeEventListener('keydown', onKey);
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
        <button type="button" data-testid="fm-sort" title="Sort" style={iconBtnStyle} onClick={openSortMenu}>
          <ArrowUpDown size={14} />
        </button>
      </div>

      {/* body: tree + splitter + preview/info */}
      <div
        ref={bodyEl!}
        style={{ ...bodyStyle, 'grid-template-columns': `${splitPct()}% 4px 1fr` }}
      >
        <div
          data-testid="fm-list"
          style={treeStyle}
          onDragOver={onListDragOver}
          onDrop={onListDrop}
          onClick={(ev) => {
            // Background click clears the selection — native FM
            // convention. Only fire when the click hit the list
            // container itself (not a row that bubbled up).
            if (ev.target === ev.currentTarget && !ev.shiftKey && !ev.ctrlKey && !ev.metaKey) {
              setSelection(new Set());
              selectionAnchor = null;
            }
          }}
        >
          <ColumnHeader
            sortKey={sortKey()}
            sortDesc={sortDesc()}
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
                color: '#666',
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
        <Splitter
          container={bodyEl}
          onChange={setSplitPct}
          onCommit={persist}
          data-testid="fm-splitter"
        />
        <div style={{ display: 'grid', 'grid-template-rows': 'auto 1fr', overflow: 'hidden' }}>
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

// rowGridCols defines the 4-column layout shared by ColumnHeader
// and each TreeRow. Tuned so the standard human-size (e.g.
// "999.9 KB") + date strings ("Dec 15 14:32") fit without
// truncating in a typical fm window. Column geometry stays
// fm-specific (don't extract to @wash/ui — apps with different
// shapes shouldn't share these widths).
const COL_DATE_W = 96;
const COL_SIZE_W = 76;
const rowGridCols = `1fr ${COL_SIZE_W}px ${COL_DATE_W}px ${COL_DATE_W}px`;

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
        color: '#aaa',
        font: '11px ui-sans-serif,system-ui,sans-serif',
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
        background: '#10101a',
        'border-bottom': '1px solid #2a2a3a',
        display: 'grid',
        'grid-template-columns': rowGridCols,
        'z-index': 2,
        'user-select': 'none',
      }}
    >
      {cell('Name', 'name', 'left')}
      {cell('Size', 'size', 'right')}
      {cell('Modified', 'mtime', 'right')}
      {cell('Created', 'ctime', 'right')}
    </div>
  );
};

const TreeRow: Component<{
  entry: Entry;
  path: string;
  depth: number;
  childCount?: number;
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
        'grid-template-columns': rowGridCols,
        'align-items': 'center',
        padding: '3px 8px',
        background: props.isDropTarget
          ? '#2a3a5a'
          : props.selected
          ? '#23234a'
          : hover()
          ? '#1d1d30'
          : 'transparent',
        color: '#eee',
        cursor: 'pointer',
        'user-select': 'none',
        font: '13px ui-sans-serif,system-ui,sans-serif',
        outline: props.isDropTarget ? '1px solid #4a6ab0' : 'none',
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
      <span style={cellNumStyle}>
        {props.renaming ? '' : sizeOrCount(props.entry, props.childCount)}
      </span>
      {/* Modified */}
      <span style={cellNumStyle}>
        {!props.renaming ? formatDate(props.entry.mod_unix) : ''}
      </span>
      {/* Created */}
      <span style={cellNumStyle}>
        {!props.renaming ? formatDate(props.entry.created_unix) : ''}
      </span>
    </div>
  );
};

const cellNumStyle: JSX.CSSProperties = {
  opacity: 0.6,
  font: '11px ui-monospace,Menlo,Consolas,monospace',
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
        background: '#1d1d40',
        color: '#eee',
        font: '13px ui-sans-serif,system-ui,sans-serif',
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
  background: '#10101a',
  color: '#eee',
  border: '1px solid #3a3a6a',
  'border-radius': '3px',
  padding: '2px 6px',
  font: '12px ui-monospace,Menlo,Consolas,monospace',
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
    <div style={{ 'border-bottom': '1px solid #2a2a3a', background: '#15152a' }}>
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
              'border-radius': '3px',
              'border-bottom': '1px dashed #2a2a3a',
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
  onRename: () => void;
  onDelete: () => void;
  onDismiss: () => void;
}> = (props) => {
  return (
    <Menu data-testid="fm-context-menu" x={props.left} y={props.top} onDismiss={props.onDismiss}>
      <MenuItem data-testid="fm-ctx-open" label="Open" onClick={props.onOpen} />
      <MenuItem data-testid="fm-ctx-copy" label="Copy path" onClick={props.onCopy} />
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
          font: '12px ui-monospace,Menlo,Consolas,monospace',
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
          font: '12px ui-monospace,Menlo,Consolas,monospace',
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
        background: '#15152a',
        border: '1px solid #2a2a3a',
        'border-radius': '4px',
        padding: '2px 0',
        'min-width': '240px',
        'max-height': '280px',
        'overflow-y': 'auto',
        'box-shadow': '0 8px 20px rgba(0,0,0,0.5)',
        'z-index': 1500,
        font: '12px ui-monospace,Menlo,Consolas,monospace',
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
              color: '#eee',
              background: i() === props.idx ? '#23234a' : 'transparent',
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
  background: '#181828',
};

const iconBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: '#eee',
  border: '1px solid #2a2a3a',
  'border-radius': '3px',
  padding: '4px 8px',
  cursor: 'pointer',
  font: '13px ui-sans-serif,system-ui,sans-serif',
  'min-width': '30px',
};

const pathInputStyle: JSX.CSSProperties = {
  flex: 1,
  background: '#10101a',
  color: '#eee',
  border: '1px solid #2a2a3a',
  'border-radius': '3px',
  padding: '4px 8px',
  font: '12px ui-sans-serif, system-ui, sans-serif',
  outline: 'none',
};

const bodyStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': '1fr 1fr',
  overflow: 'hidden',
  'border-top': '1px solid #2a2a3a',
  'border-bottom': '1px solid #2a2a3a',
};

const treeStyle: JSX.CSSProperties = {
  overflow: 'auto',
  background: '#181828',
  padding: '0 0 4px 0',
};

const previewStyle: JSX.CSSProperties = {
  overflow: 'auto',
  padding: '10px 12px',
  font: '12px ui-monospace,Menlo,Consolas,monospace',
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
  color: '#aaa',
  border: 'none',
  padding: '0 8px',
  cursor: 'pointer',
  font: '11px ui-sans-serif,system-ui,sans-serif',
};

const infoBodyStyle: JSX.CSSProperties = {
  padding: '8px 12px 12px',
  font: '12px ui-monospace,Menlo,Consolas,monospace',
  'border-top': '1px solid #2a2a3a',
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
  style: 'display:grid;grid-template-rows:36px 1fr 22px;width:100%;height:100%;background:#10101a;color:#eee;font:13px ui-sans-serif,system-ui,sans-serif;box-sizing:border-box;position:relative',
});
