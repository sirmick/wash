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
import { Button, tokens } from '@wash/ui';

// Glassy themed surfaces: the pack's menu/hover surface at partial
// opacity so the wallpaper shows through the blur, but the tint follows
// the active pack (dark on Midnight/Tokyo, light on Seoul).
const GLASS_PANEL = `color-mix(in srgb, ${tokens.bgMenu} 85%, transparent)`;
const GLASS_TAB = `color-mix(in srgb, ${tokens.bgMenu} 70%, transparent)`;
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
  /**
   * What is waiting behind a hidden sidebar, as text ('' = nothing). The
   * 14px tab is the last rung of the interrupt ladder (docs/AGENT_UX.md
   * §4): with the rail hidden, an agent blocked on a question had no way
   * of saying so short of a toast that has already faded. Rendered only
   * in hidden mode — an open sidebar shows its own section badges.
   */
  badge?: string;
  /** Logged-in user + hostname, shown in the header as `user@host` —
   *  a useful at-a-glance "which machine/session am I on" instead of a
   *  redundant "Sidebar" label. */
  user?: string;
  host?: string;
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
      'backdrop-filter': 'blur(6px)',
      '-webkit-backdrop-filter': 'blur(6px)',
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
    'flex-shrink': 0,
  };

  // Override layer atop <Button variant="icon"> (transparent, borderless,
  // radius) — keep the glyph's auto size, dim color, and tight padding.
  const closeBtnStyle: JSX.CSSProperties = {
    width: 'auto',
    height: 'auto',
    color: tokens.fgDim,
    'font-size': '14px',
    'line-height': 1,
    padding: '2px 6px',
  };

  return (
    <>
      <div
        data-testid="sidebar"
        data-mode={props.mode}
        style={panelStyle()}
      >
        <div style={headerStyle}>
          <span
            style={{ 'font-weight': 600, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}
            title="Current user @ this session's host"
          >
            {props.user && props.host ? `${props.user}@${props.host}` : props.host || props.user || ''}
          </span>
          <Button
            variant="icon"
            data-testid="sidebar-close"
            title="Hide sidebar (Ctrl+Alt+S)"
            onClick={props.onToggle}
            style={closeBtnStyle}
          >
            ›
          </Button>
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
          title={
            props.badge
              ? `${props.badge} waiting on you — show sidebar (Ctrl+Alt+S)`
              : 'Show sidebar (Ctrl+Alt+S)'
          }
          onClick={props.onToggle}
          style={tabStyle()}
          onMouseEnter={(ev) => (ev.currentTarget.style.background = GLASS_TAB_HOVER)}
          onMouseLeave={(ev) => (ev.currentTarget.style.background = GLASS_TAB)}
        >
          <Show when={props.badge} fallback={'‹'}>
            {/* A count, not a glyph: at 14px there is no room for both,
                and "2" beside a chevron reads as a chevron with a number
                on it — which is exactly what it is. */}
            <span
              data-testid="sidebar-tab-badge"
              style={{
                'font-size': '9px',
                padding: '1px 2px',
                'border-radius': tokens.radiusXl,
                background: tokens.accentAmber,
                color: '#fff',
                'font-weight': 700,
                'line-height': 1.1,
              }}
            >
              {props.badge}
            </span>
          </Show>
        </div>
      </Show>
    </>
  );
};
