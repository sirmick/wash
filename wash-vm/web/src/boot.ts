// Boot-log terminal: xterm.js wired to a stream of bytes from the
// VM's hvc0 (virtio-console port 0). Looks like a real Linux boot
// because it IS one — the kernel's console=hvc0 spam flows straight
// in.

import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';

export class BootTerminal {
  private term: Terminal;
  private fit: FitAddon;

  constructor(host: HTMLElement) {
    this.term = new Terminal({
      convertEol: true,
      // disableStdin is FALSE so the user can drop into the VM's
      // getty (running on ttyS0) by clicking the boot terminal and
      // typing. Boot messages still flow in from the kernel; user
      // input flows OUT to the VM via the onData hook below.
      disableStdin: false,
      cursorBlink: true,
      fontFamily: 'ui-monospace, "JetBrains Mono", Menlo, monospace',
      fontSize: 12,
      lineHeight: 1.15,
      theme: {
        background: '#0b0c10',
        foreground: '#cfd0d4',
        cursor: '#9ca3af',
      },
      scrollback: 5000,
    });
    this.fit = new FitAddon();
    this.term.loadAddon(this.fit);
    this.term.open(host);
    this.fit.fit();
    // Re-fit on resize so the terminal keeps the boot pane sized
    // even as the browser window changes.
    window.addEventListener('resize', () => this.fit.fit());
  }

  /** Append raw kernel-console bytes. */
  write(bytes: Uint8Array): void {
    this.term.write(bytes);
  }

  /** Append a single line of demo-side narration. */
  writeln(line: string): void {
    this.term.writeln(`\x1b[2m[demo]\x1b[0m ${line}`);
  }

  /**
   * Register a callback for user input. xterm.js receives keystrokes
   * when the terminal element is focused; we forward each UTF-8
   * string from .onData to the caller, which feeds them into the VM
   * (one byte per `serial0-input` event in v86).
   */
  onInput(cb: (data: string) => void): void {
    this.term.onData(cb);
  }

  /** Programmatically focus the terminal — used when the boot
   *  overlay is shown so keystrokes go to it immediately. */
  focus(): void {
    this.term.focus();
  }
}
