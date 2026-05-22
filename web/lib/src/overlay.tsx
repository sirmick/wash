import type { Component, JSX, ParentComponent } from 'solid-js';
import { tokens } from './tokens';

// Overlay is the centered-modal scaffold: animated full-bleed
// backdrop + animated centered box. Clicking the backdrop calls
// onDismiss (so it can route to whichever cancel/close the
// concrete dialog wants).
//
// Children control the inside of the box; Overlay handles
// positioning, backdrop, animations, and click-outside.
export interface OverlayProps {
  onDismiss: () => void;
  zIndex?: number;
  'data-testid'?: string;
  // Override the inner box style — used by Palette which uses a
  // top-anchored alignment + wider min-width.
  innerStyle?: JSX.CSSProperties;
  // Backdrop alignment: 'center' (default) or 'top' (used by the
  // command palette so the input lands at ~14vh).
  align?: 'center' | 'top';
  children?: JSX.Element;
}

export const Overlay: ParentComponent<OverlayProps> = (props) => {
  return (
    <div
      data-testid={props['data-testid']}
      onClick={(ev) => {
        if (ev.target === ev.currentTarget) props.onDismiss();
      }}
      style={{
        position: 'absolute',
        inset: 0,
        background: tokens.bgBackdrop,
        display: 'flex',
        'align-items': props.align === 'top' ? 'flex-start' : 'center',
        'justify-content': 'center',
        'padding-top': props.align === 'top' ? '14vh' : undefined,
        'z-index': props.zIndex ?? tokens.zModal,
        animation: tokens.animFadeIn,
      }}
    >
      <div
        style={{
          background: tokens.bgWindow,
          border: `1px solid ${tokens.borderWindow}`,
          'border-radius': `${tokens.radiusLg}px`,
          padding: `${tokens.spaceXl}px ${tokens.spaceXxl}px`,
          'min-width': '280px',
          'max-width': 'calc(100% - 32px)',
          // 16px breathing room top + bottom so the modal never
          // clips against a short host window. Overrides via
          // innerStyle still win, but the default already fits.
          'max-height': 'calc(100% - 32px)',
          'box-sizing': 'border-box',
          display: 'flex',
          'flex-direction': 'column',
          overflow: 'hidden',
          'box-shadow': tokens.shadowModal,
          font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
          color: tokens.fg,
          animation: tokens.animPopIn,
          ...(props.innerStyle ?? {}),
        }}
      >
        {props.children}
      </div>
    </div>
  );
};

// ConfirmDialog stacks Title + body + a Cancel/Confirm pair. The
// confirm button can be marked danger for destructive actions
// (Delete, Replace). Used by fm's two confirm overlays.
export interface ConfirmDialogProps {
  title: string;
  confirmLabel: string;
  cancelLabel?: string;
  danger?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
  'data-testid'?: string;
  confirmTestid?: string;
  cancelTestid?: string;
  children?: JSX.Element;
}

export const ConfirmDialog: Component<ConfirmDialogProps> = (props) => {
  return (
    <Overlay onDismiss={props.onCancel} data-testid={props['data-testid']}>
      <div style={{ 'font-weight': 600, 'margin-bottom': '6px' }}>{props.title}</div>
      {props.children}
      <div style={{ display: 'flex', gap: '8px', 'justify-content': 'flex-end', 'margin-top': '14px' }}>
        <button
          type="button"
          data-testid={props.cancelTestid}
          onClick={props.onCancel}
          style={confirmBtnStyle(false)}
        >
          {props.cancelLabel ?? 'Cancel'}
        </button>
        <button
          type="button"
          data-testid={props.confirmTestid}
          onClick={props.onConfirm}
          style={confirmBtnStyle(props.danger ?? false)}
        >
          {props.confirmLabel}
        </button>
      </div>
    </Overlay>
  );
};

// Larger button style than @wash/ui's <Button size="md"> — kept
// inline because the dialog uses 5px / 12px padding for the
// extra visual weight modal actions deserve. Could fold into
// Button as a size="lg" later if it shows up elsewhere.
function confirmBtnStyle(danger: boolean): JSX.CSSProperties {
  return {
    background: danger ? tokens.bgDanger : 'transparent',
    color: tokens.fg,
    border: `1px solid ${danger ? tokens.borderDanger : tokens.borderMenu}`,
    'border-radius': `${tokens.radiusSm}px`,
    padding: '5px 12px',
    cursor: 'pointer',
    font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  };
}
