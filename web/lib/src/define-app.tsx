// defineWashApp — collapses the customElements.define + connectedCallback
// + disconnectedCallback boilerplate every wash web app needs into a
// single call:
//
//   defineWashApp('wash-app-foo', (props) => <App {...props} />, {
//     style: 'display:block;...',
//   });
//
// The host element is `this`; the component is mounted into it.
// `data-wash-instance` is read once at connect and passed to the
// component as `props.instance`; `props.host` is the element itself
// (apps use it as the wash:msg / wash:state event target).
//
// Re-registering an element tag is a no-op (guarded by
// customElements.get).

import { render } from 'solid-js/web';
import type { Component } from 'solid-js';

/** Props every wash app component receives. */
export interface WashAppProps {
  /** Router-assigned instance id; "" before the shell finishes mounting. */
  instance: string;
  /** The host custom element. Apps listen for wash:msg / wash:state on it. */
  host: HTMLElement;
}

export interface DefineWashAppOptions {
  /**
   * Inline `style` to apply to the host element on connect. Apps that
   * want flex layouts, fixed dimensions, etc. set this.
   */
  style?: string;
}

/**
 * defineWashApp registers `tag` as a custom element that mounts
 * `App` on connect and disposes it on disconnect.
 *
 * If `tag` is already registered (HMR re-import, multiple bundle
 * loads), the call is a no-op.
 */
export function defineWashApp(
  tag: `wash-app-${string}`,
  App: Component<WashAppProps>,
  options: DefineWashAppOptions = {},
): void {
  // Remote-apps tag mangling (docs/REMOTE.md R2): when the shell imports
  // this bundle on behalf of a REMOTE router, it sets __washImportOrigin
  // so two routers serving the same app don't collide on one global tag.
  // We define under a per-origin tag and report the mapping so the shell's
  // mount sites instantiate the same tag. For a LOCAL import (or a
  // standalone/HMR load) the global is unset and behaviour is unchanged.
  const g = globalThis as unknown as {
    __washImportOrigin?: string;
    __washRegisterTag?: (origin: string, manifestTag: string, realTag: string) => void;
  };
  let realTag: `wash-app-${string}` = tag;
  const importOrigin = g.__washImportOrigin;
  if (importOrigin) {
    const slug = importOrigin.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'r';
    realTag = `${tag}-${slug}` as `wash-app-${string}`;
    g.__washRegisterTag?.(importOrigin, tag, realTag);
  }

  if (customElements.get(realTag)) return;

  class WashAppElement extends HTMLElement {
    private cleanup?: () => void;

    connectedCallback() {
      if (options.style) {
        this.style.cssText = options.style;
      }
      const instance = this.getAttribute('data-wash-instance') ?? '';
      this.cleanup = render(() => App({ instance, host: this }), this);
    }

    disconnectedCallback() {
      this.cleanup?.();
      this.cleanup = undefined;
    }
  }

  customElements.define(realTag, WashAppElement);
}
