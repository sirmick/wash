// Floating window component. Mounts the app's custom element in a
// shadow-DOM-free slot (the element itself owns Shadow DOM); the
// frame owns titlebar, drag, focus-raise, and close.

import { onCleanup, onMount } from 'solid-js';
import { registerMountedElement, unregisterMountedElement } from './api';
import { focused, move, raise, removeWindow, resize, Win } from './wm';

export interface WindowProps {
  win: Win;
  onClose: (windowID: number) => void;
}

export function FloatingWindow(props: WindowProps) {
  let slot!: HTMLDivElement;

  onMount(() => {
    const el = document.createElement(props.win.element);
    el.setAttribute('data-wash-instance', props.win.instanceID);
    slot.appendChild(el);
    // Register with the BE→FE message dispatcher; any messages that
    // arrived during the render gap are flushed here.
    registerMountedElement(props.win.instanceID, el);
    // Tell the router the window has focus so the BE sees it.
    window.wash.focusWindow(props.win.windowID);
  });
  onCleanup(() => {
    unregisterMountedElement(props.win.instanceID);
  });

  const onTitlebarPointerDown = (ev: PointerEvent) => {
    ev.preventDefault();
    raise(props.win.windowID);
    const startX = ev.clientX;
    const startY = ev.clientY;
    const origX = props.win.x;
    const origY = props.win.y;
    const target = ev.currentTarget as HTMLElement;
    target.setPointerCapture(ev.pointerId);
    const onMove = (m: PointerEvent) => {
      move(props.win.windowID, origX + (m.clientX - startX), origY + (m.clientY - startY));
    };
    const onUp = () => {
      target.removeEventListener('pointermove', onMove);
      target.removeEventListener('pointerup', onUp);
      target.releasePointerCapture(ev.pointerId);
    };
    target.addEventListener('pointermove', onMove);
    target.addEventListener('pointerup', onUp);
  };

  // Local raise + notify the router so the app's BE gets a focus event.
  const focusWindow = () => {
    raise(props.win.windowID);
    window.wash.focusWindow(props.win.windowID);
  };
  const onWindowPointerDown = () => focusWindow();

  // Bottom-right resize: drag updates the WM state locally; on commit
  // (pointerup) we tell the router so the BE gets EvtWindowResize.
  // Live-resize for terminal-style apps is a later opt-in.
  const onResizeHandlePointerDown = (ev: PointerEvent) => {
    ev.preventDefault();
    ev.stopPropagation();
    raise(props.win.windowID);
    const target = ev.currentTarget as HTMLElement;
    target.setPointerCapture(ev.pointerId);
    const startX = ev.clientX;
    const startY = ev.clientY;
    const origW = props.win.w;
    const origH = props.win.h;
    const onMove = (m: PointerEvent) => {
      const newW = Math.max(160, origW + (m.clientX - startX));
      const newH = Math.max(80, origH + (m.clientY - startY));
      resize(props.win.windowID, newW, newH);
    };
    const onUp = () => {
      target.removeEventListener('pointermove', onMove);
      target.removeEventListener('pointerup', onUp);
      target.releasePointerCapture(ev.pointerId);
      window.wash.resizeWindow(props.win.windowID, props.win.w, props.win.h);
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
    return {
      ...base,
      left: `${props.win.x}px`,
      top: `${props.win.y}px`,
      width: `${props.win.w}px`,
      height: `${props.win.h}px`,
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
          _
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
          {props.win.state === 'maximized' ? '❐' : '□'}
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
          style={{
            background: 'transparent',
            color: '#eee',
            border: 'none',
            'font-size': '14px',
            cursor: 'pointer',
            padding: '0 6px',
          }}
          aria-label="Close window"
        >
          ×
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
          // Light hint via a corner triangle of gradient.
          background:
            'linear-gradient(135deg, transparent 50%, rgba(255,255,255,0.18) 50%, rgba(255,255,255,0.18) 70%, transparent 70%)',
        }}
      />
    </div>
  );
}

// Convenience for the desktop button click path.
export function destroyWindow(windowID: number) {
  removeWindow(windowID);
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
