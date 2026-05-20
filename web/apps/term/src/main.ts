// wash-app-term: xterm.js inside a wash window. Receives PTY bytes on
// a raw channel the BE opened; writes keystrokes back. ResizeObserver
// tracks viewport changes and forwards new cols/rows via app_msg.

import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import xtermCSS from '@xterm/xterm/css/xterm.css?inline';

declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
      openRawChannel(channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
      writeRaw(channelID: number, bytes: Uint8Array): void;
    };
  }
}

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

class WashAppTerm extends HTMLElement {
  private instance = '';
  private host!: HTMLDivElement;
  private term: Terminal | null = null;
  private fit: FitAddon | null = null;
  private channelID = 0;
  private unsub: (() => void) | null = null;
  private resizeObs: ResizeObserver | null = null;

  connectedCallback() {
    this.instance = this.getAttribute('data-wash-instance') ?? '';

    // Inject xterm's CSS once per document.
    if (!document.querySelector('style[data-wash-xterm]')) {
      const style = document.createElement('style');
      style.dataset.washXterm = '1';
      style.textContent = xtermCSS;
      document.head.appendChild(style);
    }

    this.style.cssText = [
      'display:block',
      'width:100%',
      'height:100%',
      'box-sizing:border-box',
      'background:#000',
      'color:#eee',
      'overflow:hidden',
    ].join(';');

    this.host = document.createElement('div');
    this.host.dataset.testid = 'term-host';
    this.host.style.cssText = 'width:100%;height:100%;';
    this.appendChild(this.host);

    this.addEventListener('wash:msg', (ev) => {
      this.handleBE((ev as CustomEvent).detail as BEMessage);
    });
  }

  disconnectedCallback() {
    this.tearDown();
  }

  private handleBE(m: BEMessage) {
    switch (m.kind) {
      case 'tab_opened':
        if (this.channelID === 0) {
          this.channelID = Number(m.channel_id);
          this.startTerminal();
        }
        return;
      case 'tab_closed':
        if (this.channelID && Number(m.channel_id) === this.channelID) {
          this.tearDown();
        }
        return;
      case 'tab_error':
        if (this.term) {
          this.term.write('\r\nwash-term: ' + String(m.msg) + '\r\n');
        } else {
          const msg = document.createElement('div');
          msg.dataset.testid = 'term-error';
          msg.style.cssText = 'padding:12px;color:#f99;font:13px monospace;';
          msg.textContent = 'wash-term: ' + String(m.msg);
          this.host.appendChild(msg);
        }
        return;
    }
  }

  private startTerminal() {
    const term = new Terminal({
      fontFamily: 'ui-monospace,Menlo,Consolas,monospace',
      fontSize: 13,
      theme: { background: '#000000' },
      cursorBlink: true,
      allowProposedApi: true,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(this.host);
    // Expose for e2e tests to inspect the buffer. Not API; internal hook.
    (this.host as unknown as { __washTerm: Terminal }).__washTerm = term;
    // First fit happens after the element is in the DOM with size.
    requestAnimationFrame(() => {
      fit.fit();
      this.sendResize();
    });
    this.term = term;
    this.fit = fit;

    this.unsub = window.wash.openRawChannel(this.channelID, (bytes) => term.write(bytes));
    const encoder = new TextEncoder();
    term.onData((data) => {
      window.wash.writeRaw(this.channelID, encoder.encode(data));
    });

    const obs = new ResizeObserver(() => {
      if (!this.fit || !this.term) return;
      this.fit.fit();
      this.sendResize();
    });
    obs.observe(this);
    this.resizeObs = obs;
  }

  private sendResize() {
    if (!this.term || this.channelID === 0) return;
    window.wash.sendAppMsg(this.instance, {
      kind: 'resize',
      channel_id: this.channelID,
      cols: this.term.cols,
      rows: this.term.rows,
    });
  }

  private tearDown() {
    this.resizeObs?.disconnect();
    this.resizeObs = null;
    this.unsub?.();
    this.unsub = null;
    this.term?.dispose();
    this.term = null;
    this.fit = null;
    this.channelID = 0;
  }
}

if (!customElements.get('wash-app-term')) {
  customElements.define('wash-app-term', WashAppTerm);
}
