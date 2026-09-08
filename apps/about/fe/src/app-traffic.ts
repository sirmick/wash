// Per-app traffic rows for the About pane's link panel.
//
// The router attributes FE-bound bytes to the app that produced them
// wherever it can: raw channels carry a binding, and the app_msg relay
// knows the sender. What it cannot attribute is its own lifecycle
// traffic — window.create, session.patch, the link push itself — so the
// app rows sum to LESS than the per-class totals shown above them.
//
// Rather than leave that gap unexplained (a table that visibly does not
// add up invites the reader to distrust all of it), the difference is
// rendered as one more row: the router's own overhead, derived here
// instead of counted in the backend. Deriving it is what keeps it honest
// — it is the residue of the class totals by construction, so it cannot
// drift from them.

/** One app's FE-bound bytes/frames, indexed [Interactive, Bulk, Background, Control]. */
export interface AppClassStats {
  app_id: string;
  tx_bytes: number[];
  tx_frames: number[];
}

/** A rendered row: an app, or the derived router-overhead row. */
export interface TrafficRow {
  label: string;
  bytes: number[];
  frames: number[];
  total: number;
  /** true for the derived remainder row, which has no app behind it. */
  derived: boolean;
}

const CLASSES = 4;

const zeros = (): number[] => [0, 0, 0, 0];

/** Sum of a per-class array, tolerating a short or missing one. */
const sum = (a: number[] | undefined): number => {
  if (!a) return 0;
  let n = 0;
  for (let i = 0; i < CLASSES; i++) n += a[i] ?? 0;
  return n;
};

/**
 * Build the rows for the per-app table: one per app that has sent
 * anything, busiest first, plus the router-overhead remainder.
 *
 * classTotals is the per-class tx_bytes/tx_frames from the same snapshot.
 * The remainder is clamped at zero: the two are sampled at slightly
 * different points (app bytes when the envelope is relayed, class totals
 * when the frame reaches the wire), so a frame in flight at snapshot time
 * can briefly make an app's share exceed the total. That is a rounding
 * artifact of the sample, not a number worth showing as negative.
 */
export function trafficRows(
  apps: AppClassStats[] | undefined,
  classBytes: number[] | undefined,
  classFrames: number[] | undefined,
): TrafficRow[] {
  const rows: TrafficRow[] = [];
  const attributedBytes = zeros();
  const attributedFrames = zeros();

  for (const a of apps ?? []) {
    const bytes = zeros();
    const frames = zeros();
    for (let i = 0; i < CLASSES; i++) {
      bytes[i] = a.tx_bytes?.[i] ?? 0;
      frames[i] = a.tx_frames?.[i] ?? 0;
      attributedBytes[i] += bytes[i];
      attributedFrames[i] += frames[i];
    }
    const total = sum(bytes);
    if (total === 0 && sum(frames) === 0) continue;
    rows.push({ label: shortName(a.app_id), bytes, frames, total, derived: false });
  }

  // Busiest first: the question this table answers is "who is using the
  // link", and the answer should be the first line, not somewhere in an
  // alphabetical list. Ties break on name so the order stays stable.
  rows.sort((x, y) => (y.total - x.total) || x.label.localeCompare(y.label));

  const restBytes = zeros();
  const restFrames = zeros();
  let restTotal = 0;
  for (let i = 0; i < CLASSES; i++) {
    restBytes[i] = Math.max(0, (classBytes?.[i] ?? 0) - attributedBytes[i]);
    restFrames[i] = Math.max(0, (classFrames?.[i] ?? 0) - attributedFrames[i]);
    restTotal += restBytes[i];
  }
  if (restTotal > 0 || sum(restFrames) > 0) {
    rows.push({ label: 'router (lifecycle)', bytes: restBytes, frames: restFrames, total: restTotal, derived: true });
  }
  return rows;
}

/**
 * "com.wash.term" → "term". The reverse-DNS id is how the router names an
 * app and the right thing on the wire, but a column of "com.wash." is
 * nine characters of nothing repeated down the page. Anything not in
 * wash's own namespace is left whole, since there the prefix is the
 * information.
 */
export function shortName(appID: string): string {
  return appID.startsWith('com.wash.') ? appID.slice('com.wash.'.length) : appID;
}
