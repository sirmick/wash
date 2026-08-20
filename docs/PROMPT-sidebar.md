# Session prompt — making the right rail host-aware

Paste this as the opening prompt for a dedicated planning session. It is
self-contained: every fact below was verified against the tree on 2026-08-19,
with file:line, so you should not need to re-derive them.

Plan of record: **[SIDEBAR.md](SIDEBAR.md)**. Read it first — this prompt is the
work order and the list of things that document does *not* yet settle.

---

You are planning the remaining milestones (M1–M6) of wash's sidebar
host-awareness work. **M0 is done and merged into the branch**; everything else
is open. Branch/worktree in use: `branches/sidebar-host` (branch `sidebar-host`,
based on `main`). Do not start on `main`.

The goal is not "make the widgets work over SSH". It is to decide, per widget,
*where each piece of the sidebar should live*, and then move it — because the
remote defect is a symptom of a structural mistake, not a missing feature.

## 1. The defect, in one paragraph

Under `wash remote`, the right rail silently addresses the local host. Eight of
its eleven sections do. The cause is one API seam: `sendAppMsg(instanceID, …)`
carries origin (ids are origin-tagged, `web/shell/src/main.tsx:1625`), but
`sendAppMsgTo(recipient, …)` always uses the local `conn` (`main.tsx:1633`) —
`WashRecipient` is `{app_id}｜{instance_id}` with no origin field to pass. Every
widget is on the wrong side of it **twice**: the FE sends to its own local
session instance (`apps/session/fe/src/main.tsx:968`), and the session BE then
does `SendAppMsgTo(wire.Recipient{AppID: …})`, which resolves within its own
router only (`apps/session/be/app.go`, every `register*Gateway`). Neither hop was
a decision about hosts — the gateway exists for **sender attestation** (a
shell-originated cross-app send carries no router-attested `From`, so services
reject it, `app.go:90`). Local-only is a side effect.

## 2. Verified facts — do not re-derive these

- **B runs `--no-session`** (`internal/runner/router/router.go:183`), so there is
  no session app on a remote host to gateway through. `ShellLaunch` exists
  precisely because of this: *"This is the no-session-BE launch path"*
  (`internal/router/shell_session.go:830`).
- **`relayAppMsgToShell` fans an app's message to every attached shell**
  (`internal/router/app_session.go:627`), and A's shell **is** an attached shell
  on B. REMOTE.md §7 already names this as the presentation half.
- **`deliverAppMsg` compounds the incoming id with the delivering client's
  origin** (`web/shell/src/main.tsx:933`), so B's traffic arrives tagged as B's.
- **`StateService` keys subscribers by `from.InstanceID`** and pushes with
  `SendAppMsgTo(wire.Recipient{InstanceID: …})` (`pkg/sdk/stateservice.go`).
  This is why the shell→router ctrl-verb design was rejected — a subscription
  needs a return path and a shell is not an app instance. See SIDEBAR.md M1
  *"Why not a shell→router ctrl verb"*.
- **`com.wash.ai` already exists** (`apps/ai/be/app.go:70`) — "a window onto one
  managed agent session", a thin host around `<AgentSession>` from `@wash/ui`,
  already talking to agentd directly with correct attestation, already carrying
  a `HistoryPanel` with session metadata.
- **Bulk is single-app**: the only consumer of the bulk service is
  `apps/fm/be/upload.go`.
- **Priv is genuinely multi-app**: `journal`, `syslogs`, `packages` — plus
  background services with no window at all.
- **`hostColor(origin)` already exists** (`web/shell/src/host-colors.ts`),
  returns null for LOCAL, deterministic per origin, override-able. Its doc
  comment already anticipates "merged sidebar-widget tags".
- **Agents has no disposition in REMOTE.md §6.2** — that table predates the
  agent rail, which is why Agents is the sharpest symptom.
- **M0 is done** (`dc27963b`): B's toasts reach A's tray, host-tinted. The
  `!isLocal` guard at `main.tsx:450` is gone.

## 3. What is already decided

Do not relitigate these unless you find hard evidence against them.

- **Apps are host-portable; chrome is seat-bound.** Anything expressed as an app
  inherits `launchOn(origin, …)`, origin-tagged WM intents and correct
  attestation for free. Chrome must re-solve all of it.
- **Split awareness from control.** Awareness (counts, state, host tag) stays in
  the rail and becomes host-aware. Control (resume/stop/approve/cancel) moves
  in-app. The rail becomes a router of attention, deep-linking via
  `launchOn(origin, appID)`.
- **M1 is a background gateway app** (`com.wash.hostgw`), not a router ctrl
  verb. It needs no new wire protocol — see SIDEBAR.md M1 for the full flow and
  the two small shell-side additions (origin-aware `sendAppMsgTo`; a shell-level
  listener, since a windowless background app has no element to deliver into).
- **Clipboard and Viewport stay local-only.** The seat owns them.
- **Audio stays deferred.** Sound exits B's speakers; it needs a stream, not a
  widget (REMOTE.md §7).

## 4. What you are being asked to settle

These are the real open questions. They are why this is a planning session.

1. **Merge vs follow-the-focused-host.** SIDEBAR.md assumes awareness is merged
   and host-tagged, inherited from REMOTE.md §6.2. The alternative — the rail
   reflects whichever host owns the focused window — is materially different and
   may suit daily remote use better. This changes `hostgw`'s state shape and the
   rail's whole information design, so **settle it before M1 ships**, not after.
2. **`hostgw`'s relationship to A's session BE gateway.** Does it replace it on
   day one (one implementation, bigger blast radius) or run alongside it for
   remote origins only (two implementations that must agree)? SIDEBAR.md M6
   assumes the fold happens eventually. Decide when.
3. **Multi-user B.** `hostgw` republishes to *every* shell attached to B — the
   rejected pseudo-instance design could have targeted one. What ownership rule
   gates it? This is the single biggest security question in the plan.
4. **Badge staleness.** `sdk.StateService` is live-only: subscribers get a fresh
   snapshot on (re)subscribe, not a replay of what they missed. A badge that was
   right at subscribe time goes stale silently across an SSH blip. Prefer
   deriving badges from a snapshot on every (re)attach over incrementing from
   events — but confirm that survives the reconnect paths in RECONNECT.md.
5. **M2's shape.** `com.wash.ai` is "one window onto one session" today. Does the
   roster become a second pane in that window, or does the app go
   multi-instance with a separate roster surface? This decides whether M2 is an
   additive change or a restructuring.
6. **M4's anti-phishing story.** A window claiming to be a priv prompt is
   exactly what an attacker draws. The payoff is real (if the prompt is B's own
   window, the password never traverses A, and REMOTE.md §10's
   encrypt-to-B's-pubkey requirement becomes unnecessary) — but the trust
   indicator has to be chrome-drawn, and that needs designing.
7. **Sequencing after M2.** SIDEBAR.md orders M3→M4→M5. M2 is deliberately the
   experiment; if "rail as router of attention" feels wrong in practice, say so
   and re-cut the rest rather than pushing four more widgets through it.

## 5. Costs to keep visible

- **M2 reworks recent code.** The rail's resume/fork/terminate verb set was just
  built for GH #21. Moving it is rework — planned, not discovered.
- **M3 depends on M1.** A long copy outlives the fm window, so control-in-fm
  without real awareness blinds you to a running job.
- **This costs the local case to fix the remote case.** The rail is genuinely
  good on one box: one glance, no windows. Moving control in-app makes the 90%
  case slightly worse. Mitigation is a one-click deep-link and room for a real
  UI — but it is a regression, and it should be *felt* in M2 before M3–M5 commit.

## 6. Repo landmines a fresh session will otherwise hit

- **Never use `json.RawMessage` / `[]byte` for structured BE→FE fields** — the
  router base64-encodes byte strings. Use concrete types.
- **`StateService.Mutate` must be copy-on-write** for any slice/map a snapshot
  outlives, or `make test-race` will catch a data race (it has before). See the
  Snapshot doc comment in `pkg/sdk/stateservice.go`.
- **FE-less Go service binaries need `.PHONY`** in the Makefile or make silently
  never rebuilds them.
- **A new app must be registered in the app roster** — the Makefile templates
  the app list, and `gen-imports` / `check-icons` / `check-pkg-binaries` guard
  drift. Run them.
- **`make unit-test` does NOT rebuild the embedded FE.** A green unit tier says
  nothing about whether the binary contains the FE you just wrote. Run
  `make wash` and, if it matters, grep the binary for a string you added.
- **Test tiers:** `*.ctest.{ts,tsx}` run under vitest (`make component`, jsdom,
  root `vitest.config.ts`); `*.test.ts` run under `node --test
  --conditions=browser` (the `--conditions` matters — the default resolves the
  non-reactive SSR build of Solid). Go is `make unit-test` / `make test-race`.
- **jsdom cannot represent the accent tokens.** They are
  `var(--wash-accent-*, #fallback)`, and jsdom's CSS parser drops a `var()`
  colour inside a shorthand. Assert behaviour (data attributes, labels), not
  computed colour — see `web/shell/src/notify.ctest.ts` for the precedent and
  the reasoning.
- **`TestHandoffEndToEnd` in `internal/login` is red on `main`** and unrelated
  to this work: it finds the developer's own running wash session instead of its
  temp run-root. Do not chase it.
- **Two-router e2e** uses the `?peer=` `startRouter` fixture — see
  `e2e/tests/remote-apps.spec.ts`.

## 7. What to produce

A revision of `docs/SIDEBAR.md` that settles §4's open questions, with the
reasoning recorded — especially for #1 and #3, where a future reader will
otherwise assume the current answer was obvious. If a decision changes the
milestone shape, re-cut the milestones rather than bolting on caveats. Update
`TODO.md`'s entry to match.

Then, if the plan holds up, implement M1 on `branches/sidebar-host` behind the
project's tiered gate: build + unit green before commit, full `make all-test`
before push.
