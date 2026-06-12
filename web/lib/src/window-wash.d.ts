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
  windowID: number;
  instanceID: string;
  element: string;
  icon?: string;
  title: string;
  focused: boolean;
  state: 'normal' | 'minimized' | 'maximized';
  x: number;
  y: number;
  w: number;
  h: number;
  viewport: { vx: number; vy: number };
}

type WashLogLevel = 'error' | 'warn' | 'info' | 'debug';

interface WashGlobals {
  sendAppMsg(instanceID: string, data: unknown): void;
  sendAppMsgTo(recipient: WashRecipient, data: unknown): void;
  catalog(): WashCatalogApp[];
  onCatalog(cb: (apps: WashCatalogApp[]) => void): () => void;
  // App-supplied settings panels. loadSettingsPanel fetches+imports the
  // panel bundle so its custom element is defined; the promise resolves
  // once it's mountable. Used by the settings app to host panels other
  // apps supply (see docs/SETTINGS.md).
  settingsPanels(): WashPanelDesc[];
  onSettingsPanels(cb: (panels: WashPanelDesc[]) => void): () => void;
  loadSettingsPanel(appID: string): Promise<void>;
  windows(): WashWindowInfo[];
  onWindowsChanged(cb: (windows: WashWindowInfo[]) => void): () => void;
  focusWindow(id: number): void;
  closeWindow(id: number): void;
  moveWindow(id: number, x: number, y: number): void;
  resizeWindow(id: number, w: number, h: number): void;
  minimizeWindow(id: number): void;
  maximizeWindow(id: number): void;
  restoreWindow(id: number): void;
  // Virtual-desktop viewport API. The shell pans a viewport-sized
  // camera over a VIEWPORTS_PER_AXIS² plane; setViewport switches
  // cells with a CSS transition.
  viewports(): { perAxis: number };
  getViewport(): { vx: number; vy: number };
  setViewport(vx: number, vy: number): void;
  onViewport(cb: (vp: { vx: number; vy: number }) => void): () => void;
  onScreenSize(cb: (s: { w: number; h: number }) => void): () => void;
  log(level: WashLogLevel, source: string, msg: string, stack?: string): void;
  openRawChannel(channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
  writeRaw(channelID: number, bytes: Uint8Array): void;
  // Bytes queued in the shell socket's send buffer. A bulk producer
  // streaming over writeRaw (e.g. fm's upload) polls this to pace
  // itself, so it doesn't head-of-line block control frames (like a
  // cancel) behind its data. 0 on transports that don't expose it.
  rawBufferedAmount(): number;
}

interface Window {
  wash: WashGlobals;
}
