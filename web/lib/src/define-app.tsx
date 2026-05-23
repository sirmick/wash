// defineWashApp — collapses the customElements.define + connectedCallback
// + disconnectedCallback boilerplate every wash web app repeated.
//
// Before:
//
//   class WashAppFoo extends HTMLElement {
//     private cleanup?: () => void;
//     connectedCallback() {
//       this.style.cssText = 'display:block;...';
//       const instance = this.getAttribute('data-wash-instance') ?? '';
//       this.cleanup = render(() => <App instance={instance} host={this} />, this);
//     }
//     disconnectedCallback() {
//       this.cleanup?.();
//       this.cleanup = undefined;
//     }
//   }
//   if (!customElements.get('wash-app-foo')) {
//     customElements.define('wash-app-foo', WashAppFoo);
//   }
//
// After:
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
// Re-registering an element tag is a no-op (matches the existing
// `if (!customElements.get(...))` guard every app had).

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
 * loads), the call is a no-op — matches every app's previous guard.
 */
export function defineWashApp(
  tag: `wash-app-${string}`,
  App: Component<WashAppProps>,
  options: DefineWashAppOptions = {},
): void {
  if (customElements.get(tag)) return;

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

  customElements.define(tag, WashAppElement);
}
