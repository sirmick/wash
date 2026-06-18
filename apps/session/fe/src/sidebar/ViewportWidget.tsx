// ViewportWidget is the sidebar's section body for virtual-desktop
// navigation. It hosts the Pager — the same 3x3 cell grid that used
// to float above the taskbar (M4 removed the floating placement; see
// the Pager component in apps/session/fe/src/main.tsx).
//
// Keeping this as a separate component (vs. inlining the Pager in
// App) lets future widgets in this section grow alongside the pager
// (e.g. a "name this viewport" caption, per-cell window counts) and
// keeps the Sidebar wiring uniform — every section's body is one
// widget component.

import type { Component } from 'solid-js';
import type { JSX } from 'solid-js';
import { tokens } from '@wash/ui';

export interface ViewportWidgetProps {
  /** Render-prop hand-off so the widget doesn't have to know about
   *  the Pager's full prop shape. The session app passes
   *  () => <Pager ... />. */
  renderPager: () => JSX.Element;
}

export const ViewportWidget: Component<ViewportWidgetProps> = (props) => {
  return (
    <div
      data-testid="viewport-widget"
      style={{
        display: 'flex',
        'flex-direction': 'column',
        gap: '8px',
        'align-items': 'stretch',
      }}
    >
      {props.renderPager()}
      <div
        style={{
          'font-size': '11px',
          opacity: 0.55,
          color: tokens.fgMuted,
          'text-align': 'center',
        }}
      >
        Ctrl+Alt+arrows to pan
      </div>
    </div>
  );
};
