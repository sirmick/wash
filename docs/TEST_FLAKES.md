# Implementation prompt: fix the test-suite race conditions and flakes (2026-07-03 audit)

You are hardening the wash test suites against race conditions, flakes, order dependence, and
cross-test contamination. This document is self-contained: every item carries its own
file:line anchors, failure mechanism, and mechanical fix. It came out of a full audit of all
183 Go test files, all 92 Playwright specs, the 5 e2e fixtures, and the 39 FE unit tests,
cross-checked against CI history and local stress runs.

**Evidence baseline (2026-07-03):**

| When | Where | Symptom | Class |
|---|---|---|---|
| 2026-07-02 CI | `priv.spec.ts:240` | `connect ENOENT /tmp/wash-e2e-apps-*/control.sock` | A2 startup race |
| 2026-07-02 CI | `display.spec.ts:145` | `locator.hover` 25s timeout | display family (C5) |
| 2026-07-03 local | `display-guest.spec.ts:43` | 26s timeout (412 passed / 1 failed / 8 skipped, 2.9m) | display family (C5) |
| 2026-06-29 CI | `make test-race` | `WARNING: DATA RACE` in `internal/runner/router` — `t.Logf` from `HandleShell` goroutine vs test completion | B1 class (that one instance since fixed by 42d6698; 15 sibling files remain) |
| 2026-06-26 CI | `display-qt-popover.spec.ts:46` | hard-FAIL "missing wash-display" on a release-bump commit | A7 skip-vs-fail |
| 2026-06-25 CI | `fm-download.spec.ts:27` | `expect(received).toBe(expected)` on a release-bump commit | C-class barrier |
| 2026-07-03 local repro | `internal/loopback` `TestSpine` | `bundle bytes mismatch: ""` + `panic: Log in goroutine after TestSpine has completed` under CPU load (`(go build -a ./internal/... &); go test -run 'TestSpine$' -count=20 ./internal/loopback`) | B1 + B2 |

Local stress otherwise green: `go test -shuffle=on` ×2 (only TestSpine fired, load-induced not
order-induced), `-race -count=3` over the 14 concurrency-dense packages, fe-unit ×5, vitest ×2.

## Ground rules (do not skip)

1. **One worktree branch per phase** under `branches/` (`git -C /home/mick/wash worktree add
   branches/fix-test-flakes-a -b fix-test-flakes-a`; phase B → `…-b`, etc.), merged back to
   local `main` when the phase is fully green, worktree removed before the next phase.
   Remember: `e2e/` is not a pnpm workspace member — in a fresh worktree run
   `pnpm install --ignore-workspace` inside `e2e/`.
2. **Green gate**: commit only on `make build` + `make unit-test` green; before merging a phase
   run `make test-race` and `make e2e-test` (NOT raw playwright — the login fixture needs the
   multicall layout). Do not push.
3. **One numbered item = one commit**, message style `test(<area>): <what>` or
   `fix(<component>): <what>` for product-side changes, matching the existing log.
4. **Philosophy (from playwright.config.ts and the Makefile): no added retries, no blind
   sleeps.** Every fix is one of: await a concrete event/signal; deadline-poll an observable
   (`expect.poll` / `expect(...).toPass` / Go `waitFor` idiom); widen an *event-driven* wait
   that was tuned before the current load profile; isolate state (per-test dirs, `:0` ports);
   join/kill on teardown. If an item says "raise 5_000 to 12_000" that is widening a poll on a
   real signal, not adding a sleep.
5. **Do not refactor product code beyond the named seams.** Product-side changes in this doc
   are: post-bind log lines (A1), `HTTPServer.RunListener` split (B19), `resolveFQDN` bounded
   resolver (B27), mdns `noSelfFilter` option (B29), `setPdeathsig` on OpenWRT VMs (A11), the
   Phase E event surface. Everything else is test/fixture-only.
6. If a fix turns out larger or riskier than described, stop and ask instead of improvising.
7. Verify commands are per-item. E2e verify commands assume `cd /home/mick/wash/e2e` with
   binaries fresh (`make e2e-test` at least once first) and `WASH_E2E_MULTICALL=1`.

---

## Phase A — e2e harness + process lifecycle (branch `fix-test-flakes-a`)

The harness has a self-amplifying failure loop: a setup throw leaks a running router (+ its
inotify watchers), which slows the box, which causes more setup throws. These items break the
loop and make startup event-driven. Biggest blast radius; do them first, in order.

### A1 [P0] Routers announce "listening on" before binding — log the RESOLVED address post-bind; fixture uses `:0`
- Product: `internal/runner/router/router.go:543` logs `listening on %s` with the *configured*
  `cfg.Listen` string **before** `HTTPServer.Run` actually binds (`internal/router/http.go:273`
  area). `internal/runner/login/runner.go:329` has the same premature announce.
- Fixture: `e2e/fixtures/router.ts:183-199` `freePort()` picks a port, closes it, and hands it
  to the router — a TOCTOU across 8 workers; a collision makes the router exit
  "address in use" *after* the readiness line already matched.
- Fix (product): in `HTTPServer.Run`, immediately after `lc.Listen(...)` succeeds, add
  `s.router.log("http listening on %s", listener.Addr())`. Add the equivalent post-bind line in
  wash-login's runner. Keep the existing lines (com.wash.remote greps `listening on` —
  `internal/runner/router/router.go:638` comment — do not reorder or reword them).
- Fix (fixture): in `startRouter`, when `opts.port` is unset pass `--listen 127.0.0.1:0`, drop
  `freePort()`, change `waitForRegex` to return the match array, wait for
  `/http listening on 127\.0\.0\.1:(\d+)/` and parse the port from the match. Keep `opts.port`
  honored for reconnect.spec's same-port restart.
- Verify: `go test ./internal/router/... && cd e2e && pnpm exec playwright test tests/chrome.spec.ts --workers=8 --repeat-each=20`

### A2 [P0] Fixture readiness must include the control socket (CI-confirmed: priv.spec ENOENT)
- `internal/runner/router/router.go:550` starts the control socket in a goroutine *after* the
  HTTP announce; the socket file appears at `internal/router/control.go:62` and logs
  `control socket listening on %s` at `control.go:72`. The fixture (`router.ts:389-395`) only
  waits for the HTTP line, so a spec's first `controlRequest` can dial a not-yet-existing
  socket → `connect ENOENT` (CI 2026-07-02).
- Fix: in `startRouter`, wait for BOTH lines:
  `Promise.all([waitForRegex(logBuf, /http listening on /), waitForRegex(logBuf, /control socket listening on /)])`
  raced against `exitPromise` as today. Also give `controlRoundtrip` (router.ts:107) a short
  ENOENT/ECONNREFUSED retry loop (250ms interval, 5s cap) and append the last ~2000 chars of
  the router log to its timeout/connection errors so a failure is diagnosable.
- Verify: `cd e2e && pnpm exec playwright test tests/fm-be.spec.ts tests/priv.spec.ts --workers=8 --repeat-each=10`

### A3 [P0] Setup-throw leaks the spawned router (all four fixtures) — kill + rm on the failure path
- `router.ts`: every throw after `spawn` at :377 (notably the 5s listen-wait timing out under
  load) leaks the running router + staged 21MB appsDir + fmRoot + xdgConfigHome — the fixture
  body never reaches its try/finally. Same shape in `login.ts:188-193`, `vm.ts:70-88`,
  `remote-vm.ts:64-82` (URL-promise rejection → `use` never runs → teardown never reached).
- Fix: in `startRouter`, wrap everything after `const proc = spawn(...)` in try/catch; on
  catch: `try { proc.kill('SIGKILL') } catch {}`, `killProcsUnder(appsDir)`, `rmSync` each of
  appsDir/fmRoot/xdgConfigHome `{recursive:true, force:true}`, rethrow. Raise the listen-wait
  deadline 5s → 15s (deadline on an event). Same try/catch-kill in `startLogin`. In
  `vm.ts`/`remote-vm.ts`, wrap from the URL promise through `use` in try/finally with the
  existing kill/close logic in the finally.
- Verify: wrap the router binary in a `sleep 6 && exec` shim, run one spec, then
  `pgrep -af wash-e2e-apps` → nothing survives.

### A4 [P1] No process-group story — detached spawn + escalated, two-phase teardown
- `router.ts:563-581`: teardown SIGTERMs one pid, SIGKILLs after 1.5s, then `killProcsUnder`
  **immediately SIGKILLs** anything whose argv contains appsDir — which forecloses
  `sdk.OnTerminate`'s group-kill, orphaning grandchildren whose argv does NOT contain appsDir
  (wash-display's Xwayland, vscode's code-server). The router is not `detached`, apps get no
  `Setpgid` (`internal/router/spawn.go:53`), so there is no group to kill.
- Fix: (a) `spawn(ROUTER_BIN, args, { ..., detached: true })`; (b) stopRouter:
  `try { process.kill(-h.proc.pid, 'SIGTERM') } catch { h.proc.kill('SIGTERM') }`, poll
  `/proc/<pid>` gone ≤4s, then `process.kill(-h.proc.pid, 'SIGKILL')` (catch ESRCH);
  (c) make `killProcsUnder` two-phase: SIGTERM matches, poll gone ≤2s at 50ms, SIGKILL
  survivors.
- Verify: `cd e2e && pnpm exec playwright test tests/settings.spec.ts && pgrep -af 'Xwayland|wash-display|code-server'` → empty.

### A5 [P1] Dev-shell environment flows unfiltered into every router and app — scrub `WASH_*`, default xdgConfig on
- `router.ts:319` `{ ...process.env }`: a stray `WASH_FM_ROOT`/`WASH_FS_ROOT` in the dev shell
  becomes the global fs sandbox for the entire gate
  (`internal/runner/router/router.go:243` firstNonEmpty chain); without `opts.xdgConfig`,
  wash-session reads and **fs-watches the developer's real `~/.config/wash`**
  (`apps/session/be/config.go:186-207`) — ~60 specs render under whatever pack the dev last
  picked, and every concurrent router holds a watch on the same real config dir.
- Fix: after building `env`, scrub: `for (const k of Object.keys(env)) if (k.startsWith('WASH_') && !k.startsWith('WASH_E2E_')) delete env[k];`
  (the fixture re-adds its own WASH_* below; `extraEnv` still applies last). Second commit:
  flip `xdgConfig` to default-on so XDG_CONFIG_HOME is per-test everywhere; watch
  term-menubar/viewport/chrome-windows for computed-color assertions that relied on host theme
  (none found asserting on it, but run those three first).
- Verify: `WASH_FM_ROOT=/nonexistent make e2e-test` → green (before the fix dozens of fm/edit specs fail).

### A6 [P1] Per-test 21MB binary copies are the load behind the 12s control-socket band-aid — hardlink instead
- `router.ts:207-208` copies the multicall binary into every per-test dir: ~407 tests ≈ 8+GB
  of /tmp writes per run from 8 workers, and every launch execs a cache-cold binary. The copy
  exists so `/proc/<pid>/exe` matches the apps-dir path — a **hardlink preserves that**.
- Fix: in `stageApps`: `try { linkSync(washBin, washDest); } catch { copyFileSync(washBin, washDest); chmodSync(...); }`
  (EXDEV fallback; skip chmod on the link path). Same for the ALWAYS_OUT binaries. If /tmp is
  tmpfs on the dev box, stage under `out/.e2e-stage-<mkdtemp>` instead and add that prefix to
  the sweep patterns.
- After it lands, instrument once (`console.error('ctl rtt', Date.now()-t0)` in
  `controlRoundtrip`), run `make e2e-test` 3×, and if >5s round-trips have collapsed, drop the
  12s default back toward 5s so the timeout is signal again (separate commit).
- Verify: `time make e2e-test` before/after; `tests/terminal-attach.spec.ts` still green (the /proc/exe consumer).

### A7 [P1] Guards are preflight-only — add global-teardown sweep, screenshots-config guard, binary freshness, stale-display skip
- `e2e/global-setup.ts:73-84` checks inotify **instances** (correct metric) but only at suite
  start; `playwright.screenshots.config.ts` has no globalSetup at all; there is no
  globalTeardown; nothing checks binary freshness (`router.ts:237-242` and
  `displaySkipReason` at `router.ts:74-79` are existence-only, so a stale wash-display
  hard-FAILS the 5 display specs — CI 2026-06-26).
- Fix: (1) `global-teardown.ts`: scan /proc for cmdlines containing `/tmp/wash-e2e-`; if any:
  SIGTERM, 2s, SIGKILL, `console.warn` count+argvs. Run the same sweep at the TOP of
  global-setup (stale orphans hold the instances the counter then measures). (2) Reference the
  same globalSetup/teardown from playwright.screenshots.config.ts. (3) Freshness preflight in
  global-setup: `find apps cmd internal web -name '*.go' -o -path '*/src/*.ts*' -newer out/wash -print -quit`
  non-empty → fail "out/wash stale — run make e2e-test, not raw playwright" unless
  `WASH_E2E_ALLOW_STALE=1`. (4) `displaySkipReason`: also return a skip when
  `out/wash-display` is older than the newest file under `wash-display/src`.
- Verify: `touch apps/fm/be/app.go && cd e2e && pnpm exec playwright test tests/chrome.spec.ts` → fast stale-binary failure; unset → green.

### A8 [P2] login fixture: `execSync('cat …')` per /proc entry + LAN mDNS advertising from tests
- `login.ts:106-112` shells out `cat` twice per process scanned (hundreds of spawns per
  teardown, at the loaded moments); wash-login also advertises `_wash._tcp` on the real LAN
  from every login/auth spec (`internal/runner/login/runner.go:341-346`).
- Fix: replace both `execSync` with `readFileSync` in try/catch; spawn login with
  `WASH_DISCOVERY_NO_ADVERTISE: '1'` (confirm the env name against
  `internal/mdns`/runner usage; discovery.spec.ts:39 shows the hermetic pattern).
- Verify: `cd e2e && pnpm exec playwright test tests/login.spec.ts tests/auth-harden.spec.ts`.

### A9 [P2] vm/remote-vm fixtures: missing `error` handler crashes the worker; no SIGKILL escalation leaks qemu
- `vm.ts:61,92-97`, `remote-vm.ts:54,86-91`: no `proc.once('error', ...)` (missing binary =
  unhandled event = worker crash), and teardown gives up after SIGTERM+5s without SIGKILL.
- Fix: add `proc.once('error', (e) => reject(e))` into the URL promise; teardown:
  SIGTERM → await close ≤5s (8s remote) → `try { proc.kill('SIGKILL') } catch {}` → await ≤2s.
- Verify: `mv out/washvm-run{,.bak}; cd e2e && pnpm exec playwright test tests/net-vm-gate.spec.ts --workers=1` → clean skip; restore.

### A10 [P2] `waitForLog` matches from t=0 — add a cursor so "the save I just triggered" is expressible
- `router.ts:407` scans the whole buffer; every "wait for MY persist" barrier silently matches
  the first stale occurrence (bites C3 below). Until Phase E replaces log-greps entirely:
- Fix: add to the handle: `logMark: () => number` (returns `logBuf.length`) and
  `waitForLogSince: (mark, re, timeout?) => waitForRegex(() => logBuf.slice(mark), re, timeout)`,
  plus `logCount: (re) => (logBuf.match(re) ?? []).length`.
- Verify: type-check + used by C3.

### A11 [P0] OpenWRT qemu VMs get no pdeathsig — orphan VMs poison later runs
- `wash-vm/vm/openwrt.go:66` builds the qemu command without the `setPdeathsig(cmd)` that
  `vm.go:162` applies (whose comment credits it with ending orphan accumulation). A test-binary
  timeout panic (defers skipped) or Ctrl-C strands 2-4 qemus per multivm test, which then squat
  their mcast segments and inotify instances.
- Fix: add `setPdeathsig(w.cmd)` immediately after the `exec.CommandContext` at openwrt.go:66.
- Verify: start `TestMultiVMSegment`, `kill -9` the test binary at ~40s, `pgrep -af 'qemu-system.*wash-owrt'` → empty.

---

## Phase B — Go unit tests (branch `fix-test-flakes-b`)

### B1 [P0] `t.Logf`-as-router-logger + unjoined goroutines: panics whole packages, failed CI race gate
Sixteen test files hand `func(f string, a ...any) { t.Logf(...) }` closures to
`NewRouter`/`runRawListener`; router goroutines (`ShellSession.drainLoop`,
`HandleShell`/`HandleApp`) can log after the test returns → data race under `-race`
(CI 2026-06-29) or `panic: Log in goroutine after Test… has completed` (reproduced locally,
kills the whole package binary mid-run). `internal/loopback/pty_class_test.go:47-50` documents
the panic and fixed itself with a no-op logger; the fix never propagated.
- Files: internal/router/{multiwindow,control_dispatch,unix_listener,spine,http_tls,env_publish,notify_forward,peer,credit_integration,peer_nesting,raise_onlaunch,restart}_test.go;
  internal/runner/router/{router,listen_raw}_test.go; internal/loopback/{spine,qos_soak}_test.go.
- Fix: add one helper (suggest `internal/wiretest/logf.go`, package already exists and is
  imported by these tests — export `func SafeLogf(t *testing.T) func(string, ...any)`):
  ```go
  func SafeLogf(t *testing.T) func(string, ...any) {
      var mu sync.Mutex
      done := false
      t.Cleanup(func() { mu.Lock(); done = true; mu.Unlock() })
      return func(f string, a ...any) {
          mu.Lock(); defer mu.Unlock()
          if !done { t.Logf(f, a...) }
      }
  }
  ```
  (t.Cleanup runs before the test is marked complete; the mutex provides the happens-before.
  Post-cleanup logs are dropped — they were teardown noise.) Sweep all 16 files replacing the
  inline closures with `wiretest.SafeLogf(t)`.
- Verify: `go test -race -count=10 ./internal/router ./internal/runner/router ./internal/loopback`
  and the B2 repro command no longer panics.

### B2 [P0] loopback TestSpine asserts a wire ordering the product explicitly does not guarantee (reproduced)
- `internal/loopback/spine_test.go:295-298`: `frameReader` sets `bundleDone` on
  `ShellChannelUnbind`. The router sends bundle payload at **ClassBulk** but bind/unbind at
  control class; `internal/router/router.go:1362-1364` documents "Size lets the shell complete
  the bundle on byte-count, so Bulk-class data frames can't be overtaken by the
  higher-priority Unbind" — i.e. unbind-overtakes-payload is expected scheduler behavior. Under
  CPU load the test sees unbind first → `bundle bytes mismatch: ""` (spine_test.go:198).
  This race class is *why* the unit gate carries `-p 1`.
- Fix: mirror the real shell — complete on byte-count. In `frameReader` add
  `bundleExpect int64`; in the `wire.ShellChannelBind` case record `fr.bundleExpect = v.Size`;
  in the raw-frame branch, after `append`, set
  `fr.bundleDone = fr.bundleExpect > 0 && int64(len(fr.bundleBytes)) >= fr.bundleExpect`;
  keep the Unbind case as a no-op (or assert bytes are already complete when it arrives).
- Verify: `(go build -a ./internal/... >/dev/null 2>&1 &); go test -run 'TestSpine$' -count=50 ./internal/loopback` → green under load.
- Follow-up (note in TODO.md, not this pass): with B1+B2+B3 landed, trial removing `-p 1` from
  the unit gate for a parallel speedup.

### B3 [P1] loopback: join router goroutines on ALL paths; race-scale the p99 cap
- `qos_soak_test.go:82-101`: never joins `routerAppDone`/`routerShellDone`, never closes the
  pipes — drainLoop's exit log races test completion (B1's panic) and leaked readers park
  forever. Fix: right after spawning the handlers, register
  `defer func() { cancel(); _ = appPair.EndA().Close(); _ = shellPair.EndA().Close(); <-routerAppDone; <-routerShellDone }()`
  and drop the ad-hoc teardown at :268-275 (keep `<-bulkDone`).
- `spine_test.go:93,217-235`: joins exist only on the happy path; any mid-test `t.Fatalf`
  leaves them unjoined. Fix: move the joins into a defer registered after spawning (2s cap per
  join, `t.Error` on timeout), delete the in-body join block.
- `qos_soak_test.go:56,263`: the 50ms p99 latency cap is the subject of the test but is eaten
  by -race's 2-10x slowdown. Fix: add `//go:build race` / `//go:build !race` files defining
  `const raceEnabled bool`; use `cap := p99Cap; if raceEnabled { cap *= 4 }`.
- Verify: `go test -race -count=20 -run 'TestQoSSoak|TestSpine' ./internal/loopback`.

### B4 [P2] sdk: negative 150ms windows → deterministic FIFO probes
- `stateservice_test.go:160-170,262-271` and `watchclient_test.go:71`: "no frame within 150ms
  → pass" — sound only while SDK writes stay synchronous; silently vacuous under load.
- Fix (stateservice): after the Mutate, write a `Subscribe` probe from a new instance
  (`i-probe`) and read the reply with the existing To-instance-checked helper — pipe FIFO means
  any stray fan-out would arrive first and the helper fatals. Delete the timed selects.
- Fix (watchclient): after the untrusted event, send a trusted event for `/d/y` and assert the
  next `<-got` is `/d/y` (FIFO on the single dispatch goroutine). Delete the 150ms select.
- Verify: `go test -race -count=10 -run 'TestStateService|TestWatchClient' ./internal/sdk`.

### B5 [P3] sdk: TestCallbackOnMappedAndCloseConfirm leaks its Conn — `sdk_test.go:174`: use the
`connectForTest(t, pp)` idiom from window_test.go:15-35 + `defer c.Close()`.
Verify: `go test -race -count=10 -run 'TestCallbackOnMappedAndCloseConfirm' ./internal/sdk`.

### B6 [P2] login: TestRootLiveSessionServesShell fails on fresh worktrees (no staged shell assets)
- `root_session_test.go:45` requires 200, but `server.go:626-630` 302s to `/sessions` when
  `internal/shellassets/assets/index.html` isn't staged. `login_test.go:170-189` already
  accepts both.
- Fix: accept 200, or 302 with `Location == "/sessions"`; explicitly fatal on
  `/sessions?err=session+ended` (the dead-session behavior the test distinguishes).
- Verify: `go test -run 'TestRootLiveSession' ./internal/login` in a bare worktree and after `make test-app`.

### B7 [P3] login: shared built-router temp dir never removed — `handoff_integration_test.go:47`:
add a `TestMain` that `os.RemoveAll(filepath.Dir(testRouterBinPath))` after `m.Run()`.
Verify: run the two handoff tests; `ls -d /tmp/wash-router-test-bin-*` → none.

### B8 [P2] loopback: blocking `ReadFrame` defeats loop deadlines → 120s package hangs
- `pty_class_test.go:107,138-161`: wall-clock deadlines only checked between frames; a stall
  hangs to the package timeout, taking all diagnostics with it. spine_test.go:22-38 already has
  `readFrameWithDeadline`.
- Fix: replace both bare `shell.ReadFrame()` calls with `readFrameWithDeadline(shell, 5*time.Second, …)`.
- Verify: `go test -race -count=5 -run 'TestPTYOutputRidesBulkClass' ./internal/loopback`.

### B9 [P1] router: qos_integration bulk head-start is a blind 5ms sleep feeding an event-count bound
- `qos_integration_test.go:89,122`: sleep overshoot on a loaded box makes `bulkBefore` exceed
  the 192 bound → false fail. Fix: replace the sleep with a deadline-poll on
  `sess.scheduler.Depth(wire.ClassBulk) >= ClassQueueSize[wire.ClassBulk]` (3s cap, Fatalf with
  the depth on timeout).
- Verify: `go test -race -count=20 -run 'TestShellSchedulerControlBeatsBulk' ./internal/router`.

### B10 [P1] router: producer goroutine calls `t.Errorf` after failure paths → package-crashing panic
- `qos_integration_test.go:81`: on any early Fatalf, cleanup closes the scheduler, the producer
  gets `ErrSchedulerClosed` and logs into a dead `t`. Fix: `prodErr := make(chan error, 1)`;
  producer sends the error and returns (with `defer close(bulkSent)`); after the join,
  `select { case err := <-prodErr: t.Fatalf(...); default: }`.
- Verify: `go test -race -count=10 -run 'TestShellSchedulerControlBeatsBulk' ./internal/router`.

### B11 [P1] router: credit close-unblock test assumes the producer parked within 20ms
- `credit_integration_test.go:141`: if the goroutine hasn't reached `Reserve` when
  `closeChannel` runs, the write bypasses the (now-removed) binding and returns nil → genuine
  false fail at :150. Fix: `entered` channel closed as the goroutine's first statement;
  `<-entered`, then the file's own lines-49-54 "still blocked" select (50ms), then
  `closeChannel`.
- Verify: `go test -race -count=50 -run 'TestCreditChannelCloseUnblocksProducer' ./internal/router`.

### B12 [P3] router: unsynchronized captured vars in ingress tests (memory-model hygiene)
- `unix_listener_test.go:251` + the same pattern in `ingress_test.go`
  (`TestIngressProxy_HTTPStripAndHeader`): handler goroutine writes `gotPath`/`gotIngress`,
  test reads after the proxied response. Empirically race-clean ×5 under `-race` (stdlib
  `internal/poll` annotations bless fd IO), so hygiene only: guard with a `sync.Mutex` anyway —
  the blessing is an implementation detail of the stdlib.
- Verify: `go test -race -count=5 -run 'TestUnixListenerIngressHandoff|TestIngressProxy' ./internal/router`.

### B13 [P3] router: peer-UID rejection is "no 101 within 300ms" — make it event-bounded
- `unix_listener_test.go:224`: wait for the router's own completion signal first: read one byte
  from `ctl` with a 5s deadline — EOF proves `handleHandoff` finished (it defers `ctl.Close()`
  on every rejection path); then the existing 300ms negative read is deterministic.
- Verify: `go test -race -count=10 -run 'TestUnixListenerRejectsWrongPeerUID' ./internal/router`.

### B14 [P2] router: notify_forward — IdentityAck ≠ registered; unbounded read on the miss path
- `notify_forward_test.go:82`: poll `r.singletonInstance(NotifyAppID) != nil` (2s, 2ms interval)
  after `connectApp` before producing. In `readNotifyForward`, run `ReadFrame` in a goroutine
  feeding a channel and select against `time.After(timeout)` (pattern at :138-157) so a miss
  Fatals at 2s instead of hanging to the package timeout.
- Also `notify_forward_test.go:155` (loop-guard negative, 200ms): after the window, connect a
  second producer, emit an EvtNotify, and assert the FIRST forward received is that producer's
  (`From.AppID == "com.wash.about"`) — event-ordered teeth under arbitrary slowdown.
- Verify: `go test -race -count=20 -run 'TestRelayNotify' ./internal/router`.

### B15 [P3] router: resync "no force-frame" 300ms negative — bound it with a sentinel
- `resync_video_test.go:214`: after `r.resyncChannel(b)`, write `wire.NewEvtWindowFocus(8)` to
  the same app transport and read events until the focus sentinel arrives (readWithin), failing
  if an `EvtWindowForceFrame` appears first.
- Verify: `go test -race -count=20 -run 'TestResync_GenericKindNoForceFrame' ./internal/router`.

### B16 [P3] router: peer tests park goroutines on `<-make(chan struct{})` forever
- `peer_test.go:170,274`: `hold := make(chan struct{})`, `t.Cleanup(func() { close(hold) })`,
  goroutine does `<-hold; _ = c.Close()`.
- Verify: `go test -race -count=5 -run 'TestPeerRelayNoHeadOfLineBlocking|TestPeerRelayMaxSizeFrameSurvives' ./internal/router`.

### B17 [P2] router: FE-disconnect-unblocks test never actually blocks a producer
- `qos_integration_test.go:150`: 10 frames < 35-frame absorption capacity, so the scenario
  under test never executes (silent no-op). Fix: producer goroutine writes 64 frames with a
  `producerDone` channel; deadline-poll `Depth(Bulk) >= ClassQueueSize[Bulk]`; `fe.Close()`;
  after the existing cleanup-bound check, join `producerDone` with a 1s cap (`t.Fatal` if still
  blocked — the assertion the test's name promises).
- Verify: `go test -race -count=20 -run 'TestShellSchedulerFEDisconnectUnblocksProducers' ./internal/router`.

### B18 [P1] router: env_publish waits by iteration count, not time
- `env_publish_test.go:83`: 200 spins of `spawnEnv()` can complete before the HandleApp reader
  ever runs (the write only enqueues into the 32-slot pipe). Fix: deadline-poll (2s, 5ms sleep)
  in the shell_launch_test.go:80-90 idiom.
- Verify: `go test -race -count=50 -cpu 1 -run TestEnvPublishMergedIntoSpawnEnv ./internal/router`.

### B19 [P2] router: http_tls tests re-bind a freed port (TOCTOU) — hand the listener in
- `http_tls_test.go:21` freePort + later `HTTPServer.Run` re-bind. Fix (product seam): split
  `Run` in `internal/router/http.go` — everything after `lc.Listen` becomes
  `func (s *HTTPServer) RunListener(ctx context.Context, ln net.Listener) error`; `Run` calls
  `lc.Listen` then `RunListener`. Tests bind `127.0.0.1:0` themselves and pass the listener.
  (This also unlocks A1's post-bind log placement.)
- Verify: `go test -race -count=20 -run 'TestHTTPServerServes' ./internal/router`.

### B20 [P3] router: restart tests never close their app pairs — `restart_test.go:28-45`: make
`handshakeApp` register a `t.Cleanup` that closes the pair and joins `done` (2s cap).
Verify: `go test -race -count=5 -run 'TestAppRestart' ./internal/router`.

### B21 [P3] runner/router: session-splitter leaks + non-idempotent fake Close
- `session_splitter_test.go:34,47`: add `closeOnce sync.Once` to `fakeRaw.Close`, then
  `defer raw.Close()` in `TestSessionSplitterBasic` and `TestSessionSplitterTakeover`.
- Verify: `go test -race -count=10 -run TestSessionSplitter ./internal/runner/router`.

### B22 [P3] router: credit "still blocked" select never proves the goroutine started
- `credit_test.go:31`: add a `started` channel closed first thing in the goroutine, `<-started`
  before the select; after the timeout branch, assert `c.Sent()` unchanged.
- Verify: `go test -race -count=20 -run TestCreditReserveBlocksWhenExhausted ./internal/router`.

### B23 [P1] apps: connect-helper cleanup nils package singletons without joining the Run goroutine (×3 packages)
- `apps/notify/be/app_test.go:63-70`, `apps/fswatch/be/app_test.go:60-69`,
  `apps/netd/be/app_test.go:57-64`: `res.c.Close()` doesn't join the free `Run` goroutine; an
  in-flight handler reads `svc`/`hub`/`timer` while cleanup writes nil — a real race window
  (`make test-race` exercises it), plus netd's cleanup touches `ConfirmTimeout` without the
  package `mu`. Fix in all three connect helpers:
  ```go
  runDone := make(chan struct{})
  go func() { defer close(runDone); _ = res.c.Run(context.Background()) }()
  cleanup := func() { res.c.Close(); <-runDone; /* then the existing resets */ }
  ```
  (netd: also `timer.Stop()` under `mu`.)
- Verify: `go test -race -count=5 ./apps/notify/be ./apps/fswatch/be ./apps/netd/be`.

### B24 [P1] apps/fswatch: leaked reconnecting mount client redials — and can exec REAL ssh
- `app_test.go:60` cleanup replaces `mounts` without `router.Unmount`/`client.Close()`; the
  leaked `remotewatch.NewReconnectingClient` redials on a 100ms backoff, and once the fake
  dialer is defer-restored it execs `ssh … wash-fswatchd` (`apps/fswatch/be/app.go:163-181`).
  Fix: in cleanup (after `<-runDone`, before nilling): swap out the mounts map under
  `mountsMu`, then for each reg `router.Unmount(mp)` (or `reg.client.Close()`).
- Verify: `go test -race -count=2 ./apps/fswatch/be` → no `remotewatch: client dial` retries, no ssh children.

### B25 [P3] apps/notify: history-cap test's 50ms sleep is vacuous — make the 101st message
distinguishable (`Title: "last"`), then `waitFor` `len==HistoryCap && n[0].Title=="last"`;
delete the sleep. (`app_test.go:219-229`.)
Verify: `go test -race -count=1 -run TestNotifyHistoryCap ./apps/notify/be`.

### B26 [P2] apps: read-until-match helpers hang to the 120s package timeout on the miss path (×4 files)
- `apps/notify/be/app_test.go:126`, `apps/fswatch/be/app_test.go:86-115`,
  `apps/net/be/app_test.go:82-111`, `apps/netd/be/app_test.go:95-124`: wiretest `ReadFrame`
  blocks; the loop deadline is only checked between frames. Fix: first line of each helper:
  `w := time.AfterFunc(timeout, func() { _ = router.Close() }); defer w.Stop()` — Close
  unblocks ReadFrame with io.EOF and the existing Fatalf fires at the deadline.
- Verify: `go test -count=1 ./apps/notify/be ./apps/fswatch/be ./apps/net/be ./apps/netd/be`.

### B27 [P3] apps/session: `gatherSysInfo` does unbounded DNS lookups in the serial gate
- `apps/session/be/sysinfo.go:97-119` (`sysinfo_test.go:146` exercises it): give `resolveFQDN`
  a `context.WithTimeout(…, 2*time.Second)` + `net.Resolver` Lookup calls. Can't fail tests
  (errors fall through) but stalls offline boxes for the resolver retry budget.
- Verify: `go test -count=1 ./apps/session/be`.

### B28 [P3] apps/vscode: kill-test leaves the sleep-60 group alive if killChild regresses
- `server_kill_test.go:27`: after the Getpgid check add
  `t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })`.
- Verify: `go test -race -count=1 -run TestKillChild ./apps/vscode/be`.

### B29 [P2] mdns: integration test replaces `localIPs` while readLoop reads it (real data race, gate never sees it)
- `internal/mdns/integration_test.go:44` writes `srv.localIPs` after `New()` started readLoop
  (`mdns.go:167,254`); `make mdns-test` (Makefile:742) runs without `-race` and the unit gate
  skips it (env-gated), so it's invisible until it isn't. Fix: add unexported
  `noSelfFilter bool` to `Options`; readLoop drop-condition becomes
  `!s.opts.noSelfFilter && s.localIPs[...]`; test sets the option at construction and deletes
  line 44. Also add `-race` to the mdns-test target.
- Verify: `WASH_MDNS_INTEGRATION=1 go test -race -count=1 -run TestLoopbackDiscovery ./internal/mdns`.

### B30 [P2] wash-vm: in-guest blind sleeps papering the UCI reload-ordering bug
- `multivm_test.go:195,442,478,552`: `sleep 3` before fw4/dnsmasq restarts races async netifd;
  under host load the VLAN devices aren't up yet → unzoned firewall → assertion flips. Fix:
  replace each with bounded in-guest polls on the concrete precondition (the disks_test.go:71
  idiom), e.g. `for i in $(seq 1 60); do ip -4 addr show <nic> | grep -q 10.50.0.1 && break; sleep 0.5; done; /etc/init.d/dnsmasq restart`;
  for :478, fold the probe ping into a poll before the asserted `ping -c3`.
  (The real applier reload-ordering fix is tracked separately in TODO.md — these polls stay
  correct either way.)
- Verify: `stress-ng --cpu $(nproc) -t 600 & go test ./wash-vm/vm -run 'TestRouterServesDHCP|TestRouterVLANs|TestRouterMultiSegment' -count=2 -v` (needs images).

### B31 [P2] wash-vm: outer ctx budgets smaller than the sum of their inner polls; gate lacks -timeout
- `networkd_test.go:67,179`, `ifupdown_test.go:58,110`, `netplan_test.go:61,131`: 120s ctx
  wrapping 90s+30s polls dies mid-poll with an opaque deadline error. Fix: 240s for the
  Boot/Read tests, 300s for Apply tests; Makefile `vm-net-test` → `go test -timeout 40m …`.
- Verify: `make vm-net-test` on a loaded box (needs images).

### B32 [P3] wash-vm: mcast segments keyed by pid%1000 can splice concurrent runs
- `multivm_test.go:54,161,419,534`: fold the full pid into the group address:
  `group := fmt.Sprintf("230.99.%d.%d", (pid>>8)&0xff, pid&0xff)` (distinct final-octet base
  per test), keep the port formula.
- Verify: two simultaneous `go test -run TestMultiVMSegment` runs (with images).

### B33 [P3] washmount: 250ms sleep then mid-flight high-water read weakens the semaphore test
- `reconnect_test.go:54`: add a `started` counter; deadline-poll `started==6 && cur==2`; move
  the `max > 2` assert to after `close(release); wg.Wait()`.
- Verify: `go test -race -count=10 -run TestRunBoundsConcurrency ./internal/washmount`.

---

## Phase C — e2e specs (branch `fix-test-flakes-c`)

### C1 [P0] Timeout-archaeology sweep — stale overrides now SHRINK deliberately generous budgets
Every override below predates the config bump to 25s test / 15s expect and the fixture's own
12s control-socket lesson (`router.ts:408-412`). One commit, pure numeric/deletion changes:
- DELETE `test.setTimeout(20_000)` at `fm.spec.ts:33` and `fm-shortcuts-clipboard.spec.ts:26`;
  DELETE `test.setTimeout(15_000)` at `settings.spec.ts:46` and `settings.spec.ts:147`.
- DELETE `{ timeout: 3_000 }` / `{ timeout: 5_000 }` expect-overrides at: `fm-watch.spec.ts:118,126,133,170,191`;
  `fm-dnd.spec.ts:42,191,198,214`; `fm-replace.spec.ts:41,100,126`; `fm-upload.spec.ts:73,132,148,163`;
  `fm-dnd-errors.spec.ts:104`; `fm-scroll.spec.ts:42`; `open-routing.spec.ts:71`; `bulkops.spec.ts:226`;
  `fm-shortcuts-clipboard.spec.ts:228,247,324,330,354`; `sidebar.spec.ts:109,111`;
  `notify.spec.ts:44,45,48` (inherit the 15s default).
- `sidebar.spec.ts:74-77`: the About-widget `toPass` waits on a ticker whose FIRST push is
  by design 5s after subscribe (`apps/session/be/hoststats.go:40-43`) plus gateway hops —
  change `{ timeout: 10_000 }` to `{ timeout: 15_000, intervals: [500, 500, 1000, 1000] }`.
- RAISE `waitForLog(<bulk-ops …>, 5_000)` to `12_000` at: `fm-upload.spec.ts:66,84,98,115,135,151,179,190`;
  `fm-shortcuts-clipboard.spec.ts:113,134,208,231,250,296,333,357`; `fm-dnd.spec.ts:128`;
  `bulkops.spec.ts:78,155,173` (post-M7 these cross a cold background-service spawn).
- `services.spec.ts:196`: `5_000` → `10_000` (same priv-PTY chain packages.spec already bumped
  with a comment at packages.spec.ts:219-222).
- `settings.spec.ts`: every `{ timeout: 5_000 }` poll on the write→inotify→debounce→session
  chain (lines 131-134, 171, 191, 196, 197, 201, 202) → `{ timeout: 10_000 }`.
- `music-resume.spec.ts:57`: `waitForLog(/com\.wash\.music save_state persisted/)` →
  add explicit `15_000`.
- Verify: `make e2e-test` ×3 back-to-back; the audit's hypothesis is this sweep alone removes
  most of the "pass alone / fail in suite" surface (sidebar + fm trio + music/settings).

### C2 [P0] fm family: Node-side `existsSync` asserts race the FE→router→BE round-trip
Replace bare snapshots with polls (readFileSync/existsSync inside `expect.poll`):
- `fm-dnd.spec.ts:102`: `await expect.poll(() => existsSync(join(router.fmRoot, 'inner.txt'))).toBe(true);`
- `fm-dnd.spec.ts:186-187`: poll dest-appears (`docs/hello.txt` exists) before the negative
  src-gone check (rename is atomic).
- `fm-replace.spec.ts:104-105`: poll `readFileSync(target/hello.txt) === 'hello world\n'`
  first, keep the exists-false check after.
- `fm-replace.spec.ts:130,137`: DELETE `waitForTimeout(200)`; `await expect.poll(() => existsSync(linkPath)).toBe(true);`
- `fm-mutations.spec.ts:176-178`: insert `await expect(page.locator('[data-testid="fm-entry-inner.txt"]')).toBeVisible();`
  before the fs asserts (the file's own pattern at :110).
- `fm-be.spec.ts:193-196`: the `msg.ok` ack proves delivery, not execution —
  `await expect.poll(() => existsSync(target)).toBe(true);`
- Verify: `pnpm exec playwright test tests/fm-dnd.spec.ts tests/fm-replace.spec.ts tests/fm-mutations.spec.ts tests/fm-be.spec.ts --repeat-each=10`.

### C3 [P1] persist-before-reload barriers (uses A10's `logMark`/`waitForLogSince`/`logCount`)
- `music.spec.ts:100-101`: before the reload insert
  `await router.waitForLog(/com\.wash\.music save_state persisted/, 10_000);` (no earlier
  persist in this test; music-resume.spec.ts:54-57 documents this exact race as its historical
  full-suite flake).
- `radio.spec.ts:99,123-124`: earlier `persist()` calls exist, so COUNT:
  `const n = router.logCount(/com\.wash\.radio save_state persisted/g);` before the last
  state-changing action; after its DOM assert
  `await expect.poll(() => router.logCount(/com\.wash\.radio save_state persisted/g), { timeout: 10_000 }).toBeGreaterThan(n);`
  then reload.
- `app-state.spec.ts:57`: the existing barrier matches a STALE first occurrence (fm persists on
  navigation) — replace with the counting form: expect `>= n0 + 2` (show-hidden + info-pane
  saves) before the reload.
- Verify: `pnpm exec playwright test tests/music.spec.ts tests/radio.spec.ts tests/app-state.spec.ts --repeat-each=10`.

### C4 [P1] terminal family: echo/select races and vacuous gates
- `term-fonts.spec.ts:113-121`: after typing the marker, insert
  `await expect.poll(() => bufferText(page)).toContain('wash-copy-marker');` BEFORE
  `selectAll()`; replace the one-shot clipboard read with
  `await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toContain('wash-copy-marker');`
  (`systemCopyText` is fire-and-forget — `web/lib/src/clipboard.ts:24`). The cure is documented
  in `term-select-copy.spec.ts:68-73`.
- `term-menubar.spec.ts:51`: same pre-select echo poll (`menubar-copy-marker`), plus optionally
  the `hasSelection()` poll from term-select-copy.spec.ts:75.
- `term-wedge-recovery.spec.ts:66`: the typed command's own echo contains `${tag}-END`, so the
  30s drain gate passes instantly and the 10s follow-up absorbs the whole 20k-line burst —
  split the marker in the TYPED string only: `…done; echo ${tag}-EN''D` (assertion unchanged;
  mirror of term-live-reconnect.spec.ts:52).
- `term-modes-resize.spec.ts:80-86`: replace `waitForTimeout(300)` with: capture
  `colsAtOpen` before maximize; `await expect.poll(readCols, { timeout: 5_000 }).not.toBe(colsAtOpen);`
  read `colsBefore` AFTER the poll, before typing `tput cols`.
- `term-modes-resize.spec.ts:49`: keep a 700ms debounce wait, then add the same-socket barrier
  before reload: type `echo flush-ok` + Enter + `toContainText('flush-ok')` (proves every
  earlier FE→router frame, incl. the persist, was received).
- `term-reconcile.spec.ts:39-44`: the prompt wait matches HIDDEN tab-1's textContent — replace
  with the visible-host buffer poll (copy `activeBufferText` from term.spec.ts:24-40); after
  the Enter and before `goto('about:blank')` add
  `await expect.poll(() => activeBufferText(page)).toContain('exec sleep 1');` (echo proves the
  input reached the shell before navigation kills the socket).
- Extract the ~7 copies of `bufferText`/`termBuffer`/`activeBufferText` into
  `e2e/tests/helpers/term.ts` and import everywhere (one commit).
- Verify: `pnpm exec playwright test tests/term-fonts.spec.ts tests/term-menubar.spec.ts tests/term-wedge-recovery.spec.ts tests/term-modes-resize.spec.ts tests/term-reconcile.spec.ts --repeat-each=10`.

### C5 [P1] display family (today's only red + 2 of the 5 CI flakes live here)
- `display.spec.ts:80-81`: replace waitForSelector + one-shot `count()` with
  `await expect(page.locator('wash-app-display')).toHaveCount(2, { timeout: 10_000 });`
- `display-term-xclock.spec.ts:38`, `display-guest.spec.ts:54-59`,
  `display-input-smoke.spec.ts:31-33`, `display-qt-popover.spec.ts:70-72`: all type into the
  terminal after waiting only for `.xterm-rows` (xterm mounted ≠ shell ready) — insert
  `await expect(page.locator('wash-app-term')).toContainText(/\$|#|>/, { timeout: 10_000 });`
  after the selector wait, before clicking/typing. (These four specs skip when wash-display is
  absent — the fix still applies when present; `display-guest.spec.ts:43` was today's local
  failure.)
- `display-qt-popover.spec.ts:89`: the one-shot `dispWins()` count is sampled between the
  guest's t=3s menu and t=6s dialog — delete it; after the existing `toBe(2)` poll at :96 add
  the timing-independent post-hoc check: count `window\.create instance=<inst2>` lines in
  `router.log()` — 2 expected (main + dialog); 3 = a menu wrongly minted a window.
- Verify: `pnpm exec playwright test tests/display.spec.ts tests/display-guest.spec.ts tests/display-term-xclock.spec.ts tests/display-input-smoke.spec.ts tests/display-qt-popover.spec.ts --repeat-each=10` (needs out/wash-display; else they must SKIP, which A7 makes reliable).

### C6 [P1] shell chrome + login picker
- `chrome-windows.spec.ts:186-191`: DELETE `waitForTimeout(400)`; poll the computed transform
  matrix to `-innerW` (the viewport.spec.ts:56-62 pattern, 4s cap).
- `chrome-windows.spec.ts:274-292`: DELETE both sleeps; poll `boundingBox()?.x` to the settled
  value (≤1) before the one-shot box asserts, re-reading the box after the poll.
- `viewport.spec.ts:108-121`: the pager lives in the session FE one gateway hop behind the
  shell store — convert both one-shot evaluates to `expect.poll` (cell 2-1 true, then 0-0
  false).
- `login.spec.ts:141-146`: the picker page is server-rendered from a live /proc scan and the
  302 follows before the SIGTERM'd router exits — after the existing line-145 exit poll,
  insert `await page.reload();` before `toHaveCount(1)`.
- Verify: `pnpm exec playwright test tests/chrome-windows.spec.ts tests/viewport.spec.ts tests/login.spec.ts --repeat-each=10`.

### C7 [P2] fm interaction barriers
- `fm-dnd.spec.ts:162-183` (+ `fm-shortcuts-clipboard.spec.ts:180-185`):
  `window.wash.moveWindow/resizeWindow` are fire-and-forget — after the evaluate, poll applied
  geometry before any pointer sequence:
  `await expect.poll(async () => (await page.locator('wash-app-fm').nth(1).boundingBox())?.x ?? 0).toBeGreaterThan(500);`
- `fm-folder-grid.spec.ts:82-86`: insert `await expect(page.locator('[data-testid^="fm-tile-"]')).toHaveCount(3);`
  before the `evaluateAll` name snapshot (grid mounts before the listing arrives).
- `file-picker.spec.ts` (each test, after goto): `await expect(page.locator('wash-app-test')).toHaveCount(2);`
  before the first click (kiosk double-mount: the windowed instance mounts late under load and
  occludes the picker permanently).
- `fm-columns.spec.ts:50-61`: convert the `.all()`/getAttribute snapshots to a single
  `expect.poll(evaluateAll…)` comparison.
- Verify: `pnpm exec playwright test tests/fm-dnd.spec.ts tests/fm-folder-grid.spec.ts tests/file-picker.spec.ts tests/fm-columns.spec.ts --repeat-each=5`.

### C8 [P2] upload-cancel timing gates
- `fm-upload-bulk-cancel.spec.ts:64-68`: dispatch the first cancel as soon as the row exists;
  raise the payload toward the 50MB cap; only assert the partial-file count when the final
  status was `cancelled` (gate on the job's `status=done` log absence); add
  `test.setTimeout(40_000)` (8s row-wait + 20s poll + teardown doesn't fit 25s).
- `fm-upload-cancel.spec.ts:46`: if `data-status` is already `done` before the cancel click,
  `test.skip(true, 'transfer completed before cancel window — WS throttle ineffective')` —
  keeps the regression teeth without betting on Chromium's WS-throttle behavior.
- Verify: `pnpm exec playwright test tests/fm-upload-bulk-cancel.spec.ts tests/fm-upload-cancel.spec.ts --repeat-each=10`.

### C9 [P2] negative-assertion strengthening (false-pass hygiene; none of these flake-fail)
- `fm-dnd-errors.spec.ts:124-132` (+ :212,223; `fm-shortcuts-clipboard.spec.ts:146-148`;
  `fm-selection-invariant.spec.ts:39,59`; `fm-dnd.spec.ts:63-65`; `fm-replace.spec.ts:64`):
  replace sleep+negative with: drive a second VALID operation, await ITS positive signal
  (status/log), then assert the original negative (pipeline-drained proof); or minimally
  `await expect.poll(() => router.log().match(/bulk-ops job=\S+ op=move/) === null, { timeout: 1_000 }).toBe(true)`
  and delete the `waitForTimeout`.
- `edit-watch.spec.ts:171-173`: replace the 800ms sleep with the file's own sentinel pattern
  (:183-185): write `watch-sentinel.txt`, await its row, then assert the reload dialog count 0.
- `crash.spec.ts:142`: before the negative log assert, poll /proc until the wash-test BE is
  gone (10s cap) so a would-be `crashed:` line has landed.
- `terminal-attach.spec.ts:50`: replace the 500ms sleep with
  `await router.waitForLog(/app com\.wash\.about up instance=/, 5_000);` then the still-alive
  check.
- `term-live-reconnect.spec.ts:71`: capture `resyncsBefore = logCount(/resync complete/g)`
  before `__washDropSocket()`; after reconnect, poll the count to increase; keep the
  exactly-once marker poll after.
- `priv.spec.ts:488-491` + `packages.spec.ts:258-265`: after the positive completion signal,
  retroactively assert exactly-one-enqueue via `logCount` (proves the cancel/dialog phases
  fired nothing regardless of the window).
- Verify: run each touched spec `--repeat-each=5`.

### C10 [P2] isolation / hygiene
- `imageview-interact.spec.ts:14` + `imageview-open.spec.ts:96`: fixed shared `/tmp/wash-iv-*`
  paths with exact-count captions — replace with `mkdtempSync(join(tmpdir(), 'wash-iv-…-'))`
  at module scope.
- `fm.spec.ts`: runs against the real `$HOME` (empty-home CI fails it; big-home slows it; the
  BE watches $HOME while 7 workers run) — switch the file to `fmRoot: true` with a seeded
  visible entry and navigate within `router.fmRoot`.
- Leaked module-scope `mkdtempSync` seeds: add `afterAll(() => rmSync(dir, { recursive: true, force: true }))`
  in `fm.spec.ts` (7 dirs), `fm-upload.spec.ts:106`, `fm-dnd-torture.spec.ts:82`,
  `clipboard.spec.ts:52`, packages' fakeTasksel, services' stubDir, music/washamp/app-state
  trees, and display-qt-popover's compiled-guest dir.
- `top.spec.ts:95-104`: `test.skip(process.getuid?.() === 0, 'EPERM classification needs a non-root runner')`
  (root CI containers make `kill(1)` succeed — and actually signal init).
- Verify: `pnpm exec playwright test tests/imageview-interact.spec.ts tests/imageview-open.spec.ts tests/fm.spec.ts tests/top.spec.ts`.

### C11 [P2] singles
- `priv.spec.ts:316`: replace the 200ms sleep with
  `await router.waitForLog(/wash-priv enqueue: req_id=r-bad-1/, 5000);` (exact mirror of the
  fix the same file documents at :166-169).
- `settings.spec.ts:285,359`: DELETE both `waitForTimeout`s; make the respawn poll error-proof:
  `return r.t === 'launched' ? (r.instance_id as string) : instA;` (an error reply must keep
  polling, not pass `undefined !== instA`).
- `auth-harden.spec.ts:120-122`: single kill sweep can miss a boot-raced second spawn — poll:
  kill-all-then-return-count until 0 (8s cap).
- `reconnect.spec.ts:39-46`: shrink the fixed-port exposure — banner assert at 5s, move the
  `data-state`/retry-button asserts to after `r2 = await startRouter(...)`.
- Verify: run each touched spec `--repeat-each=10` (reconnect ×20).

---

## Phase D — FE unit tests (branch `fix-test-flakes-d`)

### D1 [P1] ws.test.ts: two-hop timer chain has ~65ms of real-time slack (the one real FE flake)
- `web/shell/src/ws.test.ts:273`: `pongTimeoutMs: 15` fires → `forceRedial` → 250ms backoff,
  all against an absolute 330ms sleep — >65ms callback lateness (39 parallel node child
  processes!) puts the redial after the assert. Fix: add a dial hook to `hbConn`
  (ws.test.ts:222): after `socks.push(s)` run `dialWaiters.splice(0).forEach(f => f())`;
  return `nextDial: () => new Promise<void>(r => dialWaiters.push(r))`. Test: arm
  `const dial = nextDial()` before `socks[0].onopen!(...)`, `await dial`, then assert zombie
  event + `socks.length >= 2`, with `{ timeout: 5000 }` on the test as a hang guard.
- Same hook replaces the three single-hop 300ms sleeps at `ws.test.ts:162,337,359`
  (deterministic today but the pattern invites copying; also −0.9s wall).
- Verify: `taskset -c 0 bash -c 'for i in $(seq 40); do node --test --conditions=browser web/shell/src/ws.test.ts || exit 1; done'`.

### D2 [P3] paths.test.ts year-rollover window
- `web/fs-client/src/paths.ts:61-67`: `formatDate` calls `new Date()` internally; the test
  derives expectations from its own `new Date()` — a Dec-31→Jan-1 rollover between the two
  breaks both branches. Fix: add a defaulted param
  `export function formatDate(unix: number, now: Date = new Date()): string` and pass the
  test's captured `now` through (paths.test.ts:80-88).
- Verify: `node --test --conditions=browser web/fs-client/src/paths.test.ts`.

---

## Phase E — test event bus (branch `wash-test-events`) — design + first increment

Goal (user direction): UI/VM/network e2e should drive **state machines** — "do X, await state
Y" — instead of greping logs and padding timeouts. This is a *test-only observation surface*:
best-effort, no arbitration, no leases — deliberately NOT the CONTROL_BUS.md M2 action-DB
settle (13wk); it can later feed M2.

### E1 Router: `wait_event` on the control socket
- Add a small in-router event ring (cap ~1024, monotonically numbered) with typed records:
  `{seq, ts, kind, app_id?, instance_id?, window_id?, detail?}`. Tap the existing seams (each
  already logs today): app up/down (`router.go:768` area and the crash/exit path
  `router.go:1035-1056`), window create/delete (wmstate upsert/delete), `app_state.set`
  persisted, bulk-ops job status transitions, fswatch watch-armed/watch-fired (fswatch
  service), `resync complete` (`router.go:1542`), control `launch` completed.
- Control-socket ops (JSON, `internal/router/control.go` dispatch):
  `{t:"events_cursor"}` → `{seq}` (current tail), and
  `{t:"wait_event", since:<seq>, match:{kind, app_id?, instance_id?, detail_re?}, timeout_ms}`
  → first matching event after `since`, or `{t:"timeout"}`. Long-poll server-side (condition
  variable over the ring), so the fixture isn't spinning.
- Gate behind `--test-events` (the e2e fixture always passes it; production default off).

### E2 Fixture API (replaces the A10 log helpers over time)
- `router.events.cursor()` → seq; `router.events.wait(match, {since, timeout})` → event.
- Convention for specs: `const c = await router.events.cursor();` → act →
  `await router.events.wait({kind:'app_state_persisted', app_id:'com.wash.music'}, {since:c})`.
  First adopters: the C3 persist barriers, C2's fs round-trips (kind:'bulk_job',
  detail:'done'), fm-watch's chain (kind:'watch_fired'), app-restart/crash respawn waits.

### E3 FE settle hook (second increment)
- Extend the existing diag surface (`web/shell/src/diag.ts`, `__washDiag`) with a test-gated
  `__washTestBus`: mirrors bus events (window mount/unmount, list_ok applied, persist acked)
  plus a monotonically increasing `settleSeq` bumped on each applied server patch. Playwright:
  `page.waitForFunction(([s]) => window.__washTestBus?.settleSeq > s, [seq])` after an action,
  instead of DOM-side-effect polling where a semantic event exists. Gated by the same
  `--test-events` flag (the router injects a flag into the shell bootstrap config).

### E4 VM/net guests (third increment)
- Standardize a guest console line protocol `WASH-EVENT <json>` emitted by the in-guest agent
  at state transitions (iface-up with addr, dnsmasq ready, fw4 loaded). The vm fixture tails
  the console and exposes `vm.events.wait(...)` with the same API as E2. multivm's B30 polls
  then collapse into event waits.
- Acceptance for the phase: C3's persist barriers and fm-watch's expect-polls rewritten onto
  `events.wait`, suite green ×3, and the 12s control-socket default demonstrably reducible.

---

## Appendix

**Environment notes (2026-07-03):** two orphan wash-routers from a packaging boot-smoke were
found running for 9h (ports 11081/11082, `/tmp/eaf*` extract) — the packaging smoke should get
the same group-kill treatment (A4/A11 pattern) in a follow-up; inotify instances were 50/128
with them alive. `e2e/test-results/` was empty (no local failure archaeology available).

**Known-issue closure mapping:**
- TODO.md "8-worker suite timing-race flakes (fm-be / net-vm / music / settings)" → C1 + C2
  (fm-be), C3/C1 (music), C1/C11 (settings), B30/B31 + A9 (net-vm), plus A6 removing the
  manufactured IO load.
- TODO.md "Sidebar e2e order: 3 fm specs pass alone but fail in full suite" → the ORIGINAL
  trio (clipboard/fm-tree/app-state) was root-caused and fixed 2026-06-01 (fm's `<For>`
  keyed rows by object reference; re-lists tore down every row's DOM — identity-stabilising
  memo fixed it; the TODO line is stale and can be dropped). The REMAINING full-suite-only
  exposure has two verified mechanisms: (1) sidebar's own 5s windows on 4-hop chains + the
  5s-first-tick hoststats ticker under a 10s toPass (C1); (2) mid-run inotify exhaustion from
  fixture leak-on-throw (A3) + every non-xdg router watching the real ~/.config/wash (A5),
  invisible to the start-only preflight (A7). Discriminating experiment if it recurs after A/C1: log
  `pgrep -cf wash-e2e-apps` + inotify instance count every 2s alongside `make e2e-test` and
  correlate with the failure timestamp; and `stress-ng --cpu $(nproc) --io 4` +
  `playwright test sidebar.spec.ts --repeat-each=10` reproduces the latency half without the
  suite.
- Memory "wash sidebar e2e order … not root-caused" → superseded by the above.

**Stress recipes:**
- Go load repro: `(go build -a ./internal/... >/dev/null 2>&1 &); go test -run '<Test>' -count=20 <pkg>`
- Go order probe: `go test -shuffle=on -count=1 -p 1 $(go list ./... | grep -v '/wash-vm/vm$')`
- FE pinned-core: `taskset -c 0 node --test --conditions=browser <files>`
- e2e single-spec soak: `cd e2e && WASH_E2E_MULTICALL=1 pnpm exec playwright test <spec> --repeat-each=10`
- Full-gate soak: `for i in 1 2 3; do make e2e-test || break; done`

**Explicitly NOT fixed here (tracked separately):**
- The UCI-applier reload-ordering product bug (B30 polls around it; TODO.md item stands).
- Lifting `-p 1` from the unit gate (re-evaluate after B1-B3).
- The 12s→5s control-socket restore (measure after A6).
- `runRawListener`-style connection-handler joins in `ListenControl` (`control.go:88`) — same
  hazard class as the fixed 42d6698, currently latent; one log line away from B1.
- wash-priv has no child-lifecycle test at all (the class that bit vscode) — new-test item,
  not a flake fix.
