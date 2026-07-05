// FilePicker — open/save modal that any wash app can drop in.
//
// Architecture
//
// The picker sends fs.list / fs.stat / fs.complete / fs.root via
// window.wash.sendAppMsg(hostInstanceID, …) — i.e., to the host's
// OWN BE. The host opts in by calling `sdk.EnableFilePicker(c)` in
// its OnReady; that helper dispatches fs.* messages through
// internal/fs (sandboxed by Session.Root) and replies via
// c.SendAppMsg. No cross-app routing, no separate service. The
// host BE keeps using internal/fs directly for its other work; the
// picker just shares the same in-process accessor through a tiny
// dispatch helper.
//
// Replies arrive on the host element's wash:msg event (same path
// every BE→FE message takes); the picker correlates by `id`.
//
// Layout (flat / Finder style):
//
//   ┌──── Open File ─────────────────────────────────┐
//   │ [breadcrumb / path input]              [Up ↑]  │
//   │ ┌─ Name ─────────────── Size ── Modified ──┐  │
//   │ │ 📁 docs                       —  Apr 14   │  │
//   │ │ 📄 hello.txt                12 B  Apr 14   │  │
//   │ │ 📄 plan.md                 1 KB  Apr 12   │  │
//   │ └────────────────────────────────────────────┘  │
//   │ [filter ▾]              [Cancel]  [Open]       │
//   └─────────────────────────────────────────────────┘
//
// (Save mode adds a name input above the action row.)

import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { File as FileIcon, Folder as FolderIcon, Link2 } from 'lucide-solid';
import { tokens } from './tokens';
import { Overlay, ConfirmDialog } from './overlay';

// FilterSpec is the user-facing filter shape. `re` is a JavaScript
// regex source string (no slashes); the picker compiles it once per
// activation and tests entry.name against it. Folders are always
// shown regardless of filter — navigation can't be blocked.
export interface FilterSpec {
  label: string;
  re: string;
}

export interface FilePickerProps {
  open: boolean;
  mode: 'open' | 'save' | 'directory';
  // Host element for delivering BE replies. The picker installs a
  // `wash:msg` listener here and correlates replies by id.
  host: HTMLElement;
  // The host's instance id — the picker sends requests to its own
  // BE via window.wash.sendAppMsg(hostInstanceID, …). The host BE
  // dispatches them through sdk.EnableFilePicker, which calls
  // internal/fs and replies via c.SendAppMsg back to this element.
  // No cross-app routing, no separate service.
  hostInstanceID: string;
  // Initial directory. Empty / undefined sends the empty path,
  // which the host BE resolves to its default start: the sandbox
  // root when confined, else the user's home directory.
  start?: string;
  // Save-mode default filename, prefilled into the name input.
  defaultName?: string;
  filters?: FilterSpec[];
  defaultFilter?: number;
  onConfirm: (path: string) => void;
  onCancel: () => void;
  // Testids — exposed so e2e tests can address the picker without
  // colliding with the host app's own elements.
  'data-testid'?: string;
}

// Entry mirrors the shape wash-fs returns in `list_ok.entries`. We
// only declare what the picker reads — adding fields is forward-
// compatible because we never destructure exhaustively.
interface Entry {
  name: string;
  type: 'dir' | 'file' | 'symlink' | 'other';
  size: number;
  mod_unix: number;
}

type SortKey = 'name' | 'size' | 'mtime';

interface ListOK {
  kind: 'fs.list_ok';
  id: string;
  path: string;
  entries: Entry[];
  truncated: boolean;
}

interface ListErr {
  kind: 'fs.list_err';
  id: string;
  code: string;
  msg: string;
}

interface StatOK {
  kind: 'fs.stat_ok';
  id: string;
  path: string;
  entry: Entry;
}

interface StatErr {
  kind: 'fs.stat_err';
  id: string;
  code: string;
  msg: string;
}

interface RootOK {
  kind: 'fs.root_ok';
  id: string;
  root: string;
}

type AnyReply = ListOK | ListErr | StatOK | StatErr | RootOK;

// We don't redeclare the global Window.wash here — every consuming
// app already does that and TypeScript merges declarations, so a
// second declaration with a narrower shape would conflict. Instead
// the lib calls into window.wash via a single typed helper that
// asserts the minimum shape it needs.
type WashGlobal = {
  sendAppMsg(instanceID: string, data: unknown): void;
};
const washAPI = (): WashGlobal => (window as unknown as { wash: WashGlobal }).wash;

// Debounce window for fs.watch_event refreshes. fsnotify can fire a
// handful of events for one logical save (write + chmod); the
// picker doesn't need to re-list each time. 100ms collapses bursts
// without making the UI feel stale.
const REFRESH_DEBOUNCE_MS = 100;

export const FilePicker: Component<FilePickerProps> = (props) => {
  // ---- reactive state ----
  // Empty cwd is the "default start" sentinel: the BE resolves it
  // to the sandbox root (confined) or the user's home (unconfined).
  // The first fs.list_ok replaces it with the resolved absolute path.
  const [cwd, setCwd] = createSignal(props.start || '');
  const [pathInput, setPathInput] = createSignal(props.start || '');
  const [entries, setEntries] = createSignal<Entry[]>([]);
  const [loading, setLoading] = createSignal(true);
  const [errorMsg, setErrorMsg] = createSignal<string>('');
  const [selectedName, setSelectedName] = createSignal<string>('');
  const [saveName, setSaveName] = createSignal(props.defaultName ?? '');
  const [sortKey, setSortKey] = createSignal<SortKey>('name');
  const [sortDesc, setSortDesc] = createSignal(false);
  const [filterIdx, setFilterIdx] = createSignal(
    typeof props.defaultFilter === 'number' ? props.defaultFilter : 0,
  );
  const [replacePrompt, setReplacePrompt] = createSignal<string>('');

  // ---- BE I/O ----

  let nextReqID = 0;
  const pending = new Map<string, (m: AnyReply) => void>();

  // send addresses the host's own BE. The host opts into the
  // picker by calling sdk.EnableFilePicker(c) in its OnReady,
  // which registers fs.* dispatch on the conn and replies via
  // c.SendAppMsg — same path every other host→FE message takes.
  // No cross-app routing.
  const send = (req: Record<string, unknown>): Promise<AnyReply> => {
    nextReqID += 1;
    const id = `fs-${nextReqID}`;
    return new Promise((resolve) => {
      const timer = window.setTimeout(() => {
        if (pending.delete(id)) {
          resolve({ kind: 'fs.list_err', id, code: 'timeout', msg: 'no reply from host BE — is sdk.EnableFilePicker called?' });
        }
      }, 5000);
      pending.set(id, (m) => {
        window.clearTimeout(timer);
        resolve(m);
      });
      washAPI().sendAppMsg(props.hostInstanceID, { ...req, id });
    });
  };

  // Listen for fs.* replies + fs.watch_event pushes from the host
  // BE. Replies (with an id) resolve a pending promise; watch
  // events are unsolicited and trigger a debounced re-list when
  // they fire under the picker's cwd.
  const onWashMsg = (ev: Event) => {
    const detail = (ev as CustomEvent).detail as
      | (AnyReply & { id?: string })
      | { kind: 'fs.watch_event'; op: string; path: string }
      | undefined;
    if (!detail || typeof detail.kind !== 'string' || !detail.kind.startsWith('fs.')) {
      return;
    }
    if (detail.kind === 'fs.watch_event') {
      // The BE only sends events for paths we're subscribed to,
      // so any event under our cwd warrants a refresh. We don't
      // try to be smart about which sub-path changed — re-listing
      // the dir is cheap.
      scheduleRefresh();
      return;
    }
    const id = (detail as { id?: string }).id;
    if (!id) return;
    const resolver = pending.get(id);
    if (!resolver) return;
    pending.delete(id);
    resolver(detail as AnyReply);
  };

  // ---- fs.watch wiring ----
  //
  // The picker subscribes to fs.watch on exactly one path: the
  // currently-displayed cwd, AND only while the picker is open.
  // Three leak surfaces and how we handle each:
  //
  //   1. cwd navigation: a createEffect transitions the
  //      subscription — releases the old path before subscribing
  //      to the new one. One watch per picker, always.
  //   2. picker close (props.open → false): the same effect sees
  //      open=false and unsubscribes.
  //   3. component teardown: onCleanup unsubscribes whatever's
  //      still active. The host is responsible for tearing the
  //      picker down (the component dies with its parent element).
  //
  // The BE's fs.unwatch is idempotent — duplicate unwatch is fine.

  let watchedPath = '';
  let refreshTimer: number | null = null;

  const sendUnwatch = (p: string) => {
    if (!p) return;
    washAPI().sendAppMsg(props.hostInstanceID, { kind: 'fs.unwatch', path: p });
  };
  const sendWatch = (p: string) => {
    if (!p) return;
    washAPI().sendAppMsg(props.hostInstanceID, { kind: 'fs.watch', path: p });
  };

  const scheduleRefresh = () => {
    if (refreshTimer != null) window.clearTimeout(refreshTimer);
    refreshTimer = window.setTimeout(() => {
      refreshTimer = null;
      const p = cwd();
      if (p && props.open) void loadDir(p);
    }, REFRESH_DEBOUNCE_MS);
  };

  // Subscription effect: whenever (props.open, cwd) changes, swap
  // the active watch. The diff vs. watchedPath ensures we never
  // hold more than one subscription and never strand one open.
  //
  // Also: on each false → true open transition, force-reload the
  // current cwd. While closed we hold no watch (we unsubscribe to
  // free the BE), so any disk changes that happened in the gap
  // are invisible to the watcher. A re-list closes that gap so
  // the user never sees a stale picker.
  let prevOpen = false;
  createEffect(() => {
    const open = props.open;
    const c = cwd();
    const want = open ? c : '';
    if (want !== watchedPath) {
      if (watchedPath) sendUnwatch(watchedPath);
      watchedPath = want;
      if (want) sendWatch(want);
    }
    if (open && !prevOpen) {
      void loadDir(c);
    }
    prevOpen = open;
  });

  onMount(() => {
    props.host.addEventListener('wash:msg', onWashMsg);
    void loadDir(cwd());
  });
  onCleanup(() => {
    props.host.removeEventListener('wash:msg', onWashMsg);
    if (watchedPath) {
      sendUnwatch(watchedPath);
      watchedPath = '';
    }
    if (refreshTimer != null) {
      window.clearTimeout(refreshTimer);
      refreshTimer = null;
    }
  });

  // Monotonic navigation generation. loadDir is async and SEVERAL
  // calls can be in flight at once: onMount's initial load, the
  // open-transition effect, the outside_root recovery bounce, a
  // watch-event refresh, and the user typing a path + Enter. Without
  // a guard the LAST reply to ARRIVE wins — so a slow initial load of
  // "/" (which bounces "/" → outside_root → fs.root → sandbox root,
  // two extra round-trips) could resolve AFTER the user navigated
  // into a subdir and clobber cwd/pathInput/entries back to the root.
  // That was a real e2e flake (imageview Open Image landing on the
  // sandbox root instead of the typed gallery dir). Each loadDir
  // captures the generation it began in and discards its reply if a
  // newer navigation has since superseded it.
  let loadGen = 0;

  // pathDirty tracks whether the user has typed into the path bar
  // since the last committed navigation. A load the user did NOT
  // initiate (the initial open load, the outside_root recovery, a
  // watch-event refresh) must not stomp text the user is mid-typing
  // — otherwise a fast tester (or user) who types a subdir right as
  // the picker opens has it erased by the still-in-flight initial
  // list, and their Enter then navigates to the wrong (reset) path.
  // Set on input, cleared whenever the user explicitly commits a
  // navigation (Enter / Up / double-click into a folder).
  let pathDirty = false;

  // history is the Back button's trail of previously-displayed
  // directories, most recent last. Only user-committed navigations
  // (Up / Root / Home / Enter / double-click) record — the initial
  // load, watch refreshes, and outside_root recovery all pass
  // record=false so they can't pollute the trail, and Back itself
  // doesn't record or Back/Back would ping-pong between two dirs.
  // canGoBack mirrors history.length into the reactive graph so the
  // button's disabled state tracks it.
  const history: string[] = [];
  const [canGoBack, setCanGoBack] = createSignal(false);
  const HISTORY_MAX = 100;

  // loadDir asks wash-fs to list `p`. On success, updates the
  // picker's view to that directory; on outside_root, asks wash-fs
  // for the configured root and retries there so the user lands
  // somewhere usable instead of staring at an error (this is the
  // common case when the picker starts at "/" but wash-fs has a
  // sandbox). Any other error surfaces beneath the path bar and
  // keeps the previous listing on screen.
  const loadDir = async (p: string, opts: { recovering?: boolean; record?: boolean } = {}) => {
    const myGen = ++loadGen;
    setLoading(true);
    setErrorMsg('');
    const reply = await send({ kind: 'fs.list', path: p });
    // Superseded by a newer navigation while we awaited — drop this
    // reply so it can't overwrite the fresher one (and don't kick off
    // the outside_root recovery for a directory nobody's viewing).
    if (myGen !== loadGen) return;
    if (reply.kind === 'fs.list_ok') {
      setLoading(false);
      if (opts.record) {
        const prev = cwd();
        if (prev && prev !== reply.path) {
          history.push(prev);
          if (history.length > HISTORY_MAX) history.shift();
          setCanGoBack(true);
        }
      }
      setCwd(reply.path);
      // Don't overwrite path-bar text the user is actively editing.
      // Once they commit (Enter/Up/dbl-click) pathDirty is cleared,
      // so the canonicalized path still lands in the bar then.
      if (!pathDirty) setPathInput(reply.path);
      setEntries(reply.entries ?? []);
      setSelectedName('');
      return;
    }
    if (reply.kind === 'fs.list_err' && reply.code === 'outside_root' && !opts.recovering) {
      // Ask wash-fs where the sandbox actually is, then re-list
      // there. `recovering` guards against an infinite bounce if
      // the root call itself goes wrong. The recovery load keeps
      // the caller's record flag: a user-committed jump that gets
      // downshifted to the sandbox root is still a navigation the
      // Back button should be able to undo.
      const rootReply = await send({ kind: 'fs.root' });
      if (myGen !== loadGen) return; // superseded while resolving root
      if (rootReply.kind === 'fs.root_ok' && rootReply.root) {
        void loadDir(rootReply.root, { recovering: true, record: opts.record });
        return;
      }
    }
    setLoading(false);
    if (reply.kind === 'fs.list_err') {
      setErrorMsg(`${reply.code}: ${reply.msg}`);
    }
  };

  // ---- derived: sort + filter ----

  const filterRE = createMemo<RegExp | null>(() => {
    const f = props.filters?.[filterIdx()];
    if (!f) return null;
    try {
      return new RegExp(f.re);
    } catch {
      return null;
    }
  });

  const visibleEntries = createMemo<Entry[]>(() => {
    const re = filterRE();
    let list = entries();
    // Directory mode is a folder chooser — only dirs are relevant.
    if (props.mode === 'directory') list = list.filter((e) => e.type === 'dir');
    if (re) list = list.filter((e) => e.type === 'dir' || re.test(e.name));
    const out = list.slice();
    const desc = sortDesc();
    const key = sortKey();
    out.sort((a, b) => {
      // Folders first regardless of column, then within-type sort.
      if (a.type === 'dir' && b.type !== 'dir') return -1;
      if (a.type !== 'dir' && b.type === 'dir') return 1;
      let cmp = 0;
      switch (key) {
        case 'name':
          cmp = a.name.toLowerCase().localeCompare(b.name.toLowerCase());
          break;
        case 'size':
          cmp = a.size - b.size;
          break;
        case 'mtime':
          cmp = a.mod_unix - b.mod_unix;
          break;
      }
      return desc ? -cmp : cmp;
    });
    return out;
  });

  // ---- actions ----

  // Back pops the history trail — the inverse of whatever committed
  // navigation the user last made. No record: see history's comment.
  const goBack = () => {
    const prev = history.pop();
    setCanGoBack(history.length > 0);
    if (!prev) return;
    pathDirty = false;
    void loadDir(prev);
  };

  // Up goes strictly one level up the directory tree (contrast with
  // Back, which retraces the user's own steps).
  const goUp = () => {
    const p = cwd();
    if (!p || p === '/') return;
    pathDirty = false;
    const i = p.lastIndexOf('/');
    void loadDir(i <= 0 ? '/' : p.slice(0, i), { record: true });
  };

  const goRoot = () => {
    if (cwd() === '/') return;
    pathDirty = false;
    void loadDir('/', { record: true });
  };

  // Home requests the empty path — the BE's "default start" — so the
  // FE never needs to learn where home actually is. Confined hosts
  // resolve it to the sandbox root, unconfined ones to $HOME.
  const goHome = () => {
    pathDirty = false;
    void loadDir('', { record: true });
  };

  const navigateToInput = () => {
    pathDirty = false;
    void loadDir(pathInput(), { record: true });
  };

  const onRowClick = (e: Entry) => {
    setSelectedName(e.name);
    if (props.mode === 'save' && e.type === 'file') {
      setSaveName(e.name);
    }
  };

  const onRowDblClick = (e: Entry) => {
    const target = joinPath(cwd(), e.name);
    if (e.type === 'dir') {
      pathDirty = false;
      void loadDir(target, { record: true });
      return;
    }
    if (e.type === 'file' && props.mode === 'open') {
      props.onConfirm(target);
    }
  };

  const onConfirmClick = async () => {
    if (props.mode === 'directory') {
      // Confirm the highlighted subfolder if one is selected, else
      // the current directory ("use this folder").
      const sel = selectedName();
      const e = sel ? entries().find((x) => x.name === sel) : null;
      props.onConfirm(e && e.type === 'dir' ? joinPath(cwd(), sel) : cwd());
      return;
    }
    if (props.mode === 'open') {
      const sel = selectedName();
      if (!sel) return;
      const e = entries().find((x) => x.name === sel);
      if (!e) return;
      const path = joinPath(cwd(), sel);
      if (e.type === 'dir') {
        pathDirty = false;
        void loadDir(path, { record: true });
        return;
      }
      props.onConfirm(path);
      return;
    }
    // save mode
    const name = saveName().trim();
    if (!name) return;
    if (name.includes('/')) {
      setErrorMsg('save: name cannot contain /');
      return;
    }
    const target = joinPath(cwd(), name);
    const reply = await send({ kind: 'fs.stat', path: target });
    if (reply.kind === 'fs.stat_ok') {
      // Exists — prompt to replace.
      setReplacePrompt(target);
      return;
    }
    // stat_err with code=not_found is the happy path: filename is free.
    props.onConfirm(target);
  };

  const onReplaceConfirm = () => {
    const p = replacePrompt();
    setReplacePrompt('');
    if (p) props.onConfirm(p);
  };
  const onReplaceCancel = () => setReplacePrompt('');

  // ---- keyboard ----

  const onPickerKey = (ev: KeyboardEvent) => {
    if (ev.key === 'Escape') {
      ev.preventDefault();
      props.onCancel();
    }
  };

  // ---- view ----
  //
  // We wrap the picker body in <Show when={props.open}> rather
  // than `if (!props.open) return null;` at the top. Solid
  // component bodies run once — early-return means the reactive
  // tree never mounts, and toggling `open` from outside can't
  // bring it back. <Show> is the reactive equivalent.

  return (
    <>
    <Show when={props.open}>
      <Overlay
        onDismiss={props.onCancel}
        data-testid={props['data-testid']}
        innerStyle={{
          // 16px breathing room on each side and top/bottom of the
          // host window — matches Overlay defaults but stated here
          // explicitly so the picker reads self-contained.
          //
          // No explicit `height`: that pinned the modal to viewport
          // minus 32px on every render, which in narrow / short
          // windows fought the inner list's natural size and made
          // the box flicker between layout passes (content
          // overflow → scrollbar → width recompute → re-layout).
          // max-height + content-driven height settles in one pass.
          width: 'calc(100% - 32px)',
          height: 'auto',
          'min-width': '320px',
          'min-height': '320px',
          'max-width': '760px',
          'max-height': 'calc(100% - 32px)',
          padding: '0',
          // Inner panel handles its own padding so the column rows
          // can extend edge-to-edge inside the box.
        }}
      >
        <div
          onKeyDown={onPickerKey}
          style={{
            display: 'flex',
            'flex-direction': 'column',
            flex: 1,
            'min-height': 0,
          }}
        >
          {/* title */}
          <div style={titleStyle}>
            {props.mode === 'open' ? 'Open File' : props.mode === 'directory' ? 'Open Folder' : 'Save As'}
          </div>

          {/* path bar */}
          <div style={pathBarStyle}>
            <button
              type="button"
              data-testid="fp-back"
              onClick={goBack}
              disabled={!canGoBack()}
              style={{ ...iconBtnStyle, opacity: canGoBack() ? 1 : 0.35 }}
              title="Back to previous directory"
            >
              ←
            </button>
            <button
              type="button"
              data-testid="fp-up"
              onClick={goUp}
              style={iconBtnStyle}
              title="Up one directory"
            >
              ↑
            </button>
            <button
              type="button"
              data-testid="fp-root"
              onClick={goRoot}
              style={iconBtnStyle}
              title="Go to filesystem root"
            >
              /
            </button>
            <button
              type="button"
              data-testid="fp-home"
              onClick={goHome}
              style={iconBtnStyle}
              title="Go to home directory"
            >
              ~
            </button>
            <input
              type="text"
              data-testid="fp-path"
              spellcheck={false}
              value={pathInput()}
              onInput={(e) => {
                pathDirty = true;
                setPathInput(e.currentTarget.value);
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  navigateToInput();
                }
              }}
              style={pathInputStyle}
            />
          </div>

          {/* error line — only when something went wrong */}
          <Show when={errorMsg()}>
            <div data-testid="fp-error" style={errorStyle}>
              {errorMsg()}
            </div>
          </Show>

          {/* column header */}
          <div style={headerRowStyle}>
            {(['name', 'size', 'mtime'] as SortKey[]).map((k) => (
              <button
                type="button"
                data-testid={`fp-col-${k}`}
                onClick={() => {
                  if (sortKey() === k) setSortDesc(!sortDesc());
                  else { setSortKey(k); setSortDesc(false); }
                }}
                style={headerCellStyle(k)}
              >
                {k === 'name' ? 'Name' : k === 'size' ? 'Size' : 'Modified'}
                <Show when={sortKey() === k}>
                  <span style={{ 'margin-left': '4px', opacity: 0.6 }}>
                    {sortDesc() ? '▼' : '▲'}
                  </span>
                </Show>
              </button>
            ))}
          </div>

          {/* entry list */}
          <div data-testid="fp-list" style={listStyle}>
            <Show when={loading()}>
              <div style={mutedRowStyle}>loading…</div>
            </Show>
            <Show when={!loading() && visibleEntries().length === 0}>
              <div style={mutedRowStyle}>(empty)</div>
            </Show>
            <For each={visibleEntries()}>
              {(e) => {
                const sel = () => selectedName() === e.name;
                return (
                  <div
                    data-testid={`fp-entry-${e.name}`}
                    data-type={e.type}
                    data-selected={sel() ? 'true' : undefined}
                    onClick={() => onRowClick(e)}
                    onDblClick={() => onRowDblClick(e)}
                    style={rowStyle(sel())}
                  >
                    <span style={rowNameCellStyle}>
                      <span style={{ 'margin-right': '6px', opacity: 0.8, display: 'inline-flex', 'align-items': 'center', 'flex-shrink': 0 }}>
                        <EntryIcon type={e.type} />
                      </span>
                      {e.name}
                    </span>
                    <span style={rowNumCellStyle}>
                      {e.type === 'file' ? humanSize(e.size) : ''}
                    </span>
                    <span style={rowNumCellStyle}>{formatDate(e.mod_unix)}</span>
                  </div>
                );
              }}
            </For>
          </div>

          {/* save-mode name input */}
          <Show when={props.mode === 'save'}>
            <div style={nameInputRowStyle}>
              <label style={{ color: tokens.fgMuted }}>Save as:</label>
              <input
                type="text"
                data-testid="fp-save-name"
                spellcheck={false}
                value={saveName()}
                onInput={(e) => setSaveName(e.currentTarget.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    void onConfirmClick();
                  }
                }}
                style={{ ...pathInputStyle, flex: 1 }}
              />
            </div>
          </Show>

          {/* footer: filter + action buttons */}
          <div style={footerStyle}>
            <Show when={props.filters && props.filters.length > 0}>
              <select
                data-testid="fp-filter"
                value={filterIdx()}
                onChange={(e) => setFilterIdx(parseInt(e.currentTarget.value, 10))}
                style={selectStyle}
              >
                <For each={props.filters!}>
                  {(f, i) => <option value={i()}>{f.label}</option>}
                </For>
              </select>
            </Show>
            <div style={{ flex: 1 }} />
            <button
              type="button"
              data-testid="fp-cancel"
              onClick={props.onCancel}
              style={actionBtnStyle(false)}
            >
              Cancel
            </button>
            <button
              type="button"
              data-testid="fp-confirm"
              onClick={() => void onConfirmClick()}
              disabled={
                (props.mode === 'open' && !selectedName()) ||
                (props.mode === 'save' && !saveName().trim())
                // directory mode is always actionable (falls back to cwd)
              }
              style={actionBtnStyle(true)}
            >
              {props.mode === 'open' ? 'Open' : props.mode === 'directory' ? 'Open Folder' : 'Save'}
            </button>
          </div>
        </div>
      </Overlay>
    </Show>

      {/* overwrite prompt — only fires in save mode */}
      <Show when={replacePrompt()}>
        <ConfirmDialog
          title="Replace existing file?"
          confirmLabel="Replace"
          danger
          onCancel={onReplaceCancel}
          onConfirm={onReplaceConfirm}
          data-testid="fp-replace-dialog"
          confirmTestid="fp-replace-confirm"
          cancelTestid="fp-replace-cancel"
        >
          <div style={{ color: tokens.fgMuted, 'font-size': tokens.fontSizeMd }}>
            {replacePrompt()}
          </div>
        </ConfirmDialog>
      </Show>
    </>
  );
};

// ---- styles ----

const titleStyle: JSX.CSSProperties = {
  padding: '12px 16px 10px',
  font: tokens.type.titleSm,
  'border-bottom': `1px solid ${tokens.borderWindow}`,
};

const pathBarStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '6px',
  padding: '8px 12px',
};

const iconBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  width: '26px',
  height: '26px',
  cursor: 'pointer',
  font: tokens.type.textMd,
};

const pathInputStyle: JSX.CSSProperties = {
  flex: 1,
  background: tokens.bgInset,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '4px 8px',
  font: tokens.type.monoMd,
  outline: 'none',
};

const errorStyle: JSX.CSSProperties = {
  padding: '4px 12px',
  color: tokens.fgDanger,
  font: tokens.type.textSm,
};

const HEADER_ROW_H = 24;

const headerRowStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': '1fr 90px 110px',
  'background': tokens.bgMenu,
  'border-top': `1px solid ${tokens.borderMenu}`,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  'user-select': 'none',
};

function headerCellStyle(_k: SortKey): JSX.CSSProperties {
  return {
    background: 'transparent',
    border: 'none',
    color: tokens.fgMuted,
    font: tokens.type.textSm,
    cursor: 'pointer',
    padding: '0 8px',
    height: `${HEADER_ROW_H}px`,
    'box-sizing': 'border-box',
    display: 'flex',
    'align-items': 'center',
    'justify-content': _k === 'name' ? 'flex-start' : 'flex-end',
  };
}

const listStyle: JSX.CSSProperties = {
  flex: 1,
  overflow: 'auto',
  background: tokens.bgWindow,
};

const mutedRowStyle: JSX.CSSProperties = {
  padding: '20px 16px',
  color: tokens.fgDim,
  'font-style': 'italic',
  font: tokens.type.textMd,
};

function rowStyle(selected: boolean): JSX.CSSProperties {
  return {
    display: 'grid',
    'grid-template-columns': '1fr 90px 110px',
    'align-items': 'center',
    padding: '4px 8px',
    background: selected ? tokens.bgRowSelected : 'transparent',
    color: tokens.fg,
    cursor: 'pointer',
    'user-select': 'none',
    font: tokens.type.textMd,
  };
}

const rowNameCellStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

const rowNumCellStyle: JSX.CSSProperties = {
  font: tokens.type.monoSm,
  color: tokens.fgMuted,
  'text-align': 'right',
  'white-space': 'nowrap',
};

const nameInputRowStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '8px',
  padding: '8px 12px',
  'border-top': `1px solid ${tokens.borderMenu}`,
  font: tokens.type.textMd,
};

const footerStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '8px',
  padding: '10px 12px',
  'border-top': `1px solid ${tokens.borderMenu}`,
  background: tokens.bgMenu,
};

const selectStyle: JSX.CSSProperties = {
  background: tokens.bgInset,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '3px 8px',
  font: tokens.type.textMd,
};

function actionBtnStyle(primary: boolean): JSX.CSSProperties {
  return {
    background: primary ? tokens.bgRowSelected : 'transparent',
    color: tokens.fg,
    border: `1px solid ${primary ? tokens.borderFocus : tokens.borderMenu}`,
    'border-radius': `${tokens.radiusSm}`,
    padding: '5px 14px',
    cursor: 'pointer',
    font: tokens.type.textMd,
  };
}

// EntryIcon mirrors wash-fm's tree-row icon contract: 12px lucide
// glyph for dir / file / symlink. Keeping the same sizes + same
// icon set so the picker reads as part of the same UI surface.
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

// ---- helpers ----

function joinPath(parent: string, name: string): string {
  if (parent.endsWith('/')) return parent + name;
  return parent + '/' + name;
}

function humanSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

function formatDate(unix: number): string {
  if (!unix) return '';
  const d = new Date(unix * 1000);
  const now = new Date();
  const day = String(d.getDate()).padStart(2, ' ');
  const month = MONTHS[d.getMonth()];
  if (d.getFullYear() === now.getFullYear()) {
    const hh = String(d.getHours()).padStart(2, '0');
    const mm = String(d.getMinutes()).padStart(2, '0');
    return `${month} ${day} ${hh}:${mm}`;
  }
  return `${month} ${day}  ${d.getFullYear()}`;
}

