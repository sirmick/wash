// defaultAgent picks the launcher's preselected adapter (docs/AGENT_UX.md
// N5a): the form should open ready to go, not on "Choose…" with a silent
// submit-time fallback that prefers whatever happens to sit first in
// agentd's table (which is codex — an accident of ordering, not a
// preference anyone chose).
//
// Preference order: `claude` if installed, else the first available
// adapter in the order agentd published them, else '' (nothing installed —
// the form stays on "Choose…", whose unavailable rows carry the
// how-to-install note).
//
// Pure decision kernel on purpose — the wiring in main.tsx applies it
// once, so a user who deliberately re-selects "Choose…" is not fought by
// the next roster push. N5b (remember last-used, BE-persisted) will feed
// its answer in ahead of this static preference.

export interface AdapterChoice {
  id: string;
  available: boolean;
}

export const PREFERRED_AGENT = 'claude';

export function defaultAgent(adapters: AdapterChoice[]): string {
  const usable = adapters.filter((a) => a.available);
  if (usable.length === 0) return '';
  return usable.find((a) => a.id === PREFERRED_AGENT)?.id ?? usable[0].id;
}
