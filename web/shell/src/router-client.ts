// RouterClient — one connection to one wash router, plus the
// per-connection dispatch state that hangs off it.
//
// Today the shell talks to exactly one router (the local one). The
// remote-apps work (docs/REMOTE.md, R2) makes the browser a client of
// N routers — its local one plus one per remote host reached over an
// `ssh -L` tunnel. This class is the unit that gets instantiated once
// per connection: each owns its own transport (Conn), its credit
// ledger, and the maps that are keyed by router-assigned ids
// (channel/instance/window). Those ids are per-connection — channel 42
// on router A is unrelated to channel 42 on router B — so they must
// live on the client, not at module scope.
//
// M1a (this commit) extracts the class and moves the connection-scoped
// state onto it, still with a single instance. M1b introduces the
// multi-client registry; M1c threads origin through the dispatch so a
// second router's windows can be composited into the same desktop.

import { Conn, type CtrlHandler, type RawHandler, type SocketFactory } from './ws';
import { CreditTracker } from './credit';

export class RouterClient {
  /** Transport to this router (one wash frame per binary message). */
  readonly conn: Conn;
  /** Per-channel flow-control ledger for this connection (QOS.md §5). */
  readonly credit: CreditTracker;

  // ---- Per-connection dispatch state ----
  // All keyed by router-assigned ids, which are scoped to this
  // connection. channelOwner records which window an open raw channel
  // is rooted at, for cleanup on window/channel teardown.
  readonly channelOwner = new Map<number, number>(); // channel_id → window_id
  // Declared instances, so window.create can resolve element by id.
  readonly instances = new Map<string, { element: string; surface: string }>();
  // Resolves once an instance's bundle has imported (customElements.define
  // has run). The router can race window.create ahead of the bundle, so
  // the create path awaits this.
  readonly bundleReady = new Map<string, Promise<void>>();
  // Window ids already first-sighted, for viewport auto-relocation of
  // freshly-spawned windows.
  readonly seenWindowIDs = new Set<number>();
  // Outstanding clipboard.get round-trips (req_id → resolver).
  readonly pendingClipboardGets = new Map<number, (text: string) => void>();

  constructor(transport: string | SocketFactory, onCtrl: CtrlHandler, onRaw: RawHandler) {
    // credit's callback sends over this.conn, which is assigned just
    // below — the callback never fires until the first raw frame is
    // absorbed, well after construction, so the forward reference is safe.
    this.credit = new CreditTracker((channelID, n) => {
      this.conn.sendCtrl({ t: 'channel.credit', ch: channelID, n });
    });
    this.conn = new Conn(transport, onCtrl, onRaw);
  }
}
