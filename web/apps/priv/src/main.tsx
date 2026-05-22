// wash-app-priv: the privilege queue UI.
//
// Shape: one row per pending request, bulk-ops style. Each row shows
// the requesting app+instance (router-attested — printed verbatim,
// can't be spoofed), the kind (run | spawn), the argv preview, and
// Approve / Reject buttons until the user decides. After approval,
// status flips through running → done and the row sticks briefly so
// the user sees the exit code.
//
// Password handshake: when an Approve triggers a BE need_password
// event, the FE shows a modal, encrypts the user's input with an
// ephemeral ECDH-P256 session key against the BE-supplied pubkey,
// and ships the ciphertext as {kind:"unlock"}. The plaintext password
// never leaves this component; we wipe the input field before send
// and don't pull it into any reactive state.
//
// Lock conditions: explicit Lock button, 15-min idle (tracked BE-side
// via WASH_PRIV_IDLE), and browser refresh — the latter handled via
// a per-mount page nonce that the BE compares against the last value
// it saw. Any change wipes the BE cache.

import { For, Show, createSignal, onCleanup, onMount } from 'solid-js';
import { createStore } from 'solid-js/store';
import { render } from 'solid-js/web';
import type { Component } from 'solid-js';
import { Button } from '@wash/ui';

declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
    };
  }
}

type Status = 'queued' | 'running' | 'done' | 'rejected' | 'error';
type Kind = 'run' | 'spawn';

interface ReqView {
  req_id: string;
  kind: Kind;
  sender_app_id: string;
  sender_inst_id: string;
  app_id: string;
  argv: string[];
  cwd?: string;
  reason: string;
  status: Status;
  created_ms: number;
  started_ms: number;
  finished_ms: number;
  exit_code: number;
  error: string;
  spawned_inst_id: string;
  // Router-attested metadata when this request came from the
  // wash-sudo CLI (sender_app_id == "cli.wash.sudo"). Fields are
  // unforgeable — read from SO_PEERCRED + /proc by the router.
  cli_origin?: {
    pid: number;
    uid: number;
    comm: string;
    tty: string;
  };
}

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

// AUTO_CLEAR_MS — terminal rows fade after this so the queue
// settles. Matches bulk-ops for consistency.
const AUTO_CLEAR_MS = 6_000;

// makePageNonce: one nonce per mount, kept in module scope so we
// can read it from every send. crypto.randomUUID is universal in
// modern browsers (Safari ≥ 15.4).
const PAGE_NONCE = (() => {
  try {
    return (crypto as Crypto & { randomUUID?: () => string }).randomUUID?.()
      ?? Math.random().toString(36).slice(2);
  } catch {
    return Math.random().toString(36).slice(2);
  }
})();

// HKDF_INFO MUST match the BE's PrivAEADInfo constant. If you bump
// this on one side, bump it on the other or the password handshake
// silently fails with "decrypt_failed".
const HKDF_INFO = 'wash-priv/password/v1';

// b64encode / b64decode are tiny base64 helpers used to pack
// binary fields (ciphertext / pubkey / nonce) for transport. We
// send via JSON-equivalent CBOR; CBOR can carry bytes natively but
// the FE's send path is JSON, so base64 strings are the safe bet.
function b64encode(bytes: Uint8Array): string {
  let s = '';
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
  return btoa(s);
}

function b64decode(s: string): Uint8Array {
  const raw = atob(s);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

// encryptPassword runs the FE side of the password handshake:
//   1. Import the BE's uncompressed P-256 public key.
//   2. Generate an ephemeral FE P-256 keypair.
//   3. Derive the shared secret via ECDH.
//   4. HKDF-SHA256 to a 32-byte AES-256-GCM key with the shared info.
//   5. Encrypt the password bytes with a random 12-byte nonce.
//
// Returns the wire payload pieces; caller decides how to ship them.
// The plaintext bytes are zeroed before this function returns. Any
// exception bubbles up so the caller can surface "encrypt failed"
// without partially leaking secrets.
async function encryptPassword(password: string, bePubRaw: Uint8Array): Promise<{
  ciphertext: Uint8Array;
  fePubKey: Uint8Array;
  nonce: Uint8Array;
}> {
  const subtle = crypto.subtle;
  const bePub = await subtle.importKey(
    'raw',
    bePubRaw,
    { name: 'ECDH', namedCurve: 'P-256' },
    false,
    [],
  );
  const fe = await subtle.generateKey(
    { name: 'ECDH', namedCurve: 'P-256' },
    true,
    ['deriveBits'],
  );
  const shared = new Uint8Array(
    await subtle.deriveBits({ name: 'ECDH', public: bePub }, fe.privateKey, 256),
  );
  // HKDF-SHA256(salt=empty, info=HKDF_INFO, length=32). importKey for
  // HKDF needs raw bytes of the shared secret; HKDF doesn't accept
  // "extractable", and the derived key goes straight into AES-GCM.
  const hkdfKey = await subtle.importKey('raw', shared, { name: 'HKDF' }, false, ['deriveKey']);
  const aesKey = await subtle.deriveKey(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: new Uint8Array(0),
      info: new TextEncoder().encode(HKDF_INFO),
    },
    hkdfKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt'],
  );
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const pwBytes = new TextEncoder().encode(password);
  const ct = new Uint8Array(
    await subtle.encrypt({ name: 'AES-GCM', iv: nonce }, aesKey, pwBytes),
  );
  // Best-effort scrub. The TextEncoder copy in pwBytes is the only
  // owned buffer; the password string is GC'd later but that's out
  // of our hands.
  pwBytes.fill(0);
  shared.fill(0);
  const fePubRaw = new Uint8Array(await subtle.exportKey('raw', fe.publicKey));
  return { ciphertext: ct, fePubKey: fePubRaw, nonce };
}

// argvPreview prints argv as a single shell-like line for the queue
// row. We deliberately don't try to be a real shell-quoter — the
// preview is informational; the BE invocation uses argv directly,
// not the preview string.
function argvPreview(argv: string[]): string {
  return argv
    .map((a) => (/[\s"'\\]/.test(a) ? JSON.stringify(a) : a))
    .join(' ');
}

interface State {
  locked: boolean;
  idle_remaining_ms: number;
  be_pubkey?: Uint8Array;
  pending_req_id?: string; // the req that triggered the password prompt
}

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [requests, setRequests] = createStore<Record<string, ReqView>>({});
  const [order, setOrder] = createSignal<string[]>([]);
  const [state, setState] = createStore<State>({ locked: true, idle_remaining_ms: 0 });
  const [passwordError, setPasswordError] = createSignal<string>('');
  const [submitting, setSubmitting] = createSignal(false);

  const send = (msg: Record<string, unknown>) =>
    window.wash.sendAppMsg(props.instance, { ...msg, page_nonce: PAGE_NONCE });

  const upsert = (r: ReqView) => {
    const known = !!requests[r.req_id];
    setRequests(r.req_id, r);
    if (!known) setOrder([...order(), r.req_id]);
    if (r.status === 'done' || r.status === 'rejected' || r.status === 'error') {
      setTimeout(() => {
        setOrder(order().filter((id) => id !== r.req_id));
        setRequests(r.req_id, undefined as unknown as ReqView);
      }, AUTO_CLEAR_MS);
    }
  };

  const handleBE = (m: BEMessage) => {
    switch (m.kind) {
      case 'state': {
        const queue = (m.queue as ReqView[]) ?? [];
        setRequests({});
        for (const r of queue) setRequests(r.req_id, r);
        setOrder(queue.map((r) => r.req_id));
        setState('locked', !!m.locked);
        setState('idle_remaining_ms', Number(m.idle_remaining_ms ?? 0));
        return;
      }
      case 'req.new': {
        upsert(m.req as ReqView);
        return;
      }
      case 'req.update': {
        upsert(m.req as ReqView);
        return;
      }
      case 'need_password': {
        // The BE handed us its ephemeral pubkey alongside the req_id
        // that triggered the prompt. Coerce: CBOR brings binary
        // through as either a string (base64) or Uint8Array depending
        // on path; we accept both.
        const raw = m.be_pubkey;
        let bePub: Uint8Array | undefined;
        if (raw instanceof Uint8Array) bePub = raw;
        else if (typeof raw === 'string') bePub = b64decode(raw);
        // The router base64-encodes byte strings in CBOR→JSON
        // normalization, which is why we accept string here.
        setState('be_pubkey', bePub);
        setState('pending_req_id', String(m.req_id ?? ''));
        setPasswordError('');
        return;
      }
      case 'unlocked': {
        setState('locked', false);
        setState('be_pubkey', undefined);
        setState('pending_req_id', undefined);
        setPasswordError('');
        return;
      }
      case 'unlock_err': {
        setPasswordError(String(m.msg ?? 'unlock failed'));
        setState('be_pubkey', undefined);
        return;
      }
      case 'locked': {
        setState('locked', true);
        setState('be_pubkey', undefined);
        setState('pending_req_id', undefined);
        return;
      }
    }
  };

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail as BEMessage);
    props.host.addEventListener('wash:msg', onMsg);
    onCleanup(() => props.host.removeEventListener('wash:msg', onMsg));
    // Send hello with the page nonce so the BE picks up a fresh-
    // mount state (and locks the cache if the nonce changed).
    send({ kind: 'hello' });
  });

  const approve = (id: string) => send({ kind: 'approve', req_id: id });
  const reject = (id: string) => send({ kind: 'reject', req_id: id, reason: '' });
  const lock = () => send({ kind: 'lock' });

  const submitPassword = async (password: string) => {
    const bePub = state.be_pubkey;
    if (!bePub) return;
    setSubmitting(true);
    setPasswordError('');
    try {
      const { ciphertext, fePubKey, nonce } = await encryptPassword(password, bePub);
      send({
        kind: 'unlock',
        ciphertext: b64encode(ciphertext),
        fe_pubkey: b64encode(fePubKey),
        nonce: b64encode(nonce),
      });
    } catch (e) {
      setPasswordError(String(e));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      data-testid="priv-root"
      style={{
        height: '100%',
        'box-sizing': 'border-box',
        padding: '8px 12px',
        color: '#eee',
        font: '13px ui-sans-serif,system-ui,sans-serif',
        background: '#10101a',
        overflow: 'auto',
        position: 'relative',
      }}
    >
      <div
        style={{
          display: 'flex',
          'align-items': 'center',
          'justify-content': 'space-between',
          'margin-bottom': '8px',
        }}
      >
        <div style={{ opacity: 0.6, font: '11px ui-monospace,Menlo,Consolas,monospace' }}>
          <span data-testid="priv-count">{order().length}</span> request(s)
          <Show when={!state.locked}>
            <span style={{ 'margin-left': '12px', color: '#9ac' }} data-testid="priv-unlocked">unlocked</span>
          </Show>
          <Show when={state.locked}>
            <span style={{ 'margin-left': '12px', opacity: 0.5 }} data-testid="priv-locked">locked</span>
          </Show>
        </div>
        <Show when={!state.locked}>
          <Button data-testid="priv-lock" variant="ghost" size="sm" onClick={lock}>Lock</Button>
        </Show>
      </div>
      <Show when={order().length === 0}>
        <div style={{ opacity: 0.4, padding: '12px 0', 'font-style': 'italic' }}>queue is empty</div>
      </Show>
      <For each={order()}>
        {(id) => (
          <ReqRow
            req={requests[id]}
            onApprove={() => approve(id)}
            onReject={() => reject(id)}
          />
        )}
      </For>
      <Show when={state.be_pubkey != null}>
        <PasswordModal
          submitting={submitting()}
          error={passwordError()}
          onSubmit={submitPassword}
          onCancel={() => {
            setState('be_pubkey', undefined);
            setPasswordError('');
          }}
        />
      </Show>
    </div>
  );
};

const ReqRow: Component<{
  req: ReqView;
  onApprove: () => void;
  onReject: () => void;
}> = (props) => {
  const isQueued = () => props.req.status === 'queued';
  const isTerminal = () =>
    props.req.status === 'done' ||
    props.req.status === 'rejected' ||
    props.req.status === 'error';
  return (
    <div
      data-testid={`priv-req-${props.req.req_id}`}
      data-status={props.req.status}
      style={{
        'border-top': '1px solid #2a2a3a',
        padding: '8px 0',
        display: 'grid',
        'grid-template-columns': '1fr auto',
        'column-gap': '8px',
        'row-gap': '4px',
        'align-items': 'start',
      }}
    >
      <div>
        <div style={{ font: '11px ui-monospace,Menlo,Consolas,monospace', opacity: 0.7 }}>
          <span data-testid={`priv-req-sender-${props.req.req_id}`}>
            <Show
              when={props.req.cli_origin}
              fallback={<>{props.req.sender_app_id || '(unknown)'}</>}
            >
              Terminal{' '}
              <Show when={props.req.cli_origin!.tty}>
                <span style={{ opacity: 0.85 }}>{props.req.cli_origin!.tty}</span>{' '}
              </Show>
              <span style={{ opacity: 0.6 }}>
                (pid {props.req.cli_origin!.pid}
                <Show when={props.req.cli_origin!.comm}>
                  , {props.req.cli_origin!.comm}
                </Show>
                )
              </span>
            </Show>
          </span>
          <span style={{ opacity: 0.5 }}> · {props.req.kind}</span>
        </div>
        <div
          style={{
            font: '12px ui-monospace,Menlo,Consolas,monospace',
            'word-break': 'break-all',
            'margin-top': '2px',
          }}
        >
          {props.req.kind === 'spawn' ? (
            <>
              <strong>{props.req.app_id}</strong>{' '}
              {argvPreview(props.req.argv ?? [])}
            </>
          ) : (
            argvPreview(props.req.argv ?? [])
          )}
        </div>
        <Show when={props.req.reason}>
          <div style={{ opacity: 0.6, 'font-size': '11px', 'margin-top': '2px' }}>
            {props.req.reason}
          </div>
        </Show>
      </div>
      <Show
        when={isQueued()}
        fallback={
          <span
            data-testid={`priv-req-status-${props.req.req_id}`}
            style={{ font: '11px ui-monospace,Menlo,Consolas,monospace', opacity: 0.6 }}
          >
            {props.req.status}
            <Show when={isTerminal() && props.req.status === 'done'}>
              {' '}(exit {props.req.exit_code})
            </Show>
          </span>
        }
      >
        <div style={{ display: 'flex', gap: '6px' }}>
          <Button
            data-testid={`priv-req-approve-${props.req.req_id}`}
            variant="primary"
            size="sm"
            onClick={props.onApprove}
          >
            Approve
          </Button>
          <Button
            data-testid={`priv-req-reject-${props.req.req_id}`}
            variant="ghost"
            size="sm"
            onClick={props.onReject}
          >
            Reject
          </Button>
        </div>
      </Show>
      <Show when={props.req.error}>
        <div
          style={{
            'grid-column': '1 / span 2',
            opacity: 0.7,
            font: '11px ui-monospace,Menlo,Consolas,monospace',
            color: '#e0a0a0',
            'word-break': 'break-all',
          }}
        >
          {props.req.error}
        </div>
      </Show>
    </div>
  );
};

const PasswordModal: Component<{
  submitting: boolean;
  error: string;
  onSubmit: (password: string) => void;
  onCancel: () => void;
}> = (props) => {
  let inputEl!: HTMLInputElement;
  const onSubmit = (e: Event) => {
    e.preventDefault();
    if (props.submitting) return;
    const v = inputEl.value;
    inputEl.value = ''; // wipe the DOM-side dwell ASAP
    props.onSubmit(v);
  };
  onMount(() => {
    inputEl?.focus();
  });
  return (
    <div
      data-testid="priv-pw-modal"
      style={{
        position: 'absolute',
        inset: '0',
        background: 'rgba(0,0,0,0.55)',
        display: 'flex',
        'align-items': 'center',
        'justify-content': 'center',
        'z-index': 10,
      }}
    >
      <form
        onSubmit={onSubmit}
        style={{
          background: '#1c1c2a',
          border: '1px solid #4a3a3a',
          'border-radius': '6px',
          padding: '14px 16px',
          width: '320px',
          'box-shadow': '0 12px 32px rgba(0,0,0,0.5)',
          display: 'flex',
          'flex-direction': 'column',
          gap: '8px',
        }}
      >
        <div style={{ 'font-weight': 600 }}>sudo password</div>
        <div style={{ opacity: 0.7, 'font-size': '11px' }}>
          encrypted to a fresh session key; never logged or persisted
        </div>
        <input
          ref={inputEl}
          data-testid="priv-pw-input"
          type="password"
          autocomplete="off"
          autocorrect="off"
          spellcheck={false}
          disabled={props.submitting}
          style={{
            background: '#10101a',
            color: '#eee',
            border: '1px solid #2a2a3a',
            'border-radius': '4px',
            padding: '8px 10px',
            font: '13px ui-monospace,Menlo,Consolas,monospace',
          }}
        />
        <Show when={props.error}>
          <div
            data-testid="priv-pw-err"
            style={{ color: '#e0a0a0', 'font-size': '11px' }}
          >
            {props.error}
          </div>
        </Show>
        <div style={{ display: 'flex', gap: '6px', 'justify-content': 'flex-end' }}>
          <Button type="button" variant="ghost" size="sm" onClick={props.onCancel}>
            Cancel
          </Button>
          <Button
            type="submit"
            data-testid="priv-pw-submit"
            variant="primary"
            size="sm"
            disabled={props.submitting}
          >
            {props.submitting ? '…' : 'Unlock'}
          </Button>
        </div>
      </form>
    </div>
  );
};

class WashAppPriv extends HTMLElement {
  private cleanup?: () => void;
  connectedCallback() {
    const instance = this.getAttribute('data-wash-instance') ?? '';
    this.cleanup = render(() => <App instance={instance} host={this} />, this);
  }
  disconnectedCallback() {
    this.cleanup?.();
    this.cleanup = undefined;
  }
}

if (!customElements.get('wash-app-priv')) {
  customElements.define('wash-app-priv', WashAppPriv);
}
