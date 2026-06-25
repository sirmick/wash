// Shell-side notification rendering: stacks toasts in the bottom-
// right of the viewport, above the chrome's taskbar (40 px). Each
// toast auto-dismisses after a few seconds; clicking dismisses
// early. v0.1 is fire-and-forget — no notification center, no
// persistence, no DND. Future direction is delegation to the
// session chrome (DE owns the UX), but for now the shell renders.

import { tokens } from '@wash/ui';

export interface ToastInput {
  instanceID: string;
  title: string;
  body?: string;
  level?: 'info' | 'warn' | 'error';
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
  card.addEventListener('click', dismiss);

  host.appendChild(card);
  // Trigger transition.
  requestAnimationFrame(() => {
    card.style.opacity = '1';
    card.style.transform = 'translateY(0)';
  });
  window.setTimeout(dismiss, TOAST_TTL_MS);
}
