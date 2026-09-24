import type { agentproto } from '@wash/ui';

export interface UsageRosterState {
  rows?: agentproto.Row[] | null;
}

// applyUsagePatch changes only the counters named by a compact agentd patch.
// Missing rows are intentionally ignored: a later canonical roster snapshot
// creates/removes rows and remains authoritative for their structure.
export function applyUsagePatch<T extends UsageRosterState>(state: T, patch: agentproto.UsagePatch): T {
  const byKey = new Map((patch.rows ?? []).map((u) => [u.key, u]));
  return {
    ...state,
    rows: (state.rows ?? []).map((row) => {
      const update = byKey.get(row.key);
      if (!update) return row;
      return { ...row, used: update.used, size: update.size };
    }),
  };
}
