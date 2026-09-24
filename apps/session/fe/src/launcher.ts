// Pure launcher decisions for the session chrome: how the start menu and
// the Ctrl+Space palette fold the BE's launcher.state (recent files + pinned
// apps) in with the app catalog. No DOM, no Solid — `node --test`-able.

/** One recent record as the session BE ships it (launcher.state). */
export interface RecentEntry {
  /** the file or folder; empty for a name entry */
  path: string;
  app_id: string;
  /** unix ms */
  at: number;
  /** a non-file item (a radio station), set instead of path */
  name?: string;
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

/** recentMatches filters recent FILE entries by a lowercase query against
 * the full path (so "notes", "home/u" and "md" all hit). Empty query → all.
 * Name entries (stations) have no path to open and are never matched.
 *
 * The store keeps a path once PER APP (a folder under Files and under
 * Terminal), but these flat lists are about paths: each is listed once, as
 * its newest entry, so a search does not show two identical rows. */
export function recentMatches(recent: ReadonlyArray<RecentEntry>, query: string): RecentEntry[] {
  const q = query.trim().toLowerCase();
  const seen = new Set<string>();
  const files: RecentEntry[] = [];
  for (const r of [...recent].sort((a, b) => b.at - a.at)) {
    if (!r.path || seen.has(r.path)) continue;
    seen.add(r.path);
    files.push(r);
  }
  if (!q) return files;
  return files.filter((r) => r.path.toLowerCase().includes(q));
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

// ---- start menu Recent flyouts ----
//
// The start menu's Recent section is one row per app — Files, Edit, Agent,
// Radio — each popping out that app's last few items. Grouping, labels and
// the verb an item sends are decided here so the component only renders.

export const FM_APP_ID = 'com.wash.fm';
export const EDIT_APP_ID = 'com.wash.edit';
export const RADIO_APP_ID = 'com.wash.radio';
export const AGENTS_APP_ID = 'com.wash.agents';

/** How many items a flyout shows. The store keeps more (the palette and
 * search reach them); a flyout is for the last few. */
export const RECENT_FLYOUT_CAP = 8;

/** One agent session as agentd's roster push lists it (State.recent). */
export interface AgentRecent {
  session_id: string;
  agent?: string;
  cwd?: string;
  dir?: string;
  title?: string;
  live?: boolean;
  detached?: boolean;
  row_key?: string;
}

export type AgentRecentAction = 'resume' | 'reattach' | 'focus' | 'none';

/** The roster fields that say whether a session is running right now. */
export interface AgentLiveRow {
  key: string;
  session_id?: string;
  detached?: boolean;
  state?: string;
  reason?: string;
}

/** A row whose adapter exited lingers on the roster (failed/exited, until
 * agentd's sweep drops it) so the failure is visible, but nothing is
 * running behind it. agentd's rosterIndex (apps/agentd/be/history.go)
 * skips these; so must the start menu, or it offers to focus a corpse. */
export function rowIsDead(r: AgentLiveRow): boolean {
  return r.state === 'failed' && r.reason === 'exited';
}

/** withLiveRows corrects each history entry's running state from the
 * roster rows in the same push. agentd recomputes the history list when
 * the history changes, not when a window detaches, so its live/detached
 * flags can trail the rows by a whole session — and the difference picks
 * the verb. A session with no row is left as the history describes it; one
 * whose only rows are dead is not running, whatever the history last said
 * (it was published while the adapter was still up). */
export function withLiveRows(agents: ReadonlyArray<AgentRecent>, rows: ReadonlyArray<AgentLiveRow>): AgentRecent[] {
  return agents.map((s) => {
    const mine = rows.filter((r) => r.session_id && r.session_id === s.session_id);
    const row = mine.find((r) => !rowIsDead(r));
    if (row) return { ...s, live: true, row_key: row.key, detached: !!row.detached };
    if (mine.length > 0) return { ...s, live: false, detached: false, row_key: undefined };
    return s;
  });
}

/** agentRecentAction is apps/ai/fe/src/HistoryPanel.tsx historyAction for
 * the start menu, and must agree with it: a detached session is reattached
 * by row key, a live one with a window is focused, only a finished one is
 * resumed. Resuming either of the first two would start a second adapter
 * on a conversation that is already running. Live with no row is not
 * reachable from here at all. A finished session that no longer knows its
 * agent is 'restart' there — a fresh session in its folder, a choice the
 * History panel explains and this menu has no verb for — so it is left out
 * here rather than sent to a resume that cannot work. */
export function agentRecentAction(s: AgentRecent): AgentRecentAction {
  if (s.detached && s.row_key) return 'reattach';
  if (s.live && s.row_key) return 'focus';
  if (s.live) return 'none';
  if (!s.agent) return 'none';
  return 'resume';
}

/** The agent's own name for the session, else which agent and where. */
export function agentRecentLabel(s: AgentRecent): string {
  if (s.title) return s.title;
  const who = s.agent || 'agent';
  return s.dir ? `${who} · ${s.dir}` : who;
}

export type RecentItem =
  | { kind: 'path'; key: string; label: string; detail: string; icon: string; entry: RecentEntry }
  | { kind: 'station'; key: string; label: string; icon: string; entry: RecentEntry }
  | { kind: 'agent'; key: string; label: string; detail: string; icon: string; session: AgentRecent; action: AgentRecentAction };

/** sameKeys is the equality for a keyed list's id memo: a push that
 * rebuilds every object but keeps the ids must not re-run the <For> over
 * them, or each row is torn down and rebuilt under the pointer. */
export function sameKeys(a: ReadonlyArray<string>, b: ReadonlyArray<string>): boolean {
  return a.length === b.length && a.every((k, i) => k === b[i]);
}

/** rectIsLaid says an element has a layout box to measure. A row that has
 * left the DOM, or is not laid out, measures all zeroes — a flyout placed
 * from that sits in the screen's top-left corner. */
export function rectIsLaid(r: { width: number; height: number }): boolean {
  return r.width > 0 || r.height > 0;
}

export interface RecentGroup {
  /** stable id: the app id the group stands for */
  id: string;
  label: string;
  /** what the empty flyout says */
  empty: string;
  items: RecentItem[];
}

const pathItem = (e: RecentEntry, icon: string): RecentItem => ({
  kind: 'path',
  key: 'p:' + e.path,
  label: recentName(e.path),
  detail: recentDir(e.path),
  icon,
  entry: e,
});

/** recentGroups folds the launcher store and agentd's session history into
 * the start menu's rows: Files, Edit, Agent, Radio always (an empty one
 * says so in its flyout, so where to look is learnable before there is
 * anything to find), then one row per other app that has recent files —
 * an image opened last week must not stop being reachable because it has
 * no named row. Newest first, capped per group. */
export function recentGroups(
  recent: ReadonlyArray<RecentEntry>,
  agents: ReadonlyArray<AgentRecent>,
  appName: (appID: string) => string | undefined,
  rows: ReadonlyArray<AgentLiveRow> = [],
  cap = RECENT_FLYOUT_CAP,
): RecentGroup[] {
  const newest = [...recent].sort((a, b) => b.at - a.at);
  const paths = (appID: string) => newest.filter((e) => e.path && e.app_id === appID);
  const groups: RecentGroup[] = [
    {
      id: FM_APP_ID,
      label: 'Files',
      empty: 'No recent folders',
      items: paths(FM_APP_ID).slice(0, cap).map((e) => pathItem(e, 'folder')),
    },
    {
      id: EDIT_APP_ID,
      label: 'Edit',
      empty: 'No recent files',
      items: paths(EDIT_APP_ID).slice(0, cap).map((e) => pathItem(e, 'file-text')),
    },
    {
      id: AGENTS_APP_ID,
      label: 'Agent',
      empty: 'No recent sessions',
      items: withLiveRows(agents, rows)
        .map((s) => ({ s, action: agentRecentAction(s) }))
        .filter(({ action }) => action !== 'none')
        .slice(0, cap)
        .map(({ s, action }) => ({
          kind: 'agent' as const,
          key: 'a:' + s.session_id,
          label: agentRecentLabel(s),
          detail: s.title ? (s.dir ?? '') : '',
          icon: 'bot',
          session: s,
          action,
        })),
    },
    {
      id: RADIO_APP_ID,
      label: 'Radio',
      empty: 'No recent stations',
      items: newest
        .filter((e) => !e.path && e.name && e.app_id === RADIO_APP_ID)
        .slice(0, cap)
        .map((e) => ({ kind: 'station' as const, key: 'n:' + e.name, label: e.name ?? '', icon: 'radio', entry: e })),
    },
  ];
  const named = new Set(groups.map((g) => g.id));
  const others: string[] = [];
  for (const e of newest) {
    if (e.path && !named.has(e.app_id) && !others.includes(e.app_id)) others.push(e.app_id);
  }
  for (const appID of others) {
    groups.push({
      id: appID,
      label: appName(appID) ?? appID,
      empty: 'No recent files',
      items: paths(appID).slice(0, cap).map((e) => pathItem(e, 'file-text')),
    });
  }
  return groups;
}

// ---- flyout aim ----
//
// A pointer on its way to an open flyout crosses other rows. aimingAt says
// whether it is still heading for the flyout — the classic submenu "safe
// triangle": the pointer is inside the triangle from where it was a moment
// ago to the flyout's near edge. A pointer that has stopped makes no
// triangle, so it is not aiming, and the row under it wins.

export interface PointerSample {
  x: number;
  y: number;
  /** ms, performance.now() */
  t: number;
}

export interface FlyoutRect {
  left: number;
  top: number;
  bottom: number;
}

/** How far back aim looks for "where the pointer was". */
export const AIM_LOOKBACK_MS = 100;

/** recordPointer appends a sample and keeps only the last few. */
export function recordPointer(trail: ReadonlyArray<PointerSample>, s: PointerSample, keep = 8): PointerSample[] {
  const out = [...trail, s];
  return out.length > keep ? out.slice(out.length - keep) : out;
}

/** aimingAt: is the latest pointer sample inside the triangle from the
 * sample ~AIM_LOOKBACK_MS earlier to the flyout's left edge (padded)? */
export function aimingAt(trail: ReadonlyArray<PointerSample>, now: number, rect: FlyoutRect, pad = 8): boolean {
  if (trail.length < 2) return false;
  const p = trail[trail.length - 1];
  // A pointer that has not moved lately is resting, not travelling.
  if (now - p.t > AIM_LOOKBACK_MS) return false;
  let a = trail[0];
  for (const s of trail) {
    if (p.t - s.t >= AIM_LOOKBACK_MS) a = s;
  }
  if (p.x >= rect.left || a.x >= rect.left) return false;
  const b = { x: rect.left, y: rect.top - pad };
  const c = { x: rect.left, y: rect.bottom + pad };
  const cross = (o: { x: number; y: number }, u: { x: number; y: number }, v: { x: number; y: number }) =>
    (u.x - o.x) * (v.y - o.y) - (u.y - o.y) * (v.x - o.x);
  const area = cross(a, b, c);
  if (Math.abs(area) < 1) return false;
  const d1 = cross(a, b, p);
  const d2 = cross(b, c, p);
  const d3 = cross(c, a, p);
  const neg = d1 < 0 || d2 < 0 || d3 < 0;
  const pos = d1 > 0 || d2 > 0 || d3 > 0;
  return !(neg && pos);
}
