import { Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js';
import { Portal } from 'solid-js/web';
import type { Component, JSX, ParentComponent } from 'solid-js';
import { tokens } from './tokens';

// Menu positions one of two ways:
//   - cursor-relative (x/y in props): drop-on-target menus,
//     right-click context menus, sort menus, menubar dropdowns.
//   - anchored: chrome menus that hang off a UI element
//     (e.g. start menu off the taskbar — anchor="bottom-left").
//
// Every menu portals into document.body and lays out with
// `position:fixed` so it can't be clipped by an `overflow:auto`
// ancestor (the obvious case is the window slot in the shell).
// Callers therefore pass *viewport* coords (clientX/clientY or
// getBoundingClientRect()-derived) — host-relative offsets would
// land in the wrong place.
//
// Menu installs its own document-pointerdown listener to dismiss
// on click-outside (one-tick deferred so the click that opened
// the menu doesn't immediately close it).
//
// pointerdown, in the CAPTURE phase, for two reasons that both showed up
// as the same bug — a menu left hanging in mid-air while the window slid
// out from under it:
//
//   - the shell's titlebar drag calls preventDefault() on pointerdown,
//     and that suppresses the compatibility mousedown entirely, so a
//     mousedown listener never hears the gesture that moves the window.
//     Resize handles do the same.
//   - window chrome stops pointerdown from propagating (so the titlebar's
//     drag doesn't start under its own buttons), which a bubbling-phase
//     listener would also miss. Capture runs before any of it.
//
// A menu is anchored to viewport coordinates, so anything that moves what
// it is anchored to has to close it; catching the gesture at its earliest
// point is what makes that reliable.
export interface MenuProps {
  x?: number;
  y?: number;
  anchor?: 'bottom-left';
  onDismiss: () => void;
  zIndex?: number;
  animation?: 'pop' | 'slide-up';
  'data-testid'?: string;
  children?: JSX.Element;
  style?: JSX.CSSProperties;
}

export const Menu: ParentComponent<MenuProps> = (props) => {
  let menuEl!: HTMLDivElement;
  onMount(() => {
    const onDocDown = (ev: Event) => {
      if (!menuEl || !menuEl.contains(ev.target as Node)) {
        props.onDismiss();
      }
    };
    setTimeout(() => document.addEventListener('pointerdown', onDocDown, true), 0);
    onCleanup(() => document.removeEventListener('pointerdown', onDocDown, true));
  });
  // Cursor-positioned menus clamp into the viewport: a right-click near
  // the bottom/right edge would otherwise push items off-screen where
  // they render but can't be clicked. The effect re-runs when x/y
  // change (the same mounted Menu can be re-opened at new coords under
  // a non-keyed <Show>), measuring the rendered size post-layout.
  const [clamped, setClamped] = createSignal<{ left: number; top: number } | null>(null);
  createEffect(() => {
    const x = props.x ?? 0;
    const y = props.y ?? 0;
    if (props.anchor || !menuEl) {
      setClamped(null);
      return;
    }
    const r = menuEl.getBoundingClientRect();
    const margin = 4;
    const left = Math.max(margin, Math.min(x, window.innerWidth - r.width - margin));
    const top = Math.max(margin, Math.min(y, window.innerHeight - r.height - margin));
    setClamped(left !== x || top !== y ? { left, top } : null);
  });
  return (
    <Portal mount={document.body}>
      <div
        ref={menuEl}
        data-testid={props['data-testid']}
        onContextMenu={(ev) => ev.preventDefault()}
        style={{
          position: 'fixed',
          background: tokens.bgMenu,
          border: `1px solid ${tokens.borderMenu}`,
          'border-radius': `${tokens.radiusMd}`,
          padding: '4px 0',
          'min-width': '160px',
          // A menu taller than the viewport clamps to the top edge above and
          // then runs off the bottom, where its last rows render but cannot be
          // clicked — edit's Syntax menu, whose last row is Word Wrap, on a
          // short window. Bound it to the viewport and let it scroll.
          'max-height': 'calc(100vh - 8px)',
          'overflow-y': 'auto',
          'box-shadow': tokens.shadowMenu,
          'z-index': props.zIndex ?? tokens.zMenu,
          ...positionFor(props),
          ...(clamped() ? { left: `${clamped()!.left}px`, top: `${clamped()!.top}px` } : {}),
          ...animFor(props),
          ...(props.style ?? {}),
        }}
      >
        {props.children}
      </div>
    </Portal>
  );
};

function positionFor(p: MenuProps): JSX.CSSProperties {
  if (p.anchor === 'bottom-left') {
    return { left: tokens.startMenuLeft, bottom: tokens.startMenuBottom };
  }
  return {
    left: `${p.x ?? 0}px`,
    top: `${p.y ?? 0}px`,
  };
}

function animFor(p: MenuProps): JSX.CSSProperties {
  if (p.animation === 'slide-up') {
    return {
      'transform-origin': 'bottom left',
      animation: tokens.animSlideUp,
    };
  }
  return {
    'transform-origin': 'top left',
    animation: tokens.animPopInFast,
  };
}

// MenuSeparator is the thin divider between groups of items.
export const MenuSeparator: Component = () => (
  <div style={{ height: '1px', background: tokens.borderMenu, margin: '4px 0' }} />
);

// MenuItem is the row inside a Menu. Most uses are a label +
// onClick; chrome menus add an icon; disabled is for unavailable
// entries (greyed out, no hover, cursor not-allowed).
//
// The highlight comes from the interaction layer (hit.ts) rather than the
// onMouseEnter/createSignal pair this used to carry — one stylesheet
// instead of a signal per rendered row, and it brings the press and
// keyboard-focus states the hand-rolled version never had. "strong" keeps
// the emphatic solid-bar highlight a menu wants; the default 10% wash
// reads too timid for a menu row.
export interface MenuItemProps {
  label: string;
  title?: string;
  icon?: JSX.Element;
  trailing?: JSX.Element;
  disabled?: boolean;
  /** opens a submenu (a start-menu Recent row): announced as a menu item
   *  with a popup rather than a button, which is what it is */
  popup?: { expanded: boolean };
  'data-testid'?: string;
  onClick: () => void;
}

export const MenuItem: Component<MenuItemProps> = (props) => {
  return (
    <button
      type="button"
      data-wash-hit="strong"
      data-testid={props['data-testid']}
      title={props.title}
      // Disabled for real, not just dimmed: without the attribute the item
      // stays keyboard-focusable and assistive tech announces it as
      // available. The onClick guard below stays as the belt to this
      // braces (a click can still be dispatched programmatically).
      disabled={props.disabled}
      aria-disabled={props.disabled ? 'true' : undefined}
      role={props.popup ? 'menuitem' : undefined}
      aria-haspopup={props.popup ? 'menu' : undefined}
      aria-expanded={props.popup ? (props.popup.expanded ? 'true' : 'false') : undefined}
      onClick={() => {
        if (!props.disabled) props.onClick();
      }}
      style={{
        display: 'flex',
        'align-items': 'center',
        gap: props.icon ? '8px' : undefined,
        width: '100%',
        'text-align': 'left',
        background: 'transparent',
        color: tokens.fg,
        border: 'none',
        'border-radius': `${tokens.radiusSm}`,
        padding: '4px 10px',
        cursor: props.disabled ? 'not-allowed' : 'pointer',
        opacity: props.disabled ? 0.5 : 1,
        font: tokens.type.textMd,
      }}
    >
      <Show when={props.icon}>
        <span
          style={{
            width: '18px',
            height: '18px',
            'flex-shrink': 0,
            display: 'inline-flex',
            'align-items': 'center',
            'justify-content': 'center',
          }}
        >
          {props.icon}
        </span>
      </Show>
      <span style={{ flex: 1 }}>{props.label}</span>
      <Show when={props.trailing}>{props.trailing}</Show>
    </button>
  );
};
