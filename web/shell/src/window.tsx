// Floating window component. Mounts the app's custom element in a
// shadow-DOM-free slot (the element itself owns Shadow DOM); the
// frame owns titlebar, drag, focus-raise, and close.
//
// Geometry, focus, and state come from the WM store, which mirrors
// router state. Pointer interactions emit window.move / window.resize
// / window.focus / window.state on the wire; the router applies them
// and broadcasts a session.patch back, which lands in the store.

import { createSignal, onCleanup, onMount } from 'solid-js';
import { registerMountedElement, unregisterMountedElement } from './api';
import { focused, moveLocal, raiseLocal, resizeLocal, Win } from './wm';
import { Maximize2, Minimize2, Minus, X } from 'lucide-solid';

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
    const el = document.createElement(props.win.element);
    el.setAttribute('data-wash-instance', props.win.instanceID);
    slot.appendChild(el);
    registerMountedElement(props.win.instanceID, el);
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
      setDragX(Math.round(origX + (m.clientX - startX)));
      setDragY(Math.round(origY + (m.clientY - startY)));
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
    const onMove = (m: PointerEvent) => {
      const newW = Math.max(160, Math.round(origW + (m.clientX - startX)));
      const newH = Math.max(80, Math.round(origH + (m.clientY - startY)));
      setResizeW(newW);
      setResizeH(newH);
    };
    const onUp = () => {
      target.removeEventListener('pointermove', onMove);
      target.removeEventListener('pointerup', onUp);
      target.releasePointerCapture(ev.pointerId);
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
  const frameStyle = () => {
    const base = {
      position: 'absolute' as const,
      background: '#222',
      border:
        focused() === props.win.windowID
          ? '1px solid #66c'
          : '1px solid #444',
      'box-shadow': '0 6px 24px rgba(0,0,0,0.4)',
      display: 'flex',
      'flex-direction': 'column' as const,
      color: '#eee',
      'box-sizing': 'border-box' as const,
      'z-index': props.win.z,
    };
    if (props.win.state === 'minimized') {
      return { ...base, display: 'none' };
    }
    if (props.win.state === 'maximized') {
      return {
        ...base,
        left: '0',
        top: '0',
        width: '100vw',
        height: 'calc(100vh - 40px)',
      };
    }
    const x = dragX() ?? props.win.x;
    const y = dragY() ?? props.win.y;
    const w = resizeW() ?? props.win.w;
    const h = resizeH() ?? props.win.h;
    return {
      ...base,
      left: `${x}px`,
      top: `${y}px`,
      width: `${w}px`,
      height: `${h}px`,
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

  return (
    <div
      class="wash-window"
      onPointerDown={onWindowPointerDown}
      style={frameStyle()}
    >
      <div
        class="wash-titlebar"
        onPointerDown={onTitlebarPointerDown}
        onDblClick={onTitlebarDblClick}
        style={{
          display: 'flex',
          'align-items': 'center',
          padding: '6px 8px',
          background: focused() === props.win.windowID ? '#33387a' : '#2a2a2a',
          cursor: 'move',
          'user-select': 'none',
          'font-size': '13px',
        }}
      >
        <span style={{ flex: 1 }}>{props.win.title}</span>
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
      <div ref={slot} style={{ flex: 1, overflow: 'auto' }} />
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
    </div>
  );
}

// Keep the symbol around even though raise* is gone — older callers
// (the desktop button click path) reach for it.
export { raiseLocal };

const titlebarBtnStyle = {
  background: 'transparent',
  color: '#eee',
  border: 'none',
  'font-size': '12px',
  cursor: 'pointer',
  padding: '0 8px',
  'line-height': '16px',
};
