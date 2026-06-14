// wash-app-connect: the windowed front-end for remote hosts
// (docs/REMOTE.md R2, §6.1).
//
// Flow: enter a host → Connect. The BE relays to the com.wash.remote
// supervisor, which SSHes out and reports per-host status + a local
// endpoint over {kind:"remote.state"}. When a host reaches "up" the FE
// attaches the endpoint as a second RouterClient (window.wash.attachRemote)
// so the host's windows composite into this desktop; once attached, the
// host's catalog arrives (window.wash.catalogFor) and the user picks an app
// to launch on it (window.wash.launchOn). Each host is tinted by a stable
// accent matching the window stripe the shell draws (see host-colors.ts).

import { For, Show, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { defineWashApp, tokens, Terminal } from '@wash/ui';

// ----- wire types -----

type HostStatus = 'starting' | 'up' | 'reconnecting' | 'down';

interface HostState {
  host: string;
  origin: string;
  status: HostStatus;
  local_endpoint?: string;
  error?: string;
  // code classifies a "down" status. "auth" means SSH refused auth under
  // BatchMode — the cue to offer the ssh-add widget (docs/REMOTE.md §6.1).
  code?: string;
}

interface RemoteState {
  hosts: HostState[];
}

type CatalogApp = ReturnType<typeof window.wash.catalogFor>[number];

// ----- host accent colour -----
//
// Mirrors web/shell/src/host-colors.ts EXACTLY (same palette order, same
// hash) so a host's status dot here is the same hue as the stripe the
// shell paints on that host's windows. Both sides draw from @wash/ui
// tokens, so the literal colours agree.
const PALETTE: string[] = [
  tokens.accentBlue,
  tokens.accentGreen,
  tokens.accentViolet,
  tokens.accentAmber,
  tokens.accentCyan,
  tokens.accentRed,
];

function hash(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = ((h << 5) - h + s.charCodeAt(i)) | 0;
  return Math.abs(h);
}

function hostColor(origin: string): string {
  return PALETTE[hash(origin) % PALETTE.length];
}

// statusLabel renders a short human label per status.
function statusLabel(s: HostStatus): string {
  switch (s) {
    case 'starting': return 'connecting…';
    case 'up': return 'connected';
    case 'reconnecting': return 'reconnecting…';
    case 'down': return 'disconnected';
  }
}

// ----- app -----

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [hosts, setHosts] = createSignal<HostState[]>([]);
  const [hostInput, setHostInput] = createSignal('');
  // catalogs is a per-origin snapshot kept in a plain object so a catalog
  // arriving (or emptying) re-renders the matching host's app list.
  const [catalogs, setCatalogs] = createSignal<Record<string, CatalogApp[]>>({});
  // Interactive SSH auth (mechanism a): when the BE opens an ssh-add pty
  // it sends the raw channel id; we mount a Terminal on it. auth tracks
  // the host being authenticated + the channel; null = no auth in flight.
  const [auth, setAuth] = createSignal<{ host: string; channel: number } | null>(null);

  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  // attached tracks origins we've called attachRemote for, so a repeated
  // "up" push (e.g. on reconnect) doesn't open a duplicate connection and
  // a vanished host gets detached exactly once.
  const attached = new Set<string>();

  const reconcileAttachments = (list: HostState[]) => {
    const live = new Set<string>();
    for (const h of list) {
      if (h.status === 'up' && h.local_endpoint) {
        live.add(h.origin);
        if (!attached.has(h.origin)) {
          window.wash.attachRemote(h.origin, h.local_endpoint);
          attached.add(h.origin);
        }
      }
    }
    // Detach any origin we'd attached that's no longer up/present.
    for (const origin of [...attached]) {
      if (!live.has(origin)) {
        window.wash.detachRemote(origin);
        attached.delete(origin);
        setCatalogs((c) => { const n = { ...c }; delete n[origin]; return n; });
      }
    }
  };

  const handleBE = (m: any) => {
    switch (m?.kind) {
      case 'remote.state': {
        const st = (m.state ?? {}) as RemoteState;
        const list = Array.isArray(st.hosts) ? st.hosts : [];
        setHosts(list);
        reconcileAttachments(list);
        break;
      }
      case 'auth_opened':
        // The ssh-add pty is live on m.channel_id — mount its terminal.
        setAuth({ host: String(m.host ?? ''), channel: Number(m.channel_id) });
        break;
      case 'auth_closed': {
        // ssh-add exited (key loaded, or the user gave up). Tear the
        // terminal down and retry the connect — if the key loaded, the
        // BatchMode connect now succeeds; if not, we land back on "auth".
        const a = auth();
        setAuth(null);
        if (a?.host) send({ kind: 'connect', host: a.host });
        break;
      }
      case 'auth_error':
        setAuth(null);
        break;
    }
  };

  const beginAuth = (host: string) => send({ kind: 'auth_begin', host });
  const cancelAuth = () => { send({ kind: 'auth_cancel' }); setAuth(null); };

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail);
    props.host.addEventListener('wash:msg', onMsg);
    // Subscribe to the supervisor's host state (relayed via our BE).
    send({ kind: 'subscribe' });
    // Seed + track each host's catalog (delivered over the shell's second
    // RouterClient once attached).
    const seed: Record<string, CatalogApp[]> = {};
    setCatalogs(seed);
    const offCatalog = window.wash.onRemoteCatalog((ev) => {
      setCatalogs((c) => ({ ...c, [ev.origin]: ev.apps }));
    });
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      send({ kind: 'unsubscribe' });
      offCatalog();
    });
  });

  const connect = () => {
    const h = hostInput().trim();
    if (!h) return;
    send({ kind: 'connect', host: h });
    setHostInput('');
  };

  const disconnect = (host: string) => send({ kind: 'disconnect', host });

  const launch = (origin: string, appID: string) => window.wash.launchOn(origin, appID);

  // launchable filters a host's catalog to window-surface, enabled apps —
  // the ones it makes sense to open as a window (background services and
  // the desktop session aren't user-launchable here).
  const launchable = (origin: string): CatalogApp[] =>
    (catalogs()[origin] ?? []).filter((a) => a.surface === 'window' && !a.disabled);

  return (
    <div style={shellStyle}>
      <Header />
      <div style={bodyStyle}>
        <div style={connectRowStyle}>
          <input
            type="text"
            placeholder="user@host"
            value={hostInput()}
            onInput={(e) => setHostInput(e.currentTarget.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') connect(); }}
            style={inputStyle}
            data-testid="connect-host-input"
          />
          <button type="button" onClick={connect} style={connectBtnStyle} data-testid="connect-submit">
            Connect
          </button>
        </div>

        <Show
          when={hosts().length > 0}
          fallback={<div style={emptyStyle}>No remote hosts. Enter a host above to connect.</div>}
        >
          <div style={hostsListStyle} data-testid="connect-hosts">
            <For each={hosts()}>
              {(h) => (
                <HostCard
                  host={h}
                  apps={launchable(h.origin)}
                  onDisconnect={() => disconnect(h.host)}
                  onLaunch={(appID) => launch(h.origin, appID)}
                  onAuth={() => beginAuth(h.host)}
                />
              )}
            </For>
          </div>
        </Show>
      </div>

      <Show when={auth()}>
        <AuthOverlay host={auth()!.host} channel={auth()!.channel} onCancel={cancelAuth} />
      </Show>
    </div>
  );
};

// AuthOverlay hosts the ssh-add pty terminal (docs/REMOTE.md §6.1). The
// user types their key passphrase here; ssh-add loads it into the agent
// and exits, which the BE reports as auth_closed (driving the retry).
const AuthOverlay: Component<{ host: string; channel: number; onCancel: () => void }> = (props) => (
  <div style={authOverlayStyle} data-testid="connect-auth">
    <div style={authHeaderStyle}>
      <span>Unlock SSH key for <strong>{props.host}</strong> — run <code>ssh-add</code></span>
      <button type="button" onClick={props.onCancel} style={disconnectBtnStyle} data-testid="connect-auth-cancel" title="Cancel">✕</button>
    </div>
    <div style={authTermStyle}>
      <Terminal channelId={props.channel} initialCols={80} initialRows={24} contextMenu={false} />
    </div>
    <div style={authHintStyle}>Enter your key passphrase above. When the key loads, the connection retries automatically.</div>
  </div>
);

const Header: Component = () => (
  <div style={headerStyle}>
    <div style={headerIconStyle}>
      <SpriteIcon name="server-cog" size={24} />
    </div>
    <div style={{ flex: 1, 'min-width': 0 }}>
      <div style={headerTitleStyle}>Connect</div>
      <div style={headerSubtitleStyle}>Run apps on another host, in this desktop</div>
    </div>
  </div>
);

const HostCard: Component<{
  host: HostState;
  apps: CatalogApp[];
  onDisconnect: () => void;
  onLaunch: (appID: string) => void;
  onAuth: () => void;
}> = (props) => {
  const color = () => hostColor(props.host.origin);
  const needsAuth = () => props.host.status === 'down' && props.host.code === 'auth';
  return (
    <div style={{ ...hostCardStyle, 'border-left': `3px solid ${color()}` }} data-testid={`connect-host-${props.host.origin}`}>
      <div style={hostHeaderStyle}>
        <span style={{ ...dotStyle, background: color() }} data-testid="connect-host-dot" />
        <span style={hostNameStyle}>{props.host.host}</span>
        <span style={hostStatusStyle} data-testid="connect-host-status" data-status={props.host.status}>
          {statusLabel(props.host.status)}
        </span>
        <Show when={needsAuth()}>
          <button type="button" onClick={props.onAuth} style={authBtnStyle} data-testid="connect-authenticate">
            Authenticate
          </button>
        </Show>
        <button type="button" onClick={props.onDisconnect} style={disconnectBtnStyle} title="Disconnect" data-testid="connect-disconnect">
          ✕
        </button>
      </div>
      <Show when={props.host.error}>
        <div style={errorStyle}>{props.host.error}</div>
      </Show>
      <Show when={props.host.status === 'up'}>
        <Show
          when={props.apps.length > 0}
          fallback={<div style={appsEmptyStyle}>loading apps…</div>}
        >
          <div style={appsGridStyle} data-testid="connect-apps">
            <For each={props.apps}>
              {(app) => (
                <button
                  type="button"
                  style={appBtnStyle}
                  onClick={() => props.onLaunch(app.id)}
                  data-testid={`connect-launch-${app.id}`}
                  title={app.id}
                >
                  <span style={appIconStyle}>
                    <Show when={app.icon} fallback={<span style={{ opacity: 0.3 }}>·</span>}>
                      <SpriteIcon name={app.icon!} size={16} />
                    </Show>
                  </span>
                  <span style={appNameStyle}>{app.name}</span>
                </button>
              )}
            </For>
          </div>
        </Show>
      </Show>
    </div>
  );
};

const SpriteIcon: Component<{ name: string; size: number }> = (props) => (
  <svg
    width={props.size}
    height={props.size}
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    style={{ display: 'block' }}
  >
    <use href={`/icons.svg#${props.name}`} />
  </svg>
);

// ----- styles -----

const shellStyle: JSX.CSSProperties = {
  position: 'relative',
  display: 'grid',
  'grid-template-rows': 'auto 1fr',
  height: '100%',
  background: tokens.bgWindow,
  color: tokens.fg,
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  overflow: 'hidden',
};

const headerStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '12px',
  padding: '14px 18px',
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  background: tokens.bgMenu,
};

const headerIconStyle: JSX.CSSProperties = {
  width: '32px',
  height: '32px',
  display: 'flex',
  'align-items': 'center',
  'justify-content': 'center',
  color: tokens.fg,
  opacity: 0.85,
};

const headerTitleStyle: JSX.CSSProperties = { font: `600 16px ${tokens.fontSans}`, 'line-height': 1.1 };
const headerSubtitleStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
  color: tokens.fgMuted,
  'margin-top': '2px',
};

const bodyStyle: JSX.CSSProperties = { overflow: 'auto', padding: '14px 18px 20px' };

const connectRowStyle: JSX.CSSProperties = { display: 'flex', gap: '8px', 'margin-bottom': '16px' };

const inputStyle: JSX.CSSProperties = {
  flex: 1,
  background: tokens.bgWindow,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}px`,
  padding: '7px 10px',
  font: `${tokens.fontSizeBase} ${tokens.fontMono}`,
  'min-width': 0,
};

const connectBtnStyle: JSX.CSSProperties = {
  background: tokens.accentBlue,
  color: '#fff',
  border: 'none',
  'border-radius': `${tokens.radiusSm}px`,
  padding: '7px 16px',
  font: `600 ${tokens.fontSizeBase} ${tokens.fontSans}`,
  cursor: 'pointer',
  'white-space': 'nowrap',
};

const emptyStyle: JSX.CSSProperties = {
  padding: '24px 0',
  color: tokens.fgMuted,
  'text-align': 'center',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
};

const hostsListStyle: JSX.CSSProperties = { display: 'flex', 'flex-direction': 'column', gap: '10px' };

const hostCardStyle: JSX.CSSProperties = {
  background: tokens.bgMenu,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}px`,
  padding: '10px 12px',
  display: 'flex',
  'flex-direction': 'column',
  gap: '8px',
};

const hostHeaderStyle: JSX.CSSProperties = { display: 'flex', 'align-items': 'center', gap: '8px' };

const dotStyle: JSX.CSSProperties = {
  width: '9px',
  height: '9px',
  'border-radius': '50%',
  flex: '0 0 auto',
};

const hostNameStyle: JSX.CSSProperties = {
  font: `600 ${tokens.fontSizeBase} ${tokens.fontMono}`,
  flex: 1,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

const hostStatusStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
  color: tokens.fgMuted,
};

const disconnectBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fgMuted,
  border: 'none',
  cursor: 'pointer',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  padding: '0 2px',
  'line-height': 1,
};

const authBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.accentAmber,
  border: `1px solid ${tokens.accentAmber}`,
  'border-radius': `${tokens.radiusSm}px`,
  cursor: 'pointer',
  font: `600 ${tokens.fontSizeSm} ${tokens.fontSans}`,
  padding: '2px 8px',
  'white-space': 'nowrap',
};

const authOverlayStyle: JSX.CSSProperties = {
  position: 'absolute',
  inset: '0',
  background: tokens.bgWindow,
  display: 'flex',
  'flex-direction': 'column',
  'z-index': 10,
};

const authHeaderStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '8px',
  padding: '10px 14px',
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  background: tokens.bgMenu,
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
};

const authTermStyle: JSX.CSSProperties = {
  flex: 1,
  'min-height': 0,
  background: '#000',
  padding: '6px',
};

const authHintStyle: JSX.CSSProperties = {
  padding: '8px 14px',
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
  color: tokens.fgMuted,
  'border-top': `1px solid ${tokens.borderMenu}`,
};

const errorStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  color: tokens.fgWarning,
  'word-break': 'break-word',
};

const appsEmptyStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
  color: tokens.fgMuted,
  opacity: 0.7,
};

const appsGridStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': 'repeat(auto-fill, minmax(130px, 1fr))',
  gap: '6px',
};

const appBtnStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '6px',
  padding: '6px 8px',
  background: tokens.bgWindow,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}px`,
  cursor: 'pointer',
  'min-width': 0,
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
  'text-align': 'left',
};

const appIconStyle: JSX.CSSProperties = {
  width: '16px',
  height: '16px',
  display: 'flex',
  'align-items': 'center',
  'justify-content': 'center',
  flex: '0 0 auto',
  opacity: 0.85,
};

const appNameStyle: JSX.CSSProperties = {
  flex: 1,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

defineWashApp('wash-app-connect', (props) => <App {...props} />, {
  style: `display:block;width:100%;height:100%;overflow:hidden;background:${tokens.bgWindow};color:${tokens.fg};font:${tokens.fontSizeBase} ${tokens.fontSans};box-sizing:border-box`,
});
