// Pure decision for a station the start menu names (a `tune` message, or
// the `tune` riding a stations_ok). No DOM, no Solid — `node --test`-able.

export type TuneDecision = { kind: 'play'; be: number } | { kind: 'hold' } | { kind: 'drop' };

/** decideTune says what to do with a named tune against the list this FE
 * holds. Stations are addressed by BE index, which only means something
 * against a list, so:
 *  - in the list (and the stream base known): play it;
 *  - no list yet: hold it — a freshly mounted FE's own stations request is
 *    in flight, and the stations_ok answering it is where the name is
 *    looked up (once; see the caller);
 *  - a list that lacks it: drop it. Holding would leave the name waiting
 *    for whatever stations_ok comes next — a paste minutes later that
 *    happens to add that station would start it playing out of the blue. */
export function decideTune(name: string, stationNames: ReadonlyArray<string>, haveBase: boolean): TuneDecision {
  if (!name) return { kind: 'drop' };
  if (stationNames.length === 0 || !haveBase) return { kind: 'hold' };
  const be = stationNames.indexOf(name);
  return be < 0 ? { kind: 'drop' } : { kind: 'play', be };
}
