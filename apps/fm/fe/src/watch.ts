// Per-directory fs.watch bookkeeping + refresh debounce. Framework-free
// (no DOM, no Solid) so it's unit-testable with node:test and reusable
// by the other apps that hand-roll the same pattern (edit). Extracted
// verbatim from fm's App() closure.
//
// Two concerns, both keyed by directory path:
//  1. watch()/unwatch() — a dedup set over the BE's idempotent watch op
//     so expand/collapse don't spam watch/unwatch requests. The BE
//     watches a dir and pushes fs_event when its contents change.
//  2. scheduleRefresh() — coalesces the burst of fs_events that one
//     logical save can fire (write+chmod) into a single re-list after
//     debounceMs, and skips dirs that are no longer listed+expanded
//     (shouldRefresh) so a stale event can't resurrect a row the user
//     just collapsed mid-debounce.

// Clock indirection so tests drive the debounce deterministically
// instead of waiting real milliseconds. Production passes the real
// timers. (Structurally identical to bus.ts's Clock — kept local so
// the two modules stay independent until they're packaged together.)
export interface Clock {
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
}

const realClock: Clock = {
  setTimeout: (fn, ms) => globalThis.setTimeout(fn, ms),
  clearTimeout: (h) => globalThis.clearTimeout(h as ReturnType<typeof setTimeout>),
};

export interface WatchOptions {
  // send a fire-and-forget message to the BE (watch / unwatch requests).
  send: (msg: unknown) => void;
  // refresh re-lists dir; called when a debounced refresh fires and
  // shouldRefresh(dir) still holds.
  refresh: (dir: string) => void;
  // shouldRefresh gates both scheduling and firing. In fm it's
  // `!!listings[dir] && !!expanded[dir]`. It's re-checked at fire time
  // because the dir may have been collapsed during the debounce window.
  shouldRefresh: (dir: string) => boolean;
  clock?: Clock;
  debounceMs?: number;
}

export interface Watch {
  // watch records dir and sends a watch request the first time only.
  watch(dir: string): void;
  // unwatch drops dir and sends an unwatch request only if it was watched.
  unwatch(dir: string): void;
  // isWatching reports membership (test / diagnostic aid).
  isWatching(dir: string): boolean;
  // scheduleRefresh debounces a re-list of dir; repeat calls within the
  // window coalesce onto a single re-list.
  scheduleRefresh(dir: string): void;
  // dispose clears any pending refresh timers (call on unmount).
  dispose(): void;
}

export function createWatch(opts: WatchOptions): Watch {
  const { send, refresh, shouldRefresh } = opts;
  const clock = opts.clock ?? realClock;
  const debounceMs = opts.debounceMs ?? 100;

  const watching = new Set<string>();
  const refreshTimers = new Map<string, unknown>();

  const watch = (dir: string): void => {
    if (watching.has(dir)) return;
    watching.add(dir);
    send({ kind: 'watch', path: dir });
  };

  const unwatch = (dir: string): void => {
    if (!watching.has(dir)) return;
    watching.delete(dir);
    send({ kind: 'unwatch', path: dir });
  };

  const scheduleRefresh = (dir: string): void => {
    if (!shouldRefresh(dir)) return;
    const prev = refreshTimers.get(dir);
    if (prev !== undefined) clock.clearTimeout(prev);
    const tok = clock.setTimeout(() => {
      refreshTimers.delete(dir);
      if (shouldRefresh(dir)) refresh(dir);
    }, debounceMs);
    refreshTimers.set(dir, tok);
  };

  const dispose = (): void => {
    for (const tok of refreshTimers.values()) clock.clearTimeout(tok);
    refreshTimers.clear();
  };

  return {
    watch,
    unwatch,
    isWatching: (dir) => watching.has(dir),
    scheduleRefresh,
    dispose,
  };
}
