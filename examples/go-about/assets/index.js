// Minimal wash app FE — vanilla custom element, no framework, no bundler.
// Demonstrates the two halves of app_msg:
//   receive — the BE's messages arrive as `wash:msg` CustomEvents on the
//             host element (e.detail is the JSON payload).
//   transmit — window.wash.sendAppMsg(instance, payload) sends to the BE.
//
// The shell mounts this element in the app's window; `data-wash-instance`
// carries the router-assigned instance id used when sending back.

class GoAboutElement extends HTMLElement {
  connectedCallback() {
    const instance = this.getAttribute('data-wash-instance') ?? '';
    this.style.cssText =
      'display:block;padding:16px;height:100%;box-sizing:border-box;' +
      'font:14px system-ui,sans-serif;color:#cdd6e0;background:#0e0e16';
    this.innerHTML = `
      <h3 style="margin:0 0 4px">Go About — signal demo</h3>
      <p style="margin:0 0 12px;opacity:.6">BE⇄FE app_msg round-trip.</p>
      <button id="send" style="padding:6px 12px;border-radius:6px;border:1px solid #2a2a3a;
        background:#1a1a28;color:inherit;cursor:pointer">Send signal to BE</button>
      <ul id="log" style="list-style:none;margin:14px 0 0;padding:0;font-family:ui-monospace,monospace;
        font-size:12px;display:flex;flex-direction:column;gap:4px"></ul>`;

    const log = this.querySelector('#log');
    const add = (text) => {
      const li = document.createElement('li');
      li.textContent = text;
      log.prepend(li);
    };

    // receive: the BE's hello + echoes.
    this.addEventListener('wash:msg', (e) => add('⬇ ' + JSON.stringify(e.detail)));

    // transmit: a signal to our BE half.
    this.querySelector('#send').addEventListener('click', () => {
      const payload = { kind: 'ping', at: Date.now() };
      window.wash.sendAppMsg(instance, payload);
      add('⬆ ' + JSON.stringify(payload));
    });
  }
}

customElements.define('wash-app-go-about', GoAboutElement);
