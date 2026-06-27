// wash-app-connect: the windowed front-end for remote hosts
// (docs/REMOTE.md R2, §6.1).
//
// Mental model — TWO verbs, never overloaded:
//   • Connect — establish the SSH session to a host (top bar, a saved host,
//     or a discovered one). One word everywhere.
//   • Open    — open an app on an already-connected host (the app buttons).
//
// Flow: Connect a host → the BE relays to the com.wash.remote supervisor,
// which SSHes out and reports per-host status + a local endpoint over
// {kind:"remote.state"}. When a host reaches "up" the FE attaches it as a
// second RouterClient (window.wash.attachRemote) so its windows composite
// into this desktop; its catalog then arrives (window.wash.catalogFor) and
// its apps appear on the host card — Terminal / Files / Editor up front, the
// rest behind "More". Mounting a remote folder and the per-host preferences
// drawer (favourite ★, auto-open apps, auto-mount folders) live on the card.
//
// Three sections, each a clear host state: Connected (top, bordered in the
// host's window-hint colour) · Saved (favourites not currently connected) ·
// On your network (LAN auto-discovery). A machine appears in exactly one.

import { For, Show, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { createAppBus, defineWashApp, tokens, Terminal, Button, Input, Checkbox } from '@wash/ui';

// ----- wire types -----

type HostStatus = 'starting' | 'up' | 'reconnecting' | 'down';

interface HostState {
  host: string;
  origin: string;
  status: HostStatus;
  error?: string;
  // code classifies a "down" status. "auth" means SSH refused auth under
  // BatchMode — the cue to offer the ssh-copy-id widget (docs/REMOTE.md §6.1).
  code?: string;
}

type MountStatus = 'mounting' | 'mounted' | 'error';

// MountState is one remote folder mounted locally over SFTP, surfaced under
// its host. mount_point is the local path (~/wash/remote/<host>/<base>).
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

// Bookmark is a saved connect target plus per-host preferences. auto_apps /
// auto_mounts are replayed the moment the host reaches "up". app_id is the
// legacy single-app field, migrated into auto_apps on load.
interface Bookmark {
  host: string;
  label?: string;
  auto_apps?: string[];
  auto_mounts?: string[];
  app_id?: string;
}

type CatalogApp = ReturnType<typeof window.wash.catalogFor>[number];

// ----- app priority -----
//
// A connected host can expose a long, mixed catalog; most of it (background
// services, the session, panels) isn't something you'd open as a window
// here. So the card shows the three apps people reach for first, in this
// order, and tucks everything else behind "More". (User decision.)
const PRIMARY_APPS = ['com.wash.term', 'com.wash.fm', 'com.wash.edit'];

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
  // Interactive SSH auth (mechanism a): when the BE opens an ssh-copy-id pty it
  // sends the raw channel id; we mount a Terminal on it. auth tracks the
  // host being authenticated + the channel; null = no auth in flight.
  const [auth, setAuth] = createSignal<{ host: string; channel: number } | null>(null);
  const [bookmarks, setBookmarks] = createSignal<Bookmark[]>([]);
  // moreFor holds the origin whose "More" app dropdown is open (one at a time).
  const [moreFor, setMoreFor] = createSignal<string | null>(null);
  // drawerFor holds the host key whose preferences drawer is open.
  const [drawerFor, setDrawerFor] = createSignal<string | null>(null);

  // attached tracks origins we've called attachRemote for, so a repeated
  // "up" push (e.g. on reconnect) doesn't open a duplicate connection and a
  // vanished host gets detached exactly once.
  const attached = new Set<string>();
  // autoDone tracks origins whose saved auto-apps/auto-mounts were already
  // replayed this session, so a re-pushed "up" doesn't relaunch them.
  const autoDone = new Set<string>();

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
        autoDone.delete(origin);
        setCatalogs((c) => { const n = { ...c }; delete n[origin]; return n; });
        if (moreFor() === origin) setMoreFor(null);
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
        replayAuto(list);
        break;
      }
      case 'auth_opened':
        // The ssh-copy-id pty is live on m.channel_id — mount its terminal.
        setAuth({ host: String(m.host ?? ''), channel: Number(m.channel_id) });
        break;
      case 'auth_closed': {
        // ssh-copy-id exited (key installed, or the user gave up). Tear the terminal
        // down and retry the connect — if the key loaded, the BatchMode
        // connect now succeeds; if not, we land back on "auth".
        const a = auth();
        setAuth(null);
        if (a?.host) send({ kind: 'connect', host: a.host });
        break;
      }
      case 'auth_error':
        setAuth(null);
        break;
      case 'bookmarks':
        setBookmarks(migrateBookmarks(Array.isArray(m.bookmarks) ? (m.bookmarks as Bookmark[]) : []));
        break;
    }
  };

  const beginAuth = (host: string) => send({ kind: 'auth_begin', host });
  const cancelAuth = () => { send({ kind: 'auth_cancel' }); setAuth(null); };

  const { send } = createAppBus(props, { onMsg: handleBE });

  // ----- bookmarks + per-host preferences -----
  const persistBookmarks = (next: Bookmark[]) => {
    setBookmarks(next);
    send({ kind: 'bookmarks_save', bookmarks: next });
  };
  const bookmarkFor = (host: string) => bookmarks().find((b) => b.host === host);
  const isBookmarked = (host: string) => bookmarks().some((b) => b.host === host);
  // upsertBookmark merges a patch into the bookmark for host, creating it if
  // absent — so editing any preference (label, auto-app, auto-mount) on an
  // unsaved host implicitly saves it.
  const upsertBookmark = (host: string, patch: Partial<Bookmark>) => {
    if (bookmarkFor(host)) {
      persistBookmarks(bookmarks().map((b) => (b.host === host ? { ...b, ...patch } : b)));
    } else {
      persistBookmarks([...bookmarks(), { host, label: host, ...patch }]);
    }
  };
  const addBookmark = (host: string, label?: string) => {
    if (!host || isBookmarked(host)) return;
    persistBookmarks([...bookmarks(), { host, label: label || host }]);
  };
  const removeBookmark = (host: string) => persistBookmarks(bookmarks().filter((b) => b.host !== host));
  const toggleBookmark = (host: string, label?: string) =>
    isBookmarked(host) ? removeBookmark(host) : addBookmark(host, label);

  const toggleAutoApp = (host: string, appID: string) => {
    const cur = bookmarkFor(host)?.auto_apps ?? [];
    const next = cur.includes(appID) ? cur.filter((a) => a !== appID) : [...cur, appID];
    upsertBookmark(host, { auto_apps: next });
  };
  const removeAutoApp = (host: string, appID: string) => {
    const cur = bookmarkFor(host)?.auto_apps ?? [];
    upsertBookmark(host, { auto_apps: cur.filter((a) => a !== appID) });
  };
  const addAutoMount = (host: string, path: string) => {
    const p = path.trim();
    if (!p) return;
    const cur = bookmarkFor(host)?.auto_mounts ?? [];
    if (cur.includes(p)) return;
    upsertBookmark(host, { auto_mounts: [...cur, p] });
  };
  const removeAutoMount = (host: string, path: string) => {
    const cur = bookmarkFor(host)?.auto_mounts ?? [];
    upsertBookmark(host, { auto_mounts: cur.filter((p) => p !== path) });
  };

  // replayAuto fires a saved host's auto-open apps + auto-mount folders once
  // it reaches "up" (and, for apps, once its catalog has arrived so the ids
  // resolve). Guarded by autoDone so a re-pushed "up" doesn't repeat it.
  const replayAuto = (list: HostState[]) => {
    for (const h of list) {
      if (h.status !== 'up' || autoDone.has(h.origin)) continue;
      const bm = bookmarkFor(h.host);
      if (!bm) continue;
      const apps = bm.auto_apps ?? [];
      const cat = catalogs()[h.origin];
      // Wait for the catalog before launching apps; mounts need no catalog.
      if (apps.length > 0 && (!cat || cat.length === 0)) continue;
      for (const appID of apps) {
        if (cat?.some((a) => a.id === appID)) window.wash.launchOn(h.origin, appID);
      }
      const mounted = new Set(mounts().filter((mt) => mt.host === h.host).map((mt) => mt.remote_root));
      for (const root of bm.auto_mounts ?? []) {
        if (!mounted.has(root)) send({ kind: 'mount', host: h.host, remote_root: root, persist: false });
      }
      autoDone.add(h.origin);
    }
  };

  // connectHost establishes the session (the host appears under Connected
  // when up). host is the dial target — pass a NAME so the supervisor lets
  // ssh read ~/.ssh/config; addr is an optional IP fallback for when the name
  // doesn't resolve. remote_port is sent only for a non-default port. 0 ⇒ 22.
  const connectHost = (host: string, port?: number, addr?: string) => {
    if (!host) return;
    send({ kind: 'connect', host, addr: addr || '', remote_port: port || 0 });
  };

  onMount(() => {
    // Subscribe to the supervisor's host state (relayed via our BE) and load
    // saved bookmarks. A freshly-arrived catalog may unblock a pending
    // auto-launch, so re-run replayAuto when one lands.
    send({ kind: 'subscribe' });
    send({ kind: 'bookmarks_load' });
    setCatalogs({});
    const offCatalog = window.wash.onRemoteCatalog((ev) => {
      setCatalogs((c) => ({ ...c, [ev.origin]: ev.apps }));
      replayAuto(hosts());
    });
    onCleanup(() => {
      send({ kind: 'unsubscribe' });
      offCatalog();
    });
  });

  // connectFromInput parses "user@host" / "host:port" / "user@host:port" and
  // connects. (Port can be given inline; a bare host defaults to 22.)
  const connectFromInput = () => {
    const raw = hostInput().trim();
    if (!raw) return;
    const { target, port } = parseTarget(raw);
    connectHost(target, port);
    setHostInput('');
  };
  const saveFromInput = () => {
    const raw = hostInput().trim();
    if (!raw) return;
    addBookmark(parseTarget(raw).target);
    setHostInput('');
  };

  const disconnect = (host: string) => send({ kind: 'disconnect', host });
  const launch = (origin: string, appID: string) => {
    window.wash.launchOn(origin, appID);
    setMoreFor(null);
  };

  // launchable filters a host's catalog to window-surface, enabled apps —
  // the ones it makes sense to open as a window.
  const launchable = (origin: string): CatalogApp[] =>
    (catalogs()[origin] ?? []).filter((a) => a.surface === 'window' && !a.disabled);

  // Connected vs Disconnected: a host the supervisor still tracks but that has
  // dropped to "down" moves to its own section, so the Connected list only
  // ever shows live (coloured-dot) hosts — a grey dot under "Connected" read
  // as "is this actually connected?". starting/reconnecting stay under
  // Connected (transient, still attached/recovering).
  const upHosts = () => hosts().filter((h) => h.status !== 'down');
  const downHosts = () => hosts().filter((h) => h.status === 'down');

  // The Saved section: bookmarks that aren't currently connected.
  const connectedHostNames = () => new Set(hosts().map((h) => h.host));
  const bookmarkedOnly = () => {
    const live = connectedHostNames();
    return bookmarks().filter((b) => !live.has(b.host));
  };

  // Discovered hosts not already connected or saved — so a candidate
  // disappears from this section the moment you act on it. We now connect/save
  // candidates by NAME, but a host could equally be tracked by its IP (a
  // manual connect, or the pre-name behaviour), so a candidate is "known" if
  // EITHER its name or its addr matches a connected/origin/saved entry.
  const candidateOnly = () => {
    const live = connectedHostNames();
    const origins = new Set(hosts().map((h) => h.origin));
    const saved = new Set(bookmarks().map((b) => b.host));
    const known = (s: string) => !!s && (live.has(s) || origins.has(s) || saved.has(s));
    return candidates().filter((c) => !known(c.host) && !known(c.addr));
  };

  const empty = () => hosts().length === 0 && bookmarkedOnly().length === 0 && candidateOnly().length === 0;

  // hostCard renders one supervisor-tracked host (used by both the Connected
  // and Disconnected sections — HostRow itself adapts to the status).
  const hostCard = (h: HostState) => (
    <HostRow
      host={h}
      apps={launchable(h.origin)}
      mounts={mounts().filter((mt) => mt.host === h.host)}
      bookmark={bookmarkFor(h.host)}
      moreOpen={moreFor() === h.origin}
      drawerOpen={drawerFor() === h.host}
      onToggleMore={() => setMoreFor(moreFor() === h.origin ? null : h.origin)}
      onCloseMore={() => setMoreFor(null)}
      onToggleDrawer={() => setDrawerFor(drawerFor() === h.host ? null : h.host)}
      onLaunch={(appID) => launch(h.origin, appID)}
      onDisconnect={() => disconnect(h.host)}
      onReconnect={() => connectHost(h.host)}
      onAuth={() => beginAuth(h.host)}
      onToggleBookmark={() => toggleBookmark(h.host)}
      onSetLabel={(label) => upsertBookmark(h.host, { label })}
      onToggleAutoApp={(appID) => toggleAutoApp(h.host, appID)}
      onAddAutoMount={(p) => addAutoMount(h.host, p)}
      onRemoveAutoMount={(p) => removeAutoMount(h.host, p)}
      onMount={(root, persist) => send({ kind: 'mount', host: h.host, remote_root: root, persist })}
      onUnmount={(mp) => send({ kind: 'unmount', mount_point: mp })}
    />
  );

  return (
    <div style={shellStyle}>
      <Header />
      <div style={bodyStyle}>
        <div style={connectRowStyle}>
          <Input
            type="text"
            placeholder="user@host"
            value={hostInput()}
            onInput={(e) => setHostInput(e.currentTarget.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') connectFromInput(); }}
            style={{ flex: 1, 'min-width': 0, font: tokens.type.monoMd }}
            data-testid="connect-host-input"
          />
          <Button onClick={connectFromInput} style={{ 'white-space': 'nowrap', 'flex-shrink': 0 }} data-testid="connect-submit">
            Connect
          </Button>
          <Button
            variant="icon"
            onClick={saveFromInput}
            style={{ color: tokens.fgMuted }}
            title="Save without connecting"
            data-testid="connect-add"
          >
            ☆
          </Button>
        </div>

        <Show
          when={!empty()}
          fallback={<div style={emptyStyle}>Enter a host above and Connect. Saved hosts and machines found on your network appear here.</div>}
        >
          {/* Connected hosts — bordered in the host's window-hint colour. */}
          <Show when={upHosts().length > 0}>
            <div style={sectionLabelStyle}>Connected</div>
            <div style={listStyle} data-testid="connect-hosts">
              <For each={upHosts()}>{(h) => hostCard(h)}</For>
            </div>
          </Show>

          {/* Hosts the supervisor still tracks but that have dropped — kept
              here (not in Connected) so the grey dot reads as "offline", and
              still one Reconnect/Authenticate click away. */}
          <Show when={downHosts().length > 0}>
            <div style={sectionLabelStyle}>Disconnected</div>
            <div style={listStyle} data-testid="connect-hosts-down">
              <For each={downHosts()}>{(h) => hostCard(h)}</For>
            </div>
          </Show>

          {/* Saved but not connected. */}
          <Show when={bookmarkedOnly().length > 0}>
            <div style={sectionLabelStyle}>Saved</div>
            <div style={listStyle} data-testid="connect-bookmarks">
              <For each={bookmarkedOnly()}>
                {(bm) => (
                  <BookmarkRow
                    bookmark={bm}
                    drawerOpen={drawerFor() === bm.host}
                    onToggleDrawer={() => setDrawerFor(drawerFor() === bm.host ? null : bm.host)}
                    onConnect={() => connectHost(bm.host)}
                    onRemove={() => removeBookmark(bm.host)}
                    onSetLabel={(label) => upsertBookmark(bm.host, { label })}
                    onRemoveAutoApp={(appID) => removeAutoApp(bm.host, appID)}
                    onAddAutoMount={(p) => addAutoMount(bm.host, p)}
                    onRemoveAutoMount={(p) => removeAutoMount(bm.host, p)}
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
                    onConnect={() => connectHost(c.host || c.addr, c.port, c.addr)}
                    onSave={() => addBookmark(c.host || c.addr, c.host || c.addr)}
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

// ----- helpers -----

// parseTarget splits "user@host:port" into the SSH target ("user@host") and
// an optional numeric port. A bare host returns port undefined (⇒ 22).
function parseTarget(raw: string): { target: string; port?: number } {
  const at = raw.lastIndexOf('@');
  const userPart = at >= 0 ? raw.slice(0, at + 1) : '';
  const hostPart = at >= 0 ? raw.slice(at + 1) : raw;
  const colon = hostPart.lastIndexOf(':');
  if (colon > 0) {
    const p = Number(hostPart.slice(colon + 1));
    if (Number.isInteger(p) && p > 0) {
      return { target: userPart + hostPart.slice(0, colon), port: p };
    }
  }
  return { target: raw };
}

// migrateBookmarks folds the legacy single app_id into auto_apps so old
// connect.json files keep working under the new per-host preferences.
function migrateBookmarks(bms: Bookmark[]): Bookmark[] {
  return bms.map((b) => {
    if (b.app_id && (!b.auto_apps || b.auto_apps.length === 0)) {
      return { ...b, auto_apps: [b.app_id], app_id: undefined };
    }
    return b;
  });
}

// ----- host card (connected) -----

// HostRow is one connected (or connecting) host. Its border carries the
// host's window-hint colour once up. Terminal / Files / Editor are direct
// "Open" buttons; everything else hides behind "More". The ⚙ drawer holds
// favourite + auto-open + auto-mount preferences.
const HostRow: Component<{
  host: HostState;
  apps: CatalogApp[];
  mounts: MountState[];
  bookmark?: Bookmark;
  moreOpen: boolean;
  drawerOpen: boolean;
  onToggleMore: () => void;
  onCloseMore: () => void;
  onToggleDrawer: () => void;
  onLaunch: (appID: string) => void;
  onDisconnect: () => void;
  onReconnect: () => void;
  onAuth: () => void;
  onToggleBookmark: () => void;
  onSetLabel: (label: string) => void;
  onToggleAutoApp: (appID: string) => void;
  onAddAutoMount: (path: string) => void;
  onRemoveAutoMount: (path: string) => void;
  onMount: (remoteRoot: string, persist: boolean) => void;
  onUnmount: (mountPoint: string) => void;
}> = (props) => {
  const color = () => hostColor(props.host.origin);
  const up = () => props.host.status === 'up';
  const down = () => props.host.status === 'down';
  const needsAuth = () => down() && props.host.code === 'auth';

  // Split the catalog into the priority three (in PRIMARY_APPS order) and the
  // rest (alphabetical, behind More).
  const primary = () =>
    PRIMARY_APPS.map((id) => props.apps.find((a) => a.id === id)).filter(Boolean) as CatalogApp[];
  const more = () =>
    props.apps.filter((a) => !PRIMARY_APPS.includes(a.id)).sort((a, b) => a.name.localeCompare(b.name));

  return (
    <div
      style={{ ...rowStyle, 'border-color': up() ? color() : tokens.borderMenu }}
      data-testid={`connect-host-${props.host.origin}`}
    >
      <div style={rowMainStyle}>
        <span style={{ ...dotStyle, background: up() ? color() : tokens.fgMuted }} data-testid="connect-host-dot" />
        <span style={hostNameStyle}>{props.bookmark?.label || props.host.host}</span>
        <span style={statusStyle} data-testid="connect-host-status" data-status={props.host.status}>
          {statusLabel(props.host.status)}
        </span>
        <Show when={needsAuth()}>
          <Button variant="ghost" size="sm" onClick={props.onAuth} style={{ 'white-space': 'nowrap', 'flex-shrink': 0 }} data-testid="connect-authenticate">
            Authenticate
          </Button>
        </Show>
        <Show when={down() && !needsAuth()}>
          <Button variant="ghost" size="sm" onClick={props.onReconnect} style={{ 'white-space': 'nowrap', 'flex-shrink': 0 }} data-testid="connect-reconnect">
            Reconnect
          </Button>
        </Show>
        <Button
          variant="icon"
          onClick={props.onToggleDrawer}
          style={{ color: props.drawerOpen ? tokens.fg : tokens.fgMuted }}
          title="Host settings"
          data-testid="connect-host-settings"
          aria-expanded={props.drawerOpen}
        >
          ⚙
        </Button>
        <Button variant="icon" onClick={props.onDisconnect} style={{ color: tokens.fgMuted }} title="Disconnect" data-testid="connect-disconnect">
          ✕
        </Button>
      </div>

      {/* Apps row — the payoff of connecting. Terminal/Files/Editor up front. */}
      <Show when={up()}>
        <div style={appsRowStyle}>
          <For each={primary()}>
            {(app) => (
              <Button
                variant="ghost"
                style={{ display: 'flex', 'align-items': 'center', gap: '6px', 'white-space': 'nowrap' }}
                onClick={() => props.onLaunch(app.id)}
                data-testid={`connect-launch-${app.id}`}
                title={`Open ${app.name}`}
              >
                <AppIcon app={app} size={15} />
                <span style={appBtnLabelStyle}>{app.name}</span>
              </Button>
            )}
          </For>
          <Show when={primary().length === 0 && more().length === 0}>
            <span style={appsEmptyStyle} data-testid="connect-apps-empty">no apps</span>
          </Show>
          <Show when={more().length > 0}>
            <MoreMenu
              apps={more()}
              open={props.moreOpen}
              onToggle={props.onToggleMore}
              onClose={props.onCloseMore}
              onPick={props.onLaunch}
            />
          </Show>
        </div>
      </Show>

      <Show when={props.host.error && !needsAuth()}>
        <div style={errorStyle}>{props.host.error}</div>
      </Show>

      <Show when={up()}>
        <MountsSection mounts={props.mounts} onMount={props.onMount} onUnmount={props.onUnmount} />
      </Show>

      <Show when={props.drawerOpen}>
        <HostDrawer
          bookmarked={!!props.bookmark}
          bookmark={props.bookmark}
          apps={props.apps}
          connected={up()}
          onToggleBookmark={props.onToggleBookmark}
          onSetLabel={props.onSetLabel}
          onToggleAutoApp={props.onToggleAutoApp}
          onAddAutoMount={props.onAddAutoMount}
          onRemoveAutoMount={props.onRemoveAutoMount}
        />
      </Show>
    </div>
  );
};

// HostDrawer is the per-host preferences editor: favourite, label, and the
// "do this automatically when I connect" set (open apps + mount folders).
// Auto-app checkboxes need the live catalog, so they show only when
// connected. Editing any field implicitly saves the host.
const HostDrawer: Component<{
  bookmarked: boolean;
  bookmark?: Bookmark;
  apps: CatalogApp[];
  connected: boolean;
  onToggleBookmark: () => void;
  onSetLabel: (label: string) => void;
  onToggleAutoApp: (appID: string) => void;
  onAddAutoMount: (path: string) => void;
  onRemoveAutoMount: (path: string) => void;
}> = (props) => {
  const [mountPath, setMountPath] = createSignal('');
  const autoApps = () => props.bookmark?.auto_apps ?? [];
  const autoMounts = () => props.bookmark?.auto_mounts ?? [];
  const submitMount = () => { props.onAddAutoMount(mountPath()); setMountPath(''); };
  return (
    <div style={drawerStyle} data-testid="connect-drawer">
      <div style={drawerRowStyle}>
        <Button
          variant="icon"
          onClick={props.onToggleBookmark}
          style={{ color: props.bookmarked ? tokens.accentAmber : tokens.fgMuted, 'font-size': '15px' }}
          title={props.bookmarked ? 'Forget this host' : 'Save this host'}
          data-testid="connect-bookmark-host"
        >
          {props.bookmarked ? '★' : '☆'}
        </Button>
        <Input
          type="text"
          value={props.bookmark?.label ?? ''}
          placeholder="label"
          onInput={(e) => props.onSetLabel(e.currentTarget.value)}
          style={{ flex: 1, 'min-width': 0 }}
          data-testid="connect-drawer-label"
        />
      </div>

      <div style={drawerSectionStyle}>
        <div style={drawerHeadStyle}>Open automatically on connect</div>
        <Show when={props.connected} fallback={<div style={drawerHintStyle}>Connect to choose apps.</div>}>
          <div style={drawerAppsStyle}>
            <For each={props.apps.filter((a) => a.surface === 'window' && !a.disabled)}>
              {(app) => (
                <Checkbox
                  checked={autoApps().includes(app.id)}
                  onChange={() => props.onToggleAutoApp(app.id)}
                  data-testid={`connect-auto-app-${app.id}`}
                  label={<><AppIcon app={app} size={14} /><span>{app.name}</span></>}
                />
              )}
            </For>
          </div>
        </Show>
      </div>

      <div style={drawerSectionStyle}>
        <div style={drawerHeadStyle}>Mount automatically on connect</div>
        <For each={autoMounts()}>
          {(p) => (
            <div style={drawerChipRowStyle} data-testid="connect-auto-mount">
              <span style={drawerChipPathStyle} title={p}>{p}</span>
              <Button variant="icon" onClick={() => props.onRemoveAutoMount(p)} style={{ color: tokens.fgMuted }} title="Remove">✕</Button>
            </div>
          )}
        </For>
        <div style={mountAddStyle}>
          <Input
            type="text"
            value={mountPath()}
            onInput={(e) => setMountPath(e.currentTarget.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') submitMount(); }}
            placeholder="remote folder (e.g. /home/user)"
            style={{ flex: 1, 'min-width': 0, font: tokens.type.monoSm }}
            data-testid="connect-auto-mount-input"
          />
          <Button variant="ghost" onClick={submitMount} style={{ 'white-space': 'nowrap', 'flex-shrink': 0 }} data-testid="connect-auto-mount-add">Add</Button>
        </div>
      </div>
    </div>
  );
};

// MountsSection lists a host's live mounted folders and lets you mount
// another by remote path. The mounted tree appears at ~/wash/remote/<host>/…
// and shows up as a volume in the file manager.
const MountsSection: Component<{
  mounts: MountState[];
  onMount: (remoteRoot: string, persist: boolean) => void;
  onUnmount: (mountPoint: string) => void;
}> = (props) => {
  const [path, setPath] = createSignal('');
  const submit = () => {
    const p = path().trim();
    if (!p) return;
    props.onMount(p, false);
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
            <Button
              variant="icon"
              onClick={() => props.onUnmount(mt.mount_point)}
              style={{ color: tokens.fgMuted }}
              title="Unmount"
              data-testid="connect-unmount"
            >
              ✕
            </Button>
          </div>
        )}
      </For>
      <div style={mountAddStyle}>
        <Input
          type="text"
          value={path()}
          onInput={(e) => setPath(e.currentTarget.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') submit(); }}
          placeholder="mount a remote folder (e.g. /home/user)"
          style={{ flex: 1, 'min-width': 0, font: tokens.type.monoSm }}
          data-testid="connect-mount-input"
        />
        <Button variant="ghost" onClick={submit} style={{ 'white-space': 'nowrap', 'flex-shrink': 0 }} data-testid="connect-mount-submit">
          Mount
        </Button>
      </div>
    </div>
  );
};

// BookmarkRow is a saved host that isn't currently connected. Connect brings
// it up; auto-apps/mounts then replay. The ⚙ drawer edits its preferences;
// saved auto-apps show as removable chips (no catalog offline).
const BookmarkRow: Component<{
  bookmark: Bookmark;
  drawerOpen: boolean;
  onToggleDrawer: () => void;
  onConnect: () => void;
  onRemove: () => void;
  onSetLabel: (label: string) => void;
  onRemoveAutoApp: (appID: string) => void;
  onAddAutoMount: (path: string) => void;
  onRemoveAutoMount: (path: string) => void;
}> = (props) => {
  const [mountPath, setMountPath] = createSignal('');
  const autoApps = () => props.bookmark.auto_apps ?? [];
  const autoMounts = () => props.bookmark.auto_mounts ?? [];
  const submitMount = () => { props.onAddAutoMount(mountPath()); setMountPath(''); };
  return (
    <div style={{ ...rowStyle, 'border-color': tokens.borderMenu }} data-testid="connect-bookmark">
      <div style={rowMainStyle}>
        <span style={{ ...dotStyle, background: tokens.fgMuted, opacity: 0.5 }} />
        <span style={hostNameStyle}>{props.bookmark.label || props.bookmark.host}</span>
        <Show when={autoApps().length > 0}>
          <span style={autoBadgeStyle} title={`Auto-opens ${autoApps().length} app(s)`}>auto</span>
        </Show>
        <Button onClick={props.onConnect} style={{ 'white-space': 'nowrap', 'flex-shrink': 0 }} data-testid="connect-bookmark-launch">
          Connect
        </Button>
        <Button
          variant="icon"
          onClick={props.onToggleDrawer}
          style={{ color: props.drawerOpen ? tokens.fg : tokens.fgMuted }}
          title="Host settings"
          aria-expanded={props.drawerOpen}
        >
          ⚙
        </Button>
        <Button variant="icon" onClick={props.onRemove} style={{ color: tokens.fgMuted }} title="Remove bookmark" data-testid="connect-bookmark-remove">
          ✕
        </Button>
      </div>
      <Show when={props.drawerOpen}>
        <div style={drawerStyle} data-testid="connect-drawer">
          <Input
            type="text"
            value={props.bookmark.label ?? ''}
            placeholder="label"
            onInput={(e) => props.onSetLabel(e.currentTarget.value)}
            style={{ flex: 1, 'min-width': 0 }}
            data-testid="connect-drawer-label"
          />
          <div style={drawerSectionStyle}>
            <div style={drawerHeadStyle}>Opens automatically on connect</div>
            <Show when={autoApps().length > 0} fallback={<div style={drawerHintStyle}>Connect, then ⚙ to choose apps.</div>}>
              <div style={drawerAppsStyle}>
                <For each={autoApps()}>
                  {(id) => (
                    <span style={drawerChipRowStyle} data-testid="connect-auto-app-chip">
                      <span style={drawerChipPathStyle}>{id.replace('com.wash.', '')}</span>
                      <Button variant="icon" onClick={() => props.onRemoveAutoApp(id)} style={{ color: tokens.fgMuted }} title="Remove">✕</Button>
                    </span>
                  )}
                </For>
              </div>
            </Show>
          </div>
          <div style={drawerSectionStyle}>
            <div style={drawerHeadStyle}>Mount automatically on connect</div>
            <For each={autoMounts()}>
              {(p) => (
                <div style={drawerChipRowStyle} data-testid="connect-auto-mount">
                  <span style={drawerChipPathStyle} title={p}>{p}</span>
                  <Button variant="icon" onClick={() => props.onRemoveAutoMount(p)} style={{ color: tokens.fgMuted }} title="Remove">✕</Button>
                </div>
              )}
            </For>
            <div style={mountAddStyle}>
              <Input
                type="text"
                value={mountPath()}
                onInput={(e) => setMountPath(e.currentTarget.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') submitMount(); }}
                placeholder="remote folder (e.g. /home/user)"
                style={{ flex: 1, 'min-width': 0, font: tokens.type.monoSm }}
                data-testid="connect-auto-mount-input"
              />
              <Button variant="ghost" onClick={submitMount} style={{ 'white-space': 'nowrap', 'flex-shrink': 0 }} data-testid="connect-auto-mount-add">Add</Button>
            </div>
          </div>
        </div>
      </Show>
    </div>
  );
};

// CandidateRow is a host auto-discovered on the LAN. Connect dials its addr
// under the current username; ☆ saves it as a bookmark (addr as target,
// friendly name as label). A "wash" chip marks a peer that announced itself
// as a wash box.
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
      <Button size="sm" onClick={props.onConnect} style={{ 'white-space': 'nowrap', 'flex-shrink': 0 }} data-testid="connect-candidate-connect">
        Connect
      </Button>
      <Button variant="icon" onClick={props.onSave} style={{ color: tokens.fgMuted }} title="Save as bookmark" data-testid="connect-candidate-save">
        ☆
      </Button>
    </div>
  </div>
);

// MoreMenu is the "More ▾" button + its app-picker dropdown for the non-
// priority apps. A transparent backdrop closes it on outside click.
const MoreMenu: Component<{
  apps: CatalogApp[];
  open: boolean;
  onToggle: () => void;
  onClose: () => void;
  onPick: (appID: string) => void;
}> = (props) => (
  <div style={launchWrapStyle}>
    <Button
      variant="ghost"
      onClick={props.onToggle}
      style={{ 'white-space': 'nowrap' }}
      data-testid="connect-launch"
      aria-haspopup="menu"
      aria-expanded={props.open}
    >
      More ▾
    </Button>
    <Show when={props.open}>
      <div style={backdropStyle} onClick={props.onClose} data-testid="connect-launch-backdrop" />
      <div style={menuStyle} data-testid="connect-apps" role="menu">
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
              <span style={menuIconStyle}><AppIcon app={app} size={16} /></span>
              <span style={menuNameStyle}>{app.name}</span>
            </button>
          )}
        </For>
      </div>
    </Show>
  </div>
);

// AppIcon renders an app's sprite icon, or a faint dot if it has none.
const AppIcon: Component<{ app: CatalogApp; size: number }> = (props) => (
  <Show when={props.app.icon} fallback={<span style={{ opacity: 0.3 }}>·</span>}>
    <SpriteIcon name={props.app.icon!} size={props.size} />
  </Show>
);

// AuthOverlay hosts the ssh-copy-id pty terminal (docs/REMOTE.md §6.1). The
// user enters the host's password here; ssh-copy-id installs their key and exits,
// which the BE reports as auth_closed (driving the retry).
const AuthOverlay: Component<{ host: string; channel: number; onCancel: () => void }> = (props) => (
  <div style={authOverlayStyle} data-testid="connect-auth">
    <div style={authHeaderStyle}>
      <span>Authorize this machine on <strong>{props.host}</strong> — enter its password</span>
      <Button variant="icon" onClick={props.onCancel} style={{ color: tokens.fgMuted }} data-testid="connect-auth-cancel" title="Cancel">✕</Button>
    </div>
    <div style={authTermStyle}>
      <Terminal channelId={props.channel} initialCols={80} initialRows={24} contextMenu={false} />
    </div>
    <div style={authHintStyle}>Enter the host's password above (once). Your SSH key is installed there and the connection retries automatically.</div>
  </div>
);

const Header: Component = () => (
  <div style={headerStyle}>
    <div style={headerIconStyle}>
      <SpriteIcon name="monitor" size={24} />
    </div>
    <div style={{ flex: 1, 'min-width': 0 }}>
      <div style={headerTitleStyle}>Connect</div>
      <div style={headerSubtitleStyle}>Run apps and open folders on another machine</div>
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
  font: tokens.type.textMd,
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

const headerTitleStyle: JSX.CSSProperties = { font: tokens.type.titleLg, 'line-height': 1.1 };
const headerSubtitleStyle: JSX.CSSProperties = {
  font: tokens.type.textMd,
  color: tokens.fgMuted,
  'margin-top': '2px',
};

const bodyStyle: JSX.CSSProperties = { overflow: 'auto', padding: '14px 18px 20px' };

const connectRowStyle: JSX.CSSProperties = { display: 'flex', 'align-items': 'center', gap: '8px', 'margin-bottom': '16px' };

const appsRowStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  'flex-wrap': 'wrap',
  gap: '6px',
};

const appBtnLabelStyle: JSX.CSSProperties = { 'line-height': 1 };

const appsEmptyStyle: JSX.CSSProperties = {
  color: tokens.fgMuted,
  font: tokens.type.textSm,
  opacity: 0.7,
};

const autoBadgeStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.accentGreen,
  border: `1px solid ${tokens.accentGreen}`,
  'border-radius': `${tokens.radiusSm}`,
  font: tokens.type.titleSm,
  padding: '1px 6px',
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
  font: tokens.type.monoSm,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
  'min-width': 0,
};

const mountStatusStyle: JSX.CSSProperties = {
  color: tokens.fgMuted,
  font: tokens.type.textSm,
  'flex-shrink': 0,
};

const mountAddStyle: JSX.CSSProperties = { display: 'flex', gap: '6px' };

// ----- preferences drawer -----

const drawerStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  gap: '10px',
  'margin-top': '8px',
  'padding-top': '10px',
  'border-top': `1px solid ${tokens.borderMenu}`,
};

const drawerRowStyle: JSX.CSSProperties = { display: 'flex', 'align-items': 'center', gap: '8px' };

const drawerSectionStyle: JSX.CSSProperties = { display: 'flex', 'flex-direction': 'column', gap: '5px' };

const drawerHeadStyle: JSX.CSSProperties = {
  font: tokens.type.titleSm,
  color: tokens.fgMuted,
  'text-transform': 'uppercase',
  'letter-spacing': '0.04em',
};

const drawerHintStyle: JSX.CSSProperties = {
  font: tokens.type.textSm,
  color: tokens.fgMuted,
  opacity: 0.8,
};

const drawerAppsStyle: JSX.CSSProperties = { display: 'flex', 'flex-wrap': 'wrap', gap: '8px' };

const drawerChipRowStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '6px',
  background: tokens.bgWindow,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '2px 4px 2px 8px',
};

const drawerChipPathStyle: JSX.CSSProperties = {
  font: tokens.type.monoSm,
  color: tokens.fg,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
  'max-width': '220px',
};

const emptyStyle: JSX.CSSProperties = {
  padding: '24px 0',
  color: tokens.fgMuted,
  'text-align': 'center',
  font: tokens.type.textMd,
};

const sectionLabelStyle: JSX.CSSProperties = {
  font: tokens.type.titleSm,
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
  font: tokens.type.monoMd,
  'font-weight': 600,
  flex: 1,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

const statusStyle: JSX.CSSProperties = {
  font: tokens.type.textSm,
  color: tokens.fgMuted,
  'white-space': 'nowrap',
};

// candidateAddrStyle is the dim IP shown after a discovered host's name.
const candidateAddrStyle: JSX.CSSProperties = {
  'margin-left': '8px',
  color: tokens.fgMuted,
  font: tokens.type.monoSm,
};

// candidateChipStyle is the small "wash" badge on a discovered wash peer.
const candidateChipStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: tokens.accentCyan,
  border: `1px solid ${tokens.accentCyan}`,
  'border-radius': `${tokens.radiusSm}`,
  font: tokens.type.titleSm,
  padding: '1px 6px',
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
  left: '0',
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
  font: tokens.type.textMd,
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
  font: tokens.type.textMd,
};

const authTermStyle: JSX.CSSProperties = {
  flex: 1,
  'min-height': 0,
  background: tokens.bgCanvas,
  padding: '6px',
};

const authHintStyle: JSX.CSSProperties = {
  padding: '8px 14px',
  font: tokens.type.textSm,
  color: tokens.fgMuted,
  'border-top': `1px solid ${tokens.borderMenu}`,
};

const errorStyle: JSX.CSSProperties = {
  font: tokens.type.monoSm,
  color: tokens.fgWarning,
  'word-break': 'break-word',
};

defineWashApp('wash-app-connect', (props) => <App {...props} />, {
  style: `display:block;width:100%;height:100%;overflow:hidden;background:${tokens.bgWindow};color:${tokens.fg};font:${tokens.type.textMd};box-sizing:border-box`,
});
