// Browser shell runtime entrypoint. Connects to ws://<host>/ws and
// drives the WM via the messages in WIRE.md §8.
//
// WM state is server-authoritative: the router sends a session.snapshot
// on connect and session.patch on every change. The shell stores the
// state and renders from it. Local pointer interactions emit
// window.move/resize/state/focus back to the router, which applies
// the mutation and broadcasts the patch to all attached shells —
// keeping every browser viewing the session in sync.

import { render } from 'solid-js/web';
import { For, Show, createEffect, createSignal } from 'solid-js';
import type { Component } from 'solid-js';
import { type ConnState } from './ws';
import { RouterClient, type ClientHandlers } from './router-client';
import {
  type Origin,
  LOCAL_ORIGIN,
  registerClient,
  unregisterClient,
  registerTag,
  clientForInstance,
  clientForOrigin,
  parseInstanceId,
  compoundInstanceId,
} from './clients';
import { beginBundle, finishBundle, pushBundleBytes } from './assets';
import { RelayChannelSocket } from './relay-socket';
import { tokens } from '@wash/ui';

const __washLoadT0 = performance.now();
import { washFetch, handleAssetReadOK, handleAssetReadErr, pushAssetBytes, finishAsset } from './wash-fetch';
import { loadSettingsPanel, handlePanelReadOK, handlePanelReadErr, pushPanelBytes, finishPanel } from './panels';
import {
  VIEWPORTS_PER_AXIS,
  applySessionPatch,
  applySessionSnapshot,
  desktop,
  dismissCrashed,
  isFocused,
  originForWindow,
  markCrashed,
  mountDesktop,
  moveLocal,
  raiseLocal,
  screenSize,
  setViewport,
  viewport,
  viewportFor,
  windows,
  dropOrigin,
  type Win,
} from './wm';
import { Desktop } from './desktop';
import { FloatingWindow } from './window';
import {
  CatalogApp,
  PanelDesc,
  Sub,
  WindowInfo,
  bindVideoChannel,
  bindPopupChannel,
  closeRawSubscriber,
  deliverRaw,
  deliverToInstance,
  forgetVideoChannel,
  replaceSavedStates,
  setSavedState,
  subscribeRaw,
} from './api';
import './wash-app-display';
import { showToast } from './notify';
import { virtioConsoleFactory } from './virtio';

interface ShellCatalog {
  t: 'catalog';
  apps: CatalogApp[];
  panels?: PanelDesc[];
}

interface ShellAppDeclared {
  t: 'app.declared';
  instance_id: string;
  element: string;
  surface: 'desktop' | 'window';
  manifest: any;
}

export interface SessionWindow {
  window_id: number;
  instance_id: string;
  element: string;
  icon?: string;
  /** Brand accent inherited from the source app's manifest. */
  accent?: string;
  title: string;
  x: number;
  y: number;
  w: number;
  h: number;
  z: number;
  state: 'normal' | 'minimized' | 'maximized';
  focused: boolean;
  restore_x?: number;
  restore_y?: number;
  restore_w?: number;
  restore_h?: number;
  // is_root is router-attested (SO_PEERCRED uid==0, or app_id is in
  // the privilege-chain reserved set). When true the WM paints a red
  // stripe + ROOT label on the titlebar. Never set by the app itself.
  is_root?: boolean;
  // chromeless drops the wash titlebar/border so the guest surface
  // draws its own chrome (e.g. Webamp's Winamp UI). Mirrors the app
  // manifest's WindowHints.Chromeless.
  chromeless?: boolean;
}

interface ShellSessionSnapshot {
  t: 'session.snapshot';
  windows: SessionWindow[];
  app_state?: Record<string, unknown>;
}

export interface SessionPatch {
  op: 'window.upsert' | 'window.delete' | 'app_state';
  window?: SessionWindow;
  window_id?: number;
  instance_id?: string;
  state?: unknown;
}

interface ShellSessionPatch {
  t: 'session.patch';
  patches: SessionPatch[];
}

interface ShellAppMsgDeliver {
  t: 'app_msg.deliver';
  instance_id: string;
  data: unknown;
}

interface ShellChannelBind {
  t: 'channel.bind';
  channel_id: number;
  window_id: number;
  kind?: string;
  instance_id?: string;
  // origin names the remote host for a kind="peer" relay channel.
  origin?: string;
}

interface ShellChannelUnbind {
  t: 'channel.unbind';
  channel_id: number;
  reason?: string;
}

interface ShellNotify {
  t: 'notify';
  instance_id: string;
  title: string;
  body?: string;
  level?: 'info' | 'warn' | 'error';
}

export interface ShellAppCrashed {
  t: 'app.crashed';
  instance_id: string;
  window_id?: number;
  app_id: string;
  exit_code: number;
  signal?: string;
  uptime: string;
  log: string;
}

// Reactive subs the chrome (mounted via window.wash) listens to.
// catalogSub is the LOCAL router's catalog (drives the launcher).
const catalogSub = new Sub<CatalogApp[]>([]);
const panelsSub = new Sub<PanelDesc[]>([]);

// Remote routers' catalogs, keyed by origin (docs/REMOTE.md §6.1). A
// remote host's catalog arrives on connect exactly like the local one;
// wash-connect lists it to offer "launch on B". remoteCatalogSub fires
// with {origin, apps} whenever any remote catalog updates (or empties on
// disconnect), so a subscriber re-reads catalogFor() for its origin.
const remoteCatalogs = new Map<Origin, CatalogApp[]>();
const remoteCatalogSub = new Sub<{ origin: Origin; apps: CatalogApp[] } | null>(null);

// Remote-apps relay (docs/REMOTE.md, "one port"): a host's entire wire
// rides a single raw channel of the LOCAL connection. peerSockets maps that
// channel id → the RelayChannelSocket feeding the host's RouterClient.
// Keyed by the local channel id (peer channels only ever bind on `local`).
const peerSockets = new Map<number, { origin: Origin; sock: RelayChannelSocket }>();

/** catalogFor returns a router's catalog by origin (LOCAL or a remote host). */
function catalogFor(origin: Origin): CatalogApp[] {
  return origin === LOCAL_ORIGIN ? catalogSub.value : remoteCatalogs.get(origin) ?? [];
}

/** clearRemoteCatalog drops a host's catalog on disconnect and notifies. */
function clearRemoteCatalog(origin: Origin): void {
  if (remoteCatalogs.delete(origin)) remoteCatalogSub.set({ origin, apps: [] });
}
const windowsSub = new Sub<WindowInfo[]>([]);
// viewportSub mirrors the Solid viewport signal into the cross-element
// pub/sub the session app subscribes to via window.wash.onViewport.
// We also publish per-window viewport assignments here so the pager
// can draw window outlines in the right cell without re-deriving
// the math FE-side.
const viewportSub = new Sub<{ vx: number; vy: number }>({ vx: 0, vy: 0 });
const screenSub = new Sub<{ w: number; h: number }>({ w: window.innerWidth, h: window.innerHeight });

// Router-held clipboard, mirrored shell-side. The Sub carries the
// latest content pushed by clipboard.changed broadcasts; gets resolve
// through pendingClipboardGets (req_id → resolver), same shape the Go
// SDK uses for its ClipboardGet round-trip.
const clipboardSub = new Sub<{ mime: string; text: string }>({ mime: '', text: '' });
let clipboardReqID = 0;

function wsURL(): string {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  const params = new URLSearchParams(window.location.search);
  // M0 remote-apps transport spike (docs/REMOTE.md): ?remote=<ws-url>
  // points the shell at an arbitrary router instead of this origin's.
  // With `ssh -L 127.0.0.1:PORT:<B-router-sock>` this connects the
  // shell to host B's router through the tunnel, proving the wire +
  // replayBundleToShell delivery end to end. (The simultaneous second
  // connection that composites a single B window arrives with the M1
  // RouterClient registry; this override is the single-conn spike.)
  const remote = params.get('remote');
  if (remote) return remote;
  // When the page URL carries ?s=<sessid>, route the WS at the
  // sessid-specific endpoint so wash-login attaches to that session
  // instead of auto-picking. Bare /ws is used when no preference is
  // declared — wash-login then spawns (0 sessions) or attaches to
  // the lone one (1 session) by default.
  const sessid = params.get('s');
  const path = sessid ? `/ws/s/${encodeURIComponent(sessid)}/` : '/ws';
  return `${proto}://${window.location.host}${path}`;
}

/**
 * Pick the wash transport from the URL. Default is a real WebSocket
 * to the router's HTTP listener. `?transport=virtio-console&port=2`
 * routes through the v86 emulator's virtio-console bus events — used
 * by the online demo (PLAN.md Phase 6b). The v86 instance attaches
 * its bus to `window.washV86Bus` before loading the shell.
 */
function pickTransport(): string | (() => import('./ws').SocketFactory extends () => infer S ? S : never) {
  // Prefer a window-global config object (set by the outer page before
  // importing the shell). Falls back to URL params for backwards
  // compatibility. The window-global path lets the demo page keep the
  // URL bar clean — no transient ?transport=… flicker on load.
  const cfg = (window as unknown as { __washShellTransport?: { kind?: string; port?: number } }).__washShellTransport;
  const params = new URLSearchParams(window.location.search);
  const t = cfg?.kind ?? params.get('transport');
  if (t !== 'virtio-console') return wsURL();
  const portN = cfg?.port ?? Number(params.get('port') ?? '2');
  const bus = (window as unknown as { washV86Bus?: import('./virtio').V86Bus }).washV86Bus;
  if (!bus) {
    console.error('wash shell: virtio-console transport requested but window.washV86Bus is undefined; falling back to WebSocket');
    return wsURL();
  }
  return virtioConsoleFactory(bus, portN) as any;
}

// makeHandlers builds the per-connection dispatch for a RouterClient,
// tagging every incoming message with that client's origin so a remote
// router's apps/windows merge into the one desktop without colliding with
// the local router's ids. The local client is just origin === LOCAL.
function makeHandlers(client: RouterClient): ClientHandlers {
  const isLocal = client.origin === LOCAL_ORIGIN;
  return {
  onCtrl: (msg) => {
    switch (msg.t) {
      case 'catalog': {
        // The local catalog drives the launcher + settings panels. A
        // remote host's catalog is stored per-origin so wash-connect can
        // list "apps you can launch on B" (docs/REMOTE.md §6.1).
        const c = msg as ShellCatalog;
        if (isLocal) {
          catalogSub.set(c.apps);
          panelsSub.set(c.panels ?? []);
        } else {
          remoteCatalogs.set(client.origin, c.apps);
          remoteCatalogSub.set({ origin: client.origin, apps: c.apps });
        }
        break;
      }
      case 'app.declared':
        handleAppDeclared(client, msg as ShellAppDeclared);
        break;
      case 'session.snapshot':
        handleSnapshot(client, msg as ShellSessionSnapshot);
        break;
      case 'session.patch':
        handlePatch(client, msg as ShellSessionPatch);
        break;
      case 'app_msg.deliver':
        deliverAppMsg(client, msg as ShellAppMsgDeliver);
        break;
      case 'notify': {
        // Remote-host notifications merge into the tray in M4; for now only
        // the local router's toasts show.
        if (!isLocal) break;
        const n = msg as ShellNotify;
        showToast({
          instanceID: n.instance_id,
          title: n.title,
          body: n.body,
          level: n.level,
        });
        break;
      }
      case 'app.crashed':
        handleCrash(client, msg as ShellAppCrashed);
        break;
      case 'shell.reload': {
        // Dev-mode signal: only the LOCAL router may bounce the page (a
        // remote host must never reload the whole desktop). The router's
        // re-exec / app respawn happens independently.
        if (!isLocal) break;
        // eslint-disable-next-line no-console
        console.info('wash shell: reload requested by router');
        window.location.reload();
        break;
      }
      case 'channel.bind': {
        const b = msg as ShellChannelBind;
        if (b.kind === 'bundle' && b.instance_id) {
          // Bundle delivery channel — accumulate (per origin) until the
          // matching channel.unbind triggers the dynamic import.
          client.bundleReady.set(b.instance_id, beginBundle(b.channel_id, b.instance_id, client.origin));
        } else if (b.kind === 'bundle') {
          // Settings-panel bundle channel (no instance_id): local-only,
          // keyed by channel_id in panels.ts. Nothing to do here.
        } else if (b.kind === 'asset') {
          // Asset channel: local-only, keyed by (req_id, channel_id) in
          // wash-fetch.ts. Nothing to do here.
        } else if (b.kind === 'video') {
          // Per-window video for the built-in <wash-app-display> decoder
          // (wire.ChannelKindVideo). Works for LOCAL and REMOTE origins: a
          // remote host's display video rides the relay's origin-scoped raw
          // path, and the registry (api.ts) is keyed by (origin,windowID) so
          // it never collides with a local window. Frame bytes flow via
          // deliverRaw; the registry handles the bind-before-mount race.
          // (docs/REMOTE.md §15.1.)
          client.channelOwner.set(b.channel_id, b.window_id);
          bindVideoChannel(client.origin, b.window_id, b.channel_id);
        } else if (b.kind === 'video-popup') {
          // Child surface (menu/dropdown) of a display window: window_id is
          // the PARENT win, drawn as a positioned overlay on its
          // <wash-app-display>. Origin-scoped like video.
          client.channelOwner.set(b.channel_id, b.window_id);
          bindPopupChannel(client.origin, b.window_id, b.channel_id);
        } else if (b.kind === 'peer' && isLocal) {
          // Remote-apps relay: A spliced this channel to host B. Stand up a
          // RouterClient for the origin whose transport is this channel.
          attachPeerChannel(b.channel_id, b.origin ?? '');
        } else {
          client.channelOwner.set(b.channel_id, b.window_id);
        }
        break;
      }
      // asset.read / panel.read are the shell fetching its OWN assets +
      // settings panels from its router — a local-only concern.
      case 'asset.read.ok':
        if (isLocal) handleAssetReadOK(msg as { req_id: number; channel_id: number; size: number; mime?: string });
        break;
      case 'asset.read.err':
        if (isLocal) handleAssetReadErr(msg as { req_id: number; code: string; msg?: string });
        break;
      case 'panel.read.ok':
        if (isLocal) handlePanelReadOK(msg as { req_id: number; channel_id: number; size: number });
        break;
      case 'panel.read.err':
        if (isLocal) handlePanelReadErr(msg as { req_id: number; code: string; msg?: string });
        break;
      case 'channel.unbind': {
        const u = msg as ShellChannelUnbind;
        // Remote-apps relay channel gone (A tore down the peer): drop the
        // host's RouterClient + windows. Do this before the generic cleanup.
        if (isLocal) {
          const peer = peerSockets.get(u.channel_id);
          if (peer) {
            peerSockets.delete(u.channel_id);
            peer.sock.close();
            detachClient(peer.origin);
            break;
          }
        }
        // Try each accumulator in turn; harmless on miss.
        finishBundle(u.channel_id, client.origin);
        // Drop any stashed video binding (this origin) so a later rebind on
        // the same window doesn't replay a dead channel to a fresh element.
        // Runs for remote origins too now that remote display is enabled.
        forgetVideoChannel(client.origin, u.channel_id);
        if (isLocal) {
          finishAsset(u.channel_id);
          finishPanel(u.channel_id);
        }
        client.channelOwner.delete(u.channel_id);
        closeRawSubscriber(client.origin, u.channel_id);
        // Forget any pending credit count — channel is gone.
        client.credit.forget(u.channel_id);
        break;
      }
      case 'clipboard.data': {
        const d = msg as { req_id: number; mime: string; text: string };
        const wait = client.pendingClipboardGets.get(d.req_id);
        if (wait) {
          client.pendingClipboardGets.delete(d.req_id);
          wait(d.text);
        }
        break;
      }
      case 'peer.error': {
        // Remote-apps relay attach failed (no registration / dial). Log it
        // (forwarded to the router) so a host that's "up" but shows no apps
        // isn't a silent mystery. wash-connect can surface it later.
        if (!isLocal) break;
        const e = msg as { origin: string; msg: string };
        console.warn(`wash: relay attach failed for ${e.origin}: ${e.msg}`);
        break;
      }
      case 'clipboard.changed': {
        // Cross-host clipboard sync is M5; mirror only the local router's.
        if (!isLocal) break;
        const c = msg as { mime: string; text: string };
        clipboardSub.set({ mime: c.mime, text: c.text });
        break;
      }
    }
  },
  onRaw: (channelID, bytes) => {
    // Remote-apps relay: a peer channel's bytes are host B's wire — feed
    // them to its RelayChannelSocket (which deframes + drives B's Conn).
    // Peer channels only ever bind on the local connection. The peer channel
    // is a CREDITLESS verbatim conduit on A (docs/REMOTE.md §7): we emit no
    // channel.credit for it. Flow control is end to end — B's RouterClient
    // (peer.sock's Conn) credits each of B's INNER channels as it absorbs
    // them, which is the real backpressure; an A-side window would only
    // double-gate and head-of-line-block B's interactive behind B's bulk.
    if (isLocal) {
      const peer = peerSockets.get(channelID);
      if (peer) {
        peer.sock.feed(bytes);
        return;
      }
    }
    // Bundle bytes (per origin) first, so a remote bundle channel id can't
    // be mistaken for a local asset channel. Asset/panel accumulators are a
    // local-only concern (the shell fetching its own assets/panels).
    if (pushBundleBytes(channelID, bytes, client.origin)) return;
    if (isLocal) {
      if (pushAssetBytes(channelID, bytes)) return;
      if (pushPanelBytes(channelID, bytes)) return;
    }
    deliverRaw(client.origin, channelID, bytes);
    // Bulk-class raw flows drain the router-side credit window — replenish
    // via channel.credit as we absorb. Bundle bytes returned early above.
    client.credit.absorbed(channelID, bytes.length);
  },
  };
}

// The shell's connection to its local router. Remote hosts add more
// clients via addClient(); makeHandlers tags each client's dispatch with
// its origin so they merge into one desktop.
const local = new RouterClient(LOCAL_ORIGIN, pickTransport() as any, makeHandlers);
registerClient(LOCAL_ORIGIN, local);

// Local-only handles for window.wash / logging / __washDiag, which address
// the shell's own router directly.
const conn = local.conn;
const instances = local.instances;
const bundleReady = local.bundleReady;
const pendingClipboardGets = local.pendingClipboardGets;

// Let a remote app bundle report the per-origin mangled element tag it
// defined (web/lib defineWashApp), so the mount sites instantiate the same
// tag via clients.tagFor(). No-op for local bundles (which never mangle).
(window as unknown as { __washRegisterTag?: (o: string, m: string, r: string) => void }).__washRegisterTag =
  registerTag;

// addClient opens a second connection to another router (a remote host
// reached over an ssh -L tunnel) and registers it under `origin`, so its
// windows composite into this desktop. Wired to ?peer=<origin>@<ws-url>
// for the two-router test below; M2's com.wash.remote service drives it
// for real.
function addClient(origin: Origin, url: string): RouterClient {
  // Idempotent: re-attaching an already-connected origin (e.g. the
  // supervisor re-reporting 'up') reuses the live client rather than
  // opening a duplicate WS and shadowing the first in the registry.
  const existing = clientForOrigin(origin);
  if (existing) return existing;
  const client = new RouterClient(origin, url, makeHandlers);
  registerClient(origin, client);
  void client.conn.ready();
  return client;
}

// attachPeerChannel stands up a remote host's RouterClient whose transport
// is a relay channel of the LOCAL connection (docs/REMOTE.md, "one port"):
// A spliced this channel to host B's router, so B's wire arrives here. The
// RelayChannelSocket deframes it; the RouterClient merges B's windows into
// the desktop exactly like a direct connection would. Driven by the
// channel.bind{kind:"peer"} A sends after a peer.attach.
function attachPeerChannel(channelID: number, origin: Origin): void {
  if (!origin || origin === LOCAL_ORIGIN) return;
  if (clientForOrigin(origin)) return; // already attached
  const sock = new RelayChannelSocket(local.conn, channelID);
  peerSockets.set(channelID, { origin, sock });
  const client = new RouterClient(origin, () => sock, makeHandlers);
  registerClient(origin, client);
  void client.conn.ready();
}

// detachClient tears down a remote origin's connection and scrubs every
// trace of it from the desktop (docs/REMOTE.md §6.1/§9): close the WS
// (no reconnect — this is a deliberate disconnect, not a blip), drop the
// origin's windows, clear its catalog, and unregister it. LOCAL is never
// detachable (it's the seat's own router).
function detachClient(origin: Origin): void {
  if (origin === LOCAL_ORIGIN) return;
  const client = clientForOrigin(origin);
  if (!client) return;
  client.conn.close();
  dropOrigin(origin);
  clearRemoteCatalog(origin);
  unregisterClient(origin);
}

{
  // ?peer=<origin>@<ws-url> (repeatable) attaches remote routers — the
  // M1f manual test harness (two local routers) and a stand-in until the
  // Hosts sidebar widget (M3) drives connections.
  for (const spec of new URLSearchParams(window.location.search).getAll('peer')) {
    const at = spec.indexOf('@');
    if (at <= 0) continue;
    const origin = spec.slice(0, at);
    const url = spec.slice(at + 1);
    if (origin === LOCAL_ORIGIN || !url) continue;
    try {
      addClient(origin, url);
    } catch (e) {
      console.error('wash: addClient', origin, e);
    }
  }
}

// deliverAppMsg routes a BE→FE message to its element, queuing if the
// element hasn't mounted yet (Solid's onMount can run after the next
// WS message is processed).
function deliverAppMsg(client: RouterClient, msg: ShellAppMsgDeliver) {
  deliverToInstance(compoundInstanceId(client.origin, msg.instance_id), msg.data);
}

// Mirror Solid's windows store into the cross-element Sub so vanilla
// custom elements (the session chrome) can subscribe without taking
// a Solid dep.
createEffect(() => {
  const s = screenSize();
  windowsSub.set(
    windows.map((w) => ({
      origin: w.origin,
      windowID: w.windowID,
      instanceID: w.instanceID,
      element: w.element,
      icon: w.icon,
      title: w.title,
      // isFocused() reads the focused signal, so this effect re-runs on
      // focus change.
      focused: isFocused(w),
      state: w.state,
      x: w.x,
      y: w.y,
      w: w.w,
      h: w.h,
      // viewport: cell where the window's center lives; convenient
      // for the pager + taskbar dblclick snap-to.
      viewport: viewportFor(w),
    })),
  );
  // screenSub also updates here because window list rendering for
  // the pager depends on cell dimensions; cheaper than its own effect.
  if (screenSub.value.w !== s.w || screenSub.value.h !== s.h) {
    screenSub.set(s);
  }
});

createEffect(() => {
  viewportSub.set(viewport());
});

function handleAppDeclared(client: RouterClient, msg: ShellAppDeclared): void {
  // Background services have no FE — no bundle, no element, no mount.
  // The shell ignores the declaration: the BE talks to other apps via
  // cross-app app_msg, and nothing on this side ever needs to address
  // it by element name.
  if ((msg.surface as string) === 'background') {
    return;
  }
  client.instances.set(msg.instance_id, { element: msg.element, surface: msg.surface });
  if (msg.surface === 'desktop') {
    // Only the LOCAL router owns the desktop chrome; a remote host's
    // desktop surface is ignored (we composite its windows, not its shell).
    if (client.origin !== LOCAL_ORIGIN) return;
    client
      .waitForBundle(msg.instance_id)
      .then(() => mountDesktop({ instanceID: msg.instance_id, element: msg.element }))
      .catch((err) => console.error('wash: desktop bundle:', err));
  }
  // surface=window apps mount on their session.window upsert; the
  // window-create path awaits the bundle promise the same way.
}

// handleSnapshot rebuilds a router's WM state from its canonical view.
// Sent on connect/reconnect. The app_state cache is replaced per-origin so
// stale entries from no-longer-running instances don't linger.
function handleSnapshot(client: RouterClient, msg: ShellSessionSnapshot): void {
  replaceSavedStates(client.origin, msg.app_state);
  applySessionSnapshot(client.origin, msg.windows, (id) => client.waitForBundle(id));
}

// handleCrash marks the matching window crashed in the WM store so
// the FloatingWindow renders the tombstone overlay instead of the
// (dead) custom element. The router still ships a window-delete
// patch right after this; wm.applySessionPatch ignores deletes for
// already-crashed windows so the tombstone survives.
function handleCrash(client: RouterClient, msg: ShellAppCrashed): void {
  markCrashed(client.origin, msg.instance_id, {
    appID: msg.app_id,
    exitCode: msg.exit_code,
    signal: msg.signal,
    uptime: msg.uptime,
    log: msg.log,
  });
}

// seenWindowIDs (on RouterClient) tracks first-sighted windows for the
// viewport auto-relocation below: the wm store can't serve this on its
// own because applySessionPatch defers an unseen window's upsert behind
// waitForBundle, so a window-in-flight isn't in `windows` yet — without
// the set, every pre-bundle patch looks "fresh" and we'd re-relocate the
// same window N times.
function handlePatch(client: RouterClient, msg: ShellSessionPatch): void {
  // Apply app_state ops first so when a window upsert in the same
  // patch triggers a remount, wash:state carries the latest blob.
  for (const p of msg.patches) {
    if (p.op === 'app_state' && typeof p.instance_id === 'string') {
      setSavedState(compoundInstanceId(client.origin, p.instance_id), p.state ?? null);
    }
  }
  // First-sight detection for viewport auto-relocation: any
  // window.upsert whose id isn't in seenWindowIDs is a new spawn.
  // The router cascades new windows from (40, 40); if the user is
  // looking at a non-(0,0) viewport, we re-issue a window.move so
  // the window appears where they're actually looking. Otherwise
  // new windows silently land off-screen in cell (0,0).
  //
  // We MUTATE the patch's window x/y here (rather than calling
  // moveLocal afterwards) so applySessionPatch's bundle-deferred
  // upsert uses the relocated coords directly — moveLocal on a
  // not-yet-in-store window is a no-op.
  const vp = viewport();
  const s = screenSize();
  const moves: Array<{ id: number; x: number; y: number }> = [];
  for (const p of msg.patches) {
    if (p.op === 'window.upsert' && p.window && !client.seenWindowIDs.has(p.window.window_id)) {
      if (vp.vx !== 0 || vp.vy !== 0) {
        p.window.x = p.window.x + vp.vx * s.w;
        p.window.y = p.window.y + vp.vy * s.h;
        moves.push({ id: p.window.window_id, x: p.window.x, y: p.window.y });
      }
      client.seenWindowIDs.add(p.window.window_id);
    }
    if (p.op === 'window.delete' && typeof p.window_id === 'number') {
      client.seenWindowIDs.delete(p.window_id);
    }
  }
  applySessionPatch(
    client.origin,
    msg.patches.filter((p) => p.op !== 'app_state'),
    (id) => client.waitForBundle(id),
  );
  for (const m of moves) {
    client.conn.sendCtrl({ t: 'window.move', window_id: m.id, x: m.x, y: m.y });
  }
}

// Bridge a window's close-button click into the WS protocol.
// Crashed windows are FE-only tombstones — the router-side state was
// already torn down on abnormal exit, so a close_clicked would have
// nowhere to land. Drop them directly out of the WM store.
function onWindowClose(win: Win): void {
  if (win.crashed) {
    dismissCrashed(win.origin, win.windowID);
    return;
  }
  (clientForOrigin(win.origin) ?? local).conn.sendCtrl({ t: 'window.close_clicked', window_id: win.windowID });
  // The actual removal happens when the router sends window.destroy.
}

const [connState, setConnState] = createSignal<ConnState>('connecting');
conn.onState(setConnState);

// When the reconnect loop gives up on auth grounds, bounce to the
// login page (wash-login) so the user re-authenticates. The raw router
// reports no login_url — recovery there is reopening the token URL, so
// we leave the banner up instead of redirecting nowhere.
createEffect(() => {
  if (connState() !== 'unauthenticated') return;
  const url = conn.loginRedirect();
  if (url) {
    // A short beat so the banner paints before navigation.
    setTimeout(() => { location.href = url; }, 800);
  }
});

// Ctrl+Alt+Arrows pan one viewport. Listening at the document level
// means the chord works regardless of which (if any) window has focus.
// Apps inside windows that want to swallow these keys can preventDefault
// on their own keydown handler — keypresses bubble up to here only when
// nobody else stops them.
window.addEventListener('keydown', (ev: KeyboardEvent) => {
  if (!ev.ctrlKey || !ev.altKey || ev.shiftKey || ev.metaKey) return;
  const vp = viewport();
  let dx = 0;
  let dy = 0;
  switch (ev.key) {
    case 'ArrowLeft':
      dx = -1;
      break;
    case 'ArrowRight':
      dx = 1;
      break;
    case 'ArrowUp':
      dy = -1;
      break;
    case 'ArrowDown':
      dy = 1;
      break;
    default:
      return;
  }
  ev.preventDefault();
  setViewport(vp.vx + dx, vp.vy + dy);
});

// Viewport pan: the cam div translates the windows layer by
// (-vx*W, -vy*H) screen pixels so the user "moves" across a
// VIEWPORTS_PER_AXIS² grid without the router knowing. The Desktop
// surface (taskbar, wallpaper) sits outside this container — it
// stays fixed across viewports, matching X11 viewport semantics.
// pointer-events:none on the cam lets clicks fall through to the
// desktop surface in empty space; floating windows re-enable
// pointer-events on their own frames.
const camStyle = () => {
  const vp = viewport();
  const s = screenSize();
  return {
    position: 'absolute' as const,
    inset: '0',
    transform: `translate(${-vp.vx * s.w}px, ${-vp.vy * s.h}px)`,
    transition: 'transform 260ms cubic-bezier(.2,.7,.2,1)',
    'will-change': 'transform' as const,
    'pointer-events': 'none' as const,
  };
};

const App = () => (
  <>
    <Desktop />
    <div data-testid="wash-cam" style={camStyle()}>
      <For each={windows}>{(w) => <FloatingWindow win={w} onClose={onWindowClose} />}</For>
    </div>
    <ConnectionBanner state={connState()} />
  </>
);

// ConnectionBanner shows a transient status overlay when the WS
// is anything other than open. Lives in the shell (not in an app)
// because if the WS is down, the apps are unreachable anyway.
// Top-center placement so it's visible without covering taskbar
// or window chrome.
const ConnectionBanner: Component<{ state: ConnState }> = (props) => (
  <Show when={props.state !== 'open'}>
    <div
      data-testid="wash-connection-banner"
      data-state={props.state}
      style={{
        position: 'fixed',
        top: '10px',
        left: '50%',
        transform: 'translateX(-50%)',
        background: props.state === 'closed' || props.state === 'unauthenticated' ? tokens.bgDanger : tokens.bgDenied,
        color: tokens.fg,
        border: `1px solid ${props.state === 'closed' || props.state === 'unauthenticated' ? tokens.borderDanger : tokens.borderDenied}`,
        'border-radius': '6px',
        padding: '6px 14px',
        font: '12px ui-sans-serif,system-ui,sans-serif',
        'box-shadow': '0 6px 16px rgba(0,0,0,0.5)',
        'z-index': 100000,
        'pointer-events': 'none',
        animation: 'wash-fade-in 200ms ease-out',
      }}
    >
      {props.state === 'connecting' && 'connecting…'}
      {props.state === 'reconnecting' && 'router unreachable — reconnecting…'}
      {props.state === 'closed' && 'disconnected'}
      {props.state === 'unauthenticated' &&
        (conn.loginRedirect()
          ? 'session expired — redirecting to log in…'
          : 'session expired — reopen your token URL to reconnect')}
    </div>
  </Show>
);

void conn.ready();
render(App, document.getElementById('root')!);

// Provide a tiny FE-side API for apps that want to send app_msg back
// to their BE half. The session app uses this to send the "launch"
// action. Exposed as window.wash so app bundles can find it without
// import gymnastics.
// Recipient mirrors wire.Recipient — exactly one field set. AppID
// works only for singleton-instancing apps (router spawns on demand
// when not yet running); InstanceID is a direct address.
export type Recipient = { app_id: string } | { instance_id: string };

declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
      sendAppMsgTo(recipient: Recipient, data: unknown): void;
      catalog(): CatalogApp[];
      onCatalog(cb: (apps: CatalogApp[]) => void): () => void;
      // Remote-host catalogs (docs/REMOTE.md §6.1). catalogFor returns the
      // apps a given origin (LOCAL or a connected remote host) advertises;
      // onRemoteCatalog fires whenever any remote catalog changes (apps
      // empty on disconnect). wash-connect uses these to list B's apps.
      catalogFor(origin: string): CatalogApp[];
      onRemoteCatalog(cb: (ev: { origin: string; apps: CatalogApp[] }) => void): () => void;
      // launchOn asks the router at `origin` to spawn appID (docs/REMOTE.md
      // §6.1). For a remote host (which runs --no-session) this is the only
      // launch path — there is no session BE there. Fire-and-forget: the
      // launched window composites in via the normal app.declared flow.
      launchOn(origin: string, appID: string): void;
      // attachRemote opens a second connection to a remote host's router
      // (the local end of an ssh -L tunnel the com.wash.remote supervisor
      // set up) and composites its windows into this desktop, tagged by
      // origin. detachRemote tears it down and drops the host's windows.
      // wash-connect drives these from the supervisor's reported endpoint.
      attachRemote(origin: string, url?: string): void;
      detachRemote(origin: string): void;
      // App-supplied settings panels (from the catalog's `panels` list).
      // loadSettingsPanel fetches+imports the panel bundle so its custom
      // element is defined; the promise resolves once it's mountable.
      settingsPanels(): PanelDesc[];
      onSettingsPanels(cb: (panels: PanelDesc[]) => void): () => void;
      loadSettingsPanel(appID: string): Promise<void>;
      windows(): WindowInfo[];
      onWindowsChanged(cb: (windows: WindowInfo[]) => void): () => void;
      // origin (optional) addresses the WM intent to a specific router:
      // window ids are per-router, so the shell chrome passes the Win's
      // origin to avoid aiming a remote window at the same-id local one.
      // Omitted → resolved by bare id (app bundles addressing their own).
      focusWindow(id: number, origin?: Origin): void;
      closeWindow(id: number, origin?: Origin): void;
      moveWindow(id: number, x: number, y: number, origin?: Origin): void;
      resizeWindow(id: number, w: number, h: number, origin?: Origin): void;
      minimizeWindow(id: number, origin?: Origin): void;
      maximizeWindow(id: number, origin?: Origin): void;
      restoreWindow(id: number, origin?: Origin): void;
      // Virtual-desktop viewport API. The shell pans a viewport-sized
      // camera over a VIEWPORTS_PER_AXIS² plane; setViewport switches
      // cells with a CSS transition. viewportFor returns the cell
      // owning a given window's center (used for taskbar dblclick).
      viewports(): { perAxis: number };
      getViewport(): { vx: number; vy: number };
      setViewport(vx: number, vy: number): void;
      onViewport(cb: (vp: { vx: number; vy: number }) => void): () => void;
      onScreenSize(cb: (s: { w: number; h: number }) => void): () => void;
      log(level: 'error' | 'warn' | 'info' | 'debug', source: string, msg: string, stack?: string): void;
      openRawChannel(channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
      writeRaw(channelID: number, bytes: Uint8Array): void;
      rawBufferedAmount(): number;
      // Origin-scoped raw API (docs/REMOTE.md §4): route a channel to a
      // specific host's connection so a remote app's pty/file stream isn't
      // mis-routed to (or collided with) the local router. origin comes from
      // the app's props.origin.
      openRawChannelFor(origin: string, channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
      writeRawFor(origin: string, channelID: number, bytes: Uint8Array): void;
      rawBufferedAmountFor(origin: string): number;
      // Router-held clipboard (the wash-internal clipboard every app
      // shares). Text-only on this surface; see clipboard.ts in
      // @wash/ui for the system-clipboard mirroring helpers.
      clipboardSetText(text: string): void;
      clipboardGetText(): Promise<string>;
      onClipboardChanged(cb: (c: { mime: string; text: string }) => void): () => void;
    };
  }
}

type LogLevel = 'error' | 'warn' | 'info' | 'debug';

// shellLog hardening: the naive version dropped lines whenever the WS
// wasn't open — and boot/reconnect windows are exactly when errors
// cluster. So: (1) lines that can't be sent are ring-buffered and
// flushed when the connection (re)opens; (2) msg/stack are capped so
// one console.log(hugeObject) can't balloon a ctrl frame; (3) a
// per-second budget stops an error loop from flooding the router log
// (suppressed lines are counted and reported, not lost silently).
const LOG_MSG_CAP = 4096;
const LOG_STACK_CAP = 8192;
const LOG_BUF_CAP = 100;
const LOG_RATE_MAX = 20; // forwarded lines per second

type LogEntry = { t: 'log'; level: LogLevel; source: string; msg: string; stack?: string };
const logBuf: LogEntry[] = [];
let logBufDropped = 0;
let logWsUp = false;
let logRateWindow = 0;
let logRateCount = 0;
let logSuppressed = 0;

function shellLog(level: LogLevel, source: string, msg: string, stack?: string) {
  if (msg.length > LOG_MSG_CAP) msg = msg.slice(0, LOG_MSG_CAP) + '…[truncated]';
  if (stack && stack.length > LOG_STACK_CAP) stack = stack.slice(0, LOG_STACK_CAP) + '…[truncated]';

  const now = Date.now();
  if (now - logRateWindow >= 1000) {
    logRateWindow = now;
    logRateCount = 0;
    if (logSuppressed > 0) {
      const n = logSuppressed;
      logSuppressed = 0;
      sendLogEntry({ t: 'log', level: 'warn', source: 'shell', msg: `log flood: suppressed ${n} line(s) in the last burst` });
    }
  }
  if (logRateCount >= LOG_RATE_MAX) {
    logSuppressed++;
    return;
  }
  logRateCount++;
  sendLogEntry({ t: 'log', level, source, msg, ...(stack ? { stack } : {}) });
}

function sendLogEntry(entry: LogEntry) {
  // Avoid recursing through console.error (we wrap it below): all
  // failure handling here is silent buffering, never logging.
  if (logWsUp) {
    try {
      conn.sendCtrl(entry);
      return;
    } catch {
      /* fall through to buffer */
    }
  }
  if (logBuf.length >= LOG_BUF_CAP) {
    logBuf.shift();
    logBufDropped++;
  }
  logBuf.push(entry);
}

function flushLogBuf() {
  if (logBufDropped > 0) {
    const n = logBufDropped;
    logBufDropped = 0;
    logBuf.unshift({ t: 'log', level: 'warn', source: 'shell', msg: `log buffer overflowed while disconnected: ${n} oldest line(s) dropped` });
  }
  while (logBuf.length > 0) {
    const entry = logBuf.shift()!;
    try {
      conn.sendCtrl(entry);
    } catch {
      logBuf.unshift(entry);
      return; // still not writable; retry on the next open
    }
  }
}

conn.onState((s) => {
  logWsUp = s === 'open';
  if (logWsUp) flushLogBuf();
});

// wmSend routes a window-manager intent to the router that owns the
// window. Callers that know the origin (the shell's own FloatingWindow,
// the session taskbar — both hold the Win/WindowInfo) MUST pass it: window
// ids are per-router, so two origins routinely share id 1, and resolving by
// bare id alone would aim a remote window's drag at the local window with
// the same id (the "connect window follows the remote one" bug). The bare-id
// originForWindow fallback remains only for app bundles that address their
// own window by id without an origin in hand.
function wmSend(origin: Origin, windowID: number, msg: Record<string, unknown>): void {
  void windowID;
  const client = clientForOrigin(origin) ?? local;
  client.conn.sendCtrl(msg);
}

window.wash = {
  sendAppMsg(instanceID, data) {
    // instanceID is the app-facing (possibly origin-tagged) id. Route to
    // the owning client and send the bare id the router understands. For
    // a local app this resolves to `local` with the id unchanged.
    const { bare } = parseInstanceId(instanceID);
    const client = clientForInstance(instanceID) ?? local;
    client.conn.sendCtrl({ t: 'app_msg.send', instance_id: bare, data });
  },
  sendAppMsgTo(recipient, data) {
    conn.sendCtrl({ t: 'app_msg.send', to: recipient, data });
  },
  catalog: () => catalogSub.value,
  onCatalog: (cb) => catalogSub.on(cb),
  // Pull a static asset (theme wallpaper, icon, …) from the router's
  // asset FS over the WS bus — transport-agnostic, so it works in the
  // in-browser VM where HTTP/ingress don't. Resolves from the runtime
  // drop spot (~/.config/wash/assets) or the embedded chrome. Always
  // the local router (asset.read is local-only).
  fetchAsset: (path) => washFetch((msg) => conn.sendCtrl(msg), path),
  catalogFor: (origin) => catalogFor(origin),
  onRemoteCatalog: (cb) => remoteCatalogSub.on((ev) => { if (ev) cb(ev); }),
  launchOn(origin, appID) {
    const client = clientForOrigin(origin);
    if (!client) {
      console.warn('wash: launchOn unknown origin', origin);
      return;
    }
    client.conn.sendCtrl({ t: 'shell.launch', app_id: appID });
  },
  attachRemote(origin, url) {
    if (origin === LOCAL_ORIGIN) return;
    if (url) {
      // Direct second connection to a reachable router (browser co-located
      // with B, e.g. two routers on one host). Valid but bypasses the relay,
      // so the supervisor never uses it — production always goes through the
      // one-port relay below. This branch backs the host-process FE-merge
      // e2e (connect-launch / ?peer=), where both routers are local.
      try {
        addClient(origin, url);
      } catch (e) {
        console.error('wash: attachRemote', origin, e);
      }
      return;
    }
    // Relay path (one port): ask A's router to splice a peer channel to the
    // socket the supervisor registered. The channel.bind reply stands up B's
    // RouterClient (attachPeerChannel). Works over any transport to A.
    local.conn.sendCtrl({ t: 'peer.attach', origin });
  },
  detachRemote(origin) {
    // Tear down A's relay (→ channel.unbind → detachClient) and detach
    // locally too (covers the direct-WS case / an attach that never bound).
    try {
      local.conn.sendCtrl({ t: 'peer.detach', origin });
    } catch {
      /* local conn down — detachClient still scrubs FE state */
    }
    detachClient(origin);
  },
  settingsPanels: () => panelsSub.value,
  onSettingsPanels: (cb: (panels: PanelDesc[]) => void) => panelsSub.on(cb),
  loadSettingsPanel: (appID: string) => loadSettingsPanel((m) => conn.sendCtrl(m), appID),
  windows: () => windowsSub.value,
  onWindowsChanged: (cb) => windowsSub.on(cb),
  focusWindow(id, origin) {
    // Local raise gives instant visual focus feedback; the router's
    // patch will confirm the z bump moments later.
    const o = origin ?? originForWindow(id);
    raiseLocal(o, id);
    wmSend(o, id, { t: 'window.focus', window_id: id });
  },
  closeWindow(id, origin) {
    wmSend(origin ?? originForWindow(id), id, { t: 'window.close_clicked', window_id: id });
  },
  moveWindow(id, x, y, origin) {
    wmSend(origin ?? originForWindow(id), id, { t: 'window.move', window_id: id, x, y });
  },
  resizeWindow(id, w, h, origin) {
    wmSend(origin ?? originForWindow(id), id, { t: 'window.resize', window_id: id, w, h });
  },
  minimizeWindow(id, origin) {
    wmSend(origin ?? originForWindow(id), id, { t: 'window.state', window_id: id, state: 'minimized' });
  },
  maximizeWindow(id, origin) {
    wmSend(origin ?? originForWindow(id), id, { t: 'window.state', window_id: id, state: 'maximized' });
  },
  restoreWindow(id, origin) {
    const o = origin ?? originForWindow(id);
    wmSend(o, id, { t: 'window.state', window_id: id, state: 'normal' });
    // Restoring also brings to front + grabs focus.
    wmSend(o, id, { t: 'window.focus', window_id: id });
  },
  viewports: () => ({ perAxis: VIEWPORTS_PER_AXIS }),
  getViewport: () => viewportSub.value,
  setViewport: (vx, vy) => setViewport(vx, vy),
  onViewport: (cb) => viewportSub.on(cb),
  onScreenSize: (cb) => screenSub.on(cb),
  log(level, source, msg, stack) {
    shellLog(level, source, msg, stack);
  },
  // Bare raw API — addresses the LOCAL router. The *For variants below
  // take an origin so a remote app's raw channel (pty, file stream) routes
  // to its own host's connection (docs/REMOTE.md §4); apps that can run
  // remote must use those with their props.origin.
  openRawChannel(channelID, onBytes) {
    return subscribeRaw(LOCAL_ORIGIN, channelID, onBytes);
  },
  writeRaw(channelID, bytes) {
    conn.sendRaw(channelID, bytes);
  },
  rawBufferedAmount() {
    return conn.bufferedAmount();
  },
  openRawChannelFor(origin, channelID, onBytes) {
    return subscribeRaw(origin, channelID, onBytes);
  },
  writeRawFor(origin, channelID, bytes) {
    (clientForOrigin(origin) ?? local).conn.sendRaw(channelID, bytes);
  },
  rawBufferedAmountFor(origin) {
    return (clientForOrigin(origin) ?? local).conn.bufferedAmount();
  },
  clipboardSetText(text) {
    clipboardSub.set({ mime: 'text/plain', text });
    conn.sendCtrl({ t: 'clipboard.set', mime: 'text/plain', text });
  },
  clipboardGetText() {
    // Always round-trip — content set by app BEs before this shell
    // attached isn't in the local mirror. The mirror only serves as
    // the timeout/disconnect fallback.
    const reqID = ++clipboardReqID;
    return new Promise<string>((resolve) => {
      pendingClipboardGets.set(reqID, resolve);
      try {
        conn.sendCtrl({ t: 'clipboard.get', req_id: reqID });
      } catch {
        pendingClipboardGets.delete(reqID);
        resolve(clipboardSub.value.text);
      }
      // WS hiccup safety: resolve with the local mirror rather than
      // hanging a paste forever.
      setTimeout(() => {
        if (pendingClipboardGets.delete(reqID)) resolve(clipboardSub.value.text);
      }, 3000);
    });
  },
  onClipboardChanged(cb) {
    return clipboardSub.on(cb);
  },
};

// Mirror every native copy/cut anywhere in the shell into the wash
// clipboard. This is what makes editor→terminal flow work without
// app-side wiring: Ctrl+C in CodeMirror/TipTap raises a real copy
// event whose selection we can read. The native copy continues to the
// system clipboard untouched. Guarded for the synthetic copies our
// own mirror helper fires (data-wash-clipboard-mirror) so a wash→
// system mirror doesn't echo back as a second set.
document.addEventListener('copy', (ev) => {
  const t = ev.target as HTMLElement | null;
  if (t && t.closest?.('[data-wash-clipboard-mirror]')) return;
  const sel = String(document.getSelection() ?? '');
  if (sel) window.wash.clipboardSetText(sel);
});
document.addEventListener('cut', (ev) => {
  const t = ev.target as HTMLElement | null;
  if (t && t.closest?.('[data-wash-clipboard-mirror]')) return;
  const sel = String(document.getSelection() ?? '');
  if (sel) window.wash.clipboardSetText(sel);
});

// Auto-capture browser errors so they show up server-side.
window.addEventListener('error', (ev: ErrorEvent) => {
  const stack = ev.error && ev.error.stack ? String(ev.error.stack) : '';
  const where = ev.filename ? `${ev.filename}:${ev.lineno}:${ev.colno}` : '';
  shellLog('error', 'shell', `${ev.message}${where ? ' @ ' + where : ''}`, stack);
});
window.addEventListener('unhandledrejection', (ev: PromiseRejectionEvent) => {
  const reason = ev.reason as unknown;
  let msg: string;
  let stack = '';
  if (reason instanceof Error) {
    msg = reason.message;
    stack = reason.stack ?? '';
  } else {
    msg = typeof reason === 'string' ? reason : JSON.stringify(reason);
  }
  shellLog('error', 'shell', 'unhandled rejection: ' + msg, stack);
});

// Mirror every console.* level so app code's traces land server-side
// too — invaluable when debugging an FE flow whose only visible
// symptom is "nothing happened." The original console output is kept
// so the dev tools view is unchanged.
function mirrorConsole(method: 'error' | 'warn' | 'log' | 'info' | 'debug', level: LogLevel): void {
  const orig = (console[method] as (...a: unknown[]) => void).bind(console);
  console[method] = (...args: unknown[]) => {
    orig(...args);
    shellLog(level, 'console', args.map(stringifyArg).join(' '));
  };
}
mirrorConsole('error', 'error');
mirrorConsole('warn', 'warn');
mirrorConsole('log', 'info');
mirrorConsole('info', 'info');
mirrorConsole('debug', 'debug');

function stringifyArg(a: unknown): string {
  if (a instanceof Error) return a.stack ?? a.message;
  if (typeof a === 'string') return a;
  try {
    return JSON.stringify(a);
  } catch {
    return String(a);
  }
}

// window.__washDiag — invoke from devtools (or via console.info pipe)
// to get a snapshot of shell state. Designed for the blank-desktop bug:
// answers "what does the shell think it has?" without devtools open.
interface WashDiagSnapshot {
  loadAgeMs: number;
  visibility: DocumentVisibilityState;
  hasFocus: boolean;
  desktop: { instanceID: string; element: string } | null;
  declaredInstances: Array<{ id: string; element: string; surface: string }>;
  bundleReady: string[];
  windowsCount: number;
  rootChildCount: number;
  desktopHostChildCount: number;
  customElements: Record<string, boolean>;
}
declare global {
  interface Window { __washDiag?: () => WashDiagSnapshot; }
}
window.__washDiag = (): WashDiagSnapshot => {
  const d = desktop();
  const root = document.getElementById('root');
  const deskHost = root?.querySelector('.wash-desktop');
  const probes = [
    'wash-app-session', 'wash-app-files', 'wash-app-terminal',
    'wash-app-about', 'wash-app-packages', 'wash-app-shell',
  ];
  const ce: Record<string, boolean> = {};
  for (const tag of probes) ce[tag] = customElements.get(tag) != null;
  const snap: WashDiagSnapshot = {
    loadAgeMs: Math.round(performance.now() - __washLoadT0),
    visibility: document.visibilityState,
    hasFocus: document.hasFocus(),
    desktop: d ? { instanceID: d.instanceID, element: d.element } : null,
    declaredInstances: [...instances.entries()].map(([id, v]) => ({ id, element: v.element, surface: v.surface })),
    bundleReady: [...bundleReady.keys()],
    windowsCount: windows.length,
    rootChildCount: root?.children.length ?? -1,
    desktopHostChildCount: deskHost?.children.length ?? -1,
    customElements: ce,
  };
  console.info(`[wash-diag] snapshot: ${JSON.stringify(snap)}`);
  return snap;
};
