// Pre-flight health check, run once before the Playwright suite.
//
// The fm/edit watch tests rely on fs.watch (inotify). inotify instances
// are a PER-USER kernel resource capped at fs.inotify.max_user_instances
// (commonly 128). When an interrupted test run leaks routers/apps, their
// inotify instances survive — and once the cap is hit, NEW fs.watch calls
// fail *silently*, which surfaces downstream as a confusing "the tree
// never refreshed" timeout in an unrelated spec (see the project memory
// feedback_e2e_orphan_accumulation).
//
// This converts that silent, misattributed failure into a loud, early one
// with a remediation hint. It keys on inotify HEADROOM — the resource that
// actually runs out — not on "are there wash processes running", because a
// dev box legitimately runs a live wash router. So it won't false-positive
// on normal local development; it only fires when the instance budget is
// genuinely (near-)exhausted, whoever is holding it.

import { readdirSync, readFileSync, readlinkSync } from 'node:fs';

// Count inotify instances currently held by `uid` by walking /proc and
// counting fds that point at an anon_inode:inotify object. This is the
// same approach `inotify-info` and similar tools use; the kernel doesn't
// expose the per-user counter directly.
function inotifyInstancesForUid(uid: number): number {
  let count = 0;
  let pids: string[];
  try {
    pids = readdirSync('/proc');
  } catch {
    return 0;
  }
  for (const pid of pids) {
    if (!/^\d+$/.test(pid)) continue;
    // Skip processes we don't own (fd dir is unreadable anyway).
    try {
      const status = readFileSync(`/proc/${pid}/status`, 'utf8');
      const m = status.match(/^Uid:\s+(\d+)/m);
      if (!m || parseInt(m[1], 10) !== uid) continue;
    } catch {
      continue;
    }
    let fds: string[];
    try {
      fds = readdirSync(`/proc/${pid}/fd`);
    } catch {
      continue; // raced exit / permission
    }
    for (const fd of fds) {
      try {
        if (readlinkSync(`/proc/${pid}/fd/${fd}`) === 'anon_inode:inotify') count++;
      } catch {
        /* fd closed under us */
      }
    }
  }
  return count;
}

function readIntFile(path: string): number | null {
  try {
    const n = parseInt(readFileSync(path, 'utf8').trim(), 10);
    return Number.isFinite(n) ? n : null;
  } catch {
    return null;
  }
}

export default function globalSetup() {
  // /proc + per-uid limits are Linux-only; elsewhere this is a no-op.
  if (process.platform !== 'linux' || typeof process.getuid !== 'function') return;

  const uid = process.getuid();
  const max = readIntFile('/proc/sys/fs/inotify/max_user_instances');
  if (max === null) return; // can't assess — don't block the run

  const used = inotifyInstancesForUid(uid);
  const free = max - used;

  // Each worker spawns a router + ~5-7 BE apps; fm/edit/watch specs each
  // establish several watches. Reserve a comfortable budget so the suite
  // doesn't itself tip over the cap mid-run.
  const HARD_MIN_FREE = 16; // below this the suite WILL hit the cap → abort
  const SOFT_MIN_FREE = 48; // below this, warn — likely a partial leak

  const summary = `inotify instances: ${used}/${max} used (${free} free) for uid ${uid}`;
  if (free < HARD_MIN_FREE) {
    throw new Error(
      `e2e pre-flight: ${summary}.\n` +
        `Too few inotify instances free — fs.watch will fail SILENTLY mid-run and\n` +
        `manifest as unrelated "tree never refreshed" timeouts.\n` +
        `Likely leaked watchers from an interrupted run. Remediate:\n` +
        `  pkill -f 'wash-router|wash-fm|wash-edit|wash-session'   # reap orphans\n` +
        `  # or raise the cap:  sudo sysctl fs.inotify.max_user_instances=512\n` +
        `(see project memory: feedback_e2e_orphan_accumulation)`,
    );
  }
  if (free < SOFT_MIN_FREE) {
    // eslint-disable-next-line no-console
    console.warn(`⚠ e2e pre-flight: ${summary} — low headroom; watch out for fs.watch flakes.`);
  }
}
