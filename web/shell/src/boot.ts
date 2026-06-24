// Boot-splash bridge + launch busy-cursor coordination.
//
// The boot splash markup + its controller live in index.html (a static
// overlay painted before this module is even fetched, so a cold/slow
// connect shows feedback immediately). This module is the typed seam the
// shell uses to drive that controller — the step log on connect — and to
// toggle the "launching" busy cursor while a freshly-spawned window waits
// on its app bundle.

interface BootCtl {
  set(id: string, label: string | null, state?: 'active' | 'done' | 'fail'): void;
  finish(): void;
}

function ctl(): BootCtl | undefined {
  return (window as unknown as { __washBoot?: BootCtl }).__washBoot;
}

/** bootStep upserts a row in the boot-splash step log. No-op once the
 *  splash has been finished (or if the controller is absent, e.g. a test
 *  harness mounting the shell without index.html). */
export function bootStep(id: string, label: string | null, state?: 'active' | 'done' | 'fail'): void {
  ctl()?.set(id, label, state);
}

/** bootFinish fades out + removes the boot splash. Idempotent. */
export function bootFinish(): void {
  ctl()?.finish();
}

// Launch busy-cursor: a refcount of in-flight window mounts. While any
// window is waiting on its bundle, <html> carries .wash-launching, which
// index.html styles as `cursor: progress`. Cleared when the count hits 0.
let launching = 0;

export function beginWindowLoad(): void {
  launching++;
  if (launching === 1) document.documentElement.classList.add('wash-launching');
}

export function endWindowLoad(): void {
  launching = Math.max(0, launching - 1);
  if (launching === 0) document.documentElement.classList.remove('wash-launching');
}
