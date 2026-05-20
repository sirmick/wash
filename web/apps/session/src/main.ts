// wash-app-session: the wash desktop chrome. Renders the desktop
// background, a bottom taskbar (start menu / open-window list / clock),
// and bridges launcher clicks back to the BE half via app_msg.

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

class WashAppSession extends HTMLElement {
  private instance = '';
  private taskbar!: HTMLDivElement;
  private windowList!: HTMLDivElement;
  private clock!: HTMLSpanElement;
  private startBtn!: HTMLButtonElement;
  private menu: HTMLDivElement | null = null;
  private cleanups: Array<() => void> = [];

  connectedCallback() {
    this.instance = this.getAttribute('data-wash-instance') ?? '';

    this.style.cssText = [
      'display:block',
      'position:absolute',
      'inset:0',
      'background:radial-gradient(circle at 30% 20%, #1a1a32 0, #0a0a18 75%)',
      'color:#eee',
      'font:14px system-ui,sans-serif',
      'overflow:hidden',
    ].join(';');

    this.appendChild(this.buildBanner());
    this.taskbar = this.buildTaskbar();
    this.appendChild(this.taskbar);

    this.cleanups.push(window.wash.onCatalog(() => this.onCatalogChange()));
    this.cleanups.push(window.wash.onWindowsChanged((w) => this.renderWindowList(w)));

    const tick = () => {
      this.clock.textContent = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    };
    tick();
    const id = window.setInterval(tick, 30_000);
    this.cleanups.push(() => clearInterval(id));
  }

  disconnectedCallback() {
    for (const c of this.cleanups) c();
    this.cleanups = [];
  }

  // ---- banner ----

  private buildBanner(): HTMLElement {
    const banner = document.createElement('div');
    banner.textContent = 'wash';
    banner.style.cssText = [
      'position:absolute',
      'left:32px',
      'top:28px',
      'font:600 22px system-ui,sans-serif',
      'letter-spacing:0.05em',
      'opacity:0.35',
    ].join(';');
    return banner;
  }

  // ---- taskbar ----

  private buildTaskbar(): HTMLDivElement {
    const bar = document.createElement('div');
    bar.style.cssText = [
      'position:absolute',
      'left:0',
      'right:0',
      'bottom:0',
      'height:40px',
      'background:rgba(15,15,30,0.85)',
      'backdrop-filter:blur(10px)',
      '-webkit-backdrop-filter:blur(10px)',
      'border-top:1px solid #2a2a4a',
      'display:flex',
      'align-items:center',
      'gap:4px',
      'padding:0 6px',
      'z-index:10000',
      'box-sizing:border-box',
    ].join(';');

    this.startBtn = this.buildIconButton(hamburgerSVG(), 'Apps');
    this.startBtn.addEventListener('click', () => this.toggleMenu());
    bar.appendChild(this.startBtn);

    const sep = document.createElement('div');
    sep.style.cssText = 'width:1px;height:22px;background:#2a2a4a;margin:0 4px;flex-shrink:0;';
    bar.appendChild(sep);

    this.windowList = document.createElement('div');
    this.windowList.style.cssText = [
      'flex:1',
      'display:flex',
      'align-items:center',
      'gap:4px',
      'overflow-x:auto',
      'overflow-y:hidden',
      'scrollbar-width:none',
    ].join(';');
    bar.appendChild(this.windowList);

    this.clock = document.createElement('span');
    this.clock.style.cssText = 'padding:0 14px;font-variant-numeric:tabular-nums;opacity:0.7;font-size:13px;';
    bar.appendChild(this.clock);

    return bar;
  }

  private buildIconButton(svg: string, title: string): HTMLButtonElement {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.innerHTML = svg;
    btn.title = title;
    btn.style.cssText = [
      'background:transparent',
      'color:#eee',
      'border:1px solid transparent',
      'width:32px',
      'height:32px',
      'border-radius:4px',
      'cursor:pointer',
      'display:flex',
      'align-items:center',
      'justify-content:center',
      'flex-shrink:0',
    ].join(';');
    btn.addEventListener('mouseenter', () => {
      btn.style.background = 'rgba(255,255,255,0.08)';
    });
    btn.addEventListener('mouseleave', () => {
      btn.style.background = 'transparent';
    });
    return btn;
  }

  // ---- window list ----

  private renderWindowList(wins: WindowInfo[]) {
    this.windowList.replaceChildren();
    for (const w of wins) {
      this.windowList.appendChild(this.buildWindowTab(w));
    }
  }

  private buildWindowTab(w: WindowInfo): HTMLElement {
    const btn = document.createElement('button');
    btn.type = 'button';
    const minimized = w.state === 'minimized';
    btn.title =
      (minimized ? '[minimized] ' : '') + w.title + ' — right-click to close';
    btn.textContent = w.title;
    btn.style.cssText = [
      `background:${w.focused ? '#33387a' : 'rgba(255,255,255,0.04)'}`,
      'color:#eee',
      `border:1px solid ${w.focused ? '#4a4f8d' : 'transparent'}`,
      'padding:0 12px',
      'height:28px',
      'border-radius:4px',
      'cursor:pointer',
      'max-width:220px',
      'overflow:hidden',
      'text-overflow:ellipsis',
      'white-space:nowrap',
      'font:13px system-ui,sans-serif',
      'flex-shrink:0',
      `opacity:${minimized ? '0.6' : '1'}`,
      `font-style:${minimized ? 'italic' : 'normal'}`,
    ].join(';');
    btn.addEventListener('click', () => {
      // Click a minimized pill → restore + focus. Otherwise → focus.
      if (w.state === 'minimized') {
        window.wash.restoreWindow(w.windowID);
      } else {
        window.wash.focusWindow(w.windowID);
      }
    });
    btn.addEventListener('contextmenu', (ev) => {
      ev.preventDefault();
      window.wash.closeWindow(w.windowID);
    });
    return btn;
  }

  // ---- start menu ----

  private toggleMenu() {
    if (this.menu) {
      this.closeMenu();
      return;
    }
    const menu = document.createElement('div');
    menu.style.cssText = [
      'position:absolute',
      'left:6px',
      'bottom:46px',
      'background:#15152a',
      'border:1px solid #2a2a4a',
      'border-radius:8px',
      'padding:4px',
      'min-width:240px',
      'box-shadow:0 8px 24px rgba(0,0,0,0.5)',
      'z-index:10001',
    ].join(';');

    const apps = window.wash.catalog();
    if (apps.length === 0) {
      const empty = document.createElement('div');
      empty.textContent = 'no apps registered';
      empty.style.cssText = 'padding:10px 14px;color:#888;font-size:13px;';
      menu.appendChild(empty);
    }
    for (const a of apps) {
      menu.appendChild(this.buildMenuEntry(a));
    }

    this.appendChild(menu);
    this.menu = menu;

    // Close on outside click. Bind on the next tick so the click that
    // opened the menu doesn't immediately close it.
    const onOutside = (ev: MouseEvent) => {
      if (!menu.contains(ev.target as Node) && ev.target !== this.startBtn && !this.startBtn.contains(ev.target as Node)) {
        this.closeMenu();
      }
    };
    setTimeout(() => document.addEventListener('mousedown', onOutside), 0);
    this.cleanups.push(() => document.removeEventListener('mousedown', onOutside));
  }

  private closeMenu() {
    if (this.menu) {
      this.menu.remove();
      this.menu = null;
    }
  }

  private onCatalogChange() {
    if (this.menu) {
      // re-render
      this.closeMenu();
      this.toggleMenu();
    }
  }

  private buildMenuEntry(app: CatalogApp): HTMLElement {
    const entry = document.createElement('button');
    entry.type = 'button';
    entry.style.cssText = [
      'display:flex',
      'align-items:center',
      'gap:10px',
      'width:100%',
      'padding:8px 12px',
      'background:transparent',
      'color:#eee',
      'border:none',
      'border-radius:4px',
      app.disabled ? 'cursor:not-allowed' : 'cursor:pointer',
      `opacity:${app.disabled ? '0.5' : '1'}`,
      'text-align:left',
      'font:14px system-ui,sans-serif',
    ].join(';');

    const iconBox = document.createElement('span');
    iconBox.style.cssText = 'width:22px;height:22px;flex-shrink:0;display:inline-flex;align-items:center;justify-content:center;';
    if (app.icon) {
      const img = document.createElement('img');
      img.src = app.icon;
      img.width = 22;
      img.height = 22;
      img.alt = '';
      iconBox.appendChild(img);
    }
    entry.appendChild(iconBox);

    const name = document.createElement('span');
    name.textContent = app.name;
    entry.appendChild(name);

    if (app.disabled) {
      const reason = document.createElement('span');
      reason.textContent = app.reason ? '· ' + app.reason : '· disabled';
      reason.style.cssText = 'margin-left:auto;color:#888;font-size:12px;';
      entry.appendChild(reason);
    } else {
      entry.addEventListener('mouseenter', () => {
        entry.style.background = '#2a2a4a';
      });
      entry.addEventListener('mouseleave', () => {
        entry.style.background = 'transparent';
      });
      entry.addEventListener('click', () => {
        this.closeMenu();
        window.wash.sendAppMsg(this.instance, { action: 'launch', app_id: app.id });
      });
    }

    return entry;
  }
}

function hamburgerSVG(): string {
  return [
    "<svg width='18' height='18' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2' stroke-linecap='round'>",
    "<line x1='4' y1='7' x2='20' y2='7'/>",
    "<line x1='4' y1='12' x2='20' y2='12'/>",
    "<line x1='4' y1='17' x2='20' y2='17'/>",
    '</svg>',
  ].join('');
}

if (!customElements.get('wash-app-session')) {
  customElements.define('wash-app-session', WashAppSession);
}
