// Floating window component. Mounts the app's custom element in a
// shadow-DOM-free slot (the element itself owns Shadow DOM); the
// frame owns titlebar, drag, focus-raise, and close.
//
// Geometry, focus, and state come from the WM store, which mirrors
// router state. Pointer interactions emit window.move / window.resize
// / window.focus / window.state on the wire; the router applies them
// and broadcasts a session.patch back, which lands in the store.

import { Show, createSignal, onCleanup, onMount } from 'solid-js';
import { registerMountedElement, unregisterMountedElement } from './api';
import {
  VIEWPORTS_PER_AXIS,
  CrashInfo,
  focused,
  moveLocal,
  raiseLocal,
  resizeLocal,
  screenSize,
  viewportFor,
  Win,
} from './wm';
import { AlertOctagon, Copy, Maximize2, Minimize2, Minus, X } from 'lucide-solid';
import { washAssetUrl } from '@wash/ui';

export interface WindowProps {
  win: Win;
  onClose: (windowID: number) => void;
}

export function FloatingWindow(props: WindowProps) {
  let slot!: HTMLDivElement;

  // Local "drag override" so the visible position tracks the cursor
  // at 60Hz without round-tripping every frame. On pointer-up we
  // send window.move once; the store catches up via the broadcast
  // patch a moment later and we clear the override.
  const [dragX, setDragX] = createSignal<number | null>(null);
  const [dragY, setDragY] = createSignal<number | null>(null);
  // Same idea for resize.
  const [resizeW, setResizeW] = createSignal<number | null>(null);
  const [resizeH, setResizeH] = createSignal<number | null>(null);

  onMount(() => {
    // Don't mount the (dead) custom element when the BE has already
    // crashed by the time we get a chance to render — the crash
    // tombstone owns the slot. props.win.crashed can also flip true
    // later, after the element is already mounted; the slot just
    // sits behind the absolute-positioned overlay in that case.
    if (props.win.crashed) {
      window.wash.focusWindow(props.win.windowID);
      return;
    }
    const el = document.createElement(props.win.element);
    el.setAttribute('data-wash-instance', props.win.instanceID);
    // window_id lets per-window shell built-ins (the <wash-app-display>
    // video decoder) find their video channel via the display-window
    // registry. Backward-compatible: existing app elements ignore it.
    el.setAttribute('data-wash-window', String(props.win.windowID));
    slot.appendChild(el);
    // Defer registerMountedElement to a microtask so the custom
    // element's Solid onMount has run and any `wash:state` /
    // `wash:msg` listeners are wired BEFORE registerMountedElement
    // dispatches them. Without this, apps that set up their
    // listeners inside onMount (e.g. wash-fm) miss the wash:state
    // event entirely and never initialize their FE state.
    queueMicrotask(() => registerMountedElement(props.win.instanceID, el));
    window.wash.focusWindow(props.win.windowID);
  });
  onCleanup(() => {
    unregisterMountedElement(props.win.instanceID);
  });

  const onTitlebarPointerDown = (ev: PointerEvent) => {
    ev.preventDefault();
    window.wash.focusWindow(props.win.windowID);
    const startX = ev.clientX;
    const startY = ev.clientY;
    const origX = props.win.x;
    const origY = props.win.y;
    const target = ev.currentTarget as HTMLElement;
    target.setPointerCapture(ev.pointerId);
    const onMove = (m: PointerEvent) => {
      // Clamp drag to the virtual-desktop plane (VIEWPORTS_PER_AXIS²
      // cells). Windows that escape would still be reachable via the
      // pager, but a window with its titlebar dragged past the right
      // edge of cell (2,*) would be orphaned for everyone without a
      // pager open. Clamping keeps the titlebar grabbable.
      const s = screenSize();
      const maxX = s.w * VIEWPORTS_PER_AXIS - props.win.w;
      const maxY = s.h * VIEWPORTS_PER_AXIS - props.win.h;
      const rawX = origX + (m.clientX - startX);
      const rawY = origY + (m.clientY - startY);
      setDragX(Math.round(Math.max(0, Math.min(maxX, rawX))));
      setDragY(Math.round(Math.max(0, Math.min(maxY, rawY))));
    };
    const onUp = () => {
      target.removeEventListener('pointermove', onMove);
      target.removeEventListener('pointerup', onUp);
      target.releasePointerCapture(ev.pointerId);
      const x = dragX();
      const y = dragY();
      if (x != null && y != null && (x !== origX || y !== origY)) {
        // Optimistic: commit to the store BEFORE clearing the
        // override so frameStyle reads the new position the next
        // frame, not the stale props.win.x while waiting for the
        // router's session.patch to land.
        moveLocal(props.win.windowID, x, y);
        window.wash.moveWindow(props.win.windowID, x, y);
      }
      setDragX(null);
      setDragY(null);
    };
    target.addEventListener('pointermove', onMove);
    target.addEventListener('pointerup', onUp);
  };

  const onWindowPointerDown = () => window.wash.focusWindow(props.win.windowID);

  // Pointer events don't fire during an HTML5 drag, so a drag from
  // one window's row to another window never raises the target.
  // Hook dragenter (fires once when the drag crosses into the frame)
  // to raise the window under the cursor so the user can see the
  // drop target. focusWindow is idempotent — re-entering an already-
  // focused window is a no-op.
  const onWindowDragEnter = () => window.wash.focusWindow(props.win.windowID);

  // Bottom-right resize: track override locally, commit on release.
  const onResizeHandlePointerDown = (ev: PointerEvent) => {
    ev.preventDefault();
    ev.stopPropagation();
    window.wash.focusWindow(props.win.windowID);
    const target = ev.currentTarget as HTMLElement;
    target.setPointerCapture(ev.pointerId);
    const startX = ev.clientX;
    const startY = ev.clientY;
    const origW = props.win.w;
    const origH = props.win.h;
    // Stream the resize to the BE live during the drag, throttled to one
    // in-flight animation frame, so the guest app reconfigures and repaints
    // continuously instead of only snapping once on release. Without this
    // the canvas just CSS-scales the last frame mid-drag ("janky / only
    // sometimes re-renders") and the guest never sees intermediate sizes.
    let rafPending: number | null = null;
    const flush = () => {
      rafPending = null;
      const w = resizeW();
      const h = resizeH();
      if (w != null && h != null) window.wash.resizeWindow(props.win.windowID, w, h);
    };
    const onMove = (m: PointerEvent) => {
      const newW = Math.max(160, Math.round(origW + (m.clientX - startX)));
      const newH = Math.max(80, Math.round(origH + (m.clientY - startY)));
      setResizeW(newW);
      setResizeH(newH);
      if (rafPending == null) rafPending = requestAnimationFrame(flush);
    };
    const onUp = () => {
      target.removeEventListener('pointermove', onMove);
      target.removeEventListener('pointerup', onUp);
      target.releasePointerCapture(ev.pointerId);
      if (rafPending != null) {
        cancelAnimationFrame(rafPending);
        rafPending = null;
      }
      const w = resizeW();
      const h = resizeH();
      if (w != null && h != null && (w !== origW || h !== origH)) {
        resizeLocal(props.win.windowID, w, h);
        window.wash.resizeWindow(props.win.windowID, w, h);
      }
      setResizeW(null);
      setResizeH(null);
    };
    target.addEventListener('pointermove', onMove);
    target.addEventListener('pointerup', onUp);
  };

  // Frame geometry depends on state. Maximized = top-left of viewport
  // minus the chrome's bottom taskbar (40 px hardcoded; will become
  // negotiable when the chrome publishes a workarea).
  // Window chrome: the stored window w/h is the CONTENT (slot) size — what
  // the app element / guest surface actually gets. The frame adds a fixed-
  // height titlebar + a 1px border per side. TITLEBAR_H MUST equal the
  // wash-titlebar element's height below, or the slot won't equal the stored
  // size and a guest surface (wash-app-display) will clip by the difference.
  const TITLEBAR_H = 28;
  const FRAME_BORDER = 1;

  const frameStyle = () => {
    // Chromeless windows (e.g. wash-washamp's Webamp) drop the frame
    // entirely: no titlebar, no border, transparent background — the
    // guest surface paints its own chrome edge-to-edge. We keep the
    // drop shadow so the floating window still reads as separate from
    // the desktop behind it.
    const chromeless = !!props.win.chromeless;
    const base = {
      position: 'absolute' as const,
      background: chromeless ? 'transparent' : '#222',
      border: chromeless
        ? 'none'
        : focused() === props.win.windowID
          ? '1px solid #66c'
          : '1px solid #444',
      'box-shadow': '0 6px 24px rgba(0,0,0,0.4)',
      display: 'flex',
      'flex-direction': 'column' as const,
      color: '#eee',
      'box-sizing': 'border-box' as const,
      'z-index': props.win.z,
      // Re-enable pointer events inside the viewport cam container,
      // which sets pointer-events:none so clicks in empty space fall
      // through to the desktop surface (taskbar, wallpaper).
      'pointer-events': 'auto' as const,
    };
    if (props.win.state === 'minimized') {
      return { ...base, display: 'none' };
    }
    if (props.win.state === 'maximized') {
      // The frame lives inside the viewport cam container, which is
      // translated by (-vx*W, -vy*H). A static left:0/top:0 only lines
      // up with the visible screen on cell (0,0); on any other cell the
      // window flies off toward the (0,0) corner. Anchor instead to the
      // window's home-viewport origin in plane coords so that, after the
      // cam translate, the frame lands flush at the screen's top-left.
      //
      // Geometry comes from screenSize() — a signal updated on the
      // window 'resize' event — so the maximized frame snaps to the
      // current screen edges and re-renders reactively when the browser
      // is resized (and stays aligned with the cam, which pans by the
      // same screenSize).
      //
      // --wash-reserved-right reserves screen width for the session
      // app's right sidebar (300px when open, 14px tab when hidden).
      // The session app writes the var on document.documentElement;
      // here we just respect it so a maximized window's titlebar
      // controls stay reachable instead of disappearing under chrome.
      const vp = viewportFor(props.win);
      const s = screenSize();
      return {
        ...base,
        left: `${vp.vx * s.w}px`,
        top: `${vp.vy * s.h}px`,
        width: `calc(${s.w}px - var(--wash-reserved-right, 0px))`,
        height: `${s.h - 40}px`,
      };
    }
    const x = dragX() ?? props.win.x;
    const y = dragY() ?? props.win.y;
    const w = resizeW() ?? props.win.w;
    const h = resizeH() ?? props.win.h;
    // Fade the frame slightly while dragging so the user can see
    // what's behind the window without releasing the pointer —
    // useful when dragging into a viewport boundary to gauge how
    // far you've moved.
    const dragging = dragX() !== null;
    // w/h is CONTENT (slot) size; a chromed frame adds a fixed titlebar +
    // 1px border per side, so the slot equals the stored size exactly —
    // guest surfaces fill it with no titlebar-height clip, and geometry
    // feedback can't shrink-spiral. A chromeless frame adds neither, so
    // the frame IS the content box.
    const bd = chromeless ? 0 : FRAME_BORDER;
    const tb = chromeless ? 0 : TITLEBAR_H;
    return {
      ...base,
      left: `${x}px`,
      top: `${y}px`,
      width: `${w + bd * 2}px`,
      height: `${h + tb + bd * 2}px`,
      opacity: dragging ? 0.72 : 1,
    };
  };

  // Double-click the titlebar toggles maximize ↔ normal.
  const onTitlebarDblClick = () => {
    if (props.win.state === 'maximized') {
      window.wash.restoreWindow(props.win.windowID);
    } else {
      window.wash.maximizeWindow(props.win.windowID);
    }
  };

  // Titlebar background tracks focus, EXCEPT for windows whose backing
  // process is uid 0 (or one of the privilege-chain reserved ids): a
  // dark red stripe identifies the privilege scope at a glance, and
  // the value comes from router-attested SessionWindow.is_root — apps
  // cannot self-declare it. The non-focused tone is a darker maroon
  // so focus is still distinguishable. A crashed window's titlebar
  // is a brighter red regardless of focus — the tombstone state needs
  // to be unmistakable.
  const titlebarBackground = () => {
    if (props.win.crashed) return '#8a1d1d';
    if (props.win.isRoot) {
      return focused() === props.win.windowID ? '#7a1f1f' : '#5a1818';
    }
    return focused() === props.win.windowID ? '#33387a' : '#2a2a2a';
  };

  // Icon color matches the source app's manifest accent. Apps that
  // didn't declare one fall back to a deterministic HSL hash of
  // their element tag — same algorithm as the start menu so the
  // launcher row and the titlebar icon agree. Root windows keep a
  // muted off-white so the accent doesn't clash with the red
  // privilege stripe (which already carries the identity signal).
  const titlebarIconColor = () => {
    if (props.win.isRoot) return undefined;
    if (props.win.accent) return props.win.accent;
    let h = 0;
    const k = props.win.element || props.win.instanceID;
    for (let i = 0; i < k.length; i++) {
      h = ((h << 5) - h + k.charCodeAt(i)) | 0;
    }
    return `hsl(${Math.abs(h) % 360} 55% 65%)`;
  };

  return (
    <div
      class="wash-window"
      classList={{ 'wash-window-root': !!props.win.isRoot }}
      data-is-root={props.win.isRoot ? 'true' : undefined}
      onPointerDown={onWindowPointerDown}
      onDragEnter={onWindowDragEnter}
      style={frameStyle()}
    >
      <Show when={!props.win.chromeless}>
      <div
        class="wash-titlebar"
        onPointerDown={onTitlebarPointerDown}
        onDblClick={onTitlebarDblClick}
        style={{
          display: 'flex',
          'align-items': 'center',
          height: `${TITLEBAR_H}px`,
          'box-sizing': 'border-box',
          padding: '0 8px',
          background: titlebarBackground(),
          cursor: 'move',
          'user-select': 'none',
          'font-size': '13px',
        }}
      >
        {props.win.icon && (
          <svg
            width="13"
            height="13"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
            style={{
              'flex-shrink': 0,
              'margin-right': '6px',
              opacity: 0.85,
              color: titlebarIconColor(),
            }}
            aria-hidden="true"
          >
            <use href={washAssetUrl(`icons.svg#${props.win.icon}`)} />
          </svg>
        )}
        <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>{props.win.title}</span>
        <button
          onPointerDown={(e) => e.stopPropagation()}
          onClick={(e) => {
            e.stopPropagation();
            window.wash.minimizeWindow(props.win.windowID);
          }}
          data-testid="window-minimize"
          aria-label="Minimize window"
          style={titlebarBtnStyle}
        >
          <Minus size={14} />
        </button>
        <button
          onPointerDown={(e) => e.stopPropagation()}
          onClick={(e) => {
            e.stopPropagation();
            if (props.win.state === 'maximized') {
              window.wash.restoreWindow(props.win.windowID);
            } else {
              window.wash.maximizeWindow(props.win.windowID);
            }
          }}
          data-testid="window-maximize"
          aria-label={props.win.state === 'maximized' ? 'Restore window' : 'Maximize window'}
          style={titlebarBtnStyle}
        >
          {props.win.state === 'maximized' ? <Minimize2 size={12} /> : <Maximize2 size={12} />}
        </button>
        <button
          // Stop pointerdown from bubbling so the titlebar's drag
          // handler does not capture the pointer and swallow the click.
          onPointerDown={(e) => e.stopPropagation()}
          onClick={(e) => {
            e.stopPropagation();
            props.onClose(props.win.windowID);
          }}
          data-testid="window-close"
          style={titlebarBtnStyle}
          aria-label="Close window"
        >
          <X size={14} />
        </button>
      </div>
      </Show>
      <div ref={slot} style={{ flex: 1, overflow: props.win.chromeless ? 'visible' : 'auto', position: 'relative' }}>
        <Show when={props.win.crashed}>
          <CrashPane info={props.win.crashed!} title={props.win.title} />
        </Show>
      </div>
      <Show when={!props.win.chromeless}>
        <div
          class="wash-resize-handle"
          data-testid="window-resize"
          onPointerDown={onResizeHandlePointerDown}
          title="Resize"
          style={{
            position: 'absolute',
            right: '0',
            bottom: '0',
            width: '14px',
            height: '14px',
            cursor: 'nwse-resize',
            'z-index': '1',
            background:
              'linear-gradient(135deg, transparent 50%, rgba(255,255,255,0.18) 50%, rgba(255,255,255,0.18) 70%, transparent 70%)',
          }}
        />
      </Show>
    </div>
  );
}

// Re-exported for the desktop button click path, which raises a
// window before invoking a wire helper.
export { raiseLocal };

// CrashPane renders the in-place tombstone for a window whose BE
// process exited abnormally. Replaces the (dead) custom element with
// a red-headed banner + scrollable log + Copy button. The window's
// close button still works (FE-only removal from the WM store).
function CrashPane(props: { info: CrashInfo; title: string }) {
  const [copied, setCopied] = createSignal(false);
  const summary = () => {
    const parts = [`exit ${props.info.exitCode}`];
    if (props.info.signal) parts.push(`signal ${props.info.signal}`);
    if (props.info.uptime) parts.push(`ran ${props.info.uptime}`);
    return parts.join(' • ');
  };
  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(props.info.log);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1_500);
    } catch {
      // clipboard blocked (insecure context, no permission) — the
      // textarea is still selectable, so this is just a nicety.
    }
  };
  return (
    <div
      data-testid="window-crashed"
      style={{
        position: 'absolute',
        inset: '0',
        background: '#1a0e0e',
        color: '#f4e4e4',
        display: 'flex',
        'flex-direction': 'column',
        'box-sizing': 'border-box',
        font: '12px ui-monospace, Menlo, Consolas, monospace',
      }}
    >
      <div
        style={{
          display: 'flex',
          'align-items': 'center',
          gap: '8px',
          padding: '10px 12px',
          background: '#3a0f0f',
          'border-bottom': '1px solid #5a1818',
          color: '#ffd0d0',
          font: '13px ui-sans-serif, system-ui, sans-serif',
        }}
      >
        <AlertOctagon size={16} />
        <div style={{ flex: 1 }}>
          <div style={{ 'font-weight': '600' }}>{props.title} crashed</div>
          <div style={{ opacity: '0.75', 'font-size': '11px' }}>
            {props.info.appID} — {summary()}
          </div>
        </div>
        <button
          type="button"
          onClick={onCopy}
          data-testid="window-crashed-copy"
          aria-label="Copy crash log"
          title="Copy log to clipboard"
          style={{
            display: 'flex',
            'align-items': 'center',
            gap: '4px',
            background: copied() ? '#2d5a2d' : '#5a1818',
            color: '#fff',
            border: '1px solid #7a2828',
            'border-radius': '3px',
            padding: '4px 8px',
            cursor: 'pointer',
            font: 'inherit',
            'font-size': '12px',
          }}
        >
          <Copy size={12} />
          {copied() ? 'copied' : 'copy'}
        </button>
      </div>
      <textarea
        data-testid="window-crashed-log"
        readonly
        spellcheck={false}
        value={props.info.log || '(no output captured)'}
        style={{
          flex: 1,
          margin: '0',
          padding: '10px 12px',
          background: '#10070a',
          color: '#f4d0d0',
          border: 'none',
          outline: 'none',
          resize: 'none',
          'font-family': 'inherit',
          'font-size': '12px',
          'line-height': '1.45',
          'white-space': 'pre',
          'overflow': 'auto',
        }}
      />
    </div>
  );
}

const titlebarBtnStyle = {
  background: 'transparent',
  color: '#eee',
  border: 'none',
  'font-size': '12px',
  cursor: 'pointer',
  padding: '0 8px',
  'line-height': '16px',
};
