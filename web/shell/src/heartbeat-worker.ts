// Heartbeat tick source that runs off the main thread.
//
// A plain `setInterval` on the main thread is not a reliable clock: Chrome
// throttles background-tab timers, and — the case that actually bit us
// (docs/PTY_ROBUST.md's channel-9 stall loop persisted even in an actively
// used, foregrounded tab) — any synchronous main-thread work (a big xterm
// write/reflow, a heavy re-render) delays a same-thread `setInterval`
// callback for as long as the thread stays busy. Either way the periodic
// ping silently stops firing, the router's readIdleLoop (45s) reaps the
// connection as a zombie, and the FE has to reconnect + resync every
// terminal channel — which is exactly the "stalled" cycle this exists to
// prevent.
//
// A dedicated Worker has its own thread: its timers keep firing on
// schedule regardless of what the main thread's JS is doing. This worker
// only produces tick signals — Conn still owns the WebSocket and decides
// what to send, on the main thread, when a tick arrives.
let timer: ReturnType<typeof setInterval> | null = null;

type InMsg = { type: 'start'; intervalMs: number } | { type: 'stop' };

self.onmessage = (ev: MessageEvent<InMsg>) => {
  const msg = ev.data;
  if (msg.type === 'start') {
    if (timer != null) clearInterval(timer);
    timer = setInterval(() => self.postMessage({ type: 'tick' }), msg.intervalMs);
  } else if (msg.type === 'stop') {
    if (timer != null) {
      clearInterval(timer);
      timer = null;
    }
  }
};
