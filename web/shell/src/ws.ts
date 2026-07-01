// Browser-side wash transport: one wash frame per binary message
// (WIRE.md §1). Reconnects on close with backoff so dev-reload's
// `syscall.Exec` self-restart shows up as a brief "reconnecting…"
// banner instead of a permanently dead tab.
//
// The default transport is a real WebSocket to the router's HTTP
// listener. With ?transport=virtio-console (v86 online demo) main.tsx
// hands Conn a factory that produces VirtioConsoleSocket instances
// instead — same SocketLike contract, different underlying byte
// stream.

import {
  CLASS_BULK,
  CLASS_CONTROL,
  CLASS_INTERACTIVE,
  type Class,
  decodeFrame,
  encodeCtrl,
  encodeFrame,
  flagsWithClass,
  FLAG_END,
  decodeCtrl,
} from './wire.ts';
import type { SocketLike } from './virtio.ts';

export type CtrlHandler = (msg: any) => void;
export type RawHandler = (channelID: number, bytes: Uint8Array) => void;
export type StateHandler = (state: ConnState) => void;
// 'unauthenticated' is terminal: the reconnect loop stops because the
// server refused the handshake on auth grounds (expired wash-login
// cookie, or a rotated raw-router token), not a transient drop. The
// shell turns this into a "log in again" / "reopen token URL" prompt
// instead of spinning on 'reconnecting' forever — see the /auth/check
// preflight below.
export type ConnState = 'connecting' | 'open' | 'reconnecting' | 'closed' | 'unauthenticated';

/** Factory returning a fresh SocketLike each call. Reconnect calls it. */
export type SocketFactory = () => SocketLike;

// ConnEvent is the connection-lifecycle debug trail. State transitions are
// already observable via onState; these carry the *why* and the timing the
// banner + the FE log + __washDiag want when diagnosing a flaky link (clean
// close vs zombie? how long down? was input lost?). The shell wires onEvent
// into shellLog, so the trail is forwarded to the router and surfaces in the
// About panel and the router log.
export type ConnEventKind =
  | 'connecting' // dialing a socket (attempt > 0 means a reconnect)
  | 'open' // socket established (carries rttMs once the first pong lands)
  | 'close' // socket closed (carries code)
  | 'reconnect-scheduled' // backoff timer armed
  | 'zombie' // heartbeat got no pong in time — forcing a redial
  | 'wake' // a wake signal (visibility/online/pageshow) drove a probe
  | 'reconnect-now' // explicit immediate redial, backoff skipped
  | 'lost-input'; // pending queue overflowed; queued frames discarded

export interface ConnEvent {
  kind: ConnEventKind;
  msg: string;
  /** WebSocket close code, for 'close'. */
  code?: number;
}

export type EventHandler = (e: ConnEvent) => void;

/** Point-in-time connection diagnostics for the banner + __washDiag. */
export interface ConnDiag {
  state: ConnState;
  /** ms since the last inbound frame of any kind (Infinity if never). */
  sinceContactMs: number;
  /** ms since the last heartbeat pong (Infinity if never). */
  sincePongMs: number;
  /** Last measured heartbeat round-trip, or null. */
  rttMs: number | null;
  /** Reconnect attempts in the current down-streak (0 when open). */
  reconnectAttempts: number;
  /** Total successful reconnects this page load. */
  totalReconnects: number;
  /** Frames queued awaiting reconnect. */
  pendingFrames: number;
  pendingBytes: number;
  /** Last close code/reason seen — "why did it drop". */
  lastCloseCode: number | null;
  lastCloseReason: string;
  /** navigator.onLine at snapshot time (true if unknown). */
  online: boolean;
  /** Whether the heartbeat runs on this transport. */
  heartbeat: boolean;
}

// Heartbeat timing. The FE pings on an interval; if a ping goes unanswered
// for longer than the pong timeout the socket is a zombie (the OS froze it
// on laptop-suspend and never delivered a close) and we force a redial. The
// router reaps its own side after readIdleTimeout (45s) off these same pings.
const HEARTBEAT_INTERVAL_MS = 15_000;
const PONG_TIMEOUT_MS = 10_000;
// After a wake signal we want a verdict in seconds, not on the next 15s
// tick, so the wake probe uses a tighter deadline.
const WAKE_PROBE_TIMEOUT_MS = 4_000;

/** Tuning + injection seams. Timing fields exist for fast unit tests. */
export interface ConnOpts {
  fetchImpl?: typeof fetch;
  /** Run the liveness heartbeat. Defaults to true for real-WebSocket
   *  (string-URL) transports, false for relay/virtio factories whose
   *  liveness is already covered by the underlying connection. */
  heartbeat?: boolean;
  heartbeatMs?: number;
  pongTimeoutMs?: number;
  wakeProbeMs?: number;
  /** Clock source (tests). Defaults to Date.now. */
  now?: () => number;
}

export class Conn {
  private ws!: SocketLike;
  private handler: CtrlHandler;
  private rawHandler: RawHandler;
  private stateHandlers = new Set<StateHandler>();
  private factory: SocketFactory;
  private opening: Promise<void>;
  private state: ConnState = 'connecting';
  private reconnectAttempts = 0;
  private closedByUser = false;
  // Only HTTP(S)-backed sockets (real WebSocket from a URL) get the
  // /auth/check preflight; a virtio/serial factory has no same-origin
  // auth endpoint to probe, so it keeps the plain backoff loop.
  private wantAuthProbe: boolean;
  // Where to send the user when auth is gone. Populated from the
  // /auth/check 401 body ("/login" for wash-login; null for the raw
  // router, whose recovery is "reopen the token URL"). Read by the UI
  // once state goes 'unauthenticated'.
  private loginURL: string | null = null;
  // Injectable for tests; defaults to the platform fetch.
  private fetchImpl: typeof fetch;
  // Outbound frames produced while the socket is down. WebSocket.send
  // on a closed socket silently discards, so without this queue every
  // keystroke / app_msg / save_state issued during the reconnect
  // window vanishes. Flushed FIFO the moment the replacement socket
  // opens (before state handlers run, so queued frames keep their
  // causal order ahead of anything a handler sends on 'open').
  // Channel ids and instance ids survive a reconnect router-side, so
  // replaying the frames verbatim is safe; frames for a channel that
  // closed during the detach are dropped by the router with a log.
  private pending: Uint8Array[] = [];
  private pendingBytes = 0;
  // Cap on queued bytes. On overflow the whole queue is dropped —
  // delivering a gap-toothed middle of a byte stream is worse than
  // delivering none of it.
  static readonly MAX_PENDING_BYTES = 1 << 20;

  // ---- diagnostics + heartbeat state ----
  private eventHandlers = new Set<EventHandler>();
  private now: () => number;
  private totalReconnects = 0;
  // lastContactAt: ms timestamp of the most recent inbound frame (any kind).
  // Drives the banner's "no contact for Ns" and is the FE mirror of the
  // router's lastReadAt. 0 = nothing received yet.
  private lastContactAt = 0;
  private lastPongAt = 0;
  private lastRttMs: number | null = null;
  private lastCloseCode: number | null = null;
  private lastCloseReason = '';
  // The pending reconnect backoff timer, held so reconnectNow()/forceRedial()
  // can cancel and re-dial without waiting it out.
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  private readonly hbEnabled: boolean;
  private readonly hbIntervalMs: number;
  private readonly pongTimeoutMs: number;
  private readonly wakeProbeMs: number;
  // Fallback ticker used only when Worker isn't available (test runner /
  // non-browser environments). The real path is hbWorker below — see
  // startHeartbeat.
  private hbTimer: ReturnType<typeof setInterval> | null = null;
  // Ticks the periodic ping from a dedicated thread so a busy or throttled
  // main thread can't silently starve it (see heartbeat-worker.ts). Created
  // lazily on first startHeartbeat and reused across reconnects; only
  // started/stopped per connection.
  private hbWorker: Worker | null = null;
  private pongTimer: ReturnType<typeof setTimeout> | null = null;
  private pingSeq = 0;
  // Seq of the ping we're awaiting a pong for, or null when none outstanding.
  private outstandingPing: number | null = null;
  private lastPingSentAt = 0;

  /**
   * Construct from a URL (default: real WebSocket) or a SocketFactory
   * (e.g. VirtioConsoleSocket factory from `virtio.ts`). See ConnOpts for
   * the auth-probe fetch override + heartbeat tuning/injection seams.
   */
  constructor(
    urlOrFactory: string | SocketFactory,
    handler: CtrlHandler,
    rawHandler: RawHandler,
    opts?: ConnOpts,
  ) {
    this.handler = handler;
    this.rawHandler = rawHandler;
    this.wantAuthProbe = typeof urlOrFactory === 'string';
    this.fetchImpl = opts?.fetchImpl ?? ((...a: Parameters<typeof fetch>) => fetch(...a));
    this.now = opts?.now ?? (() => Date.now());
    // Heartbeat only on real WebSocket transports by default: a relay
    // (RelayChannelSocket) rides the local WS, which heartbeats itself, and
    // the virtio/v86 demo transport has no suspend story — pinging there
    // would just inject redundant frames into B's wire.
    this.hbEnabled = opts?.heartbeat ?? this.wantAuthProbe;
    this.hbIntervalMs = opts?.heartbeatMs ?? HEARTBEAT_INTERVAL_MS;
    this.pongTimeoutMs = opts?.pongTimeoutMs ?? PONG_TIMEOUT_MS;
    this.wakeProbeMs = opts?.wakeProbeMs ?? WAKE_PROBE_TIMEOUT_MS;
    this.factory =
      typeof urlOrFactory === 'string'
        ? () => new WebSocket(urlOrFactory) as unknown as SocketLike
        : urlOrFactory;
    this.opening = new Promise((resolve, reject) => {
      this.connect(resolve, reject);
    });
  }

  ready(): Promise<void> { return this.opening; }

  /**
   * close terminally tears this connection down: it stops the reconnect
   * loop (closedByUser), drops any queued frames, and closes the socket.
   * Used when a remote host is detached (docs/REMOTE.md §6.1) — unlike a
   * transport blip (which reconnects), a user disconnect must not re-dial.
   * Idempotent.
   */
  close(): void {
    this.closedByUser = true;
    this.stopHeartbeat();
    this.hbWorker?.terminate();
    this.hbWorker = null;
    this.clearReconnectTimer();
    this.clearPending();
    this.setState('closed');
    try {
      this.ws?.close();
    } catch {
      /* already closed */
    }
  }

  /** The redirect target when state is 'unauthenticated', or null when
   * recovery is out-of-band (raw router: reopen the token URL). */
  loginRedirect(): string | null { return this.loginURL; }

  onState(fn: StateHandler): () => void {
    this.stateHandlers.add(fn);
    fn(this.state);
    return () => { this.stateHandlers.delete(fn); };
  }

  /** Subscribe to the connection-lifecycle debug trail (see ConnEvent). */
  onEvent(fn: EventHandler): () => void {
    this.eventHandlers.add(fn);
    return () => { this.eventHandlers.delete(fn); };
  }

  private emit(e: ConnEvent) {
    for (const fn of this.eventHandlers) fn(e);
  }

  private setState(s: ConnState) {
    if (this.state === s) return;
    this.state = s;
    for (const fn of this.stateHandlers) fn(s);
  }

  private connect(resolveReady?: () => void, rejectReady?: (e: Error) => void) {
    const attempt = this.reconnectAttempts;
    this.setState(attempt === 0 ? 'connecting' : 'reconnecting');
    this.emit({ kind: 'connecting', msg: attempt === 0 ? 'dialing router' : `redial (attempt ${attempt})` });
    this.ws = this.factory();
    this.ws.binaryType = 'arraybuffer';
    this.ws.onopen = () => {
      const wasReconnect = this.reconnectAttempts > 0;
      this.reconnectAttempts = 0;
      if (wasReconnect) this.totalReconnects += 1;
      this.flushPending();
      this.setState('open');
      this.emit({ kind: 'open', msg: wasReconnect ? `reconnected (#${this.totalReconnects})` : 'connected' });
      this.startHeartbeat();
      resolveReady?.();
    };
    this.ws.onerror = () => {
      if (this.reconnectAttempts === 0) {
        rejectReady?.(new Error('socket error'));
      }
    };
    this.ws.onmessage = (ev) => this.onMessage(ev);
    this.ws.onclose = (ev) => {
      this.lastCloseCode = ev?.code ?? null;
      this.lastCloseReason = ev?.reason ?? '';
      this.stopHeartbeat();
      this.emit({
        kind: 'close',
        code: ev?.code,
        msg: `socket closed code=${ev?.code ?? '?'}${ev?.reason ? ` reason=${ev.reason}` : ''}`,
      });
      if (this.closedByUser) {
        this.clearPending();
        this.setState('closed');
        return;
      }
      this.scheduleReconnect();
    };
  }

  // scheduleReconnect arms the backoff timer: 250ms, 500ms, 1s, 2s, then
  // capped at 4s. Held in reconnectTimer so reconnectNow()/forceRedial()
  // can cancel and dial immediately.
  private scheduleReconnect(): void {
    if (this.closedByUser) return;
    const delay = Math.min(250 * 2 ** this.reconnectAttempts, 4000);
    this.reconnectAttempts += 1;
    this.setState('reconnecting');
    this.emit({ kind: 'reconnect-scheduled', msg: `reconnect in ${delay}ms (attempt ${this.reconnectAttempts})` });
    this.clearReconnectTimer();
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      void this.reconnectTick();
    }, delay);
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer != null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  // reconnectTick fires after the backoff delay. Before blindly
  // re-dialing, it asks the server whether the close was an auth
  // failure: a refused WS handshake (expired cookie / rotated token)
  // is indistinguishable from a network drop at the WebSocket API
  // level — both surface only as onclose. The /auth/check preflight
  // disambiguates: a 401 there means "stop looping, the user must
  // re-authenticate"; anything else (200/204, or the probe itself
  // failing because the server is unreachable) means "transient, keep
  // reconnecting".
  private async reconnectTick(): Promise<void> {
    if (this.closedByUser) return;
    if (this.wantAuthProbe && (await this.authGone())) {
      this.clearPending();
      this.setState('unauthenticated');
      return;
    }
    if (this.closedByUser) return;
    this.connect();
  }

  // authGone probes /auth/check. Returns true only on a definitive
  // 401 (or an opaque redirect to a login page); a network error
  // returns false so we keep retrying rather than falsely declaring
  // the session dead when the server is merely down.
  private async authGone(): Promise<boolean> {
    try {
      const base = typeof location !== 'undefined' ? location.href : 'http://localhost/';
      const resp = await this.fetchImpl(new URL('/auth/check', base).href, {
        credentials: 'same-origin',
        redirect: 'manual',
        cache: 'no-store',
      });
      if (resp.type === 'opaqueredirect') {
        // A 3xx to a login page — treated as not-authenticated. We
        // can't read the body of an opaque redirect, so fall back to
        // the default login path.
        this.loginURL = '/login';
        return true;
      }
      if (resp.status === 401) {
        try {
          const body = (await resp.json()) as { login_url?: string | null };
          this.loginURL = body.login_url ?? null;
        } catch {
          this.loginURL = null;
        }
        return true;
      }
      return false;
    } catch {
      return false;
    }
  }

  private onMessage(ev: MessageEvent) {
    // Any inbound frame proves the socket is alive — refresh the liveness
    // clock before doing anything else (mirrors the router's lastReadAt).
    this.lastContactAt = this.now();
    const f = decodeFrame(new Uint8Array(ev.data as ArrayBuffer));
    if (f.channel !== 0) {
      this.rawHandler(f.channel, f.payload);
      return;
    }
    let msg: any;
    try {
      msg = decodeCtrl(f.payload);
    } catch (e) {
      console.error('wash: decode ctrl:', e);
      return;
    }
    // Pong is a Conn-internal heartbeat reply; consume it here so it never
    // reaches the app ctrl handler (which has no 'pong' case).
    if (msg && msg.t === 'pong') {
      this.onPong(typeof msg.seq === 'number' ? msg.seq : -1);
      return;
    }
    this.handler(msg);
  }

  // ---- heartbeat ----
  //
  // While the socket is open the FE pings every hbIntervalMs and expects a
  // pong within pongTimeoutMs. A miss means the OS froze the socket without
  // a close (the laptop-suspend zombie) — onPongTimeout forces a redial. The
  // router answers every ping (handlePing → pong) and also uses the inbound
  // pings to reap its own dead side (readIdleLoop).

  private startHeartbeat(): void {
    if (!this.hbEnabled) return;
    this.stopHeartbeat();
    // Ping immediately so the first RTT (and the router's watchdog arming)
    // doesn't wait a whole interval, then settle into the periodic cadence.
    this.sendPing(this.pongTimeoutMs);
    if (typeof Worker !== 'undefined') {
      this.ensureHbWorker().postMessage({ type: 'start', intervalMs: this.hbIntervalMs });
    } else {
      // No Worker (test runner, or an environment without one) — fall back
      // to a main-thread interval. Loses the anti-starvation property but
      // keeps the heartbeat working at all.
      this.hbTimer = setInterval(() => this.sendPing(this.pongTimeoutMs), this.hbIntervalMs);
    }
  }

  private stopHeartbeat(): void {
    if (this.hbTimer != null) { clearInterval(this.hbTimer); this.hbTimer = null; }
    this.hbWorker?.postMessage({ type: 'stop' });
    if (this.pongTimer != null) { clearTimeout(this.pongTimer); this.pongTimer = null; }
    this.outstandingPing = null;
  }

  // ensureHbWorker lazily creates the heartbeat worker once and reuses it
  // across reconnects (startHeartbeat/stopHeartbeat just start/stop its
  // internal interval). Each 'tick' message it posts back drives one
  // sendPing on the main thread — the worker never touches the socket
  // itself, it only supplies a schedule the main thread's JS can't stall.
  private ensureHbWorker(): Worker {
    if (this.hbWorker) return this.hbWorker;
    const w = new Worker(new URL('./heartbeat-worker.ts', import.meta.url), { type: 'module' });
    w.onmessage = (ev: MessageEvent) => {
      if (ev.data?.type === 'tick') this.sendPing(this.pongTimeoutMs);
    };
    this.hbWorker = w;
    return w;
  }

  // sendPing emits one heartbeat with a fresh seq and arms a deadline timer.
  // deadlineMs is pongTimeoutMs for the periodic beat and the shorter
  // wakeProbeMs for a wake-triggered probe. Control class so the ping (and
  // the pong) jump ahead of any bulk traffic in either direction — a busy
  // link must not look like a dead one.
  private sendPing(deadlineMs: number): void {
    if (!this.hbEnabled || this.state !== 'open') return;
    this.pingSeq += 1;
    const seq = this.pingSeq;
    this.outstandingPing = seq;
    this.lastPingSentAt = this.now();
    if (this.pongTimer != null) clearTimeout(this.pongTimer);
    this.pongTimer = setTimeout(() => this.onPongTimeout(seq), deadlineMs);
    this.sendCtrl({ t: 'ping', seq }, CLASS_CONTROL);
  }

  private onPong(seq: number): void {
    this.lastPongAt = this.now();
    if (seq !== this.outstandingPing) return; // stale / duplicate pong
    this.lastRttMs = this.lastPongAt - this.lastPingSentAt;
    this.outstandingPing = null;
    if (this.pongTimer != null) { clearTimeout(this.pongTimer); this.pongTimer = null; }
  }

  private onPongTimeout(seq: number): void {
    if (this.state !== 'open' || this.outstandingPing !== seq) return;
    this.forceRedial(`no heartbeat pong in ${Math.round((this.now() - this.lastPingSentAt))}ms — connection looks dead`);
  }

  // forceRedial tears down a socket we believe is a zombie and dials again
  // without trusting the dead socket to ever fire onclose. It nulls the old
  // socket's handlers first so a late close can't double-drive the state
  // machine, then schedules a near-immediate reconnect (attempts reset).
  private forceRedial(reason: string): void {
    if (this.closedByUser || this.state === 'reconnecting' || this.state === 'unauthenticated') return;
    this.emit({ kind: 'zombie', msg: reason });
    this.stopHeartbeat();
    this.teardownSocket();
    this.reconnectAttempts = 0;
    this.scheduleReconnect();
  }

  private teardownSocket(): void {
    const ws = this.ws;
    if (!ws) return;
    ws.onopen = null;
    ws.onerror = null;
    ws.onmessage = null;
    ws.onclose = null;
    try { ws.close(); } catch { /* already dead */ }
  }

  /**
   * reconnectNow cancels any pending backoff and re-dials immediately.
   * Backs the banner's "Reconnect now" button and the wake handler. No-op
   * when already open or terminally closed; for 'unauthenticated' it re-runs
   * the auth probe (the session may have come back).
   */
  reconnectNow(): void {
    if (this.closedByUser || this.state === 'open' || this.state === 'connecting') return;
    this.emit({ kind: 'reconnect-now', msg: 'immediate reconnect requested' });
    this.clearReconnectTimer();
    this.reconnectAttempts = 0;
    void this.reconnectTick();
  }

  /**
   * wake is called by the shell on a resume signal (tab became visible, the
   * browser fired `online`, or a bfcache `pageshow`). Laptop-suspend often
   * freezes the socket without a close, so the page can come back believing
   * it is still connected. When open we fire a tight-deadline heartbeat probe
   * to confirm liveness fast; when down we skip the backoff and dial now.
   */
  wake(source: string): void {
    if (this.closedByUser) return;
    this.emit({ kind: 'wake', msg: `wake (${source}) while ${this.state}` });
    if (this.state === 'open') {
      if (this.hbEnabled) this.sendPing(this.wakeProbeMs);
    } else if (this.state === 'reconnecting' || this.state === 'unauthenticated') {
      this.reconnectNow();
    }
  }

  /**
   * dropSocketForTest simulates a transient network drop of the live
   * socket: it closes the current socket WITHOUT nulling its handlers, so
   * the real onclose fires and drives the normal scheduleReconnect path —
   * exactly a same-router live-page reconnect (no page reload). Used by the
   * term-live-reconnect e2e (REVIEW-RECONNECT H1). No-op if already down.
   */
  dropSocketForTest(): void {
    try { this.ws?.close(); } catch { /* already dead */ }
  }

  /** Point-in-time connection diagnostics (banner + __washDiag + tests). */
  diag(): ConnDiag {
    const now = this.now();
    return {
      state: this.state,
      sinceContactMs: this.lastContactAt ? now - this.lastContactAt : Infinity,
      sincePongMs: this.lastPongAt ? now - this.lastPongAt : Infinity,
      rttMs: this.lastRttMs,
      reconnectAttempts: this.reconnectAttempts,
      totalReconnects: this.totalReconnects,
      pendingFrames: this.pending.length,
      pendingBytes: this.pendingBytes,
      lastCloseCode: this.lastCloseCode,
      lastCloseReason: this.lastCloseReason,
      online: typeof navigator !== 'undefined' ? navigator.onLine : true,
      heartbeat: this.hbEnabled,
    };
  }

  /**
   * Send a JSON control message on channel 0. Class defaults to
   * Interactive — control frames are user-action / lifecycle by
   * nature. Callers in unusual cases (telemetry batches?) can pass
   * an explicit class.
   */
  sendCtrl(msg: unknown, cls: Class = CLASS_INTERACTIVE): void {
    const payload = encodeCtrl(msg);
    this.sendFrame(encodeFrame({ flags: flagsWithClass(FLAG_END, cls), channel: 0, payload }));
  }

  /**
   * Send raw bytes on a dynamic channel. Class defaults to Bulk
   * because most raw streams from the FE side are large/streaming
   * (input pasting, file uploads); keystroke-style interactive
   * raw flows (e.g. wash-term's one-byte keystroke writes) should
   * pass CLASS_INTERACTIVE explicitly so they don't queue behind
   * other apps' bulk traffic on the way to the router.
   */
  sendRaw(channelID: number, payload: Uint8Array, cls: Class = CLASS_BULK): void {
    this.sendFrame(encodeFrame({ flags: flagsWithClass(FLAG_END, cls), channel: channelID, payload }));
  }

  // sendFrame writes when the socket is open, queues while it is
  // reconnecting, and discards once the connection is terminally
  // gone (closed by us, or auth-dead — the page is headed to /login
  // and a stale queue must not replay into a fresh session).
  private sendFrame(frame: Uint8Array): void {
    if (this.state === 'open') {
      this.ws.send(frame);
      return;
    }
    if (this.closedByUser || this.state === 'closed' || this.state === 'unauthenticated') return;
    this.pending.push(frame);
    this.pendingBytes += frame.byteLength;
    if (this.pendingBytes > Conn.MAX_PENDING_BYTES) {
      const n = this.pending.length;
      const bytes = this.pendingBytes;
      console.warn(`wash: dropping ${n} queued frames (${bytes} bytes) — offline too long`);
      this.clearPending();
      // Surface the loss instead of swallowing it: the user's recent input
      // during the outage is gone, and the banner/log should say so.
      this.emit({ kind: 'lost-input', msg: `dropped ${n} unsent frame(s) (${bytes} bytes) — offline too long` });
    }
  }

  private flushPending(): void {
    const frames = this.pending;
    this.clearPending();
    for (const f of frames) this.ws.send(f);
  }

  private clearPending(): void {
    this.pending = [];
    this.pendingBytes = 0;
  }

  /** Frames currently queued awaiting reconnect (tests/diagnostics). */
  pendingCount(): number {
    return this.pending.length;
  }

  /**
   * Bytes still queued in the socket's send buffer. A bulk producer
   * (e.g. fm's file upload) polls this to apply backpressure: without
   * it the producer dumps the whole payload into the buffer at once,
   * head-of-line blocking later control frames (like a cancel) behind
   * megabytes of data on this single socket. Returns 0 for transports
   * that don't expose it (virtio).
   */
  bufferedAmount(): number {
    return this.ws.bufferedAmount ?? 0;
  }
}
