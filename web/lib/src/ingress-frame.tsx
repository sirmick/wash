import { createMemo, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { washAssetUrl } from './assets';
import { installIngressClipboardBridge } from './ingress-clipboard';
import { installIngressActivityBridge, installIngressFocusBridge } from './ingress-focus';
import { noteUserActivity } from './screensaver';
import { createIngressHeal, type IngressHeal } from './ingress-heal';

// IngressFrame embeds a web app published through the router's
// generic ingress (/app/<token>/*) in a full-bleed iframe. It is the
// FE counterpart to the BE's sdk.PublishIngress: the BE hands its FE
// an ingress path, the FE drops it here, and the workbench (or any
// HTTP/WS web app — code-server is consumer #1) renders inside.
//
// The path is served under the SAME origin as the shell — it's just a
// different route on the router — so the iframe is same-origin: no
// mixed-content block, cookies/storage work, and the embedded app's
// relative asset URLs resolve under its base path. washAssetUrl keeps
// it correct when the whole shell is hosted under a subpath.
//
// Bridge: any message the embedded app posts as
// `{ __wash: true, ... }` is re-dispatched on the host element as a
// `wash:ingress` CustomEvent, so an app shell can react (title sync,
// theme, focus) without IngressFrame knowing app specifics. Apps that
// don't post anything (plain code-server) simply get the iframe.
//
// Self-heal: with onDead set, the frame probes its ingress base on every
// load and whenever the shell link comes back (ingress-heal.ts). A dead
// token (401/410) calls onDead so the app re-ensures and hands back a
// fresh path; if the same path instead recovers in place, the frame
// reloads itself off the error page.

export interface IngressFrameProps {
  /** Ingress base path from PublishIngress, e.g. "/app/<token>/". */
  path: string;
  /** Accessible title for the iframe element. */
  title?: string;
  /**
   * Host element to re-dispatch bridged `{__wash:true}` messages on,
   * as `wash:ingress` CustomEvents. Optional — omit for a plain embed.
   */
  host?: HTMLElement;
  /** Fires when the iframe finishes loading the embedded app. */
  onLoad?: () => void;
  /**
   * The ingress token behind `path` died (401/410). The app should ask
   * its BE for a fresh path (its ensure/launch message) and pass it back
   * in as `path`. Omit to opt out of self-heal.
   */
  onDead?: () => void;
  /** Extra styles merged onto the iframe (last). */
  style?: JSX.CSSProperties;
}

export const IngressFrame: Component<IngressFrameProps> = (props) => {
  // Strip the leading slash so washAssetUrl can prepend the deploy
  // base ("/" by default, "/wash/" under a subpath host).
  const src = createMemo(() => washAssetUrl(props.path.replace(/^\//, '')));

  let frame: HTMLIFrameElement | undefined;

  // Detach the focus bridge from the previous document before the iframe
  // navigates (each load is a fresh document with its own listeners).
  let detachFocus: (() => void) | undefined;
  let detachActivity: (() => void) | undefined;

  // raiseWindow makes an interaction inside the (event-swallowing) iframe
  // behave as if the user clicked the window frame: synthesize a bubbling
  // pointerdown on the iframe element so the shell's per-window
  // onWindowPointerDown handler runs and raises the window (#12). Keeping
  // it a plain DOM event means IngressFrame stays agnostic of the WM — it
  // reuses the exact focus-raise path every other wash surface already
  // uses, rather than reaching for window.wash here.
  const raiseWindow = () => {
    // composed:true so it crosses any shadow boundary the shell wraps the
    // frame in, matching a real user pointer event (which is composed).
    frame?.dispatchEvent(new Event('pointerdown', { bubbles: true, composed: true }));
  };

  let heal: IngressHeal | undefined;

  onMount(() => {
    onCleanup(() => {
      detachFocus?.();
      detachActivity?.();
    });
    if (props.onDead) {
      heal = createIngressHeal({
        base: () => props.path,
        reensure: () => props.onDead?.(),
        onRecovered: (sameBase) => {
          // A fresh base already re-navigated the frame via src; the same
          // base means the frame may still be showing the failure page.
          if (sameBase && frame) frame.src = src();
        },
      });
      onCleanup(() => heal?.dispose());
    }
    if (!props.host) return;
    const onMessage = (ev: MessageEvent) => {
      // Only relay messages from our own iframe, and only those that
      // opt into the wash bridge envelope.
      if (frame && ev.source !== frame.contentWindow) return;
      const d = ev.data as { __wash?: boolean } | null;
      if (!d || d.__wash !== true) return;
      props.host!.dispatchEvent(new CustomEvent('wash:ingress', { detail: d }));
    };
    window.addEventListener('message', onMessage);
    onCleanup(() => window.removeEventListener('message', onMessage));
  });

  return (
    <iframe
      ref={frame}
      data-testid="ingress-frame"
      src={src()}
      title={props.title ?? 'Embedded app'}
      // Same-origin embed of our own trusted backend — no sandbox.
      // Allow the capabilities a full IDE/web app expects.
      allow="clipboard-read; clipboard-write; fullscreen; cross-origin-isolated"
      onLoad={() => {
        // On the insecure LAN origin wash ships on, the embedded app's
        // navigator.clipboard is missing, so its copy/paste breaks (#9).
        // Same-origin lets us bridge it to the wash clipboard in place.
        installIngressClipboardBridge(frame?.contentWindow);
        // Forward interactions inside the iframe up to the WM so clicking
        // the embedded surface raises the window (#12). Re-arm on every
        // load: each navigation is a new document.
        detachFocus?.();
        detachFocus = installIngressFocusBridge(frame?.contentWindow, raiseWindow);
        // And report typing/mouse inside it as activity, or the screensaver
        // starts while the user is working in the embed.
        detachActivity?.();
        detachActivity = installIngressActivityBridge(frame?.contentWindow, noteUserActivity);
        // The load event fires for an error page too — the iframe can't
        // see its status, so ask the route directly.
        heal?.check();
        props.onLoad?.();
      }}
      style={{
        display: 'block',
        width: '100%',
        height: '100%',
        border: 'none',
        margin: 0,
        background: '#1e1e1e',
        ...(props.style ?? {}),
      }}
    />
  );
};
