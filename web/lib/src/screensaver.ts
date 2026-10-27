// Screensavers: a full-viewport animation that starts after the user has been
// idle for a while, and goes away on the next key, click or mouse move.
//
// Pieces, all framework-free:
//   - SCREENSAVERS: the registry (id + name). The effects themselves are NOT
//     bundled here. Each is a standalone ES module built by web/screensavers
//     into the shell assets (`screensavers/<id>.js`, thumbnail
//     `screensavers/<id>.jpg`) and loaded on demand by loadSaver() over
//     window.wash.fetchAsset. That is the router's asset.read channel, so it
//     works in the in-browser VM too, where a plain HTTP import can't.
//   - The module contract (SaverModule): `options` describes its settings so
//     Settings can draw the controls without knowing each effect, and
//     `mount(container, opts)` starts it and returns a handle to dispose.
//   - createIdleTimer: the idle kernel, clock injected (unit-tested).
//   - showScreensaver: the overlay. It covers everything (above wash-display's
//     popup canvases), and dismisses on input without letting that input reach
//     the window underneath. Settings' Preview calls it directly.
//   - startScreensaverHost: idle timer + activity listeners + overlay, driven
//     by the `screensaver` block of desktop.json. The session app runs one
//     per browser tab: each tab goes idle on its own.
//
// Config lives in desktop.json as `screensaver` (see ScreensaverConfig). The
// session BE passes the block through verbatim; normalizeScreensaver() is the
// one place that applies defaults and clamps.

// ---- module contract ------------------------------------------------------

export type SaverOption =
  | { key: string; label: string; kind: 'range'; min: number; max: number; step?: number; default: number }
  | { key: string; label: string; kind: 'select'; choices: [string, string][]; default: string }
  | { key: string; label: string; kind: 'toggle'; default: boolean };

export interface SaverHandle {
  dispose(): void;
}

export interface SaverModule {
  options: SaverOption[];
  /** Start the effect inside `container` (which it fills). Throws if the
   *  browser can't run it (no WebGL2); the overlay then stays plain black. */
  mount(container: HTMLElement, opts: Record<string, unknown>): SaverHandle;
}

// ---- registry --------------------------------------------------------------

export interface SaverDesc {
  /** Stable id stored in desktop.json; also the asset name. */
  id: string;
  name: string;
  /** False for the built-in blank screen: no module, no thumbnail. */
  module: boolean;
}

export const SCREENSAVERS: SaverDesc[] = [
  { id: 'logowriter3d', name: 'LogoWriter 3D', module: true },
  { id: 'harbor', name: 'Harbor Night', module: true },
  { id: 'moire-interference', name: 'Moiré Interference', module: true },
  { id: 'moire-glow', name: 'Moiré Glow', module: true },
  { id: 'blank', name: 'Blank', module: false },
];

/** 'random' picks one of the animated savers each time it starts. */
export const RANDOM_SAVER = 'random';

export function getSaver(id: string): SaverDesc | undefined {
  return SCREENSAVERS.find((s) => s.id === id);
}

export function saverThumbnailPath(id: string): string {
  return `screensavers/${id}.jpg`;
}

const loaded = new Map<string, Promise<SaverModule>>();

/**
 * loadSaver fetches `screensavers/<id>.js` over the asset channel and imports
 * it from a blob URL. Cached per id; a failed load is forgotten so the next
 * start retries.
 */
export function loadSaver(id: string): Promise<SaverModule> {
  let p = loaded.get(id);
  if (!p) {
    p = (async () => {
      const desc = getSaver(id);
      if (!desc?.module) throw new Error(`screensaver ${id}: no module`);
      const a = await window.wash.fetchAsset(`screensavers/${id}.js`);
      const url = URL.createObjectURL(new Blob([a.bytes], { type: 'text/javascript' }));
      try {
        return (await import(/* @vite-ignore */ url)) as SaverModule;
      } finally {
        URL.revokeObjectURL(url);
      }
    })();
    p.catch(() => loaded.delete(id));
    loaded.set(id, p);
  }
  return p;
}

// ---- config ----------------------------------------------------------------

export interface ScreensaverConfig {
  enabled: boolean;
  /** Idle seconds before it starts. */
  timeout_s: number;
  /** A saver id, or RANDOM_SAVER. */
  saver: string;
  /** Per-saver option values, keyed by saver id then option key. */
  options: Record<string, Record<string, unknown>>;
}

export const SCREENSAVER_DEFAULTS: ScreensaverConfig = {
  enabled: true,
  timeout_s: 600,
  saver: 'harbor',
  options: {},
};

/** The shortest timeout honoured. Settings offers 1 minute and up; anything
 *  lower is for tests. */
export const MIN_TIMEOUT_S = 1;

export function normalizeScreensaver(raw: unknown): ScreensaverConfig {
  const r = (raw && typeof raw === 'object' ? raw : {}) as Partial<Record<keyof ScreensaverConfig, unknown>>;
  const t = typeof r.timeout_s === 'number' && Number.isFinite(r.timeout_s) ? r.timeout_s : SCREENSAVER_DEFAULTS.timeout_s;
  const saver = typeof r.saver === 'string' && (r.saver === RANDOM_SAVER || getSaver(r.saver)) ? r.saver : SCREENSAVER_DEFAULTS.saver;
  const options: ScreensaverConfig['options'] = {};
  if (r.options && typeof r.options === 'object') {
    for (const [id, v] of Object.entries(r.options as Record<string, unknown>)) {
      if (v && typeof v === 'object' && !Array.isArray(v)) options[id] = { ...(v as Record<string, unknown>) };
    }
  }
  return { enabled: typeof r.enabled === 'boolean' ? r.enabled : SCREENSAVER_DEFAULTS.enabled, timeout_s: Math.max(MIN_TIMEOUT_S, Math.round(t)), saver, options };
}

/** resolveSaver turns the configured choice into a concrete saver id. */
export function resolveSaver(saver: string, rand: () => number = Math.random): string {
  if (saver !== RANDOM_SAVER) return getSaver(saver) ? saver : 'blank';
  const pool = SCREENSAVERS.filter((s) => s.module);
  return pool[Math.min(pool.length - 1, Math.floor(rand() * pool.length))].id;
}

/** optionValues fills a saver's stored values with its option defaults,
 *  dropping keys it doesn't declare and values of the wrong type. */
export function optionValues(specs: SaverOption[], stored: Record<string, unknown> | undefined): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const o of specs) {
    const v = stored?.[o.key];
    if (o.kind === 'range') {
      out[o.key] = typeof v === 'number' && Number.isFinite(v) ? Math.min(o.max, Math.max(o.min, v)) : o.default;
    } else if (o.kind === 'select') {
      out[o.key] = typeof v === 'string' && o.choices.some(([c]) => c === v) ? v : o.default;
    } else {
      out[o.key] = typeof v === 'boolean' ? v : o.default;
    }
  }
  return out;
}

// ---- idle kernel -----------------------------------------------------------

export interface IdleClock {
  now(): number;
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(id: unknown): void;
}

const realClock: IdleClock = {
  now: () => Date.now(),
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
};

export interface IdleTimer {
  /** Note user activity: the countdown restarts. */
  activity(): void;
  /** Change the timeout (ms; 0 disables) and restart the countdown. */
  setTimeoutMs(ms: number): void;
  stop(): void;
  readonly idle: boolean;
}

/**
 * createIdleTimer calls onIdle once the user has been inactive for timeoutMs,
 * then not again until there has been activity. Activity is cheap: it only
 * records a timestamp. A single pending timer re-checks the deadline when it
 * fires, so a stream of mouse moves never re-arms timers. `canIdle` can veto
 * going idle (e.g. the tab is hidden); the timer then checks again after
 * another full timeout.
 */
export function createIdleTimer(opts: {
  timeoutMs: number;
  onIdle: () => void;
  canIdle?: () => boolean;
  clock?: IdleClock;
}): IdleTimer {
  const clock = opts.clock ?? realClock;
  let timeoutMs = opts.timeoutMs;
  let last = clock.now();
  let timer: unknown = null;
  let idle = false;
  let stopped = false;

  const disarm = () => {
    if (timer !== null) clock.clearTimeout(timer);
    timer = null;
  };
  const arm = (ms: number) => {
    disarm();
    if (stopped || timeoutMs <= 0) return;
    timer = clock.setTimeout(check, Math.max(0, ms));
  };
  const check = () => {
    timer = null;
    const left = last + timeoutMs - clock.now();
    if (left > 0) return arm(left);
    if (opts.canIdle && !opts.canIdle()) {
      last = clock.now();
      return arm(timeoutMs);
    }
    idle = true;
    opts.onIdle();
  };

  arm(timeoutMs);
  return {
    activity() {
      last = clock.now();
      if (idle) {
        idle = false;
        arm(timeoutMs);
      } else if (timer === null) {
        arm(timeoutMs);
      }
    },
    setTimeoutMs(ms) {
      timeoutMs = ms;
      last = clock.now();
      idle = false;
      arm(timeoutMs);
    },
    stop() {
      stopped = true;
      disarm();
    },
    get idle() {
      return idle;
    },
  };
}

// ---- activity --------------------------------------------------------------

/** The event other code dispatches on `window` to report activity the
 *  window can't see itself (input inside a same-origin iframe). */
export const ACTIVITY_EVENT = 'wash-activity';

export function noteUserActivity(): void {
  if (typeof window !== 'undefined') window.dispatchEvent(new Event(ACTIVITY_EVENT));
}

const ACTIVITY_INPUTS = ['keydown', 'pointerdown', 'pointermove', 'wheel', 'touchstart'] as const;

/** watchActivity calls `fn` on any user input reaching `win` (capture phase,
 *  so a handler that stops propagation can't hide it). Returns cleanup. */
export function watchActivity(win: Window, fn: () => void): () => void {
  const opts = { capture: true, passive: true } as const;
  for (const t of ACTIVITY_INPUTS) win.addEventListener(t, fn, opts);
  win.addEventListener(ACTIVITY_EVENT, fn);
  return () => {
    for (const t of ACTIVITY_INPUTS) win.removeEventListener(t, fn, opts);
    win.removeEventListener(ACTIVITY_EVENT, fn);
  };
}

// ---- overlay ---------------------------------------------------------------

/** Above everything in the page, including wash-display popup canvases
 *  (2147483646). */
export const SCREENSAVER_Z = 2147483647;
/** Mouse motion is ignored for this long after the overlay appears (a
 *  Preview click, a hand resting on the mouse), and must travel this far. */
export const WAKE_GRACE_MS = 600;
export const WAKE_MOVE_PX = 10;

export interface ScreensaverView {
  readonly el: HTMLElement;
  dismiss(): void;
}

/**
 * showScreensaver covers the page with saver `id` and returns once the
 * overlay is up (the effect itself loads in the background). Any key, click,
 * wheel or real mouse move dismisses it. The waking input is swallowed: the
 * keydown (and its keyup), or the pointerdown (and the pointerup / click /
 * contextmenu that follow), never reach the page.
 */
export function showScreensaver(
  id: string,
  opts: Record<string, unknown> | undefined,
  onDismiss?: () => void,
): ScreensaverView {
  const doc = document;
  const win = doc.defaultView ?? window;
  const el = doc.createElement('div');
  el.dataset.testid = 'screensaver';
  el.dataset.saver = id;
  el.setAttribute('aria-hidden', 'true');
  Object.assign(el.style, {
    position: 'fixed',
    inset: '0',
    zIndex: String(SCREENSAVER_Z),
    background: '#000',
    cursor: 'none',
    opacity: '0',
    transition: 'opacity 0.8s ease',
    overflow: 'hidden',
  } satisfies Partial<CSSStyleDeclaration>);
  doc.body.appendChild(el);
  // Fade in on the next frame (a style change in the same frame as the
  // insert doesn't transition).
  win.requestAnimationFrame?.(() => (el.style.opacity = '1'));

  // Take focus off whatever had it, so a focused iframe (an ingress app)
  // doesn't keep receiving keys the overlay never sees.
  const prevFocus = doc.activeElement as HTMLElement | null;
  prevFocus?.blur?.();

  let handle: SaverHandle | null = null;
  let done = false;
  const desc = getSaver(id);
  if (desc?.module) {
    loadSaver(id)
      .then((m) => {
        if (done) return;
        handle = m.mount(el, optionValues(m.options, opts));
      })
      .catch((e) => console.warn(`screensaver ${id}:`, e));
  }

  const shownAt = performance.now();
  let origin: { x: number; y: number } | null = null;

  // Swallow the rest of the waking gesture after the overlay is gone.
  const swallowAfter = (types: string[]) => {
    const stop = (ev: Event) => {
      ev.preventDefault();
      ev.stopImmediatePropagation();
    };
    for (const t of types) win.addEventListener(t, stop, { capture: true, once: true });
    win.setTimeout(() => {
      for (const t of types) win.removeEventListener(t, stop, { capture: true });
    }, 1000);
  };

  const swallow = (ev: Event) => {
    ev.preventDefault();
    ev.stopImmediatePropagation();
  };
  const onKey = (ev: KeyboardEvent) => {
    swallow(ev);
    swallowAfter(['keyup']);
    dismiss();
  };
  const onDown = (ev: Event) => {
    swallow(ev);
    swallowAfter(['pointerup', 'mouseup', 'click', 'contextmenu', 'touchend']);
    dismiss();
  };
  const onWheel = (ev: Event) => {
    swallow(ev);
    dismiss();
  };
  const onMove = (ev: PointerEvent) => {
    swallow(ev);
    if (performance.now() - shownAt < WAKE_GRACE_MS) return;
    if (!origin) {
      origin = { x: ev.clientX, y: ev.clientY };
      return;
    }
    if (Math.hypot(ev.clientX - origin.x, ev.clientY - origin.y) >= WAKE_MOVE_PX) dismiss();
  };
  const cap = { capture: true } as const;
  const listeners: [string, EventListener][] = [
    ['keydown', onKey as EventListener],
    ['keypress', swallow],
    ['keyup', swallow],
    ['pointerdown', onDown],
    ['mousedown', onDown],
    ['touchstart', onDown],
    ['wheel', onWheel],
    ['pointermove', onMove as EventListener],
  ];
  for (const [t, fn] of listeners) win.addEventListener(t, fn, { ...cap, passive: false });

  function dismiss() {
    if (done) return;
    done = true;
    for (const [t, fn] of listeners) win.removeEventListener(t, fn, cap);
    try {
      handle?.dispose();
    } catch (e) {
      console.warn(`screensaver ${id}: dispose`, e);
    }
    handle = null;
    el.remove();
    if (prevFocus && prevFocus.isConnected) prevFocus.focus?.({ preventScroll: true });
    onDismiss?.();
  }

  return { el, dismiss };
}

// ---- host ------------------------------------------------------------------

export interface ScreensaverHost {
  /** Apply a (raw or normalized) desktop.json `screensaver` block. */
  configure(raw: unknown): void;
  readonly active: boolean;
  dispose(): void;
}

/**
 * startScreensaverHost runs the idle timer for this tab and shows the
 * configured saver when it fires. While the tab is hidden it never starts
 * (nobody is looking, and a hidden tab's rAF is throttled anyway).
 */
export function startScreensaverHost(win: Window = window): ScreensaverHost {
  let cfg: ScreensaverConfig = SCREENSAVER_DEFAULTS;
  let view: ScreensaverView | null = null;

  const show = (id: string, opts: Record<string, unknown> | undefined) => {
    if (view) return;
    view = showScreensaver(id, opts, () => {
      view = null;
      timer.activity();
    });
  };
  const timer = createIdleTimer({
    timeoutMs: 0,
    canIdle: () => !win.document.hidden && !view,
    onIdle: () => {
      if (!cfg.enabled) return;
      const id = resolveSaver(cfg.saver);
      show(id, cfg.options[id]);
    },
  });
  const unwatch = watchActivity(win, () => timer.activity());

  return {
    configure(raw) {
      cfg = normalizeScreensaver(raw);
      timer.setTimeoutMs(cfg.enabled ? cfg.timeout_s * 1000 : 0);
    },
    get active() {
      return view !== null;
    },
    dispose() {
      unwatch();
      timer.stop();
      view?.dismiss();
    },
  };
}
