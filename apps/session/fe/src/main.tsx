// wash-app-session: the wash desktop chrome. Renders the desktop
// background, a bottom taskbar (start menu / open-window list /
// screenshot button / clock), the start menu, the command palette,
// and the screenshot capture flow. Bridges launcher clicks back to
// the BE half via app_msg.
//
// Solid drives the UI; ad-hoc state (which menu is open, palette
// input, screenshot status, etc.) lives in signals instead of class
// fields scattered across multiple render methods.

import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Menu, MenuItem, defineWashApp, tokens, washAssetUrl } from '@wash/ui';
import { toBlob } from 'html-to-image';
import { Camera, Search, PanelRightOpen } from 'lucide-solid';
import { Sidebar, type SidebarMode } from './sidebar/Sidebar';
import { Section, type SectionState } from './sidebar/Section';
import { ViewportWidget } from './sidebar/ViewportWidget';
import { AboutWidget, type AboutHostStats } from './sidebar/AboutWidget';
import { NotifyWidget, type NotifyEntry } from './sidebar/NotifyWidget';
import { BulkWidget, type BulkJob } from './sidebar/BulkWidget';
import { BulkConflictOverlay, type BulkConflict } from './sidebar/BulkConflictOverlay';
import { PrivWidget, type PrivReq } from './sidebar/PrivWidget';
import { PrivUnlockOverlay, type PrivUnlockState } from './sidebar/PrivUnlockOverlay';

interface CatalogApp {
  id: string;
  name: string;
  icon?: string;
  /** Brand color for the launcher icon; falls back to a hash of id. */
  accent?: string;
  surface: string;
  instancing: string;
  disabled?: boolean;
  reason?: string;
  root_variant?: {
    name?: string;
    icon?: string;
    args?: string[];
  };
}

interface WindowInfo {
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

// DesktopConfigMsg mirrors the BE's desktop.config app_msg. Bytes
// arrive as base64 — the router CBOR→JSON normalizer encodes byte
// strings that way (see internal/router/app_session.go toJSON).
interface DesktopConfigMsg {
  kind: 'desktop.config';
  wallpaper: {
    mode?: 'cover' | 'contain' | 'tile' | 'center' | '';
    fallback_color?: string;
    mime?: string;
    bytes?: string | null;
  };
  clock: {
    format?: '12h' | '24h' | '';
    show_seconds?: boolean;
  };
  taskbar: {
    position?: 'top' | 'bottom' | '';
  };
}

// SystemInfoMsg mirrors the BE's system.info app_msg. Sent once on
// session ready (plus on each desktop.request) and rendered by the
// top-left banner.
//
// `interfaces` is the grouped form: one entry per real-looking NIC,
// each carrying that interface's IPv4 + (deduped) IPv6 addresses.
// The BE drops empty interfaces; FE rendering is a row per group.
interface IfaceIPs {
  name: string;
  ips: string[];
}

// RouterInfo mirrors the BE's wash-router build identity. version is
// always present; commit / built come from the Go binary's
// debug.BuildInfo (populated by `go build`/`go install` from VCS
// when possible); dev is true when --dev was passed at router start.
interface RouterInfo {
  version: string;
  commit?: string;
  built?: string;
  dev?: boolean;
}

interface SystemInfoMsg {
  kind: 'system.info';
  hostname: string;
  fqdn: string;
  username: string;
  cpus: number;
  mem_bytes: number;
  interfaces: IfaceIPs[];
  router?: RouterInfo;
  /** wash-router --name, surfaced in the desktop banner so the
   *  user can tell which session this tab is attached to. */
  session_name?: string;
}

// ROOT_PREFIX — synthetic-id prefix for the "run as root" launcher
// rows. Apps that declare manifest.root_variant produce a derived
// entry "<ROOT_PREFIX><app_id>"; clicking it ships the source app id
// plus the manifest-supplied args to the session BE.
const ROOT_PREFIX = '__root:';

// isRootEntryID is the inverse: extract the source app id from a
// synthetic root row id, or '' if it isn't one.
function rootSourceID(syntheticID: string): string {
  return syntheticID.startsWith(ROOT_PREFIX) ? syntheticID.slice(ROOT_PREFIX.length) : '';
}

// rootEntryFor builds the synthetic CatalogApp row that the launcher
// renders for a source app declaring root_variant.
//
// Default name is the source app's name verbatim — the red-tinted
// icon carries the "this runs as root" signal, so duplicating it in
// the label was noise. Default icon is the source app's own icon
// (so root terminal still looks like a terminal, root syslogs like
// syslogs, etc.) tinted red at render time. Apps that want a
// distinct icon override via manifest.root_variant.icon.
function rootEntryFor(src: CatalogApp): CatalogApp | null {
  if (!src.root_variant) return null;
  return {
    id: ROOT_PREFIX + src.id,
    name: src.root_variant.name ?? src.name,
    icon: src.root_variant.icon ?? src.icon,
    surface: src.surface,
    instancing: src.instancing,
    disabled: false,
  };
}

// accentFor returns the brand color for a catalog row. Apps that
// declare `accent` in their manifest get that exact color; the rest
// fall back to a deterministic hue derived from the id so no
// launcher row ends up monochrome. Saturation + lightness are fixed
// so the palette stays in the same visual register as the
// hand-picked accents — only the hue varies.
function accentFor(app: CatalogApp): string {
  if (app.accent) return app.accent;
  let h = 0;
  for (let i = 0; i < app.id.length; i++) {
    h = ((h << 5) - h + app.id.charCodeAt(i)) | 0;
  }
  // Map to 0..359°, fixed S/L for "soft pastel on dark" look.
  return `hsl(${Math.abs(h) % 360} 55% 65%)`;
}

// ROOT_ICON_COLOR — the tint applied to root-row icons in the start
// menu and command palette. Bright enough to stand out on the dark
// menu background but not so loud that the row feels destructive.
// Single source of truth so both rendering sites stay consistent.
const ROOT_ICON_COLOR = '#e26060';

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  // ---- reactive state ----
  const [catalog, setCatalog] = createSignal<CatalogApp[]>(window.wash.catalog());
  const [windows, setWindows] = createSignal<WindowInfo[]>(window.wash.windows());
  // Pager subscribes to viewport + screen size so it can highlight the
  // active cell and scale window outlines correctly when the user
  // resizes the browser.
  const [vp, setVp] = createSignal(window.wash.getViewport());
  const [screen, setScreen] = createSignal({ w: window.innerWidth, h: window.innerHeight });
  const [menuOpen, setMenuOpen] = createSignal(false);
  const [paletteOpen, setPaletteOpen] = createSignal(false);
  const [paletteQuery, setPaletteQuery] = createSignal('');
  const [paletteSelected, setPaletteSelected] = createSignal(0);
  // Desktop config arrives from the BE as desktop.config app_msg
  // (initial push on connect + every fswatch fire). Defaults below
  // = "no config file yet", matching the BE's zero-value reply.
  const [clockFormat, setClockFormat] = createSignal<'12h' | '24h'>('24h');
  const [showSeconds, setShowSeconds] = createSignal(false);
  const [taskbarPosition, setTaskbarPosition] = createSignal<'top' | 'bottom'>('bottom');
  const [clock, setClock] = createSignal(formatClock(clockFormat(), showSeconds()));
  // System info — populated by the BE's system.info app_msg on
  // session ready. Empty defaults render the legacy "wash" placeholder.
  const [sysInfo, setSysInfo] = createSignal<SystemInfoMsg | null>(null);
  const [screenshotStatus, setScreenshotStatus] = createSignal('');
  const [screenshotVisible, setScreenshotVisible] = createSignal(false);
  // Sidebar state. Open-by-default for first session use; persisted
  // via wash:state so a refresh restores the user's preference. The
  // per-section state map is keyed by section id (sidebar/Sidebar.tsx
  // constants); we initialise lazily as sections register.
  const [sidebarMode, setSidebarMode] = createSignal<SidebarMode>('open');
  const [sectionStates, setSectionStates] = createSignal<Record<string, SectionState>>({
    viewport: 'expanded',
    about: 'collapsed',
    notify: 'collapsed',
    bulk: 'collapsed',
    priv: 'collapsed',
  });
  // Host stats (CPU% / mem%) — pushed by the session BE every 5s as
  // a `host.stats` app_msg. Null until the first tick lands.
  const [hostStats, setHostStats] = createSignal<AboutHostStats | null>(null);
  // Notify history — fed by com.wash.notify's StateService snapshot
  // pushes. We subscribe in onMount; updates arrive on wash:msg as a
  // {kind:"state"} envelope coming from the cross-app sender.
  const [notifications, setNotifications] = createSignal<NotifyEntry[]>([]);
  // Bulk queue + active conflicts — fed by com.wash.bulk's StateService.
  // First pending conflict (if any) pops as a screen-anchored overlay.
  const [bulkJobs, setBulkJobs] = createSignal<BulkJob[]>([]);
  const [bulkConflicts, setBulkConflicts] = createSignal<BulkConflict[]>([]);
  // Priv queue + lock state — fed by com.wash.priv's broadcasts.
  // need_password drives the unlock overlay.
  const [privReqs, setPrivReqs] = createSignal<PrivReq[]>([]);
  const [privLocked, setPrivLocked] = createSignal<boolean>(true);
  const [privUnlock, setPrivUnlock] = createSignal<PrivUnlockState | null>(null);
  const [privUnlockErr, setPrivUnlockErr] = createSignal<string>('');
  // persistSidebar is debounced so a flurry of toggles doesn't
  // hammer the BE's save_state path. Matches the wash-edit cadence.
  let persistTimer: number | null = null;
  const persistSidebar = () => {
    if (persistTimer != null) window.clearTimeout(persistTimer);
    persistTimer = window.setTimeout(() => {
      persistTimer = null;
      window.wash.sendAppMsg(props.instance, {
        kind: 'save_state',
        state: { sidebar_mode: sidebarMode(), section_states: sectionStates() },
      });
    }, 300);
  };
  const toggleSidebar = () => {
    setSidebarMode(sidebarMode() === 'open' ? 'hidden' : 'open');
    persistSidebar();
  };
  // Keep --wash-reserved-right in sync with sidebar mode so the
  // shell's window.tsx can shrink maximized windows away from the
  // sidebar — otherwise titlebar controls (close, restore) fall
  // under it. 300 = SIDEBAR_OPEN_WIDTH; 14 = SIDEBAR_TAB_WIDTH.
  createEffect(() => {
    const px = sidebarMode() === 'open' ? 300 : 14;
    document.documentElement.style.setProperty('--wash-reserved-right', `${px}px`);
  });
  // Section helpers. Widgets call autoExpandSection when an event
  // arrives the user might want to see (new bulk job, new priv req,
  // new notification). User can collapse manually; next event re-opens.
  const setSectionState = (id: string, next: SectionState) => {
    setSectionStates({ ...sectionStates(), [id]: next });
    persistSidebar();
  };
  const toggleSection = (id: string) => {
    const cur = sectionStates()[id] ?? 'expanded';
    setSectionState(id, cur === 'expanded' ? 'collapsed' : 'expanded');
  };
  // autoExpandSection is the event-driven open. Widgets call this
  // when a new event arrives that the user probably wants to see
  // (new bulk job, new priv approval, new notification). User
  // collapsing a section after an auto-expand simply re-collapses
  // it; the next event will re-open. v0.1 doesn't model a stronger
  // "mute" — kept the surface minimal until we know the right shape.
  const autoExpandSection = (id: string) => {
    const cur = sectionStates()[id] ?? 'collapsed';
    if (cur === 'expanded') return;
    setSectionState(id, 'expanded');
  };
  // notifyBadge — show the unread count in the section header. Empty
  // string hides the badge (Section's Show guard treats it as falsy).
  const notifyBadge = (): string => {
    const unread = notifications().filter((n) => !n.read).length;
    return unread > 0 ? String(unread) : '';
  };
  // bulkBadge — show the count of in-flight (queued + running) jobs.
  // Terminal-state rows don't count (they're informational, auto-
  // evicting). Conflicts also count — they're blocking work.
  const bulkBadge = (): string => {
    const active = bulkJobs().filter(
      (j) => j.status === 'queued' || j.status === 'running',
    ).length;
    return active > 0 ? String(active) : '';
  };
  // privBadge — count pending approval requests. Empty when the queue
  // is settled (everything either approved-and-done or already
  // rejected — those don't demand the user's attention).
  const privBadge = (): string => {
    const pending = privReqs().filter((r) => r.status === 'queued').length;
    return pending > 0 ? String(pending) : '';
  };
  // privAccent — red trust tint used on the priv section header,
  // matching the (now-removed) priv window's titlebar stripe and the
  // ROOT-flagged window treatment in window.tsx.
  const PRIV_ACCENT = '#e26060';
  let screenshotTimer = 0;
  let currentObjectURL: string | null = null;

  let paletteInputEl: HTMLInputElement | undefined;

  // Derived list of synthetic "root variant" rows — one per catalog
  // app that declares manifest.root_variant. The launcher renders
  // these alongside the normal catalog with a red highlight.
  const rootEntries = createMemo((): CatalogApp[] => {
    return catalog()
      .filter((a) => !a.disabled && a.root_variant)
      .map((a) => rootEntryFor(a))
      .filter((a): a is CatalogApp => a !== null);
  });

  // Filtered palette results. Root rows are mixed into the normal
  // catalog and sorted by name like everything else — the red row
  // already makes them stand out, no pinning needed.
  const paletteResults = createMemo(() => {
    const q = paletteQuery().trim().toLowerCase();
    const apps = [...catalog().filter((a) => !a.disabled), ...rootEntries()];
    apps.sort((a, b) => a.name.localeCompare(b.name));
    if (!q) return apps;
    return apps.filter((a) => a.id.toLowerCase().includes(q) || a.name.toLowerCase().includes(q));
  });

  const launchApp = (appID: string) => {
    window.wash.sendAppMsg(props.instance, { action: 'launch', app_id: appID });
  };

  // launchPick handles both regular catalog rows and synthetic root
  // rows. Routes the latter through the session BE → wash-priv path
  // (queue + approval + password modal + sudo).
  const launchPick = (id: string) => {
    const src = rootSourceID(id);
    if (src) {
      const app = catalog().find((a) => a.id === src);
      const args = app?.root_variant?.args ?? [];
      const msg: Record<string, unknown> = { action: 'spawn_root', app_id: src };
      if (args.length > 0) msg.args = args;
      window.wash.sendAppMsg(props.instance, msg);
      return;
    }
    launchApp(id);
  };

  // ---- desktop config ----

  // applyDesktopConfig pushes wallpaper bytes + mode + fallback color
  // onto the host element's inline style and updates clock/taskbar
  // signals. Object URLs are revoked when they're replaced — the
  // browser keeps the blob alive until the URL is gone, and a long
  // session with many wallpaper changes would otherwise leak memory.
  const applyDesktopConfig = (cfg: DesktopConfigMsg) => {
    const wp = cfg.wallpaper || {};
    const fallback = wp.fallback_color || 'radial-gradient(circle at 30% 20%, #1a1a32 0, #0a0a18 75%)';
    const mode = wp.mode || 'cover';
    let imageCSS: string | null = null;
    let nextURL: string | null = null;
    if (wp.bytes) {
      const bytes = decodeBase64(wp.bytes);
      const blob = new Blob([bytes], { type: wp.mime || 'application/octet-stream' });
      nextURL = URL.createObjectURL(blob);
      imageCSS = `url("${nextURL}")`;
    }
    // Apply to host. Always set fallback as background-color so an
    // image with transparency still shows it; image overlays on top.
    const host = props.host;
    if (imageCSS) {
      host.style.background = `${imageCSS} center/cover no-repeat ${fallback.startsWith('radial-') ? '#0a0a18' : fallback}`;
      host.style.backgroundSize = mode === 'tile' ? 'auto' : mode === 'center' ? 'auto' : mode; // 'cover' | 'contain'
      host.style.backgroundRepeat = mode === 'tile' ? 'repeat' : 'no-repeat';
      host.style.backgroundPosition = 'center';
    } else {
      host.style.background = fallback;
      host.style.backgroundSize = '';
      host.style.backgroundRepeat = '';
      host.style.backgroundPosition = '';
    }
    if (currentObjectURL) URL.revokeObjectURL(currentObjectURL);
    currentObjectURL = nextURL;

    setClockFormat(cfg.clock?.format === '12h' ? '12h' : '24h');
    setShowSeconds(!!cfg.clock?.show_seconds);
    setTaskbarPosition(cfg.taskbar?.position === 'top' ? 'top' : 'bottom');
    setClock(formatClock(clockFormat(), showSeconds()));
  };

  // ---- screenshot ----

  const setStatus = (text: string, hideAfterMs: number) => {
    setScreenshotStatus(text);
    setScreenshotVisible(true);
    if (screenshotTimer) {
      clearTimeout(screenshotTimer);
      screenshotTimer = 0;
    }
    if (hideAfterMs > 0) {
      screenshotTimer = window.setTimeout(() => {
        setScreenshotVisible(false);
        screenshotTimer = 0;
      }, hideAfterMs);
    }
  };

  const captureScreenshot = async () => {
    setStatus('capturing…', 0);
    try {
      const blob = await toBlob(document.documentElement, {
        cacheBust: false,
        pixelRatio: window.devicePixelRatio || 1,
      });
      if (!blob) {
        setStatus('capture failed', 4_000);
        return;
      }
      const resp = await fetch('/screenshot', { method: 'POST', body: blob });
      if (!resp.ok) {
        const msg = await resp.text();
        setStatus(`save failed: ${msg}`, 5_000);
        return;
      }
      const name = (await resp.text()).trim();
      setStatus(`saved ${name}`, 4_000);
    } catch (err) {
      setStatus(`error: ${err instanceof Error ? err.message : String(err)}`, 5_000);
    }
  };

  // ---- palette ----

  const togglePalette = () => {
    if (paletteOpen()) {
      closePalette();
      return;
    }
    setPaletteQuery('');
    setPaletteSelected(0);
    setPaletteOpen(true);
    queueMicrotask(() => paletteInputEl?.focus());
  };

  const closePalette = () => setPaletteOpen(false);

  const launchSelected = () => {
    const apps = paletteResults();
    const app = apps[paletteSelected()];
    if (!app) return;
    closePalette();
    launchPick(app.id);
  };

  const onPaletteKey = (ev: KeyboardEvent) => {
    const apps = paletteResults();
    switch (ev.key) {
      case 'Escape':
        ev.preventDefault();
        closePalette();
        return;
      case 'Enter':
        ev.preventDefault();
        launchSelected();
        return;
      case 'ArrowDown':
        ev.preventDefault();
        if (apps.length > 0) setPaletteSelected((paletteSelected() + 1) % apps.length);
        return;
      case 'ArrowUp':
        ev.preventDefault();
        if (apps.length > 0) {
          setPaletteSelected((paletteSelected() - 1 + apps.length) % apps.length);
        }
        return;
    }
  };

  // ---- start menu ----

  const toggleMenu = () => setMenuOpen(!menuOpen());

  // ---- lifecycle ----

  onMount(() => {
    const offCat = window.wash.onCatalog(setCatalog);
    const offWin = window.wash.onWindowsChanged(setWindows);
    const offVp = window.wash.onViewport(setVp);
    const offScreen = window.wash.onScreenSize(setScreen);

    // BE → FE: desktop.config arrives once at startup and again
    // on every fswatch fire (wash-settings rewrote the file).
    // system.info arrives once at startup; the banner re-renders.
    // host.stats arrives every 5s while the session BE is running.
    // state messages with no kind come from cross-app subscribes
    // (we currently subscribe to com.wash.notify only — others can
    // share the dispatch path when they grow).
    const onMsg = (ev: Event) => {
      const data = (ev as CustomEvent).detail as { kind?: string; state?: { notifications?: NotifyEntry[] } };
      if (!data) return;
      switch (data.kind) {
        case 'desktop.config':
          applyDesktopConfig(data as DesktopConfigMsg);
          return;
        case 'system.info':
          setSysInfo(data as SystemInfoMsg);
          return;
        case 'host.stats':
          setHostStats(data as unknown as AboutHostStats);
          return;
        case 'notify.state': {
          // notify service → session BE forwards StateService payload
          // verbatim under a service-specific kind so the FE doesn't
          // have to disambiguate "state" between services. The state
          // shape is the notify service's public State type —
          // { notifications: [...] }.
          const next = (data.state as { notifications?: NotifyEntry[] })?.notifications ?? [];
          // Auto-expand the notify section when a NEW notification
          // arrives (id we haven't seen) unless the user pinned it
          // closed. Compare ids against the current view.
          const seen = new Set(notifications().map((n) => n.id));
          const fresh = next.find((n) => !seen.has(n.id));
          setNotifications(next);
          if (fresh) {
            autoExpandSection('notify');
          }
          return;
        }
        case 'bulk.state': {
          // wash-bulk's StateService payload: { jobs: [...], conflicts: [...] }.
          const next = data.state as { jobs?: BulkJob[]; conflicts?: BulkConflict[] };
          const nextJobs = next?.jobs ?? [];
          const nextConflicts = next?.conflicts ?? [];
          // Auto-expand the bulk section on a NEW job (id not seen).
          // Cancelled / failed terminal-state additions are still
          // "new" — they're a state change the user might want to see.
          const seenJobs = new Set(bulkJobs().map((j) => j.job_id));
          const fresh = nextJobs.find((j) => !seenJobs.has(j.job_id));
          setBulkJobs(nextJobs);
          setBulkConflicts(nextConflicts);
          if (fresh) {
            autoExpandSection('bulk');
          }
          return;
        }
        case 'priv.state': {
          // wash-priv full snapshot — broadcast on subscribe + every
          // queue change. Shape: { locked: bool, queue: [...] }.
          const next = data.state as { locked?: boolean; queue?: PrivReq[] };
          const nextReqs = next?.queue ?? [];
          const seen = new Set(privReqs().map((r) => r.req_id));
          const fresh = nextReqs.find((r) => !seen.has(r.req_id));
          setPrivReqs(nextReqs);
          setPrivLocked(!!next?.locked);
          if (fresh) {
            autoExpandSection('priv');
          }
          return;
        }
        case 'priv.req.new':
        case 'priv.req.update': {
          // Per-request transitions arrive in addition to the full
          // state push. We can rely on the broadcast for queue
          // shape, but auto-expand on req.new specifically.
          const req = (data as unknown as { req?: PrivReq }).req;
          if (req) {
            const seen = new Set(privReqs().map((r) => r.req_id));
            // Merge: replace the existing entry or append.
            const merged = seen.has(req.req_id)
              ? privReqs().map((r) => (r.req_id === req.req_id ? req : r))
              : [...privReqs(), req];
            setPrivReqs(merged);
            if (data.kind === 'priv.req.new') autoExpandSection('priv');
          }
          return;
        }
        case 'priv.need_password': {
          const m = data as unknown as { be_pubkey?: string; req_id?: string };
          if (m.be_pubkey && m.req_id) {
            setPrivUnlockErr('');
            setPrivUnlock({ be_pubkey: m.be_pubkey, req_id: m.req_id });
            autoExpandSection('priv');
          }
          return;
        }
        case 'priv.unlock_err': {
          const m = data as unknown as { msg?: string; code?: string };
          setPrivUnlockErr(m.msg || m.code || 'unlock failed');
          return;
        }
        case 'priv.unlocked': {
          setPrivLocked(false);
          setPrivUnlock(null);
          setPrivUnlockErr('');
          return;
        }
        case 'priv.locked': {
          setPrivLocked(true);
          setPrivUnlock(null);
          return;
        }
      }
    };
    // Subscribe via the session BE gateways. Shell-originated cross-
    // app sends don't carry a router-attested From, so the services
    // would reject direct sendAppMsgTo. Routing through our own BE
    // makes the sender attestation correct (the router stamps the
    // session instance as From).
    window.wash.sendAppMsg(props.instance, { kind: 'notify_subscribe' });
    window.wash.sendAppMsg(props.instance, { kind: 'bulk_subscribe' });
    window.wash.sendAppMsg(props.instance, { kind: 'priv_subscribe' });
    props.host.addEventListener('wash:msg', onMsg);

    // wash:state restores the persisted sidebar config on (re)mount.
    // Fires once on first attach and again on any browser refresh.
    // Treat absence of fields as "keep the default" so a partial
    // blob from an older client doesn't wipe new settings.
    const onState = (ev: Event) => {
      const s = (ev as CustomEvent).detail as
        | { sidebar_mode?: SidebarMode; section_states?: Record<string, SectionState> }
        | null;
      if (!s) return;
      if (s.sidebar_mode === 'open' || s.sidebar_mode === 'hidden') {
        setSidebarMode(s.sidebar_mode);
      }
      if (s.section_states && typeof s.section_states === 'object') {
        setSectionStates({ ...sectionStates(), ...s.section_states });
      }
    };
    props.host.addEventListener('wash:state', onState);
    // Belt + braces: ask for current state in case the BE's initial
    // push raced our listener install (the SDK runs OnReady before
    // the FE's connectedCallback in some orderings).
    window.wash.sendAppMsg(props.instance, { kind: 'desktop.request' });

    // Outside-click closes the palette. The start menu owns its
    // own dismissal via @wash/ui Menu.
    const onDocMouseDown = (ev: MouseEvent) => {
      const t = ev.target as Node;
      if (paletteOpen()) {
        const root = props.host.querySelector('[data-testid="palette"]');
        if (root && !root.contains(t)) closePalette();
      }
    };

    const onKey = (ev: KeyboardEvent) => {
      if (ev.ctrlKey && (ev.key === ' ' || ev.code === 'Space')) {
        ev.preventDefault();
        togglePalette();
        return;
      }
      // Ctrl+Alt+S toggles the sidebar between open and hidden.
      // Doesn't collide with Ctrl+Alt+Arrows (viewport pan) — the
      // shell-level keymap owns those; this fires for the unbound
      // 'S' key only.
      if (ev.ctrlKey && ev.altKey && !ev.shiftKey && !ev.metaKey && (ev.key === 's' || ev.key === 'S')) {
        ev.preventDefault();
        toggleSidebar();
        return;
      }
    };

    document.addEventListener('mousedown', onDocMouseDown);
    document.addEventListener('keydown', onKey);
    // 30s tick when minutes-only; 1s when seconds are shown.
    const tickClock = () => setClock(formatClock(clockFormat(), showSeconds()));
    let clockId = window.setInterval(tickClock, showSeconds() ? 1_000 : 30_000);
    const offClockSwap = (() => {
      let lastSecs = showSeconds();
      return setInterval(() => {
        if (showSeconds() !== lastSecs) {
          lastSecs = showSeconds();
          clearInterval(clockId);
          clockId = window.setInterval(tickClock, lastSecs ? 1_000 : 30_000);
        }
      }, 1_000);
    })();

    onCleanup(() => {
      offCat();
      offWin();
      offVp();
      offScreen();
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('wash:state', onState);
      // Cooperative unsubscribe via the session BE gateways. Cheap
      // fire-and-forget; gateways forward with proper sender
      // attestation.
      try {
        window.wash.sendAppMsg(props.instance, { kind: 'notify_unsubscribe' });
        window.wash.sendAppMsg(props.instance, { kind: 'bulk_unsubscribe' });
        window.wash.sendAppMsg(props.instance, { kind: 'priv_unsubscribe' });
      } catch {
        /* ignore — connection may already be torn down */
      }
      document.removeEventListener('mousedown', onDocMouseDown);
      document.removeEventListener('keydown', onKey);
      clearInterval(clockId);
      clearInterval(offClockSwap);
      if (screenshotTimer) clearTimeout(screenshotTimer);
      if (currentObjectURL) URL.revokeObjectURL(currentObjectURL);
      if (persistTimer != null) clearTimeout(persistTimer);
    });
  });

  // Taskbar height is fixed at 40px in the styles below; we pass it
  // to the Sidebar so its inset stays accurate even if the constant
  // ever moves to a token.
  const taskbarHeight = 40;

  return (
    <>
      <Banner info={sysInfo} />
      <Sidebar
        mode={sidebarMode()}
        taskbarPos={taskbarPosition()}
        taskbarHeight={taskbarHeight}
        onToggle={toggleSidebar}
      >
        <Section
          id="viewport"
          title="Viewport"
          icon="layout-grid"
          state={sectionStates().viewport ?? 'expanded'}
          onToggle={() => toggleSection('viewport')}
        >
          <ViewportWidget
            renderPager={() => <Pager windows={windows} vp={vp} screen={screen} />}
          />
        </Section>
        <Section
          id="about"
          title="About"
          icon="info"
          state={sectionStates().about ?? 'collapsed'}
          onToggle={() => toggleSection('about')}
        >
          <AboutWidget
            info={() => sysInfo()}
            stats={() => hostStats()}
          />
        </Section>
        <Section
          id="notify"
          title="Notifications"
          icon="bell"
          state={sectionStates().notify ?? 'collapsed'}
          onToggle={() => toggleSection('notify')}
          badge={notifyBadge()}
        >
          <NotifyWidget
            notifications={notifications}
            onMarkRead={(id) => window.wash.sendAppMsg(props.instance, { kind: 'notify_mark_read', id })}
            onClearAll={() => window.wash.sendAppMsg(props.instance, { kind: 'notify_clear_all' })}
          />
        </Section>
        <Section
          id="bulk"
          title="Bulk Ops"
          icon="list-checks"
          state={sectionStates().bulk ?? 'collapsed'}
          onToggle={() => toggleSection('bulk')}
          badge={bulkBadge()}
        >
          <BulkWidget
            jobs={bulkJobs}
            onCancel={(jobID) =>
              window.wash.sendAppMsg(props.instance, { kind: 'bulk_cancel', job_id: jobID })
            }
          />
        </Section>
        <Section
          id="priv"
          title="Privilege"
          icon="shield-check"
          accent={PRIV_ACCENT}
          state={sectionStates().priv ?? 'collapsed'}
          onToggle={() => toggleSection('priv')}
          badge={privBadge()}
        >
          <PrivWidget
            locked={privLocked}
            reqs={privReqs}
            onApprove={(id) =>
              window.wash.sendAppMsg(props.instance, { kind: 'priv_approve', req_id: id })
            }
            onReject={(id, reason) =>
              window.wash.sendAppMsg(props.instance, {
                kind: 'priv_reject',
                req_id: id,
                reason: reason ?? '',
              })
            }
            onLock={() => window.wash.sendAppMsg(props.instance, { kind: 'priv_lock' })}
          />
        </Section>
      </Sidebar>
      <BulkConflictOverlay
        conflict={() => bulkConflicts()[0] ?? null}
        onResolve={(jobID, action) =>
          window.wash.sendAppMsg(props.instance, {
            kind: 'bulk_resolve_conflict',
            job_id: jobID,
            action,
          })
        }
      />
      <PrivUnlockOverlay
        state={privUnlock}
        error={privUnlockErr}
        onUnlock={(req) =>
          window.wash.sendAppMsg(props.instance, {
            kind: 'priv_unlock',
            ciphertext: req.ciphertext,
            fe_pubkey: req.fe_pubkey,
            nonce: req.nonce,
          })
        }
        onCancel={(reqID) => {
          setPrivUnlock(null);
          setPrivUnlockErr('');
          window.wash.sendAppMsg(props.instance, {
            kind: 'priv_reject',
            req_id: reqID,
            reason: 'unlock cancelled',
          });
        }}
      />
      <div style={taskbarPosition() === 'top' ? taskbarStyleTop : taskbarStyle}>
        <IconButton
          title="Apps"
          onClick={toggleMenu}
        >
          <img src={washAssetUrl('wash-logo.svg')} width="20" height="20" alt="wash" style={{ display: 'block' }} />
        </IconButton>
        <IconButton
          testid="palette-open"
          title="Search apps (Ctrl+Space)"
          onClick={togglePalette}
        >
          <Search size={16} />
        </IconButton>
        <div style={separatorStyle} />
        <div style={windowListStyle}>
          <For each={windows()}>{(w) => <WindowPill win={w} />}</For>
        </div>
        <span
          data-testid="screenshot-status"
          style={{ ...screenshotStatusStyle, opacity: screenshotVisible() ? 1 : 0 }}
        >
          {screenshotStatus()}
        </span>
        <IconButton
          testid="screenshot-btn"
          title="Screenshot"
          onClick={(ev) => {
            (ev.currentTarget as HTMLButtonElement).blur();
            void captureScreenshot();
          }}
        >
          <Camera size={17} />
        </IconButton>
        <IconButton
          testid="sidebar-toggle"
          title="Toggle sidebar (Ctrl+Alt+S)"
          onClick={toggleSidebar}
        >
          <PanelRightOpen size={17} />
        </IconButton>
        <span style={clockStyle}>{clock()}</span>
      </div>

      <Show when={menuOpen()}>
        <StartMenu
          apps={catalog()}
          rootRows={rootEntries()}
          onDismiss={() => setMenuOpen(false)}
          onPick={(id) => {
            setMenuOpen(false);
            launchPick(id);
          }}
          onLogout={() => {
            setMenuOpen(false);
            // Top-level navigation hits wash-login's /logout, which
            // clears the cookie and SIGTERMs every per-user router
            // owned by this uid (end_all=true). For single-session
            // users (the common case) that's exactly the desktop
            // "Log out" verb; the picker (M4) adds per-session
            // "End session" granularity. See docs/MULTIUSER.md.
            window.location.href = '/logout?end_all=true';
          }}
          onDisconnect={() => {
            setMenuOpen(false);
            // /logout without query params clears the session cookie
            // but leaves the per-user router running. The browser
            // gets bounced back to the login form; reconnecting later
            // attaches to the same session by name.
            window.location.href = '/logout';
          }}
        />
      </Show>

      <Show when={paletteOpen()}>
        <Palette
          inputRef={(el) => (paletteInputEl = el)}
          query={paletteQuery()}
          onQueryChange={(v) => {
            setPaletteQuery(v);
            setPaletteSelected(0);
          }}
          results={paletteResults()}
          selected={paletteSelected()}
          isRootRowID={(id) => id.startsWith(ROOT_PREFIX)}
          onHover={setPaletteSelected}
          onPick={() => launchSelected()}
          onKey={onPaletteKey}
          onClose={closePalette}
        />
      </Show>
    </>
  );
};

// ---- sub-components ----

// SpriteIcon renders a Lucide icon from the router-served sprite at
// /icons.svg (built by web/shell/build-icons.mjs). The manifest icon
// field is just the lucide name, e.g. "folder".
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
    <use href={washAssetUrl(`icons.svg#${props.name}`)} />
  </svg>
);

// formatMem renders bytes as a short human string like "16 GB" /
// "512 MB". Used by the Banner only; doesn't need binary-vs-decimal
// pedantry — the value is informational.
function formatMem(bytes: number): string {
  if (!bytes) return '?';
  const gb = bytes / (1024 * 1024 * 1024);
  if (gb >= 1) return `${gb.toFixed(gb >= 10 ? 0 : 1)} GB`;
  const mb = bytes / (1024 * 1024);
  return `${Math.round(mb)} MB`;
}

// Banner is the top-left desktop identity block. Renders:
//
//   • host.example.com           (large, FQDN)
//   • alice                      (bold)
//   • 8 cores · 16 GB            (small)
//   • 10.0.0.5  192.168.1.42     (small)
//
// Falls back to a faded "wash" placeholder before the BE's
// system.info message arrives.
const Banner: Component<{ info: () => SystemInfoMsg | null }> = (props) => {
  const info = props.info;

  const placeholderStyle: JSX.CSSProperties = {
    position: 'absolute',
    left: '32px',
    top: '28px',
    font: '600 22px system-ui,sans-serif',
    'letter-spacing': '0.05em',
    opacity: 0.35,
    'pointer-events': 'none',
  };

  return (
    <Show
      when={info()}
      fallback={
        <div style={placeholderStyle} data-testid="desktop-banner-placeholder">
          wash
        </div>
      }
    >
      {(s) => (
        <div
          data-testid="desktop-banner"
          style={{
            position: 'absolute',
            left: '32px',
            top: '24px',
            color: '#eee',
            font: '14px system-ui,sans-serif',
            'pointer-events': 'none',
            'max-width': '480px',
            'line-height': '1.4',
          }}
        >
          <div
            data-testid="desktop-banner-host"
            style={{
              font: '600 22px system-ui,sans-serif',
              'letter-spacing': '0.02em',
              opacity: 0.85,
              'text-shadow': '0 1px 2px rgba(0,0,0,0.6)',
              'word-break': 'break-all',
              display: 'flex',
              'align-items': 'baseline',
              gap: '10px',
              'flex-wrap': 'wrap',
            }}
          >
            <span>{s().fqdn || s().hostname || 'wash'}</span>
            <span
              data-testid="desktop-banner-user"
              style={{
                font: '500 14px system-ui,sans-serif',
                opacity: 0.7,
                'font-weight': 600,
              }}
            >
              {s().username || '?'}
            </span>
            <Show when={s().session_name}>
              <span
                data-testid="desktop-banner-session-name"
                style={{
                  font: '500 12px ui-monospace,Menlo,Consolas,monospace',
                  opacity: 0.55,
                }}
              >
                · {s().session_name}
              </span>
            </Show>
          </div>
          <div
            data-testid="desktop-banner-hw"
            style={{
              'margin-top': '2px',
              font: '12px ui-monospace,Menlo,Consolas,monospace',
              opacity: 0.6,
              'text-shadow': '0 1px 2px rgba(0,0,0,0.6)',
            }}
          >
            {s().cpus || '?'} cores · {formatMem(s().mem_bytes)}
          </div>
          <Show when={s().interfaces && s().interfaces.length > 0}>
            <div
              data-testid="desktop-banner-ifaces"
              style={{
                'margin-top': '2px',
                font: '12px ui-monospace,Menlo,Consolas,monospace',
                opacity: 0.55,
                'text-shadow': '0 1px 2px rgba(0,0,0,0.6)',
              }}
            >
              <For each={s().interfaces}>
                {(iface) => (
                  <div
                    data-testid={`desktop-banner-iface-${iface.name}`}
                    style={{ 'word-break': 'break-all' }}
                  >
                    <span style={{ opacity: 0.7 }}>{iface.name}</span>
                    {' '}
                    {iface.ips.join('  ')}
                  </div>
                )}
              </For>
            </div>
          </Show>
          <Show when={s().router}>
            {(r) => (
              <div
                data-testid="desktop-banner-router"
                style={{
                  'margin-top': '6px',
                  font: '11px ui-monospace,Menlo,Consolas,monospace',
                  opacity: 0.45,
                  'text-shadow': '0 1px 2px rgba(0,0,0,0.6)',
                  display: 'flex',
                  'align-items': 'center',
                  gap: '6px',
                  'flex-wrap': 'wrap',
                }}
              >
                <span>wash-router v{r().version}</span>
                <Show when={r().commit}>
                  <span style={{ opacity: 0.7 }}>{r().commit}</span>
                </Show>
                <Show when={r().built}>
                  <span style={{ opacity: 0.7 }}>{formatBuilt(r().built!)}</span>
                </Show>
                <Show when={r().dev}>
                  <span
                    data-testid="desktop-banner-router-dev"
                    style={{
                      background: '#a04040',
                      color: '#fff',
                      padding: '0 6px',
                      'border-radius': '2px',
                      'font-weight': 700,
                      'letter-spacing': '0.05em',
                      opacity: 1,
                    }}
                  >
                    DEV
                  </span>
                </Show>
              </div>
            )}
          </Show>
        </div>
      )}
    </Show>
  );
};

// formatBuilt trims the wire's ISO-8601 build timestamp down to
// something glanceable in the banner. The BE ships full RFC 3339
// (e.g. 2026-05-23T12:34:56Z); we keep date+hh:mm and drop the rest.
function formatBuilt(s: string): string {
  // Match the leading "YYYY-MM-DDTHH:MM". If that fails, fall back
  // to the full string so a future BE shape change doesn't hide it.
  const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2})/.exec(s);
  if (!m) return s;
  return `${m[1]} ${m[2]}`;
}

// Pager renders the 3x3 virtual-desktop overview. Embedded in the
// sidebar's Viewport widget — fills the section width (no chrome /
// rounded rect; cells size themselves to the available width). Click
// a cell to pan; click a window outline to pan + focus.
const PAGER_GAP = 3;
const PAGER_PAD = 6;

const Pager: Component<{
  windows: () => WindowInfo[];
  vp: () => { vx: number; vy: number };
  screen: () => { w: number; h: number };
}> = (props) => {
  const perAxis = window.wash.viewports().perAxis;
  // SIDEBAR_OPEN_WIDTH is 300px; section body has 10px horizontal
  // padding (Section.tsx) → ~280px of usable width. We compute the
  // cell width from `perAxis` so any future N×N config Just Works.
  const PAGER_USABLE_W = 280 - PAGER_PAD * 2;
  const cellW = () => Math.floor((PAGER_USABLE_W - (perAxis - 1) * PAGER_GAP) / perAxis);
  const cellH = () => {
    const s = props.screen();
    const aspect = s.h / Math.max(1, s.w);
    return Math.max(1, Math.round(cellW() * aspect));
  };
  const panelW = () => perAxis * cellW() + (perAxis - 1) * PAGER_GAP + PAGER_PAD * 2;
  const panelH = () => perAxis * cellH() + (perAxis - 1) * PAGER_GAP + PAGER_PAD * 2;
  const containerStyle = (): JSX.CSSProperties => ({
    position: 'relative',
    width: `${panelW()}px`,
    height: `${panelH()}px`,
    margin: '0 auto',
    padding: `${PAGER_PAD}px`,
    'box-sizing': 'border-box',
    'user-select': 'none',
  });
  const cells = () => {
    const out: { vx: number; vy: number }[] = [];
    for (let y = 0; y < perAxis; y++) {
      for (let x = 0; x < perAxis; x++) out.push({ vx: x, vy: y });
    }
    return out;
  };
  return (
    <div data-testid="pager" style={containerStyle()}>
      <div
        style={{
          position: 'relative',
          width: '100%',
          height: '100%',
        }}
      >
        <For each={cells()}>
          {(c) => {
            // createMemo makes the filter reactive: it re-runs when
            // any of props.windows / props.screen change, and the
            // result is a stable accessor that PagerCell can read.
            // Without this the filter would be a one-shot snapshot taken
            // at child-mount time, so windows that land in the store
            // after the cell mounts (snapshot replay, drags, new
            // spawns) would never show up.
            const visible = createMemo(() => {
              const s = props.screen();
              const cl = c.vx * s.w;
              const cr = (c.vx + 1) * s.w;
              const ct = c.vy * s.h;
              const cb = (c.vy + 1) * s.h;
              return props.windows().filter((w) => {
                if (w.state === 'minimized') return false;
                const wr = w.x + w.w;
                const wb = w.y + w.h;
                return wr > cl && w.x < cr && wb > ct && w.y < cb;
              });
            });
            return (
              <PagerCell
                cell={c}
                cellW={cellW()}
                cellH={cellH()}
                active={props.vp().vx === c.vx && props.vp().vy === c.vy}
                windows={visible()}
                screen={props.screen()}
              />
            );
          }}
        </For>
      </div>
    </div>
  );
};

const PagerCell: Component<{
  cell: { vx: number; vy: number };
  cellW: number;
  cellH: number;
  active: boolean;
  windows: WindowInfo[];
  screen: { w: number; h: number };
}> = (props) => {
  const left = () => props.cell.vx * (props.cellW + PAGER_GAP);
  const top = () => props.cell.vy * (props.cellH + PAGER_GAP);
  const cellStyle = (): JSX.CSSProperties => ({
    position: 'absolute',
    left: `${left()}px`,
    top: `${top()}px`,
    width: `${props.cellW}px`,
    height: `${props.cellH}px`,
    background: props.active ? 'rgba(80,90,180,0.28)' : 'rgba(255,255,255,0.04)',
    border: props.active ? '1.5px solid #6a7adf' : '1px solid #2a2a4a',
    'border-radius': '3px',
    cursor: 'pointer',
    overflow: 'hidden',
    'box-sizing': 'border-box',
  });
  const onCellClick = (ev: MouseEvent) => {
    // Only fire if the click landed on the cell background (not on
    // a window-rect — those have their own handler that
    // stopPropagation()s).
    if (ev.currentTarget !== ev.target) return;
    window.wash.setViewport(props.cell.vx, props.cell.vy);
  };
  return (
    <div
      data-testid={`pager-cell-${props.cell.vx}-${props.cell.vy}`}
      style={cellStyle()}
      onClick={onCellClick}
    >
      <For each={props.windows}>
        {(w) => <PagerWindow win={w} cell={props.cell} cellW={props.cellW} cellH={props.cellH} screen={props.screen} />}
      </For>
    </div>
  );
};

const PagerWindow: Component<{
  win: WindowInfo;
  cell: { vx: number; vy: number };
  cellW: number;
  cellH: number;
  screen: { w: number; h: number };
}> = (props) => {
  // Map a window's global-plane (x,y,w,h) into the pager cell's local
  // coords. The window's center decides its owning cell, but its
  // body may straddle neighbors — clipping at cell overflow:hidden
  // keeps the visual tidy without dropping the rect entirely.
  const rect = () => {
    const s = props.screen;
    const cellOriginX = props.cell.vx * s.w;
    const cellOriginY = props.cell.vy * s.h;
    const scaleX = props.cellW / Math.max(1, s.w);
    const scaleY = props.cellH / Math.max(1, s.h);
    return {
      left: Math.round((props.win.x - cellOriginX) * scaleX),
      top: Math.round((props.win.y - cellOriginY) * scaleY),
      width: Math.max(2, Math.round(props.win.w * scaleX)),
      height: Math.max(2, Math.round(props.win.h * scaleY)),
    };
  };
  const style = (): JSX.CSSProperties => {
    const r = rect();
    return {
      position: 'absolute',
      left: `${r.left}px`,
      top: `${r.top}px`,
      width: `${r.width}px`,
      height: `${r.height}px`,
      background: props.win.focused ? 'rgba(120,140,240,0.55)' : 'rgba(200,210,240,0.18)',
      border: `1px solid ${props.win.focused ? '#a0b0ff' : '#5a6090'}`,
      'border-radius': '1.5px',
      'box-sizing': 'border-box',
      cursor: 'pointer',
    };
  };
  const onClick = (ev: MouseEvent) => {
    ev.stopPropagation();
    window.wash.setViewport(props.cell.vx, props.cell.vy);
    if (props.win.state === 'minimized') window.wash.restoreWindow(props.win.windowID);
    else window.wash.focusWindow(props.win.windowID);
  };
  return (
    <div
      data-testid={`pager-window-${props.win.windowID}-${props.cell.vx}-${props.cell.vy}`}
      style={style()}
      onClick={onClick}
      title={props.win.title}
    />
  );
};

const IconButton: Component<{
  title: string;
  testid?: string;
  ref?: (el: HTMLButtonElement) => void;
  onClick: (ev: MouseEvent) => void;
  children: JSX.Element;
}> = (props) => {
  const [hover, setHover] = createSignal(false);
  return (
    <button
      type="button"
      title={props.title}
      data-testid={props.testid}
      ref={props.ref}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      onClick={props.onClick}
      style={{
        background: hover() ? 'rgba(255,255,255,0.08)' : 'transparent',
        color: '#eee',
        border: '1px solid transparent',
        width: '32px',
        height: '32px',
        'border-radius': '4px',
        cursor: 'pointer',
        display: 'flex',
        'align-items': 'center',
        'justify-content': 'center',
        'flex-shrink': 0,
      }}
    >
      {props.children}
    </button>
  );
};

const WindowPill: Component<{ win: WindowInfo }> = (props) => {
  const minimized = () => props.win.state === 'minimized';
  return (
    <button
      type="button"
      title={`${minimized() ? '[minimized] ' : ''}${props.win.title} — dblclick to jump to its viewport, right-click to close`}
      onClick={() => {
        if (props.win.state === 'minimized') window.wash.restoreWindow(props.win.windowID);
        else window.wash.focusWindow(props.win.windowID);
      }}
      onDblClick={() => {
        // Snap the camera to the cell holding this window, then focus
        // (or restore-and-focus if minimized). Single-click already
        // fires first and is idempotent with the dblclick action —
        // both end states converge on "focused & visible".
        const v = props.win.viewport;
        window.wash.setViewport(v.vx, v.vy);
        if (props.win.state === 'minimized') window.wash.restoreWindow(props.win.windowID);
        else window.wash.focusWindow(props.win.windowID);
      }}
      onContextMenu={(ev) => {
        ev.preventDefault();
        window.wash.closeWindow(props.win.windowID);
      }}
      style={{
        background: props.win.focused ? '#33387a' : 'rgba(255,255,255,0.04)',
        color: '#eee',
        border: `1px solid ${props.win.focused ? '#4a4f8d' : 'transparent'}`,
        padding: '0 12px',
        height: '28px',
        'border-radius': '4px',
        cursor: 'pointer',
        'max-width': '220px',
        font: '13px system-ui,sans-serif',
        'flex-shrink': 0,
        opacity: minimized() ? 0.6 : 1,
        'font-style': minimized() ? 'italic' : 'normal',
        display: 'inline-flex',
        'align-items': 'center',
        gap: '6px',
      }}
    >
      <Show when={props.win.icon}>
        <SpriteIcon name={props.win.icon!} size={14} />
      </Show>
      <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>{props.win.title}</span>
    </button>
  );
};

const StartMenu: Component<{
  apps: CatalogApp[];
  rootRows: CatalogApp[];
  onPick: (id: string) => void;
  onDismiss: () => void;
  onLogout: () => void;
  onDisconnect: () => void;
}> = (props) => {
  // Merge synthetic root rows in with the catalog and sort
  // alphabetically. Root rows get a red-tinted icon — that's the
  // only visual signal; everything else (padding, font, hover) is
  // the standard MenuItem shape.
  const items = createMemo(() => {
    const merged = [...props.apps, ...props.rootRows];
    merged.sort((a, b) => a.name.localeCompare(b.name));
    return merged;
  });
  const isRootRow = (id: string) => id.startsWith(ROOT_PREFIX);
  return (
    <Menu
      data-testid="start-menu"
      anchor="bottom-left"
      animation="slide-up"
      zIndex={tokens.zStartMenu}
      onDismiss={props.onDismiss}
      // overflow:hidden clips the one-shot shimmer band to the menu
      // panel so the diagonal sweep can't escape past the rounded
      // corners. Pointer-events on the shimmer div are off so it
      // doesn't intercept clicks on the rows underneath.
      style={{ 'min-width': '240px', padding: '4px', overflow: 'hidden' }}
    >
      <div class="wash-shimmer-sweep" aria-hidden="true" />
      <Show when={items().length > 0} fallback={<div style={emptyStyle}>no apps registered</div>}>
        <For each={items()}>
          {(app) => {
            const root = isRootRow(app.id);
            // Stable data-testid hook for every launcher row so e2e
            // tests can disambiguate without relying on accessible
            // name (root variants share their parent app's name).
            //
            //   non-root: start-menu-<app-id>
            //   root:     start-menu-root-<source-app-id>
            //
            // Legacy wash-term root entry keeps the historic alias
            // "start-menu-root-terminal" for tests that hard-coded it.
            const rowTestid = root
              ? (app.id.slice(ROOT_PREFIX.length) === 'com.wash.term'
                  ? 'start-menu-root-terminal'
                  : `start-menu-root-${app.id.slice(ROOT_PREFIX.length)}`)
              : `start-menu-${app.id}`;
            const iconNode = app.icon ? (
              <span style={{ color: root ? ROOT_ICON_COLOR : accentFor(app), display: 'inline-flex' }}>
                <SpriteIcon name={app.icon} size={20} />
              </span>
            ) : undefined;
            return (
              <MenuItem
                data-testid={rowTestid}
                label={app.name}
                disabled={app.disabled}
                icon={iconNode}
                trailing={
                  app.disabled ? (
                    <span style={{ color: tokens.fgMuted, 'font-size': tokens.fontSizeMd }}>
                      {app.reason ? '· ' + app.reason : '· disabled'}
                    </span>
                  ) : undefined
                }
                onClick={() => props.onPick(app.id)}
              />
            );
          }}
        </For>
      </Show>
      <div
        aria-hidden="true"
        style={{
          height: '1px',
          background: tokens.borderMenu,
          margin: '4px 6px',
        }}
      />
      <MenuItem
        data-testid="start-menu-disconnect"
        label="Disconnect"
        icon={<SpriteIcon name="unplug" size={20} />}
        onClick={() => props.onDisconnect()}
      />
      <MenuItem
        data-testid="start-menu-logout"
        label="Log out"
        icon={<SpriteIcon name="log-out" size={20} />}
        onClick={() => props.onLogout()}
      />
    </Menu>
  );
};

const Palette: Component<{
  inputRef: (el: HTMLInputElement) => void;
  query: string;
  onQueryChange: (v: string) => void;
  results: CatalogApp[];
  selected: number;
  isRootRowID: (id: string) => boolean;
  onHover: (i: number) => void;
  onPick: () => void;
  onKey: (ev: KeyboardEvent) => void;
  onClose: () => void;
}> = (props) => {
  return (
    <div
      data-testid="palette"
      onClick={(ev) => {
        if (ev.currentTarget === ev.target) props.onClose();
      }}
      style={{
        position: 'absolute',
        inset: 0,
        display: 'flex',
        'align-items': 'flex-start',
        'justify-content': 'center',
        'padding-top': '14vh',
        background: 'rgba(0,0,0,0.35)',
        'z-index': 10002,
        animation: 'wash-fade-in 120ms ease-out',
      }}
    >
      <div
        style={{
          background: '#181828',
          border: '1px solid #2a2a4a',
          'border-radius': '8px',
          'min-width': '380px',
          'max-width': '520px',
          'box-shadow': '0 16px 48px rgba(0,0,0,0.6)',
          overflow: 'hidden',
          animation: 'wash-pop-in 140ms ease-out',
        }}
      >
        <input
          type="text"
          placeholder="Search apps…"
          data-testid="palette-input"
          ref={props.inputRef}
          value={props.query}
          onInput={(e) => props.onQueryChange(e.currentTarget.value)}
          onKeyDown={props.onKey}
          style={{
            width: '100%',
            'box-sizing': 'border-box',
            padding: '14px 16px',
            background: 'transparent',
            color: '#eee',
            border: 'none',
            'border-bottom': '1px solid #2a2a4a',
            outline: 'none',
            font: '15px system-ui,sans-serif',
          }}
        />
        <div data-testid="palette-list" style={{ 'max-height': '50vh', overflow: 'auto' }}>
          <Show
            when={props.results.length > 0}
            fallback={<div style={emptyStyle}>no matches</div>}
          >
            <For each={props.results}>
              {(app, i) => (
                <PaletteRow
                  app={app}
                  selected={i() === props.selected}
                  isRoot={props.isRootRowID(app.id)}
                  onHover={() => props.onHover(i())}
                  onPick={props.onPick}
                />
              )}
            </For>
          </Show>
        </div>
      </div>
    </div>
  );
};

const PaletteRow: Component<{
  app: CatalogApp;
  selected: boolean;
  isRoot?: boolean;
  onHover: () => void;
  onPick: () => void;
}> = (props) => {
  let el!: HTMLButtonElement;
  // Scroll into view when selection lands on this row.
  onMount(() => {
    if (props.selected) el.scrollIntoView({ block: 'nearest' });
  });
  return (
    <button
      type="button"
      data-testid={`palette-item-${props.app.id}`}
      ref={el!}
      onMouseEnter={props.onHover}
      onClick={props.onPick}
      style={{
        display: 'flex',
        'align-items': 'center',
        gap: '10px',
        width: '100%',
        padding: '8px 16px',
        background: props.selected ? '#2a2a4a' : 'transparent',
        color: '#eee',
        border: 'none',
        'text-align': 'left',
        cursor: 'pointer',
        font: '14px system-ui,sans-serif',
      }}
    >
      <span
        style={{
          width: '20px',
          height: '20px',
          'flex-shrink': 0,
          display: 'inline-flex',
          'align-items': 'center',
          'justify-content': 'center',
          // Root entries get the same red tint as in the start menu;
          // the icon is the only signal — row chrome is unchanged.
          color: props.isRoot ? ROOT_ICON_COLOR : undefined,
        }}
      >
        <Show when={props.app.icon}>
          <SpriteIcon name={props.app.icon!} size={18} />
        </Show>
      </span>
      <span style={{ flex: 1 }}>{props.app.name}</span>
      <span style={{ opacity: 0.55, 'font-size': '12px' }}>{props.app.id}</span>
    </button>
  );
};

function formatClock(format: '12h' | '24h', showSeconds: boolean): string {
  const opts: Intl.DateTimeFormatOptions = {
    hour: '2-digit',
    minute: '2-digit',
    hour12: format === '12h',
  };
  if (showSeconds) opts.second = '2-digit';
  return new Date().toLocaleTimeString([], opts);
}

// decodeBase64 returns a Uint8Array from the router's base64 string
// form of CBOR byte data (see internal/router/app_session.go toJSON).
function decodeBase64(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// ---- styles ----

const taskbarStyle: JSX.CSSProperties = {
  position: 'absolute',
  left: 0,
  right: 0,
  bottom: 0,
  height: '40px',
  background: 'rgba(15,15,30,0.85)',
  'backdrop-filter': 'blur(10px)',
  '-webkit-backdrop-filter': 'blur(10px)',
  'border-top': '1px solid #2a2a4a',
  display: 'flex',
  'align-items': 'center',
  gap: '4px',
  padding: '0 6px',
  'z-index': 10000,
  'box-sizing': 'border-box',
};

// Top-anchored variant: same chrome, bottom-border instead of top-
// border so the separating line still sits between bar and content.
const taskbarStyleTop: JSX.CSSProperties = {
  ...taskbarStyle,
  bottom: undefined,
  top: 0,
  'border-top': undefined,
  'border-bottom': '1px solid #2a2a4a',
};

const separatorStyle: JSX.CSSProperties = {
  width: '1px',
  height: '22px',
  background: '#2a2a4a',
  margin: '0 4px',
  'flex-shrink': 0,
};

const windowListStyle: JSX.CSSProperties = {
  flex: 1,
  display: 'flex',
  'align-items': 'center',
  gap: '4px',
  'overflow-x': 'auto',
  'overflow-y': 'hidden',
  'scrollbar-width': 'none',
};

const screenshotStatusStyle: JSX.CSSProperties = {
  'font-size': '12px',
  transition: 'opacity 0.25s',
  color: '#9aa',
  'white-space': 'nowrap',
  'pointer-events': 'none',
};

const clockStyle: JSX.CSSProperties = {
  padding: '0 14px',
  'font-variant-numeric': 'tabular-nums',
  opacity: 0.7,
  'font-size': '13px',
};

const emptyStyle: JSX.CSSProperties = {
  padding: '10px 14px',
  color: '#888',
  'font-size': '13px',
};

// ---- custom element wrapper ----

defineWashApp('wash-app-session', (props) => <App {...props} />, {
  style: 'display:block;position:absolute;inset:0;background:radial-gradient(circle at 30% 20%, #1a1a32 0, #0a0a18 75%);color:#eee;font:14px system-ui,sans-serif;overflow:hidden',
});
