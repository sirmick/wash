// Ambient declaration for `window.wash` — the shell-provided API
// every wash web app uses to talk back to the router. Living in
// @wash/ui means importing `@wash/ui` in any app picks up the type
// automatically, so this is the one declaration apps share.
//
// The implementation lives in web/shell/src/main.tsx; this file is
// the single source of truth for the type.
//
// This file is loaded via the triple-slash reference in index.ts.
// Keeping it as a pure ambient declaration (no `export {}`) means
// the rollup bundler doesn't try to resolve a runtime module here.

// Recipient mirrors wire.Recipient — exactly one field set. AppID
// works only for singleton-instancing apps (router spawns on demand
// when not yet running); InstanceID is a direct address.
type WashRecipient =
  | { app_id: string }
  | { instance_id: string };

interface WashCatalogApp {
  id: string;
  name: string;
  icon?: string;
  surface: 'window' | 'desktop';
  instancing: 'multi' | 'single' | 'singleton';
  disabled?: boolean;
  reason?: string;
  /**
   * When set, the launcher renders an additional synthetic row that
   * spawns the app via wash-priv. Mirrors the BE-side manifest field.
   */
  root_variant?: {
    name?: string;
    icon?: string;
    args?: string[];
  };
}

// WashPanelDesc is one app-supplied settings panel (catalog `panels`
// list). The settings app renders a section per descriptor and calls
// loadSettingsPanel(app_id) to define + mount the element on demand.
interface WashPanelDesc {
  app_id: string;
  section: string;
  element: string;
  icon?: string;
  order?: number;
}

interface WashWindowInfo {
  // Origin (router) the window belongs to; '' / 'local' for this machine,
  // a remote host's origin for a tunnelled window. Pair (origin, windowID)
  // is the only unique window identity — ids are per-router.
  origin: string;
  windowID: number;
  instanceID: string;
  element: string;
  icon?: string;
  title: string;
  focused: boolean;
  // attention: this window's app says it needs the human — an agent
  // blocked on a permission question, a job that stopped. The router
  // clears it as soon as the window is focused, so a pill can pulse for
  // it without the app having to remember to switch it off
  // (docs/AGENT_UX.md N6).
  attention: boolean;
  state: 'normal' | 'minimized' | 'maximized';
  x: number;
  y: number;
  w: number;
  h: number;
  viewport: { vx: number; vy: number };
}

type WashLogLevel = 'error' | 'warn' | 'info' | 'debug';

// Link-health telemetry (docs/QOS.md). The router pushes per-class
// throughput + the session running totals ~1/s; the shell folds in
// FE-derived rates, the WS send-buffer backlog, and reconnect count.
// Per-class arrays are indexed [Interactive, Bulk, Background, Control].
interface WashLinkSnapshot {
  tx_bytes: number[];
  tx_frames: number[];
  queue_full: number[];
  dropped: number[];
  depth_hi: number[];
  depth: number[];
  credit_stalls: number;
  credit_wait_ns: number;
  rx_bytes: number;
  rx_frames: number;
  raw_bytes: number;
  wire_bytes: number;
  display_tx_bytes: number;
  display_tx_frames: number;
}

interface WashLinkHealth {
  live: WashLinkSnapshot;    // current connection (cumulative)
  session: WashLinkSnapshot; // session running totals (banked + live)
  connects: number;
  uptimeMs: number;
  rateDownBps: number; // instant router→browser throughput
  peakDownBps: number;
  rateUpBps: number;   // instant browser→router throughput
  displayBitrateBps: number; // instant display video bitrate, bits/s
  displayPeakBitrateBps: number;
  displayFps: number; // instant display video frames/s
  displayPeakFps: number;
  bufferedAmount: number; // WS send-buffer backlog (browser→router)
  reconnects: number;     // FE-observed reconnects this page-load
  status: 'ok' | 'warn' | 'bad';
}

/**
 * One host's awareness state: service name ("notify" | "bulk" | "priv" |
 * "net" | "audio" | "agent") → that service's latest snapshot. Bodies are
 * opaque at this layer; each rail widget knows its own service's shape.
 */
type WashHostServices = ReadonlyMap<string, unknown>;

/** The merged awareness map: origin → service → snapshot. */
type WashHostgwMap = ReadonlyMap<string, WashHostServices>;

interface WashGlobals {
  sendAppMsg(instanceID: string, data: unknown): void;
  sendAppMsgTo(recipient: WashRecipient, data: unknown): void;
  catalog(): WashCatalogApp[];
  onCatalog(cb: (apps: WashCatalogApp[]) => void): () => void;
  // Fetch a static asset (theme wallpaper, icon, …) from the router's
  // asset FS over the WS bus (the asset.read channel) — transport-
  // agnostic, so it works in the in-browser VM where HTTP/ingress
  // don't. Path is rooted at the asset namespace (e.g.
  // "wallpapers/midnight.svg"); resolves from the runtime drop spot
  // (~/.config/wash/assets) or the embedded chrome.
  fetchAsset(path: string): Promise<{ bytes: Uint8Array; mime: string }>;
  // Remote-host APIs (docs/REMOTE.md §6.1), used by wash-connect.
  // catalogFor returns the apps a connected origin advertises (LOCAL or a
  // remote host reached over an ssh -L tunnel); onRemoteCatalog fires when
  // any remote catalog changes (apps empty on disconnect). launchOn asks
  // the router at `origin` to spawn appID — the only launch path for a
  // remote host, which runs --no-session. attachRemote/detachRemote open
  // and tear down the second connection the supervisor's endpoint points
  // at, compositing the host's windows into this desktop.
  catalogFor(origin: string): WashCatalogApp[];
  onRemoteCatalog(cb: (ev: { origin: string; apps: WashCatalogApp[] }) => void): () => void;
  launchOn(origin: string, appID: string): void;
  // focusOrLaunch is launchOn's door-shaped sibling (docs/AGENT_UX.md N1):
  // raise that host's window for the app if one is open, and only spawn
  // when none is. Sidebar doors and per-host rows use this — clicking
  // "open X on B" four times should show you X on B, not four copies of
  // it. With several open it cycles, newest first.
  focusOrLaunch(origin: string, appID: string): void;
  attachRemote(origin: string, url?: string): void;
  detachRemote(origin: string): void;
  // App-supplied settings panels. loadSettingsPanel fetches+imports the
  // panel bundle so its custom element is defined; the promise resolves
  // once it's mountable. Used by the settings app to host panels other
  // apps supply (see docs/SETTINGS.md).
  settingsPanels(): WashPanelDesc[];
  onSettingsPanels(cb: (panels: WashPanelDesc[]) => void): () => void;
  loadSettingsPanel(appID: string): Promise<void>;
  displayScaleMode(): 'auto' | '1' | '2';
  setDisplayScaleMode(mode: 'auto' | '1' | '2'): 'auto' | '1' | '2';
  windows(): WashWindowInfo[];
  onWindowsChanged(cb: (windows: WashWindowInfo[]) => void): () => void;
  // origin (optional) addresses the intent to a specific router. Window ids
  // are per-router, so two origins routinely share id 1; pass the window's
  // origin (from WashWindowInfo / props.origin) when known so a remote
  // window's drag/focus doesn't land on the same-id local window. Omitted →
  // resolved by bare id against the merged window list.
  focusWindow(id: number, origin?: string): void;
  closeWindow(id: number, origin?: string): void;
  moveWindow(id: number, x: number, y: number, origin?: string): void;
  resizeWindow(id: number, w: number, h: number, origin?: string): void;
  minimizeWindow(id: number, origin?: string): void;
  maximizeWindow(id: number, origin?: string): void;
  restoreWindow(id: number, origin?: string): void;
  // Virtual-desktop viewport API. The shell pans a viewport-sized
  // camera over a VIEWPORTS_PER_AXIS² plane; setViewport switches
  // cells with a CSS transition.
  viewports(): { perAxis: number };
  getViewport(): { vx: number; vy: number };
  setViewport(vx: number, vy: number): void;
  onViewport(cb: (vp: { vx: number; vy: number }) => void): () => void;
  onScreenSize(cb: (s: { w: number; h: number }) => void): () => void;
  // Link-health telemetry (docs/QOS.md): per-class throughput + session
  // running totals + derived rates/health. The desktop info panel + the
  // About screen render it. null until the first link.stats arrives.
  linkStats(): WashLinkHealth | null;
  onLinkStats(cb: (h: WashLinkHealth) => void): () => void;
  // Host-awareness state, merged across hosts (docs/SIDEBAR.md M1): every
  // router runs com.wash.hostgw, which republishes its own host's
  // background-service snapshots; the shell tags each by the origin it
  // arrived from. The right rail reads this to answer "what needs me, and
  // where" for LOCAL and remote hosts alike.
  //
  // Read-only, and full-replace: each push replaces one (origin, service)
  // cell wholesale, so consumers recompute from the snapshot rather than
  // incrementing counters. A host that detaches loses its whole entry.
  // Control is NOT here — it lives in the owning app, reached with
  // launchOn(origin, appID).
  hostgwState(): WashHostgwMap;
  onHostgwState(cb: (m: WashHostgwMap) => void): () => void;
  log(level: WashLogLevel, source: string, msg: string, stack?: string): void;
  openRawChannel(channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
  // interactive=true tags the frame CLASS_INTERACTIVE instead of the
  // default CLASS_BULK — use for latency-sensitive writes (terminal
  // keystrokes) so they don't queue behind another app's bulk traffic and
  // so link-health stats classify them correctly.
  writeRaw(channelID: number, bytes: Uint8Array, interactive?: boolean): void;
  // Origin-scoped raw API (docs/REMOTE.md §4): an app that can run on a
  // remote host routes its raw channels (pty, file stream) to that host's
  // connection via these, passing its props.origin — bare openRawChannel/
  // writeRaw above always address the LOCAL router.
  openRawChannelFor(origin: string, channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
  // Resync (docs/PTY_ROBUST.md, Fix B): register a callback the shell runs
  // when the router asks to reset this channel's terminal before replaying
  // a scrollback snapshot. Returns an unsubscribe fn.
  subscribeResyncFor(origin: string, channelID: number, onResync: () => void): () => void;
  writeRawFor(origin: string, channelID: number, bytes: Uint8Array, interactive?: boolean): void;
  rawBufferedAmountFor(origin: string): number;
  // Sends a zero-byte credit grant for channelID. Harmless no-op on the
  // credit ledger, but it makes the router re-check whether the channel is
  // "behind" and, if so, resync it (docs/PTY_ROBUST.md, Fix B) — a safe
  // self-heal nudge for a terminal that looks stalled under a healthy
  // socket (Fix D). See terminal.tsx's stall watchdog.
  nudgeChannelFor(origin: string, channelID: number): void;
  // Bytes queued in the shell socket's send buffer. A bulk producer
  // streaming over writeRaw (e.g. fm's upload) polls this to pace
  // itself, so it doesn't head-of-line block control frames (like a
  // cancel) behind its data. 0 on transports that don't expose it.
  rawBufferedAmount(): number;
  // Router-held clipboard — the wash-internal clipboard every app
  // shares. Distinct from the browser's system clipboard, which is
  // gesture-gated; the washCopyText helper in @wash/ui bridges the
  // two where the browser allows.
  clipboardSetText(text: string): void;
  clipboardGetText(): Promise<string>;
  onClipboardChanged(cb: (c: { mime: string; text: string }) => void): () => void;
}

interface Window {
  wash: WashGlobals;
}
