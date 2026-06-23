// wash-app-packages: search + install/remove/upgrade UI for the
// host's package manager. Search results render as rows; selecting
// an action sends a run_action to the BE which dispatches to wash-
// priv with pty:true. PTY bytes stream back through the BE as
// action_stream app msgs and feed an embedded <Terminal>.
//
// Refresh policy: on action_done with exit 0, refire the current
// search query so the row's Installed state updates. Per session
// memory wash_e2e_pattern: search remains first-class so users can
// keep typing while an install streams in the terminal pane.

import { For, Show, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { CheckCircle2, Download, RefreshCw, Search, Trash2, X, XCircle } from 'lucide-solid';
import { Button, ConfirmDialog, Menu, MenuItem, MenuSeparator, StatusBar, Terminal, defineWashApp, tokens } from '@wash/ui';
import type { TerminalAPI } from '@wash/ui';

interface Package {
  name: string;
  version: string;
  summary: string;
  installed: boolean;
  kind?: string; // "" for regular packages, "task" for tasksel tasks
}

interface GlobalAction {
  id: string;
  label: string;
  desc: string;
  danger?: boolean;
  surface?: 'toolbar' | 'menu';
}

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

type ActionKind = 'install' | 'remove' | 'upgrade_pkg' | 'global';

interface RunningAction {
  kind: ActionKind;
  pkg?: string;
  pkgKind?: string;
  globalID?: string;
  globalLabel?: string;
}

const SEARCH_DEBOUNCE_MS = 200;

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [backendName, setBackendName] = createSignal<string>('');
  const [backendError, setBackendError] = createSignal<string>('');
  const [globalActions, setGlobalActions] = createSignal<GlobalAction[]>([]);
  // Menubar dropdown state: null = closed, otherwise host-relative
  // anchor coords (the Menu lives inside the app's positioned host
  // so its overflow:hidden doesn't clip).
  const [actionsMenu, setActionsMenu] = createSignal<{ x: number; y: number } | null>(null);
  // Pending Update-system confirmation. The toolbar combo touches
  // kernel + every installed package, so we gate it behind an
  // explicit "yes" rather than firing on a stray click.
  const [confirmAction, setConfirmAction] = createSignal<GlobalAction | null>(null);

  const toolbarAction = () => globalActions().find((g) => g.surface === 'toolbar');
  const menuActions = () => globalActions().filter((g) => g.surface !== 'toolbar');

  const [query, setQuery] = createSignal('');
  const [packages, setPackages] = createSignal<Package[]>([]);
  const [searching, setSearching] = createSignal(false);
  const [searchError, setSearchError] = createSignal('');

  const [action, setAction] = createSignal<RunningAction | null>(null);
  const [lastExit, setLastExit] = createSignal<number | null>(null);
  const [lastActionError, setLastActionError] = createSignal('');

  let termAPI: TerminalAPI | null = null;

  const send = (m: unknown) => window.wash.sendAppMsg(props.instance, m);

  // ---- search ----

  let searchTimer: number | undefined;
  const scheduleSearch = (q: string) => {
    if (searchTimer !== undefined) window.clearTimeout(searchTimer);
    searchTimer = window.setTimeout(() => {
      runSearch(q);
    }, SEARCH_DEBOUNCE_MS);
  };

  const runSearch = (q: string) => {
    if (!q.trim()) {
      setPackages([]);
      setSearching(false);
      setSearchError('');
      return;
    }
    setSearching(true);
    send({ kind: 'search', query: q });
  };

  const refireSearch = () => {
    if (query().trim()) runSearch(query());
  };

  // ---- actions ----

  const startAction = (kind: ActionKind, pkg?: string, pkgKind?: string) => {
    if (action()) return;
    if (backendError()) return;
    setAction({ kind, pkg, pkgKind });
    setLastExit(null);
    setLastActionError('');
    termAPI?.write('\x1bc');
    const cols = termAPI?.cols() ?? 80;
    const rows = termAPI?.rows() ?? 24;
    send({ kind: 'run_action', action: kind, pkg: pkg ?? '', pkg_kind: pkgKind ?? '', cols, rows });
  };

  const startGlobal = (g: GlobalAction) => {
    if (action()) return;
    if (backendError()) return;
    setAction({ kind: 'global', globalID: g.id, globalLabel: g.label });
    setLastExit(null);
    setLastActionError('');
    termAPI?.write('\x1bc');
    const cols = termAPI?.cols() ?? 80;
    const rows = termAPI?.rows() ?? 24;
    setActionsMenu(null);
    send({ kind: 'run_action', action: 'global', global_id: g.id, cols, rows });
  };

  const cancelAction = () => {
    if (!action()) return;
    send({ kind: 'action_cancel' });
  };

  // ---- BE ----

  const handleBE = (m: BEMessage) => {
    switch (m.kind) {
      case 'backend_status':
        setBackendName(String(m.name ?? ''));
        setBackendError(String(m.error ?? ''));
        setGlobalActions(Array.isArray(m.global_actions) ? (m.global_actions as GlobalAction[]) : []);
        return;
      case 'search_result':
        // Drop late replies for stale queries — the user has typed
        // past this query and a fresher reply is on its way.
        if (String(m.query ?? '') !== query()) return;
        setSearching(false);
        setSearchError(String(m.error ?? ''));
        setPackages(Array.isArray(m.packages) ? (m.packages as Package[]) : []);
        return;
      case 'action_started':
        return;
      case 'action_stream': {
        const b64 = m.bytes;
        if (typeof b64 !== 'string') return;
        const bytes = base64ToBytes(b64);
        termAPI?.write(bytes);
        return;
      }
      case 'action_done': {
        const exit = Number(m.exit ?? -1);
        setLastExit(exit);
        setLastActionError(String(m.error ?? ''));
        const a = action();
        setAction(null);
        if (exit === 0 && a) {
          // Refire the current search so the row reflects new
          // installed-state. upgrade_system isn't tied to a package
          // so we just refresh whatever the user has typed.
          refireSearch();
        }
        return;
      }
    }
  };

  // ---- lifecycle ----

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail as BEMessage);
    props.host.addEventListener('wash:msg', onMsg);
    send({ kind: 'hello' });
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      if (searchTimer !== undefined) window.clearTimeout(searchTimer);
    });
  });

  // Push resize updates to BE→priv whenever the terminal reflows.
  // The terminal's onResize fires immediately on ready and on every
  // subsequent fit; an active priv PTY listens for our resize msg.
  const handleTermResize = (cols: number, rows: number) => {
    if (action()) {
      send({ kind: 'action_resize', cols, rows });
    }
  };

  // Forward xterm keystrokes during an action to priv stdin.
  // Outside an action, drop input — there's no PTY to feed.
  const handleTermInput = (bytes: Uint8Array) => {
    if (!action()) return;
    send({ kind: 'action_input', bytes: bytesToBase64(bytes) });
  };

  // ---- derived ----

  const canRunActions = () => !backendError() && !action();

  // Visual state of the terminal pane: idle / running / success /
  // failure. Drives header tint + body dim so the user can glance
  // at the pane and tell where their last action landed without
  // reading the exit code.
  type TermStatus = 'idle' | 'running' | 'success' | 'failure';
  const termStatus = (): TermStatus => {
    if (action()) return 'running';
    if (lastExit() === null) return 'idle';
    return lastExit() === 0 ? 'success' : 'failure';
  };

  // Whether the terminal pane is showing. The <Terminal> stays mounted
  // (so termAPI is live the instant an action starts — startAction
  // resets it and reads its cols/rows), but the pane collapses to zero
  // height while idle so there's no black bar parked under the package
  // list doing nothing. It expands once an action runs and stays up to
  // show the result (exit code + output) until the next action begins.
  const termVisible = (): boolean => action() !== null || lastExit() !== null;

  // Action button per row. Three buttons potentially: install (if
  // not installed), upgrade (if installed and supported for its
  // kind), remove (if installed). Tasksel tasks have no per-task
  // upgrade — packages inside the task upgrade individually — so
  // the Upgrade button hides for kind="task".
  const rowButtons = (p: Package): JSX.Element => (
    <div style={{ display: 'flex', gap: '6px' }}>
      <Show when={!p.installed}>
        <Button
          size="sm"
          variant="default"
          disabled={!canRunActions()}
          onClick={() => startAction('install', p.name, p.kind)}
          data-testid={`pkg-install-${p.name}`}
        >
          <Download size={12} /> Install
        </Button>
      </Show>
      <Show when={p.installed}>
        <Show when={p.kind !== 'task'}>
          <Button
            size="sm"
            disabled={!canRunActions()}
            onClick={() => startAction('upgrade_pkg', p.name, p.kind)}
            data-testid={`pkg-upgrade-${p.name}`}
          >
            <RefreshCw size={12} /> Upgrade
          </Button>
        </Show>
        <Button
          size="sm"
          variant="danger"
          disabled={!canRunActions()}
          onClick={() => startAction('remove', p.name, p.kind)}
          data-testid={`pkg-remove-${p.name}`}
        >
          <Trash2 size={12} /> Remove
        </Button>
      </Show>
    </div>
  );

  return (
    <>
      {/* Menubar — single "Actions" dropdown for the granular ops
          (Update, Upgrade, Dist-upgrade, Autoremove). The Menu
          mounts inside the wash-app host which has position:relative,
          so we use host-relative coords (edit's pattern) and avoid
          the overflow:hidden clipping the old viewport-anchored
          version hit. */}
      <div style={menubarStyle}>
        <button
          type="button"
          data-testid="pkg-actions-button"
          disabled={!canRunActions() || menuActions().length === 0}
          style={menubarBtnStyle(!!actionsMenu())}
          onClick={(e) => {
            if (actionsMenu()) {
              setActionsMenu(null);
              return;
            }
            // Viewport coords — Menu portals to body with position:fixed.
            const btnRect = (e.currentTarget as HTMLButtonElement).getBoundingClientRect();
            setActionsMenu({ x: btnRect.left, y: btnRect.bottom + 2 });
          }}
        >
          Actions
        </button>
        <Show when={actionsMenu()}>
          <Menu
            x={actionsMenu()!.x}
            y={actionsMenu()!.y}
            onDismiss={() => setActionsMenu(null)}
            data-testid="pkg-actions-menu"
            style={{ 'min-width': '220px' }}
          >
            <For each={menuActions()}>
              {(g, i) => (
                <>
                  <Show when={g.danger && i() > 0 && !menuActions()[i() - 1].danger}>
                    <MenuSeparator />
                  </Show>
                  <MenuItem
                    label={g.label}
                    onClick={() => { setActionsMenu(null); startGlobal(g); }}
                    data-testid={`pkg-action-${g.id}`}
                  />
                </>
              )}
            </For>
          </Menu>
        </Show>
      </div>

      {/* Toolbar — search owns the full width, Update System sits
          flush right. Search container has min-width:0 so it can
          shrink under the button; button is flex-shrink:0 + matching
          height so it lines up cleanly with the input. */}
      <div style={toolbarStyle}>
        <div style={searchContainerStyle}>
          <Search size={14} style={searchIconStyle} />
          <input
            type="text"
            data-testid="pkg-search"
            placeholder="Search packages…"
            value={query()}
            onInput={(e) => {
              setQuery(e.currentTarget.value);
              scheduleSearch(e.currentTarget.value);
            }}
            style={searchInputStyle}
            disabled={!!backendError()}
          />
        </div>
        <Show when={toolbarAction()}>
          <Button
            size="sm"
            disabled={!canRunActions()}
            onClick={() => setConfirmAction(toolbarAction()!)}
            data-testid="pkg-update-system"
            title={toolbarAction()!.desc}
            style={upgradeSystemBtnStyle}
          >
            <RefreshCw size={12} /> {toolbarAction()!.label}
          </Button>
        </Show>
      </div>

      <Show when={backendError()}>
        <div style={errorBannerStyle} data-testid="pkg-backend-error">
          {backendError()}
        </div>
      </Show>

      {/* Results list */}
      <div style={resultsContainerStyle} data-testid="pkg-results">
        <Show
          when={packages().length > 0}
          fallback={
            <div style={emptyStateStyle}>
              <Show
                when={query().trim()}
                fallback={<span>Type to search the {backendName() || 'package'} catalog</span>}
              >
                <Show
                  when={!searching()}
                  fallback={<span>Searching…</span>}
                >
                  <Show
                    when={!searchError()}
                    fallback={<span style={{ color: tokens.borderDanger }}>{searchError()}</span>}
                  >
                    <span>No matches for "{query()}"</span>
                  </Show>
                </Show>
              </Show>
            </div>
          }
        >
          <For each={packages()}>
            {(p) => (
              <div style={rowStyle} data-testid={`pkg-row-${p.name}`}>
                <div style={{ flex: 1, 'min-width': 0 }}>
                  <div style={rowNameStyle}>
                    <span data-testid={`pkg-name-${p.name}`}>{p.name}</span>
                    <Show when={p.kind === 'task'}>
                      <span style={kindBadgeStyle} data-testid={`pkg-kind-${p.name}`}>task</span>
                    </Show>
                    <Show when={p.installed}>
                      <span style={installedBadgeStyle} data-testid={`pkg-installed-${p.name}`}>
                        installed
                        <Show when={p.version}>{` ${p.version}`}</Show>
                      </span>
                    </Show>
                  </div>
                  <div style={rowSummaryStyle}>{p.summary}</div>
                </div>
                {rowButtons(p)}
              </div>
            )}
          </For>
        </Show>
      </div>

      {/* Terminal pane — stays mounted so action_stream always has a live
          termAPI, but collapses to zero height while idle (hidden until an
          action runs). When an action finishes the pane dims and the header
          tints green (success) or red (failure) so the result reads at a
          glance. */}
      <div style={termPaneStyle(termVisible())} data-testid="pkg-term-pane" data-status={termStatus()} data-visible={termVisible() ? '1' : '0'}>
        <div style={termHeaderForStatus(termStatus())}>
          <span style={{ display: 'flex', 'align-items': 'center', gap: '6px', color: tokens.fgMuted, 'font-size': tokens.fontSizeMd }}>
            <Show when={termStatus() === 'success'}>
              <CheckCircle2 size={14} color={successColor} />
            </Show>
            <Show when={termStatus() === 'failure'}>
              <XCircle size={14} color={tokens.borderDanger} />
            </Show>
            <Show
              when={action()}
              fallback={
                <Show
                  when={lastExit() !== null}
                  fallback={<>Output will appear here</>}
                >
                  <span data-testid="pkg-action-done">
                    {termStatus() === 'success' ? 'Done' : 'Failed'} — exit code <strong>{lastExit()}</strong>
                    <Show when={lastActionError()}>
                      <span style={{ color: tokens.borderDanger, 'margin-left': '8px' }}>
                        {lastActionError()}
                      </span>
                    </Show>
                  </span>
                </Show>
              }
            >
              <span data-testid="pkg-action-running">
                {action()!.kind === 'global'
                  ? `${action()!.globalLabel}…`
                  : `${action()!.kind}: ${action()!.pkg}`}
              </span>
            </Show>
          </span>
          <Show when={action()}>
            <Button size="sm" variant="ghost" onClick={cancelAction} data-testid="pkg-action-cancel">
              <X size={12} /> Cancel
            </Button>
          </Show>
        </div>
        <div style={termBodyForStatus(termStatus())}>
          <Terminal
            onReady={(api) => {
              termAPI = api;
            }}
            onResize={handleTermResize}
            onInput={handleTermInput}
          />
        </div>
      </div>

      <Show when={confirmAction()}>
        <ConfirmDialog
          title={`${confirmAction()!.label}?`}
          confirmLabel="Update"
          danger
          data-testid="pkg-confirm-update-system"
          confirmTestid="pkg-confirm-update-system-ok"
          cancelTestid="pkg-confirm-update-system-cancel"
          onCancel={() => setConfirmAction(null)}
          onConfirm={() => {
            const a = confirmAction()!;
            setConfirmAction(null);
            startGlobal(a);
          }}
        >
          <div style={{ 'line-height': 1.5 }}>
            <p style={{ margin: '0 0 8px 0' }}>
              This refreshes the package index and then upgrades every
              installed package — including the kernel and any held
              dependencies the dist-upgrade resolver pulls in.
            </p>
            <p style={{ margin: 0, color: tokens.fgMuted, 'font-family': tokens.fontMono, 'font-size': tokens.fontSizeMd }}>
              {confirmAction()!.desc}
            </p>
          </div>
        </ConfirmDialog>
      </Show>

      <StatusBar data-testid="pkg-status">
        <Show
          when={backendName()}
          fallback={<span>no backend</span>}
        >
          <span>backend: {backendName()}</span>
        </Show>
        <Show when={packages().length > 0}>
          <span style={{ 'margin-left': '12px', color: tokens.fgDim }}>
            {packages().length} result{packages().length === 1 ? '' : 's'}
          </span>
        </Show>
      </StatusBar>
    </>
  );
};

// ---- base64 helpers ----

function base64ToBytes(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function bytesToBase64(b: Uint8Array): string {
  let s = '';
  for (let i = 0; i < b.length; i++) s += String.fromCharCode(b[i]);
  return btoa(s);
}

// ---- styles ----

const menubarStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'stretch',
  gap: '2px',
  padding: '0 4px',
  background: tokens.bgMenu,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  'flex-shrink': 0,
  position: 'relative', // Menu (position:absolute) anchors inside.
};

function menubarBtnStyle(open: boolean): JSX.CSSProperties {
  return {
    background: open ? tokens.bgRowHover : 'transparent',
    color: tokens.fg,
    border: 'none',
    padding: '4px 10px',
    cursor: 'pointer',
    font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
  };
}

const toolbarStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '8px',
  padding: '8px 10px',
  background: tokens.bgWindow,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  'flex-shrink': 0,
};

const TOOLBAR_CTRL_HEIGHT = 28;

const searchContainerStyle: JSX.CSSProperties = {
  position: 'relative',
  flex: 1,
  // Without min-width:0, the input's intrinsic width keeps the
  // container at its preferred size and the sibling button gets
  // pushed off the right edge of the window.
  'min-width': 0,
  display: 'flex',
};

const searchIconStyle: JSX.CSSProperties = {
  position: 'absolute',
  left: '8px',
  top: '50%',
  transform: 'translateY(-50%)',
  color: tokens.fgMuted,
  'pointer-events': 'none',
};

const searchInputStyle: JSX.CSSProperties = {
  width: '100%',
  height: `${TOOLBAR_CTRL_HEIGHT}px`,
  padding: '0 10px 0 28px',
  background: tokens.bgMenu,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}`,
  color: tokens.fg,
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  outline: 'none',
  'box-sizing': 'border-box',
};

const upgradeSystemBtnStyle: JSX.CSSProperties = {
  // Match the search input height + center the icon/label so the
  // toolbar reads as one row instead of two stacked controls.
  height: `${TOOLBAR_CTRL_HEIGHT}px`,
  padding: '0 12px',
  display: 'inline-flex',
  'align-items': 'center',
  gap: '6px',
  'flex-shrink': 0,
  'box-sizing': 'border-box',
};

const errorBannerStyle: JSX.CSSProperties = {
  padding: '8px 12px',
  background: tokens.bgDanger,
  color: tokens.fg,
  'border-bottom': `1px solid ${tokens.borderDanger}`,
  'font-size': tokens.fontSizeMd,
  'flex-shrink': 0,
};

const resultsContainerStyle: JSX.CSSProperties = {
  flex: 1,
  'min-height': 0,
  'overflow-y': 'auto',
  background: tokens.bgWindow,
};

const emptyStateStyle: JSX.CSSProperties = {
  padding: '24px',
  color: tokens.fgDim,
  'text-align': 'center',
  'font-size': tokens.fontSizeBase,
};

const rowStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '12px',
  padding: '8px 12px',
  'border-bottom': `1px solid ${tokens.borderMenu}`,
};

const rowNameStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '8px',
  color: tokens.fg,
  'font-size': tokens.fontSizeBase,
  'font-weight': 500,
};

const installedBadgeStyle: JSX.CSSProperties = {
  'font-size': tokens.fontSizeSm,
  color: tokens.fgMuted,
  background: tokens.bgRowSelected,
  padding: '1px 6px',
  'border-radius': `${tokens.radiusSm}`,
  'font-weight': 'normal',
};

// Tinted blue badge so collection-style rows (tasksel tasks today,
// dnf groups / flatpak later) read at a glance against the regular-
// package neutral row chrome.
const kindBadgeStyle: JSX.CSSProperties = {
  'font-size': tokens.fontSizeSm,
  color: tokens.fgInfo,
  background: tokens.bgInfo,
  border: `1px solid ${tokens.borderFocus}`,
  padding: '0 6px',
  'border-radius': `${tokens.radiusSm}`,
  'font-weight': 'normal',
};

const rowSummaryStyle: JSX.CSSProperties = {
  color: tokens.fgMuted,
  'font-size': tokens.fontSizeMd,
  'margin-top': '2px',
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

// Success/failure accent colors. Pulled out as constants so the
// header tint and the status icon agree. Greens picked to match the
// tokens palette — a dim background tint plus a brighter glyph.
const successColor = tokens.fgSuccess;
const successBg = tokens.bgSuccess;
const successBorder = tokens.fgSuccess;
const failureBg = tokens.bgDanger;
const failureBorder = tokens.fgDanger;

const termPaneBaseStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  background: tokens.bgCanvas,
  'flex-shrink': 0,
  overflow: 'hidden',
  transition: 'height 160ms ease, min-height 160ms ease',
};

// Collapsed (idle) vs expanded (an action ran / is running). Collapsing to
// zero height — rather than unmounting — keeps the xterm + termAPI alive
// so startAction can reset and size it immediately.
function termPaneStyle(visible: boolean): JSX.CSSProperties {
  if (!visible) {
    return { ...termPaneBaseStyle, height: '0px', 'min-height': '0px', 'border-top': '1px solid transparent' };
  }
  return { ...termPaneBaseStyle, height: '40%', 'min-height': '180px', 'border-top': `1px solid ${tokens.borderMenu}` };
}

const termHeaderBaseStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  'justify-content': 'space-between',
  padding: '4px 10px',
  background: tokens.bgMenu,
  'border-top': '2px solid transparent',
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  'flex-shrink': 0,
  transition: 'background 150ms ease, border-color 150ms ease',
};

function termHeaderForStatus(status: 'idle' | 'running' | 'success' | 'failure'): JSX.CSSProperties {
  switch (status) {
    case 'success':
      return { ...termHeaderBaseStyle, background: successBg, 'border-top-color': successBorder };
    case 'failure':
      return { ...termHeaderBaseStyle, background: failureBg, 'border-top-color': failureBorder };
    default:
      return termHeaderBaseStyle;
  }
}

// Dim the terminal body once the action settles so the finished
// state reads as "frozen" — output is still legible but visually
// recedes versus a live, running stream.
function termBodyForStatus(status: 'idle' | 'running' | 'success' | 'failure'): JSX.CSSProperties {
  const base: JSX.CSSProperties = {
    flex: 1,
    position: 'relative',
    'min-height': 0,
    transition: 'opacity 150ms ease',
  };
  if (status === 'success' || status === 'failure') {
    return { ...base, opacity: 0.65 };
  }
  return base;
}

defineWashApp('wash-app-packages', (props) => <App {...props} />, {
  // position:relative kept as defense in depth for in-app overlays.
  // Menus themselves portal to document.body with position:fixed,
  // so they don't depend on the host being a containing block.
  style:
    `display:flex;flex-direction:column;width:100%;height:100%;box-sizing:border-box;background:${tokens.bgWindow};color:${tokens.fg};overflow:hidden;position:relative`,
});
