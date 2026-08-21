// Shell-side notification rendering: stacks toasts in the bottom-
// right of the viewport, above the chrome's taskbar (40 px). Each
// toast auto-dismisses after a few seconds; clicking dismisses
// early. v0.1 is fire-and-forget — no notification center, no
// persistence, no DND. Future direction is delegation to the
// session chrome (DE owns the UX), but for now the shell renders.

import { tokens } from '@wash/ui';
import { hostColor } from './host-colors.ts';

export interface ToastInput {
  instanceID: string;
  title: string;
  body?: string;
  level?: 'info' | 'warn' | 'error';
  /**
   * Click-to-focus: called with the toast's instance id when the card is
   * clicked, before it dismisses. A toast is a pointer at the thing that
   * wants you ("Claude needs your input"), so clicking it should land on
   * that window rather than merely making the card go away. Omitted (or a
   * no-op for an instance with no window) leaves click = dismiss.
   *
   * `key` is the sender's own subject key when the notification carried
   * one (wire.EvtNotify.Key) — "this is about agent session acp:3" — so
   * the click can land on the thing rather than on the app in general.
   */
  onActivate?: (instanceID: string, key?: string) => void;
  /**
   * Origin the toast came from. A remote host's toast is indistinguishable
   * from a local one without this — and "the build box finished" reads very
   * differently from "this machine finished". LOCAL renders unchanged (no
   * stripe, no label), so the common case is untouched.
   */
  origin?: string;
  /**
   * Opaque subject key from the notification, handed back to onActivate.
   * The shell never parses it; see wire.EvtNotify.Key.
   */
  key?: string;
}

const TOAST_TTL_MS = 4500;
const TOASTBAR_BOTTOM = 48; // 40 px taskbar + 8 px gap

let container: HTMLDivElement | null = null;

function ensureContainer(): HTMLDivElement {
  if (container) return container;
  const div = document.createElement('div');
  div.dataset.testid = 'notification-stack';
  div.style.cssText = [
    'position:fixed',
    `bottom:${TOASTBAR_BOTTOM}px`,
    'right:16px',
    'display:flex',
    'flex-direction:column-reverse',
    'gap:8px',
    'z-index:11000',
    'max-width:340px',
    'pointer-events:none',
  ].join(';');
  document.body.appendChild(div);
  container = div;
  return div;
}

// Toast background per level — the semantic status tones, so toasts
// re-skin with the pack (and read correctly on light themes, where the
// tone goes light and tokens.fg goes dark) instead of staying dark.
function colorFor(level: ToastInput['level']): string {
  switch (level) {
    case 'error':
      return tokens.bgDanger;
    case 'warn':
      return tokens.bgWarning;
    default:
      return tokens.bgInfo;
  }
}

export function showToast(t: ToastInput): void {
  const host = ensureContainer();
  const card = document.createElement('div');
  card.dataset.testid = 'notification';
  card.dataset.level = t.level ?? 'info';
  card.dataset.instance = t.instanceID;
  // The subject key on the card, so a test can see which thing a toast
  // claims to be about — the difference between "a toast appeared" and
  // "a toast that can take you somewhere appeared".
  if (t.key) card.dataset.key = t.key;
  const hue = t.origin ? hostColor(t.origin) : null;
  if (t.origin) card.dataset.origin = t.origin;
  card.style.cssText = [
    'pointer-events:auto',
    `background:${colorFor(t.level)}`,
    `color:${tokens.fg}`,
    'border:1px solid rgba(255,255,255,0.08)',
    `border-radius:${tokens.radiusLg}`,
    'padding:10px 12px',
    `font:${tokens.type.textMd}`,
    'box-shadow:0 6px 18px rgba(0,0,0,0.5)',
    'cursor:pointer',
    'opacity:0',
    'transform:translateY(6px)',
    'transition:opacity 120ms, transform 120ms',
  ].join(';');
  // Host stripe down the left edge, same hue the window top-stripe and the
  // Hosts widget use, so one glance ties the toast to its machine. Set after
  // cssText, as a deliberate override of the `border` shorthand above.
  if (hue) card.style.borderLeft = `3px solid ${hue}`;

  if (hue) {
    const who = document.createElement('div');
    who.dataset.testid = 'notification-host';
    who.textContent = t.origin!;
    who.style.cssText = `color:${hue};font-size:11px;font-weight:600;margin-bottom:1px;`;
    card.appendChild(who);
  }

  const title = document.createElement('div');
  title.dataset.testid = 'notification-title';
  title.textContent = t.title;
  title.style.cssText = 'font-weight:600;margin-bottom:2px;';
  card.appendChild(title);

  if (t.body) {
    const body = document.createElement('div');
    body.dataset.testid = 'notification-body';
    body.textContent = t.body;
    body.style.cssText = 'opacity:0.85;';
    card.appendChild(body);
  }

  let dismissed = false;
  const dismiss = () => {
    if (dismissed) return;
    dismissed = true;
    card.style.opacity = '0';
    card.style.transform = 'translateY(6px)';
    setTimeout(() => card.remove(), 200);
  };
  card.addEventListener('click', () => {
    // Activate first: dismiss() starts a 200ms fade and removes the card,
    // and the focus intent must not depend on that finishing.
    if (t.onActivate) t.onActivate(t.instanceID, t.key);
    dismiss();
  });

  host.appendChild(card);
  // Trigger transition.
  requestAnimationFrame(() => {
    card.style.opacity = '1';
    card.style.transform = 'translateY(0)';
  });
  window.setTimeout(dismiss, TOAST_TTL_MS);
}
