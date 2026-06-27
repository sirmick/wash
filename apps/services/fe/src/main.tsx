// wash-app-services: init system frontend.
//
// Layout:
//   [ toolbar: search · refresh · auto-refresh ]
//   [ scrolling service table — per-row: status badge, name +
//     description, enabled chip, action buttons ]
//   [ status bar: init system · count · last refresh ]
//
// Actions (start/stop/restart/enable/disable) round-trip through the
// BE which routes them through wash-priv; we just show a spinner on
// the row while the request is in flight. show_logs spawns the
// matching log viewer in a fresh window.

import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Button, createAppBus, defineWashApp, Input, StatusBar, tokens } from '@wash/ui';
import { serviceBadge, isActive as isActiveState, isFailed as isFailedState, type BadgeTone } from './service-status.ts';
import {
  Check,
  CircleAlert,
  CircleDashed,
  CirclePlay,
  CircleX,
  FileText,
  Pause,
  Play,
  RefreshCcw,
  RotateCcw,
  Search,
} from 'lucide-solid';

interface Service {
  name: string;
  description: string;
  active: string;
  sub: string;
  load: string;
  enabled: string;
}

interface ServicesList {
  kind: 'services_list';
  init: '' | 'systemd' | 'openrc';
  services: Service[];
}

interface ActionDone {
  kind: 'action_done';
  name: string;
  op: string;
  ok: boolean;
  exit: number;
  stderr: string;
}

type BEMessage = ServicesList | ActionDone | { kind: string; [k: string]: unknown };

const AUTO_REFRESH_MS = 5000;

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [initSys, setInitSys] = createSignal<'' | 'systemd' | 'openrc'>('');
  const [services, setServices] = createSignal<Service[]>([]);
  const [search, setSearch] = createSignal('');
  const [autoRefresh, setAutoRefresh] = createSignal(true);
  const [lastRefresh, setLastRefresh] = createSignal<number>(0);
  // busy maps a tab name → the op currently in-flight against it. Used
  // to spin the row + disable other buttons while priv is prompting.
  const [busy, setBusy] = createSignal<Record<string, string>>({});
  // Per-row last error from action_done — surfaces an action that
  // returned a non-zero exit so the user knows what went wrong.
  const [errors, setErrors] = createSignal<Record<string, string>>({});

  const requestList = () => send({ kind: 'list' });
  const requestAction = (name: string, op: string) => {
    setBusy((b) => ({ ...b, [name]: op }));
    setErrors((e) => {
      if (!(name in e)) return e;
      const out = { ...e };
      delete out[name];
      return out;
    });
    send({ kind: 'action', name, op });
  };
  const requestLogs = (name: string) => send({ kind: 'show_logs', name });

  const handleBE = (m: BEMessage) => {
    if (m.kind === 'services_list') {
      const msg = m as ServicesList;
      setInitSys(msg.init);
      setServices(msg.services ?? []);
      setLastRefresh(Date.now());
      return;
    }
    if (m.kind === 'action_done') {
      const msg = m as ActionDone;
      setBusy((b) => {
        if (!(msg.name in b)) return b;
        const out = { ...b };
        delete out[msg.name];
        return out;
      });
      if (!msg.ok) {
        const detail = msg.stderr.trim() || `exit ${msg.exit}`;
        setErrors((e) => ({ ...e, [msg.name]: detail }));
      }
      return;
    }
  };

  const { send } = createAppBus(props, { onMsg: handleBE });

  let refreshTimer: number | undefined;
  const tickAutoRefresh = () => {
    if (refreshTimer != null) window.clearInterval(refreshTimer);
    if (!autoRefresh()) return;
    refreshTimer = window.setInterval(requestList, AUTO_REFRESH_MS);
  };

  onMount(() => {
    // First listing is unsolicited from the BE; we also request one
    // here so a re-mount (e.g. SaveState reload) gets fresh data.
    requestList();
    tickAutoRefresh();
    onCleanup(() => {
      if (refreshTimer != null) window.clearInterval(refreshTimer);
    });
  });

  // Restart the interval whenever the toggle changes.
  const toggleAutoRefresh = () => {
    setAutoRefresh(!autoRefresh());
    tickAutoRefresh();
  };

  const filtered = createMemo(() => {
    const q = search().trim().toLowerCase();
    const list = services();
    if (!q) return list;
    return list.filter((s) =>
      s.name.toLowerCase().includes(q) ||
      (s.description && s.description.toLowerCase().includes(q))
    );
  });

  return (
    <>
      <div data-testid="srv-toolbar" style={toolbarStyle}>
        <div style={searchWrapStyle}>
          <Search size={12} />
          <Input
            type="text"
            placeholder="Filter services…"
            data-testid="srv-search"
            value={search()}
            onInput={(e) => setSearch(e.currentTarget.value)}
            style={searchInputStyle}
          />
        </div>
        <Button
          variant="icon"
          data-testid="srv-refresh"
          title="Refresh"
          onClick={requestList}
          style={toolbarBtnStyle}
        >
          <RefreshCcw size={14} />
        </Button>
        <label style={toggleLabelStyle} data-testid="srv-auto-label">
          <input
            type="checkbox"
            data-testid="srv-auto"
            checked={autoRefresh()}
            onChange={toggleAutoRefresh}
            style={checkboxStyle}
          />
          <span>Auto-refresh</span>
        </label>
      </div>

      <div style={bodyStyle}>
        <Show
          when={initSys() !== ''}
          fallback={
            <div data-testid="srv-no-init" style={emptyStateStyle}>
              <CircleAlert size={28} />
              <div style={{ 'margin-top': '8px', 'font-size': tokens.fontSizeBase }}>
                No supported init system detected.
              </div>
              <div style={{ 'margin-top': '4px', color: tokens.fgDim }}>
                wash-services supports systemd and OpenRC.
              </div>
            </div>
          }
        >
          <Show
            when={filtered().length > 0}
            fallback={
              <div data-testid="srv-empty" style={emptyStateStyle}>
                <CircleDashed size={28} />
                <div style={{ 'margin-top': '8px' }}>
                  {services().length === 0 ? 'Loading services…' : 'No services match your filter.'}
                </div>
              </div>
            }
          >
            <div data-testid="srv-list" style={listStyle}>
              <For each={filtered()}>
                {(s) => (
                  <ServiceRow
                    service={s}
                    busy={() => busy()[s.name] ?? ''}
                    error={() => errors()[s.name] ?? ''}
                    onAction={(op) => requestAction(s.name, op)}
                    onLogs={() => requestLogs(s.name)}
                  />
                )}
              </For>
            </div>
          </Show>
        </Show>
      </div>

      <StatusBar data-testid="srv-status">
        <span>{initSys() ? `init: ${initSys()}` : 'init: —'}</span>
        <span style={{ 'margin-left': '12px' }}>{filtered().length} / {services().length}</span>
        <Show when={lastRefresh() > 0}>
          <span style={{ 'margin-left': '12px', color: tokens.fgDim }}>
            updated {fmtAgo(lastRefresh())}
          </span>
        </Show>
      </StatusBar>
    </>
  );
};

const ServiceRow: Component<{
  service: Service;
  busy: () => string;
  error: () => string;
  onAction: (op: string) => void;
  onLogs: () => void;
}> = (props) => {
  const s = () => props.service;
  const isActive = () => isActiveState(s().active);
  const isFailed = () => isFailedState(s().active, s().sub);
  const isEnabled = () => s().enabled === 'enabled' || s().enabled === 'alias' || s().enabled === 'static';
  const masked = () => s().enabled === 'masked';
  const isBusy = () => props.busy() !== '';

  return (
    <div
      data-testid={`srv-row-${s().name}`}
      data-active={isActive() ? 'true' : undefined}
      data-failed={isFailed() ? 'true' : undefined}
      style={rowStyle}
    >
      <StatusBadge active={s().active} sub={s().sub} />
      <div style={nameColStyle}>
        <div data-testid={`srv-name-${s().name}`} style={nameStyle}>{s().name}</div>
        <Show when={s().description}>
          <div style={descStyle}>{s().description}</div>
        </Show>
        <Show when={props.error()}>
          <div data-testid={`srv-err-${s().name}`} style={errStyle}>
            <CircleX size={12} /> {props.error()}
          </div>
        </Show>
      </div>
      <EnabledChip enabled={s().enabled} />
      <div style={actionsStyle}>
        <Show
          when={isActive()}
          fallback={
            <Button
              variant="ghost"
              data-testid={`srv-start-${s().name}`}
              title="Start"
              disabled={isBusy() || masked()}
              onClick={() => props.onAction('start')}
              style={iconBtnStyle(false)}
            >
              <Play size={14} />
            </Button>
          }
        >
          <Button
            variant="ghost"
            data-testid={`srv-stop-${s().name}`}
            title="Stop"
            disabled={isBusy()}
            onClick={() => props.onAction('stop')}
            style={iconBtnStyle(false)}
          >
            <Pause size={14} />
          </Button>
        </Show>
        <Button
          variant="ghost"
          data-testid={`srv-restart-${s().name}`}
          title="Restart"
          disabled={isBusy() || masked()}
          onClick={() => props.onAction('restart')}
          style={iconBtnStyle(false)}
        >
          <RotateCcw size={14} />
        </Button>
        <Button
          variant="ghost"
          data-testid={`srv-toggle-${s().name}`}
          title={isEnabled() ? 'Disable' : 'Enable'}
          disabled={isBusy() || masked()}
          onClick={() => props.onAction(isEnabled() ? 'disable' : 'enable')}
          style={iconBtnStyle(isEnabled())}
        >
          <Check size={14} />
        </Button>
        <Button
          variant="ghost"
          data-testid={`srv-logs-${s().name}`}
          title="View logs"
          onClick={props.onLogs}
          style={iconBtnStyle(false)}
        >
          <FileText size={14} />
        </Button>
        <Show when={isBusy()}>
          <span data-testid={`srv-busy-${s().name}`} style={busyTagStyle}>
            {props.busy()}…
          </span>
        </Show>
      </div>
    </div>
  );
};

// Tone → colours. The tone + label decision is the unit-tested kernel
// (service-status.ts); this component only maps tone to presentation.
const TONE_STYLE: Record<BadgeTone, { bg: string; fg: string }> = {
  failed: { bg: tokens.bgDanger, fg: tokens.fgDanger },
  active: { bg: tokens.bgSuccess, fg: tokens.fgSuccess },
  transitioning: { bg: tokens.bgWarning, fg: tokens.fgWarning },
  inactive: { bg: tokens.bgNeutral, fg: tokens.fgDim },
};

const StatusBadge: Component<{ active: string; sub: string }> = (props) => {
  const badge = () => serviceBadge(props.active, props.sub);
  const sty = () => TONE_STYLE[badge().tone];
  const icon = () => {
    switch (badge().tone) {
      case 'failed':
        return <CircleX size={12} />;
      case 'active':
        return <CirclePlay size={12} />;
      default: // transitioning + inactive
        return <CircleDashed size={12} />;
    }
  };
  return (
    <span style={{ ...badgeStyle, background: sty().bg, color: sty().fg }} title={`${props.active} / ${props.sub}`}>
      {icon()}
      <span style={{ 'margin-left': '4px' }}>{badge().label}</span>
    </span>
  );
};

const EnabledChip: Component<{ enabled: string }> = (props) => {
  if (!props.enabled) return <span style={chipDimStyle}>—</span>;
  // Match the `systemctl` idiom: enabled = green (affirmative, starts at
  // boot), masked = red (can't run), and every other config state (disabled,
  // static, alias, linked, indirect, generated) is quiet neutral — no
  // arbitrary accent hue.
  const map: Record<string, { bg: string; fg: string }> = {
    enabled: { bg: tokens.bgSuccess, fg: tokens.fgSuccess },
    masked:  { bg: tokens.bgDanger, fg: tokens.fgDanger },
  };
  const t = map[props.enabled] ?? { bg: tokens.bgNeutral, fg: tokens.fgMuted };
  return <span style={{ ...chipStyle, background: t.bg, color: t.fg }}>{props.enabled}</span>;
};

// ---- helpers ----

function fmtAgo(ts: number): string {
  const s = Math.floor((Date.now() - ts) / 1000);
  if (s < 5) return 'just now';
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  return `${Math.floor(s / 3600)}h ago`;
}

// ---- styles ----

const toolbarStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  background: tokens.bgMenu,
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  padding: '4px 8px',
  gap: '8px',
  'flex-shrink': 0,
  'min-height': '30px',
};

const searchWrapStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '6px',
  background: tokens.bgInset,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '0 8px',
  height: '22px',
  flex: 1,
  'max-width': '320px',
  color: tokens.fgDim,
};

const searchInputStyle: JSX.CSSProperties = {
  flex: 1,
  background: 'transparent',
  border: 'none',
  outline: 'none',
  color: tokens.fg,
  font: tokens.type.textMd,
  height: '20px',
};

// Footprint override on top of <Button variant="icon"> (the icon base
// supplies the transparent chrome, flex-centering, radius and cursor).
const toolbarBtnStyle: JSX.CSSProperties = {
  width: '24px',
  height: '22px',
};

const toggleLabelStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '6px',
  font: tokens.type.textMd,
  color: tokens.fgDim,
  cursor: 'pointer',
  'user-select': 'none',
};

const checkboxStyle: JSX.CSSProperties = {
  margin: 0,
  cursor: 'pointer',
};

const bodyStyle: JSX.CSSProperties = {
  flex: 1,
  overflow: 'auto',
};

const listStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
};

const rowStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': '110px 1fr 90px auto',
  'align-items': 'center',
  gap: '12px',
  padding: '8px 12px',
  'border-bottom': `1px solid ${tokens.borderMenu}`,
};

const nameColStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  gap: '2px',
  'min-width': 0,
};

const nameStyle: JSX.CSSProperties = {
  // Unit names read as the row's primary label in the proportional UI font
  // (mono made the whole list look heavy / terminal-ish).
  font: tokens.type.textMd,
  'font-weight': 600,
  color: tokens.fg,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

const descStyle: JSX.CSSProperties = {
  font: tokens.type.textMd,
  color: tokens.fgDim,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

const errStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '4px',
  font: tokens.type.textSm,
  color: tokens.fgDanger,
  'margin-top': '2px',
};

const badgeStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  padding: '2px 8px',
  'border-radius': `${tokens.radiusSm}`,
  font: tokens.type.textSm,
  'white-space': 'nowrap',
};

const chipStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  'justify-content': 'center',
  padding: '2px 6px',
  'border-radius': `${tokens.radiusSm}`,
  font: tokens.type.textSm,
};

const chipDimStyle: JSX.CSSProperties = {
  color: tokens.fgDim,
  font: tokens.type.textSm,
};

const actionsStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '4px',
};

// Footprint + active-fill override on top of <Button variant="ghost">
// (the ghost base supplies the transparent bg, borderMenu outline, radius,
// color and cursor; this carries the compact square footprint and the
// selected-state background when the action is the current state).
function iconBtnStyle(active: boolean): JSX.CSSProperties {
  return {
    display: 'inline-flex',
    'align-items': 'center',
    'justify-content': 'center',
    width: '26px',
    height: '24px',
    padding: 0,
    ...(active ? { background: tokens.bgRowSelected } : {}),
  };
}

const busyTagStyle: JSX.CSSProperties = {
  font: tokens.type.textSm,
  color: tokens.fgDim,
  'margin-left': '4px',
};

const emptyStateStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  'align-items': 'center',
  'justify-content': 'center',
  padding: '48px 16px',
  color: tokens.fgDim,
  height: '100%',
};

// ---- custom element ----

defineWashApp('wash-app-services', (props) => <App {...props} />, {
  style: `display:flex;flex-direction:column;position:relative;width:100%;height:100%;overflow:hidden;background:${tokens.bgWindow};color:${tokens.fg};font:${tokens.type.textMd};box-sizing:border-box`,
});
