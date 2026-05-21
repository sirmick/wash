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
import { Splitter, StatusBar, tokens } from '@wash/ui';
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

  // openContent is the text of the currently-open file, populated
  // by a read response. Empty when nothing's open or the file was
  // binary. openMeta carries the per-file status (size, binary,
  // truncated) for the status bar.
  const [openContent, setOpenContent] = createSignal('');
  const [openMeta, setOpenMeta] = createSignal<{ binary: boolean; truncated: boolean; size: number } | null>(null);

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

  const loadFile = async (path: string) => {
    const reply = await sendWithReply({ kind: 'read', path });
    if (reply.kind === 'read_ok') {
      const binary = !!reply.binary;
      const size = Number(reply.size ?? 0);
      const truncated = !!reply.truncated;
      setOpenMeta({ binary, truncated, size });
      setOpenContent(binary ? '' : String(reply.content ?? ''));
    } else {
      setOpenMeta(null);
      setOpenContent('');
    }
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
      void loadFile(row.path);
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

    // Create the EditorView once. Doc + language reconfigure on
    // file open via dispatch + compartment.
    editorView = new EditorView({
      state: EditorState.create({
        doc: '',
        extensions: baseExtensions(),
      }),
      parent: editorMountEl,
    });

    // Boot with a list of "/" — the BE's Confine downshifts to
    // the sandbox root automatically when one is configured.
    void loadDir('/');
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      editorView?.destroy();
      editorView = undefined;
    });
  });

  // When openContent / selectedPath changes, push the new doc and
  // matching language into the CodeMirror view. Untracked
  // editorView access is safe — the ref is set by onMount before
  // any signal change in normal user flows.
  createEffect(() => {
    const text = openContent();
    const path = selectedPath();
    if (!editorView) return;
    editorView.dispatch({
      changes: { from: 0, to: editorView.state.doc.length, insert: text },
      effects: langCompartment.reconfigure(langForPath(path)),
    });
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

        {/* editor area — CodeMirror mounts into editorMountEl. The
            placeholder layer sits on top when nothing is open OR
            the file is binary, since the CM view is created with
            an empty doc and shouldn't visually claim the pane in
            those states. */}
        <div data-testid="edit-pane" style={editorPaneStyle}>
          <div
            ref={editorMountEl!}
            data-testid="edit-cm"
            style={{ position: 'absolute', inset: 0 }}
          />
          <Show when={!selectedPath() || openMeta()?.binary}>
            <div data-testid="edit-placeholder" style={placeholderOverlayStyle}>
              <Show when={!selectedPath()}>Pick a file from the sidebar.</Show>
              <Show when={openMeta()?.binary}>
                <div>{selectedPath()}</div>
                <div style={{ color: tokens.fgDim, 'margin-top': '6px' }}>
                  Binary file — not displayed.
                </div>
              </Show>
            </div>
          </Show>
        </div>
      </div>

      <StatusBar data-testid="edit-status">
        <Show when={selectedPath()} fallback={`${visibleRows().length} entries`}>
          <span>{selectedPath()}</span>
          <Show when={openMeta()}>
            <span style={{ 'margin-left': '12px', color: tokens.fgDim }}>
              {humanSize(openMeta()!.size)}
              <Show when={openMeta()!.truncated}> · truncated</Show>
              <Show when={openMeta()!.binary}> · binary</Show>
            </span>
          </Show>
        </Show>
      </StatusBar>
    </>
  );
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
  position: 'relative',
};

const placeholderStyle: JSX.CSSProperties = {
  padding: '20px 24px',
  color: tokens.fgMuted,
  font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
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
