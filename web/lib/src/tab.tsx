// <Tab> — the document-tab control: wash-edit's open files, wash-term's
// shells, and anything else that wants the browser-tab idiom (a strip of
// closeable tabs above a content surface).
//
// Extracted because the two existing implementations had drifted on every
// dimension that makes them look like one desktop: edit's tab was 26px with
// a hardcoded 6px top radius going bgWindow when active, term's was
// auto-height with tokens.radiusLg going bgRowSelected, one in the UI sans
// and the other in mono, and their close affordances shared nothing. Two
// hand-rolled tabs drifting is the same argument that produced <MenuBar>.
//
// Deliberately NOT covering the OTHER tab idiom in wash: wash-net's section
// nav (an underline that marks the current pane) and the sidebar's icon
// rail are different controls that happen to share the word, and folding
// them in here would produce a component with two personalities.
//
// The strip itself stays with the caller. edit's is a plain flex row; term's
// is an absolutely-positioned per-pane overlay carrying split geometry
// (docs/TERM_LAYOUT.md §6). Those have nothing in common worth sharing — the
// tab does.

import { Show, splitProps } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';

export interface TabProps extends JSX.ButtonHTMLAttributes<HTMLButtonElement> {
  /** The selected tab: takes the content surface, so it reads as joined to
   *  the pane below rather than as a button sitting above it. */
  active?: boolean;
  /** Identity stripe along the top edge — wash-term's per-tab tag colours.
   *  An active tab with no accent gets the blue selection accent. */
  accent?: string;
  /** Before the label: a status badge, a file-type icon. */
  leading?: JSX.Element;
  /** Renders the close affordance. Receives the click, already stopped from
   *  reaching the tab's own onClick. */
  onClose?: () => void;
  /** What the close affordance shows. Defaults to ×; wash-edit passes ● for
   *  a tab with unsaved changes. */
  closeGlyph?: JSX.Element;
  closeTestId?: string;
  closeTitle?: string;
  /** Monospace label — shell tabs, where the name is a path or a command. */
  mono?: boolean;
  /** Dimmed because this tab is the one being dragged. */
  dragging?: boolean;
  /** Marks this tab as the drop slot during a reorder drag. */
  dropBefore?: boolean;
}

/** Height of a tab, and so the min-height a tab strip needs. */
export const TAB_HEIGHT = 26;

export const Tab: Component<TabProps> = (props) => {
  const [local, rest] = splitProps(props, [
    'active', 'accent', 'leading', 'onClose', 'closeGlyph', 'closeTestId',
    'closeTitle', 'mono', 'dragging', 'dropBefore', 'style', 'children', 'type',
  ]);
  // An active tab is always striped: its own accent if it has one, else the
  // blue selection accent. An inactive tab shows its accent (so a tagged tab
  // keeps its identity in the strip) but nothing otherwise — and reserves the
  // 2px either way, so selecting a tab doesn't shift the strip.
  const stripe = () => (local.active ? (local.accent ?? tokens.accentBlue) : (local.accent ?? 'transparent'));
  return (
    <button
      type={local.type ?? 'button'}
      data-wash-hit
      data-active={local.active ? 'true' : undefined}
      style={{
        display: 'flex',
        'align-items': 'center',
        gap: '6px',
        height: `${TAB_HEIGHT}px`,
        padding: '0 4px 0 8px',
        border: 'none',
        'border-top': `2px solid ${stripe()}`,
        'border-right': `1px solid ${tokens.borderMenu}`,
        // Rounded on top only: the bottom edge meets the strip's
        // border-bottom flush, which is what makes it read as a tab.
        'border-radius': `${tokens.radiusLg} ${tokens.radiusLg} 0 0`,
        // The active tab wears the CONTENT surface, not a highlight colour —
        // that is what joins it to the pane below.
        background: local.active ? tokens.bgWindow : 'transparent',
        color: local.active ? tokens.fg : tokens.fgMuted,
        font: local.mono ? tokens.type.monoMd : tokens.type.textMd,
        'user-select': 'none',
        'max-width': '200px',
        'flex-shrink': 0,
        overflow: 'hidden',
        'white-space': 'nowrap',
        opacity: local.dragging ? 0.4 : undefined,
        'box-shadow': local.dropBefore ? `inset 3px 0 0 ${tokens.accentBlue}` : undefined,
        ...((local.style as JSX.CSSProperties | undefined) ?? {}),
      }}
      {...rest}
    >
      <Show when={local.leading}>
        <span style={{ display: 'inline-flex', 'align-items': 'center', 'flex-shrink': 0 }}>
          {local.leading}
        </span>
      </Show>
      <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
        {local.children}
      </span>
      <Show when={local.onClose}>
        <span
          data-wash-hit
          data-testid={local.closeTestId}
          title={local.closeTitle}
          role="button"
          aria-label="Close tab"
          onClick={(ev) => {
            // The tab underneath must not also activate on a close click.
            ev.stopPropagation();
            local.onClose?.();
          }}
          style={{
            width: '14px',
            height: '14px',
            'flex-shrink': 0,
            display: 'inline-flex',
            'align-items': 'center',
            'justify-content': 'center',
            'border-radius': `${tokens.radiusSm}`,
            font: tokens.type.monoSm,
            color: tokens.fgMuted,
          }}
        >
          {local.closeGlyph ?? '×'}
        </span>
      </Show>
    </button>
  );
};
