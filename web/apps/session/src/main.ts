// wash-app-session: the session app's web component. Renders the
// desktop background and a single launcher entry, "About". Clicking
// sends an APP_MSG to the BE half, which replies with spawn.request.
//
// This bundle must be self-contained — no external module imports.
// It addresses the shell-provided window.wash API to send APP_MSGs.

declare global {
  interface Window {
    wash: { sendAppMsg(instanceID: string, data: unknown): void };
  }
}

class WashAppSession extends HTMLElement {
  connectedCallback() {
    const instance = this.getAttribute('data-wash-instance') ?? '';

    this.style.cssText = [
      'display:block',
      'position:absolute',
      'inset:0',
      'background:radial-gradient(circle at 30% 20%, #1a1a32 0, #0a0a18 75%)',
      'color:#eee',
      'font:14px system-ui,sans-serif',
      'overflow:hidden',
    ].join(';');

    const banner = document.createElement('div');
    banner.textContent = 'wash — Web Application SHell';
    banner.style.cssText = [
      'position:absolute',
      'left:32px',
      'top:32px',
      'font:600 18px system-ui,sans-serif',
      'letter-spacing:0.04em',
      'opacity:0.6',
    ].join(';');
    this.appendChild(banner);

    const launcher = document.createElement('button');
    launcher.type = 'button';
    launcher.textContent = 'About';
    launcher.style.cssText = [
      'position:absolute',
      'left:24px',
      'bottom:24px',
      'padding:12px 22px',
      'background:#33387a',
      'color:#eee',
      'border:1px solid #4a4f8d',
      'border-radius:6px',
      'cursor:pointer',
      'font:14px system-ui,sans-serif',
    ].join(';');
    launcher.addEventListener('click', () => {
      window.wash.sendAppMsg(instance, { action: 'launch', app_id: 'com.wash.about' });
    });
    this.appendChild(launcher);
  }
}

if (!customElements.get('wash-app-session')) {
  customElements.define('wash-app-session', WashAppSession);
}
