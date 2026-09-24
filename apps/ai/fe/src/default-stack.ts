// defaultStack picks the launcher's preselected stack (docs/AGENT_UX.md
// N5a/N5b): the form should open ready to go, on the stack you used last if
// it can still start here, else the first one that can, else '' (nothing
// can start; the greyed rows carry the reasons).
//
// "Used last" needs no new persistence. agentd keeps a per-user session
// history on disk and publishes it newest-first as `recent`, and each entry
// names the stack it was started from — so this is a read of state that
// already survives restarts, not a second store that could disagree with it.
//
// Pure decision kernel on purpose — main.tsx applies it once, so a user who
// deliberately picks something else is not fought by the next roster push.

/** agentd's `recent` list is newest first. */
import type { agentproto } from '@wash/ui';

export function defaultStack(
  stacks: Pick<agentproto.StackView, 'id' | 'available'>[],
  recent: Pick<agentproto.Session, 'stack' | 'cwd'>[] = [],
): string {
  const usable = stacks.filter((s) => s.available);
  if (usable.length === 0) return '';
  // A stack that can no longer start (its key cleared, its adapter
  // uninstalled) must not win: the form would open on a row that fails.
  for (const r of recent) {
    const hit = r.stack && usable.find((s) => s.id === r.stack);
    if (hit) return hit.id;
  }
  return usable[0].id;
}

/**
 * defaultCwd is the folder the launcher opens on: where you were working
 * last. '' means "leave it empty", which the form renders as Home — the
 * right answer on a machine with no history rather than a guess.
 */
export function defaultCwd(recent: Pick<agentproto.Session, 'stack' | 'cwd'>[] = []): string {
  for (const r of recent) {
    if (r.cwd) return r.cwd;
  }
  return '';
}
