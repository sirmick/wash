// wash-app-session: the wash desktop chrome. Renders the desktop
// background, a bottom taskbar (start menu / open-window list /
// screenshot button / clock), the start menu, the command palette,
// and the screenshot capture flow. Bridges launcher clicks back to
// the BE half via app_msg.
//
// Solid drives the UI; ad-hoc state (which menu is open, palette
// input, screenshot status, etc.) lives in signals instead of class
// fields scattered across multiple render methods.

import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Menu, MenuItem, defineWashApp, tokens } from '@wash/ui';
import { toBlob } from 'html-to-image';
import { Camera, Search } from 'lucide-solid';

interface CatalogApp {
  id: string;
  name: string;
  icon?: string;
  surface: string;
  instancing: string;
  disabled?: boolean;
  reason?: string;
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
interface SystemInfoMsg {
  kind: 'system.info';
  hostname: string;
  fqdn: string;
  username: string;
  cpus: number;
  mem_bytes: number;
  ips: string[];
}

// rootTerminalEntry — synthetic launcher item exposed in StartMenu
// + Palette. Module-level (not closed over App's scope) so the
// paletteResults memo can read it during its eager initial run
// without a TDZ on the still-uninitialised inner binding.
const rootTerminalEntry: CatalogApp = {
  id: '__root-terminal',
  name: 'Root Terminal',
  icon: 'shield-alert',
  surface: 'window',
  instancing: 'multi',
  disabled: false,
};

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
  let screenshotTimer = 0;
  let currentObjectURL: string | null = null;

  let paletteInputEl: HTMLInputElement | undefined;
  let startBtnEl: HTMLButtonElement | undefined;

  // Filtered palette results. Root Terminal is mixed into the
  // normal catalog and sorted by name like everything else — the
  // red row already makes it stand out, no pinning needed.
  const paletteResults = createMemo(() => {
    const q = paletteQuery().trim().toLowerCase();
    const apps = [...catalog().filter((a) => !a.disabled), rootTerminalEntry];
    apps.sort((a, b) => a.name.localeCompare(b.name));
    if (!q) return apps;
    return apps.filter((a) => a.id.toLowerCase().includes(q) || a.name.toLowerCase().includes(q));
  });

  const launchApp = (appID: string) => {
    window.wash.sendAppMsg(props.instance, { action: 'launch', app_id: appID });
  };

  // launchRootTerminal routes a "spawn as root" through the session
  // BE → wash-priv. The user sees the approval row in wash-priv's
  // window with router-attested sender (com.wash.session). Picking
  // this from the menu does NOT bypass the password modal.
  const launchRootTerminal = () => {
    window.wash.sendAppMsg(props.instance, { action: 'spawn_root', app_id: 'com.wash.term' });
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
    if (app.id === rootTerminalEntry.id) launchRootTerminal();
    else launchApp(app.id);
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
    const onMsg = (ev: Event) => {
      const data = (ev as CustomEvent).detail as { kind?: string };
      if (!data) return;
      switch (data.kind) {
        case 'desktop.config':
          applyDesktopConfig(data as DesktopConfigMsg);
          return;
        case 'system.info':
          setSysInfo(data as SystemInfoMsg);
          return;
      }
    };
    props.host.addEventListener('wash:msg', onMsg);
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
      document.removeEventListener('mousedown', onDocMouseDown);
      document.removeEventListener('keydown', onKey);
      clearInterval(clockId);
      clearInterval(offClockSwap);
      if (screenshotTimer) clearTimeout(screenshotTimer);
      if (currentObjectURL) URL.revokeObjectURL(currentObjectURL);
    });
  });

  return (
    <>
      <Banner info={sysInfo} />
      <Pager
        windows={windows}
        vp={vp}
        screen={screen}
        taskbarPos={taskbarPosition}
      />
      <div style={taskbarPosition() === 'top' ? taskbarStyleTop : taskbarStyle}>
        <IconButton
          ref={(el) => (startBtnEl = el)}
          title="Apps"
          onClick={toggleMenu}
        >
          <img src="/wash-logo.svg" width="20" height="20" alt="wash" style={{ display: 'block' }} />
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
        <span style={clockStyle}>{clock()}</span>
      </div>

      <Show when={menuOpen()}>
        <StartMenu
          apps={catalog()}
          rootTerminal={rootTerminalEntry}
          onDismiss={() => setMenuOpen(false)}
          onPick={(id) => {
            setMenuOpen(false);
            if (id === rootTerminalEntry.id) launchRootTerminal();
            else launchApp(id);
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
          rootTerminalID={rootTerminalEntry.id}
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
    <use href={`/icons.svg#${props.name}`} />
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
            }}
          >
            {s().fqdn || s().hostname || 'wash'}
          </div>
          <div
            style={{
              'margin-top': '4px',
              opacity: 0.7,
              'text-shadow': '0 1px 2px rgba(0,0,0,0.6)',
            }}
          >
            <span data-testid="desktop-banner-user" style={{ 'font-weight': 700 }}>
              {s().username || '?'}
            </span>
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
          <Show when={s().ips && s().ips.length > 0}>
            <div
              data-testid="desktop-banner-ips"
              style={{
                'margin-top': '2px',
                font: '12px ui-monospace,Menlo,Consolas,monospace',
                opacity: 0.55,
                'text-shadow': '0 1px 2px rgba(0,0,0,0.6)',
                'word-break': 'break-all',
              }}
            >
              {s().ips.join('  ')}
            </div>
          </Show>
        </div>
      )}
    </Show>
  );
};

// Pager renders the 3x3 virtual-desktop overview as a panel parked
// just above the taskbar. Each cell is a scaled-down rect of the
// real viewport; window outlines inside each cell preview where
// they live across the plane. Click a cell to pan; click a window
// to pan + focus.
const PAGER_CELL_W = 56;
const PAGER_GAP = 3;
const PAGER_PAD = 6;

const Pager: Component<{
  windows: () => WindowInfo[];
  vp: () => { vx: number; vy: number };
  screen: () => { w: number; h: number };
  taskbarPos: () => 'top' | 'bottom';
}> = (props) => {
  const perAxis = window.wash.viewports().perAxis;
  const cellH = () => {
    const s = props.screen();
    const aspect = s.h / Math.max(1, s.w);
    return Math.round(PAGER_CELL_W * aspect);
  };
  const panelW = () => perAxis * PAGER_CELL_W + (perAxis - 1) * PAGER_GAP + PAGER_PAD * 2;
  const panelH = () => perAxis * cellH() + (perAxis - 1) * PAGER_GAP + PAGER_PAD * 2;
  const containerStyle = () => {
    const s: JSX.CSSProperties = {
      position: 'absolute',
      right: '14px',
      width: `${panelW()}px`,
      height: `${panelH()}px`,
      background: 'rgba(15,15,30,0.75)',
      'backdrop-filter': 'blur(10px)',
      '-webkit-backdrop-filter': 'blur(10px)',
      border: '1px solid #2a2a4a',
      'border-radius': '8px',
      padding: `${PAGER_PAD}px`,
      'box-sizing': 'border-box',
      'box-shadow': '0 4px 14px rgba(0,0,0,0.4)',
      'z-index': 9999, // just below the taskbar (10000) so the bar wins on overlap
      'user-select': 'none',
    };
    // 16px gap from the taskbar on whichever edge it lives.
    if (props.taskbarPos() === 'top') s.top = '56px';
    else s.bottom = '56px';
    return s;
  };
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
            // Without this, the filter was a one-shot snapshot taken
            // at child-mount time — windows that landed in the store
            // after the cell mounted (snapshot replay, drags, new
            // spawns) never showed up, producing the "random" outlines
            // the user saw on browser refresh.
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
  cellH: number;
  active: boolean;
  windows: WindowInfo[];
  screen: { w: number; h: number };
}> = (props) => {
  const left = () => props.cell.vx * (PAGER_CELL_W + PAGER_GAP);
  const top = () => props.cell.vy * (props.cellH + PAGER_GAP);
  const cellStyle = (): JSX.CSSProperties => ({
    position: 'absolute',
    left: `${left()}px`,
    top: `${top()}px`,
    width: `${PAGER_CELL_W}px`,
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
        {(w) => <PagerWindow win={w} cell={props.cell} cellH={props.cellH} screen={props.screen} />}
      </For>
    </div>
  );
};

const PagerWindow: Component<{
  win: WindowInfo;
  cell: { vx: number; vy: number };
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
    const scaleX = PAGER_CELL_W / Math.max(1, s.w);
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

// rootMenuItemStyle + rootMenuBadgeStyle are declared above StartMenu
// because Solid's JSX compile path evaluates inline style references
// at module init in some configurations — declaring after the
// component triggers a TDZ "cannot access X before initialization"
// error on the bundle's first run.
// Styled to match MenuItem dimensions exactly — same padding, gap,
// icon slot width — so the Root Terminal label lines up with the
// labels of normal entries above and below it. Only the colours
// differ.
const rootMenuItemStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '10px',
  width: '100%',
  padding: '6px 14px',
  background: '#7a1f1f',
  color: '#fff',
  border: 'none',
  'border-radius': '4px',
  cursor: 'pointer',
  font: '13px ui-sans-serif,system-ui,sans-serif',
  'text-align': 'left',
};

const StartMenu: Component<{
  apps: CatalogApp[];
  rootTerminal?: CatalogApp;
  onPick: (id: string) => void;
  onDismiss: () => void;
}> = (props) => {
  // Merge the synthetic Root Terminal in with the catalog and sort
  // alphabetically — the red row stands out on its own, no pinning.
  const items = createMemo(() => {
    const merged = props.rootTerminal
      ? [...props.apps, props.rootTerminal]
      : props.apps.slice();
    merged.sort((a, b) => a.name.localeCompare(b.name));
    return merged;
  });
  return (
    <Menu
      data-testid="start-menu"
      anchor="bottom-left"
      animation="slide-up"
      zIndex={tokens.zStartMenu}
      onDismiss={props.onDismiss}
      style={{ 'min-width': '240px', padding: '4px' }}
    >
      <Show when={items().length > 0} fallback={<div style={emptyStyle}>no apps registered</div>}>
        <For each={items()}>
          {(app) => {
            const isRoot = props.rootTerminal && app.id === props.rootTerminal.id;
            if (isRoot) {
              return (
                <button
                  type="button"
                  data-testid="start-menu-root-terminal"
                  onClick={() => props.onPick(app.id)}
                  style={rootMenuItemStyle}
                  onMouseEnter={(e) => { (e.currentTarget as HTMLButtonElement).style.background = '#a02828'; }}
                  onMouseLeave={(e) => { (e.currentTarget as HTMLButtonElement).style.background = '#7a1f1f'; }}
                >
                  {/* Match MenuItem's 22×22 icon slot so the label
                      lines up across the catalog/root mix. */}
                  <span
                    style={{
                      width: '22px',
                      height: '22px',
                      'flex-shrink': 0,
                      display: 'inline-flex',
                      'align-items': 'center',
                      'justify-content': 'center',
                    }}
                  >
                    <SpriteIcon name={app.icon!} size={20} />
                  </span>
                  <span style={{ flex: 1, 'text-align': 'left' }}>{app.name}</span>
                </button>
              );
            }
            return (
              <MenuItem
                label={app.name}
                disabled={app.disabled}
                icon={app.icon ? <SpriteIcon name={app.icon} size={20} /> : undefined}
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
    </Menu>
  );
};

const Palette: Component<{
  inputRef: (el: HTMLInputElement) => void;
  query: string;
  onQueryChange: (v: string) => void;
  results: CatalogApp[];
  selected: number;
  rootTerminalID?: string;
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
                  isRoot={app.id === props.rootTerminalID}
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
        background: props.isRoot
          ? (props.selected ? '#a02828' : '#7a1f1f')
          : (props.selected ? '#2a2a4a' : 'transparent'),
        color: props.isRoot ? '#fff' : '#eee',
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
        }}
      >
        <Show when={props.app.icon}>
          <SpriteIcon name={props.app.icon!} size={18} />
        </Show>
      </span>
      <span style={{ flex: 1 }}>{props.app.name}</span>
      <Show when={!props.isRoot}>
        <span style={{ opacity: 0.55, 'font-size': '12px' }}>{props.app.id}</span>
      </Show>
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
