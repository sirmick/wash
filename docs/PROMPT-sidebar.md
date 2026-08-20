# Session prompt — implement M1: `com.wash.hostgw`

Paste this as the opening prompt for a dedicated implementation session. The
planning is **done**: docs/SIDEBAR.md is the plan of record, §3.2 records the
seven settled design decisions with reasoning, and the M1 section carries the
staging plan and a "Pinned mechanics" list of verified file:line facts. Read
SIDEBAR.md §1–§4 (through M1) before writing code. Do not relitigate §3.2.

State: **M0 is done** (`dc27963b` — remote toasts, host-tinted). Branch:
`sidebar-host`, worktree `branches/sidebar-host`. Work there; never on `main`.

---

## 0. Scope and stop point

Implement **M1 only**, as three commits (M1a → M1b → M1c below), then stop for
review before M2. M2–M6 are out of scope. Also out of scope, explicitly:

- **Do not delete or modify any session BE gateway** (`register*Gateway`,
  `apps/session/be/app.go`). They keep the control verbs and the widgets'
  interactive feeds until M3/M4/M6. Dual subscription to local services
  (hostgw + session BE) is intended and harmless.
- **Do not add app-facing `window.wash` API** beyond the read accessor +
  subscription for hostgw state (the `linkStats`/`windowsSub` pattern).
- **Do not touch control paths** (priv approve, bulk cancel, agent verbs…).

## 1. What you are building

A FE-less background singleton, `com.wash.hostgw` (`apps/hostgw/be`), that runs
on **every** router. On startup it subscribes — as a normal, router-attested
app — to the same background services the session BE gateways subscribe to
today (mirror `serviceFEKind`, `apps/session/be/app.go:168`: notify, bulk,
priv, netd, audio, agentd). It caches the latest snapshot per service and
republishes each `state` push to its own FE as
`{kind:"hostgw.state", service:"<name>", state:<verbatim>}` — which
`relayAppMsgToShell` (`internal/router/app_session.go:627`) fans to every
attached shell. A's shell is an attached shell on B, so B's awareness state
arrives at A over the connection that already exists. **No router changes, no
new wire protocol.**

Flow: shell attaches to origin → sends `{kind:"subscribe"}` to
`{app_id:"com.wash.hostgw"}` over that origin's conn (spawns the singleton via
`resolveRecipient`) → hostgw replies with one `hostgw.state` per known service
→ every push thereafter is republished → the shell intercepts, tags by origin,
and exposes a merged (origin, service) map to the session chrome → the rail
renders host groups.

## 2. Commit plan

### M1a — hostgw + shell plumbing (remote origins)

**Go** (`apps/hostgw/be/`, modelled on `apps/notify/be/` for shape and on the
session gateways for behaviour):

- `app.go`: manifest (background, singleton), startup subscribes to each
  service via `SendAppMsgTo(wire.Recipient{AppID: …}, {"kind":"subscribe"})`;
  `sdk.HandleFromVoid(bus, "state", …)` branching on `from.AppID` → service
  name (mirror the session forwarder at `apps/session/be/app.go:111`); cache +
  republish via `conn.SendAppMsg`. Handle `{kind:"subscribe"}` from shells
  with a **plain** (unattested) handler that replies with all cached
  snapshots — read-only by design, see SIDEBAR.md "Pinned mechanics".
- `app_test.go`: mirror `apps/session/be/gateway_test.go`. Must cover: state
  push → republish; late subscribe → full snapshot replay; unknown sender
  app id → ignored.
- Registration: add to `SVC_APPS` (`Makefile:68`), rerun `gen-pkg-binaries` /
  `gen-imports`, satisfy `check-pkg-binaries`. FE-less Go binaries need
  `.PHONY` or make silently never rebuilds them.

**Shell** (`web/shell/src/`):

- Maintain an instance→app-id map from `app.declared` (it fires for every
  instance, background included — `shell_session.go:93`, late shells:
  `router.go:1766`). Route on this map, **never** on payload shape (payload
  routes are spoofable by any app).
- Intercept in `deliverAppMsg` (`main.tsx:932`) **before** `deliverToInstance`
  — `deliverToInstance` queues unboundedly for element-less instances
  (`api.ts:186`, `pendingMessages`).
- A cross-element Sub holding `Map<origin, Map<service, state>>` + a
  `window.wash` accessor/subscription (copy the `linkStats` / `windowsSub`
  pattern; consumed at `apps/session/fe/src/main.tsx:319`).
- Send the subscribe on peer attach and on every reattach
  (`reconcileRemoteAttachments` and the client reconnect path) — snapshots are
  full-replace, so re-subscribing is idempotent and is the §3.2(4) staleness
  answer.
- On origin `down`: drop that origin's map entries. `reconnecting` handling is
  M1c's concern (presentation), not the data layer's.

**Proof**: two-router e2e (`?peer=` `startRouter` fixture, see
`e2e/tests/remote-apps.spec.ts`): trigger a notify on B (or seed agentd
state), assert the hostgw map on A holds it under B's origin, and — the
regression that motivated this whole plan — that A's own state does NOT show
under B or vice versa.

### M1b — flip local awareness reads

A's shell subscribes to A's own hostgw identically (LOCAL is just another
origin). The rail's **badges and counts** read the merged hostgw map for all
origins including LOCAL. The widgets' interactive internals (lists, overlays:
`BulkConflictOverlay`, `PrivUnlockOverlay`, agent asks) keep reading the
legacy `notify.state`/`bulk.state`/… kinds — do not touch them. Careful with
double counting: a badge must come from exactly one source (hostgw), even
though the data now arrives twice.

**Proof**: existing component tier stays green (`make component`, 91 tests) +
badge counts still correct locally in the running desktop.

### M1c — rail host groups

Per SIDEBAR.md §3.2(1) and (4): sections group by host, local first; remote
groups collapsed-but-badged; `autoExpandSection`
(`apps/session/fe/src/main.tsx:356`) fires on remote events; grey the group on
`reconnecting`, drop on `down` (host status from the RemoteWidget feed);
host names tinted via `hostColor(origin)` (`web/shell/src/host-colors.ts`).
Keep it to the sections that have hostgw data (Notify, Bulk, Priv, Net,
Agents); About/Audio/Link/Viewport/Clipboard are untouched.

`.ctest.tsx` coverage for the grouped rendering (model:
`AgentsWidget.ctest.tsx`). Assert behaviour (labels, `data-*` attributes,
grouping), **not** computed colour — jsdom drops `var()` colours in
shorthands; `web/shell/src/notify.ctest.ts` is the precedent and explains it.

## 3. Gates (the project's tiered rule)

Per commit: build + unit green (`make wash` first — **`make unit-test` does
NOT rebuild the embedded FE**; a green unit tier says nothing about whether
the binary contains your FE — then `make unit-test`, `make component`).
`make test-race` must pass for hostgw (its cache is exactly the
`StateService` copy-on-write shape that bit before — Mutate/cache writes must
be copy-on-write for anything a snapshot outlives). Before declaring M1 done:
full `make all-test`. Push only if asked.

## 4. Landmines (beyond those already inlined above)

- Never `json.RawMessage`/`[]byte` for structured BE→FE fields — the router
  base64-encodes byte strings. State is `any` end to end
  (`serviceStateMsg`, `apps/session/be/app.go:448`).
- `*.test.ts` run under `node --test --conditions=browser` — without the flag
  Solid resolves its non-reactive SSR build. `*.ctest.*` run under vitest
  (`make component`, root `vitest.config.ts`).
- `TestHandoffEndToEnd` (`internal/login`) is red on `main` — host-state
  leakage from the developer's live session, unrelated. Do not chase it.
- Interrupted Playwright runs leak child processes; >128 breaks fs.watch
  silently. Count processes before blaming your code.
- e2e in a fresh worktree needs `pnpm install --ignore-workspace` inside
  `e2e/` (it is not a workspace member).

## 5. Definition of done

Three commits on `sidebar-host`; two-router e2e proving B→A state flow and
origin isolation; component + unit + race tiers green; full `make all-test`
run and its result reported honestly; TODO.md's M1 entry ticked; a short note
appended to SIDEBAR.md M1 recording anything discovered that contradicts the
pinned mechanics. Then **stop** — M2 starts with the §3.2(7) tripwire
conversation, not with code.
