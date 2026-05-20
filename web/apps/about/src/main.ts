// wash-app-about: a small windowed app that renders static
// version + license text. Self-contained library bundle — no
// external module imports.

class WashAppAbout extends HTMLElement {
  connectedCallback() {
    this.style.cssText = [
      'display:block',
      'height:100%',
      'box-sizing:border-box',
      'padding:16px 20px',
      'color:#eee',
      'font:14px/1.5 system-ui,sans-serif',
    ].join(';');

    const title = document.createElement('h2');
    title.textContent = 'wash';
    title.style.cssText = 'margin:0 0 4px;font:600 18px system-ui,sans-serif;';
    this.appendChild(title);

    const sub = document.createElement('p');
    sub.textContent = 'Web Application SHell';
    sub.style.cssText = 'margin:0 0 16px;opacity:0.6;';
    this.appendChild(sub);

    const meta = document.createElement('p');
    meta.textContent = 'v0.0 · AGPL-3.0';
    meta.style.cssText = 'margin:0;opacity:0.8;font-size:13px;';
    this.appendChild(meta);
  }
}

if (!customElements.get('wash-app-about')) {
  customElements.define('wash-app-about', WashAppAbout);
}
