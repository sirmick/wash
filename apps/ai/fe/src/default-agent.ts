// defaultAgent picks the launcher's preselected adapter (docs/AGENT_UX.md
// N5a): the form should open ready to go, not on "Choose…" with a silent
// submit-time fallback that prefers whatever happens to sit first in
// agentd's table (which is codex — an accident of ordering, not a
// preference anyone chose).
//
// Preference order: the agent you used last if it is still installed
// (N5b), else `claude` if installed, else the first available adapter in
// the order agentd published them, else '' (nothing installed — the form
// stays on "Choose…", whose unavailable rows carry the how-to-install
// note).
//
// N5b needs no new persistence. agentd already keeps a per-user session
// history on disk and publishes it newest-first as `recent`, with the
// agent and directory of each — so "what did I use last" is a read of
// state that already survives restarts, rather than a second store that
// could disagree with it.
//
// Pure decision kernel on purpose — the wiring in main.tsx applies it
// once, so a user who deliberately re-selects "Choose…" is not fought by
// the next roster push.

export interface AdapterChoice {
  id: string;
  available: boolean;
}

/** One row of agentd's `recent` list; newest first. */
export interface RecentChoice {
  agent?: string;
  cwd?: string;
}

export const PREFERRED_AGENT = 'claude';

export function defaultAgent(adapters: AdapterChoice[], recent: RecentChoice[] = []): string {
  const usable = adapters.filter((a) => a.available);
  if (usable.length === 0) return '';
  const has = (id: string) => usable.find((a) => a.id === id)?.id;
  // An agent that was uninstalled since it was last used must not win —
  // the form would open on a row that cannot start anything.
  for (const r of recent) {
    if (r.agent) {
      const hit = has(r.agent);
      if (hit) return hit;
    }
  }
  return has(PREFERRED_AGENT) ?? usable[0].id;
}

/**
 * defaultCwd is the folder the launcher opens on: where you were working
 * last. '' means "leave it empty", which the form renders as Home — the
 * right answer on a machine with no history rather than a guess.
 */
export function defaultCwd(recent: RecentChoice[] = []): string {
  for (const r of recent) {
    if (r.cwd) return r.cwd;
  }
  return '';
}
