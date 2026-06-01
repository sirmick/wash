// FE half of the wash-vm login handshake (wash-vm/UNIFY.md). The in-guest
// login front (wash-vm/guest) gates the wash-wire channel: on a fresh viewer
// it emits {t:"login.required"}; the FE collects credentials, sends
// {t:"login",user,pass}, and the front replies {t:"login.ok"} (then forks
// wash-router as that user) or {t:"login.err"}.
//
// This module is framework-free and operates on a SocketLike — the same seam
// the shell's Conn uses — so it runs over both substrates (virtio-console in
// the browser box, a WebSocket to proxy.go in wemu) unchanged. It does NOT
// open or close the socket: the same live connection is handed to the shell
// after login.ok (closing it would tear down the just-forked router).

import { encodeCtrl, decodeCtrl, encodeFrame, decodeFrame, FLAG_END } from './wire.ts';
import type { SocketLike } from './virtio.ts';

export const T_LOGIN = 'login';
export const T_LOGIN_OK = 'login.ok';
export const T_LOGIN_ERR = 'login.err';
export const T_LOGIN_REQUIRED = 'login.required';

export interface Credentials {
  user: string;
  pass: string;
}

/** Thrown when the front rejects credentials (login.err). */
export class LoginError extends Error {
  constructor(msg: string) {
    super(msg || 'invalid credentials');
    this.name = 'LoginError';
  }
}

/** True if a decoded channel-0 control payload is the front's login.required prompt. */
export function isLoginRequired(payload: Uint8Array): boolean {
  return peekType(payload) === T_LOGIN_REQUIRED;
}

/** Read the `t` tag of a channel-0 control payload, or '' if it isn't JSON ctrl. */
export function peekType(payload: Uint8Array): string {
  try {
    const msg = decodeCtrl(payload) as { t?: string };
    return typeof msg.t === 'string' ? msg.t : '';
  } catch {
    return '';
  }
}

/**
 * Send credentials over `sock` and resolve when the front accepts (login.ok),
 * rejecting with LoginError on login.err. Temporarily owns sock.onmessage and
 * restores the prior handler before settling, so the caller can immediately
 * hand the same socket to the shell. login.required frames (e.g. a re-emitted
 * prompt) are ignored while waiting.
 *
 * The socket must already be open. A socket error/close while waiting rejects.
 */
export function authenticate(sock: SocketLike, creds: Credentials): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const prevMessage = sock.onmessage;
    const prevError = sock.onerror;
    const prevClose = sock.onclose;
    let settled = false;

    const restore = () => {
      sock.onmessage = prevMessage;
      sock.onerror = prevError;
      sock.onclose = prevClose;
    };
    const done = (err?: Error) => {
      if (settled) return;
      settled = true;
      restore();
      err ? reject(err) : resolve();
    };

    sock.onmessage = (ev: MessageEvent) => {
      const f = decodeFrame(new Uint8Array(ev.data as ArrayBuffer));
      if (f.channel !== 0) return; // pre-login: only ctrl frames matter
      const t = peekType(f.payload);
      if (t === T_LOGIN_OK) {
        done();
      } else if (t === T_LOGIN_ERR) {
        let msg = '';
        try { msg = (decodeCtrl(f.payload) as { msg?: string }).msg ?? ''; } catch { /* keep generic */ }
        done(new LoginError(msg));
      }
      // T_LOGIN_REQUIRED or anything else: keep waiting.
    };
    sock.onerror = () => done(new Error('socket error during login'));
    sock.onclose = () => done(new Error('socket closed during login'));

    const payload = encodeCtrl({ t: T_LOGIN, user: creds.user, pass: creds.pass });
    sock.send(encodeFrame({ flags: FLAG_END, channel: 0, payload }));
  });
}
