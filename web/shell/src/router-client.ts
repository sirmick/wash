// RouterClient — one connection to one wash router, plus the
// per-connection dispatch state that hangs off it.
//
// The shell is a client of N routers (docs/REMOTE.md, R2): its local one
// plus one per remote host over an `ssh -L` tunnel. Each connection is a
// RouterClient: it owns its transport (Conn), its credit ledger, and the
// maps keyed by router-assigned ids (channel/instance/window) — ids that
// are scoped per-connection, so they live on the client, never at module
// scope. Its dispatch is built by a per-origin handler factory so every
// incoming message is tagged with this client's origin.

import { Conn, type CtrlHandler, type RawHandler, type SocketFactory } from './ws';
import { CreditTracker } from './credit';
import { type Origin } from './clients';

export interface ClientHandlers {
  onCtrl: CtrlHandler;
  onRaw: RawHandler;
}

export class RouterClient {
  /** Which router this client talks to (LOCAL for the shell's own). */
  readonly origin: Origin;
  /** Transport to this router (one wash frame per binary message). */
  readonly conn: Conn;
  /** Per-channel flow-control ledger for this connection (QOS.md §5). */
  readonly credit: CreditTracker;

  // ---- Per-connection dispatch state (bare router ids; this instance is
  // the namespace, so channel 5 here ≠ channel 5 on another client). ----
  readonly channelOwner = new Map<number, number>(); // channel_id → window_id
  readonly instances = new Map<string, { element: string; surface: string }>();
  readonly bundleReady = new Map<string, Promise<void>>();
  readonly seenWindowIDs = new Set<number>();
  readonly pendingClipboardGets = new Map<number, (text: string) => void>();

  constructor(
    origin: Origin,
    transport: string | SocketFactory,
    makeHandlers: (client: RouterClient) => ClientHandlers,
  ) {
    this.origin = origin;
    // credit's callback sends over this.conn, assigned just below — it
    // never fires until the first raw frame is absorbed, well after
    // construction, so the forward reference is safe.
    this.credit = new CreditTracker((channelID, n) => {
      this.conn.sendCtrl({ t: 'channel.credit', ch: channelID, n });
    });
    // makeHandlers closes over `this`; the returned closures aren't invoked
    // until messages arrive (post-construction), so reading this.conn etc.
    // inside them is safe.
    const { onCtrl, onRaw } = makeHandlers(this);
    this.conn = new Conn(transport, onCtrl, onRaw);
  }

  // waitForBundle resolves once an instance's bundle has imported (and its
  // customElements.define has run). The channel.bind for the bundle can
  // arrive just after app.declared, so poll briefly for the promise.
  waitForBundle(instanceID: string): Promise<void> {
    const existing = this.bundleReady.get(instanceID);
    if (existing) return existing;
    return new Promise((resolve, reject) => {
      const start = Date.now();
      const check = () => {
        const p = this.bundleReady.get(instanceID);
        if (p) {
          p.then(resolve, reject);
          return;
        }
        if (Date.now() - start > 10_000) {
          reject(new Error(`bundle for ${instanceID} not announced within 10s`));
          return;
        }
        setTimeout(check, 25);
      };
      check();
    });
  }
}
