# wash — backlog

The one backlog file, grouped by theme. Detailed designs and implementation
prompts live in `docs/` and on the linked GitHub issues; resolved items are
DELETED (git history is the archive), never kept as struck-through history.

Last consolidated: 2026-09-29 — every open entry was re-verified against the
current tree, file by file. Entries fixed on that pass are deleted; entries
whose description had gone stale were rewritten rather than trusted, and
several counts had drifted badly enough to change the decision (the doc
rename went 311 → 433 referrers and is now explicitly deferred). Anything
stated here as a count or a line number was true on 2026-09-29; re-derive
before relying on it.

Prior consolidation: 2026-07-03 — (that sweep deleted the 2026-07-01
review docs + fix prompts, MAKE-PLAN, PACKS-PROMPT, NEXT, and the sftp-mount
bug list — all fully landed; see `git log` if you need their content.)

---

## Repo conventions (working-rules gaps)

- [ ] Doc filenames are `UPPER_CASE.md` (50 files in `docs/`), referenced
  from ~433 files including Go and TS comments. Rename to normal case with
  a link check in the same change — `docs/ARCHITECTURE.md:1` and friends.
  **Deliberately not scheduled**: the referrer count has gone 311 → 433
  since this was filed, so the rename is churn against a moving target and
  gets more expensive the longer the tree grows. Do it when something else
  forces a mass doc edit, or not at all. Four files already use normal case
  (`Project.md`, `Todo.md`, `Sweeps.md`, `Review-findings.md`), which is
  the drift the entry was originally about.
- [ ] `COMMANDS.md:1` sits at the repo root; belongs in `docs/`.
- [ ] The **docs** sweep has not run. The apps sweep did (2026-09-08), and
  `docs/Sweeps.md` + `docs/Review-findings.md` exist and record it — the
  old claim that neither file existed is gone. What is still outstanding is
  a docs sweep following getting-started cold.

## Security  (docs/CORE_AUDIT.md §1)

- [ ] **1.1 — verify wash-login's HMAC session cookie on the raw router.**
  Defense-in-depth: the router-token gate already blocks anonymous LAN
  access; this is belt-and-suspenders — when the router is *not* fronted by
  wash-login, verify the `internal/login/cookie.go` HMAC cookie on `/ws`,
  `/screenshot`, `/app/`.
- [ ] **1.4 — blocking Content-Security-Policy on the shell.** Headers
  (X-Frame-Options / nosniff / Referrer-Policy) shipped; a real CSP is
  deferred because the shell loads xterm, CodeMirror and Webamp, each needing
  inline/worker/blob allowances (`internal/httpsec/httpsec.go:29`). Needs
  in-browser verification before it can block (start `default-src 'self'`,
  expect some `style-src 'unsafe-inline'`).

## Ingress & remote apps  (docs/REMOTE.md)

- [x] **Remote `/app/` ingress over the relay** — **issue #15** — DONE
  2026-07-08 (Approach A, user-confirmed; design record in docs/REMOTE.md
  §17). B serves its ingress registry over `--listen-ingress` (unix socket,
  second `-L` on the same ssh); A resolves locally-unknown tokens against
  peers and reverse-proxies (`internal/router/ingress_remote.go`). Covered
  by unit tests + `e2e/tests/remote-ingress.spec.ts` (two local routers via
  the `--peer-ingress` seam).
- [ ] **Single-host ingress self-heal after a wash-login restart** (separate
  from #15). A restart tears down sessions + per-launch ingress tokens; the
  shell's `/ws` reconnects fine but the vscode-workbench iframe keeps
  pointing at the dead `/app/<token>/` → 401 (login: `identityFromRequest`
  fail) or 410 (router stale-token) until re-open. Fix: the workbench
  (`apps/vscode-workbench/fe/src/main.tsx`) treats 401/410 from its iframe
  as "ingress died" → auto re-`ensure` (re-mint token / relaunch). Affected:
  vscode/workbench, music, radio, washamp. Diagnosed live on carrier-dev
  2026-06-24.
- [ ] **R3 — stream fm downloads to disk, not a Blob.** The relay is
  creditless, so the FE's writable sink is the last place backpressure can
  live; today fm concatenates chunks into RAM. File System Access API
  (`showSaveFilePicker` → `createWritable`, Blob fallback); slow disk →
  writable backpressure → B paces itself. Verify as a REMOTE download over
  the relay.
- [ ] **R4 — credit-window sizing** (after R3; measure first, don't guess).
  A single remote download is RTT-bound to `DefaultChannelCredit` (64 KiB)
  per credit round-trip, and over the relay that RTT is the ssh hop. Knobs:
  `internal/router/credit.go` `DefaultChannelCredit` + the fm bufio size.
- [ ] **M2e — persist B's router across an SSH drop.** The supervisor ties
  B's router to the `ssh` pid (`apps/remote/be/supervisor.go`); a blip kills
  B's apps. Start B's router detached; re-dial with backoff, report
  `reconnecting`, freeze→thaw windows (docs/REMOTE.md §2/§9).
- [ ] **M4/M5 — multi-host services** — plan of record:
  **docs/SIDEBAR.md** (supersedes REMOTE.md §6.2). The right rail is
  local-only in 8 of 11 sections: widgets reach services through the session
  BE gateway, whose `SendAppMsgTo` has no origin (`sendAppMsgTo` always uses
  the local conn). Fix is by *relocation*, not by merging every verb — control
  moves in-app (apps are host-portable), the rail keeps awareness only.
  - [x] **M0 — remote toasts** — DONE 2026-08-19. B's toasts already reached
    A's shell via `shellList()` and were dropped by one guard; deleted, and
    the toast is now host-tinted + origin-named.
  - [x] **M1 — awareness channel** (`com.wash.hostgw`) — DONE 2026-08-19.
    A background singleton on every router subscribes to its own host's
    services and republishes their state; `relayAppMsgToShell` carries it to
    every attached shell, and A's is one of B's. All three stages landed:
    M1a channel + shell plumbing, M1b the five badges recomputed from the
    merged map (one source, so local state arriving twice can't double-count),
    M1c per-host groups (named in the host hue, collapsed-but-badged,
    auto-expanding on a rise, greyed on reconnecting). No session BE gateway
    touched — control still moves in-app in M2–M6. Two-router e2e proves B→A
    flow, origin isolation, the rendered badge and the group. As-built
    corrections to the plan's pinned mechanics: SIDEBAR.md M1 "As built".
  - [x] **M2 — Agents into `com.wash.ai`** — DONE 2026-08-20. Roster pane
    (master-detail, shared `<AgentRoster>` in @wash/ui), the per-session
    verbs key-addressed through the app's own BE, and the rail down to
    counts + a per-host door via `launchOn`. Two-router e2e proves the
    payoff: the Agent app opened on B shows B's sessions and not A's.
    Permission asks stay in the rail by decision (§3.2(8)); the session BE
    gateway keeps only subscribe/unsubscribe + agent_answer. As-built
    notes and the four surprises: SIDEBAR.md M2 "As built".
  - [x] **M3 — Bulk into fm** — DONE 2026-08-20. M3a the host's job queue
    (fm subscribes to the bulk singleton, so any fm window commands every
    job on its host), M3b the conflict answered where the job is — plus the
    toast's focus→launch-the-owning-app fallback a windowless service needs
    — and M3c the rail down to a host-grouped glance plus a per-host door
    into fm. The rail still holds bulk ids for the LOCAL pop-open only,
    because hostgw's rise-detector skips LOCAL; folding that in is M6.
    As-built notes: SIDEBAR.md M3 "As built".
  - [x] **M4 — priv answers where the escalation is** — DONE 2026-08-20.
    M4a: wash-priv had never toasted at all, so a remote escalation was
    invisible, not merely unanswerable. M4b: a new `surface: "modal"` —
    autoboots and hides like a service, but the SHELL draws it above every
    window with the desktop blurred, which is the anti-phishing mechanism
    (a window cannot blur the desktop or draw chrome's host label). No
    separate face app needed: com.wash.priv changed surface. Prompts are
    user-summoned, never self-opening. The password now transits priv's
    own FE, not the session app's. As-built: SIDEBAR.md M4 "As built".
  - [x] **M5 — Net per-host** — DONE 2026-08-20. A door per remote host
    into `com.wash.net`, keyed on HAVING netd rather than having a
    problem (`hostsWithService`) — a network that is quietly fine is
    still a thing you open settings for. **About's B card is deferred
    with a reason**: its data comes from the session BE's own ticker and
    B runs --no-session, so it needs a stats service on B published via
    hostgw — a new state shape, not launch-addressing. Its own milestone.
  - [x] **M6 — cleanup + hardening** — DONE 2026-08-20. Removed bulk's
    and remote's dead gateway verbs (and bulk's serviceFEKind entry);
    folded LOCAL into hostgw's rise-detection so local and remote hosts
    interrupt on the same rule, deleting the legacy bulk.state handler;
    audited §3.2(3) — unix attach is SO_PEERCRED per-uid, TCP /ws is a
    bearer token, and hostgw's audience is exactly the audience that
    already gets toasts and window state, so it adds no new exposure.
    As-built: SIDEBAR.md M6 "As built".
- [ ] **Clipboard sync hub** — the rest of the old M4/M5 entry. Clipboard
  state is still per-router (`internal/router/clipboard.go:1-3`, "v0.1 keeps
  the state inside the router"), so copy on B does not paste on A. The
  **cross-origin z-band** half of this entry largely landed in `e566bc39`
  (the colliding per-router `z` became an FE-arbitrated global `gz`, chrome
  bands in place); only the "focused-host windows on top" grouping rule is
  still unimplemented.
- [ ] **M6 — remote hardening pass**: multi-tenancy, provenance/priv-phishing
  review, reconnect-audit alignment, B-router teardown/linger policy.
- [ ] **Un-diagnosed report from the R2 bug bash**: "something serious funky
  gone wrong with rendering content" — the screenshot never reached the
  session; not reproduced. Repro harness pattern when it resurfaces:
  `e2e/tests/remote-apps.spec.ts` + the `startRouter` fixture (`?peer=` two
  local routers, launch remote term/fm, screenshot).

## Display  (wash-display; implementation guide: docs/DISPLAY_FEATURES_H.md)

- [ ] **H1 — wl_data_device drag-and-drop.** In-app drag is dead
  (`request_start_drag` unhandled): GTK text drags, Qt file-view drags,
  Chromium tab-tear do nothing. Largest + highest UX value; ~3 sub-commits
  (seat grab → FE drag surface → data transfer via the poll-bounded
  `handle_set_selection` pattern).
- [ ] **H5 — xdg-activation.** "Focus me" / URL-handoff requests silently
  dropped; needs `wlr_xdg_activation_v1_create` + a new app→router
  `window.activate` event into the focus path.
- [ ] **H7-rest — set_app_id → per-app icons + parent stacking.** Cosmetic;
  `EvtWindowCreate.ParentWin` already exists — thread into `SessionWindow`
  + FE stacking; icons via a new `window.app_id` report.
- [ ] **Manual-verification checklist** (needs a human at a display): G2
  non-US keymap typing; G3 selection-claim leak; G4 Qt hard-min resize feel;
  H2 middle-click paste (incl. X11); H3 mpv/SDL2 fractional crispness; H7
  CSD minimize; plus H1/H5/H7-rest behaviors once built.

## Test stability  (docs/TEST_FLAKES.md — the 2026-07-03 full-suite audit)

- [ ] **e2e coverage the 2026-08-24 audit found missing.** The suite is
  otherwise strong (190 spec files / ~1279 `test(...)` calls in `e2e/tests`
  as of 2026-09-29, plus ~1334 Go tests and 69 FE unit files — the audit's
  "500 passing" is ~2.5x stale, so re-derive any proportion below rather
  than trusting it); these are the holes worth filling, in order:
  - **idle policy / idle-inhibit has no e2e.** LIFETIME's user-visible
    promise — a session outlives a closed browser, and an agent holding an
    inhibit suspends the reaper — is Go-unit-tested only. The reaper lives
    in `RunUnixListener`, so a spec needs a wash-login-style harness
    rather than the fixture's `--listen` router; that is why it was not
    done alongside the attention spec.
  - **single-file.spec covers 6 of ~26 FE bundles** (one of them the test
    app) — `e2e/tests/single-file.spec.ts:30-91` against the Makefile's 22
    `FE_APPS` + 4 `FE_PANEL_APPS` (`Makefile:64-67`), so the hole is bigger
    than the audit's "6 of 24". The bundle-size / no-chunks /
    allowed-externals contract is unenforced for the other ~20, so a stray
    dependency ships silently. Derive the list from FE_APPS rather than
    hand-listing.
  - **com.wash.audio and com.wash.fswatch** appear in app lists but nothing
    asserts their own behaviour (each shows up only as a launch target in
    another app's spec). About is now partly covered —
    `e2e/tests/about-app-traffic.spec.ts` asserts per-app traffic
    attribution — but the cheap registered-apps roster tripwire named here
    is still missing.
  - **QoS/credit backpressure** (docs/QOS.md §9 has a test plan) has no
    browser-level spec — only the Go soak test.

- [ ] **Keep `docs/FLAKE_LOG.md` current** — the dated record of flakes actually
  seen, each A/B'd against its pre-change baseline so "my branch broke it" is a
  finding, not a guess. 2026-08-06: the three standing failures (`reconnect` +
  the display pair, which between them had blocked `make push`'s e2e gate for
  weeks) are **root-caused and fixed** — the boot splash swallowing the
  connection banner's retry click, and A10's stale `env.publish` match launching
  terminals before `WASH_X_DISPLAY` existed. Neither was load, and C5's
  "compositor stalls under concurrency" premise was wrong. Issue **#7**
  (display-input-smoke timing out on `wash-app-display`, which blocked #5) is
  the same symptom from the same cause and should be closed with this.

- [ ] **Execute the phased plan in docs/TEST_FLAKES.md** (~75 verified items,
  written for a smaller LLM): Phase A e2e harness/process lifecycle
  (readiness lines logged before bind, leak-on-throw, no process-group kill,
  env scrub, hardlink staging, freshness/teardown guards); Phase B Go-unit
  races (the t.Logf-after-test class, now ×36 test files and still
  spreading, TestSpine byte-count —
  tracked as **issue #8**, OpenWRT qemu pdeathsig); Phase C e2e spec sweeps
  (stale timeout overrides, fs-assert barriers, persist-before-reload);
  Phase D FE unit; Phase E the **test event bus** (control-socket
  `wait_event` + FE settle hook + guest `WASH-EVENT` lines) so tests drive
  state machines instead of timing. Open flake trackers: **#8** (TestSpine).
  (**#7**, display-input-smoke, was fixed 2026-08-06 — see above.)
- [ ] After phases B1–B3: trial dropping `-p 1` from the unit gate (it exists
  to dampen the loopback scheduling race).
- [ ] After phase A6 (hardlink staging): measure control-socket RTTs and walk
  the fixture's 12s band-aid back toward 5s so timeouts are signal again.
- [ ] **Packaging boot-smoke leaks routers** (two 9h orphans found
  2026-07-03 on ports 11081/11082): give the run_matrix boot-smoke/serve
  paths the same group-kill + escalation treatment as the e2e fixture.

## Reliability follow-ups  (residuals from the 2026-07-01 reviews)

- [ ] Focus snapshot-claim still adopted when `focused()==null`
  (`web/shell/src/wm.ts:465-473`, kernel `web/shell/src/wm-focus.ts`) —
  residual of the 5523ef3 cross-origin focus fix. **Left deliberately**:
  the hazard is stealing focus from work in progress, and when nothing is
  focused there is nothing to steal. Revisit only on a real complaint. The
  minimized half of this entry is fixed.
- [ ] Bundles re-shipped + re-imported on every live reconnect (fresh
  `bundleSent` per ShellSession, `internal/router/shell_session.go:43-49`,
  cleared at `:138`) — harmless (defineWashApp guards redefinition) but
  wasted bandwidth + a scary "bundle FAILED" log on slow links. Needs a
  per-client rather than per-session ledger.
- [x] **Control-socket last-binder-wins collision** — FIXED 2026-08-24,
  shipped in 0.14.0. A router now steps aside onto a per-pid path when the
  configured socket still ANSWERS (a stale file is still reused), resolves
  that before bring-up so no app is handed a path we are about to abandon,
  and unlinks on shutdown only when the path still names its own socket.
  The per-session default path in the original sketch turned out to be
  unnecessary: not stealing and not orphaning removes the damage, and
  keeping the well-known name for whoever got there first is what makes a
  bare `wash launch` keep working.

## Apps / UX

- [ ] **Apps sweep follow-ups (docs/Review-findings.md, 2026-09-08).** P0 and
  P1 shipped in 0.14.4; the P2 "missing everyday workflows" lists are the
  open backlog for fm, edit, term, agent and the cross-app seams. The
  terminal-intercept tier was deleted (docs/AGENT_APP.md §10), so `claude`
  in a wash terminal is an ordinary command; agent features go through the
  Agent app over ACP.
- [x] **Agent UX phase Now (N1–N6)** — docs/AGENT_UX.md, shipped
  2026-08-21. Focus-or-launch everywhere, agentd needs-input toasts keyed
  to their session, hidden-sidebar badge, single-click reattach, launcher
  defaults (last-used, else claude), window attention flag. See §5 for the
  as-built notes, including the two places the plan was wrong.
- [ ] **Agent UX phase Next (0.15)** — messenger consolidation. Design
  doc landed 2026-08-24: **docs/AGENT_MESSENGER.md**, M1–M5. The list
  merge (live + stored, one row, one search) is M1 and wants the three
  time representations reconciled first; one window per host is M2 and
  needs BOTH spawn paths made instancing-aware, since `EvtSpawnRequest`
  ignores `Instancing` entirely today (`internal/router/app_session.go:995-1010`
  calls `spawnChild` unconditionally, unlike the launch paths).
  **M5 shipped 2026-08-24**, ahead of M1 — the three defects this entry
  used to list as live are fixed (`web/lib/src/agent-status.ts:70-75`
  renders `failed` red; `apps/session/fe/src/sidebar/awareness.ts:254-259`
  counts only `working`). M1–M4 remain.
- [ ] **fm/edit: surface access-denied + "relaunch as root"** — **issue #6**
  (full implementation prompt is a comment there). Part A is now mostly
  done and the old description was stale: edit HAS a status-bar error
  surface (`statusError`, `apps/edit/fe/src/main.tsx:392-395`, 19 call
  sites incl. the permission-denied open), and fm DOES handle `read_err`
  (`apps/fm/fe/src/main.tsx:678`) — but routes it to the preview pane
  rather than the status bar, which is a weaker surface than the issue
  asks for. **Part B is the real remaining work**: `PrivSpawn` wiring
  (no match anywhere in `apps/fm` or `apps/edit`) + the confinement
  decision (a root spawn inherits
  `FSRoot` — decide whether confined deployments need an unconfined-root
  option before building). Invariant: fm/edit never declare
  `CapPrepareSpawn`.
- [ ] **fm expand-folder scroll anchoring** — WebKit/Safari only (no native
  `overflow-anchor`; Chromium/Firefox already pin). A JS polyfill fought
  Solid's async `<For>` timing and was backed out (2026-06-23). Revisit via
  an observer-based anchor only if Safari/iPad becomes a supported target.

## QoS / transport  (docs/QOS.md)

- [ ] **Un-chunked emitters defeat the class scheduler.** The scheduler is
  preemptive between frames and not inside one: `drainLoop` commits a whole
  frame to the socket before consulting the queues again
  (`internal/router/shell_session.go:1185`). `maxChunkBytes` caps that at
  32 KB (`internal/router/qos.go:306`) — but only for callers that go
  through `writeChunked`. The `app_msg` relay does not
  (`internal/router/app_session.go:697-709` hands a whole payload to
  `WriteCtrlClass`, bounded only by `wire.MaxPayload` = 16 MiB), so one
  large app message is a single non-preemptible write that stalls every
  class behind it, including Control. QOS.md §12.2 states the rule; nothing
  enforces it.
  - The obvious fix is NOT one-day work, as first estimated: `app_msg` rides
    the control channel, where one frame carries one whole encoded message
    with `FlagEnd` set, and `pkg/wire/frame.go:20-22` says plainly
    "fragmentation is reserved". Neither side implements reassembly. So
    chunking control messages is a wire feature (continuation frames + FE
    reassembly + version handling), not a call-site change.
  - Cheaper steps that need no wire change, in order: enforce a
    class-dependent cap in `Scheduler.Submit` (`qos.go:106`) so an
    oversized frame is at least *visible* instead of silent; lower
    `maxChunkBytes` for Bulk/Background only; per-app round-robin inside
    Bulk (QOS.md §2 defers it; `recordAppTx` already has the attribution),
    which is what users actually report as app-vs-app starvation.
- [ ] **Multiple WebSockets per traffic class — investigated, not doing.**
  All four classes share one socket (`internal/router/http.go:94`,
  `web/shell/src/ws.ts:226`), which three docs assert as architecture
  (ARCHITECTURE.md:107, WIRE.md:11, QOS.md §2). Splitting them fixes only
  TCP-level head-of-line blocking under loss — the least painful of the
  four mechanisms, and negligible on LAN. The cost is structural:
  `ShellSession` *is* the connection, a second socket is currently treated
  as a second tab (`router.go:1677` sends `ShellSuperseded`), and
  cross-socket ordering has no guarantee where QOS.md §12.3/§12.4 already
  record two outages caused by reordering *within* one pipe. Revisit only
  on a concrete lossy-WAN complaint; do the chunking item above instead.

## Backend structural debt  (docs/TECH_DEBT.md P2, docs/CORE_AUDIT.md §3)

- [ ] **`pkg/sdk/bus.go` struct→`map[string]any`→struct round-trip.**
  Collapse the BE↔FE decode path to a typed one. Architectural — touches
  every app's message decode.
- [ ] **2.4 `bus.Emit` swallow annotations.** 16 bare `_ = bus.Emit(...)`
  sites; either an `EmitLogged` helper or per-site "safe to drop" comments.
  Low value, annotation-only.
- [ ] **Workspace store retention (`internal/swarm/store.go`).** `State.Workspaces`
  and `State.Receipts` are append-only: an ended workspace keeps its whole
  message log (up to 10k × 32 KiB) forever, and `Store.change` clones,
  marshals and fsyncs the *entire* file on every MCP call. Cost therefore
  grows with everything the box has ever run, not with the live workspace.
  A retention policy has to keep what `workspace_configure` needs for QA
  resume — an ended workspace is still looked up by `QADocument.Path` when a
  final export failed — so pruning means keeping that projection state and
  dropping the message/QA-event bodies, not dropping the workspace row.

## Frontend structural debt  (docs/FE_REFACTOR_PLAN.md)

- [ ] **FE monolith slimming (Phase 5).** `apps/fm/fe/src/main.tsx` (4799
  lines) and `edit` (5030) — slim `App` to wiring + a `view/` split. The
  shared logic already moved to `@wash/fs-client`; this is the remaining big
  one. Its own dedicated effort.
- [ ] **`createEditState` (Phase 6).** Apply the state+controller split to
  `edit` (it shares the package but keeps an inline store).
- [ ] **(optional) Phase 7** — same playbook for `session` / `top`.

## Interaction / visual consistency  (docs/INTERACTION.md)

The hover/press/focus sweep landed the layer, the guard and the shared
`<Tab>`. What it deliberately left:

- [ ] **`<select>` has no hover treatment.** Replaced elements cannot render
  the `::after` overlay the layer draws, so `panel-kit`'s `Select` is the one
  control the sweep does not reach. Needs either a custom listbox or a
  bespoke rule.
- [ ] **114 raw `<button>`s that could be `<Button>`.** They all carry the
  interaction layer now, so this is appearance-only drift (padding, radius,
  font) rather than a missing-state bug. Worth folding in per app, biggest
  first — `edit`, `net`, `term`, `fm`.
- [ ] **`SmallBtn` and `Button` are two small-button styles.** `panel-kit`'s
  `SmallBtn` predates `Button variant="ghost" size="sm"`; fold it in and keep
  the name as an alias so the settings panels don't churn.
- [ ] **wash-net's underline section nav** is a third tab idiom (alongside
  `<Tab>` and the sidebar's icon rail). Fine as-is, but if a second app wants
  section tabs it should become a shared `<SectionTabs>` rather than a copy.

## Won't-do / deliberate no-ops (recorded so they don't get re-flagged)

- **fm trash** (decided 2026-09-08) — delete stays `os.Remove`/`RemoveAll`
  behind the confirm dialog; no trash folder, no restore. Undo for
  move/rename/paste is a separate question and still open.
- Hand-rolled insertion sorts (`cmd/wash/main.go`, `runtime_stats.go`) —
  intentional, avoids importing `sort` for two lines.
- `fm-replace.spec.ts` symlink test asserts `existsSync` only — by design.
- CORE_AUDIT 2.3 divergence traps (sparkline, Overlay screen-scope, token
  subset) — deliberate, see docs/CORE_AUDIT.md §2.
- `wash new-app` scaffold — deferred until a new app actually needs it.
- **H6 Xwayland `-auth`** (decided 2026-07-03) — inherited wlroots limitation
  (shared with sway), not an Xwayland bug; only bites shared-host-shared-netns
  multi-user and isn't closeable by a compositor patch (the X socket is
  abstract/netns-global). Mitigable via per-user netns at the privileged
  spawn layer IF that deployment ever materializes.
- **H4 presentation-time** (decided 2026-07-03) — wash captures surfaces, not
  the scene, so real feedback isn't cheaply derivable; browsers fall back to
  frame callbacks fine; half-advertising risks mpv waiting forever. Revisit
  only on a concrete A/V-sync complaint.
- **Qt popover classifier over-match (#5)** — investigated twice; no robust
  untitled-menu-vs-dialog signal exists (app_id / decoration mode / min-max /
  commit timing / content probing all rejected); a titled dialog already
  stays a window.
- **Nested serial-less Qt submenu chaining** (`TODO` in
  `toplevel_setup_popover`) — low value; the primary classifier covers the
  common case.
- **Touch input** — the FE only synthesizes pointer events.
