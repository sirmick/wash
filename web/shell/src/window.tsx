// Floating window component. Mounts the app's custom element in a
// shadow-DOM-free slot (the element itself owns Shadow DOM); the
// frame owns titlebar, drag, focus-raise, and close.

import { onCleanup, onMount } from 'solid-js';
import { registerMountedElement, unregisterMountedElement } from './api';
import { focused, move, raise, removeWindow, Win } from './wm';

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

  return (
    <div
      class="wash-window"
      onPointerDown={onWindowPointerDown}
      style={{
        position: 'absolute',
        left: `${props.win.x}px`,
        top: `${props.win.y}px`,
        width: `${props.win.w}px`,
        height: `${props.win.h}px`,
        'z-index': props.win.z,
        background: '#222',
        border: focused() === props.win.windowID ? '1px solid #66c' : '1px solid #444',
        'box-shadow': '0 6px 24px rgba(0,0,0,0.4)',
        display: 'flex',
        'flex-direction': 'column',
        color: '#eee',
        'box-sizing': 'border-box',
      }}
    >
      <div
        class="wash-titlebar"
        onPointerDown={onTitlebarPointerDown}
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
    </div>
  );
}

// Convenience for the desktop button click path.
export function destroyWindow(windowID: number) {
  removeWindow(windowID);
}
