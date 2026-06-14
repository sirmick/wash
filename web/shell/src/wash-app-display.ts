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
import { moveLocal, windowById, screenSize, VIEWPORTS_PER_AXIS } from './wm';

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

// One input batch flushed to the BE per rAF (motion) or immediately
// (button/key/wheel). Events are surface-relative ints; see docs/DISPLAY.md §6.
type InputEvent = Record<string, string | number>;

// DOM mouse button → wash button name.
const BUTTON_NAME: Record<number, string> = { 0: 'left', 1: 'middle', 2: 'right' };

// A child-surface (menu/dropdown) overlay: a canvas positioned in viewport
// space relative to the parent window, fed by a "video-popup" channel.
// See docs/DISPLAY.md §12 (M3).
interface PopupOverlay {
  channelID: number;
  canvas: HTMLCanvasElement;
  ctx: CanvasRenderingContext2D | null;
  x: number; // offset relative to the parent window's content origin
  y: number;
  unsub?: () => void;
  cleanup: () => void;
}

export class WashAppDisplay extends HTMLElement {
  private canvas?: HTMLCanvasElement;
  private ctx?: CanvasRenderingContext2D | null;
  private windowID = -1;
  private instanceID = '';
  private unsubscribe?: () => void;
  private errorCount = 0;

  // Pending input batch + rAF handle. Motion coalesces (one per frame);
  // buttons/keys/wheel flush immediately so clicks/keystrokes stay snappy.
  private pending: InputEvent[] = [];
  private rafID = 0;
  private inputCleanup?: () => void;

  // Active popup overlays, keyed by their video-popup channel id.
  private popups = new Map<number, PopupOverlay>();

  // Interactive-move state (M8). A CSD guest (chromeless window) requests an
  // xdg_toplevel.move when its own titlebar is dragged; the compositor relays
  // it as a {move:true} control frame. We then drag the wash window following
  // the pointer (this element holds the pointer capture), instead of
  // forwarding motion to the guest. moveAnchor records the pointer + window
  // origin at grab time; lastClientX/Y track the live pointer.
  private moving = false;
  private moveAnchor: { px: number; py: number; ox: number; oy: number } | null = null;
  private lastClientX = 0;
  private lastClientY = 0;

  connectedCallback(): void {
    const winAttr = this.getAttribute('data-wash-window');
    this.windowID = winAttr != null ? parseInt(winAttr, 10) : -1;
    this.instanceID = this.getAttribute('data-wash-instance') ?? '';

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

    // Forward pointer/keyboard/scroll to the owning wash-display instance.
    if (this.windowID >= 0 && this.instanceID) {
      this.setupInput();
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
    for (const ch of [...this.popups.keys()]) this.removePopup(ch);
    if (this.inputCleanup) {
      this.inputCleanup();
      this.inputCleanup = undefined;
    }
    if (this.rafID) {
      cancelAnimationFrame(this.rafID);
      this.rafID = 0;
    }
  }

  // --- input capture (docs/DISPLAY.md §6) ----------------------------

  // setupInput wires DOM pointer/keyboard/wheel listeners on this element
  // and forwards them, surface-relative, to the wash-display BE which
  // injects them into the real wlroots surface.
  private setupInput(): void {
    // Make the element focusable so it can receive key events when its
    // window is active; clicking it (below) gives it DOM focus. The outline
    // would be visual noise over guest pixels.
    this.tabIndex = 0;
    this.style.outline = 'none';

    const onPointerMove = (ev: PointerEvent) => {
      this.lastClientX = ev.clientX;
      this.lastClientY = ev.clientY;
      // While dragging the window (CSD guest's titlebar move, M8) we drive
      // the wash window instead of forwarding motion to the guest.
      if (this.moving) {
        this.applyMove(ev);
        return;
      }
      this.queueMotion(ev);
      this.scheduleFlush();
    };
    const onPointerDown = (ev: PointerEvent) => {
      this.lastClientX = ev.clientX;
      this.lastClientY = ev.clientY;
      // Capture so a drag that leaves the element still delivers move/up
      // (dragging a scrollbar, selecting text, etc.). Focus for keys.
      try {
        this.setPointerCapture(ev.pointerId);
      } catch {
        /* not all pointer types are capturable */
      }
      this.focus({ preventScroll: true });
      this.queueMotion(ev);
      this.queue({ ev: 'button', btn: BUTTON_NAME[ev.button] ?? 'left', state: 'down' });
      this.flushNow();
    };
    const onPointerUp = (ev: PointerEvent) => {
      this.lastClientX = ev.clientX;
      this.lastClientY = ev.clientY;
      if (this.moving) {
        this.endMove();
        return;
      }
      this.queueMotion(ev);
      this.queue({ ev: 'button', btn: BUTTON_NAME[ev.button] ?? 'left', state: 'up' });
      this.flushNow();
    };
    const onWheel = (ev: WheelEvent) => {
      // Forward both axes; deltaMode 0 (pixels) is the common case, the BE
      // treats the value as a ~120-per-notch hi-res wheel delta. Negate to
      // match wlroots' positive-down convention. preventDefault stops the
      // shell scrolling underneath.
      ev.preventDefault();
      if (ev.deltaY) this.queue({ ev: 'axis', axis: 'v', delta: Math.round(ev.deltaY) });
      if (ev.deltaX) this.queue({ ev: 'axis', axis: 'h', delta: Math.round(ev.deltaX) });
      this.flushNow();
    };
    const onKeyDown = (ev: KeyboardEvent) => {
      // Keep browser shortcuts/scroll from firing while a guest is focused;
      // the guest owns the keyboard. (Repeat is the client's job, so a
      // synthetic repeat — ev.repeat — still forwards as a fresh down.)
      ev.preventDefault();
      this.queue({ ev: 'key', code: ev.code, state: 'down' });
      this.flushNow();
    };
    const onKeyUp = (ev: KeyboardEvent) => {
      ev.preventDefault();
      this.queue({ ev: 'key', code: ev.code, state: 'up' });
      this.flushNow();
    };
    // Right-click must reach the guest, not pop the browser menu.
    const onContextMenu = (ev: Event) => ev.preventDefault();

    this.addEventListener('pointermove', onPointerMove);
    this.addEventListener('pointerdown', onPointerDown);
    this.addEventListener('pointerup', onPointerUp);
    this.addEventListener('wheel', onWheel, { passive: false });
    this.addEventListener('keydown', onKeyDown);
    this.addEventListener('keyup', onKeyUp);
    this.addEventListener('contextmenu', onContextMenu);

    this.inputCleanup = () => {
      this.removeEventListener('pointermove', onPointerMove);
      this.removeEventListener('pointerdown', onPointerDown);
      this.removeEventListener('pointerup', onPointerUp);
      this.removeEventListener('wheel', onWheel);
      this.removeEventListener('keydown', onKeyDown);
      this.removeEventListener('keyup', onKeyUp);
      this.removeEventListener('contextmenu', onContextMenu);
    };
  }

  // queueMotion appends (coalescing) a surface-relative motion event. The
  // canvas is drawn 1:1 at top-left (DPR 1.0), so surface coords are the
  // offset into the canvas box. Consecutive motions collapse to the latest.
  private queueMotion(ev: PointerEvent): void {
    const box = this.canvas && this.canvas.width > 0 ? this.canvas : this;
    const r = box.getBoundingClientRect();
    const x = Math.max(0, Math.round(ev.clientX - r.left));
    const y = Math.max(0, Math.round(ev.clientY - r.top));
    const last = this.pending[this.pending.length - 1];
    if (last && last.ev === 'motion') {
      last.x = x;
      last.y = y;
    } else {
      this.pending.push({ ev: 'motion', x, y });
    }
  }

  private queue(e: InputEvent): void {
    this.pending.push(e);
  }

  // --- interactive move (M8) -----------------------------------------
  // beginMove starts dragging the wash window in response to a CSD guest's
  // xdg_toplevel.move (relayed as a {move:true} control frame). We hold the
  // pointer capture, so subsequent pointermove/up land here; we drive the
  // window (moveLocal for 60fps optimism) and commit once on release. The
  // grab is anchored at the live pointer + the window's current origin.
  private beginMove(): void {
    if (this.windowID < 0 || this.moving) return;
    const w = windowById(this.windowID);
    if (!w) return;
    this.moving = true;
    this.moveAnchor = { px: this.lastClientX, py: this.lastClientY, ox: w.x, oy: w.y };
    // The guest requested the move off a press it will never see released
    // (we're taking over the grab — xdg-shell semantics). Send a synthetic
    // button-up so its CSD drag state resets cleanly.
    this.queue({ ev: 'button', btn: 'left', state: 'up' });
    this.flushNow();
  }

  private applyMove(ev: PointerEvent): void {
    const a = this.moveAnchor;
    if (!a || this.windowID < 0) return;
    const w = windowById(this.windowID);
    const s = screenSize();
    const maxX = s.w * VIEWPORTS_PER_AXIS - (w ? w.w : 0);
    const maxY = s.h * VIEWPORTS_PER_AXIS - (w ? w.h : 0);
    const x = Math.round(Math.max(0, Math.min(maxX, a.ox + (ev.clientX - a.px))));
    const y = Math.round(Math.max(0, Math.min(maxY, a.oy + (ev.clientY - a.py))));
    moveLocal(this.windowID, x, y); // live; router commit happens on release
  }

  private endMove(): void {
    this.moving = false;
    this.moveAnchor = null;
    const w = windowById(this.windowID);
    if (w) window.wash?.moveWindow(this.windowID, w.x, w.y);
  }

  // scheduleFlush batches motion to one send per animation frame.
  private scheduleFlush(): void {
    if (this.rafID) return;
    this.rafID = requestAnimationFrame(() => {
      this.rafID = 0;
      this.flushNow();
    });
  }

  // flushNow sends the pending batch as one app_msg to the wash-display
  // instance (addressed by instance_id; the BE routes by the payload win).
  private flushNow(): void {
    if (this.rafID) {
      cancelAnimationFrame(this.rafID);
      this.rafID = 0;
    }
    if (this.pending.length === 0) return;
    const events = this.pending;
    this.pending = [];
    this.sendInput({ win: this.windowID }, events);
  }

  // sendInput posts one input batch to the wash-display instance. `target`
  // is {win} for the window itself or {popup_chan} for a popup overlay (the
  // BE routes by whichever is set).
  private sendInput(target: Record<string, number>, events: InputEvent[]): void {
    if (events.length === 0) return;
    if (typeof window === 'undefined' || !window.wash) return;
    window.wash.sendAppMsgTo({ instance_id: this.instanceID }, { kind: 'input', ...target, events });
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
    // Sub-header frames on the video channel are JSON control messages, not
    // pixels — {cursor:"<css-name>"} from cursor-shape-v1 (M4), or {move:true}
    // when a CSD guest drags its own titlebar and asks for a move (M8).
    if (bytes.length < HEADER_BYTES) {
      try {
        const ctrl = JSON.parse(new TextDecoder().decode(bytes));
        if (typeof ctrl.cursor === 'string') {
          this.style.cursor = ctrl.cursor;
          if (this.canvas) this.canvas.style.cursor = ctrl.cursor;
        } else if (ctrl.move === true) {
          this.beginMove();
        }
      } catch {
        /* ignore malformed control frame */
      }
      return;
    }

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

  // --- popup overlays (DISPLAY.md §12 M3) ----------------------------

  // attachPopupChannel subscribes to a child-surface (menu/dropdown)
  // channel. Frames < HEADER_BYTES are JSON control (geometry / close);
  // frames ≥ HEADER_BYTES are WS pixel frames (same format as the main
  // stream). Called by the display registry on channel.bind kind=video-popup.
  attachPopupChannel(channelID: number): void {
    if (this.popups.has(channelID)) return;
    const unsub = subscribeRaw(channelID, (bytes) => this.onPopupBytes(channelID, bytes));
    // The overlay canvas is created lazily on the first pixel frame; record
    // the subscription now so close/teardown always works.
    const placeholder: PopupOverlay = {
      channelID,
      canvas: undefined as unknown as HTMLCanvasElement,
      ctx: null,
      x: 0,
      y: 0,
      unsub,
      cleanup: () => {},
    };
    this.popups.set(channelID, placeholder);
  }

  private onPopupBytes(channelID: number, bytes: Uint8Array): void {
    const p = this.popups.get(channelID);
    if (!p) return;
    if (bytes.length < HEADER_BYTES) {
      // Control frame: { x, y } geometry update, or { close: true }.
      let ctrl: { x?: number; y?: number; close?: boolean };
      try {
        ctrl = JSON.parse(new TextDecoder().decode(bytes));
      } catch {
        return;
      }
      if (ctrl.close) {
        this.removePopup(channelID);
        return;
      }
      if (typeof ctrl.x === 'number') p.x = ctrl.x;
      if (typeof ctrl.y === 'number') p.y = ctrl.y;
      if (p.canvas) this.repositionPopup(p);
      return;
    }
    // Pixel frame.
    let header: FrameHeader;
    try {
      header = parseHeader(new DataView(bytes.buffer, bytes.byteOffset, HEADER_BYTES));
    } catch (e) {
      this.logError('popup header parse failed', e);
      return;
    }
    const payload = bytes.subarray(HEADER_BYTES);
    if (payload.length === 0) return;
    const copy = payload.slice();
    if (!p.canvas) this.ensurePopupCanvas(p);
    createImageBitmap(new Blob([copy]))
      .then((bitmap) => {
        const live = this.popups.get(channelID);
        if (!live || !live.canvas || !live.ctx) {
          bitmap.close?.();
          return;
        }
        const cv = live.canvas;
        if (header.frameW > 0 && cv.width !== header.frameW) {
          cv.width = header.frameW;
          cv.style.width = header.frameW + 'px';
        }
        if (header.frameH > 0 && cv.height !== header.frameH) {
          cv.height = header.frameH;
          cv.style.height = header.frameH + 'px';
        }
        live.ctx.drawImage(bitmap, header.dirtyX, header.dirtyY);
        bitmap.close?.();
        this.repositionPopup(live);
      })
      .catch((e) => this.logError('popup decode/draw failed', e));
  }

  // ensurePopupCanvas builds the overlay canvas: position:fixed on <body>
  // (so it can overflow the parent window box, like a real menu), above the
  // window stack, forwarding its own pointer/wheel input keyed by the popup
  // channel (the BE has no win for a popup).
  private ensurePopupCanvas(p: PopupOverlay): void {
    const cv = document.createElement('canvas');
    cv.style.position = 'fixed';
    cv.style.zIndex = '2147483646';
    cv.style.imageRendering = 'auto';
    cv.style.pointerEvents = 'auto';
    document.body.appendChild(cv);
    p.canvas = cv;
    p.ctx = cv.getContext('2d');

    const surfacePos = (ev: PointerEvent) => {
      const r = cv.getBoundingClientRect();
      return {
        x: Math.max(0, Math.round(ev.clientX - r.left)),
        y: Math.max(0, Math.round(ev.clientY - r.top)),
      };
    };
    const tgt = { popup_chan: p.channelID };
    const onMove = (ev: PointerEvent) => {
      const { x, y } = surfacePos(ev);
      this.sendInput(tgt, [{ ev: 'motion', x, y }]);
    };
    const onDown = (ev: PointerEvent) => {
      const { x, y } = surfacePos(ev);
      this.sendInput(tgt, [
        { ev: 'motion', x, y },
        { ev: 'button', btn: BUTTON_NAME[ev.button] ?? 'left', state: 'down' },
      ]);
    };
    const onUp = (ev: PointerEvent) => {
      const { x, y } = surfacePos(ev);
      this.sendInput(tgt, [
        { ev: 'motion', x, y },
        { ev: 'button', btn: BUTTON_NAME[ev.button] ?? 'left', state: 'up' },
      ]);
    };
    const onWheel = (ev: WheelEvent) => {
      ev.preventDefault();
      const evs: InputEvent[] = [];
      if (ev.deltaY) evs.push({ ev: 'axis', axis: 'v', delta: Math.round(ev.deltaY) });
      if (ev.deltaX) evs.push({ ev: 'axis', axis: 'h', delta: Math.round(ev.deltaX) });
      this.sendInput(tgt, evs);
    };
    cv.addEventListener('pointermove', onMove);
    cv.addEventListener('pointerdown', onDown);
    cv.addEventListener('pointerup', onUp);
    cv.addEventListener('wheel', onWheel, { passive: false });
    p.cleanup = () => {
      cv.removeEventListener('pointermove', onMove);
      cv.removeEventListener('pointerdown', onDown);
      cv.removeEventListener('pointerup', onUp);
      cv.removeEventListener('wheel', onWheel);
      cv.remove();
    };
    this.repositionPopup(p);
  }

  // repositionPopup places the overlay in viewport space at the parent
  // window's content origin plus the popup's offset. Recomputed each frame
  // so it tracks the window if it moves.
  private repositionPopup(p: PopupOverlay): void {
    if (!p.canvas || !this.canvas) return;
    const r = this.canvas.getBoundingClientRect();
    p.canvas.style.left = Math.round(r.left + p.x) + 'px';
    p.canvas.style.top = Math.round(r.top + p.y) + 'px';
  }

  private removePopup(channelID: number): void {
    const p = this.popups.get(channelID);
    if (!p) return;
    this.popups.delete(channelID);
    try {
      p.unsub?.();
    } catch {
      /* ignore */
    }
    try {
      p.cleanup();
    } catch {
      /* ignore */
    }
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
