// The Ctrl+Alt+Tab window-switcher overlay (docs/Review-findings.md P2
// cross-app, "no Alt+Tab").
//
// Chrome, not an app: it draws above every window (including a maximised
// one) and above the modal blur is deliberately NOT true — a summoned modal
// owns the seat, and the switcher sits below it. The decisions behind it
// (MRU order, which row starts highlighted, cycling) live in switcher.ts so
// they are unit-testable; this file only paints the list main.tsx hands it.
//
// Nothing here is clickable: the overlay exists only while Ctrl+Alt is held
// down, which is a keyboard gesture from beginning to end. A mouse user
// already has the taskbar.

import { For, Show } from 'solid-js';
import type { Component } from 'solid-js';
import { tokens, washAssetUrl } from '@wash/ui';
import type { Win } from './wm';

export const SwitcherOverlay: Component<{ wins: ReadonlyArray<Win>; index: number }> = (props) => (
  <div
    data-testid="window-switcher"
    data-wash-no-hit
    style={{
      position: 'fixed',
      inset: '0',
      display: 'flex',
      'align-items': 'center',
      'justify-content': 'center',
      // Above every window and the taskbar; below the modal layer.
      'z-index': 2_000_000,
      'pointer-events': 'none',
    }}
  >
    <div
      style={{
        display: 'flex',
        'flex-direction': 'column',
        gap: '2px',
        'min-width': '280px',
        'max-width': 'min(70vw, 560px)',
        padding: '8px',
        background: tokens.bgMenu,
        color: tokens.fg,
        border: `1px solid ${tokens.borderMenu}`,
        'border-radius': tokens.radiusMd,
        'box-shadow': tokens.shadowMenu,
        font: tokens.type.text,
      }}
    >
      <For each={props.wins}>
        {(w, i) => (
          <div
            data-testid="window-switcher-item"
            data-window-id={w.windowID}
            data-selected={i() === props.index ? 'true' : undefined}
            style={{
              display: 'flex',
              'align-items': 'center',
              gap: '8px',
              padding: '6px 10px',
              'border-radius': tokens.radiusSm,
              background: i() === props.index ? tokens.bgRowSelected : 'transparent',
              color: tokens.fg,
              overflow: 'hidden',
            }}
          >
            <Show when={w.icon}>
              {(icon) => (
                <svg
                  width="16"
                  height="16"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  style={{ 'flex-shrink': 0, opacity: 0.9 }}
                  aria-hidden="true"
                >
                  <use href={washAssetUrl(`icons.svg#${icon()}`)} />
                </svg>
              )}
            </Show>
            <span
              style={{
                flex: 1,
                overflow: 'hidden',
                'text-overflow': 'ellipsis',
                'white-space': 'nowrap',
              }}
            >
              {w.title || w.element}
            </span>
            {/* A minimised window is a legitimate switch target (the
                commit path restores it), but say so, or picking it looks
                like nothing happened. */}
            <Show when={w.state === 'minimized'}>
              <span style={{ opacity: 0.6, 'font-size': tokens.fontSizeSm }}>minimised</span>
            </Show>
          </div>
        )}
      </For>
    </div>
  </div>
);
