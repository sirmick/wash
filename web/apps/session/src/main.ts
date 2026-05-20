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
  private palette: PaletteState | null = null;
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

    // Single outside-click handler that re-reads current menu state.
    // Don't capture menuEl in a closure — re-opening would leave stale
    // closures from prior opens still listening and erroneously
    // closing the new menu mid-click.
    const onDocMouseDown = (ev: MouseEvent) => {
      const t = ev.target as Node;
      if (this.menu && !this.menu.contains(t) && !this.startBtn.contains(t)) {
        this.closeMenu();
      }
      if (this.palette && !this.palette.root.contains(t)) {
        this.closePalette();
      }
    };
    document.addEventListener('mousedown', onDocMouseDown);
    this.cleanups.push(() => document.removeEventListener('mousedown', onDocMouseDown));

    const tick = () => {
      this.clock.textContent = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    };
    tick();
    const id = window.setInterval(tick, 30_000);
    this.cleanups.push(() => clearInterval(id));

    // Global hotkey: Ctrl+Space opens the palette. Document-level so
    // it works regardless of which window has focus.
    const onKey = (ev: KeyboardEvent) => {
      if (ev.ctrlKey && (ev.key === ' ' || ev.code === 'Space')) {
        ev.preventDefault();
        this.togglePalette();
      }
    };
    document.addEventListener('keydown', onKey);
    this.cleanups.push(() => document.removeEventListener('keydown', onKey));
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

    const paletteBtn = this.buildIconButton(searchSVG(), 'Search apps (Ctrl+Space)');
    paletteBtn.dataset.testid = 'palette-open';
    paletteBtn.addEventListener('click', () => this.togglePalette());
    bar.appendChild(paletteBtn);

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
    menu.dataset.testid = 'start-menu';
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
    // Outside-click closure is handled by the single document
    // listener installed in connectedCallback (which re-reads
    // this.menu each time and avoids the stale-closure bug).
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
    if (this.palette) {
      this.renderPaletteResults('');
    }
  }

  // ---- command palette ----

  private togglePalette() {
    if (this.palette) {
      this.closePalette();
      return;
    }
    const root = document.createElement('div');
    root.dataset.testid = 'palette';
    root.style.cssText = [
      'position:absolute',
      'inset:0',
      'display:flex',
      'align-items:flex-start',
      'justify-content:center',
      'padding-top:14vh',
      'background:rgba(0,0,0,0.35)',
      'z-index:10002',
    ].join(';');
    // Click backdrop (but not inner box) to close.
    root.addEventListener('click', (ev) => {
      if (ev.target === root) this.closePalette();
    });

    const box = document.createElement('div');
    box.style.cssText = [
      'background:#181828',
      'border:1px solid #2a2a4a',
      'border-radius:8px',
      'min-width:380px',
      'max-width:520px',
      'box-shadow:0 16px 48px rgba(0,0,0,0.6)',
      'overflow:hidden',
    ].join(';');
    root.appendChild(box);

    const input = document.createElement('input');
    input.type = 'text';
    input.placeholder = 'Search apps…';
    input.dataset.testid = 'palette-input';
    input.style.cssText = [
      'width:100%',
      'box-sizing:border-box',
      'padding:14px 16px',
      'background:transparent',
      'color:#eee',
      'border:none',
      'border-bottom:1px solid #2a2a4a',
      'outline:none',
      'font:15px system-ui,sans-serif',
    ].join(';');
    box.appendChild(input);

    const list = document.createElement('div');
    list.dataset.testid = 'palette-list';
    list.style.cssText = 'max-height:50vh;overflow:auto;';
    box.appendChild(list);

    this.palette = { root, input, list, apps: [], selected: 0 };
    this.appendChild(root);
    input.focus();

    input.addEventListener('input', () => this.renderPaletteResults(input.value));
    input.addEventListener('keydown', (ev) => this.onPaletteKey(ev));
    // Backdrop click closes the palette (kept here because the
    // root.click listener is specifically for backdrop, distinct
    // from the document mousedown handler that closes both).
    root.addEventListener('click', (ev) => {
      if (ev.target === root) this.closePalette();
    });

    this.renderPaletteResults('');
  }

  private closePalette() {
    if (!this.palette) return;
    this.palette.root.remove();
    this.palette = null;
  }

  private renderPaletteResults(query: string) {
    if (!this.palette) return;
    const q = query.trim().toLowerCase();
    const apps = window.wash.catalog().filter((a) => !a.disabled);
    const filtered = q
      ? apps.filter((a) => a.id.toLowerCase().includes(q) || a.name.toLowerCase().includes(q))
      : apps;
    this.palette.apps = filtered;
    this.palette.selected = filtered.length > 0 ? 0 : -1;
    this.palette.list.replaceChildren();
    if (filtered.length === 0) {
      const empty = document.createElement('div');
      empty.textContent = 'no matches';
      empty.style.cssText = 'padding:10px 16px;color:#888;font-size:13px;';
      this.palette.list.appendChild(empty);
      return;
    }
    filtered.forEach((a, i) => {
      const row = document.createElement('button');
      row.type = 'button';
      row.dataset.testid = `palette-item-${a.id}`;
      const isSel = i === this.palette!.selected;
      row.style.cssText = [
        'display:flex',
        'align-items:center',
        'gap:10px',
        'width:100%',
        'padding:8px 16px',
        `background:${isSel ? '#2a2a4a' : 'transparent'}`,
        'color:#eee',
        'border:none',
        'text-align:left',
        'cursor:pointer',
        'font:14px system-ui,sans-serif',
      ].join(';');
      const iconBox = document.createElement('span');
      iconBox.style.cssText = 'width:20px;height:20px;flex-shrink:0;';
      if (a.icon) {
        const img = document.createElement('img');
        img.src = a.icon;
        img.width = 20;
        img.height = 20;
        iconBox.appendChild(img);
      }
      row.appendChild(iconBox);
      const name = document.createElement('span');
      name.textContent = a.name;
      name.style.cssText = 'flex:1;';
      row.appendChild(name);
      const id = document.createElement('span');
      id.textContent = a.id;
      id.style.cssText = 'opacity:0.55;font-size:12px;';
      row.appendChild(id);
      row.addEventListener('mouseenter', () => {
        if (!this.palette) return;
        this.palette.selected = i;
        this.highlightSelection();
      });
      row.addEventListener('click', () => this.launchSelected());
      this.palette!.list.appendChild(row);
    });
  }

  private highlightSelection() {
    if (!this.palette) return;
    const rows = this.palette.list.querySelectorAll<HTMLElement>('button');
    rows.forEach((row, i) => {
      row.style.background = i === this.palette!.selected ? '#2a2a4a' : 'transparent';
    });
  }

  private onPaletteKey(ev: KeyboardEvent) {
    if (!this.palette) return;
    switch (ev.key) {
      case 'Escape':
        ev.preventDefault();
        this.closePalette();
        return;
      case 'Enter':
        ev.preventDefault();
        this.launchSelected();
        return;
      case 'ArrowDown':
        ev.preventDefault();
        if (this.palette.apps.length > 0) {
          this.palette.selected = (this.palette.selected + 1) % this.palette.apps.length;
          this.highlightSelection();
          this.palette.list.children[this.palette.selected]?.scrollIntoView({ block: 'nearest' });
        }
        return;
      case 'ArrowUp':
        ev.preventDefault();
        if (this.palette.apps.length > 0) {
          this.palette.selected =
            (this.palette.selected - 1 + this.palette.apps.length) % this.palette.apps.length;
          this.highlightSelection();
          this.palette.list.children[this.palette.selected]?.scrollIntoView({ block: 'nearest' });
        }
        return;
    }
  }

  private launchSelected() {
    if (!this.palette) return;
    const app = this.palette.apps[this.palette.selected];
    if (!app) return;
    this.closePalette();
    window.wash.sendAppMsg(this.instance, { action: 'launch', app_id: app.id });
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

function searchSVG(): string {
  return [
    "<svg width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2' stroke-linecap='round'>",
    "<circle cx='11' cy='11' r='7'/>",
    "<line x1='16.5' y1='16.5' x2='21' y2='21'/>",
    '</svg>',
  ].join('');
}

interface PaletteState {
  root: HTMLDivElement;
  input: HTMLInputElement;
  list: HTMLDivElement;
  apps: CatalogApp[];
  selected: number;
}

if (!customElements.get('wash-app-session')) {
  customElements.define('wash-app-session', WashAppSession);
}
