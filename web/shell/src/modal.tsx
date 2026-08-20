// The session-modal layer (docs/SIDEBAR.md M4).
//
// A modal-surface app is a service that can paint: it autoboots and stays
// out of the launcher like a background app, but ships an FE that the
// SHELL draws — above every window, desktop blurred behind it. That
// framing is the security property. An ordinary window cannot blur the
// desktop or escape its own bounds, so a chrome-drawn modal is something
// an app is structurally unable to forge.
//
// Two rules make that hold, and both live here rather than in the app:
//
//   1. Never self-opening. The service raises a toast and a rail badge;
//      the modal appears only when the user summons it. So a "priv
//      prompt" that appears on its own is, by construction, a forgery.
//   2. One at a time. The blur claims the whole seat's attention, and two
//      modals would be a lie about that. A second summon replaces the
//      first, which stays pending and can be summoned again.
//
// A remote modal wears its host's accent and says the host's name, the
// same language window stripes and rail groups already speak — so
// "approve on build01" never looks like "approve here".

import { Show, createSignal, onCleanup } from 'solid-js';
import type { Component } from 'solid-js';
import { tokens } from '@wash/ui';
import { LOCAL_ORIGIN, compoundInstanceId, tagFor, type Origin } from './clients';
import { registerMountedElement, unregisterMountedElement } from './api';
import { hostColor } from './host-colors';

export interface ModalApp {
  origin: Origin;
  /** bare, router-assigned instance id */
  instanceID: string;
  element: string;
  appID: string;
}

// Registry of every declared modal app, keyed origin→appID. Populated on
// app.declared; these boot with the session, long before anyone needs one.
const registry = new Map<string, ModalApp>();
const key = (origin: Origin, appID: string) => `${origin}␟${appID}`;

const [summoned, setSummoned] = createSignal<ModalApp | null>(null);

/** registerModal records a declared modal app. Does not show it. */
export function registerModal(m: ModalApp): void {
  registry.set(key(m.origin, m.appID), m);
}

export function forgetModalsFor(origin: Origin): void {
  for (const k of [...registry.keys()]) {
    if (k.startsWith(`${origin}␟`)) registry.delete(k);
  }
  const cur = summoned();
  if (cur && cur.origin === origin) setSummoned(null);
}

/**
 * summonModal raises a modal the user asked for. Returns false when the
 * app isn't declared on that host — the caller should fall back rather
 * than blur the screen over nothing.
 */
export function summonModal(origin: Origin, appID: string): boolean {
  const m = registry.get(key(origin, appID));
  if (!m) return false;
  setSummoned(m);
  return true;
}

export function dismissModal(): void {
  setSummoned(null);
}

/** hasModal reports whether an app id is a modal on that origin. */
export function hasModal(origin: Origin, appID: string): boolean {
  return registry.has(key(origin, appID));
}

// ModalLayer renders the summoned modal, or nothing at all. Mounted once
// at the shell root, above the camera and the windows.
export const ModalLayer: Component = () => {
  // Escape dismisses: the modal is a question, and declining to answer it
  // now must always be possible — the request stays queued either way.
  const onKey = (ev: KeyboardEvent) => {
    if (ev.key === 'Escape' && summoned()) {
      ev.preventDefault();
      dismissModal();
    }
  };
  window.addEventListener('keydown', onKey);
  onCleanup(() => window.removeEventListener('keydown', onKey));

  // keyed: the callback gets the VALUE, not an accessor. The unkeyed form
  // hands back an accessor that goes stale the moment the modal dismisses,
  // and reading it during teardown throws — which is exactly what
  // dismiss-on-detach did. Keyed also gives the right identity semantics:
  // summoning a different host's modal rebuilds the frame rather than
  // mutating the one on screen.
  return (
    <Show when={summoned()} keyed>
      {(m) => {
        const isLocal = () => m.origin === LOCAL_ORIGIN;
        const hue = () => (isLocal() ? tokens.accentTeal : hostColor(m.origin) ?? tokens.accentTeal);
        return (
          <div
            data-testid="modal-layer"
            data-origin={m.origin}
            data-app={m.appID}
            style={{
              position: 'fixed',
              inset: '0',
              // The blur IS the affordance: everything else is out of
              // reach until this is answered or dismissed.
              background: 'rgba(0,0,0,0.45)',
              'backdrop-filter': 'blur(6px)',
              '-webkit-backdrop-filter': 'blur(6px)',
              display: 'flex',
              'align-items': 'center',
              'justify-content': 'center',
              // Above the taskbar (10000) and the toast stack (11000):
              // a toast that raised this must not cover the answer.
              'z-index': 12000,
            }}
            onClick={(ev) => {
              // Click-outside dismisses, same reasoning as Escape.
              if (ev.target === ev.currentTarget) dismissModal();
            }}
          >
            <div
              data-testid="modal-frame"
              style={{
                'min-width': '380px',
                'max-width': '560px',
                'max-height': '80vh',
                overflow: 'auto',
                background: tokens.bgMenu,
                border: `1px solid ${tokens.borderMenu}`,
                'border-top': `3px solid ${hue()}`,
                'border-radius': tokens.radiusLg,
                'box-shadow': '0 18px 48px rgba(0,0,0,0.55)',
              }}
            >
              {/* Chrome draws the host label, NOT the app. This is the
                  line an attacker cannot reproduce: the app id comes from
                  app.declared, which is the router's word. */}
              <div
                data-testid="modal-host"
                style={{
                  display: 'flex',
                  'align-items': 'center',
                  gap: '6px',
                  padding: '6px 12px',
                  'border-bottom': `1px solid ${tokens.borderMenu}`,
                  color: hue(),
                  font: tokens.type.textSm,
                  'font-weight': 600,
                }}
              >
                <span>{isLocal() ? 'this machine' : m.origin}</span>
                <span style={{ flex: 1 }} />
                <span style={{ color: tokens.fgDim, 'font-weight': 400 }}>{m.appID}</span>
              </div>
              <ModalHost app={m} />
            </div>
          </div>
        );
      }}
    </Show>
  );
};

// ModalHost instantiates the app's custom element, the same way a window
// does — per-origin mangled tag so a remote modal runs the bundle ITS
// host served, and an origin-tagged instance id so its messages route
// home.
const ModalHost: Component<{ app: ModalApp }> = (props) => {
  let slot!: HTMLDivElement;
  const cid = () => compoundInstanceId(props.app.origin, props.app.instanceID);
  const mount = (el: HTMLDivElement) => {
    slot = el;
    const node = document.createElement(tagFor(props.app.origin, props.app.element));
    node.setAttribute('data-wash-instance', cid());
    node.setAttribute('data-wash-origin', props.app.origin);
    slot.appendChild(node);
    // Same microtask defer as the window path: the element's onMount must
    // wire its wash:state / wash:msg listeners before we dispatch them.
    queueMicrotask(() => registerMountedElement(cid(), node));
  };
  onCleanup(() => unregisterMountedElement(cid()));
  return <div ref={mount} data-testid="modal-body" />;
};
