// wash-app-connect: the windowed front-end for remote hosts
// (docs/REMOTE.md R2, §6.1).
//
// Flow: enter a host → Add (save it) or Launch (connect now). The BE relays
// to the com.wash.remote supervisor, which SSHes out and reports per-host
// status + a local endpoint over {kind:"remote.state"}. When a host reaches
// "up" the FE attaches it as a second RouterClient (window.wash.attachRemote)
// so its windows composite into this desktop; once attached, the host's
// catalog arrives (window.wash.catalogFor) and the user picks an app to run
// from a dropdown (window.wash.launchOn).
//
// The list has two sections: hosts you've connected to (top, bordered in the
// host's window-hint colour) and saved-but-unconnected bookmarks (below).
// Every entry has a Launch button whose dropdown lists launchable apps; a
// connected entry lists its live catalog, so you can launch another app at
// any time.

import { For, Show, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { createAppBus, defineWashApp, tokens, Terminal } from '@wash/ui';

// ----- wire types -----

type HostStatus = 'starting' | 'up' | 'reconnecting' | 'down';

interface HostState {
  host: string;
  origin: string;
  status: HostStatus;
  error?: string;
  // code classifies a "down" status. "auth" means SSH refused auth under
  // BatchMode — the cue to offer the ssh-add widget (docs/REMOTE.md §6.1).
  code?: string;
}

type MountStatus = 'mounting' | 'mounted' | 'error';

// MountState is one remote folder mounted locally over SFTP, surfaced under its
// host. mount_point is the local path (~/wash/remote/<host>/<base>).
interface MountState {
  host: string;
  remote_root: string;
  mount_point: string;
  status: MountStatus;
  error?: string;
}

// Candidate is a host found on the network (mDNS today) that isn't yet
// connected or bookmarked — rendered under "On your network". addr is the
// reliable SSH target (an IP); host is the friendly display name. wash=true
// means it announced itself as a wash box.
interface Candidate {
  host: string;
  addr: string;
  port?: number;
  source: string;
  wash?: boolean;
}

interface RemoteState {
  hosts: HostState[];
  mounts?: MountState[];
  candidates?: Candidate[];
}

// Bookmark is a saved connect target (persisted on disk by the BE). Host-
// level: the app to run is chosen from the dropdown at launch time. (app_id
// is retained in the type for on-disk back-compat but no longer set.)
interface Bookmark {
  host: string;
  app_id?: string;
  label?: string;
}

type CatalogApp = ReturnType<typeof window.wash.catalogFor>[number];

// ----- host accent colour -----
//
// Mirrors web/shell/src/host-colors.ts EXACTLY (same palette order, same
// hash) so an entry's border is the same hue as the stripe the shell paints
// on that host's windows. Both sides draw from @wash/ui tokens, so the
// literal colours agree.
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
  const [mounts, setMounts] = createSignal<MountState[]>([]);
  const [candidates, setCandidates] = createSignal<Candidate[]>([]);
  const [hostInput, setHostInput] = createSignal('');
  // catalogs is a per-origin snapshot kept in a plain object so a catalog
  // arriving (or emptying) re-renders the matching host's app list.
  const [catalogs, setCatalogs] = createSignal<Record<string, CatalogApp[]>>({});
  // Interactive SSH auth (mechanism a): when the BE opens an ssh-add pty
  // it sends the raw channel id; we mount a Terminal on it. auth tracks
  // the host being authenticated + the channel; null = no auth in flight.
  const [auth, setAuth] = createSignal<{ host: string; channel: number } | null>(null);
  const [bookmarks, setBookmarks] = createSignal<Bookmark[]>([]);
  // menuFor holds the origin whose Launch dropdown is open (one at a time).
  const [menuFor, setMenuFor] = createSignal<string | null>(null);

  // pendingOpenMenu holds hosts we just connected via a Launch click; when the
  // host comes up and its catalog lands, its dropdown auto-opens so "Launch"
  // on a new/bookmarked host ends in the same app-picker as a connected one.
  const pendingOpenMenu = new Set<string>();

  // attached tracks origins we've called attachRemote for, so a repeated
  // "up" push (e.g. on reconnect) doesn't open a duplicate connection and
  // a vanished host gets detached exactly once.
  const attached = new Set<string>();

  const reconcileAttachments = (list: HostState[]) => {
    const live = new Set<string>();
    for (const h of list) {
      if (h.status === 'up') {
        live.add(h.origin);
        if (!attached.has(h.origin)) {
          // One-port relay: attachRemote with no url asks the shell to mux
          // this host over its single connection to A (docs/REMOTE.md).
          window.wash.attachRemote(h.origin);
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
        if (menuFor() === origin) setMenuFor(null);
      }
    }
  };

  const handleBE = (m: any) => {
    switch (m?.kind) {
      case 'remote.state': {
        const st = (m.state ?? {}) as RemoteState;
        const list = Array.isArray(st.hosts) ? st.hosts : [];
        setHosts(list);
        setMounts(Array.isArray(st.mounts) ? st.mounts : []);
        setCandidates(Array.isArray(st.candidates) ? st.candidates : []);
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
      case 'bookmarks':
        setBookmarks(Array.isArray(m.bookmarks) ? (m.bookmarks as Bookmark[]) : []);
        break;
    }
  };

  const beginAuth = (host: string) => send({ kind: 'auth_begin', host });
  const cancelAuth = () => { send({ kind: 'auth_cancel' }); setAuth(null); };

  // tryAutoOpenMenu fires once a pending host is connected and its catalog
  // actually lists apps — opening its Launch dropdown.
  const tryAutoOpenMenu = (origin: string) => {
    const host = hosts().find((h) => h.origin === origin)?.host ?? origin;
    if (!pendingOpenMenu.has(host) && !pendingOpenMenu.has(origin)) return;
    if (launchable(origin).length > 0) {
      pendingOpenMenu.delete(host);
      pendingOpenMenu.delete(origin);
      setMenuFor(origin);
    }
  };

  const { send } = createAppBus(props, { onMsg: handleBE });

  // ----- bookmarks (host-level) -----
  const persistBookmarks = (next: Bookmark[]) => {
    setBookmarks(next);
    send({ kind: 'bookmarks_save', bookmarks: next });
  };
  const isBookmarked = (host: string) => bookmarks().some((b) => b.host === host);
  const addBookmark = (host: string, label?: string) => {
    if (!host || isBookmarked(host)) return;
    persistBookmarks([...bookmarks(), { host, label: label || host }]);
  };
  const removeBookmark = (host: string) => persistBookmarks(bookmarks().filter((b) => b.host !== host));

  // connectHost connects (the host appears in the connected section when up)
  // and queues its Launch dropdown to auto-open once apps arrive.
  const connectHost = (host: string, port?: number) => {
    if (!host) return;
    // remote_port is only sent for a non-default port (a discovered peer
    // advertising e.g. 2222); the BE dials it with ssh -p. 0/undefined ⇒ 22.
    send({ kind: 'connect', host, remote_port: port || 0 });
    pendingOpenMenu.add(host);
  };

  onMount(() => {
    // Subscribe to the supervisor's host state (relayed via our BE) and
    // load saved bookmarks.
    send({ kind: 'subscribe' });
    send({ kind: 'bookmarks_load' });
    // Seed + track each host's catalog (delivered over the shell's second
    // RouterClient once attached). A freshly-arrived catalog may satisfy a
    // pending auto-open.
    setCatalogs({});
    const offCatalog = window.wash.onRemoteCatalog((ev) => {
      setCatalogs((c) => ({ ...c, [ev.origin]: ev.apps }));
      tryAutoOpenMenu(ev.origin);
    });
    onCleanup(() => {
      send({ kind: 'unsubscribe' });
      offCatalog();
    });
  });

  const addFromInput = () => {
    const h = hostInput().trim();
    if (!h) return;
    addBookmark(h);
    setHostInput('');
  };
  const launchFromInput = () => {
    const h = hostInput().trim();
    if (!h) return;
    connectHost(h);
    setHostInput('');
  };

  const disconnect = (host: string) => send({ kind: 'disconnect', host });
  const launch = (origin: string, appID: string) => {
    window.wash.launchOn(origin, appID);
    setMenuFor(null);
  };

  // launchable filters a host's catalog to window-surface, enabled apps —
  // the ones it makes sense to open as a window (background services and
  // the desktop session aren't user-launchable here).
  const launchable = (origin: string): CatalogApp[] =>
    (catalogs()[origin] ?? []).filter((a) => a.surface === 'window' && !a.disabled);

  // The bottom section: bookmarks that aren't currently connected.
  const connectedHostNames = () => new Set(hosts().map((h) => h.host));
  const bookmarkedOnly = () => {
    const live = connectedHostNames();
    return bookmarks().filter((b) => !live.has(b.host));
  };

  // Discovered hosts not already connected (by addr or origin) or saved as a
  // bookmark — so a candidate disappears from this section the moment you act
  // on it.
  const candidateOnly = () => {
    const live = connectedHostNames();
    const origins = new Set(hosts().map((h) => h.origin));
    const saved = new Set(bookmarks().map((b) => b.host));
    return candidates().filter(
      (c) => !live.has(c.addr) && !origins.has(c.addr) && !saved.has(c.addr),
    );
  };

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
            onKeyDown={(e) => { if (e.key === 'Enter') launchFromInput(); }}
            style={inputStyle}
            data-testid="connect-host-input"
          />
          <button type="button" onClick={addFromInput} style={addBtnStyle} data-testid="connect-add">
            Add
          </button>
          <button type="button" onClick={launchFromInput} style={launchBtnStyle} data-testid="connect-submit">
            Launch
          </button>
        </div>

        <Show
          when={hosts().length > 0 || bookmarkedOnly().length > 0 || candidateOnly().length > 0}
          fallback={<div style={emptyStyle}>No hosts yet. Enter a host above, then Add it or Launch to connect.</div>}
        >
          {/* Connected hosts — bordered in the host's window-hint colour. */}
          <Show when={hosts().length > 0}>
            <div style={sectionLabelStyle}>Connected</div>
            <div style={listStyle} data-testid="connect-hosts">
              <For each={hosts()}>
                {(h) => (
                  <HostRow
                    host={h}
                    apps={launchable(h.origin)}
                    mounts={mounts().filter((mt) => mt.host === h.host)}
                    bookmarked={isBookmarked(h.host)}
                    menuOpen={menuFor() === h.origin}
                    onToggleMenu={() => setMenuFor(menuFor() === h.origin ? null : h.origin)}
                    onCloseMenu={() => setMenuFor(null)}
                    onLaunch={(appID) => launch(h.origin, appID)}
                    onDisconnect={() => disconnect(h.host)}
                    onAuth={() => beginAuth(h.host)}
                    onToggleBookmark={() => (isBookmarked(h.host) ? removeBookmark(h.host) : addBookmark(h.host))}
                    onMount={(root, persist) => send({ kind: 'mount', host: h.host, remote_root: root, persist })}
                    onUnmount={(mp) => send({ kind: 'unmount', mount_point: mp })}
                  />
                )}
              </For>
            </div>
          </Show>

          {/* Saved but not connected. */}
          <Show when={bookmarkedOnly().length > 0}>
            <div style={sectionLabelStyle}>Bookmarks</div>
            <div style={listStyle} data-testid="connect-bookmarks">
              <For each={bookmarkedOnly()}>
                {(bm) => (
                  <BookmarkRow
                    bookmark={bm}
                    onLaunch={() => connectHost(bm.host)}
                    onRemove={() => removeBookmark(bm.host)}
                  />
                )}
              </For>
            </div>
          </Show>

          {/* Auto-discovered on the local network (mDNS). Connecting uses the
              addr (an IP, reliable) under your current username; the friendly
              name is kept as the bookmark label if you save it. */}
          <Show when={candidateOnly().length > 0}>
            <div style={sectionLabelStyle}>On your network</div>
            <div style={listStyle} data-testid="connect-candidates">
              <For each={candidateOnly()}>
                {(c) => (
                  <CandidateRow
                    candidate={c}
                    onConnect={() => connectHost(c.addr, c.port)}
                    onSave={() => addBookmark(c.addr, c.host)}
                  />
                )}
              </For>
            </div>
          </Show>
        </Show>
      </div>

      <Show when={auth()}>
        <AuthOverlay host={auth()!.host} channel={auth()!.channel} onCancel={cancelAuth} />
      </Show>
    </div>
  );
};

// HostRow is one connected (or connecting) host. Its border carries the
// host's window-hint colour once up. The Launch dropdown lists the host's
// live catalog so any app can be launched at any time.
const HostRow: Component<{
  host: HostState;
  apps: CatalogApp[];
  mounts: MountState[];
  bookmarked: boolean;
  menuOpen: boolean;
  onToggleMenu: () => void;
  onCloseMenu: () => void;
  onLaunch: (appID: string) => void;
  onDisconnect: () => void;
  onAuth: () => void;
  onToggleBookmark: () => void;
  onMount: (remoteRoot: string, persist: boolean) => void;
  onUnmount: (mountPoint: string) => void;
}> = (props) => {
  const color = () => hostColor(props.host.origin);
  const up = () => props.host.status === 'up';
  const needsAuth = () => props.host.status === 'down' && props.host.code === 'auth';
  return (
    <div
      style={{ ...rowStyle, 'border-color': up() ? color() : tokens.borderMenu }}
      data-testid={`connect-host-${props.host.origin}`}
    >
      <div style={rowMainStyle}>
        <span style={{ ...dotStyle, background: up() ? color() : tokens.fgMuted }} data-testid="connect-host-dot" />
        <span style={hostNameStyle}>{props.host.host}</span>
        <span style={statusStyle} data-testid="connect-host-status" data-status={props.host.status}>
          {statusLabel(props.host.status)}
        </span>
        <Show when={needsAuth()}>
          <button type="button" onClick={props.onAuth} style={authBtnStyle} data-testid="connect-authenticate">
            Authenticate
          </button>
        </Show>
        <Show when={up()}>
          <LaunchMenu
            apps={props.apps}
            open={props.menuOpen}
            onToggle={props.onToggleMenu}
            onClose={props.onCloseMenu}
            onPick={props.onLaunch}
          />
        </Show>
        <button
          type="button"
          onClick={props.onToggleBookmark}
          style={iconBtnStyle}
          title={props.bookmarked ? 'Remove bookmark' : 'Bookmark this host'}
          data-testid="connect-bookmark-host"
        >
          {props.bookmarked ? '★' : '☆'}
        </button>
        <button type="button" onClick={props.onDisconnect} style={iconBtnStyle} title="Disconnect" data-testid="connect-disconnect">
          ✕
        </button>
      </div>
      <Show when={props.host.error && !needsAuth()}>
        <div style={errorStyle}>{props.host.error}</div>
      </Show>
      <Show when={up()}>
        <MountsSection mounts={props.mounts} onMount={props.onMount} onUnmount={props.onUnmount} />
      </Show>
    </div>
  );
};

// MountsSection lists a host's mounted folders and lets you mount another by
// remote path. The mounted tree appears at ~/wash/remote/<host>/… and shows up
// as a volume in the file manager.
const MountsSection: Component<{
  mounts: MountState[];
  onMount: (remoteRoot: string, persist: boolean) => void;
  onUnmount: (mountPoint: string) => void;
}> = (props) => {
  const [path, setPath] = createSignal('');
  const [persist, setPersist] = createSignal(false);
  const submit = () => {
    const p = path().trim();
    if (!p) return;
    props.onMount(p, persist());
    setPath('');
  };
  const statusText = (mt: MountState) =>
    mt.status === 'mounted' ? '✓ mounted' : mt.status === 'mounting' ? 'mounting…' : (mt.error || 'mount failed');
  return (
    <div style={mountsStyle} data-testid="connect-mounts">
      <For each={props.mounts}>
        {(mt) => (
          <div style={mountRowStyle} data-testid="connect-mount" data-status={mt.status} data-mount={mt.mount_point}>
            <span style={mountPathStyle} title={mt.mount_point}>{mt.remote_root}</span>
            <span style={mountStatusStyle}>{statusText(mt)}</span>
            <button
              type="button"
              onClick={() => props.onUnmount(mt.mount_point)}
              style={iconBtnStyle}
              title="Unmount"
              data-testid="connect-unmount"
            >
              ✕
            </button>
          </div>
        )}
      </For>
      <div style={mountAddStyle}>
        <input
          type="text"
          value={path()}
          onInput={(e) => setPath(e.currentTarget.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') submit(); }}
          placeholder="mount a remote folder (e.g. /home/user)"
          style={mountInputStyle}
          data-testid="connect-mount-input"
        />
        <button type="button" onClick={submit} style={mountBtnStyle} data-testid="connect-mount-submit">
          Mount
        </button>
      </div>
      <label style={mountPersistStyle} title="Re-establish this mount when wash next starts">
        <input
          type="checkbox"
          checked={persist()}
          onChange={(e) => setPersist(e.currentTarget.checked)}
          data-testid="connect-mount-persist"
        />
        Reconnect at launch
      </label>
    </div>
  );
};

// BookmarkRow is a saved host that isn't currently connected. Launch connects
// it; its dropdown opens automatically once the catalog arrives.
const BookmarkRow: Component<{
  bookmark: Bookmark;
  onLaunch: () => void;
  onRemove: () => void;
}> = (props) => (
  <div style={{ ...rowStyle, 'border-color': tokens.borderMenu }} data-testid="connect-bookmark">
    <div style={rowMainStyle}>
      <span style={{ ...dotStyle, background: tokens.fgMuted, opacity: 0.5 }} />
      <span style={hostNameStyle}>{props.bookmark.label || props.bookmark.host}</span>
      <button type="button" onClick={props.onLaunch} style={launchBtnStyle} data-testid="connect-bookmark-launch">
        Launch
      </button>
      <button type="button" onClick={props.onRemove} style={iconBtnStyle} title="Remove bookmark" data-testid="connect-bookmark-remove">
        ✕
      </button>
    </div>
  </div>
);

// CandidateRow is a host auto-discovered on the LAN. Connect dials its addr
// under the current username; the ☆ saves it as a bookmark (addr as target,
// friendly name as label). A "wash" chip marks a peer that announced itself
// as a wash box (vs a generic SSH host a future provider might surface).
const CandidateRow: Component<{
  candidate: Candidate;
  onConnect: () => void;
  onSave: () => void;
}> = (props) => (
  <div style={{ ...rowStyle, 'border-color': tokens.borderMenu }} data-testid={`connect-candidate-${props.candidate.addr}`}>
    <div style={rowMainStyle}>
      <span style={{ ...dotStyle, background: tokens.accentCyan, opacity: 0.6 }} />
      <span style={hostNameStyle} title={props.candidate.addr}>
        {props.candidate.host}
        <span style={candidateAddrStyle}>
          {props.candidate.addr}{props.candidate.port ? `:${props.candidate.port}` : ''}
        </span>
      </span>
      <Show when={props.candidate.wash}>
        <span style={candidateChipStyle} data-testid="connect-candidate-wash">wash</span>
      </Show>
      <button type="button" onClick={props.onConnect} style={launchBtnStyle} data-testid="connect-candidate-connect">
        Connect
      </button>
      <button type="button" onClick={props.onSave} style={iconBtnStyle} title="Save as bookmark" data-testid="connect-candidate-save">
        ☆
      </button>
    </div>
  </div>
);

// LaunchMenu is the Launch button + its app-picker dropdown: a plain vertical
// list of apps (icon + name). A transparent backdrop closes it on outside
// click.
const LaunchMenu: Component<{
  apps: CatalogApp[];
  open: boolean;
  onToggle: () => void;
  onClose: () => void;
  onPick: (appID: string) => void;
}> = (props) => (
  <div style={launchWrapStyle}>
    <button
      type="button"
      onClick={props.onToggle}
      style={launchBtnStyle}
      data-testid="connect-launch"
      aria-haspopup="menu"
      aria-expanded={props.open}
    >
      Launch ▾
    </button>
    <Show when={props.open}>
      <div style={backdropStyle} onClick={props.onClose} data-testid="connect-launch-backdrop" />
      <div style={menuStyle} data-testid="connect-apps" role="menu">
        <Show
          when={props.apps.length > 0}
          fallback={<div style={menuEmptyStyle}>loading apps…</div>}
        >
          <For each={props.apps}>
            {(app) => (
              <button
                type="button"
                style={menuItemStyle}
                onClick={() => props.onPick(app.id)}
                data-testid={`connect-launch-${app.id}`}
                role="menuitem"
                title={app.id}
              >
                <span style={menuIconStyle}>
                  <Show when={app.icon} fallback={<span style={{ opacity: 0.3 }}>·</span>}>
                    <SpriteIcon name={app.icon!} size={16} />
                  </Show>
                </span>
                <span style={menuNameStyle}>{app.name}</span>
              </button>
            )}
          </For>
        </Show>
      </div>
    </Show>
  </div>
);

// AuthOverlay hosts the ssh-add pty terminal (docs/REMOTE.md §6.1). The
// user types their key passphrase here; ssh-add loads it into the agent
// and exits, which the BE reports as auth_closed (driving the retry).
const AuthOverlay: Component<{ host: string; channel: number; onCancel: () => void }> = (props) => (
  <div style={authOverlayStyle} data-testid="connect-auth">
    <div style={authHeaderStyle}>
      <span>Unlock SSH key for <strong>{props.host}</strong> — run <code>ssh-add</code></span>
      <button type="button" onClick={props.onCancel} style={iconBtnStyle} data-testid="connect-auth-cancel" title="Cancel">✕</button>
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
      <SpriteIcon name="monitor" size={24} />
    </div>
    <div style={{ flex: 1, 'min-width': 0 }}>
      <div style={headerTitleStyle}>Connect</div>
      <div style={headerSubtitleStyle}>Run apps on another host, in this desktop</div>
    </div>
  </div>
);

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
  'border-radius': `${tokens.radiusSm}`,
  padding: '7px 10px',
  font: `${tokens.fontSizeBase} ${tokens.fontMono}`,
  'min-width': 0,
};

const launchBtnStyle: JSX.CSSProperties = {
  background: tokens.accentBlue,
  color: '#fff',
  border: 'none',
  'border-radius': `${tokens.radiusSm}`,
  padding: '7px 14px',
  font: `600 ${tokens.fontSizeBase} ${tokens.fontSans}`,
  cursor: 'pointer',
  'white-space': 'nowrap',
  'flex-shrink': 0,
};

const addBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '7px 14px',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  cursor: 'pointer',
  'white-space': 'nowrap',
  'flex-shrink': 0,
};

const mountsStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  gap: '5px',
  'margin-top': '8px',
  'padding-top': '8px',
  'border-top': `1px solid ${tokens.borderMenu}`,
};

const mountRowStyle: JSX.CSSProperties = { display: 'flex', 'align-items': 'center', gap: '8px' };

const mountPathStyle: JSX.CSSProperties = {
  flex: 1,
  color: tokens.fg,
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
  'min-width': 0,
};

const mountStatusStyle: JSX.CSSProperties = {
  color: tokens.fgMuted,
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
  'flex-shrink': 0,
};

const mountAddStyle: JSX.CSSProperties = { display: 'flex', gap: '6px' };

const mountPersistStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '6px',
  color: tokens.fgMuted,
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
  cursor: 'pointer',
};

const mountInputStyle: JSX.CSSProperties = {
  flex: 1,
  background: tokens.bgWindow,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '5px 8px',
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  'min-width': 0,
};

const mountBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '5px 12px',
  font: `600 ${tokens.fontSizeSm} ${tokens.fontSans}`,
  cursor: 'pointer',
  'white-space': 'nowrap',
  'flex-shrink': 0,
};

const emptyStyle: JSX.CSSProperties = {
  padding: '24px 0',
  color: tokens.fgMuted,
  'text-align': 'center',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
};

const sectionLabelStyle: JSX.CSSProperties = {
  font: `600 ${tokens.fontSizeSm} ${tokens.fontSans}`,
  color: tokens.fgMuted,
  'text-transform': 'uppercase',
  'letter-spacing': '0.05em',
  margin: '4px 2px 6px',
};

const listStyle: JSX.CSSProperties = { display: 'flex', 'flex-direction': 'column', gap: '8px', 'margin-bottom': '14px' };

const rowStyle: JSX.CSSProperties = {
  background: tokens.bgMenu,
  border: `1px solid ${tokens.borderMenu}`,
  'border-left-width': '3px',
  'border-radius': `${tokens.radiusSm}`,
  padding: '8px 10px',
  display: 'flex',
  'flex-direction': 'column',
  gap: '6px',
};

const rowMainStyle: JSX.CSSProperties = { display: 'flex', 'align-items': 'center', gap: '8px' };

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

const statusStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
  color: tokens.fgMuted,
  'white-space': 'nowrap',
};

// candidateAddrStyle is the dim IP shown after a discovered host's name.
const candidateAddrStyle: JSX.CSSProperties = {
  'margin-left': '8px',
  color: tokens.fgMuted,
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  'font-weight': 400,
};

// candidateChipStyle is the small "wash" badge on a discovered wash peer.
const candidateChipStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.accentCyan,
  border: `1px solid ${tokens.accentCyan}`,
  'border-radius': `${tokens.radiusSm}`,
  font: `600 ${tokens.fontSizeSm} ${tokens.fontSans}`,
  padding: '1px 6px',
  'flex-shrink': 0,
};

const iconBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.fgMuted,
  border: 'none',
  cursor: 'pointer',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  padding: '0 4px',
  'line-height': 1,
  'flex-shrink': 0,
};

const authBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.accentAmber,
  border: `1px solid ${tokens.accentAmber}`,
  'border-radius': `${tokens.radiusSm}`,
  cursor: 'pointer',
  font: `600 ${tokens.fontSizeSm} ${tokens.fontSans}`,
  padding: '3px 8px',
  'white-space': 'nowrap',
  'flex-shrink': 0,
};

const launchWrapStyle: JSX.CSSProperties = { position: 'relative', 'flex-shrink': 0 };

const backdropStyle: JSX.CSSProperties = {
  position: 'fixed',
  inset: '0',
  'z-index': 40,
};

const menuStyle: JSX.CSSProperties = {
  position: 'absolute',
  top: 'calc(100% + 4px)',
  right: '0',
  'z-index': 41,
  'min-width': '180px',
  'max-height': '260px',
  overflow: 'auto',
  background: tokens.bgMenu,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  'box-shadow': '0 8px 24px rgba(0,0,0,0.45)',
  padding: '4px',
  display: 'flex',
  'flex-direction': 'column',
  gap: '1px',
};

const menuItemStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '8px',
  padding: '6px 8px',
  background: 'transparent',
  color: tokens.fg,
  border: 'none',
  'border-radius': `${tokens.radiusSm}`,
  cursor: 'pointer',
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
  'text-align': 'left',
  width: '100%',
};

const menuIconStyle: JSX.CSSProperties = {
  width: '16px',
  height: '16px',
  display: 'flex',
  'align-items': 'center',
  'justify-content': 'center',
  flex: '0 0 auto',
  opacity: 0.85,
};

const menuNameStyle: JSX.CSSProperties = {
  flex: 1,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

const menuEmptyStyle: JSX.CSSProperties = {
  padding: '8px',
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
  color: tokens.fgMuted,
  opacity: 0.7,
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

defineWashApp('wash-app-connect', (props) => <App {...props} />, {
  style: `display:block;width:100%;height:100%;overflow:hidden;background:${tokens.bgWindow};color:${tokens.fg};font:${tokens.fontSizeBase} ${tokens.fontSans};box-sizing:border-box`,
});
