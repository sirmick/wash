// defaultCatalog picks the launcher's preselected catalog (docs/AGENT_UX.md
// N5a/N5b): the form should open ready to go, on the catalog you used last
// if it can still start here, else the first one that can, else '' (nothing
// can start; the greyed rows carry the reasons).
//
// "Used last" needs no new persistence. agentd keeps a per-user session
// history on disk and publishes it newest-first as `recent`, and each entry
// names the catalog it was started from — so this is a read of state that
// already survives restarts, not a second store that could disagree with it.
//
// Pure decision kernel on purpose — main.tsx applies it once, so a user who
// deliberately picks something else is not fought by the next roster push.

/** agentd's `recent` list is newest first. */
import type { agentproto } from '@wash/ui';

export function defaultCatalog(
  catalogs: Pick<agentproto.CatalogView, 'id' | 'available'>[],
  recent: Pick<agentproto.Session, 'catalog' | 'cwd'>[] = [],
  pref = '',
): string {
  const usable = catalogs.filter((c) => c.available);
  if (usable.length === 0) return '';
  // An explicitly chosen default beats history: the person said what
  // "start an agent" means, and the launcher should open on it rather
  // than on whatever they happened to run last (docs/PLACES.md §4.5).
  // Still subject to the availability check below — a default whose key
  // was since cleared must not preselect a row that fails.
  if (pref) {
    const hit = usable.find((c) => c.id === pref);
    if (hit) return hit.id;
  }
  // A catalog that can no longer start (its key cleared, its adapter
  // uninstalled) must not win: the form would open on a row that fails.
  for (const r of recent) {
    const hit = r.catalog && usable.find((c) => c.id === r.catalog);
    if (hit) return hit.id;
  }
  return usable[0].id;
}

/**
 * defaultCwd is the folder the launcher opens on: where you were working
 * last. '' means "leave it empty", which the form renders as Home — the
 * right answer on a machine with no history rather than a guess.
 */
export function defaultCwd(recent: Pick<agentproto.Session, 'catalog' | 'cwd'>[] = []): string {
  for (const r of recent) {
    if (r.cwd) return r.cwd;
  }
  return '';
}
