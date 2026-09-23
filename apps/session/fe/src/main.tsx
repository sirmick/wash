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
import { createStore, reconcile } from 'solid-js/store';
import {
  AgentAsks,
  Menu,
  MenuItem,
  accentColor,
  applyScheme,
  defineWashApp,
  getPack,
  tokens,
  washAssetUrl,
} from '@wash/ui';
import { PrivWidget, PrivUnlockOverlay } from '@wash/ui';
import type { Pack, PrivReq, PrivUnlockState, RosterAsk, RosterRow } from '@wash/ui';
import { toBlob } from 'html-to-image';
import { Camera, Search, PanelRightOpen } from 'lucide-solid';
import { Sidebar, type SidebarMode } from './sidebar/Sidebar';
import { Section, type SectionState } from './sidebar/Section';
import { ViewportWidget } from './sidebar/ViewportWidget';
import { AboutWidget, type AboutHostStats } from './sidebar/AboutWidget';
import { NotifyWidget, type NotifyEntry } from './sidebar/NotifyWidget';
import { TimelineWidget } from './sidebar/TimelineWidget';
import { mergeNewestFirst, prependLive, type TimelineEntry } from './timeline';

import { NetWidget, type NetState, type NetIface } from './sidebar/NetWidget';
import { RemoteWidget, type RemoteHost } from './sidebar/RemoteWidget';
import { reconcileRemoteAttachments } from './remote-reconcile';
import {
  appMatches,
  paletteEntries,
  pinnedRows,
  recentDir,
  aimingAt,
  recentGroups,
  recordPointer,
  recentMatches,
  recentName,
  recentPathOf,
  rectIsLaid,
  sameKeys,
  stepSelection,
  type AgentRecent,
  type AgentRecentAction,
  type PaletteEntry,
  type PointerSample,
  type RecentEntry,
  type RecentGroup,
  type RecentItem,
} from './launcher';
import { LinkWidget } from './sidebar/LinkWidget';
import { AudioWidget, type AudioState } from './sidebar/AudioWidget';
import { ClipboardWidget } from './sidebar/ClipboardWidget';
import {
  SERVICE_AGENT,
  SERVICE_BULK,
  SERVICE_NET,
  SERVICE_NOTIFY,
  SERVICE_PRIV,
  activeBulkJobs,
  agentHostSummary,
  agentSummary,
  badgeText,
  bulkSummary,
  countBadge,
  netBadge as netBadgeFor,
  netBadgeForHost,
  netSummary,
  notifySummary,
  pendingPrivReqs,
  privSummary,
  remoteHostRows,
  totalCount,
  unreadNotifications,
  waitingAgents,
  AWARENESS_COUNTERS,
  countByHost,
  hostsWithService,
  stateFor,
  LOCAL_ORIGIN,
  sectionForService,
  type HostGroupRow,
  type HostLinkStatus,
  type HostgwMap,
} from './sidebar/awareness';
import { HostGroups } from './sidebar/HostGroups';
import { AgentOpen } from './sidebar/AgentOpen';
import { HostOpen } from './sidebar/HostOpen';
import { hostHue } from './sidebar/host-hue';

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
  // Origin (router) the window belongs to. Pair (origin, windowID) is the
  // only unique identity — ids are per-router — so the pager/taskbar pass
  // it to WM intents to address a remote window's twin-id local sibling
  // correctly (docs/REMOTE.md R2).
  origin: string;
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

// WinRow is a WindowInfo carrying the identity reconcile() matches rows on.
// (origin, windowID) is the only unique window identity — window ids are
// per-router — so the key folds both.
type WinRow = WindowInfo & { key: string };

function keyWindows(wins: WindowInfo[]): WinRow[] {
  return wins.map((w) => ({ ...w, key: `${w.origin}#${w.windowID}` }));
}

// DesktopConfigMsg mirrors the BE's desktop.config app_msg. `bytes` is
// present only for a user's *custom* wallpaper image; it arrives as
// base64 (the router CBOR→JSON normalizer encodes byte strings that
// way, see internal/router/app_session.go toJSON). For the built-in
// default `bytes` is null and we render the bundled SVG instead — the
// default never crosses the wire.
interface DesktopConfigMsg {
  kind: 'desktop.config';
  pack?: string;
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
  /** Friendly CPU architecture label (e.g. "x86-64", "arm64"). */
  arch: string;
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

// accentFor returns the brand color for a catalog row, resolved onto the
// pack's themeable accent palette so launcher icons re-skin with the pack:
// a manifest `accent` (hue name or hex) snaps to the nearest accent token;
// apps without one are hashed deterministically onto the ring by id.
function accentFor(app: CatalogApp): string {
  return accentColor(app.id, app.accent);
}

// ROOT_ICON_COLOR — the tint applied to root-row icons in the start
// menu and command palette. Bright enough to stand out on the dark
// menu background but not so loud that the row feels destructive.
// Single source of truth so both rendering sites stay consistent.
const ROOT_ICON_COLOR = tokens.accentRed;

// describeErr coerces a thrown value into a legible string. html-to-image
// and the DOM can reject with non-Error values (an Event, a failed <img>,
// a plain object) — for those String(err) is the useless "[object Object]".
// Tease out the real cause so the screenshot status + log name it.
function describeErr(err: unknown): string {
  if (err instanceof Error) return err.message || err.name;
  if (typeof err === 'string') return err;
  if (typeof HTMLImageElement !== 'undefined' && err instanceof HTMLImageElement) {
    return `image failed: ${err.src || '(no src)'}`;
  }
  if (err instanceof Event) {
    const t = err.target as { src?: string; href?: string } | null;
    return `${err.type}${t?.src ? ` ${t.src}` : t?.href ? ` ${t.href}` : ''}`;
  }
  if (err && typeof err === 'object') {
    const o = err as Record<string, unknown>;
    if (typeof o.message === 'string' && o.message) return o.message;
    if (typeof o.src === 'string') return `resource failed: ${o.src}`;
    try {
      const s = JSON.stringify(err);
      if (s && s !== '{}') return s;
    } catch {
      /* circular — fall through */
    }
    return (err.constructor && err.constructor.name) || Object.prototype.toString.call(err);
  }
  return String(err);
}

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  // ---- reactive state ----
  const [catalog, setCatalog] = createSignal<CatalogApp[]>(window.wash.catalog());
  // Window rows are RECONCILED, never replaced wholesale. The shell rebuilds
  // the entire WindowInfo array on every wm patch (a focus change, a move, a
  // retitle), so a plain signal handed <For> all-new object references — and
  // <For> keys on reference, so it tore down and rebuilt every taskbar pill on
  // every window event. A rebuilt pill re-creates its <svg><use
  // href="/icons.svg#…">, and an external-document <use> does not paint on the
  // frame it is inserted: the icon blanked for a frame each time, which is the
  // taskbar icon flicker. reconcile keyed on origin#windowID (ids are
  // per-router, so neither half is unique alone) merges field changes into the
  // existing row objects, so the pill's DOM — and its icon — survives and only
  // the changed properties re-render.
  const [winRows, setWinRows] = createStore<{ list: WinRow[] }>({ list: keyWindows(window.wash.windows()) });
  const windows = (): WinRow[] => winRows.list;
  const setWindows = (next: WindowInfo[]) => setWinRows('list', reconcile(keyWindows(next), { key: 'key' }));
  // Pager subscribes to viewport + screen size so it can highlight the
  // active cell and scale window outlines correctly when the user
  // resizes the browser.
  const [vp, setVp] = createSignal(window.wash.getViewport());
  const [screen, setScreen] = createSignal({ w: window.innerWidth, h: window.innerHeight });
  const [menuOpen, setMenuOpen] = createSignal(false);
  const [paletteOpen, setPaletteOpen] = createSignal(false);
  const [paletteQuery, setPaletteQuery] = createSignal('');
  const [paletteSelected, setPaletteSelected] = createSignal(0);
  // Launcher memory, fed by the session BE's launcher.state push
  // (apps/session/be/launcher.go): recent files the router routed to a
  // handler (newest first, missing files already filtered BE-side) and
  // the pinned app ids. Persisted under $XDG_STATE_HOME/wash/recent.json.
  const [recent, setRecent] = createSignal<RecentEntry[]>([]);
  // Right-click menu on a Recent row: {x, y, path} while open. Held at
  // App level (not inside StartMenu) so the start menu's outside-click
  // dismissal can be suppressed while it is up — the context menu is a
  // portal, so to the start menu a click on it looks like "outside".
  // `group` is set when the menu was opened from a flyout: Remove and Clear
  // then act on that app's entries only.
  const [recentMenu, setRecentMenu] = createSignal<{ x: number; y: number; entry: RecentEntry; group?: { id: string; label: string } } | null>(null);
  // Right-click on an APP row: {x, y, appID} while open. Same portal
  // caveat as recentMenu — the start menu must not dismiss under it.
  const [appMenu, setAppMenu] = createSignal<{ x: number; y: number; appID: string } | null>(null);
  // Pinned app ids in pin order, from the same launcher.state push.
  const [pinned, setPinned] = createSignal<string[]>([]);
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
    timeline: 'collapsed',
    bulk: 'collapsed',
    priv: 'collapsed',
    net: 'collapsed',
    remote: 'collapsed',
    audio: 'collapsed',
    agents: 'collapsed',
    clipboard: 'collapsed',
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
  // Priv queue + lock state — fed by com.wash.priv's broadcasts.
  // need_password drives the unlock overlay.
  const [privReqs, setPrivReqs] = createSignal<PrivReq[]>([]);
  const [privGrants, setPrivGrants] = createSignal<string[]>([]);
  const [privLocked, setPrivLocked] = createSignal<boolean>(true);
  const [privUnlock, setPrivUnlock] = createSignal<PrivUnlockState | null>(null);
  const [privUnlockErr, setPrivUnlockErr] = createSignal<string>('');
  // Net — com.wash.netd status snapshot (status/phase/summary/diagnostics),
  // fed by the session BE's net.state forwarder. Null until first push.
  const [netState, setNetState] = createSignal<NetState | null>(null);
  // Remote-host sessions, fed by the session BE's remote.state forwarder
  // (com.wash.remote supervisor). Glanceable list; wash-connect manages.
  const [remoteHosts, setRemoteHosts] = createSignal<RemoteHost[]>([]);
  // attachedRemotes tracks origins this always-alive session FE has asked the
  // shell to attach. wash-connect runs the same reconcile, but only while its
  // window is open; the session FE keeps remote windows re-attaching after an
  // SSH blip even with Connect closed (REVIEW-RECONNECT M4). See
  // remote-reconcile.ts.
  const attachedRemotes = new Set<string>();
  // Bumped on every remote-catalog change so the sidebar's per-host launch
  // dropdowns re-render; the catalogs themselves live in the shell (a
  // connected host stays attached even after wash-connect closes, so
  // window.wash.catalogFor / launchOn work straight from here).
  const [remoteCatVer, setRemoteCatVer] = createSignal(0);
  // Live interface IPs from the session BE's host-stats ticker (host.ifaces).
  const [netIfaces, setNetIfaces] = createSignal<NetIface[]>([]);
  // Coding-agent roster — com.wash.agentd's StateService snapshot
  // (docs/AGENT_TERM.md §7), forwarded by the session BE as agent.state.
  // Rows arrive pre-sorted (needs-input first); we only anchor each row's
  // elapsed clock locally, the way the terminal's own status line does.
  const [agentRows, setAgentRows] = createSignal<RosterRow[]>([]);
  // Pending permission questions (docs/AGENT_TERM.md §12) ride the same
  // roster push. An agent blocked on a human is the one thing in the
  // sidebar worth opening the section for on its own.
  const [agentAsks, setAgentAsks] = createSignal<RosterAsk[]>([]);
  // agentd's session history (State.recent), newest first — the start
  // menu's Agent flyout. Same push as the roster.
  const [agentRecent, setAgentRecent] = createSignal<AgentRecent[]>([]);

  // Audio mixer — com.wash.audio's StateService snapshot (sources +
  // master volume), forwarded by the session BE as audio.state.
  const [audioState, setAudioState] = createSignal<AudioState | null>(null);
  // Link-health telemetry for the desktop info panel (docs/QOS.md). The
  // shell pushes it ~1/s via window.wash.onLinkStats.
  const [link, setLink] = createSignal<WashLinkHealth | null>(window.wash.linkStats?.() ?? null);
  // Merged host-awareness state (docs/SIDEBAR.md M1): origin → service →
  // snapshot, from a com.wash.hostgw on every attached router. This is the
  // SINGLE source for the section badges below, LOCAL included — the same
  // local state also still arrives on the legacy per-service kinds, which
  // the widget BODIES read, so counting both would double every local
  // number. Seeded synchronously in case state landed before we mounted.
  const [hostgw, setHostgw] = createSignal<HostgwMap>(
    window.wash.hostgwState?.() ?? new Map(),
  );
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
  // notifyBadge — unread count in the section header, merged across every
  // attached host (docs/SIDEBAR.md M1b). Reads the hostgw map, NOT
  // notifications(): that signal is this host's feed only, and it is what
  // the widget body renders until notify's own relocation. Empty string
  // hides the badge (Section's Show guard treats it as falsy).
  const notifyBadge = (): string =>
    badgeText(totalCount(hostgw(), SERVICE_NOTIFY, unreadNotifications));
  // wantsAttention — the instances with an unread warn/error notification.
  // A window whose app has said something urgent and unread wears an amber
  // dot on its taskbar pill, which is the standing version of the toast
  // that has already faded (docs/AGENT_TERM.md §5: an agent waiting on you
  // must still be findable a minute later). Generic on purpose: it is the
  // notification level that earns the badge, not the app that sent it.
  const wantsAttention = createMemo(() => {
    const out = new Set<string>();
    for (const n of notifications()) {
      if (!n.read && (n.level === 'warn' || n.level === 'error') && n.source_instance) {
        out.add(n.source_instance);
      }
    }
    return out;
  });
  // Visiting the window is the acknowledgement — clear its unread warns so
  // the badge doesn't outlive the reason for it.
  const clearAttention = (instanceID: string) => {
    for (const n of notifications()) {
      if (!n.read && n.source_instance === instanceID) {
        window.wash.sendAppMsg(props.instance, { kind: 'notify_mark_read', id: n.id });
      }
    }
  };
  // bulkBadge — in-flight (queued + running) jobs across every host.
  // Terminal-state rows don't count (informational, auto-evicting). This
  // badge is why M1 is a hard prerequisite for M3: a long copy outlives
  // the fm window, so awareness cannot depend on any window being open.
  const bulkBadge = (): string =>
    badgeText(totalCount(hostgw(), SERVICE_BULK, activeBulkJobs));
  // privBadge — pending approval requests across every host. Empty when
  // every queue is settled (approved-and-done or already rejected — those
  // demand nothing). Answering still happens on the requesting host,
  // which is M4's business; this only says where to look.
  const privBadge = (): string =>
    badgeText(totalCount(hostgw(), SERVICE_PRIV, pendingPrivReqs));
  // ---- per-host awareness groups (docs/SIDEBAR.md M1c) ----
  //
  // hostLinkStatus reads the Hosts widget's own feed, which is the only
  // thing that knows whether a host is up, blipping or gone. A host the
  // supervisor has not mentioned is treated as up: its state reached us,
  // so something is connected — better to show it plainly than to grey a
  // host on the strength of a missing record.
  const hostLinkStatus = (origin: string): HostLinkStatus => {
    const h = remoteHosts().find((x) => x.origin === origin);
    if (!h) return 'up';
    if (h.status === 'down' || h.status === 'error') return 'down';
    if (h.status === 'up') return 'up';
    // starting / reconnecting / anything mid-flight: the numbers are from
    // before the interruption, so they get the honest treatment.
    return 'reconnecting';
  };
  // The focused window's host reads as "where you are" (§3.2(1)) — the one
  // concession to follow-the-focused-host, kept in presentation only.
  const focusedOrigin = (): string | undefined => windows().find((w) => w.focused)?.origin;
  // hostRows builds one section's remote rows. Each section supplies how to
  // badge and how to summarise its own service; the ordering, the
  // local-exclusion and the down/reconnecting policy are shared.
  const hostRows = (
    service: string,
    badgeOf: (state: unknown) => string,
    summaryOf: (state: unknown) => string,
  ): HostGroupRow[] => remoteHostRows(hostgw(), service, badgeOf, summaryOf, hostLinkStatus);
  // groupProps is the boilerplate every section's <HostGroups> shares:
  // collapse rides the section-state machinery, so a host group persists
  // and auto-expands exactly as a section does.
  const groupProps = {
    stateFor: (id: string) => sectionStates()[id] ?? ('collapsed' as SectionState),
    onToggle: (id: string) => toggleSection(id),
    hostColor: (origin: string) => hostHue(origin),
    focusedOrigin,
  };
  // privAccent — red trust tint used on the priv section header,
  // matching the (now-removed) priv window's titlebar stripe and the
  // ROOT-flagged window treatment in window.tsx.
  const PRIV_ACCENT = tokens.accentRed;
  // netBadge — flag when any host's netd wants attention: "!" while a
  // change awaits confirmation (the lock-out window), the status verb on a
  // terminal outcome, empty when every host is idle/committed. An
  // await-confirm anywhere outranks the rest — that host is one timeout
  // from locking someone out.
  const netBadge = (): string => netBadgeFor(hostgw());
  const NET_ACCENT = tokens.accentBlue;
  // remoteBadge — count of currently-connected remote hosts ("up"),
  // empty when none. The glanceable "how many sessions are open."
  const remoteBadge = (): string => {
    const up = remoteHosts().filter((h) => h.status === 'up').length;
    return up > 0 ? String(up) : '';
  };
  const REMOTE_ACCENT = tokens.accentViolet;
  // agentBadge — the count of agents waiting on the human. That is the
  // only number worth interrupting for; working agents are visible in the
  // section, not on its header.
  const agentBadge = (): string => {
    // A question waiting on you counts the same as an agent waiting on
    // you — both mean "someone is blocked until you look". Merged across
    // hosts: an agent on build01 was the sharpest symptom of the rail
    // being local-only (docs/SIDEBAR.md §1.2).
    return badgeText(totalCount(hostgw(), SERVICE_AGENT, waitingAgents));
  };
  // audioBadge — show a play glyph while something is actively playing,
  // empty otherwise. Mirrors the other section badges' "needs attention"
  // semantics (here: "sound is on").
  const audioBadge = (): string => {
    const playing = (audioState()?.sources ?? []).some((s) => s.status === 'playing');
    return playing ? '♪' : '';
  };
  const AUDIO_ACCENT = tokens.accentGreen;
  const AGENTS_ACCENT = tokens.accentTeal;
  let screenshotTimer = 0;
  let currentObjectURL: string | null = null;
  // Dedicated wallpaper layer — applyWallpaper paints onto this instead of
  // props.host so the wallpaper can stop at the taskbar's edge.
  let wallpaperEl: HTMLDivElement | undefined;
  // The active theme pack — drives the start-menu icon (the scheme vars
  // and wallpaper are applied imperatively in applyDesktopConfig). Seeded
  // with the default so the first paint, before desktop.config arrives,
  // is already correct.
  const [activePack, setActivePack] = createSignal<Pack>(getPack(null));

  // Per-theme wallpaper extent: by default the wallpaper fills the whole
  // window (runs behind the taskbar); a pack can set --wash-wallpaper-extent:
  // desktop to inset it so it stops at the taskbar's edge (Dreamtime does).
  // Read off the live CSS var, re-evaluated when the pack changes.
  const wallpaperExcludesTaskbar = (): boolean => {
    activePack(); // track pack changes
    if (typeof document === 'undefined') return false;
    return getComputedStyle(document.documentElement)
      .getPropertyValue('--wash-wallpaper-extent').trim() === 'desktop';
  };

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
  //
  // Recent files ride along as rows too (launcher.ts paletteEntries): a
  // query matches anywhere in the path, so "notes" and "home/u" both find
  // /home/u/notes.md; with no query the newest few are listed under the apps.
  const paletteResults = createMemo((): PaletteEntry[] => {
    const apps = [...catalog().filter((a) => !a.disabled), ...rootEntries()];
    return paletteEntries(apps, recent(), paletteQuery());
  });

  const launchApp = (appID: string) => {
    window.wash.sendAppMsg(props.instance, { action: 'launch', app_id: appID });
  };

  // ---- launcher memory (recent files / pins) ----
  // All mutations go through the session BE, which owns the state file and
  // pushes launcher.state back; the FE never edits its copy locally.
  // openRecent carries the app that recorded the entry: a folder has no
  // extension for the router to resolve, so the BE spawns that app on it.
  // A flyout item names its app (the same path can be under Files and
  // Terminal); a palette or search row is the path's newest entry.
  const openRecent = (path: string, appID?: string) => {
    const app = appID ?? recent().find((r) => r.path === path)?.app_id ?? '';
    window.wash.sendAppMsg(props.instance, { kind: 'recent.open', path, app_id: app });
  };
  const playRecent = (e: RecentEntry) => {
    window.wash.sendAppMsg(props.instance, { kind: 'recent.play', app_id: e.app_id, name: e.name ?? '' });
  };
  const openAgentRecent = (s: AgentRecent, action: AgentRecentAction) => {
    window.wash.sendAppMsg(props.instance, {
      kind: 'agent_open',
      action,
      session_id: s.session_id,
      row_key: s.row_key ?? '',
    });
  };
  // Scoped to the entry's app from a flyout, which lists one app's entries.
  // The flat search list shows a path once for every app that recorded it,
  // so Remove there forgets the path everywhere (a path with no app_id), or
  // an older twin would take the removed row's place. A name always belongs
  // to its app.
  const removeRecent = (e: RecentEntry, scoped: boolean) => {
    const appID = scoped || !e.path ? e.app_id : '';
    window.wash.sendAppMsg(props.instance, { kind: 'recent.remove', path: e.path, name: e.name ?? '', app_id: appID });
  };
  // No app id clears every app's entries.
  const clearRecent = (appID?: string) => {
    window.wash.sendAppMsg(props.instance, appID ? { kind: 'recent.clear', app_id: appID } : { kind: 'recent.clear' });
  };
  // One memo, not an inline prop expression: a prop expression is re-run on
  // every read, and the start menu reads its groups from rows, keyboard
  // handlers and the flyout alike.
  const menuGroups = createMemo(() =>
    recentGroups(recent(), agentRecent(), (id) => catalog().find((a) => a.id === id)?.name, agentRows()),
  );
  const setPin = (appID: string, on: boolean) => {
    window.wash.sendAppMsg(props.instance, { kind: 'launcher.pin', app_id: appID, on });
  };

  // Mission Commander's switch (docs/COMMANDER.md §5.3): the local host's
  // commander publishes its settings and stats through hostgw; the toggle
  // goes to our own BE, which forwards it attested.
  const commanderAuto = (): { on: boolean; detail: string } | null => {
    const st = stateFor(hostgw(), LOCAL_ORIGIN, 'commander') as
      { settings?: { automatic?: boolean }; stats?: { running?: boolean; reason?: string; briefs?: number; provider?: string } } | undefined;
    if (!st?.settings) return null;
    const on = !!st.settings.automatic;
    const stats = st.stats ?? {};
    const detail = !on ? 'Automatic briefs are off'
      : stats.running ? `Briefing via ${stats.provider ?? 'provider'} · ${stats.briefs ?? 0} so far`
        : `Not running: ${stats.reason ?? 'waiting'}`;
    return { on, detail };
  };

  // ---- activity timeline (docs/COMMANDER.md §6) ----
  // The journal is per host; the widget shows every connected host's,
  // merged newest first. Loaded on mount and whenever the section opens,
  // kept live by the routers' tails while the section is expanded, so a
  // collapsed Timeline costs nothing but the query it made on mount.
  const [timeline, setTimeline] = createSignal<TimelineEntry[]>([]);
  const [timelineCursors, setTimelineCursors] = createSignal<Record<string, string>>({});
  const [timelineLoading, setTimelineLoading] = createSignal(false);
  const [timelineOff, setTimelineOff] = createSignal(false);
  const loadTimeline = async (more = false) => {
    setTimelineLoading(true);
    try {
      const cursors = timelineCursors();
      const pages = more
        ? await Promise.all(Object.entries(cursors).map(([host, cursor]) =>
            window.wash.activityQuery(host === 'local' ? undefined : host, { limit: 100, cursor })
              .catch(() => ({ host, entries: [] as TimelineEntry[] }))))
        : await window.wash.activityQueryAll({ limit: 100 });
      const next: Record<string, string> = more ? {} : {};
      for (const p of pages) if (p.cursor) next[p.host] = p.cursor;
      setTimelineCursors(next);
      setTimeline((prev) => mergeNewestFirst(more ? [{ entries: prev }, ...pages] : pages, more ? 2000 : 500));
      // Every host refusing is the journal being off, not a quiet day.
      setTimelineOff(!more && pages.length > 0 && pages.every((p) => p.entries.length === 0) && await allJournalsOff());
    } finally {
      setTimelineLoading(false);
    }
  };
  const allJournalsOff = async () => {
    try {
      const st = await window.wash.activityStats(undefined);
      return !st.enabled;
    } catch {
      return true;
    }
  };
  let timelineTailOff: (() => void) | null = null;
  createEffect(() => {
    const open = (sectionStates().timeline ?? 'collapsed') === 'expanded';
    if (open && !timelineTailOff) {
      void loadTimeline();
      timelineTailOff = window.wash.onActivity((e) => setTimeline((prev) => prependLive(prev, e)));
    } else if (!open && timelineTailOff) {
      timelineTailOff();
      timelineTailOff = null;
    }
  });
  onCleanup(() => { timelineTailOff?.(); });
  // jumpTimeline acts on a row's intent through the paths the desktop
  // already has: a window is focused (restored first) on its host, a
  // session goes through agent_open, a path through recent.open.
  const jumpTimeline = (e: TimelineEntry) => {
    const i = e.intent;
    if (!i) return;
    const origin = e.host === 'local' ? 'local' : e.host;
    switch (i.kind) {
      case 'focus': {
        const w = i.window_id ? windows().find((x) => x.origin === origin && x.windowID === i.window_id) : undefined;
        if (w) {
          // Restore first when buried, then focus: a restore alone brings
          // the window back where it was in the stack, not to the front.
          if (w.state === 'minimized') window.wash.restoreWindow(w.windowID, w.origin);
          window.wash.focusWindow(w.windowID, w.origin);
        } else if (i.app_id) {
          window.wash.focusOrLaunch(origin, i.app_id);
        }
        return;
      }
      case 'resume': {
        const live = i.row_key ? agentRows().some((r) => r.key === i.row_key) : false;
        window.wash.sendAppMsg(props.instance, {
          kind: 'agent_open', action: live ? 'focus' : 'resume', session_id: i.session_id ?? '', row_key: live ? i.row_key : '',
        });
        return;
      }
      case 'open':
        if (i.path) openRecent(i.path, i.app_id);
        else if (i.app_id) window.wash.focusOrLaunch(origin, i.app_id);
        return;
    }
  };
  const clearTimeline = async () => {
    await Promise.all(window.wash.windows().map((w) => w.origin).filter((o, k, a) => a.indexOf(o) === k)
      .map((o) => window.wash.activityClear(o === 'local' ? undefined : o).catch(() => undefined)));
    await window.wash.activityClear(undefined).catch(() => undefined);
    setTimeline([]);
    setTimelineCursors({});
  };

  // ---- remote hosts (sidebar) ----
  // appsForHost lists a connected host's launchable apps (window surface,
  // enabled). Reads remoteCatVer() so it re-runs when a catalog changes.
  const appsForHost = (origin: string) => {
    remoteCatVer();
    return window.wash
      .catalogFor(origin)
      .filter((a) => a.surface === 'window' && !a.disabled)
      .map((a) => ({ id: a.id, name: a.name, icon: a.icon }));
  };
  const launchOnHost = (origin: string, appID: string) => window.wash.launchOn(origin, appID);
  // openConnect focuses an already-open wash-connect window (singleton launch
  // is a no-op that doesn't raise it — the old "Manage does nothing" bug),
  // else launches it. Used to add new hosts / manage auth + bookmarks.
  const openConnect = () => {
    const w = windows().find((x) => x.element === 'wash-app-connect');
    if (w) window.wash.focusWindow(w.windowID, w.origin);
    else launchApp('com.wash.connect');
  };

  // launchPick handles both regular catalog rows and synthetic root
  // rows. Routes the latter through the session BE → wash-priv path
  // (queue + approval + password modal + sudo).
  const launchPick = (id: string) => {
    const recentPath = recentPathOf(id);
    if (recentPath !== null) {
      openRecent(recentPath);
      return;
    }
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

  // applyDesktopConfig applies the active pack (scheme vars on the root
  // + start icon + default wallpaper), then layers the wallpaper bytes /
  // mode / fallback color onto the host element's inline style and
  // updates clock/taskbar signals. Object URLs are revoked when they're
  // replaced — the browser keeps the blob alive until the URL is gone,
  // and a long session with many wallpaper changes would otherwise leak.
  const applyDesktopConfig = (cfg: DesktopConfigMsg) => {
    // Resolve + apply the pack. Scheme vars go on document.documentElement
    // so they cascade into every open app (light DOM); see packs.ts.
    const pack = getPack(cfg.pack);
    applyScheme(document.documentElement, pack);
    setActivePack(pack);

    void applyWallpaper(pack, cfg.wallpaper || {});

    setClockFormat(cfg.clock?.format === '12h' ? '12h' : '24h');
    setShowSeconds(!!cfg.clock?.show_seconds);
    setTaskbarPosition(cfg.taskbar?.position === 'top' ? 'top' : 'bottom');
    setClock(formatClock(clockFormat(), showSeconds()));
  };

  // applyWallpaper paints the desktop background. The bytes come from
  // one of two places: a user's *custom* image (base64 over the wire in
  // the config) or the active pack's wallpaper asset, pulled from the
  // router via window.wash.fetchAsset (the transport-agnostic asset.read
  // channel — works in the in-browser VM where a plain HTTP url() can't).
  // Async, so a generation counter drops a stale fetch if a newer
  // desktop.config lands first. Object URLs are revoked when replaced.
  let wallpaperGen = 0;
  // Tell the shell the desktop background is actually on screen so it can
  // tear down the boot splash (web/shell boot.ts). Fires once, best-effort.
  let desktopPainted = false;
  const signalPainted = () => {
    if (desktopPainted) return;
    desktopPainted = true;
    try {
      window.dispatchEvent(new CustomEvent('wash:desktop-painted'));
    } catch {
      /* no CustomEvent (non-browser test env) — splash backstop covers it */
    }
  };
  // Best-effort decode so "painted" means pixels are ready, not just that
  // the blob URL was assigned. Swallows errors (e.g. an SVG the browser
  // declines to decode) — the caller paints + signals regardless.
  const decodeImage = async (url: string) => {
    try {
      const img = new Image();
      img.src = url;
      await img.decode();
    } catch {
      /* best-effort */
    }
  };
  const applyWallpaper = async (pack: Pack, wp: DesktopConfigMsg['wallpaper']) => {
    const gen = ++wallpaperGen;
    const fallback = wp.fallback_color || 'radial-gradient(circle at 30% 20%, #1a1a32 0, #0a0a18 75%)';
    const mode = wp.mode || 'cover';
    // The wallpaper paints onto a dedicated layer (wallpaperEl) that is
    // inset to stop at the taskbar's edge, so the painting's bottom sits at
    // the top of the taskbar rather than running behind it. props.host keeps
    // a solid base so the taskbar strip (the translucent taskbar sits over
    // it) reads cleanly.
    const baseColor = fallback.startsWith('radial-') ? '#0a0a18' : fallback;
    // paint() applies the resolved background, but only if this is still
    // the latest call; otherwise it revokes the just-made URL and bails.
    const paint = (imageCSS: string, objURL: string | null) => {
      if (gen !== wallpaperGen) {
        if (objURL) URL.revokeObjectURL(objURL);
        return;
      }
      const layer = wallpaperEl ?? props.host;
      layer.style.background = `${imageCSS} center/cover no-repeat ${baseColor}`;
      layer.style.backgroundSize = mode === 'tile' ? 'auto' : mode === 'center' ? 'auto' : mode; // 'cover' | 'contain'
      layer.style.backgroundRepeat = mode === 'tile' ? 'repeat' : 'no-repeat';
      layer.style.backgroundPosition = 'center';
      props.host.style.background = baseColor;
      if (currentObjectURL) URL.revokeObjectURL(currentObjectURL);
      currentObjectURL = objURL;
    };
    if (wp.bytes) {
      const url = URL.createObjectURL(new Blob([decodeBase64(wp.bytes)], { type: wp.mime || 'application/octet-stream' }));
      await decodeImage(url);
      paint(`url("${url}")`, url);
      signalPainted();
      return;
    }
    if (!window.wash?.fetchAsset) {
      // No transport-agnostic fetch (older shell / standalone): fall back
      // to a plain HTTP asset URL. Won't render in the in-browser VM.
      paint(`url("${washAssetUrl(pack.wallpaper)}")`, null);
      signalPainted();
      return;
    }
    try {
      const a = await window.wash.fetchAsset(pack.wallpaper);
      const url = URL.createObjectURL(new Blob([a.bytes], { type: a.mime || 'image/svg+xml' }));
      await decodeImage(url);
      paint(`url("${url}")`, url);
      signalPainted();
    } catch (err) {
      console.warn(`wash-session: wallpaper fetch ${pack.wallpaper}:`, err);
      // Don't strand the boot splash on a wallpaper failure — the gradient
      // fallback is already showing; tell the shell we're as painted as
      // we'll get.
      signalPainted();
    }
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
    const fail = (msg: string) => {
      setStatus(msg, 6_000);
      window.wash?.log?.('warn', 'wash-session', `screenshot: ${msg}`);
    };
    try {
      // html-to-image is fragile on two things that the desktop routinely
      // has open, so harden against both:
      //  - cross-origin iframes (ingress apps: vscode, code-server, the
      //    browser VM) throw a SecurityError when cloned — skip them so one
      //    embedded app can't fail the whole capture (it just renders blank).
      //  - web-font embedding fetches @font-face CSS, which can hang or 404;
      //    skipFonts uses the already-rendered text instead.
      // And cap the wait so a heavy desktop can never hang the button.
      const blob = await Promise.race([
        toBlob(document.documentElement, {
          cacheBust: false,
          pixelRatio: window.devicePixelRatio || 1,
          skipFonts: true,
          filter: (node) => !(node instanceof HTMLIFrameElement),
          // If a single embedded image can't be inlined, substitute a
          // transparent pixel instead of rejecting the whole capture.
          imagePlaceholder:
            'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M8AAAMBAQDJ/pLvAAAAAElFTkSuQmCC',
        }),
        new Promise<null>((resolve) => setTimeout(() => resolve(null), 10_000)),
      ]);
      if (!blob) {
        fail('capture failed (timed out or empty)');
        return;
      }
      const resp = await fetch('/screenshot', { method: 'POST', body: blob });
      if (!resp.ok) {
        fail(`save failed: ${(await resp.text()).slice(0, 80)}`);
        return;
      }
      const name = (await resp.text()).trim();
      setStatus(`saved ${name}`, 4_000);
    } catch (err) {
      fail(`error: ${describeErr(err)}`);
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
    const offRemoteCat = window.wash.onRemoteCatalog(() => setRemoteCatVer((v) => v + 1));
    const offWin = window.wash.onWindowsChanged(setWindows);
    const offVp = window.wash.onViewport(setVp);
    const offScreen = window.wash.onScreenSize(setScreen);
    const offLink = window.wash.onLinkStats?.(setLink);
    // Host awareness (docs/SIDEBAR.md M1b). Fires immediately with the
    // current map, then on every (origin, service) change. Nothing is sent
    // from here: the shell owns the subscribe to each host's hostgw,
    // because it owns the connections and has to re-ask on every reconnect.
    // Auto-expand on a REMOTE event (§3.2(1)): when a host's count for a
    // service goes UP, open that section and that host's group, so the
    // thing that just started needing you is on screen rather than folded
    // away. Only on a rise — a count falling means something resolved, and
    // pulling the rail open to announce that is noise.
    //
    // Keyed by (service, origin) against the previous counts, which is the
    // same "recompute from snapshots" discipline as the badges: no event
    // stream to miss, no counter to drift.
    const lastCounts = new Map<string, number>();
    const offHostgw = window.wash.onHostgwState?.((next) => {
      setHostgw(next);
      for (const [service, count] of AWARENESS_COUNTERS) {
        for (const { origin, count: n } of countByHost(next, service, count)) {
          // LOCAL included since M6. It was skipped to avoid doubling with
          // the legacy per-service handlers, but autoExpandSection early-
          // returns when a section is already open, so a double call is a
          // no-op — and the skip meant local and remote hosts interrupted
          // on DIFFERENT rules, which is a wart in the one behaviour the
          // rail exists for.
          const key = `${service}:${origin}`;
          if (n > (lastCounts.get(key) ?? 0)) {
            autoExpandSection(sectionForService(service));
            autoExpandSection(key);
          }
          lastCounts.set(key, n);
        }
      }
    });

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
        case 'host.ifaces':
          setNetIfaces((data.interfaces as NetIface[] | undefined) ?? []);
          return;
        case 'launcher.state': {
          const d = data as unknown as { recent?: RecentEntry[]; pinned?: string[] };
          setRecent(Array.isArray(d.recent) ? d.recent : []);
          setPinned(Array.isArray(d.pinned) ? d.pinned : []);
          return;
        }
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
        case 'priv.state': {
          // wash-priv full snapshot — broadcast on subscribe + every
          // queue change. Shape: { locked: bool, queue: [...] }.
          const next = data.state as { locked?: boolean; queue?: PrivReq[]; app_grants?: string[] };
          const nextReqs = next?.queue ?? [];
          const seen = new Set(privReqs().map((r) => r.req_id));
          const fresh = nextReqs.find((r) => !seen.has(r.req_id));
          setPrivReqs(nextReqs);
          setPrivGrants(Array.isArray(next?.app_grants) ? next.app_grants : []);
          setPrivLocked(!!next?.locked);
          if (fresh) {
            autoExpandSection('priv');
          }
          return;
        }
        case 'net.state': {
          // com.wash.netd's StateService snapshot: {status, phase, summary,
          // diagnostics}. Auto-expand on await-confirm — that's the
          // commit-confirm "about to be locked out" moment the user must see.
          const next = data.state as unknown as NetState;
          const wasAwaiting = netState()?.status === 'await-confirm';
          setNetState(next ?? null);
          if (next?.status === 'await-confirm' && !wasAwaiting) {
            autoExpandSection('net');
          }
          return;
        }
        case 'remote.state': {
          // com.wash.remote's StateService snapshot: {hosts:[…]}. Mirror it
          // into the sidebar's glanceable list, and reconcile attachments so
          // remote windows re-attach after an SSH blip even with wash-connect
          // closed (REVIEW-RECONNECT M4). wash-connect still owns
          // connect/auth/launch — this only re-issues attach/detach.
          const st = data.state as unknown as { hosts?: RemoteHost[] };
          const hostList = Array.isArray(st?.hosts) ? st.hosts : [];
          setRemoteHosts(hostList);
          reconcileRemoteAttachments(hostList, attachedRemotes, {
            attach: (o) => window.wash.attachRemote(o),
            detach: (o) => window.wash.detachRemote(o),
          });
          return;
        }
        case 'agent.state': {
          // com.wash.agentd's roster snapshot. Anchor each row's clock on
          // arrival (since_ms is elapsed at push time, so no cross-clock
          // comparison), and auto-expand when an agent first wants the
          // human — the one case worth pulling the section open.
          const state = data.state as unknown as {
            rows?: RosterRow[];
            asks?: RosterAsk[];
            recent?: AgentRecent[];
          };
          setAgentRecent(Array.isArray(state?.recent) ? state.recent : []);
          const next = (state?.rows ?? []) as RosterRow[];
          const asks = (state?.asks ?? []) as RosterAsk[];
          const hadAsks = agentAsks().length > 0;
          setAgentAsks(asks);
          if (asks.length > 0 && !hadAsks) autoExpandSection('agents');
          const waiting = next.some((r) => r.state === 'needs-input');
          const wasWaiting = agentRows().some((r) => r.state === 'needs-input');
          setAgentRows(next);
          if (waiting && !wasWaiting) autoExpandSection('agents');
          return;
        }
        case 'audio.state': {
          // com.wash.audio's StateService snapshot: {sources, master_volume,
          // master_mute}. Auto-expand when a source first appears so the
          // user sees what started playing.
          const next = data.state as unknown as AudioState;
          const had = (audioState()?.sources?.length ?? 0) > 0;
          const has = (next?.sources?.length ?? 0) > 0;
          setAudioState(next ?? null);
          if (has && !had) {
            autoExpandSection('audio');
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
    window.wash.sendAppMsg(props.instance, { kind: 'priv_subscribe' });
    window.wash.sendAppMsg(props.instance, { kind: 'net_subscribe' });
    window.wash.sendAppMsg(props.instance, { kind: 'remote_subscribe' });
    window.wash.sendAppMsg(props.instance, { kind: 'audio_subscribe' });
    window.wash.sendAppMsg(props.instance, { kind: 'agent_subscribe' });
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
      offRemoteCat();
      offWin();
      offVp();
      offScreen();
      offLink?.();
      offHostgw?.();
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('wash:state', onState);
      // Cooperative unsubscribe via the session BE gateways. Cheap
      // fire-and-forget; gateways forward with proper sender
      // attestation.
      try {
        window.wash.sendAppMsg(props.instance, { kind: 'agent_unsubscribe' });
        window.wash.sendAppMsg(props.instance, { kind: 'notify_unsubscribe' });
        window.wash.sendAppMsg(props.instance, { kind: 'bulk_unsubscribe' });
        window.wash.sendAppMsg(props.instance, { kind: 'priv_unsubscribe' });
        window.wash.sendAppMsg(props.instance, { kind: 'net_unsubscribe' });
        window.wash.sendAppMsg(props.instance, { kind: 'audio_unsubscribe' });
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
      {/* Wallpaper layer — sits behind all desktop content. Per-theme
          knob: --wash-wallpaper-extent (window | desktop) insets it to stop
          at the taskbar's edge. pointer-events:none; painted by
          applyWallpaper. */}
      <div
        ref={wallpaperEl}
        data-testid="desktop-wallpaper"
        style={{
          position: 'absolute',
          left: 0,
          right: 0,
          top: wallpaperExcludesTaskbar() && taskbarPosition() === 'top' ? `${taskbarHeight}px` : 0,
          bottom: wallpaperExcludesTaskbar() && taskbarPosition() === 'bottom' ? `${taskbarHeight}px` : 0,
          'pointer-events': 'none',
          'z-index': 0,
        }}
      />
      {/* Frame for the wallpaper border (--wash-wallpaper-border; Dreamtime:
          5px black). Its own TOPMOST layer, not a border on the z-index:0
          wallpaper: content that reaches the screen edge — a maximized
          window, the sidebar — would otherwise paint over the frame (#5).
          Same inset as the wallpaper so the frame keeps hugging the
          painting; pointer-events:none so it never intercepts clicks. */}
      <div
        data-testid="viewport-frame"
        style={{
          position: 'absolute',
          left: 0,
          right: 0,
          top: wallpaperExcludesTaskbar() && taskbarPosition() === 'top' ? `${taskbarHeight}px` : 0,
          bottom: wallpaperExcludesTaskbar() && taskbarPosition() === 'bottom' ? `${taskbarHeight}px` : 0,
          border: tokens.wallpaperBorder,
          'box-sizing': 'border-box',
          'pointer-events': 'none',
          'z-index': 2147483000,
        }}
      />
      <Banner info={sysInfo} />
      <Sidebar
        mode={sidebarMode()}
        taskbarPos={taskbarPosition()}
        taskbarHeight={taskbarHeight}
        onToggle={toggleSidebar}
        user={sysInfo()?.username}
        host={sysInfo()?.hostname}
        // The same number the Agents section wears, so a hidden rail can
        // still say an agent is blocked (docs/AGENT_UX.md N3). Agents
        // only: the other sections' counts are news, not a question
        // holding someone up.
        badge={agentBadge()}
      >
        <Section
          id="viewport"
          title="Viewport"
          icon="layout-grid"
          iconColor={tokens.accentCyan}
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
          iconColor={tokens.accentViolet}
          state={sectionStates().about ?? 'collapsed'}
          onToggle={() => toggleSection('about')}
        >
          <AboutWidget
            info={() => sysInfo()}
            stats={() => hostStats()}
          />
          <LinkWidget health={link} />
        </Section>
        <Section
          id="notify"
          title="Notifications"
          icon="bell"
          iconColor={tokens.accentAmber}
          state={sectionStates().notify ?? 'collapsed'}
          onToggle={() => toggleSection('notify')}
          badge={notifyBadge()}
        >
          <NotifyWidget
            notifications={notifications}
            onMarkRead={(id) => window.wash.sendAppMsg(props.instance, { kind: 'notify_mark_read', id })}
            onClearAll={() => window.wash.sendAppMsg(props.instance, { kind: 'notify_clear_all' })}
          />
          {/* Remote hosts' unread counts, below this seat's own list. The
              list itself stays local until notify's relocation — a remote
              notification is actionable on its own host, not from here. */}
          <HostGroups
            section="notify"
            rows={() => hostRows(SERVICE_NOTIFY, countBadge(unreadNotifications), notifySummary)}
            {...groupProps}
          />
        </Section>
        <Section
          id="timeline"
          title="Timeline"
          icon="activity"
          iconColor={tokens.accentViolet}
          state={sectionStates().timeline ?? 'collapsed'}
          onToggle={() => toggleSection('timeline')}
          badge={timeline().length > 0 && (sectionStates().timeline ?? 'collapsed') === 'collapsed' ? String(Math.min(timeline().length, 99)) : ''}
        >
          <TimelineWidget
            entries={timeline}
            more={() => Object.keys(timelineCursors()).length > 0}
            loading={timelineLoading}
            off={timelineOff}
            onJump={jumpTimeline}
            onLoadMore={() => void loadTimeline(true)}
            onClear={() => void clearTimeline()}
            hostColor={(h) => hostHue(h)}
            auto={commanderAuto}
            onAuto={(on) => window.wash.sendAppMsg(props.instance, { kind: 'commander_set', automatic: on })}
            onBriefNow={() => window.wash.sendAppMsg(props.instance, { kind: 'commander_run' })}
          />
        </Section>
        <Section
          id="bulk"
          title="Bulk Ops"
          icon="list-checks"
          iconColor={tokens.accentGreen}
          state={sectionStates().bulk ?? 'collapsed'}
          onToggle={() => toggleSection('bulk')}
          badge={bulkBadge()}
        >
          <HostGroups
            section="bulk"
            rows={() => hostRows(SERVICE_BULK, countBadge(activeBulkJobs), bulkSummary)}
            {...groupProps}
          />
          {/* The queue and its cancel button live in com.wash.fm now
              (docs/SIDEBAR.md M3a). They had to: cancelling is a send, and
              the rail's sends gateway through the session BE, which
              resolves inside its own router — so the rail's cancel could
              never reach a job on B. fm subscribes to the bulk singleton
              directly, so any fm window on a host commands every job on it.

              No door for a host with nothing in flight, unlike Agents: an
              empty queue has nothing to manage, and "open a file manager"
              is what the launcher is for. */}
          <HostOpen
            testid="bulk-open"
            what="file manager"
            doors={() =>
              countByHost(hostgw(), SERVICE_BULK, activeBulkJobs).map(({ origin, count }) => ({
                origin,
                label:
                  origin === LOCAL_ORIGIN
                    ? `${count} in flight`
                    : `${count} in flight · ${origin}`,
              }))
            }
            onOpen={(origin) => window.wash.launchOn(origin, 'com.wash.fm')}
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
            grants={privGrants}
            onApprove={(id) =>
              window.wash.sendAppMsg(props.instance, { kind: 'priv_approve', req_id: id })
            }
            onApproveApp={(id) =>
              window.wash.sendAppMsg(props.instance, { kind: 'priv_approve_app', req_id: id })
            }
            onReject={(id, reason) =>
              window.wash.sendAppMsg(props.instance, {
                kind: 'priv_reject',
                req_id: id,
                reason: reason ?? '',
              })
            }
            onRevokeApp={(appID) =>
              window.wash.sendAppMsg(props.instance, { kind: 'priv_revoke_app', app_id: appID })
            }
            onLock={() => window.wash.sendAppMsg(props.instance, { kind: 'priv_lock' })}
          />
          {/* Deliberately count-only: approving B's escalation from A is
              exactly what M4 moves to a prompt window on the requesting
              host, so the rail says where to go and nothing more. */}
          <HostGroups
            section="priv"
            rows={() => hostRows(SERVICE_PRIV, countBadge(pendingPrivReqs), privSummary)}
            {...groupProps}
          />
        </Section>
        <Section
          id="net"
          title="Network"
          icon="network"
          accent={NET_ACCENT}
          state={sectionStates().net ?? 'collapsed'}
          onToggle={() => toggleSection('net')}
          badge={netBadge()}
        >
          <NetWidget state={netState} ifaces={netIfaces} onConfigure={() => launchApp('com.wash.net')} />
          {/* Per-host network status. The collapsed badge folds hosts
              together by severity; here each host speaks for itself. */}
          <HostGroups
            section="net"
            rows={() => hostRows(SERVICE_NET, netBadgeForHost, netSummary)}
            {...groupProps}
          />
          {/* netd manages the network of the machine it runs on, so this
              is per-host by nature rather than merged: "configure the
              network" is meaningless without saying whose. The widget
              above still opens the local one; these open a remote host's,
              which the rail could never do — its sends resolve inside
              their own router (docs/SIDEBAR.md M5).

              Every host with netd gets a door, not just the ones with
              something wrong: a network that is quietly fine is still a
              thing you open settings for. */}
          <HostOpen
            testid="net-open"
            what="network settings"
            doors={() =>
              hostsWithService(hostgw(), SERVICE_NET)
                .filter((origin) => origin !== LOCAL_ORIGIN)
                .map((origin) => ({
                  origin,
                  label: `Configure · ${origin}`,
                  badge: netBadgeForHost(stateFor(hostgw(), origin, SERVICE_NET)) === '!' ? 1 : 0,
                }))
            }
            onOpen={(origin) => window.wash.launchOn(origin, 'com.wash.net')}
          />
        </Section>
        <Section
          id="remote"
          title="Remote"
          icon="monitor"
          accent={REMOTE_ACCENT}
          state={sectionStates().remote ?? 'collapsed'}
          onToggle={() => toggleSection('remote')}
          badge={remoteBadge()}
        >
          <RemoteWidget
            hosts={remoteHosts}
            appsFor={appsForHost}
            onLaunch={launchOnHost}
            onManage={openConnect}
          />
        </Section>
        <Section
          id="audio"
          title="Audio"
          icon="music"
          accent={AUDIO_ACCENT}
          state={sectionStates().audio ?? 'collapsed'}
          onToggle={() => toggleSection('audio')}
          badge={audioBadge()}
        >
          <AudioWidget
            state={audioState}
            onControl={(id, action) =>
              window.wash.sendAppMsg(props.instance, { kind: 'audio_control', id, action })
            }
            onMasterVolume={(value) =>
              window.wash.sendAppMsg(props.instance, { kind: 'audio_set_master_volume', value })
            }
          />
        </Section>
        <Section
          id="agents"
          title="Agents"
          icon="bot"
          accent={AGENTS_ACCENT}
          state={sectionStates().agents ?? 'collapsed'}
          onToggle={() => toggleSection('agents')}
          badge={agentBadge()}
        >
          {/* Questions stay here — a NAMED exception to the relocation
              (docs/SIDEBAR.md §3.2(8)). Everything else moved because you
              were opening the app anyway; answering yes/no is the one case
              where opening a window is pure overhead, and an agent blocked
              on a human is the thing this rail exists to surface.
              agent_answer still routes through our own BE, because a
              shell-originated send carries no router-attested From. */}
          <AgentAsks
            asks={agentAsks}
            onAnswer={(ask, decision, remember) =>
              window.wash.sendAppMsg(props.instance, {
                kind: 'agent_answer',
                id: ask.id,
                decision,
                remember,
                rule: ask.suggested_rule ?? '',
              })
            }
          />
          {/* The roster and its verbs live in com.wash.agents now. The rail
              says how many and where, and opens the app on the right
              host — which is the whole point: launchOn carries an origin,
              and the verbs it used to hold could not.

              focusOrLaunch, not launchOn (docs/AGENT_UX.md N1): this door
              was spawning a fresh Agent window on every click, so the way
              back to the agent you were watching was the way to lose it
              among four identical windows. */}
          <AgentOpen
            hosts={() => agentHostSummary(hostgw())}
            onOpen={(origin) => window.wash.focusOrLaunch(origin, 'com.wash.agents')}
          />
          {/* The sharpest case in the whole plan (§1.2): an agent on B was
              invisible here. It now has a host, a count and a summary that
              distinguishes "waiting on you" from "working". The roster and
              its verbs move into com.wash.agents. */}
          <HostGroups
            section="agents"
            rows={() => hostRows(SERVICE_AGENT, countBadge(waitingAgents), agentSummary)}
            {...groupProps}
            onOpen={(origin) => window.wash.focusOrLaunch(origin, 'com.wash.agents')}
            openTitle={(origin) => `Open Agent on ${origin}`}
          />
        </Section>
        <Section
          id="clipboard"
          title="Clipboard"
          icon="clipboard"
          iconColor={tokens.accentCyan}
          state={sectionStates().clipboard ?? 'collapsed'}
          onToggle={() => toggleSection('clipboard')}
        >
          <ClipboardWidget />
        </Section>
      </Sidebar>
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
          <Show
            when={activePack().startIconSVG}
            fallback={<img src={washAssetUrl('wash-logo.svg')} width="20" height="20" alt="wash" style={{ display: 'block' }} />}
          >
            <span
              style={{ display: 'block', width: '20px', height: '20px' }}
              // eslint-disable-next-line solid/no-innerhtml -- pack start icons are built-in, trusted SVG markup
              innerHTML={activePack().startIconSVG}
            />
          </Show>
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
          <For each={windows()}>
            {(w) => (
              // Two roads to the same dot. The notification one is
              // after-the-fact ("something warned and you haven't read
              // it"); w.attention is the window's own live claim, which
              // the router drops the moment you look at it
              // (docs/AGENT_UX.md N6). An agent blocked on a question
              // needs the second: its toast may be long gone, and there is
              // no unread notification to point at when the ask came from
              // a session this window is showing.
              <WindowPill
                win={w}
                attention={w.attention || wantsAttention().has(w.instanceID)}
                onVisit={() => clearAttention(w.instanceID)}
              />
            )}
          </For>
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
          recent={recent()}
          groups={menuGroups()}
          holdFlyout={recentMenu() !== null}
          version={sysInfo()?.router?.version}
          onDismiss={() => {
            // A click on a row context menu is "outside" the start menu
            // (portal); keep the menu up until that menu is gone.
            if (recentMenu() || appMenu()) return;
            setMenuOpen(false);
          }}
          onPick={(id) => {
            setMenuOpen(false);
            launchPick(id);
          }}
          onOpenRecent={(path) => {
            setMenuOpen(false);
            openRecent(path);
          }}
          onRecentItem={(item) => {
            setMenuOpen(false);
            if (item.kind === 'path') openRecent(item.entry.path, item.entry.app_id);
            else if (item.kind === 'station') playRecent(item.entry);
            else openAgentRecent(item.session, item.action);
          }}
          pinned={pinned()}
          onRecentContextMenu={(ev, entry, group) => {
            ev.preventDefault();
            ev.stopPropagation();
            setAppMenu(null);
            setRecentMenu({ x: ev.clientX, y: ev.clientY, entry, group });
          }}
          onAppContextMenu={(ev, appID) => {
            ev.preventDefault();
            ev.stopPropagation();
            setRecentMenu(null);
            setAppMenu({ x: ev.clientX, y: ev.clientY, appID });
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

      <Show when={recentMenu()}>
        {(m) => (
          <Menu
            data-testid="start-menu-recent-menu"
            x={m().x}
            y={m().y}
            zIndex={tokens.zStartMenu + 1}
            onDismiss={() => setRecentMenu(null)}
          >
            <MenuItem
              data-testid="start-menu-recent-remove"
              label="Remove"
              onClick={() => {
                removeRecent(m().entry, !!m().group);
                setRecentMenu(null);
              }}
            />
            {/* From a flyout, Clear is that app's: clearing the Edit row
                must not also forget folders, stations and every other
                app's files. The search list, which mixes apps, keeps the
                clear-everything verb. */}
            <Show
              when={m().group}
              fallback={
                <MenuItem
                  data-testid="start-menu-recent-clear"
                  label="Clear recent"
                  onClick={() => {
                    clearRecent();
                    setRecentMenu(null);
                  }}
                />
              }
            >
              {(g) => (
                <MenuItem
                  data-testid="start-menu-recent-clear-group"
                  label={`Clear ${g().label} history`}
                  onClick={() => {
                    clearRecent(g().id);
                    setRecentMenu(null);
                  }}
                />
              )}
            </Show>
          </Menu>
        )}
      </Show>

      <Show when={appMenu()}>
        {(m) => (
          <Menu
            data-testid="start-menu-app-menu"
            x={m().x}
            y={m().y}
            zIndex={tokens.zStartMenu + 1}
            onDismiss={() => setAppMenu(null)}
          >
            <MenuItem
              data-testid="start-menu-pin"
              label={pinned().includes(m().appID) ? 'Unpin from start' : 'Pin to start'}
              onClick={() => {
                setPin(m().appID, !pinned().includes(m().appID));
                setAppMenu(null);
              }}
            />
          </Menu>
        )}
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
    font: tokens.type.titleLg,
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
            // left/top compensated for the padding so the text still starts
            // at ~32/24 while the backdrop rect extends behind it.
            left: '20px',
            top: '14px',
            padding: '10px 14px',
            'border-radius': tokens.radiusLg,
            'box-sizing': 'border-box',
            width: 'fit-content',
            // Over the wallpaper, not chrome — a pack whose chrome text is
            // dark on a dark wallpaper (NT) overrides --wash-banner-fg.
            color: `var(--wash-banner-fg, ${tokens.fg})`,
            // Themeable darken + blur backdrop behind the panel (default off;
            // Dreamtime opts in) so the text reads over a busy wallpaper.
            background: tokens.bannerBg,
            'backdrop-filter': `blur(${tokens.bannerBlur})`,
            '-webkit-backdrop-filter': `blur(${tokens.bannerBlur})`,
            // Themed legibility halo: the pack's own surface color behind
            // the text so the banner stands out against any wallpaper —
            // a light glow on light packs, a dark one on dark packs.
            'text-shadow': `0 1px 6px ${tokens.bgWindow}, 0 0 3px ${tokens.bgWindow}`,
            font: tokens.type.textLg,
            'pointer-events': 'none',
            'max-width': '480px',
            'line-height': '1.4',
          }}
        >
          <div
            data-testid="desktop-banner-host"
            style={{
              font: tokens.type.titleLg,
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
                font: tokens.type.titleSm,
                opacity: 0.7,
              }}
            >
              {s().username || '?'}
            </span>
            <Show when={s().session_name}>
              <span
                data-testid="desktop-banner-session-name"
                style={{
                  font: tokens.type.monoMd,
                  fontWeight: 500,
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
              font: tokens.type.monoMd,
              opacity: 0.6,
              'text-shadow': '0 1px 2px rgba(0,0,0,0.6)',
            }}
          >
            {s().cpus || '?'} cores{s().arch ? ` (${s().arch})` : ''} · {formatMem(s().mem_bytes)}
          </div>
          <Show when={s().interfaces && s().interfaces.length > 0}>
            <div
              data-testid="desktop-banner-ifaces"
              style={{
                'margin-top': '2px',
                font: tokens.type.monoMd,
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
                  font: tokens.type.monoSm,
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
                      background: tokens.accentRed,
                      color: '#fff',
                      padding: '0 6px',
                      'border-radius': tokens.radiusSm,
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
    background: props.active ? `color-mix(in srgb, ${tokens.accentBlue} 28%, transparent)` : `color-mix(in srgb, ${tokens.fg} 4%, transparent)`,
    border: props.active ? `1.5px solid ${tokens.accentBlue}` : `1px solid ${tokens.borderMenu}`,
    'border-radius': tokens.radiusSm,
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
      data-wash-hit
      data-testid={`pager-cell-${props.cell.vx}-${props.cell.vy}`}
      data-active={props.active ? 'true' : 'false'}
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
      background: props.win.focused ? `color-mix(in srgb, ${tokens.accentBlue} 60%, transparent)` : `color-mix(in srgb, ${tokens.fgMuted} 28%, transparent)`,
      border: `1px solid ${props.win.focused ? tokens.accentBlue : tokens.borderFocus}`,
      'border-radius': tokens.radiusSm,
      'box-sizing': 'border-box',
      cursor: 'pointer',
    };
  };
  const onClick = (ev: MouseEvent) => {
    ev.stopPropagation();
    window.wash.setViewport(props.cell.vx, props.cell.vy);
    if (props.win.state === 'minimized') window.wash.restoreWindow(props.win.windowID, props.win.origin);
    else window.wash.focusWindow(props.win.windowID, props.win.origin);
  };
  return (
    <div
      data-wash-hit
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
      data-wash-hit
      type="button"
      title={props.title}
      data-testid={props.testid}
      ref={props.ref}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      onClick={props.onClick}
      style={{
        background: hover() ? `color-mix(in srgb, ${tokens.fg} 8%, transparent)` : 'transparent',
        color: tokens.fg,
        border: '1px solid transparent',
        width: '32px',
        height: '32px',
        'border-radius': tokens.radiusMd,
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

const WindowPill: Component<{
  win: WindowInfo;
  /** the window's app has an unread warn/error — wear the amber dot */
  attention?: boolean;
  /** called when the user visits the window, so the badge can clear */
  onVisit?: () => void;
}> = (props) => {
  const minimized = () => props.win.state === 'minimized';
  const visit = () => {
    if (props.win.state === 'minimized') window.wash.restoreWindow(props.win.windowID, props.win.origin);
    else window.wash.focusWindow(props.win.windowID, props.win.origin);
    props.onVisit?.();
  };
  return (
    <button
      data-wash-hit
      type="button"
      data-testid="taskbar-pill"
      data-attention={props.attention ? 'true' : undefined}
      title={`${minimized() ? '[minimized] ' : ''}${props.win.title}${props.attention ? ' — wants your attention' : ''} — dblclick to jump to its viewport, middle- or right-click to close`}
      onClick={visit}
      onDblClick={() => {
        // Snap the camera to the cell holding this window, then focus
        // (or restore-and-focus if minimized). Single-click already
        // fires first and is idempotent with the dblclick action —
        // both end states converge on "focused & visible".
        const v = props.win.viewport;
        window.wash.setViewport(v.vx, v.vy);
        visit();
      }}
      onContextMenu={(ev) => {
        ev.preventDefault();
        window.wash.closeWindow(props.win.windowID, props.win.origin);
      }}
      // Middle-click closes, the way it does on every browser tab strip and
      // every other taskbar. It goes through window.wash.closeWindow, so the
      // app gets the same close handshake a titlebar × gives it — an unsaved
      // editor still gets to object.
      onMouseDown={(ev) => {
        // Chromium starts autoscroll on middle mousedown; Firefox pastes the
        // X selection. Neither is what a taskbar middle-click means.
        if (ev.button === 1) ev.preventDefault();
      }}
      onAuxClick={(ev) => {
        if (ev.button !== 1) return;
        ev.preventDefault();
        window.wash.closeWindow(props.win.windowID, props.win.origin);
      }}
      style={{
        background: props.win.focused ? tokens.bgRowSelected : `color-mix(in srgb, ${tokens.fg} 4%, transparent)`,
        color: tokens.fg,
        border: `1px solid ${props.win.focused ? tokens.borderFocus : 'transparent'}`,
        padding: '0 12px',
        height: '28px',
        'border-radius': tokens.radiusMd,
        cursor: 'pointer',
        'max-width': '220px',
        // Window name on the start bar uses the title type (matches the
        // window's own titlebar — Chicago in Copland, etc.).
        font: tokens.type.titleSm,
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
      {/* Amber dot = this window said something urgent you haven't read.
          Placed after the title so it reads as a status, not an icon. */}
      <Show when={props.attention}>
        <span
          data-testid="taskbar-pill-attention"
          style={{
            width: '7px',
            height: '7px',
            'border-radius': '50%',
            background: tokens.accentAmber,
            'flex-shrink': 0,
          }}
        />
      </Show>
    </button>
  );
};

const StartMenu: Component<{
  apps: CatalogApp[];
  rootRows: CatalogApp[];
  /** recent files, newest first (session BE launcher.state) */
  recent: RecentEntry[];
  /** the Recent rows and their flyout items (launcher.ts recentGroups) */
  groups: RecentGroup[];
  /** keep the flyout up while an App-level menu it opened is showing */
  holdFlyout: boolean;
  /** pinned app ids, in pin order (session BE launcher.state) */
  pinned: string[];
  onPick: (id: string) => void;
  onOpenRecent: (path: string) => void;
  onRecentItem: (item: RecentItem) => void;
  /** `group` is the flyout the entry was right-clicked in, if any */
  onRecentContextMenu: (ev: MouseEvent, entry: RecentEntry, group?: { id: string; label: string }) => void;
  onAppContextMenu: (ev: MouseEvent, appID: string) => void;
  onDismiss: () => void;
  onLogout: () => void;
  onDisconnect: () => void;
  version?: string;
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

  // ---- search + keyboard ----
  // The menu opens with the filter focused, so the whole launcher is
  // type-then-Enter without ever reaching for the mouse. The three sections
  // (Pinned, Recent, Apps) are ONE keyboard list: `rows` flattens them in
  // render order so Arrow keys walk straight through the headers.
  const [query, setQuery] = createSignal('');
  const [selected, setSelected] = createSignal(0);
  // The cursor is the Enter target from the moment the menu opens, so
  // type-then-Enter needs no ceremony. PAINTING it on open is a different
  // matter: the top row then wears a highlight before the pointer has been
  // anywhere near it, which reads as "why is About wash already selected?".
  // So the cursor only becomes visible once the keyboard is actually
  // driving — a filter keystroke or an arrow. `data-selected` still marks
  // the Enter target throughout, because that is what it means.
  const [keyboardDriving, setKeyboardDriving] = createSignal(false);
  const pinnedApps = createMemo(() => appMatches(pinnedRows(items(), props.pinned), query()));
  // Searching flattens Recent back into matching files: a query is a
  // question about names, and making someone open four flyouts to find
  // the answer would hide it. With no query, Recent is one row per app.
  const searching = () => query().trim() !== '';
  const recentHits = createMemo(() => (searching() ? recentMatches(props.recent, query()) : []));
  const groups = createMemo(() => (searching() ? [] : props.groups));
  const appHits = createMemo(() => appMatches(items(), query()));
  // The rows are keyed by group id, not by object: every agentd push and
  // launcher.state rebuilds the group objects, and a <For> over those tore
  // down each row — and the flyout's items — under a pointer about to click.
  const groupByID = createMemo(() => new Map(groups().map((g) => [g.id, g] as const)));
  const groupIDs = createMemo(() => groups().map((g) => g.id), undefined, { equals: sameKeys });
  // The flyout reads the unfiltered groups: it is closed while searching.
  const liveGroupByID = createMemo(() => new Map(props.groups.map((g) => [g.id, g] as const)));

  // ---- Recent flyouts ----
  // A group row pops its items out to the right of the menu. The flyout is
  // a second portal <Menu>, so to the start menu a click inside it is
  // "outside"; downInFlyout, set by a capture listener registered before
  // Menu's own, is what tells the two apart.
  const [flyout, setFlyout] = createSignal<{ id: string; x: number; y: number; kb: boolean } | null>(null);
  const [flyoutSel, setFlyoutSel] = createSignal(0);
  const groupEls = new Map<string, HTMLDivElement>();
  let menuPanel: HTMLElement | null = null;
  let downInFlyout = false;
  let hoverTimer: ReturnType<typeof setTimeout> | undefined;
  // Recent pointer samples, for aim (see hoverGroup).
  let trail: PointerSample[] = [];
  onMount(() => {
    const onDown = (ev: Event) => {
      downInFlyout = !!(ev.target as Element | null)?.closest?.('[data-testid="start-menu-flyout"]');
    };
    const onMove = (ev: MouseEvent) => {
      trail = recordPointer(trail, { x: ev.clientX, y: ev.clientY, t: performance.now() });
    };
    document.addEventListener('pointerdown', onDown, true);
    document.addEventListener('mousemove', onMove, true);
    onCleanup(() => {
      document.removeEventListener('pointerdown', onDown, true);
      document.removeEventListener('mousemove', onMove, true);
      clearTimeout(hoverTimer);
    });
  });
  // Opening is idempotent: a click on a row the hover already opened must
  // not close it again. A keyboard open of a pointer-opened flyout does
  // hand it the arrows, from the top.
  const openFlyout = (id: string, kb: boolean) => {
    clearTimeout(hoverTimer);
    const cur = flyout();
    if (cur?.id === id && cur.kb === kb) return;
    const row = groupEls.get(id);
    // A timer can outlive its row (a search, a push that dropped the group):
    // a detached row measures all zeroes and would park the flyout in the
    // corner of the screen.
    if (!row || !row.isConnected) return;
    const r = row.getBoundingClientRect();
    if (!rectIsLaid(r)) return;
    const panel = menuPanel ?? row.closest<HTMLElement>('[data-testid="start-menu"]');
    // Beside the menu, its first item level with the row (the flyout's 4px
    // top padding is why y sits 4px above the row).
    const x = (panel?.getBoundingClientRect().right ?? r.right) + 2;
    setFlyoutSel(0);
    setFlyout({ id, x, y: r.top - 4, kb });
  };
  const closeFlyout = () => {
    clearTimeout(hoverTimer);
    setFlyout(null);
  };
  // Hover opens after a beat. A fixed beat alone was not enough: heading
  // diagonally for an item low in the flyout crosses the rows below its
  // own, and a pointer that takes longer than the beat to cross one swapped
  // the flyout out from under itself. So while the pointer is still AIMING
  // at the open flyout (launcher.ts aimingAt), the swap waits and is asked
  // again; a pointer that stops on a row stops aiming, and gets its row.
  const hoverGroup = (id: string) => {
    clearTimeout(hoverTimer);
    if (flyout()?.id === id) return;
    if (!flyout()) {
      hoverTimer = setTimeout(() => openFlyout(id, false), 120);
      return;
    }
    if (aimingAtFlyout()) {
      hoverTimer = setTimeout(() => hoverGroup(id), 80);
      return;
    }
    hoverTimer = setTimeout(() => openFlyout(id, false), 150);
  };
  // Leaving the Recent rows for anything else closes the flyout after a
  // beat — unless the pointer is still aiming at it: the way to a low item
  // can pass over the app rows below the Recent section.
  //
  // A pointer already inside the flyout's box counts as having arrived: its
  // own padding is outside the element that cancels these timers, so a
  // re-check landing there used to find "not aiming" and close the flyout
  // the pointer had just reached.
  const aimingAtFlyout = () => {
    const target = document.querySelector('[data-testid="start-menu-flyout"]')?.getBoundingClientRect();
    if (!target) return false;
    const p = trail[trail.length - 1];
    if (p && p.x >= target.left && p.x <= target.right && p.y >= target.top && p.y <= target.bottom) return true;
    return aimingAt(trail, performance.now(), target);
  };
  const hoverOther = () => {
    clearTimeout(hoverTimer);
    if (!flyout() || flyout()!.kb) return;
    hoverTimer = setTimeout(() => (aimingAtFlyout() ? hoverOther() : closeFlyout()), aimingAtFlyout() ? 80 : 250);
  };
  // The live group, not the one captured at open: a launcher.state push
  // while the flyout is up must show in it.
  const flyoutGroup = createMemo(() => {
    const f = flyout();
    return f ? (liveGroupByID().get(f.id) ?? null) : null;
  });
  // A group can go while its flyout is up — the last Image Viewer entry
  // removed, its file deleted. The flyout goes with it rather than keep
  // offering the items it last showed.
  createEffect(() => {
    const f = flyout();
    if (f && !liveGroupByID().has(f.id)) closeFlyout();
  });
  // Flyout items are keyed like the rows: by their stable key.
  const flyoutItemByKey = createMemo(() => new Map((flyoutGroup()?.items ?? []).map((it) => [it.key, it] as const)));
  const flyoutItemKeys = createMemo(() => (flyoutGroup()?.items ?? []).map((it) => it.key), undefined, { equals: sameKeys });
  const runItem = (item: RecentItem) => {
    closeFlyout();
    props.onRecentItem(item);
  };
  // A Recent row is the app it names first and a submenu second. It used
  // to be only the second, which made the four most-used rows in the menu
  // the one place where clicking a row named "Files" did not start Files.
  // The recents are still one hover — or one ArrowRight — away.
  const runGroup = (id: string) => {
    const app = props.apps.find((a) => a.id === id && !a.disabled);
    // A group left behind by an app that is gone (or disabled) has nothing
    // to start, so for it the click stays the submenu open it always was.
    if (!app) {
      openFlyout(id, false);
      return;
    }
    closeFlyout();
    props.onPick(app.id);
  };

  type Row = { run: () => void; group?: RecentGroup };
  const rows = createMemo<Row[]>(() => [
    ...pinnedApps().map((a) => ({ run: () => props.onPick(a.id) })),
    ...groups().map((g) => ({ run: () => runGroup(g.id), group: g })),
    ...recentHits().map((r) => ({ run: () => props.onOpenRecent(r.path) })),
    ...appHits().map((a) => ({ run: () => props.onPick(a.id) })),
  ]);
  // Section offsets into that flat list, for the per-row selected mark.
  const groupBase = createMemo(() => pinnedApps().length);
  const recentBase = createMemo(() => groupBase() + groups().length);
  const appBase = createMemo(() => recentBase() + recentHits().length);
  // A new query renumbers everything; start again at the top. It also
  // takes the Recent rows away, so any flyout goes too: a keyboard flyout
  // left up would keep Enter and the arrows while the list below shows
  // search hits, and Enter would run an item nobody was looking at. A hover
  // timer armed on a row the search just removed must not fire either.
  createEffect(() => {
    query();
    setSelected(0);
    closeFlyout();
  });
  // Typing in the filter is keyboard driving, so the cursor shows from the
  // first keystroke — which is also when it starts being useful.
  createEffect(() => {
    if (query() !== '') setKeyboardDriving(true);
  });
  const selMark = (i: number) => (i === selected() ? 'true' : undefined);
  const onKey = (ev: KeyboardEvent) => {
    // A keyboard-opened flyout takes the arrows until it is closed again;
    // Escape and ArrowLeft close only it, the way a submenu does.
    const f = flyout();
    if (f?.kb) {
      const items = flyoutGroup()?.items ?? [];
      if (ev.key === 'Escape' || ev.key === 'ArrowLeft') {
        ev.preventDefault();
        closeFlyout();
        return;
      }
      if (ev.key === 'Enter') {
        ev.preventDefault();
        const item = items[flyoutSel()];
        if (item) runItem(item);
        return;
      }
      const next = stepSelection(ev.key, flyoutSel(), items.length);
      if (next !== null) {
        ev.preventDefault();
        setFlyoutSel(next);
      }
      return;
    }
    if (ev.key === 'Escape') {
      ev.preventDefault();
      // Like a submenu: Escape closes the innermost level first.
      if (flyout()) closeFlyout();
      else props.onDismiss();
      return;
    }
    // ArrowRight is the submenu, Enter is the row: on a Recent row those
    // are now two different things — the recents, and the app it names.
    const group = rows()[selected()]?.group;
    if (ev.key === 'ArrowRight' && group) {
      ev.preventDefault();
      setKeyboardDriving(true);
      openFlyout(group.id, true);
      return;
    }
    if (ev.key === 'Enter') {
      ev.preventDefault();
      setKeyboardDriving(true);
      rows()[selected()]?.run();
      return;
    }
    const next = stepSelection(ev.key, selected(), rows().length);
    if (next === null) return;
    ev.preventDefault();
    if (!keyboardDriving()) {
      // The first arrow REVEALS the cursor where it already sits — which is
      // the row Enter would have launched all along. Stepping on this press
      // instead would walk straight past the top row without it ever having
      // been seen.
      setKeyboardDriving(true);
      return;
    }
    setSelected(next);
  };
  return (
    <>
    <Menu
      data-testid="start-menu"
      anchor="bottom-left"
      animation="slide-up"
      zIndex={tokens.zStartMenu}
      onDismiss={() => {
        if (downInFlyout) return;
        props.onDismiss();
      }}
      // overflow:hidden clips the one-shot shimmer band to the menu
      // panel so the diagonal sweep can't escape past the rounded
      // corners. Pointer-events on the shimmer div are off so it
      // doesn't intercept clicks on the rows underneath.
      // Start-menu surface is themeable independent of other menus
      // (--wash-startmenu-bg), defaulting to the pack's menu colour. Copland
      // sets it to a greyer Platinum tone so the menu matches its taskbar/
      // sidebar instead of reading lighter than the rest of the chrome.
      style={{
        'min-width': '240px',
        padding: '4px',
        overflow: 'hidden',
        background: `var(--wash-startmenu-bg, ${tokens.bgMenu})`,
      }}
    >
      <div class="wash-shimmer-sweep" aria-hidden="true" ref={(el) => queueMicrotask(() => (menuPanel = el.parentElement))} />
      {/* Brand header: the wash logo + "wash <version>" in a larger
          italic face, sitting above the launcher rows. */}
      <div
        data-testid="start-menu-brand"
        style={{
          display: 'flex',
          'align-items': 'center',
          gap: '10px',
          padding: '6px 10px 9px',
          'border-bottom': `1px solid ${tokens.borderMenu}`,
          'margin-bottom': '4px',
        }}
      >
        <img
          src={washAssetUrl('wash-logo.svg')}
          width="30"
          height="30"
          alt=""
          style={{ display: 'block', 'flex-shrink': 0 }}
        />
        <span
          style={{
            'font-size': '18px',
            'font-style': 'italic',
            'font-weight': 600,
            color: tokens.fg,
            'letter-spacing': '0.2px',
            'line-height': 1,
          }}
        >
          wash{props.version ? ` ${props.version}` : ''}
        </span>
      </div>
      <div onKeyDown={onKey}>
      {/* Filter: matches app names/ids and recent PATHS, the same rule the
          Ctrl+Space palette uses (launcher.ts). Autofocused, so opening the
          menu and typing is the fast path; Arrows/Enter/Esc are handled on
          the wrapper below so they work wherever focus sits inside. */}
      <input
        type="text"
        data-testid="start-menu-search"
        placeholder="Search…"
        value={query()}
        ref={(el) => queueMicrotask(() => el.focus())}
        onInput={(e) => setQuery(e.currentTarget.value)}
        style={{
          width: '100%',
          'box-sizing': 'border-box',
          padding: '6px 10px',
          margin: '0 0 4px',
          background: 'transparent',
          color: tokens.fg,
          border: `1px solid ${tokens.borderMenu}`,
          'border-radius': tokens.radiusSm,
          outline: 'none',
          font: tokens.type.text,
        }}
      />
      <div style={{ 'max-height': '56vh', 'overflow-y': 'auto', 'overflow-x': 'hidden' }} onScroll={closeFlyout}>
      {/* Pinned: apps the person put here by hand (right-click → Pin to
          start), in pin order, above everything the machine decided. An
          uninstalled app's id simply drops out (pinnedRows). */}
      <Show when={pinnedApps().length > 0}>
        <div data-testid="start-menu-pinned" style={sectionHeaderStyle}>Pinned</div>
        <For each={pinnedApps()}>
          {(app, i) => (
            <div
              data-selected={selMark(i())}
              style={rowSelStyle(keyboardDriving() && i() === selected())}
              onMouseEnter={hoverOther}
              onContextMenu={(ev) => props.onAppContextMenu(ev, app.id)}
            >
              <MenuItem
                data-testid="start-menu-pinned-item"
                label={app.name}
                disabled={app.disabled}
                icon={
                  app.icon ? (
                    <span style={{ color: accentFor(app), display: 'inline-flex' }}>
                      <SpriteIcon name={app.icon} size={16} />
                    </span>
                  ) : undefined
                }
                onClick={() => props.onPick(app.id)}
              />
            </div>
          )}
        </For>
      </Show>
      {/* Recent: one row per app — Files, Edit, Agent, Radio, then any
          other app with recent files — each popping out its last few
          items (launcher.ts recentGroups). The named rows show even when
          empty, so where to look is learnable before there is anything to
          find; the flyout says there is nothing yet. */}
      <Show when={groups().length > 0}>
        <div data-testid="start-menu-recent" style={sectionHeaderStyle}>Recent</div>
        <For each={groupIDs()}>
          {(id, i) => {
            const group = () => groupByID().get(id);
            const label = () => group()?.label ?? '';
            const app = () => props.apps.find((a) => a.id === id);
            const open = () => flyout()?.id === id;
            let rowEl: HTMLDivElement | undefined;
            onCleanup(() => {
              if (rowEl && groupEls.get(id) === rowEl) groupEls.delete(id);
            });
            return (
              <div
                ref={(el) => {
                  rowEl = el;
                  groupEls.set(id, el);
                }}
                data-selected={selMark(groupBase() + i())}
                data-open={open() ? 'true' : undefined}
                style={rowSelStyle((keyboardDriving() && groupBase() + i() === selected()) || open())}
                onMouseEnter={() => hoverGroup(id)}
                onMouseLeave={() => clearTimeout(hoverTimer)}
              >
                <MenuItem
                  data-testid={`start-menu-recent-group-${label().toLowerCase().replace(/[^a-z0-9]+/g, '-')}`}
                  label={label()}
                  title={app() ? `Open ${label()} — its recent items are in the submenu` : undefined}
                  icon={
                    <span style={{ color: app() ? accentFor(app()!) : tokens.fgMuted, display: 'inline-flex' }}>
                      <SpriteIcon name={app()?.icon ?? groupIcon(id)} size={16} />
                    </span>
                  }
                  // Both a launcher and a submenu trigger. "Files" was
                  // only ever the second, which made the most-used rows in
                  // the menu the one place a click did not start the app
                  // it names. Hovering (or ArrowRight) still pops the
                  // recents out to the side; the chevron says so.
                  popup={{ expanded: open() }}
                  trailing={<span aria-hidden="true" style={{ color: tokens.fgMuted }}>›</span>}
                  // A group whose app is not installed — a recent file
                  // left behind by one that was removed — has nothing to
                  // launch, so for it the click still opens the flyout.
                  // That open is idempotent: the hover has usually opened
                  // it by the time the click lands, and a toggle would
                  // close it again.
                  onClick={() => runGroup(id)}
                />
              </div>
            );
          }}
        </For>
      </Show>
      {/* Searching: matching recent files, flat, newest first. A row
          re-issues the open through the session BE (same ext → handler
          resolution as the original double-click); right-click offers
          Remove / Clear recent. */}
      <Show when={recentHits().length > 0}>
        <div data-testid="start-menu-recent-hits" style={sectionHeaderStyle}>Recent</div>
        <For each={recentHits()}>
          {(r, i) => (
            <div
              data-selected={selMark(recentBase() + i())}
              style={rowSelStyle(keyboardDriving() && recentBase() + i() === selected())}
              onContextMenu={(ev) => props.onRecentContextMenu(ev, r)}
            >
              <MenuItem
                data-testid="start-menu-recent-item"
                label={recentName(r.path)}
                icon={
                  <span style={{ color: tokens.fgMuted, display: 'inline-flex' }}>
                    <SpriteIcon name="file-text" size={16} />
                  </span>
                }
                trailing={
                  <span
                    title={r.path}
                    style={{
                      color: tokens.fgMuted,
                      'font-size': tokens.fontSizeSm,
                      'max-width': '160px',
                      overflow: 'hidden',
                      'text-overflow': 'ellipsis',
                      'white-space': 'nowrap',
                    }}
                  >
                    {recentDir(r.path)}
                  </span>
                }
                onClick={() => props.onOpenRecent(r.path)}
              />
            </div>
          )}
        </For>
      </Show>
      <Show when={pinnedApps().length > 0 || groups().length > 0 || recentHits().length > 0}>
        <div data-testid="start-menu-apps" style={sectionHeaderStyle}>Apps</div>
      </Show>
      <Show
        when={appHits().length > 0}
        fallback={<div style={emptyStyle}>{items().length === 0 ? 'no apps registered' : 'no matches'}</div>}
      >
        <For each={appHits()}>
          {(app, i) => {
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
                <SpriteIcon name={app.icon} size={16} />
              </span>
            ) : undefined;
            return (
              <div
                data-selected={selMark(appBase() + i())}
                style={rowSelStyle(keyboardDriving() && appBase() + i() === selected())}
                onMouseEnter={hoverOther}
                onContextMenu={(ev) => props.onAppContextMenu(ev, app.id)}
              >
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
              </div>
            );
          }}
        </For>
      </Show>
      </div>
      </div>
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
        icon={<SpriteIcon name="unplug" size={16} />}
        onClick={() => props.onDisconnect()}
      />
      <MenuItem
        data-testid="start-menu-logout"
        label="Log out"
        icon={<SpriteIcon name="log-out" size={16} />}
        onClick={() => props.onLogout()}
      />
    </Menu>
    <Show when={flyoutGroup()}>
      {(group) => (
        <Menu
          data-testid="start-menu-flyout"
          x={flyout()!.x}
          y={flyout()!.y}
          zIndex={tokens.zStartMenu + 1}
          style={{ 'min-width': '220px', 'max-width': '360px', padding: '4px' }}
          onDismiss={() => {
            // A click back on the start menu (another group, the search
            // box) or on the Remove menu this flyout opened is not a reason
            // to go; everything else outside is.
            if (props.holdFlyout) return;
            setTimeout(() => {
              if (!downInFlyout && !menuPanel?.matches(':hover')) closeFlyout();
            }, 0);
          }}
        >
          <div data-group={group().id} onMouseEnter={() => clearTimeout(hoverTimer)}>
            <Show
              when={group().items.length > 0}
              fallback={<MenuItem data-testid="start-menu-flyout-empty" label={group().empty} disabled onClick={() => {}} />}
            >
              <For each={flyoutItemKeys()}>
                {(key, i) => {
                  // Read through the map so a push that rebuilds the item
                  // (a new verb, a new title) updates this row in place.
                  const item = () => flyoutItemByKey().get(key);
                  const action = () => {
                    const it = item();
                    return it?.kind === 'agent' ? it.action : undefined;
                  };
                  const detail = () => {
                    const it = item();
                    return it && it.kind !== 'station' ? it.detail : '';
                  };
                  const detailTitle = () => {
                    const it = item();
                    return it?.kind === 'path' ? it.entry.path : it?.kind === 'agent' ? it.session.cwd : undefined;
                  };
                  const sel = () => !!flyout()?.kb && i() === flyoutSel();
                  return (
                    <Show when={item()}>
                      {(it) => (
                        <div
                          data-kind={it().kind}
                          data-action={action()}
                          data-selected={sel() ? 'true' : undefined}
                          style={rowSelStyle(sel())}
                          onContextMenu={(ev) => {
                            const cur = it();
                            // Agent sessions are history agentd owns; the
                            // Agents app is where they are renamed and deleted.
                            if (cur.kind === 'agent') return;
                            props.onRecentContextMenu(ev, cur.entry, { id: group().id, label: group().label });
                          }}
                        >
                          <MenuItem
                            data-testid="start-menu-flyout-item"
                            label={it().label}
                            icon={
                              <span style={{ color: tokens.fgMuted, display: 'inline-flex' }}>
                                <SpriteIcon name={it().icon} size={16} />
                              </span>
                            }
                            trailing={
                              detail() ? (
                                <span
                                  title={detailTitle()}
                                  style={{
                                    color: tokens.fgMuted,
                                    'font-size': tokens.fontSizeSm,
                                    // Narrow screens give the name the room:
                                    // a 160px path left "note-0.md" wrapping.
                                    'max-width': 'min(160px, 25vw)',
                                    'min-width': 0,
                                    overflow: 'hidden',
                                    'text-overflow': 'ellipsis',
                                    'white-space': 'nowrap',
                                  }}
                                >
                                  {detail()}
                                </span>
                              ) : undefined
                            }
                            onClick={() => runItem(it())}
                          />
                        </div>
                      )}
                    </Show>
                  );
                }}
              </For>
            </Show>
          </div>
        </Menu>
      )}
    </Show>
    </>
  );
};

// groupIcon is a Recent row's icon when its app is not in the catalog (the
// Agents app on a host without it, say).
function groupIcon(appID: string): string {
  switch (appID) {
    case 'com.wash.fm':
      return 'folder';
    case 'com.wash.edit':
      return 'file-pen';
    case 'com.wash.agents':
      return 'bot';
    case 'com.wash.radio':
      return 'radio';
  }
  return 'file-text';
}

const Palette: Component<{
  inputRef: (el: HTMLInputElement) => void;
  query: string;
  onQueryChange: (v: string) => void;
  results: PaletteEntry[];
  selected: number;
  isRootRowID: (id: string) => boolean;
  onHover: (i: number) => void;
  onPick: () => void;
  onKey: (ev: KeyboardEvent) => void;
  onClose: () => void;
}> = (props) => {
  return (
    <div
      // Dismiss backdrop — a click on the scrim closes the palette, but the
      // scrim is not a thing you point at.
      data-wash-no-hit
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
        background: tokens.bgBackdrop,
        'z-index': 10002,
        animation: 'wash-fade-in 120ms ease-out',
      }}
    >
      <div
        style={{
          background: tokens.bgWindow,
          border: `1px solid ${tokens.borderMenu}`,
          'border-radius': tokens.radiusXl,
          'min-width': '380px',
          'max-width': '520px',
          'box-shadow': '0 16px 48px rgba(0,0,0,0.6)',
          overflow: 'hidden',
          animation: 'wash-pop-in 140ms ease-out',
        }}
      >
        <input
          type="text"
          placeholder="Search apps and recent files…"
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
            color: tokens.fg,
            border: 'none',
            'border-bottom': `1px solid ${tokens.borderMenu}`,
            outline: 'none',
            font: tokens.type.textLg,
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
  app: PaletteEntry;
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
      data-wash-hit
      type="button"
      data-testid={`palette-item-${props.app.id}`}
      data-path={props.app.recent?.path}
      ref={el!}
      onMouseEnter={props.onHover}
      onClick={props.onPick}
      style={{
        display: 'flex',
        'align-items': 'center',
        gap: '10px',
        width: '100%',
        padding: '8px 16px',
        background: props.selected ? tokens.bgRowSelected : 'transparent',
        color: tokens.fg,
        border: 'none',
        'text-align': 'left',
        cursor: 'pointer',
        font: tokens.type.textLg,
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
      <span style={{ opacity: 0.55, 'font-size': '12px' }}>{props.app.subtitle ?? props.app.id}</span>
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
  // Configurable backdrop: a pack sets the color (--wash-taskbar-bg), the
  // opacity of that color over the surface behind (--wash-taskbar-opacity,
  // default 80%), and the frost blur (--wash-taskbar-blur, default 6px).
  // Sunken grey at 80% by default so the bar reads a touch darker than
  // windows while letting more of the wallpaper through; a pack can make it
  // solid, more transparent, or unblurred.
  background: `color-mix(in srgb, var(--wash-taskbar-bg, ${tokens.bgInset}) var(--wash-taskbar-opacity, 80%), transparent)`,
  'backdrop-filter': 'blur(var(--wash-taskbar-blur, 6px))',
  '-webkit-backdrop-filter': 'blur(var(--wash-taskbar-blur, 6px))',
  // Top edge: a pack can paint a raised highlight here (NT does).
  'border-top': `1px solid var(--wash-taskbar-top, ${tokens.borderMenu})`,
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
  'border-bottom': `1px solid ${tokens.borderMenu}`,
};

const separatorStyle: JSX.CSSProperties = {
  width: '1px',
  height: '22px',
  background: tokens.borderMenu,
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
  color: tokens.fgMuted,
  'white-space': 'nowrap',
  'pointer-events': 'none',
};

const clockStyle: JSX.CSSProperties = {
  padding: '0 14px',
  'font-variant-numeric': 'tabular-nums',
  opacity: 0.7,
  'font-size': '13px',
};

// Keyboard highlight for a start-menu row. MenuItem paints itself
// transparent at rest, so a background on the wrapper reads as the row's
// own selection — no fork of the shared component to add one prop.
const rowSelStyle = (on: boolean): JSX.CSSProperties => ({
  background: on ? tokens.bgRowSelected : 'transparent',
  'border-radius': tokens.radiusSm,
});

// Section label inside the start menu ("Recent", "Pinned", "Apps").
const sectionHeaderStyle: JSX.CSSProperties = {
  padding: '6px 10px 2px',
  color: tokens.fgMuted,
  font: tokens.type.textSm,
  'text-transform': 'uppercase',
  'letter-spacing': '0.6px',
  'user-select': 'none',
};

const emptyStyle: JSX.CSSProperties = {
  padding: '10px 14px',
  color: tokens.fgMuted,
  'font-size': '13px',
};

// ---- custom element wrapper ----

defineWashApp('wash-app-session', (props) => <App {...props} />, {
  style: `display:block;position:absolute;inset:0;background:radial-gradient(circle at 30% 20%, #1a1a32 0, #0a0a18 75%);color:${tokens.fg};font:${tokens.type.textLg};overflow:hidden`,
});
