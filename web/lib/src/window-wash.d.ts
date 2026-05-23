// Ambient declaration for `window.wash` — the shell-provided API
// every wash web app uses to talk back to the router. Living in
// @wash/ui means importing `@wash/ui` in any app picks up the type
// automatically; apps no longer redeclare partial versions of this.
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
}

interface Window {
  wash: WashGlobals;
}
