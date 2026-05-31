// Built-in <wash-app-display> custom element.
//
// Registered at shell startup via a side-effect import from main.tsx
// (`import './wash-app-display'`), so the tag exists before any display
// window mounts. wash-display is a surface=background instance whose
// windows carry element "wash-app-display" with an EMPTY app bundle —
// there is no app bundle to define the element, so the shell must
// provide it as a built-in.
//
// Each instance renders one window's video stream onto a <canvas>. The
// shell routes the window's "video" channel (channel.bind kind="video")
// to the element through the display-window registry in api.ts. Frame
// bytes arrive on the raw per-channel path (subscribeRaw); each frame is
// a 45-byte little-endian header followed by an image payload — WebP or
// PNG, auto-detected by createImageBitmap from the magic bytes.

import { registerDisplayWindow, subscribeRaw, unregisterDisplayWindow } from './api';

// Frame header layout (little-endian). See mac-phoenix client.js and
// docs/DISPLAY.md. Only the dirty-rect + full-surface size are used; the
// timestamps and cursor fields are ignored here.
const HEADER_BYTES = 45;

interface FrameHeader {
  dirtyX: number;
  dirtyY: number;
  dirtyW: number;
  dirtyH: number;
  frameW: number;
  frameH: number;
}

function parseHeader(view: DataView): FrameHeader {
  // off 0  u64 t1_frame_ready (ignored)
  return {
    dirtyX: view.getUint32(8, true),
    dirtyY: view.getUint32(12, true),
    dirtyW: view.getUint32(16, true),
    dirtyH: view.getUint32(20, true),
    frameW: view.getUint32(24, true),
    frameH: view.getUint32(28, true),
    // off 32 u64 t4, off 40 u16 cursor_x, off 42 u16 cursor_y,
    // off 44 u8 cursor_visible — all ignored.
  };
}

export class WashAppDisplay extends HTMLElement {
  private canvas?: HTMLCanvasElement;
  private ctx?: CanvasRenderingContext2D | null;
  private windowID = -1;
  private unsubscribe?: () => void;
  private errorCount = 0;

  connectedCallback(): void {
    const winAttr = this.getAttribute('data-wash-window');
    this.windowID = winAttr != null ? parseInt(winAttr, 10) : -1;

    if (!this.canvas) {
      // Render the guest buffer at native 1:1 pixels, anchored top-left,
      // and CLIP the element box (no CSS scaling). During a drag-resize the
      // wash frame resizes instantly but the guest repaints a beat later;
      // with 1:1 + clip the stale frame is revealed/clipped (like a real X
      // window mid-resize) instead of being stretched/blurred to the new
      // box. Steady state (frame == guest size) is still exactly 1:1.
      this.style.display = 'block';
      this.style.width = '100%';
      this.style.height = '100%';
      this.style.position = 'relative';
      this.style.overflow = 'hidden';
      // No opaque fill: during a grow-resize the slot expands a frame before
      // the guest's larger buffer arrives, so the not-yet-covered strip shows
      // through. Transparent lets the window's own dark chrome show there
      // instead of a harsh black flash (the resize "flicker").
      this.style.background = 'transparent';

      const canvas = document.createElement('canvas');
      canvas.style.display = 'block';
      canvas.style.position = 'absolute';
      canvas.style.left = '0';
      canvas.style.top = '0';
      canvas.style.imageRendering = 'auto';
      this.appendChild(canvas);
      this.canvas = canvas;
      this.ctx = canvas.getContext('2d');
    }

    // Register so the shell can hand us our window's video channel. If the
    // channel.bind already arrived before mount, the registry replays it
    // immediately by calling attachVideoChannel (see api.ts).
    if (this.windowID >= 0) {
      registerDisplayWindow(this.windowID, this);
    }
  }

  disconnectedCallback(): void {
    if (this.windowID >= 0) {
      unregisterDisplayWindow(this.windowID, this);
    }
    if (this.unsubscribe) {
      try {
        this.unsubscribe();
      } catch {
        /* ignore */
      }
      this.unsubscribe = undefined;
    }
  }

  // attachVideoChannel subscribes to the raw byte stream for the window's
  // video channel. Called by the display-window registry once the bind is
  // known. Idempotent / re-bindable: any prior subscription is dropped.
  attachVideoChannel(channelID: number): void {
    if (this.unsubscribe) {
      try {
        this.unsubscribe();
      } catch {
        /* ignore */
      }
    }
    this.unsubscribe = subscribeRaw(channelID, (bytes) => this.onFrame(bytes));
  }

  private onFrame(bytes: Uint8Array): void {
    if (!this.canvas || !this.ctx) return;
    if (bytes.length < HEADER_BYTES) return;

    let header: FrameHeader;
    try {
      const view = new DataView(bytes.buffer, bytes.byteOffset, HEADER_BYTES);
      header = parseHeader(view);
    } catch (e) {
      this.logError('frame header parse failed', e);
      return;
    }

    const payload = bytes.subarray(HEADER_BYTES);
    if (payload.length === 0) return;

    // Copy out of the (reused) WS frame buffer so the Blob owns its bytes.
    const copy = payload.slice();

    createImageBitmap(new Blob([copy]))
      .then((bitmap) => {
        const canvas = this.canvas;
        const ctx = this.ctx;
        if (!canvas || !ctx) {
          bitmap.close?.();
          return;
        }
        // Keep the canvas CSS box equal to its backing-store pixels so the
        // bitmap is drawn 1:1 (never scaled). The element clips/letterboxes
        // any difference between this and the current frame size.
        if (header.frameW > 0 && header.frameH > 0) {
          if (canvas.width !== header.frameW) {
            canvas.width = header.frameW;
            canvas.style.width = header.frameW + 'px';
          }
          if (canvas.height !== header.frameH) {
            canvas.height = header.frameH;
            canvas.style.height = header.frameH + 'px';
          }
        }
        ctx.drawImage(bitmap, header.dirtyX, header.dirtyY);
        bitmap.close?.();
      })
      .catch((e) => this.logError('decode/draw failed', e));
  }

  private logError(msg: string, e: unknown): void {
    // Throttle: log only the first few errors to avoid console spam on a
    // misbehaving stream.
    if (this.errorCount < 5) {
      this.errorCount++;
      console.error('wash-app-display:', msg, e);
    }
  }
}

// Register once, at module load (side-effect import from main.tsx).
// customElements.define throws if the tag is already defined, so guard it.
if (typeof customElements !== 'undefined' && !customElements.get('wash-app-display')) {
  customElements.define('wash-app-display', WashAppDisplay);
}
