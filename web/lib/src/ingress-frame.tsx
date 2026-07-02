import { createMemo, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { washAssetUrl } from './assets';
import { installIngressClipboardBridge } from './ingress-clipboard';

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
  /** Extra styles merged onto the iframe (last). */
  style?: JSX.CSSProperties;
}

export const IngressFrame: Component<IngressFrameProps> = (props) => {
  // Strip the leading slash so washAssetUrl can prepend the deploy
  // base ("/" by default, "/wash/" under a subpath host).
  const src = createMemo(() => washAssetUrl(props.path.replace(/^\//, '')));

  let frame: HTMLIFrameElement | undefined;

  onMount(() => {
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
