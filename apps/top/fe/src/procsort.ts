// Pure filter + sort for the process table, extracted from top's filteredFlat
// memo so the 6-column sort and the 4-field filter are unit-testable directly
// instead of only through the (slow) top e2e. Generic over the fields it
// reads so it stays decoupled from main.tsx's full ProcInfo.

export type SortKey = 'cpu' | 'mem' | 'pid' | 'user' | 'cmd' | 'time';

export interface SortableProc {
  PID: number;
  Comm: string;
  Cmd: string;
  User?: string;
  CPU: number;
  RSS: number;
  TimeJiff: number;
}

// Case-insensitive substring filter over cmd/comm/pid/user, then sort by the
// chosen column (desc flips direction). Returns a new array; inputs untouched.
export function filterSortProcs<T extends SortableProc>(
  rows: readonly T[],
  filter: string,
  key: SortKey,
  desc: boolean,
): T[] {
  const f = filter.trim().toLowerCase();
  let out: readonly T[] = rows;
  if (f) {
    out = out.filter(
      (p) =>
        p.Cmd.toLowerCase().includes(f) ||
        p.Comm.toLowerCase().includes(f) ||
        String(p.PID).includes(f) ||
        (p.User || '').toLowerCase().includes(f),
    );
  }
  const dir = desc ? -1 : 1;
  const cmp = (a: T, b: T): number => {
    switch (key) {
      case 'cpu':
        return (a.CPU - b.CPU) * dir;
      case 'mem':
        return (a.RSS - b.RSS) * dir;
      case 'pid':
        return (a.PID - b.PID) * dir;
      case 'user':
        return (a.User || '').localeCompare(b.User || '') * dir;
      case 'cmd':
        return (a.Cmd || '').localeCompare(b.Cmd || '') * dir;
      case 'time':
        return (a.TimeJiff - b.TimeJiff) * dir;
    }
  };
  return [...out].sort(cmp);
}
