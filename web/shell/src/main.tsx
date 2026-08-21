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
import { For, Show, createEffect, createSignal, onCleanup } from 'solid-js';
import type { Component } from 'solid-js';
import { type ConnState } from './ws';
import { CLASS_BULK, CLASS_INTERACTIVE } from './wire';
import { RouterClient, type ClientHandlers } from './router-client';
import {
  type Origin,
  LOCAL_ORIGIN,
  registerClient,
  unregisterClient,
  registerTag,
  clientForInstance,
  clientForOrigin,
  origins,
  parseInstanceId,
  compoundInstanceId,
} from './clients';
import { ModalLayer, registerModal, summonModal, hasModal, forgetModalsFor } from './modal';
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
  deliverResync,
  deliverToInstance,
  forgetVideoChannel,
  replaceSavedStates,
  setSavedState,
  subscribeRaw,
  subscribeResync,
} from './api';
import './wash-app-display';
import { showToast } from './notify';
import { virtioConsoleFactory } from './virtio';
import { bootStep, bootFinish } from './boot';
import { ingestLinkStats, linkHealth, onLinkHealth, noteConnState, type RawLinkStatsMsg, type LinkHealth } from './linkstats';
import {
  HOSTGW_APP_ID,
  dropHostgwOrigin,
  hostgwState,
  ingestHostgwMsg,
  onHostgwState,
  type HostgwMap,
} from './hostgw';
import { pickWindow } from './focus-or-launch';

interface ShellCatalog {
  t: 'catalog';
  apps: CatalogApp[];
  panels?: PanelDesc[];
}

interface ShellAppDeclared {
  t: 'app.declared';
  instance_id: string;
  element: string;
  surface: 'background' | 'desktop' | 'window' | 'modal';
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
  // Client size hints (0/absent = unset). The interactive resize clamps to
  // these so a window can't be dragged below a toolkit's hard minimum or
  // above its maximum. Sourced from the app manifest / guest xdg toplevel.
  min_w?: number;
  min_h?: number;
  max_w?: number;
  max_h?: number;
  // is_root is router-attested (SO_PEERCRED uid==0, or app_id is in
  // the privilege-chain reserved set). When true the WM paints a red
  // stripe + ROOT label on the titlebar. Never set by the app itself.
  is_root?: boolean;
  // chromeless drops the wash titlebar/border so the guest surface
  // draws its own chrome (e.g. Webamp's Winamp UI). Mirrors the app
  // manifest's WindowHints.Chromeless.
  chromeless?: boolean;
  // attention: the owning app asked for the human (EvtWindowAttention).
  // Router-owned — it clears the flag on focus, so the shell only has to
  // render it (docs/AGENT_UX.md N6).
  attention?: boolean;
}

interface ShellSessionSnapshot {
  t: 'session.snapshot';
  windows: SessionWindow[];
  app_state?: Record<string, unknown>;
  shell_id?: string;
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
  // encoding: content-coding of a kind="bundle" channel ('' | 'gzip').
  encoding?: string;
  // size: on-the-wire byte count for a kind="bundle" channel (drives
  // byte-count completion so Bulk-class data isn't truncated by the unbind).
  size?: number;
}

interface ShellChannelUnbind {
  t: 'channel.unbind';
  channel_id: number;
  reason?: string;
}

interface ShellChannelResync {
  t: 'channel.resync';
  channel_id: number;
  window_id: number;
  kind?: string;
}

interface ShellNotify {
  t: 'notify';
  instance_id: string;
  title: string;
  body?: string;
  level?: 'info' | 'warn' | 'error';
  // Sender's own subject key, opaque here (wire.ShellNotify.Key).
  key?: string;
}

// Dev-mode reload signal (LOCAL router only). Carries no payload beyond
// the discriminant.
interface ShellReload {
  t: 'shell.reload';
}

// asset.read / panel.read replies: the shell fetching its OWN assets +
// settings panels from its router (local-only). *.ok opens a raw channel
// carrying the bytes; *.err reports the failure.
interface ShellAssetReadOK {
  t: 'asset.read.ok';
  req_id: number;
  channel_id: number;
  size: number;
  mime?: string;
  // Router-side content-coding of the streamed bytes: absent/'' for
  // identity, 'gzip' when the asset was pre-compressed (wash-fetch.ts
  // inflates). See internal/router/assetcache.go.
  encoding?: string;
}

interface ShellAssetReadErr {
  t: 'asset.read.err';
  req_id: number;
  code: string;
  msg?: string;
}

interface ShellPanelReadOK {
  t: 'panel.read.ok';
  req_id: number;
  channel_id: number;
  size: number;
}

interface ShellPanelReadErr {
  t: 'panel.read.err';
  req_id: number;
  code: string;
  msg?: string;
}

// clipboard.data is the reply to a clipboard.get round-trip (req_id
// echoed); clipboard.changed broadcasts the router-held clipboard.
interface ShellClipboardData {
  t: 'clipboard.data';
  req_id: number;
  mime: string;
  text: string;
}

interface ShellClipboardChanged {
  t: 'clipboard.changed';
  mime: string;
  text: string;
}

// Remote-apps relay attach failed (docs/REMOTE.md): the host is "up" but
// could not be reached/registered.
interface ShellPeerError {
  t: 'peer.error';
  origin: string;
  msg: string;
}

interface ShellSuperseded {
  t: 'shell.superseded';
  msg: string;
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

// ShellCtrlMsg is the discriminated union of every control-plane message
// the shell dispatches on (the `t` field; WIRE.md §8). makeHandlers'
// onCtrl narrows on `msg.t`, so each case sees a fully-typed shape and
// the per-case `as` casts are gone. The transport (CtrlHandler in ws.ts)
// stays `any`; we narrow at this one dispatch point.
type ShellCtrlMsg =
  | ShellCatalog
  | ShellAppDeclared
  | ShellSessionSnapshot
  | ShellSessionPatch
  | ShellAppMsgDeliver
  | ShellNotify
  | ShellAppCrashed
  | ShellReload
  | ShellChannelBind
  | ShellAssetReadOK
  | ShellAssetReadErr
  | ShellPanelReadOK
  | ShellPanelReadErr
  | ShellChannelUnbind
  | ShellChannelResync
  | ShellClipboardData
  | ShellClipboardChanged
  | ShellPeerError
  | ShellSuperseded
  | RawLinkStatsMsg;

// Reactive subs the chrome (mounted via window.wash) listens to.
// catalogSub is the LOCAL router's catalog (drives the launcher).
const catalogSub = new Sub<CatalogApp[]>([]);
const panelsSub = new Sub<PanelDesc[]>([]);
let localShellID = '';

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

// focusInstance raises the window belonging to an app instance, restoring
// it first if it was minimized. Toast click-to-focus uses it: a
// notification names the instance that raised it (ShellNotify carries
// instance_id), and clicking "Claude needs your input" should land on the
// terminal that said so. Silently does nothing for an instance with no
// window (a background service's toast) — the toast still dismisses.
// raiseWindow brings one known window to the front: snap the camera to
// its viewport cell first, because focusing a window one cell over would
// otherwise "work" with nothing visible happening. Same move the taskbar
// pill's dblclick makes.
function raiseWindow(w: WindowInfo): void {
  const cell = viewportFor(w);
  setViewport(cell.vx, cell.vy);
  // restoreWindow raises + focuses on its own; focusWindow for the rest.
  if (w.state === 'minimized') window.wash.restoreWindow(w.windowID, w.origin);
  else window.wash.focusWindow(w.windowID, w.origin);
}

// appIDForWindow resolves a window's app id from the router-attested
// instance→app-id map (app.declared). The app cannot forge it, which is
// what makes it safe to route navigation off.
function appIDForWindow(w: WindowInfo): string {
  const { origin, bare } = parseInstanceId(w.instanceID);
  return clientForOrigin(origin)?.appIDs.get(bare) ?? '';
}

// focusOrLaunch is the one door primitive (docs/AGENT_UX.md N1): raise this
// app's window on that host if it has one, and only launch when it does
// not. Every rail door and roster row that used to call launchOn — and so
// spawned a fresh window on every click — goes through here.
//
// Navigation, not control: this mutates no app state, which is why the
// rail's awareness-only doctrine (docs/SIDEBAR.md §3.2) permits it.
function focusOrLaunch(origin: Origin, appID: string): void {
  const target = pickWindow(
    windowsSub.value.map((w) => ({
      windowID: w.windowID,
      origin: w.origin,
      appID: appIDForWindow(w),
      focused: w.focused,
    })),
    origin,
    appID,
  );
  if (!target) {
    // A modal is summoned, never launched (docs/SIDEBAR.md M4); falls
    // through to a normal launch on a host that has no such modal.
    if (hasModal(origin, appID)) summonModal(origin, appID);
    else window.wash.launchOn(origin, appID);
    return;
  }
  const w = windowsSub.value.find((x) => x.origin === target.origin && x.windowID === target.windowID);
  if (w) raiseWindow(w);
}

// FOCUS_KIND is the cross-app message a toast's activation sends back to
// the app that raised it, when that toast named a subject
// (docs/AGENT_UX.md N2). "You told the desktop this was about K; the
// human has now asked for K."
//
// Sending a keyed notification is a promise to handle this — the shell
// hands the whole gesture over, because only the sender knows whether K
// already has a window, needs one, or lives somewhere else entirely.
const FOCUS_KIND = 'wash.focus';

// activateToast is what clicking a notification does.
//
// Two shapes. A keyed toast goes back to its own app on its own host,
// which resolves the subject (agentd: raise the Agent window showing that
// session, or open one for a detached session). An unkeyed toast keeps
// the original behaviour: focus the window that raised it, falling back
// to opening the app that speaks for a windowless service.
//
// The app id comes from the router-attested app.declared map, never from
// the notification, so a toast can only ever hand its key to the app that
// actually sent it.
function activateToast(instanceID: string, key?: string): void {
  if (key) {
    const { origin, bare } = parseInstanceId(instanceID);
    const client = clientForOrigin(origin);
    const appID = client?.appIDs.get(bare);
    if (client && appID) {
      client.conn.sendCtrl({ t: 'app_msg.send', to: { app_id: appID }, data: { kind: FOCUS_KIND, key } });
      return;
    }
  }
  focusInstance(instanceID);
}

function focusInstance(instanceID: string): void {
  const w = windowsSub.value.find((x) => x.instanceID === instanceID);
  if (!w) {
    // No window to focus. That is the NORMAL case for a background
    // service's toast (docs/SIDEBAR.md M3b): wash-bulk stalls a copy on a
    // conflict and says so, and the thing you need is a file manager on
    // the host the job is running on — which may be a host you have no
    // window open on at all. Falling back to launching the owning app
    // turns an inert toast into the way in.
    launchOwningApp(instanceID);
    return;
  }
  raiseWindow(w);
}

// launchOwningApp opens the app that raised a windowless notification, on
// the host that raised it.
//
// The app id comes from the instance→app-id map the shell records from
// app.declared — the router's word, which the app cannot forge — so a
// service cannot use a toast to make the desktop launch something else.
// Windowed apps whose window has since closed resolve the same way.
function launchOwningApp(instanceID: string): void {
  const { origin, bare } = parseInstanceId(instanceID);
  const client = clientForOrigin(origin);
  const appID = client?.appIDs.get(bare);
  if (!client || !appID) return;
  // A windowless service's toast should land on the app that speaks for
  // it, not on the service — opening com.wash.bulk shows you nothing.
  // notifyOpeners names that app; everything else opens itself, which is
  // the right answer for a windowed app whose window has since closed.
  //
  // Note this deliberately does NOT consult client.instances to detect a
  // background surface: that map skips background apps entirely (they have
  // no element to mount), so the check would silently never fire — it
  // didn't, and the toast launched the service instead of the file
  // manager. The explicit table is the whole test.
  const target = notifyOpeners[appID] ?? appID;
  // focusOrLaunch, not launchOn: a service that toasts twice should take
  // you back to the window the first toast opened, not stack up a second
  // one (docs/AGENT_UX.md N1). It also handles the modal case — a service
  // that fronts a MODAL summons it instead of launching a window, the
  // user-summon half of the anti-phishing rule (docs/SIDEBAR.md M4).
  focusOrLaunch(origin, target);
}

// notifyOpeners maps a windowless service to the app that speaks for it,
// so activating its toast lands somewhere useful. Deliberately a tiny
// explicit table rather than a manifest field, and the reasoning got
// sharper with modals: a field on the SERVICE would let a service choose
// what the desktop launches, and a field on the FACE ("I speak for
// com.wash.priv") would let any app claim the one surface where being
// impersonated matters most. A table in the shell can do neither.
//
// A service that fronts a modal needs no entry: the modal is the service's
// OWN app id, so the ?? fallback resolves it and hasModal summons it.
const notifyOpeners: Record<string, string> = {
  'com.wash.bulk': 'com.wash.fm',
};
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
  onCtrl: (msg: ShellCtrlMsg) => {
    switch (msg.t) {
      case 'catalog': {
        // The local catalog drives the launcher + settings panels. A
        // remote host's catalog is stored per-origin so wash-connect can
        // list "apps you can launch on B" (docs/REMOTE.md §6.1).
        const c = msg;
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
        handleAppDeclared(client, msg);
        break;
      case 'session.snapshot':
        handleSnapshot(client, msg, isLocal);
        // A fresh snapshot from the LOCAL router means this tab just (re)attached
        // as the foreground head, so any prior "opened elsewhere" notice is stale.
        if (isLocal) setSuperseded(null);
        break;
      case 'session.patch':
        handlePatch(client, msg);
        break;
      case 'app_msg.deliver':
        deliverAppMsg(client, msg);
        break;
      case 'notify': {
        // Every attached router's toasts show, host-tinted (docs/SIDEBAR.md
        // M0). A remote router already broadcasts to us — its shell is this
        // one — so nothing new is subscribed here; the frames were simply
        // being dropped. The id is compounded because B names its own bare
        // instance and focusInstance matches against origin-tagged windows.
        const n = msg;
        showToast({
          instanceID: compoundInstanceId(client.origin, n.instance_id),
          title: n.title,
          body: n.body,
          level: n.level,
          origin: client.origin,
          key: n.key,
          onActivate: activateToast,
        });
        break;
      }
      case 'app.crashed':
        handleCrash(client, msg);
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
        const b = msg;
        if (b.kind === 'bundle' && b.instance_id) {
          // Bundle delivery channel — accumulate (per origin) until the
          // byte-count completes the import. encoding tells the accumulator
          // whether to inflate (gzip) first; size drives completion so the
          // Bulk-class data can't be truncated by the Unbind.
          client.bundleReady.set(b.instance_id, beginBundle(b.channel_id, b.instance_id, client.origin, b.encoding, b.size));
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
      case 'channel.resync': {
        // The router wants this terminal reset before it replays a
        // realigned scrollback snapshot (it suppressed live output while
        // the FE was behind, to avoid a torn stream — docs/PTY_ROBUST.md,
        // Fix B). Run the channel's resync callback synchronously, BEFORE
        // the snapshot bytes that follow on the raw channel (same WS
        // message order), so they render into a reset terminal. Dispatch
        // order is only half of it — the subscriber has to keep that order
        // inside its own renderer too, which is why the terminal issues
        // the reset as an in-band escape rather than calling term.reset()
        // (see web/lib/src/terminal.tsx onResync).
        // Logged (was silent): a resync means this channel was suppressed
        // and just recovered — the trail you want when chasing a terminal
        // that "went black" under an otherwise-healthy socket.
        shellLog('info', 'resync', `channel ${msg.channel_id} resync (origin=${client.origin})`);
        deliverResync(client.origin, msg.channel_id);
        break;
      }
      // Link-health telemetry (docs/QOS.md): the LOCAL router's per-class
      // throughput + session totals, ~1/s. Fold in the live WS send-buffer
      // backlog the shell reads here; the desktop info panel + About render
      // it via window.wash.onLinkStats.
      case 'link.stats':
        if (isLocal) ingestLinkStats(msg, conn.bufferedAmount());
        break;
      // asset.read / panel.read are the shell fetching its OWN assets +
      // settings panels from its router — a local-only concern.
      case 'asset.read.ok':
        if (isLocal) handleAssetReadOK(msg);
        break;
      case 'asset.read.err':
        if (isLocal) handleAssetReadErr(msg);
        break;
      case 'panel.read.ok':
        if (isLocal) handlePanelReadOK(msg);
        break;
      case 'panel.read.err':
        if (isLocal) handlePanelReadErr(msg);
        break;
      case 'channel.unbind': {
        const u = msg;
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
        const d = msg;
        const wait = client.pendingClipboardGets.get(d.req_id);
        if (wait) {
          client.pendingClipboardGets.delete(d.req_id);
          wait(d.text);
        }
        break;
      }
      case 'peer.error': {
        // Remote-apps relay attach failed (no registration / dial). Surface it
        // as a toast so a host that's "up" but shows no apps isn't a silent
        // mystery — the auto-reconciler (session FE) re-issues attach on every
        // remote.state, so a transient failure self-heals, but a persistent one
        // is now visible (REVIEW-RECONNECT M4). Still logged to the router.
        if (!isLocal) break;
        const e = msg;
        shellLog('warn', 'conn', `relay attach failed for ${e.origin}: ${e.msg}`);
        showToast({
          instanceID: 'com.wash.remote',
          title: 'Remote host',
          body: `Couldn’t attach ${e.origin}: ${e.msg}`,
          level: 'warn',
        });
        break;
      }
      case 'shell.superseded': {
        // This tab lost the foreground head to a newer connection (another
        // tab/window/device took over the session); its terminals go quiet as
        // their channels migrate. Raise a persistent banner so the tab isn't
        // silently dark (REVIEW-RECONNECT L2). LOCAL only — head is a
        // local-router concept.
        if (!isLocal) break;
        shellLog('warn', 'conn', `superseded: ${msg.msg}`);
        setSuperseded(msg.msg);
        break;
      }
      case 'clipboard.changed': {
        // Cross-host clipboard sync is M5; mirror only the local router's.
        if (!isLocal) break;
        const c = msg;
        clipboardSub.set({ mime: c.mime, text: c.text });
        // Best-effort outward mirror: this change came from somewhere
        // with no browser gesture to ride — an app BE (fm "Copy path"),
        // an X app via wash-display's clipboard bridge, another attached
        // shell — so without this push a native Ctrl+V would paste the
        // system clipboard's STALE text right after a wash-side copy.
        // Chrome permits writeText on a focused document without a
        // gesture; browsers that refuse (Firefox needs transient
        // activation) reject silently and the wash clipboard still wins
        // through washPasteText's fallback. Skipped when unfocused: the
        // user is outside this browser and may be copying there — a
        // deferred overwrite would clobber that.
        if (c.text && document.hasFocus()) {
          void navigator.clipboard?.writeText(c.text).catch(() => { /* no gesture / permission — best effort */ });
        }
        break;
      }
    }
  },
  onRaw: (channelID, bytes, cls) => {
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
    const consumed = deliverRaw(client.origin, channelID, bytes);
    // Replenish the router-side credit window ONLY for Bulk-class frames a
    // real subscriber consumed. Only Bulk frames debit credit router-side
    // (REVIEW-DATAPATH F8), so crediting Interactive replays/resync snapshots
    // would inflate the window indefinitely. And crediting bytes merely PARKED
    // in pendingRaw (no subscriber) would keep the router streaming into an
    // unbounded queue (F7) — grant on real consumption instead.
    if (consumed && cls === CLASS_BULK) {
      client.credit.absorbed(channelID, bytes.length);
    }
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

const DISPLAY_APP_ID = 'com.wash.display';
const DISPLAY_HIDPI_THRESHOLD = 1.5;
const DISPLAY_SCALE_MODE_KEY = 'wash.display.scaleMode';
type DisplayScaleMode = 'auto' | '1' | '2';

function parseDisplayScaleMode(value: unknown): DisplayScaleMode {
  return value === '1' || value === '2' || value === 'auto' ? value : 'auto';
}

function initialDisplayScaleMode(): DisplayScaleMode {
  try {
    return parseDisplayScaleMode(window.localStorage?.getItem(DISPLAY_SCALE_MODE_KEY));
  } catch {
    return 'auto';
  }
}

const [displayDpr, setDisplayDpr] = createSignal(window.devicePixelRatio || 1);
const [displayScaleMode, setDisplayScaleModeSignal] = createSignal<DisplayScaleMode>(initialDisplayScaleMode());
const sentDisplayMetrics = new Map<Origin, string>();

function refreshDisplayDpr(): void {
  const next = window.devicePixelRatio || 1;
  if (Math.abs(next - displayDpr()) > 0.01) setDisplayDpr(next);
}

window.addEventListener('resize', refreshDisplayDpr);
window.visualViewport?.addEventListener('resize', refreshDisplayDpr);

function currentDisplayScale(): 1 | 2 {
  const mode = displayScaleMode();
  if (mode === '1') return 1;
  if (mode === '2') return 2;
  return displayDpr() >= DISPLAY_HIDPI_THRESHOLD ? 2 : 1;
}

function setDisplayScaleMode(mode: unknown): DisplayScaleMode {
  const next = parseDisplayScaleMode(mode);
  setDisplayScaleModeSignal(next);
  try {
    window.localStorage?.setItem(DISPLAY_SCALE_MODE_KEY, next);
  } catch {
    /* localStorage can be blocked; the live setting still applies */
  }
  publishDisplayMetricsToAll();
  return next;
}

function currentDisplayMetrics() {
  const s = screenSize();
  const dpr = displayDpr();
  const scale = currentDisplayScale();
  const cssW = Math.max(1, Math.round(s.w));
  const cssH = Math.max(1, Math.round(s.h));
  (window as unknown as { __washDisplayScale?: number }).__washDisplayScale = scale;
  return {
    kind: 'display.set_metrics',
    css_w: cssW,
    css_h: cssH,
    dpr,
    scale_mode: displayScaleMode(),
    scale,
    w: cssW * scale,
    h: cssH * scale,
  };
}

function publishDisplayMetrics(client: RouterClient): void {
  const metrics = currentDisplayMetrics();
  const key = `${metrics.w}x${metrics.h}@${metrics.scale}:${metrics.css_w}x${metrics.css_h}:${metrics.scale_mode}`;
  if (sentDisplayMetrics.get(client.origin) === key) return;
  sentDisplayMetrics.set(client.origin, key);
  client.conn.sendCtrl({
    t: 'app_msg.send',
    to: { app_id: DISPLAY_APP_ID },
    data: metrics,
  });
}

function publishDisplayMetricsToAll(): void {
  for (const origin of origins()) {
    const client = clientForOrigin(origin);
    if (client) publishDisplayMetrics(client);
  }
}

// Keyboard-layout hint (REVIEW-X11-WAYLAND #13). The FE forwards physical
// KeyboardEvent.code to wash-display, whose keymap defaults to the server
// layout — so a non-US host types wrong chars. Detect the host layout via the
// Keyboard Map API (Chromium) and tell the compositor to match. Conservative:
// only switch on a confident signature (a wrong guess would type wrong chars),
// so unrecognised layouts stay on the server default (us-like).
let detectedLayout: string | null = null; // null = detection not finished
const layoutSentTo = new Set<Origin>();

async function detectKeyboardLayout(): Promise<string> {
  try {
    const kb = (navigator as unknown as { keyboard?: { getLayoutMap?: () => Promise<Map<string, string>> } }).keyboard;
    if (!kb?.getLayoutMap) return '';
    const map = await kb.getLayoutMap();
    const q = map.get('KeyQ'), a = map.get('KeyA'), y = map.get('KeyY');
    if (q === 'a' && a === 'q') return 'fr'; // AZERTY
    if (y === 'z') return 'de'; // QWERTZ (German/Swiss)
    return ''; // unrecognised → keep the server default
  } catch {
    return '';
  }
}

function publishKeymap(client: RouterClient): void {
  if (!detectedLayout || layoutSentTo.has(client.origin)) return;
  layoutSentTo.add(client.origin);
  client.conn.sendCtrl({
    t: 'app_msg.send',
    to: { app_id: DISPLAY_APP_ID },
    data: { kind: 'display.set_keymap', layout: detectedLayout },
  });
}

void detectKeyboardLayout().then((l) => {
  if (!l) return;
  detectedLayout = l;
  for (const origin of origins()) {
    const client = clientForOrigin(origin);
    if (client) publishKeymap(client);
  }
});

function installDisplayMetricsPublisher(client: RouterClient): void {
  client.conn.onState((state) => {
    if (state === 'open') {
      sentDisplayMetrics.delete(client.origin);
      publishDisplayMetrics(client);
      // Re-send the layout too: the compositor may have restarted.
      layoutSentTo.delete(client.origin);
      publishKeymap(client);
    }
  });
}

installDisplayMetricsPublisher(local);
// LOCAL is just another origin for awareness (docs/SIDEBAR.md §3.2(2)): A's
// own hostgw feeds the same merged map B's does. Its state already arrives
// unprompted — every background app autoboots on shell connect — but the
// explicit subscribe is what a REFRESH depends on: the gateway is already
// running by then, so without a catch-up request this tab would show no
// local state until something happened to change it.
installHostgwSubscriber(local);

createEffect(() => {
  screenSize();
  displayDpr();
  displayScaleMode();
  publishDisplayMetricsToAll();
});

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
  installDisplayMetricsPublisher(client);
  installHostgwSubscriber(client);
  publishDisplayMetrics(client);
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
  // An unrecoverable relay desync must detach the origin, not let the
  // RouterClient reconnect into this same dead socket forever (M6). Detaching
  // scrubs B's frozen windows/catalog; the user re-opens via wash-connect
  // (which re-sends peer.attach) rather than us auto-retrying a corrupt link.
  sock.onFatalClose = () => detachClient(origin);
  peerSockets.set(channelID, { origin, sock });
  const client = new RouterClient(origin, () => sock, makeHandlers);
  registerClient(origin, client);
  installDisplayMetricsPublisher(client);
  installHostgwSubscriber(client);
  publishDisplayMetrics(client);
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
  // Awareness state for a host that is gone is not stale, it is wrong —
  // drop the whole cell so no badge outlives its host (SIDEBAR.md §3.2(4)).
  dropHostgwOrigin(origin);
  // Same reasoning for a summoned modal: a blur belonging to a host that
  // is gone would trap the seat behind a question nobody can answer.
  forgetModalsFor(origin);
  sentDisplayMetrics.delete(origin);
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
//
// hostgw is intercepted BEFORE deliverToInstance (docs/SIDEBAR.md M1): it
// is a background app with no element, and deliverToInstance parks
// messages for element-less instances in pendingMessages — so routing its
// state pushes down that path would queue them unboundedly for the life
// of the tab. The intercept keys on the app id the router attested in
// app.declared, never on the payload's shape.
function deliverAppMsg(client: RouterClient, msg: ShellAppMsgDeliver) {
  if (client.appIDs.get(msg.instance_id) === HOSTGW_APP_ID) {
    ingestHostgwMsg(client.origin, msg.data);
    return;
  }
  deliverToInstance(compoundInstanceId(client.origin, msg.instance_id), msg.data);
}

// installHostgwSubscriber asks a router's awareness gateway for its host's
// state, now and on every reconnect.
// Addressed by app id, which resolveRecipient spawns on demand. Sent on
// every transition to 'open', which covers the first attach AND every
// reconnect: snapshots are full-replace, so a redundant subscribe is free,
// whereas a missed one leaves the rail quietly stale — the gateway is
// already running by then and has no reason to re-push on its own.
function installHostgwSubscriber(client: RouterClient): void {
  client.conn.onState((state) => {
    if (state !== 'open') return;
    client.conn.sendCtrl({
      t: 'app_msg.send',
      to: { app_id: HOSTGW_APP_ID },
      data: { kind: 'subscribe' },
    });
  });
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
      attention: w.attention ?? false,
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
  const appID = (msg.manifest as { id?: string } | undefined)?.id ?? '';
  if (appID === DISPLAY_APP_ID) {
    sentDisplayMetrics.delete(client.origin);
    publishDisplayMetrics(client);
  }
  // Record the owning app id for EVERY declared instance, background
  // included — this is how deliverAppMsg recognises chrome-bound traffic
  // from a windowless app (docs/SIDEBAR.md M1). app.declared fires for
  // every instance, and a late-connecting shell is told about the ones
  // already running, so this map is complete for both.
  if (appID) client.appIDs.set(msg.instance_id, appID);
  // Background services have no FE — no bundle, no element, no mount.
  // The shell ignores the rest of the declaration: the BE talks to other
  // apps via cross-app app_msg, and nothing on this side ever needs to
  // address it by element name.
  if ((msg.surface as string) === 'background') {
    return;
  }
  client.instances.set(msg.instance_id, { element: msg.element, surface: msg.surface });
  if ((msg.surface as string) === 'modal') {
    // Registered, not shown. A modal boots with the session so it is
    // READY when an escalation lands, but it paints only on summon —
    // that rule is what makes an unbidden prompt provably a forgery
    // (docs/SIDEBAR.md M4). The bundle is fetched lazily on first
    // summon, via the same waitForBundle the window path uses.
    if (appID) registerModal({ origin: client.origin, instanceID: msg.instance_id, element: msg.element, appID });
    return;
  }
  if (msg.surface === 'desktop') {
    // Only the LOCAL router owns the desktop chrome; a remote host's
    // desktop surface is ignored (we composite its windows, not its shell).
    if (client.origin !== LOCAL_ORIGIN) return;
    bootStep('desktop', 'loading desktop…', 'active');
    client
      .waitForBundle(msg.instance_id)
      .then(() => {
        mountDesktop({ instanceID: msg.instance_id, element: msg.element });
        // The session app paints the wallpaper next; the boot splash waits
        // for its wash:desktop-painted signal (listener below) before it
        // tears down, so the user never sees a bare desktop mid-load.
        bootStep('desktop', 'rendering wallpaper…', 'active');
      })
      .catch((err) => console.error('wash: desktop bundle:', err));
  }
  // surface=window apps mount on their session.window upsert; the
  // window-create path awaits the bundle promise the same way.
}

// handleSnapshot rebuilds a router's WM state from its canonical view.
// Sent on connect/reconnect. The app_state cache is replaced per-origin so
// stale entries from no-longer-running instances don't linger.
function handleSnapshot(client: RouterClient, msg: ShellSessionSnapshot, isLocal: boolean): void {
  if (isLocal && msg.shell_id) {
    if (localShellID && localShellID !== msg.shell_id) {
      console.info('wash shell: router asset identity changed; reloading page');
      window.location.reload();
      return;
    }
    localShellID = msg.shell_id;
  }
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
// Feed connection transitions to the link-health module so the panel can
// report how many times the link dropped + recovered this page-load.
conn.onState(noteConnState);

// ---- connection diagnostics wiring (docs/RECONNECT.md) ----
//
// The Conn emits a lifecycle event trail (connect/close/zombie/wake/
// reconnect/lost-input). Forward it to shellLog so the *why* of every drop
// is in the router log + About panel — the data you want when chasing a
// "my laptop closed and wash got stuck" report. lost-input also surfaces in
// the banner (the user's recent keystrokes during the outage were dropped).
const [lostInput, setLostInput] = createSignal<string | null>(null);
conn.onEvent((e) => {
  const level: LogLevel = e.kind === 'zombie' || e.kind === 'lost-input' ? 'warn' : 'info';
  shellLog(level, 'conn', `${e.kind}: ${e.msg}`);
  if (e.kind === 'lost-input') setLostInput(e.msg);
  if (e.kind === 'open') setLostInput(null);
});

// superseded holds the "opened elsewhere" notice: the router tells a still-
// live shell it lost the foreground head to a newer connection, so its
// terminals go quiet (REVIEW-RECONNECT L2). Set by the shell.superseded ctrl
// message; cleared when this tab (re)attaches as head (its own fresh
// session.snapshot) or reconnects.
const [superseded, setSuperseded] = createSignal<string | null>(null);

// connTick drives the banner's live "no contact for Ns / next retry" readout
// while the link is down. Only ticks when not open, so a healthy desktop
// pays nothing.
const [connTick, setConnTick] = createSignal(0);
createEffect(() => {
  if (connState() === 'open') return;
  const h = setInterval(() => setConnTick((n) => n + 1), 1000);
  onCleanup(() => clearInterval(h));
});

// Wake handling: laptop-suspend often freezes the WS without delivering a
// close, so on resume the page can believe it is still connected over a dead
// socket. These browser signals all mean "we may have just resumed" — hand
// them to Conn, which probes liveness fast (open) or skips the backoff and
// redials now (down). See docs/RECONNECT.md.
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') conn.wake('visible');
  });
}
if (typeof window !== 'undefined') {
  window.addEventListener('online', () => conn.wake('online'));
  window.addEventListener('offline', () => shellLog('warn', 'conn', 'browser reports network offline'));
  // pageshow with persisted=true is a bfcache restore — the socket is
  // certainly stale. Treat any pageshow as a wake.
  window.addEventListener('pageshow', () => conn.wake('pageshow'));
}

// Remote (peer) relay channels do NOT survive a local reconnect: the router
// tears down every ssh -L'd peer socket when the browser's shell session
// ends (closeAllPeers), and can't tell us over the by-then-dead socket. So
// on a *reconnect* (open after we'd been down) the stale peer RouterClients
// point at defunct channel ids. Scrub them and re-issue peer.attach over the
// fresh connection so remote windows self-heal instead of going dead.
let connHadOpen = false;
conn.onState((s) => {
  if (s !== 'open') return;
  if (!connHadOpen) { connHadOpen = true; return; } // first connect: nothing to reattach
  reattachPeersAfterReconnect();
});

function reattachPeersAfterReconnect(): void {
  if (peerSockets.size === 0) return;
  const origins = new Set<Origin>();
  for (const { origin } of peerSockets.values()) origins.add(origin);
  // Drop the dead relay sockets + their RouterClients (windows/catalog).
  for (const [id, p] of [...peerSockets]) {
    p.sock.close();
    peerSockets.delete(id);
  }
  for (const origin of origins) detachClient(origin);
  // Re-dial each remote over the fresh local connection. handlePeerAttach is
  // idempotent router-side and the host's peer registration outlives the
  // blip (the supervisor app does), so this stands the windows back up.
  for (const origin of origins) {
    shellLog('info', 'conn', `reattach remote origin=${origin} after reconnect`);
    local.conn.sendCtrl({ t: 'peer.attach', origin });
  }
}

// Boot splash (web/shell/src/boot.ts + the #wash-boot overlay in
// index.html). The overlay is already showing "loading shell…" from
// static markup; now that this module has parsed, mark it done and start
// the connect step. The splash is torn down when the session app reports
// the wallpaper actually rendered (wash:desktop-painted), with a 12s
// backstop in index.html so it can never wedge.
bootStep('boot', 'shell loaded', 'done');
bootStep('ws', 'connecting to router…', 'active');
let bootWsSettled = false;
createEffect(() => {
  const s = connState();
  if (s === 'open') {
    if (!bootWsSettled) {
      bootWsSettled = true;
      bootStep('ws', 'connected', 'done');
    }
  } else if (s === 'closed' || s === 'unauthenticated' || s === 'reconnecting') {
    // Lost (or never had) the router — fail the step and drop the splash so
    // the ConnectionBanner's error shows through underneath.
    //
    // 'reconnecting' belongs here too: the splash is a full-screen, opaque,
    // z-index-999999 overlay that swallows pointer events until it is torn
    // down, and it is normally torn down by wash:desktop-painted. If the
    // connection drops BEFORE the desktop paints, that signal never comes —
    // so without this the splash sat on top of the banner for the full 12s
    // backstop, hiding the outage and eating clicks on "Reconnect now"
    // exactly when the user needs it (e2e/tests/reconnect.spec.ts).
    bootStep('ws', s === 'unauthenticated' ? 'session expired' : 'router unreachable', 'fail');
    bootFinish();
  }
});
window.addEventListener(
  'wash:desktop-painted',
  () => {
    bootStep('desktop', 'desktop ready', 'done');
    bootFinish();
  },
  { once: true },
);

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
    {/* Above the camera, so the blur covers every window rather than
        riding along with the viewport transform. */}
    <ModalLayer />
    <ConnectionBanner state={connState()} />
  </>
);

// ConnectionBanner shows a status overlay when the WS is anything other
// than open. Lives in the shell (not in an app) because if the WS is down,
// the apps are unreachable anyway. Top-center placement so it's visible
// without covering taskbar or window chrome. While down it shows live
// diagnostics (time since last contact, attempt count) and a "Reconnect
// now" button that skips the backoff — see docs/RECONNECT.md.
const ConnectionBanner: Component<{ state: ConnState }> = (props) => {
  const danger = () => props.state === 'closed' || props.state === 'unauthenticated';
  // Re-read on every connTick so the elapsed-time line counts up live.
  const detail = (): string => {
    connTick();
    if (props.state === 'open') return '';
    const d = conn.diag();
    const parts: string[] = [];
    if (Number.isFinite(d.sinceContactMs) && d.sinceContactMs > 1500) {
      parts.push(`no contact ${Math.round(d.sinceContactMs / 1000)}s`);
    }
    if (d.reconnectAttempts > 0) parts.push(`attempt ${d.reconnectAttempts}`);
    if (!d.online) parts.push('device offline');
    return parts.join(' · ');
  };
  const label = (): string => {
    switch (props.state) {
      case 'connecting': return 'connecting…';
      case 'reconnecting': return 'router unreachable — reconnecting…';
      case 'closed': return 'disconnected';
      case 'unauthenticated':
        return conn.loginRedirect()
          ? 'session expired — redirecting to log in…'
          : 'session expired — reopen your token URL to reconnect';
      default: return '';
    }
  };
  // The button can only help on a transient drop; an expired session needs
  // re-auth, and a clean connect is already in flight.
  const canRetry = () => props.state === 'reconnecting' || props.state === 'closed';
  return (
    <Show when={props.state !== 'open' || lostInput() || superseded()}>
      <div
        data-testid="wash-connection-banner"
        data-state={props.state}
        style={{
          position: 'fixed',
          top: '10px',
          left: '50%',
          transform: 'translateX(-50%)',
          background: danger() ? tokens.bgDanger : tokens.bgDenied,
          color: tokens.fg,
          border: `1px solid ${danger() ? tokens.borderDanger : tokens.borderDenied}`,
          'border-radius': '6px',
          padding: '6px 14px',
          font: tokens.type.textMd,
          'box-shadow': '0 6px 16px rgba(0,0,0,0.5)',
          // Above the boot splash (#wash-boot, z 999999) as well as every
          // window: a connection outage is the one thing that must never be
          // covered by chrome the user then cannot dismiss.
          'z-index': 1000000,
          // Re-enable pointer events so the Reconnect button is clickable;
          // the banner is small + top-center so it doesn't block the desktop.
          'pointer-events': 'auto',
          display: 'flex',
          'align-items': 'center',
          gap: '10px',
          animation: 'wash-fade-in 200ms ease-out',
        }}
      >
        <span style={{ display: 'flex', 'flex-direction': 'column', 'line-height': '1.3' }}>
          <Show when={props.state !== 'open'}><span>{label()}</span></Show>
          <Show when={detail()}>
            <span data-testid="wash-connection-detail" style={{ opacity: '0.75', font: tokens.type.textSm }}>{detail()}</span>
          </Show>
          <Show when={lostInput()}>
            <span data-testid="wash-connection-lost-input" style={{ opacity: '0.85', font: tokens.type.textSm }}>
              ⚠ {lostInput()}
            </span>
          </Show>
          <Show when={superseded()}>
            <span data-testid="wash-connection-superseded" style={{ opacity: '0.9', font: tokens.type.textSm }}>
              ⚠ {superseded()}
            </span>
          </Show>
        </span>
        <Show when={canRetry()}>
          <button
            data-testid="wash-connection-retry"
            onClick={() => conn.reconnectNow()}
            style={{
              font: tokens.type.textSm,
              color: tokens.fg,
              background: 'rgba(255,255,255,0.12)',
              border: `1px solid ${tokens.borderDanger}`,
              'border-radius': '4px',
              padding: '2px 8px',
              cursor: 'pointer',
            }}
          >
            Reconnect now
          </button>
        </Show>
        <Show when={superseded() && props.state === 'open'}>
          <button
            data-testid="wash-connection-use-here"
            onClick={() => location.reload()}
            style={{
              font: tokens.type.textSm,
              color: tokens.fg,
              background: 'rgba(255,255,255,0.12)',
              border: `1px solid ${tokens.borderDenied}`,
              'border-radius': '4px',
              padding: '2px 8px',
              cursor: 'pointer',
            }}
          >
            Use here
          </button>
        </Show>
      </div>
    </Show>
  );
};

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
      // focusOrLaunch is launchOn's door-shaped sibling (docs/AGENT_UX.md
      // N1): raise that host's window for the app if one is open, and only
      // spawn when none is. Sidebar doors and per-host rows use this —
      // clicking "open X on B" four times should show you X on B, not four
      // copies of it. With several open it cycles, newest first.
      focusOrLaunch(origin: string, appID: string): void;
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
      displayScaleMode(): DisplayScaleMode;
      setDisplayScaleMode(mode: DisplayScaleMode): DisplayScaleMode;
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
      // Link-health telemetry (docs/QOS.md): per-class throughput + session
      // running totals + derived rates/health. The desktop info panel + the
      // About screen render it. null until the first link.stats arrives.
      linkStats(): LinkHealth | null;
      onLinkStats(cb: (h: LinkHealth) => void): () => void;
      // Host-awareness state, merged across origins (docs/SIDEBAR.md M1):
      // origin → service → that service's latest snapshot, fed by each
      // host's com.wash.hostgw. Read-only by design — the rail routes
      // attention with this and deep-links to an app for control.
      hostgwState(): HostgwMap;
      onHostgwState(cb: (m: HostgwMap) => void): () => void;
      log(level: 'error' | 'warn' | 'info' | 'debug', source: string, msg: string, stack?: string): void;
      openRawChannel(channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
      // interactive=true tags the frame CLASS_INTERACTIVE instead of the
      // default CLASS_BULK — use for latency-sensitive writes (terminal
      // keystrokes) so they don't queue behind another app's bulk traffic
      // (ws.ts sendRaw) and so link-health stats classify them correctly.
      writeRaw(channelID: number, bytes: Uint8Array, interactive?: boolean): void;
      rawBufferedAmount(): number;
      // Origin-scoped raw API (docs/REMOTE.md §4): route a channel to a
      // specific host's connection so a remote app's pty/file stream isn't
      // mis-routed to (or collided with) the local router. origin comes from
      // the app's props.origin.
      openRawChannelFor(origin: string, channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
      // Resync (docs/PTY_ROBUST.md, Fix B): register a callback the shell
      // runs when the router asks to reset this channel's terminal before
      // replaying a scrollback snapshot. Returns an unsubscribe fn.
      subscribeResyncFor(origin: string, channelID: number, onResync: () => void): () => void;
      writeRawFor(origin: string, channelID: number, bytes: Uint8Array, interactive?: boolean): void;
      rawBufferedAmountFor(origin: string): number;
      // Sends a zero-byte credit grant for channelID — harmless on the
      // ledger, but makes the router re-check/resync a "behind" channel.
      // Self-heal nudge for terminal.tsx's stall watchdog (Fix D).
      nudgeChannelFor(origin: string, channelID: number): void;
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
  // summonModal raises a modal-surface app the user asked for, on the host
  // that owns it. Returns false when that host has no such modal, so the
  // caller can fall back rather than blur the screen over nothing. The
  // modal never appears any other way (docs/SIDEBAR.md M4).
  summonModal(origin: string, appID: string): boolean {
    return summonModal(origin, appID);
  },
  launchOn(origin, appID) {
    const client = clientForOrigin(origin);
    if (!client) {
      console.warn('wash: launchOn unknown origin', origin);
      return;
    }
    client.conn.sendCtrl({ t: 'shell.launch', app_id: appID });
  },
  focusOrLaunch(origin, appID) {
    focusOrLaunch(origin, appID);
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
  displayScaleMode: () => displayScaleMode(),
  setDisplayScaleMode: (mode) => setDisplayScaleMode(mode),
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
  linkStats: () => linkHealth(),
  onLinkStats: (cb) => onLinkHealth(cb),
  hostgwState: () => hostgwState(),
  onHostgwState: (cb) => onHostgwState(cb),
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
  writeRaw(channelID, bytes, interactive) {
    conn.sendRaw(channelID, bytes, interactive ? CLASS_INTERACTIVE : undefined);
  },
  rawBufferedAmount() {
    return conn.bufferedAmount();
  },
  openRawChannelFor(origin, channelID, onBytes) {
    return subscribeRaw(origin, channelID, onBytes);
  },
  subscribeResyncFor(origin, channelID, onResync) {
    return subscribeResync(origin, channelID, onResync);
  },
  writeRawFor(origin, channelID, bytes, interactive) {
    (clientForOrigin(origin) ?? local).conn.sendRaw(channelID, bytes, interactive ? CLASS_INTERACTIVE : undefined);
  },
  nudgeChannelFor(origin, channelID) {
    (clientForOrigin(origin) ?? local).conn.sendCtrl({ t: 'channel.credit', ch: channelID, n: 0 });
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
//
// document.getSelection() doesn't cover <input>/<textarea> selections
// in every browser (Firefox keeps field selections out of the document
// selection entirely), so a focused field's selectionStart/End range is
// read directly — otherwise Ctrl+C in a path bar or rename field leaves
// the wash clipboard stale. Password fields are skipped: their content
// must not leak into a clipboard every app can read.
function nativeCopySelection(): string {
  const el = document.activeElement as HTMLInputElement | HTMLTextAreaElement | null;
  if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')) {
    if ((el as HTMLInputElement).type === 'password') return '';
    const { selectionStart, selectionEnd } = el;
    if (selectionStart != null && selectionEnd != null && selectionEnd > selectionStart) {
      return el.value.slice(selectionStart, selectionEnd);
    }
    return '';
  }
  return String(document.getSelection() ?? '');
}
function mirrorCopyEvent(ev: ClipboardEvent): void {
  const t = ev.target as HTMLElement | null;
  if (t && t.closest?.('[data-wash-clipboard-mirror]')) return;
  const sel = nativeCopySelection();
  if (sel) window.wash.clipboardSetText(sel);
}
document.addEventListener('copy', mirrorCopyEvent);
document.addEventListener('cut', mirrorCopyEvent);

// Mirror every native paste's text into the wash clipboard too. A
// Ctrl+V carries the SYSTEM clipboard (the paste event's clipboardData)
// straight into xterm/CodeMirror; without this fold the wash clipboard
// — and everything that reads it (right-click Paste, X apps via
// wash-display, the sidebar widget) — silently diverges from what the
// user just demonstrably pasted. Skipped when the text already matches
// (the common case now that washPasteText folds system reads itself).
// CAPTURE phase: xterm stopPropagation()s paste events on its textarea,
// so a bubble listener would miss exactly the terminal pastes.
document.addEventListener('paste', (ev) => {
  const t = ev.target as HTMLElement | null;
  if (t && t.closest?.('[data-wash-clipboard-mirror]')) return;
  const text = ev.clipboardData?.getData('text/plain') ?? '';
  if (text && text !== clipboardSub.value.text) window.wash.clipboardSetText(text);
}, { capture: true });

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
  conn: ReturnType<typeof conn.diag>;
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
    conn: conn.diag(),
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

// window.__washDropSocket — test-only handle to force a transient WS drop
// without a page reload, exercising the same-router live-reconnect path
// (reattachChannelsToShell → channel.resync + replay). See the
// term-live-reconnect e2e (REVIEW-RECONNECT H1).
declare global {
  interface Window { __washDropSocket?: () => void; }
}
window.__washDropSocket = (): void => { conn.dropSocketForTest(); };
