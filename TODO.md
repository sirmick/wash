# wash — TODO / backlog

The one backlog file, grouped by theme. Detailed designs and implementation
prompts live in `docs/` and on the linked GitHub issues; resolved items are
DELETED (git history is the archive), never kept as struck-through history.

Last consolidated: 2026-07-03 — every entry below was re-verified still-open
against git history and the current tree. (That sweep deleted the 2026-07-01
review docs + fix prompts, MAKE-PLAN, PACKS-PROMPT, NEXT, and the sftp-mount
bug list — all fully landed; see `git log` if you need their content.)

---

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
- [ ] **M4/M5 — multi-host services**: notify/bulk/priv merge + priv host
  attribution; clipboard sync hub; cross-origin z-band (focused-host windows
  on top, below chrome z 9999/10000).
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

- [ ] **Keep `docs/FLAKE_LOG.md` current** — the dated record of flakes actually
  seen, each A/B'd against its pre-change baseline so "my branch broke it" is a
  finding, not a guess. 2026-07-29: the display capstone tier fails ~2 runs in 3
  under parallel load on BOTH sides of a feature branch (C5 / issue #7), and the
  `display-term-xclock` mechanism turns out to be A10 (a stale `env.publish`
  match → no `DISPLAY` at pty spawn), not C5's lost-keystroke diagnosis.

- [ ] **Execute the phased plan in docs/TEST_FLAKES.md** (~75 verified items,
  written for a smaller LLM): Phase A e2e harness/process lifecycle
  (readiness lines logged before bind, leak-on-throw, no process-group kill,
  env scrub, hardlink staging, freshness/teardown guards); Phase B Go-unit
  races (the t.Logf-after-test class ×16 files, TestSpine byte-count —
  tracked as **issue #8**, OpenWRT qemu pdeathsig); Phase C e2e spec sweeps
  (stale timeout overrides, fs-assert barriers, persist-before-reload);
  Phase D FE unit; Phase E the **test event bus** (control-socket
  `wait_event` + FE settle hook + guest `WASH-EVENT` lines) so tests drive
  state machines instead of timing. Open flake trackers: **#7**
  (display-input-smoke), **#8** (TestSpine).
- [ ] After phases B1–B3: trial dropping `-p 1` from the unit gate (it exists
  to dampen the loopback scheduling race).
- [ ] After phase A6 (hardlink staging): measure control-socket RTTs and walk
  the fixture's 12s band-aid back toward 5s so timeouts are signal again.
- [ ] **Packaging boot-smoke leaks routers** (two 9h orphans found
  2026-07-03 on ports 11081/11082): give the run_matrix boot-smoke/serve
  paths the same group-kill + escalation treatment as the e2e fixture.

## Reliability follow-ups  (residuals from the 2026-07-01 reviews)

- [ ] Focus snapshot-claim still adopted when `focused()==null` or the
  claimed window is minimized (`web/shell/src/wm.ts:330-338`) — residual of
  the 5523ef3 cross-origin focus fix.
- [ ] login first-spawn flock doesn't re-`List` under the lock
  (`internal/login/spawn.go:146-155` vs `server.go:239-248`) → two
  concurrently-reconnecting tabs can spawn two routers (silent session
  duplication, not a wedge).
- [ ] Bundles re-shipped + re-imported on every live reconnect (fresh
  `bundleSent` per ShellSession) — harmless (defineWashApp guards
  redefinition) but wasted bandwidth + a scary "bundle FAILED" log on slow
  links.
- [ ] `connect()` from async `reconnectTick`: a throwing factory becomes an
  unhandled rejection that permanently kills the reconnect loop (unreachable
  with the stock factory; cheap to guard).
- [ ] `pendingRaw` buffers unboundedly for a channel that never gets a
  subscriber (`web/shell/src/api.ts:199-204`).
- [ ] `ListenControl` (`internal/router/control.go:88`) doesn't join
  per-connection handler goroutines on shutdown — same hazard class as the
  fixed `runRawListener` (42d6698); latent today, one log line away from the
  t.Logf-panic class.

## Apps / UX

- [ ] **Agent-aware terminals: follow-ups** — docs/AGENT_TERM.md. **M1–M5
  are all DONE**, which closes **issue #19** (item 1 close-confirm shipped
  earlier, item 2 by M3's policy engine, item 3 by M5's smart paste). What
  is deliberately left: the Agents settings pane could become a real
  define-settings-panel owned by agentd, with a hook-install toggle
  replacing the CLI-only path (§9.3); the remote roster merge rides
  REMOTE.md §6.2; hook adapters for Codex/Gemini/Aider (§11) now that the
  shape is proven on Claude Code; and the wider §11 non-goals (prompt
  library, `--resume` orchestration) remain non-goals until asked for.

- [ ] **fm/edit: surface access-denied + "relaunch as root"** — **issue #6**
  (full implementation prompt is a comment there). Part A: status-bar
  surfacing (edit has no error surface at all; fm misses `read_err`); Part
  B: `PrivSpawn` wiring + the confinement decision (a root spawn inherits
  `FSRoot` — decide whether confined deployments need an unconfined-root
  option before building). Invariant: fm/edit never declare
  `CapPrepareSpawn`.
- [ ] **fm expand-folder scroll anchoring** — WebKit/Safari only (no native
  `overflow-anchor`; Chromium/Firefox already pin). A JS polyfill fought
  Solid's async `<For>` timing and was backed out (2026-06-23). Revisit via
  an observer-based anchor only if Safari/iPad becomes a supported target.

## Backend structural debt  (docs/TECH_DEBT.md P2, docs/CORE_AUDIT.md §3)

- [ ] **`internal/sdk/bus.go` struct→`map[string]any`→struct round-trip.**
  Collapse the BE↔FE decode path to a typed one. Architectural — touches
  every app's message decode.
- [ ] **2.4 `bus.Emit` swallow annotations.** 14 bare `_ = bus.Emit(...)`
  sites; either an `EmitLogged` helper or per-site "safe to drop" comments.
  Low value, annotation-only.

## Frontend structural debt  (docs/FE_REFACTOR_PLAN.md)

- [ ] **FE monolith slimming (Phase 5).** `apps/fm/fe/src/main.tsx` (~3.6k
  lines) and `edit` (~3.3k) — slim `App` to wiring + a `view/` split. The
  shared logic already moved to `@wash/fs-client`; this is the remaining big
  one. Its own dedicated effort.
- [ ] **`createEditState` (Phase 6).** Apply the state+controller split to
  `edit` (it shares the package but keeps an inline store).
- [ ] **(optional) Phase 7** — same playbook for `session` / `top`.

## Won't-do / deliberate no-ops (recorded so they don't get re-flagged)

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
