// Section is the collapsible primitive for the right sidebar.
// Header is always visible; the body collapses on demand.
//
// State is binary: `expanded` or `collapsed`. The Sidebar's auto-
// expand-on-event behaviour fires for collapsed sections only; a
// user who collapses a section after an event ticks accepts that
// the next event will re-open it. The auto-expand is the only
// behaviour distinction worth modelling.

import type { Component, JSX } from 'solid-js';
import { Show } from 'solid-js';
import { washAssetUrl } from '@wash/ui';

export type SectionState = 'collapsed' | 'expanded';

export interface SectionProps {
  /** Stable id used in persistence + for data-testid hooks. */
  id: string;
  title: string;
  /** Optional Lucide sprite icon name rendered left of the title. */
  icon?: string;
  /** Optional color for the icon glyph only — gives each section a bit
   *  of per-widget identity without tinting the chevron/badge. Falls
   *  back to `accent`, then a neutral grey. */
  iconColor?: string;
  state: SectionState;
  /** Flip between expanded and collapsed. */
  onToggle: () => void;
  /** Optional badge text rendered right-aligned in the header (e.g. "3"
   *  for a count of pending items). Hidden when empty. */
  badge?: string;
  /** Optional accent color for the chevron + badge — used by widgets
   *  that want a strong visual signal (e.g. priv = red trust). */
  accent?: string;
  children?: JSX.Element;
}

const HEADER_HEIGHT = 32;

export const Section: Component<SectionProps> = (props) => {
  const isOpen = () => props.state === 'expanded';

  const headerStyle = (): JSX.CSSProperties => ({
    height: `${HEADER_HEIGHT}px`,
    display: 'flex',
    'align-items': 'center',
    gap: '6px',
    padding: '0 8px',
    cursor: 'pointer',
    background: 'rgba(255,255,255,0.02)',
    'border-bottom': '1px solid #1f1f30',
    'user-select': 'none',
    color: '#ddd',
    font: '12px ui-sans-serif,system-ui,sans-serif',
    'letter-spacing': '0.02em',
    'text-transform': 'uppercase',
    opacity: 0.9,
  });

  const chevronStyle = (): JSX.CSSProperties => ({
    'flex-shrink': 0,
    width: '12px',
    height: '12px',
    display: 'inline-flex',
    'align-items': 'center',
    'justify-content': 'center',
    transition: 'transform 120ms',
    transform: isOpen() ? 'rotate(90deg)' : 'rotate(0deg)',
    color: props.accent ?? '#8a8a9a',
  });

  return (
    <div
      data-testid={`sidebar-section-${props.id}`}
      data-state={props.state}
      style={{ display: 'flex', 'flex-direction': 'column' }}
    >
      <div
        style={headerStyle()}
        onClick={props.onToggle}
        data-testid={`sidebar-section-header-${props.id}`}
      >
        <span style={chevronStyle()}>▶</span>
        <Show when={props.icon}>
          <span
            style={{
              'flex-shrink': 0,
              color: props.iconColor ?? props.accent ?? '#a0a0b0',
              display: 'inline-flex',
              'align-items': 'center',
            }}
          >
            <svg
              width="14"
              height="14"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <use href={washAssetUrl(`icons.svg#${props.icon}`)} />
            </svg>
          </span>
        </Show>
        <span style={{ flex: 1, 'font-weight': 600 }}>{props.title}</span>
        <Show when={props.badge}>
          <span
            data-testid={`sidebar-section-badge-${props.id}`}
            style={{
              'font-size': '10px',
              padding: '1px 6px',
              'border-radius': '8px',
              background: props.accent ?? '#3a3a5a',
              color: '#fff',
              'font-weight': 700,
              'text-transform': 'none',
              'letter-spacing': 0,
            }}
          >
            {props.badge}
          </span>
        </Show>
      </div>
      <Show when={isOpen()}>
        <div
          data-testid={`sidebar-section-body-${props.id}`}
          style={{
            padding: '8px 10px',
            'border-bottom': '1px solid #1f1f30',
            color: '#ccc',
            font: '12px ui-sans-serif,system-ui,sans-serif',
          }}
        >
          {props.children}
        </div>
      </Show>
    </div>
  );
};
