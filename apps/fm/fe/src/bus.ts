// Request/reply correlation over the wash:msg event bus. Framework-free
// (no DOM, no Solid) so it's unit-testable with node:test and reusable
// across apps that hand-roll the same pattern (edit, the @wash/ui
// file-picker). Extracted verbatim from fm's App() closure.
//
// The wash bridge is fire-and-forget: an app calls send(msg) and the
// BE's messages arrive separately as wash:msg CustomEvents. createBus
// layers request/response on top: request() tags a message with a fresh
// `f-<n>` id and resolves when a BE message echoes that id; tryResolve()
// is called from the app's wash:msg handler to consume those echoes.

export interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

// Clock indirection so tests drive timeouts deterministically instead of
// waiting real milliseconds. Production passes the real timers.
export interface Clock {
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
}

const realClock: Clock = {
  setTimeout: (fn, ms) => globalThis.setTimeout(fn, ms),
  clearTimeout: (h) => globalThis.clearTimeout(h as ReturnType<typeof setTimeout>),
};

export interface Bus {
  // request tags req with a fresh correlation id, sends it, and resolves
  // with the BE message that echoes that id — or a synthetic
  // { kind: 'timeout_err', ... } after timeoutMs so callers never leak a
  // resolver when the BE goes silent. Inspect the resolved msg.kind to
  // discriminate ok / err.
  request(req: Record<string, unknown>, timeoutMs?: number): Promise<BEMessage>;
  // tryResolve consumes m if it's a reply to an outstanding request:
  // resolves that promise and returns true. Returns false for
  // uncorrelated (push) messages, which the caller dispatches itself.
  tryResolve(m: BEMessage): boolean;
  // pending is the count of in-flight requests (test/diagnostic aid).
  pending(): number;
}

export function createBus(
  send: (msg: unknown) => void,
  clock: Clock = realClock,
  idPrefix = 'f',
): Bus {
  let nextReqID = 0;
  const pendingReplies = new Map<string, (m: BEMessage) => void>();

  const request = (req: Record<string, unknown>, timeoutMs = 5000): Promise<BEMessage> => {
    nextReqID += 1;
    const id = `${idPrefix}-${nextReqID}`;
    return new Promise((resolve) => {
      const timer = clock.setTimeout(() => {
        if (pendingReplies.delete(id)) {
          resolve({ kind: 'timeout_err', id, code: 'timeout', msg: `no reply within ${timeoutMs}ms` });
        }
      }, timeoutMs);
      pendingReplies.set(id, (m) => {
        clock.clearTimeout(timer);
        resolve(m);
      });
      send({ ...req, id });
    });
  };

  const tryResolve = (m: BEMessage): boolean => {
    const replyID = typeof m.id === 'string' ? m.id : undefined;
    if (replyID && pendingReplies.has(replyID)) {
      const resolver = pendingReplies.get(replyID)!;
      pendingReplies.delete(replyID);
      resolver(m);
      return true;
    }
    return false;
  };

  return { request, tryResolve, pending: () => pendingReplies.size };
}
