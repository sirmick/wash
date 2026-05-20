// wash-app-about: a small windowed app that renders static
// version + license text. Self-contained library bundle — Solid
// drives the UI but pulls in its tiny reactive runtime; the bundle
// is still well under the per-app cap.

import { render } from 'solid-js/web';
import type { Component } from 'solid-js';

const App: Component = () => (
  <div
    style={{
      display: 'block',
      height: '100%',
      'box-sizing': 'border-box',
      padding: '16px 20px',
      color: '#eee',
      font: '14px/1.5 system-ui,sans-serif',
    }}
  >
    <h2 style={{ margin: '0 0 4px', font: '600 18px system-ui,sans-serif' }}>wash</h2>
    <p style={{ margin: '0 0 16px', opacity: 0.6 }}>Web Application SHell</p>
    <p style={{ margin: 0, opacity: 0.8, 'font-size': '13px' }}>v0.0 · AGPL-3.0</p>
  </div>
);

class WashAppAbout extends HTMLElement {
  private cleanup?: () => void;

  connectedCallback() {
    this.cleanup = render(() => <App />, this);
  }

  disconnectedCallback() {
    this.cleanup?.();
    this.cleanup = undefined;
  }
}

if (!customElements.get('wash-app-about')) {
  customElements.define('wash-app-about', WashAppAbout);
}
