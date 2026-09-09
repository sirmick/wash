// Pure launcher decisions for the session chrome: how the start menu and
// the Ctrl+Space palette fold the BE's launcher.state (recent files + pinned
// apps) in with the app catalog. No DOM, no Solid — `node --test`-able.

/** One recent-file record as the session BE ships it (launcher.state). */
export interface RecentEntry {
  path: string;
  app_id: string;
  /** unix ms */
  at: number;
}

/** The subset of a catalog row the launcher logic needs. */
export interface LauncherApp {
  id: string;
  name: string;
  icon?: string;
  disabled?: boolean;
}

/** Palette / menu rows that stand for a recent file carry this id prefix. */
export const RECENT_PREFIX = 'recent:';

export function recentRowID(path: string): string {
  return RECENT_PREFIX + path;
}

/** The path behind a recent row id, or null for a plain app id. */
export function recentPathOf(id: string): string | null {
  return id.startsWith(RECENT_PREFIX) ? id.slice(RECENT_PREFIX.length) : null;
}

/** Display name for a recent path: the last path segment ("/" → "/"). */
export function recentName(path: string): string {
  const trimmed = path.replace(/\/+$/, '');
  if (trimmed === '') return path || '/';
  const i = trimmed.lastIndexOf('/');
  return i < 0 ? trimmed : trimmed.slice(i + 1);
}

/** Parent directory shown as the row's subtitle ("/home/u/notes.md" → "/home/u"). */
export function recentDir(path: string): string {
  const trimmed = path.replace(/\/+$/, '');
  const i = trimmed.lastIndexOf('/');
  if (i <= 0) return '/';
  return trimmed.slice(0, i);
}

/** recentMatches filters recent entries by a lowercase query against the
 * full path (so "notes", "home/u" and "md" all hit). Empty query → all. */
export function recentMatches(recent: ReadonlyArray<RecentEntry>, query: string): RecentEntry[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...recent];
  return recent.filter((r) => r.path.toLowerCase().includes(q));
}

/** appMatches is the catalog filter the palette already applied: id or name. */
export function appMatches<T extends LauncherApp>(apps: ReadonlyArray<T>, query: string): T[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...apps];
  return apps.filter((a) => a.id.toLowerCase().includes(q) || a.name.toLowerCase().includes(q));
}

/** pinnedRows resolves pinned ids against the catalog, in pin order,
 * skipping ids that are no longer registered (an uninstalled app must not
 * leave a dead row). */
export function pinnedRows<T extends LauncherApp>(apps: ReadonlyArray<T>, pinned: ReadonlyArray<string>): T[] {
  const out: T[] = [];
  for (const id of pinned) {
    const app = apps.find((a) => a.id === id);
    if (app) out.push(app);
  }
  return out;
}

/** Palette entry: an app row, or a recent-file row dressed as one so the
 * existing row component renders it (name = file name, subtitle = dir). */
export interface PaletteEntry {
  id: string;
  name: string;
  icon?: string;
  disabled?: boolean;
  subtitle?: string;
  recent?: RecentEntry;
}

/** paletteEntries builds the palette's result list: matching apps sorted by
 * name (root rows mixed in by the caller), then matching recent files
 * newest-first. With no query, recent files are listed too — they are the
 * point of the palette's memory — but capped so the app list stays the
 * first screen. */
export function paletteEntries(
  apps: ReadonlyArray<LauncherApp>,
  recent: ReadonlyArray<RecentEntry>,
  query: string,
  recentCapWhenEmpty = 5,
): PaletteEntry[] {
  const matchedApps = appMatches(apps, query);
  matchedApps.sort((a, b) => a.name.localeCompare(b.name));
  let matchedRecent = recentMatches(recent, query);
  if (!query.trim()) matchedRecent = matchedRecent.slice(0, recentCapWhenEmpty);
  const out: PaletteEntry[] = matchedApps.map((a) => ({ id: a.id, name: a.name, icon: a.icon, disabled: a.disabled }));
  for (const r of matchedRecent) {
    out.push({ id: recentRowID(r.path), name: recentName(r.path), icon: 'file-text', subtitle: recentDir(r.path), recent: r });
  }
  return out;
}

/** Keyboard model for a vertical list of `count` rows: Arrow keys move with
 * wraparound, Home/End jump. Returns the next index, or null when the key
 * is not a navigation key. */
export function stepSelection(key: string, current: number, count: number): number | null {
  if (count <= 0) return null;
  switch (key) {
    case 'ArrowDown':
      return (current + 1) % count;
    case 'ArrowUp':
      return (current - 1 + count) % count;
    case 'Home':
      return 0;
    case 'End':
      return count - 1;
  }
  return null;
}
