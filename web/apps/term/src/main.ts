// wash-app-term: tabbed xterm.js wrapper. One floating window can
// host many PTY tabs; each tab is a separate raw channel + Terminal
// instance. Tab bar at top, terminals stack below with display:none
// on inactive ones so state and scrollback survive switching.

import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import xtermCSS from '@xterm/xterm/css/xterm.css?inline';

declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
      openRawChannel(channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
      writeRaw(channelID: number, bytes: Uint8Array): void;
      saveState(instanceID: string, state: unknown): void;
    };
  }
}

// Persisted across browser refresh. Tab channels are kept alive by
// the router (their bound app stays running), so restoring is just
// "recreate UI for the channels I previously had".
interface PersistedState {
  tabs?: Array<{ channel_id: number; shell: string }>;
  active?: number;
}

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

interface Tab {
  channelID: number;
  shell: string;
  host: HTMLDivElement;
  term: Terminal;
  fit: FitAddon;
  unsub: () => void;
  tabEl: HTMLButtonElement;
}

const TAB_BAR_HEIGHT = 28;

class WashAppTerm extends HTMLElement {
  private instance = '';
  private tabBar!: HTMLDivElement;
  private termArea!: HTMLDivElement;
  private addBtn!: HTMLButtonElement;
  private tabs = new Map<number, Tab>();
  private active = 0;
  private resizeObs: ResizeObserver | null = null;

  connectedCallback() {
    this.instance = this.getAttribute('data-wash-instance') ?? '';

    if (!document.querySelector('style[data-wash-xterm]')) {
      const style = document.createElement('style');
      style.dataset.washXterm = '1';
      style.textContent = xtermCSS;
      document.head.appendChild(style);
    }

    this.style.cssText = [
      'display:flex',
      'flex-direction:column',
      'width:100%',
      'height:100%',
      'box-sizing:border-box',
      'background:#000',
      'color:#eee',
      'overflow:hidden',
    ].join(';');

    this.tabBar = document.createElement('div');
    this.tabBar.dataset.testid = 'term-tabbar';
    this.tabBar.style.cssText = [
      `height:${TAB_BAR_HEIGHT}px`,
      'background:#181828',
      'border-bottom:1px solid #2a2a3a',
      'display:flex',
      'align-items:stretch',
      'gap:1px',
      'padding:0 2px',
      'overflow-x:auto',
      'overflow-y:hidden',
      'font:12px ui-monospace,Menlo,Consolas,monospace',
      'flex-shrink:0',
    ].join(';');
    this.appendChild(this.tabBar);

    this.addBtn = document.createElement('button');
    this.addBtn.type = 'button';
    this.addBtn.dataset.testid = 'term-new-tab';
    this.addBtn.textContent = '+';
    this.addBtn.title = 'New tab (Ctrl+Shift+T)';
    this.addBtn.style.cssText = [
      'background:transparent',
      'color:#eee',
      'border:none',
      'padding:0 10px',
      'cursor:pointer',
      'font:14px ui-monospace,Menlo,Consolas,monospace',
      'opacity:0.8',
    ].join(';');
    this.addBtn.addEventListener('click', () => this.openNewTab());
    this.tabBar.appendChild(this.addBtn);

    this.termArea = document.createElement('div');
    this.termArea.style.cssText = 'flex:1;position:relative;min-height:0;';
    this.appendChild(this.termArea);

    this.addEventListener('wash:msg', (ev) => {
      this.handleBE((ev as CustomEvent).detail as BEMessage);
    });
    // Restore tab list on (re)mount. The BE keeps the ptys alive
    // across refresh; the router rebinds the raw channels and
    // replays scrollback. The FE just needs to recreate xterm
    // hosts for the channels it previously had.
    this.addEventListener('wash:state', (ev) => {
      const s = (ev as CustomEvent).detail as PersistedState | null;
      if (s) this.restoreFrom(s);
    });

    // Window/element resize → fit the active terminal.
    this.resizeObs = new ResizeObserver(() => {
      const tab = this.tabs.get(this.active);
      if (!tab) return;
      tab.fit.fit();
      this.sendResize(tab);
    });
    this.resizeObs.observe(this);
  }

  disconnectedCallback() {
    this.resizeObs?.disconnect();
    this.resizeObs = null;
    for (const tab of this.tabs.values()) {
      tab.unsub();
      tab.term.dispose();
    }
    this.tabs.clear();
  }

  // ---- BE → FE ----

  private handleBE(m: BEMessage) {
    switch (m.kind) {
      case 'tab_opened':
        this.addTab(Number(m.channel_id), String(m.shell ?? 'shell'));
        return;
      case 'tab_closed':
        this.removeTab(Number(m.channel_id));
        return;
      case 'tab_error':
        // Display in the active tab, if any.
        {
          const tab = this.tabs.get(this.active);
          if (tab) {
            tab.term.write('\r\n\x1b[31mwash-term: ' + String(m.msg) + '\x1b[0m\r\n');
          }
        }
        return;
    }
  }

  // ---- tab lifecycle ----

  private addTab(channelID: number, shellPath: string) {
    if (this.tabs.has(channelID)) return;

    const host = document.createElement('div');
    host.dataset.testid = 'term-host';
    host.dataset.channel = String(channelID);
    host.style.cssText = 'position:absolute;inset:0;display:none;';
    this.termArea.appendChild(host);

    const term = new Terminal({
      fontFamily: 'ui-monospace,Menlo,Consolas,monospace',
      fontSize: 13,
      theme: { background: '#000000' },
      cursorBlink: true,
      allowProposedApi: true,
    });
    term.attachCustomKeyEventHandler((ev) => this.onTermKey(ev));
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host);
    (host as unknown as { __washTerm: Terminal }).__washTerm = term;

    const unsub = window.wash.openRawChannel(channelID, (bytes) => term.write(bytes));
    const encoder = new TextEncoder();
    term.onData((data) => window.wash.writeRaw(channelID, encoder.encode(data)));

    const tabEl = this.buildTabButton(channelID, shellPath);
    // Insert before the + button so it stays at the end.
    this.tabBar.insertBefore(tabEl, this.addBtn);

    const tab: Tab = { channelID, shell: shellPath, host, term, fit, unsub, tabEl };
    this.tabs.set(channelID, tab);
    this.activate(channelID);
    this.persist();
  }

  private removeTab(channelID: number) {
    const tab = this.tabs.get(channelID);
    if (!tab) return;
    tab.unsub();
    tab.term.dispose();
    tab.host.remove();
    tab.tabEl.remove();
    this.tabs.delete(channelID);
    if (this.active === channelID) {
      // Pick another tab to activate; map iteration order is
      // insertion order so this picks the next "left" of the
      // current — close enough to user-expected behaviour.
      const next = this.tabs.keys().next().value as number | undefined;
      if (next !== undefined) {
        this.activate(next);
      } else {
        this.active = 0;
        // BE will confirm window close on its side once it sees the
        // sessions map empty.
      }
    }
    this.persist();
  }

  private activate(channelID: number) {
    const changed = this.active !== channelID;
    this.active = channelID;
    for (const tab of this.tabs.values()) {
      const isActive = tab.channelID === channelID;
      tab.host.style.display = isActive ? 'block' : 'none';
      tab.tabEl.style.background = isActive ? '#33387a' : 'transparent';
      tab.tabEl.style.borderTop = isActive ? '2px solid #66c' : '2px solid transparent';
    }
    const tab = this.tabs.get(channelID);
    if (tab) {
      // Defer focus + fit so the host has its display:block bounding rect.
      requestAnimationFrame(() => {
        tab.fit.fit();
        tab.term.focus();
        this.sendResize(tab);
      });
    }
    if (changed) this.persist();
  }

  // ---- state persistence (refresh resilience) ----

  private persist() {
    if (!this.instance) return;
    const tabs = Array.from(this.tabs.values()).map((t) => ({
      channel_id: t.channelID,
      shell: t.shell,
    }));
    const state: PersistedState = { tabs, active: this.active || undefined };
    window.wash.saveState(this.instance, state);
  }

  // restoreFrom recreates xterm tabs for the channels we previously
  // owned. The BE keeps the ptys alive; the router has already
  // rebound the channels to this shell and queued any scrollback
  // bytes for us. openRawChannel inside addTab will drain that queue.
  private restoreFrom(s: PersistedState) {
    if (!s.tabs?.length) return;
    for (const t of s.tabs) {
      this.addTab(t.channel_id, t.shell);
    }
    if (s.active && this.tabs.has(s.active)) {
      this.activate(s.active);
    }
  }

  private buildTabButton(channelID: number, shellPath: string): HTMLButtonElement {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.dataset.testid = `term-tab-${channelID}`;
    btn.style.cssText = [
      'background:transparent',
      'color:#eee',
      'border:none',
      'border-top:2px solid transparent',
      'padding:0 6px 0 10px',
      'cursor:pointer',
      'font:12px ui-monospace,Menlo,Consolas,monospace',
      'display:flex',
      'align-items:center',
      'gap:8px',
      'max-width:200px',
    ].join(';');
    const label = document.createElement('span');
    label.textContent = shortShellName(shellPath);
    label.style.cssText = 'overflow:hidden;text-overflow:ellipsis;white-space:nowrap;';
    btn.appendChild(label);
    const x = document.createElement('span');
    x.textContent = '×';
    x.dataset.testid = `term-tab-close-${channelID}`;
    x.style.cssText = 'opacity:0.6;font-size:13px;cursor:pointer;padding:0 4px;';
    x.addEventListener('click', (ev) => {
      ev.stopPropagation();
      this.requestCloseTab(channelID);
    });
    btn.appendChild(x);
    btn.addEventListener('click', () => this.activate(channelID));
    return btn;
  }

  // ---- actions ----

  private openNewTab() {
    window.wash.sendAppMsg(this.instance, { kind: 'new_tab' });
  }

  private requestCloseTab(channelID: number) {
    window.wash.sendAppMsg(this.instance, { kind: 'close_tab', channel_id: channelID });
  }

  private sendResize(tab: Tab) {
    window.wash.sendAppMsg(this.instance, {
      kind: 'resize',
      channel_id: tab.channelID,
      cols: tab.term.cols,
      rows: tab.term.rows,
    });
  }

  // ---- keyboard shortcuts ----

  // attachCustomKeyEventHandler returns true to keep default behaviour
  // (passing the key to the pty), false to suppress.
  private onTermKey(ev: KeyboardEvent): boolean {
    if (ev.type !== 'keydown') return true;
    if (ev.ctrlKey && ev.shiftKey && (ev.key === 'T' || ev.key === 't')) {
      this.openNewTab();
      return false;
    }
    if (ev.ctrlKey && ev.shiftKey && (ev.key === 'W' || ev.key === 'w')) {
      if (this.active) this.requestCloseTab(this.active);
      return false;
    }
    if (ev.ctrlKey && ev.key === 'Tab') {
      ev.preventDefault();
      this.cycleTabs(ev.shiftKey ? -1 : 1);
      return false;
    }
    return true;
  }

  private cycleTabs(dir: number) {
    const ids = Array.from(this.tabs.keys());
    if (ids.length < 2) return;
    const i = ids.indexOf(this.active);
    if (i < 0) return;
    const next = ids[(i + dir + ids.length) % ids.length];
    this.activate(next);
  }
}

function shortShellName(p: string): string {
  const i = p.lastIndexOf('/');
  return i >= 0 ? p.slice(i + 1) : p;
}

if (!customElements.get('wash-app-term')) {
  customElements.define('wash-app-term', WashAppTerm);
}
