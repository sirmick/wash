// PrivUnlockOverlay is the screen-anchored password modal that pops
// when wash-priv asks for the sudo password (need_password event).
// Crypto handshake runs entirely in the FE — see privCrypto.ts for
// the ECDH/HKDF/AES-GCM details. The plaintext password never leaves
// this component; we wipe the input field before send and don't
// pull it into any reactive store.
//
// Architectural note: the password keystrokes briefly transit through
// session FE memory. Session FE is the same trust tier as the shell
// (same browser process), so this is not a boundary change, but worth
// knowing.

import type { Component, JSX } from 'solid-js';
import { Show, createSignal } from 'solid-js';
import { Button, tokens } from '@wash/ui';
import { b64decode, b64encode, encryptPassword } from './privCrypto';

export interface PrivUnlockState {
  /** Base64-encoded BE pubkey from the need_password event. */
  be_pubkey: string;
  /** The req_id whose Approve triggered the prompt — included for
   *  display; the BE already knows which request to run. */
  req_id: string;
}

export interface PrivUnlockOverlayProps {
  state: () => PrivUnlockState | null;
  /** Last unlock error from the BE (kind:"unlock_err"), shown inline. */
  error: () => string;
  /** Send the encrypted handshake. Caller forwards via the session
   *  BE's priv_unlock gateway. All fields are base64 strings. */
  onUnlock: (req: { ciphertext: string; fe_pubkey: string; nonce: string }) => void;
  /** Dismiss without unlocking — fires a reject for the prompting req. */
  onCancel: (reqID: string) => void;
}

export const PrivUnlockOverlay: Component<PrivUnlockOverlayProps> = (props) => {
  return (
    <Show when={props.state()}>
      {(s) => <UnlockModal state={s()} error={props.error} onUnlock={props.onUnlock} onCancel={props.onCancel} />}
    </Show>
  );
};

const UnlockModal: Component<{
  state: PrivUnlockState;
  error: () => string;
  onUnlock: (req: { ciphertext: string; fe_pubkey: string; nonce: string }) => void;
  onCancel: (reqID: string) => void;
}> = (props) => {
  let inputEl!: HTMLInputElement;
  const [busy, setBusy] = createSignal(false);
  const [localError, setLocalError] = createSignal('');

  const submit = () => {
    const password = inputEl.value;
    if (!password) {
      setLocalError('password is empty');
      return;
    }
    setBusy(true);
    setLocalError('');
    try {
      const bePub = b64decode(props.state.be_pubkey);
      const { ciphertext, fePubKey, nonce } = encryptPassword(password, bePub);
      // Wipe the input + scrub any local refs we touched.
      inputEl.value = '';
      props.onUnlock({
        ciphertext: b64encode(ciphertext),
        fe_pubkey: b64encode(fePubKey),
        nonce: b64encode(nonce),
      });
    } catch (e) {
      setLocalError(`encrypt failed: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const cancel = () => {
    inputEl.value = '';
    props.onCancel(props.state.req_id);
  };

  const onKey = (ev: KeyboardEvent) => {
    if (ev.key === 'Enter') {
      ev.preventDefault();
      submit();
    } else if (ev.key === 'Escape') {
      ev.preventDefault();
      cancel();
    }
  };

  const backdropStyle: JSX.CSSProperties = {
    position: 'fixed',
    inset: 0,
    background: 'rgba(0,0,0,0.55)',
    display: 'flex',
    'align-items': 'center',
    'justify-content': 'center',
    'z-index': 12000,
    animation: 'wash-fade-in 120ms ease-out',
  };

  const panelStyle: JSX.CSSProperties = {
    background: tokens.bgWindow,
    border: `1px solid ${tokens.bgDanger}`, // red accent — root chain
    'border-radius': tokens.radiusXl,
    padding: '20px 22px',
    'min-width': '380px',
    'max-width': '480px',
    'box-shadow': '0 16px 40px rgba(0,0,0,0.6)',
    color: tokens.fg,
    font: tokens.type.textMd,
    animation: 'wash-pop-in 140ms ease-out',
  };

  return (
    <div data-testid="priv-unlock-overlay" style={backdropStyle}>
      <div data-testid="priv-unlock-modal" style={panelStyle}>
        <div
          style={{
            font: tokens.type.titleSm,
            'margin-bottom': '8px',
            color: tokens.fgDanger,
          }}
        >
          🛡 sudo password
        </div>
        <div style={{ opacity: 0.75, 'font-size': '12px', 'margin-bottom': '12px' }}>
          wash-priv needs your sudo password to approve request
          <span
            style={{
              font: tokens.type.monoSm,
              opacity: 0.85,
              'margin-left': '4px',
            }}
          >
            {props.state.req_id}
          </span>
          .
        </div>
        <input
          ref={inputEl!}
          data-testid="priv-unlock-input"
          type="password"
          autocomplete="current-password"
          autofocus
          onKeyDown={onKey}
          disabled={busy()}
          style={{
            width: '100%',
            'box-sizing': 'border-box',
            padding: '8px 10px',
            background: 'rgba(0,0,0,0.25)',
            color: tokens.fg,
            border: `1px solid ${tokens.borderFocus}`,
            'border-radius': tokens.radiusMd,
            font: tokens.type.monoMd,
            'margin-bottom': '8px',
          }}
        />
        <Show when={props.error() || localError()}>
          <div
            data-testid="priv-unlock-error"
            style={{
              color: tokens.fgDanger,
              'font-size': '11px',
              'margin-bottom': '8px',
            }}
          >
            {localError() || props.error()}
          </div>
        </Show>
        <div style={{ display: 'flex', 'justify-content': 'flex-end', gap: '8px' }}>
          <Button data-testid="priv-unlock-cancel" variant="ghost" size="sm" onClick={cancel} disabled={busy()}>
            Cancel
          </Button>
          <Button data-testid="priv-unlock-submit" variant="danger" size="sm" onClick={submit} disabled={busy()}>
            Unlock
          </Button>
        </div>
      </div>
    </div>
  );
};
