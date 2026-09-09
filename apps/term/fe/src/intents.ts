// Split intents — where a not-yet-opened tab should land.
//
// Opening a tab is a BE round-trip (`new_tab` → pty → `tab_opened`), so a
// split has to remember the group and direction it asked for until the tab
// arrives. The first version kept those in a FIFO and let ANY arrival shift
// it, which leaked: a `tab_error` never shifted, so the next plain New Tab
// landed as a split; and arrivals nobody asked for here (a reconcile after
// reattach, agentd's `exec_tab`) consumed whatever was queued.
//
// So an intent is keyed to the request that made it. Every `new_tab` carries
// a request id, the BE echoes it on `tab_opened` / `tab_error`, and only the
// matching arrival consumes the intent; an arrival with no id takes nothing.
// Pure, so it unit-tests under plain node:test.

import type { Dir } from './layout';

export interface SplitIntent {
  path: string;
  dir: Dir;
}

export class SplitIntents {
  private readonly byReq = new Map<string, SplitIntent>();
  private seq = 0;

  // mint returns a fresh request id for the next `new_tab`.
  mint(): string {
    this.seq += 1;
    return `t${this.seq}`;
  }

  // set records where the tab opened by `req` should go.
  set(req: string, intent: SplitIntent): void {
    this.byReq.set(req, intent);
  }

  // take consumes and returns the intent for `req`. An arrival without a
  // request id — a reconcile, an exec_tab from another surface — never
  // matches, so it can never steal a pending split.
  take(req: string | undefined): SplitIntent | undefined {
    if (!req) return undefined;
    const intent = this.byReq.get(req);
    if (intent !== undefined) this.byReq.delete(req);
    return intent;
  }

  // drop forgets the intent for a request that failed (`tab_error`), so
  // it cannot attach itself to the next tab that does open.
  drop(req: string | undefined): void {
    if (req) this.byReq.delete(req);
  }

  get size(): number {
    return this.byReq.size;
  }
}
