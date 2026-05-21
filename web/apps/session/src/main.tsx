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
import { render } from 'solid-js/web';
import type { Component, JSX } from 'solid-js';
import { toBlob } from 'html-to-image';
import { Camera, Menu as MenuIcon, Search } from 'lucide-solid';

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
  title: string;
  focused: boolean;
  state: 'normal' | 'minimized' | 'maximized';
}

declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
      catalog(): CatalogApp[];
      onCatalog(cb: (apps: CatalogApp[]) => void): () => void;
      windows(): WindowInfo[];
      onWindowsChanged(cb: (windows: WindowInfo[]) => void): () => void;
      focusWindow(id: number): void;
      closeWindow(id: number): void;
      restoreWindow(id: number): void;
    };
  }
}

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  // ---- reactive state ----
  const [catalog, setCatalog] = createSignal<CatalogApp[]>(window.wash.catalog());
  const [windows, setWindows] = createSignal<WindowInfo[]>(window.wash.windows());
  const [menuOpen, setMenuOpen] = createSignal(false);
  const [paletteOpen, setPaletteOpen] = createSignal(false);
  const [paletteQuery, setPaletteQuery] = createSignal('');
  const [paletteSelected, setPaletteSelected] = createSignal(0);
  const [clock, setClock] = createSignal(formatClock());
  const [screenshotStatus, setScreenshotStatus] = createSignal('');
  const [screenshotVisible, setScreenshotVisible] = createSignal(false);
  let screenshotTimer = 0;

  let paletteInputEl: HTMLInputElement | undefined;
  let startBtnEl: HTMLButtonElement | undefined;

  // Filtered palette results.
  const paletteResults = createMemo(() => {
    const q = paletteQuery().trim().toLowerCase();
    const apps = catalog().filter((a) => !a.disabled);
    if (!q) return apps;
    return apps.filter((a) => a.id.toLowerCase().includes(q) || a.name.toLowerCase().includes(q));
  });

  const launchApp = (appID: string) => {
    window.wash.sendAppMsg(props.instance, { action: 'launch', app_id: appID });
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
    launchApp(app.id);
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

    // Outside-click closes menus/palette.
    const onDocMouseDown = (ev: MouseEvent) => {
      const t = ev.target as Node;
      if (menuOpen()) {
        const menuEl = props.host.querySelector('[data-testid="start-menu"]');
        if (menuEl && !menuEl.contains(t) && startBtnEl && !startBtnEl.contains(t)) {
          setMenuOpen(false);
        }
      }
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
    const clockId = window.setInterval(() => setClock(formatClock()), 30_000);

    onCleanup(() => {
      offCat();
      offWin();
      document.removeEventListener('mousedown', onDocMouseDown);
      document.removeEventListener('keydown', onKey);
      clearInterval(clockId);
      if (screenshotTimer) clearTimeout(screenshotTimer);
    });
  });

  return (
    <>
      <Banner />
      <div style={taskbarStyle}>
        <IconButton
          ref={(el) => (startBtnEl = el)}
          title="Apps"
          onClick={toggleMenu}
        >
          <MenuIcon size={18} />
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
          onPick={(id) => {
            setMenuOpen(false);
            launchApp(id);
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

const Banner: Component = () => (
  <div
    style={{
      position: 'absolute',
      left: '32px',
      top: '28px',
      font: '600 22px system-ui,sans-serif',
      'letter-spacing': '0.05em',
      opacity: 0.35,
    }}
  >
    wash
  </div>
);

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
      title={`${minimized() ? '[minimized] ' : ''}${props.win.title} — right-click to close`}
      onClick={() => {
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
        overflow: 'hidden',
        'text-overflow': 'ellipsis',
        'white-space': 'nowrap',
        font: '13px system-ui,sans-serif',
        'flex-shrink': 0,
        opacity: minimized() ? 0.6 : 1,
        'font-style': minimized() ? 'italic' : 'normal',
      }}
    >
      {props.win.title}
    </button>
  );
};

const StartMenu: Component<{
  apps: CatalogApp[];
  onPick: (id: string) => void;
}> = (props) => (
  <div data-testid="start-menu" style={menuBoxStyle}>
    <Show when={props.apps.length > 0} fallback={<div style={emptyStyle}>no apps registered</div>}>
      <For each={props.apps}>
        {(app) => <MenuEntry app={app} onPick={() => props.onPick(app.id)} />}
      </For>
    </Show>
  </div>
);

const MenuEntry: Component<{ app: CatalogApp; onPick: () => void }> = (props) => {
  const [hover, setHover] = createSignal(false);
  return (
    <button
      type="button"
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      onClick={() => {
        if (!props.app.disabled) props.onPick();
      }}
      style={{
        display: 'flex',
        'align-items': 'center',
        gap: '10px',
        width: '100%',
        padding: '8px 12px',
        background: !props.app.disabled && hover() ? '#2a2a4a' : 'transparent',
        color: '#eee',
        border: 'none',
        'border-radius': '4px',
        cursor: props.app.disabled ? 'not-allowed' : 'pointer',
        opacity: props.app.disabled ? 0.5 : 1,
        'text-align': 'left',
        font: '14px system-ui,sans-serif',
      }}
    >
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
        <Show when={props.app.icon}>
          <SpriteIcon name={props.app.icon!} size={20} />
        </Show>
      </span>
      <span>{props.app.name}</span>
      <Show when={props.app.disabled}>
        <span style={{ 'margin-left': 'auto', color: '#888', 'font-size': '12px' }}>
          {props.app.reason ? '· ' + props.app.reason : '· disabled'}
        </span>
      </Show>
    </button>
  );
};

const Palette: Component<{
  inputRef: (el: HTMLInputElement) => void;
  query: string;
  onQueryChange: (v: string) => void;
  results: CatalogApp[];
  selected: number;
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

function formatClock(): string {
  return new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
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

const menuBoxStyle: JSX.CSSProperties = {
  position: 'absolute',
  left: '6px',
  bottom: '46px',
  background: '#15152a',
  border: '1px solid #2a2a4a',
  'border-radius': '8px',
  padding: '4px',
  'min-width': '240px',
  'box-shadow': '0 8px 24px rgba(0,0,0,0.5)',
  'z-index': 10001,
  'transform-origin': 'bottom left',
  animation: 'wash-slide-up 140ms ease-out',
};

const emptyStyle: JSX.CSSProperties = {
  padding: '10px 14px',
  color: '#888',
  'font-size': '13px',
};

// ---- custom element wrapper ----

class WashAppSession extends HTMLElement {
  private cleanup?: () => void;

  connectedCallback() {
    this.style.cssText = [
      'display:block',
      'position:absolute',
      'inset:0',
      'background:radial-gradient(circle at 30% 20%, #1a1a32 0, #0a0a18 75%)',
      'color:#eee',
      'font:14px system-ui,sans-serif',
      'overflow:hidden',
    ].join(';');

    const instance = this.getAttribute('data-wash-instance') ?? '';
    this.cleanup = render(() => <App instance={instance} host={this} />, this);
  }

  disconnectedCallback() {
    this.cleanup?.();
    this.cleanup = undefined;
  }
}

if (!customElements.get('wash-app-session')) {
  customElements.define('wash-app-session', WashAppSession);
}
