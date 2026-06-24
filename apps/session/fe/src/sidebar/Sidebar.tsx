// Sidebar is the right-edge chrome that hosts the widget sections.
// Two modes (M4 scope):
//
//   open    — 300px panel; sections render full chrome
//   hidden  — 0px (a 14px tab on the right edge with a chevron lets
//             the user re-open; Ctrl+Alt+S toggles too)
//
// Rail mode (icons-only ~48px) is in the plan but deferred — open/
// hidden covers the demo case and avoids the rail's mini-icon layout
// before we know which widgets need it. Add later by widening the
// `mode` union.
//
// Sidebar sits ABOVE the taskbar (z-index just below taskbar's
// 10000) so the taskbar always wins on overlap. Positioned
// right:0; bottom:taskbar-height — the taskbar's position (top or
// bottom) flips the inset accordingly.

import type { Component, JSX } from 'solid-js';
import { Show } from 'solid-js';
import { tokens } from '@wash/ui';

// Glassy themed surfaces: the pack's menu/hover surface at partial
// opacity so the wallpaper shows through the blur, but the tint follows
// the active pack (dark on Midnight/Tokyo, light on Seoul).
const GLASS_PANEL = `color-mix(in srgb, ${tokens.bgMenu} 92%, transparent)`;
const GLASS_TAB = `color-mix(in srgb, ${tokens.bgMenu} 78%, transparent)`;
const GLASS_TAB_HOVER = `color-mix(in srgb, ${tokens.bgRowHover} 90%, transparent)`;

export type SidebarMode = 'open' | 'hidden';

/** Width of the sidebar in open mode. Hard-coded for M4; theming /
 *  user-resize lands later. */
export const SIDEBAR_OPEN_WIDTH = 300;
/** Width of the always-visible toggle tab when sidebar is hidden. */
export const SIDEBAR_TAB_WIDTH = 14;

export interface SidebarProps {
  mode: SidebarMode;
  /** Taskbar's vertical position so we can inset on the right axis
   *  that doesn't collide with it. */
  taskbarPos: 'top' | 'bottom';
  /** Height of the taskbar in pixels (used to compute the sidebar's
   *  bottom/top inset). */
  taskbarHeight: number;
  onToggle: () => void;
  children?: JSX.Element;
}

export const Sidebar: Component<SidebarProps> = (props) => {
  const isOpen = () => props.mode === 'open';

  const panelStyle = (): JSX.CSSProperties => {
    const base: JSX.CSSProperties = {
      position: 'absolute',
      right: 0,
      width: `${SIDEBAR_OPEN_WIDTH}px`,
      background: GLASS_PANEL,
      'backdrop-filter': 'blur(10px)',
      '-webkit-backdrop-filter': 'blur(10px)',
      'border-left': `1px solid ${tokens.borderMenu}`,
      display: 'flex',
      'flex-direction': 'column',
      'z-index': 9999, // just below taskbar (10000)
      'box-sizing': 'border-box',
      transition: 'transform 180ms cubic-bezier(.2,.7,.2,1)',
      transform: isOpen() ? 'translateX(0)' : `translateX(${SIDEBAR_OPEN_WIDTH}px)`,
      'box-shadow': '-4px 0 16px rgba(0,0,0,0.35)',
      // Hidden mode keeps the panel translated off-screen but its
      // 300px-wide bounding box still intercepts pointer events on
      // the right edge — disable hit-testing entirely while hidden so
      // windows underneath stay interactive.
      'pointer-events': isOpen() ? ('auto' as const) : ('none' as const),
    };
    if (props.taskbarPos === 'top') {
      base.top = `${props.taskbarHeight}px`;
      base.bottom = 0;
    } else {
      base.top = 0;
      base.bottom = `${props.taskbarHeight}px`;
    }
    return base;
  };

  const tabStyle = (): JSX.CSSProperties => {
    const base: JSX.CSSProperties = {
      position: 'absolute',
      right: 0,
      width: `${SIDEBAR_TAB_WIDTH}px`,
      background: GLASS_TAB,
      'border-left': `1px solid ${tokens.borderMenu}`,
      cursor: 'pointer',
      'z-index': 9998,
      display: 'flex',
      'align-items': 'center',
      'justify-content': 'center',
      color: tokens.fgMuted,
      transition: 'background 120ms, color 120ms',
      'font-size': '11px',
    };
    if (props.taskbarPos === 'top') {
      base.top = `${props.taskbarHeight}px`;
      base.bottom = 0;
    } else {
      base.top = 0;
      base.bottom = `${props.taskbarHeight}px`;
    }
    return base;
  };

  const headerStyle: JSX.CSSProperties = {
    height: '34px',
    display: 'flex',
    'align-items': 'center',
    'justify-content': 'space-between',
    padding: '0 10px',
    'border-bottom': `1px solid ${tokens.borderMenu}`,
    color: tokens.fgMuted,
    font: tokens.type.textSm,
    'letter-spacing': '0.1em',
    'text-transform': 'uppercase',
    'flex-shrink': 0,
  };

  const closeBtnStyle: JSX.CSSProperties = {
    background: 'transparent',
    border: 'none',
    color: tokens.fgDim,
    cursor: 'pointer',
    'font-size': '14px',
    'line-height': 1,
    padding: '2px 6px',
    'border-radius': tokens.radiusSm,
  };

  return (
    <>
      <div
        data-testid="sidebar"
        data-mode={props.mode}
        style={panelStyle()}
      >
        <div style={headerStyle}>
          <span style={{ 'font-weight': 600 }}>sidebar</span>
          <button
            type="button"
            data-testid="sidebar-close"
            title="Hide sidebar (Ctrl+Alt+S)"
            onClick={props.onToggle}
            style={closeBtnStyle}
          >
            ›
          </button>
        </div>
        <div
          data-testid="sidebar-sections"
          style={{
            flex: 1,
            'overflow-y': 'auto',
            'overflow-x': 'hidden',
            'scrollbar-width': 'thin',
          }}
        >
          {props.children}
        </div>
      </div>
      <Show when={!isOpen()}>
        <div
          data-testid="sidebar-tab"
          title="Show sidebar (Ctrl+Alt+S)"
          onClick={props.onToggle}
          style={tabStyle()}
          onMouseEnter={(ev) => (ev.currentTarget.style.background = GLASS_TAB_HOVER)}
          onMouseLeave={(ev) => (ev.currentTarget.style.background = GLASS_TAB)}
        >
          ‹
        </div>
      </Show>
    </>
  );
};
