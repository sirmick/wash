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
import { render } from 'solid-js/web';
import type { Component, JSX } from 'solid-js';
import { FilePicker, Splitter, StatusBar, tokens } from '@wash/ui';
import { EditorState, Compartment } from '@codemirror/state';
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter } from '@codemirror/view';
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { searchKeymap, search } from '@codemirror/search';
import {
  bracketMatching,
  defaultHighlightStyle,
  foldGutter,
  foldKeymap,
  indentOnInput,
  syntaxHighlighting,
} from '@codemirror/language';
import { javascript } from '@codemirror/lang-javascript';
import { json } from '@codemirror/lang-json';
import { markdown } from '@codemirror/lang-markdown';

declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
    } & Record<string, unknown>;
  }
}

interface Entry {
  name: string;
  type: 'dir' | 'file' | 'symlink' | 'other';
  size: number;
  mod_unix: number;
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
interface PersistedState {
  paths?: string[];
  active?: string;
  split_pct?: number;
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
      if (!root()) setRoot(abs);
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
      const state: PersistedState = {
        paths: tabs().filter((t) => t.path).map((t) => t.path),
        active: activeTab()?.path || undefined,
        split_pct: splitPct(),
      };
      send({ kind: 'save_state', state });
    }, 250);
  };

  const restoreFrom = async (s: PersistedState) => {
    if (typeof s.split_pct === 'number') {
      setSplitPct(Math.max(15, Math.min(85, s.split_pct)));
    }
    if (s.paths && s.paths.length > 0) {
      for (const p of s.paths) {
        await openInTab(p);
      }
      if (s.active) {
        const t = tabs().find((x) => x.path === s.active);
        if (t) setActiveID(t.id);
      }
    }
  };

  // captureActiveState snapshots the live CM state into the
  // outgoing tab right before a switch. Without this, switching
  // away from a tab loses its undo history and cursor.
  const captureActiveState = () => {
    const t = activeTab();
    if (!t || !editorView) return;
    if (t.state === editorView.state) return;
    setTabs(tabs().map((x) => x.id === t.id ? { ...x, state: editorView!.state } : x));
  };

  const handleBE = (m: BEMessage) => {
    const replyID = typeof m.id === 'string' ? m.id : undefined;
    if (replyID && pendingReplies.has(replyID)) {
      const resolver = pendingReplies.get(replyID)!;
      pendingReplies.delete(replyID);
      resolver(m);
    }
  };

  // ---- tree ops ----

  const toggleExpand = (path: string) => {
    if (expanded[path]) {
      setExpanded(produce((s) => { delete s[path]; }));
    } else {
      setExpanded(path, true);
      if (!listings[path]) void loadDir(path);
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

  // ---- row click ----

  const onRowClick = (row: { entry: Entry; path: string }) => {
    if (row.entry.type === 'dir') {
      toggleExpand(row.path);
      return;
    }
    if (row.entry.type === 'file') {
      setSelectedPath(row.path);
      void openInTab(row.path);
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

  const baseExtensions = () => [
    lineNumbers(),
    foldGutter(),
    highlightActiveLine(),
    highlightActiveLineGutter(),
    history(),
    search(),
    bracketMatching(),
    indentOnInput(),
    syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
    langCompartment.of([]),
    dirtyListener,
    keymap.of([
      ...defaultKeymap,
      ...historyKeymap,
      ...searchKeymap,
      ...foldKeymap,
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
    }, { dark: true }),
  ];

  // langForPath maps a file's extension to a CM6 language pack.
  // Unknown extensions get plain text (no highlighting); add more
  // packs by importing the package and extending the switch.
  const langForPath = (path: string) => {
    const ext = path.toLowerCase().split('.').pop() ?? '';
    switch (ext) {
      case 'js':
      case 'jsx':
      case 'mjs':
      case 'cjs':
        return javascript({ jsx: ext === 'jsx' });
      case 'ts':
      case 'tsx':
        return javascript({ typescript: true, jsx: ext === 'tsx' });
      case 'json':
        return json();
      case 'md':
      case 'markdown':
        return markdown();
      default:
        return [];
    }
  };

  // ---- lifecycle ----

  let bodyEl!: HTMLDivElement;

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail as BEMessage);
    props.host.addEventListener('wash:msg', onMsg);
    const onState = (ev: Event) => {
      const s = (ev as CustomEvent).detail as PersistedState | null;
      if (s) void restoreFrom(s);
    };
    props.host.addEventListener('wash:state', onState);

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
    const onKey = (ev: KeyboardEvent) => {
      const cmd = ev.ctrlKey || ev.metaKey;
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
    persist();
  });

  // When the active tab changes, swap CM's whole state. We
  // capture-then-restore each tab's EditorState so undo, cursor,
  // and scroll are per-tab. First activation of a tab seeds a
  // fresh state from its baseline + the language matching its
  // path; subsequent activations reuse the captured state.
  createEffect(() => {
    const id = activeID();
    if (!editorView) return;
    const t = tabs().find((x) => x.id === id);
    if (!t) {
      // No active tab — leave the view empty.
      editorView.setState(EditorState.create({ doc: '', extensions: baseExtensions() }));
      return;
    }
    if (t.state) {
      editorView.setState(t.state);
      editorView.dispatch({ effects: langCompartment.reconfigure(langForPath(t.path)) });
    } else {
      const fresh = EditorState.create({
        doc: t.baseline,
        extensions: baseExtensions(),
      });
      editorView.setState(fresh);
      editorView.dispatch({ effects: langCompartment.reconfigure(langForPath(t.path)) });
    }
    editorView.focus();
  });

  // ---- render ----

  return (
    <>
      <div
        ref={bodyEl!}
        style={{ ...bodyStyle, 'grid-template-columns': `${splitPct()}% 4px 1fr` }}
      >
        {/* sidebar */}
        <div data-testid="edit-sidebar" style={sidebarStyle}>
          <div style={sidebarHeaderStyle}>{root() || 'loading…'}</div>
          <div style={sidebarListStyle}>
            <For each={visibleRows()}>
              {(row) => {
                const sel = () => selectedPath() === row.path;
                const isExpanded = () => !!expanded[row.path];
                return (
                  <div
                    data-testid={`edit-entry-${row.entry.name}`}
                    data-type={row.entry.type}
                    data-selected={sel() ? 'true' : undefined}
                    style={rowStyle(sel(), row.depth)}
                    onClick={() => onRowClick(row)}
                  >
                    <span style={{ width: '12px', 'flex-shrink': 0, opacity: 0.6 }}>
                      <Show when={row.entry.type === 'dir'} fallback="">
                        {isExpanded() ? '▾' : '▸'}
                      </Show>
                    </span>
                    <span style={{ 'margin-right': '4px', opacity: 0.8 }}>
                      {row.entry.type === 'dir' ? '📁' : '📄'}
                    </span>
                    <span style={{
                      overflow: 'hidden',
                      'text-overflow': 'ellipsis',
                      'white-space': 'nowrap',
                    }}>
                      {row.entry.name}
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
    </>
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

// ---- styles ----

const bodyStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-rows': '1fr',
  overflow: 'hidden',
  height: '100%',
  'border-bottom': `1px solid ${tokens.borderWindow}`,
};

const sidebarStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  background: tokens.bgMenu,
  overflow: 'hidden',
};

const sidebarHeaderStyle: JSX.CSSProperties = {
  padding: '6px 10px',
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
  color: tokens.fgMuted,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

const sidebarListStyle: JSX.CSSProperties = {
  flex: 1,
  overflow: 'auto',
  padding: '4px 0',
};

function rowStyle(selected: boolean, depth: number): JSX.CSSProperties {
  return {
    display: 'flex',
    'align-items': 'center',
    padding: `2px 8px 2px ${4 + depth * 12}px`,
    background: selected ? tokens.bgRowSelected : 'transparent',
    color: tokens.fg,
    cursor: 'pointer',
    'user-select': 'none',
    font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
    gap: '2px',
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

class WashAppEdit extends HTMLElement {
  private dispose?: () => void;
  connectedCallback() {
    const instance = this.getAttribute('data-wash-instance') ?? '';
    this.style.cssText = [
      'display:grid',
      'grid-template-rows:1fr auto',
      'height:100%',
      'background:' + tokens.bgWindow,
      'color:' + tokens.fg,
      'overflow:hidden',
    ].join(';');
    this.dispose = render(() => <App instance={instance} host={this} />, this);
  }
  disconnectedCallback() {
    this.dispose?.();
  }
}

if (!customElements.get('wash-app-edit')) {
  customElements.define('wash-app-edit', WashAppEdit);
}
