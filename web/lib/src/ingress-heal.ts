// Ingress self-heal: notice that an app's /app/<token>/ path has died and
// ask the app's BE for a fresh one, instead of leaving an iframe or an
// <audio> element pointed at a dead token until the window is re-opened.
//
// A token dies under a FE that is still mounted: a wash-login restart
// (the /app/ request then fails login's cookie check → 401), or the
// publishing BE / service being replaced so the router no longer knows the
// token (→ 410 "unknown or expired ingress", internal/router/ingress.go).
// Neither is visible to the consumer: an iframe can't read its document's
// HTTP status, and a media element only reports an opaque error. So we
// probe instead — a same-origin HEAD of the ingress base — and classify the
// status. Probes run on a cue, never on a timer:
//   - the shell link coming back up after a drop (window.wash.onConnState),
//   - the consumer saying "something looked wrong" (iframe load, media
//     error) via check().
//
// A dead verdict calls the app's own re-ensure path (vscode's `ensure`,
// music's rescan, radio/washamp's list request), which hands back a fresh
// base; the app re-points its consumer at it. A follow-up probe is
// scheduled with backoff to confirm, re-asking up to MAX_HEAL_ATTEMPTS
// times per episode, then giving up until the next reconnect. We never
// ask while the link is down — the BE can't answer, and the reconnect cue
// will re-check anyway.
//
// The decision logic (verdictForStatus, healDelay, ingressBase and the
// createIngressHeal state machine with injected deps) is framework- and
// DOM-free so ingress-heal.test.ts can drive it under node --test.

import { washAssetUrl } from './assets.ts';

/** What a probe of the ingress base says about the token. */
export type IngressVerdict = 'alive' | 'dead' | 'unknown';

// verdictForStatus maps a probe's HTTP status to a verdict. 401 (login
// rejected the request) and 410 (the router doesn't know the token) mean
// the token is unusable and a fresh one is needed. null — the fetch
// itself failed or timed out — says nothing about the token (the link or
// the host is down), so it is never grounds to re-mint. Anything else,
// including the backend's own 404/5xx, means the route still resolves.
export function verdictForStatus(status: number | null): IngressVerdict {
  if (status == null || status === 0) return 'unknown';
  if (status === 401 || status === 410) return 'dead';
  return 'alive';
}

/** Re-ensure attempts per heal episode before giving up. */
export const MAX_HEAL_ATTEMPTS = 4;

// healDelay is the wait (ms) before re-probing after the attempt-th
// re-ensure (0-based): 1s, 2s, 4s, … capped at 15s — long enough for a
// cold code-server launch to finish publishing before we ask again.
export function healDelay(attempt: number): number {
  return Math.min(15_000, 1_000 * 2 ** Math.max(0, attempt));
}

// ingressBase extracts the "/app/<token>/" base from any URL or path
// under it ("/app/t/?folder=…", "/wash/app/t/lib/a.mp3",
// "https://h/app/t/stream?i=1"). Returns '' when there is none — e.g. a
// washamp stream track, which is an absolute upstream URL.
export function ingressBase(url: string | null | undefined): string {
  if (!url) return '';
  const m = /\/app\/([^/?#]+)\//.exec(url);
  return m ? `/app/${m[1]}/` : '';
}

/** App-side hooks for createIngressHeal. */
export interface IngressHealOptions {
  /** The current ingress base (or any URL under it); '' = nothing to watch. */
  base: () => string;
  /** Ask the BE for a fresh path (the app's existing ensure/launch path). */
  reensure: () => void;
  /**
   * The base answered alive again after a dead verdict. sameBase is true
   * when the base never changed (the token recovered in place, e.g. a
   * transient 401), so a consumer still showing the failure — an iframe
   * error page — must reload itself; false when a fresh base arrived.
   */
  onRecovered?: (sameBase: boolean) => void;
}

/** Environment seams; defaults talk to fetch + window.wash + timers. */
export interface IngressHealDeps {
  /** HEAD the base path; resolve the HTTP status, or null on failure. */
  probe: (base: string) => Promise<number | null>;
  /** Is the shell link up right now? */
  linkUp: () => boolean;
  /** Subscribe to "the link came back up after a drop". Returns unsubscribe. */
  onReconnect: (cb: () => void) => () => void;
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (h: unknown) => void;
  log?: (msg: string) => void;
}

export interface IngressHeal {
  /** Probe now (coalesced with an in-flight probe). */
  check: () => void;
  dispose: () => void;
}

// createIngressHeal wires the probe → re-ensure → confirm loop for one
// consumer. deps is overridable for tests; production code passes only
// opts.
export function createIngressHeal(opts: IngressHealOptions, deps: IngressHealDeps = defaultDeps()): IngressHeal {
  let disposed = false;
  let inflight = false;
  let again = false; // a check() arrived mid-probe: re-run once it lands
  let attempts = 0; // re-ensures this episode
  let deadBase = ''; // base of the last dead verdict ('' = healthy)
  let gaveUp = false;
  let timer: unknown = null;

  const clearTimer = () => {
    if (timer != null) deps.clearTimeout(timer);
    timer = null;
  };

  const check = (): void => {
    if (disposed) return;
    if (inflight) {
      again = true;
      return;
    }
    const base = ingressBase(opts.base());
    // Nothing to watch, or the link is down — the reconnect cue re-checks.
    if (!base || !deps.linkUp()) return;
    inflight = true;
    void deps.probe(base).then(
      (status) => land(base, status),
      () => land(base, null),
    );
  };

  const land = (base: string, status: number | null): void => {
    inflight = false;
    if (disposed) return;
    const rerun = again;
    again = false;
    // The app swapped bases mid-probe: the answer is about a path we no
    // longer care about, so look at the current one instead.
    if (ingressBase(opts.base()) !== base) {
      check();
      return;
    }
    const verdict = verdictForStatus(status);
    if (verdict === 'unknown') {
      // A probe that failed across a link drop says nothing; if a cue
      // (the reconnect) arrived meanwhile, honour it with a fresh probe.
      if (rerun) check();
      return;
    }
    if (verdict === 'alive') {
      clearTimer();
      if (deadBase) {
        const same = deadBase === base;
        deadBase = '';
        attempts = 0;
        gaveUp = false;
        deps.log?.(`ingress ${base} healthy again${same ? '' : ' (fresh token)'}`);
        opts.onRecovered?.(same);
      }
      return;
    }
    // Dead.
    deadBase = base;
    if (attempts >= MAX_HEAL_ATTEMPTS) {
      if (!gaveUp) deps.log?.(`ingress ${base} still dead after ${attempts} re-ensures; giving up until reconnect`);
      gaveUp = true;
      return;
    }
    // Never re-mint while the link is down; the reconnect cue resumes.
    if (!deps.linkUp()) return;
    deps.log?.(`ingress ${base} answered ${status}; re-ensuring (attempt ${attempts + 1})`);
    opts.reensure();
    clearTimer();
    timer = deps.setTimeout(() => {
      timer = null;
      check();
    }, healDelay(attempts));
    attempts++;
  };

  // A reconnect is a fresh episode: the world changed under us, so a
  // previous give-up no longer applies.
  const offReconnect = deps.onReconnect(() => {
    attempts = 0;
    gaveUp = false;
    check();
  });

  return {
    check,
    dispose() {
      disposed = true;
      clearTimer();
      offReconnect();
    },
  };
}

// probeIngress HEADs the base on the shell's origin. redirect:'manual'
// keeps it to one hop — the dead answers (login's 401, the router's 410)
// are never redirects, and a backend redirect means the route is alive.
// Times out so a wedged backend can't hold the probe forever.
export async function probeIngress(base: string, timeoutMs = 5_000): Promise<number | null> {
  const ac = new AbortController();
  const t = setTimeout(() => ac.abort(), timeoutMs);
  try {
    const res = await fetch(washAssetUrl(base.replace(/^\//, '')), {
      method: 'HEAD',
      credentials: 'same-origin',
      cache: 'no-store',
      redirect: 'manual',
      signal: ac.signal,
    });
    return res.type === 'opaqueredirect' ? 302 : res.status;
  } catch {
    return null;
  } finally {
    clearTimeout(t);
  }
}

// defaultDeps binds the controller to the real shell. Older shells (and
// test harnesses) without connState/onConnState read as "always up, never
// reconnects" — the consumer-driven check() cues still work.
function defaultDeps(): IngressHealDeps {
  const wash = typeof window !== 'undefined' ? window.wash : undefined;
  return {
    probe: (base) => probeIngress(base),
    linkUp: () => (wash?.connState?.() ?? 'open') === 'open',
    onReconnect: (cb) => {
      let down = false;
      const off = wash?.onConnState?.((s) => {
        if (s !== 'open') down = true;
        else if (down) {
          down = false;
          cb();
        }
      });
      return off ?? (() => {});
    },
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
    log: (msg) => wash?.log?.('info', 'ingress-heal', msg),
  };
}
