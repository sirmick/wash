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

import { registerDisplayWindow, subscribeRaw, subscribeResync, unregisterDisplayWindow } from './api';
import { type Origin, LOCAL_ORIGIN } from './clients';
import { moveLocal, resizeLocal, windowById, screenSize, VIEWPORTS_PER_AXIS } from './wm';

// xdg_toplevel resize edge bitmask (matches wlroots / xdg-shell).
const EDGE_TOP = 1;
const EDGE_BOTTOM = 2;
const EDGE_LEFT = 4;
const EDGE_RIGHT = 8;
const MIN_W = 100;
const MIN_H = 60;

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

function currentFrameScale(): number {
  const scale = Number((window as unknown as { __washDisplayScale?: number }).__washDisplayScale || 1);
  return scale >= 2 ? 2 : 1;
}

function frameCssPx(px: number): number {
  return Math.max(1, Math.round(px / currentFrameScale()));
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
  // The window's origin (which router owns it). Window ids are per-router, so
  // the registry + raw subscription must be origin-scoped or a remote display
  // window collides with a local one of the same id (docs/REMOTE.md R2).
  private origin: Origin = LOCAL_ORIGIN;
  private instanceID = '';
  private unsubscribe?: () => void;
  private unsubscribeResync?: () => void;
  private errorCount = 0;

  // Pending input batch + rAF handle. Motion coalesces (one per frame);
  // buttons/keys/wheel flush immediately so clicks/keystrokes stay snappy.
  private pending: InputEvent[] = [];
  private rafID = 0;
  private inputCleanup?: () => void;
  // Physical codes currently held down, so they can be released on blur /
  // tab-hide — otherwise a modifier latched when the host browser is
  // Alt-Tabbed away stays depressed in the guest's xkb state and is re-sent on
  // the next focus (REVIEW-X11-WAYLAND #9).
  private heldKeys = new Set<string>();

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
  // Interactive-resize state (M8b), same model as move but driven by the
  // guest's xdg_toplevel.resize (a {resize:<edges>} control frame). Top/left
  // edges move the origin as well as the size.
  private resizing = false;
  private resizeAnchor:
    | { px: number; py: number; ox: number; oy: number; ow: number; oh: number; edges: number }
    | null = null;

  connectedCallback(): void {
    const winAttr = this.getAttribute('data-wash-window');
    this.windowID = winAttr != null ? parseInt(winAttr, 10) : -1;
    this.origin = this.getAttribute('data-wash-origin') || LOCAL_ORIGIN;
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
    this.tabIndex = 0;
    this.style.outline = 'none';

    // Register so the shell can hand us our window's video channel. If the
    // channel.bind already arrived before mount, the registry replays it
    // immediately by calling attachVideoChannel (see api.ts).
    if (this.windowID >= 0) {
      registerDisplayWindow(this.origin, this.windowID, this);
    }

    // Forward pointer/keyboard/scroll to the owning wash-display instance.
    if (this.windowID >= 0 && this.instanceID) {
      this.setupInput();
    }
  }

  disconnectedCallback(): void {
    if (this.windowID >= 0) {
      unregisterDisplayWindow(this.origin, this.windowID, this);
    }
    if (this.unsubscribe) {
      try {
        this.unsubscribe();
      } catch {
        /* ignore */
      }
      this.unsubscribe = undefined;
    }
    if (this.unsubscribeResync) {
      try {
        this.unsubscribeResync();
      } catch {
        /* ignore */
      }
      this.unsubscribeResync = undefined;
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
    // Reset drag state so a reconnected element (custom elements can be
    // re-adopted into the DOM) doesn't start stuck mid-move/resize.
    this.moving = false;
    this.resizing = false;
  }

  // --- input capture (docs/DISPLAY.md §6) ----------------------------

  // setupInput wires DOM pointer/keyboard/wheel listeners on this element
  // and forwards them, surface-relative, to the wash-display BE which
  // injects them into the real wlroots surface.
  private setupInput(): void {
    if (this.inputCleanup) return;
    // Make the element focusable so it can receive key events when its
    // window is active; clicking it (below) gives it DOM focus. The outline
    // would be visual noise over guest pixels.
    this.tabIndex = 0;
    this.style.outline = 'none';

    const onPointerMove = (ev: PointerEvent) => {
      this.lastClientX = ev.clientX;
      this.lastClientY = ev.clientY;
      // While dragging the window (CSD guest's titlebar move/resize, M8/M8b)
      // we drive the wash window instead of forwarding motion to the guest.
      if (this.moving) {
        this.applyMove(ev);
        return;
      }
      if (this.resizing) {
        this.applyResize(ev);
        return;
      }
      this.queueMotion(ev);
      this.scheduleFlush();
    };
    const onPointerDown = (ev: PointerEvent) => {
      this.lastClientX = ev.clientX;
      this.lastClientY = ev.clientY;
      ev.preventDefault();
      window.wash?.focusWindow(this.windowID, this.origin);
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
      ev.preventDefault();
      if (this.moving) {
        this.endMove();
        return;
      }
      if (this.resizing) {
        this.endResize();
        return;
      }
      this.queueMotion(ev);
      this.queue({ ev: 'button', btn: BUTTON_NAME[ev.button] ?? 'left', state: 'up' });
      this.flushNow();
    };
    const onWheel = (ev: WheelEvent) => {
      // Normalize deltaMode before forwarding: Firefox delivers LINE deltas
      // (±3 per notch), not pixels, so passing deltaY raw scrolled guests ~40×
      // too slow (REVIEW-X11-WAYLAND #10). Convert lines→px (~40px/line) and
      // pages→viewport height, then send BOTH a pixel delta (the BE derives a
      // continuous value for smooth trackpad scroll) and an integer notch count
      // so the BE emits a clean value120 = notches*120 — Xwayland accumulates
      // value120 into buttons 4/5 and ragged values make discrete-consuming
      // clients (xterm, Qt list views) skip/lag.
      ev.preventDefault();
      window.wash?.focusWindow(this.windowID, this.origin);
      this.focus({ preventScroll: true });
      const LINE_PX = 40;
      const NOTCH_PX = 120;
      const scale = ev.deltaMode === 1 ? LINE_PX
                  : ev.deltaMode === 2 ? (this.clientHeight || 800)
                  : 1;
      const py = ev.deltaY * scale;
      const px = ev.deltaX * scale;
      if (py) this.queue({ ev: 'axis', axis: 'v', delta: Math.round(py), notches: Math.round(py / NOTCH_PX) });
      if (px) this.queue({ ev: 'axis', axis: 'h', delta: Math.round(px), notches: Math.round(px / NOTCH_PX) });
      this.flushNow();
    };
    const onKeyDown = (ev: KeyboardEvent) => {
      // Keep browser shortcuts/scroll from firing while a guest is focused;
      // the guest owns the keyboard.
      ev.preventDefault();
      ev.stopPropagation();
      // Drop browser autorepeat: Wayland clients repeat held keys themselves
      // via repeat_info, and re-sending a press for an already-pressed key is
      // both a double-repeat (browser timer + client timer) and a protocol
      // violation. Let the client be the single repeat authority
      // (REVIEW-X11-WAYLAND #11). Xwayland/X clients see the initial press and
      // do X autorepeat as usual.
      if (ev.repeat) return;
      window.wash?.focusWindow(this.windowID, this.origin);
      this.heldKeys.add(ev.code);
      this.queue({ ev: 'key', code: ev.code, state: 'down' });
      this.flushNow();
    };
    const onKeyUp = (ev: KeyboardEvent) => {
      ev.preventDefault();
      ev.stopPropagation();
      this.heldKeys.delete(ev.code);
      this.queue({ ev: 'key', code: ev.code, state: 'up' });
      this.flushNow();
    };
    // Release every held key when we lose focus or the tab is hidden — the
    // host browser eats the keyup that would otherwise arrive (Alt-Tab, tab
    // switch), leaving modifiers latched in the guest (REVIEW-X11-WAYLAND #9).
    const releaseHeld = () => {
      if (this.heldKeys.size === 0) return;
      for (const code of this.heldKeys) this.queue({ ev: 'key', code, state: 'up' });
      this.heldKeys.clear();
      this.flushNow();
    };
    const onVisibility = () => { if (document.hidden) releaseHeld(); };
    // Right-click must reach the guest, not pop the browser menu.
    const onContextMenu = (ev: Event) => ev.preventDefault();

    this.addEventListener('pointermove', onPointerMove);
    this.addEventListener('pointerdown', onPointerDown);
    this.addEventListener('pointerup', onPointerUp);
    this.addEventListener('wheel', onWheel, { passive: false });
    this.addEventListener('keydown', onKeyDown);
    this.addEventListener('keyup', onKeyUp);
    this.addEventListener('blur', releaseHeld);
    this.addEventListener('contextmenu', onContextMenu);
    document.addEventListener('visibilitychange', onVisibility);

    this.inputCleanup = () => {
      this.removeEventListener('pointermove', onPointerMove);
      this.removeEventListener('pointerdown', onPointerDown);
      this.removeEventListener('pointerup', onPointerUp);
      this.removeEventListener('wheel', onWheel);
      this.removeEventListener('keydown', onKeyDown);
      this.removeEventListener('keyup', onKeyUp);
      this.removeEventListener('blur', releaseHeld);
      this.removeEventListener('contextmenu', onContextMenu);
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }

  // queueMotion appends (coalescing) a surface-relative motion event. The
  // The canvas backing store may be HiDPI, but its CSS box is logical
  // surface size. DOM pointer offsets are therefore the logical coordinates
  // wlroots expects. Consecutive motions collapse to the latest.
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
    const w = windowById(this.origin, this.windowID);
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
    const w = windowById(this.origin, this.windowID);
    const s = screenSize();
    const maxX = s.w * VIEWPORTS_PER_AXIS - (w ? w.w : 0);
    const maxY = s.h * VIEWPORTS_PER_AXIS - (w ? w.h : 0);
    const x = Math.round(Math.max(0, Math.min(maxX, a.ox + (ev.clientX - a.px))));
    const y = Math.round(Math.max(0, Math.min(maxY, a.oy + (ev.clientY - a.py))));
    moveLocal(this.origin, this.windowID, x, y); // live; router commit happens on release
  }

  private endMove(): void {
    this.moving = false;
    this.moveAnchor = null;
    const w = windowById(this.origin, this.windowID);
    if (w) window.wash?.moveWindow(this.windowID, w.x, w.y);
  }

  // --- interactive resize (M8b) --------------------------------------
  // Driven by the guest's xdg_toplevel.resize ({resize:<edges>}). Like move,
  // we take over the grab (button-up to the guest), update the wash window box
  // live (resizeLocal, + moveLocal when a top/left edge shifts the origin),
  // and commit once on release. The router's window.resize round-trips to the
  // compositor's set_size, repainting the guest at the new size.
  private beginResize(edges: number): void {
    if (this.windowID < 0 || this.resizing || this.moving) return;
    const w = windowById(this.origin, this.windowID);
    if (!w) return;
    this.resizing = true;
    this.resizeAnchor = { px: this.lastClientX, py: this.lastClientY, ox: w.x, oy: w.y, ow: w.w, oh: w.h, edges };
    this.queue({ ev: 'button', btn: 'left', state: 'up' });
    this.flushNow();
  }

  private applyResize(ev: PointerEvent): void {
    const a = this.resizeAnchor;
    if (!a || this.windowID < 0) return;
    const dx = ev.clientX - a.px;
    const dy = ev.clientY - a.py;
    let x = a.ox, y = a.oy, w = a.ow, h = a.oh;
    if (a.edges & EDGE_RIGHT) w = Math.max(MIN_W, a.ow + dx);
    if (a.edges & EDGE_LEFT) {
      w = Math.max(MIN_W, a.ow - dx);
      x = a.ox + (a.ow - w); // keep the right edge fixed
    }
    if (a.edges & EDGE_BOTTOM) h = Math.max(MIN_H, a.oh + dy);
    if (a.edges & EDGE_TOP) {
      h = Math.max(MIN_H, a.oh - dy);
      y = a.oy + (a.oh - h); // keep the bottom edge fixed
    }
    w = Math.round(w);
    h = Math.round(h);
    resizeLocal(this.origin, this.windowID, w, h);
    if (Math.round(x) !== a.ox || Math.round(y) !== a.oy) moveLocal(this.origin, this.windowID, Math.round(x), Math.round(y));
  }

  private endResize(): void {
    const edges = this.resizeAnchor?.edges ?? 0;
    this.resizing = false;
    this.resizeAnchor = null;
    const w = windowById(this.origin, this.windowID);
    if (!w) return;
    window.wash?.resizeWindow(this.windowID, w.w, w.h);
    if (edges & (EDGE_LEFT | EDGE_TOP)) window.wash?.moveWindow(this.windowID, w.x, w.y);
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
  // BE routes by whichever is set). Addressed via sendAppMsg with the
  // (compound) instance id so a REMOTE display window's input routes to its
  // own host's wash-display, not the local router (docs/REMOTE.md §15.1) —
  // sendAppMsgTo would always go over the local connection.
  private sendInput(target: Record<string, number>, events: InputEvent[]): void {
    if (events.length === 0) return;
    if (typeof window === 'undefined' || !window.wash) return;
    window.wash.sendAppMsg(this.instanceID, { kind: 'input', ...target, events });
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
    if (this.unsubscribeResync) {
      try {
        this.unsubscribeResync();
      } catch {
        /* ignore */
      }
    }
    // Subscribe on the window's own origin so a remote display window reads
    // its host's relayed video channel, not a local channel of the same id.
    // (Remote video frames don't flow until the bind is un-gated in main.tsx;
    // this keeps the path origin-correct for when they do.)
    this.unsubscribe = subscribeRaw(this.origin, channelID, (bytes) => this.onFrame(bytes));
    // The router resets this channel (channel.resync) after it went "behind".
    // For a WebP frame stream the ring replay is meaningless (the router now
    // skips it), so clear the canvas to a known-blank state instead of leaving
    // stale/torn regions on screen forever (REVIEW-X11-WAYLAND #6).
    this.unsubscribeResync = subscribeResync(this.origin, channelID, () => this.onResync());
  }

  private onResync(): void {
    if (this.canvas && this.ctx) {
      this.ctx.clearRect(0, 0, this.canvas.width, this.canvas.height);
    }
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
        } else if (typeof ctrl.resize === 'number') {
          this.beginResize(ctrl.resize);
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

    createImageBitmap(new Blob([payload]))
      .then((bitmap) => {
        const canvas = this.canvas;
        const ctx = this.ctx;
        if (!canvas || !ctx) {
          bitmap.close?.();
          return;
        }
        // Keep the backing store in physical frame pixels while the CSS box
        // stays in logical surface pixels. On HiDPI this gives the browser a
        // dense canvas instead of visibly doubling the window size.
        if (header.frameW > 0 && header.frameH > 0) {
          if (canvas.width !== header.frameW) {
            canvas.width = header.frameW;
          }
          const cssW = frameCssPx(header.frameW);
          if (canvas.style.width !== cssW + 'px') canvas.style.width = cssW + 'px';
          if (canvas.height !== header.frameH) {
            canvas.height = header.frameH;
          }
          const cssH = frameCssPx(header.frameH);
          if (canvas.style.height !== cssH + 'px') canvas.style.height = cssH + 'px';
        }
        // Clear the dirty rect first so transparent pixels REPLACE (not
        // source-over blend onto) the previous frame — required now that
        // frames carry real alpha (M8c: shaped popups, rounded corners).
        ctx.clearRect(header.dirtyX, header.dirtyY, bitmap.width, bitmap.height);
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
    const unsub = subscribeRaw(this.origin, channelID, (bytes) => this.onPopupBytes(channelID, bytes));
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
    if (!p.canvas) this.ensurePopupCanvas(p);
    createImageBitmap(new Blob([payload]))
      .then((bitmap) => {
        const live = this.popups.get(channelID);
        if (!live || !live.canvas || !live.ctx) {
          bitmap.close?.();
          return;
        }
        const cv = live.canvas;
        if (header.frameW > 0 && cv.width !== header.frameW) {
          cv.width = header.frameW;
        }
        if (header.frameW > 0) {
          const cssW = frameCssPx(header.frameW);
          if (cv.style.width !== cssW + 'px') cv.style.width = cssW + 'px';
        }
        if (header.frameH > 0 && cv.height !== header.frameH) {
          cv.height = header.frameH;
        }
        if (header.frameH > 0) {
          const cssH = frameCssPx(header.frameH);
          if (cv.style.height !== cssH + 'px') cv.style.height = cssH + 'px';
        }
        // Clear first so the menu's transparent rounded corners / shadow
        // replace prior pixels rather than blending (M8c alpha).
        live.ctx.clearRect(header.dirtyX, header.dirtyY, bitmap.width, bitmap.height);
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
