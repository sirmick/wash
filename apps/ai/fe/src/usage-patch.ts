import type { RosterRow } from '@wash/ui';

export interface UsageRosterState {
  rows?: RosterRow[];
}

interface UsagePatchRow {
  key?: unknown;
  used?: unknown;
  size?: unknown;
}

// applyUsagePatch changes only the counters named by a compact agentd patch.
// Missing rows are intentionally ignored: a later canonical roster snapshot
// creates/removes rows and remains authoritative for their structure.
export function applyUsagePatch<T extends UsageRosterState>(state: T, raw: unknown): T {
  const updates = Array.isArray(raw) ? (raw as UsagePatchRow[]) : [];
  const byKey = new Map(updates.map((u) => [String(u.key ?? ''), u]));
  return {
    ...state,
    rows: (state.rows ?? []).map((row) => {
      const update = byKey.get(row.key);
      if (!update) return row;
      return {
        ...row,
        used: Number(update.used ?? 0),
        size: Number(update.size ?? 0),
      };
    }),
  };
}
