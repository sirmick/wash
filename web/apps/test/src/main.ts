// wash-app-test: control panel + event log used for end-to-end tests
// and manual UI exercising. Every interactive piece carries a stable
// data-testid so Playwright (or any other driver) can address it
// deterministically.
//
// Wire shape between this FE and its BE half (both halves identical
// across instances; see cmd/wash-test):
//
//   FE → BE  : { kind: "ping" }                                     → BE replies with pong+seq
//                { kind: "set_title", title: <text> }               → BE calls c.SetTitle
//                { kind: "spawn", app_id: <text> }                  → BE calls c.SpawnRequest
//                { kind: "set_veto_close", on: <bool> }             → BE updates veto flag
//                { kind: "fe_event", type: <text>, ... }            → BE logs the event
//
//   BE → FE  : { kind: "pong", seq: <int> }
//                { kind: "event", type: "mapped|focus|unfocus|close_requested|spawn_ok|spawn_err", ... }
//
// Custom element: <wash-app-test>. Receives BE messages as the
// CustomEvent "wash:msg" on itself; receives DOM input events as usual.

declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
      log(level: 'error' | 'warn' | 'info' | 'debug', source: string, msg: string, stack?: string): void;
      // (other members exist but the test app doesn't use them)
    };
  }
}

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

const MAX_LOG_ROWS = 50;

class WashAppTest extends HTMLElement {
  private instance = '';
  private state = {
    keys: 0,
    clicks: 0,
    dblclicks: 0,
    ctxmenus: 0,
    pings: 0,
    events: 0,
    title: '(no title)',
    focused: false,
    vetoNextClose: false,
  };
  private titleSeq = 0;

  connectedCallback() {
    this.instance = this.getAttribute('data-wash-instance') ?? '';
    this.render();
    this.bind();
  }

  // -------- rendering --------

  private render() {
    this.style.cssText = [
      'display:block',
      'height:100%',
      'box-sizing:border-box',
      'padding:14px 18px',
      'background:#10101a',
      'color:#eee',
      'font:13px ui-monospace,Menlo,Consolas,monospace',
      'overflow:auto',
      'outline:none',
    ].join(';');
    this.tabIndex = 0; // accept keyboard focus

    this.innerHTML = `
      <header style="display:flex;gap:18px;align-items:baseline;margin-bottom:8px;">
        <span style="font-weight:600;">wash-app-test</span>
        <span data-testid="instance" style="opacity:0.6;">${this.instance}</span>
        <span data-testid="focused" style="opacity:0.8;">focused: <b>no</b></span>
        <span data-testid="title" style="opacity:0.8;">title: <b>(none)</b></span>
      </header>

      <section style="display:grid;grid-template-columns:repeat(3,1fr);gap:6px 14px;margin:8px 0 14px;">
        ${counterRow('keys', 'counter-keys')}
        ${counterRow('clicks', 'counter-clicks')}
        ${counterRow('dblclicks', 'counter-dblclicks')}
        ${counterRow('ctxmenus', 'counter-ctxmenus')}
        ${counterRow('pings (FE↔BE)', 'counter-pings')}
        ${counterRow('BE events', 'counter-events')}
      </section>

      <section style="margin:10px 0;">
        <span style="opacity:0.7;">veto next close:</span>
        <b data-testid="veto-next-close">off</b>
      </section>

      <section style="display:flex;flex-wrap:wrap;gap:6px;margin:10px 0;">
        ${actionBtn('set-title',      'Set title')}
        ${actionBtn('spawn-self',     'Spawn another instance')}
        ${actionBtn('spawn-bad',      'Spawn nonexistent (err)')}
        ${actionBtn('toggle-veto',    'Toggle veto close')}
        ${actionBtn('ping',           'Ping BE (round-trip)')}
        ${actionBtn('throw',          'Throw uncaught')}
        ${actionBtn('console-error',  'console.error')}
        ${actionBtn('reject-promise', 'Reject unhandled')}
      </section>

      <section>
        <div style="opacity:0.7;margin-bottom:4px;">events (newest first):</div>
        <div data-testid="event-log" style="
          height:160px;
          overflow:auto;
          background:#181828;
          border:1px solid #2a2a3a;
          padding:6px 8px;
          font-size:12px;
          line-height:1.55;
        "></div>
      </section>
    `;
  }

  private bind() {
    // DOM input events tracked on the FE side. Each is also forwarded
    // to the BE so we can verify on the back end that the FE-side
    // event was actually delivered (or to drive cross-process asserts).
    this.addEventListener('keydown', (ev: KeyboardEvent) => {
      this.state.keys += 1;
      this.set('counter-keys', String(this.state.keys));
      this.send({ kind: 'fe_event', type: 'keydown', key: ev.key });
    });
    this.addEventListener('click', () => {
      this.state.clicks += 1;
      this.set('counter-clicks', String(this.state.clicks));
      this.send({ kind: 'fe_event', type: 'click' });
    });
    this.addEventListener('dblclick', () => {
      this.state.dblclicks += 1;
      this.set('counter-dblclicks', String(this.state.dblclicks));
      this.send({ kind: 'fe_event', type: 'dblclick' });
    });
    this.addEventListener('contextmenu', (ev: MouseEvent) => {
      ev.preventDefault();
      this.state.ctxmenus += 1;
      this.set('counter-ctxmenus', String(this.state.ctxmenus));
      this.send({ kind: 'fe_event', type: 'contextmenu' });
    });

    // Per-button actions.
    this.querySelector('[data-testid="action-set-title"]')?.addEventListener('click', () => {
      this.titleSeq += 1;
      this.send({ kind: 'set_title', title: `test-${this.titleSeq}` });
    });
    this.querySelector('[data-testid="action-spawn-self"]')?.addEventListener('click', () => {
      this.send({ kind: 'spawn', app_id: 'com.wash.test' });
    });
    this.querySelector('[data-testid="action-spawn-bad"]')?.addEventListener('click', () => {
      this.send({ kind: 'spawn', app_id: 'com.does.not.exist' });
    });
    this.querySelector('[data-testid="action-toggle-veto"]')?.addEventListener('click', () => {
      this.state.vetoNextClose = !this.state.vetoNextClose;
      this.set('veto-next-close', this.state.vetoNextClose ? 'on' : 'off');
      this.send({ kind: 'set_veto_close', on: this.state.vetoNextClose });
    });
    this.querySelector('[data-testid="action-ping"]')?.addEventListener('click', () => {
      this.send({ kind: 'ping' });
    });
    this.querySelector('[data-testid="action-throw"]')?.addEventListener('click', () => {
      // Defer so this handler doesn't swallow the throw.
      setTimeout(() => {
        throw new Error('test app: deliberate uncaught error');
      }, 0);
    });
    this.querySelector('[data-testid="action-console-error"]')?.addEventListener('click', () => {
      // eslint-disable-next-line no-console
      console.error('test app: deliberate console.error', { detail: 1 });
    });
    this.querySelector('[data-testid="action-reject-promise"]')?.addEventListener('click', () => {
      void Promise.reject(new Error('test app: deliberate unhandled rejection'));
    });

    // Messages from the BE half arrive as CustomEvents on this
    // element (dispatched by the shell on app_msg.deliver).
    this.addEventListener('wash:msg', (ev) => {
      const m = (ev as CustomEvent).detail as BEMessage;
      this.handleBE(m);
    });
  }

  // -------- BE → FE handler --------

  private handleBE(m: BEMessage) {
    if (!m || typeof m !== 'object') return;
    switch (m.kind) {
      case 'pong': {
        this.state.pings += 1;
        this.set('counter-pings', String(this.state.pings));
        this.logEvent('pong', `seq=${m.seq}`);
        return;
      }
      case 'event': {
        this.state.events += 1;
        this.set('counter-events', String(this.state.events));
        const type = String(m.type ?? '');
        switch (type) {
          case 'mapped':
            this.logEvent('mapped');
            break;
          case 'focus':
            this.state.focused = true;
            this.set('focused', 'yes');
            this.logEvent('focus');
            break;
          case 'unfocus':
            this.state.focused = false;
            this.set('focused', 'no');
            this.logEvent('unfocus');
            break;
          case 'close_requested':
            this.logEvent('close_requested', `allow=${m.allow ?? 'true'}`);
            break;
          case 'title_set':
            this.state.title = String(m.title ?? '');
            this.set('title', this.state.title || '(none)');
            this.logEvent('title_set', this.state.title);
            break;
          case 'spawn_ok':
            this.logEvent('spawn_ok', `${m.app_id} → ${m.instance_id}`);
            break;
          case 'spawn_err':
            this.logEvent('spawn_err', `${m.app_id} ${m.code}: ${m.msg}`);
            break;
          case 'veto_changed':
            this.state.vetoNextClose = !!m.on;
            this.set('veto-next-close', m.on ? 'on' : 'off');
            this.logEvent('veto_changed', m.on ? 'on' : 'off');
            break;
          default:
            this.logEvent(type);
        }
        return;
      }
    }
  }

  // -------- helpers --------

  private send(data: unknown) {
    window.wash.sendAppMsg(this.instance, data);
  }

  private set(testid: string, value: string) {
    const el = this.querySelector(`[data-testid="${CSS.escape(testid)}"]`);
    if (!el) return;
    // Two patterns: `counter-*` rows render the value inside <b>;
    // simple rows render the value directly.
    const b = el.querySelector('b');
    if (b) {
      b.textContent = value;
    } else {
      el.textContent = value;
    }
  }

  private logEvent(type: string, detail = '') {
    const log = this.querySelector('[data-testid="event-log"]') as HTMLDivElement | null;
    if (!log) return;
    const row = document.createElement('div');
    row.setAttribute('data-testid', `event-row-${type}`);
    row.textContent = `[${ts()}] ${type}${detail ? '  ' + detail : ''}`;
    log.insertBefore(row, log.firstChild);
    while (log.children.length > MAX_LOG_ROWS) {
      log.removeChild(log.lastChild!);
    }
  }
}

function counterRow(label: string, testid: string): string {
  return `<div>${label}: <b data-testid="${testid}">0</b></div>`;
}

function actionBtn(name: string, label: string): string {
  return `<button type="button" data-testid="action-${name}" style="
    background:#2a2a4a;
    color:#eee;
    border:1px solid #3a3a5a;
    padding:5px 10px;
    border-radius:3px;
    cursor:pointer;
    font:12px ui-monospace,Menlo,Consolas,monospace;
  ">${label}</button>`;
}

function ts(): string {
  const d = new Date();
  return (
    String(d.getHours()).padStart(2, '0') +
    ':' +
    String(d.getMinutes()).padStart(2, '0') +
    ':' +
    String(d.getSeconds()).padStart(2, '0') +
    '.' +
    String(d.getMilliseconds()).padStart(3, '0')
  );
}

if (!customElements.get('wash-app-test')) {
  customElements.define('wash-app-test', WashAppTest);
}
