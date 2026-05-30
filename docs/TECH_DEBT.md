# Tech debt & cleanup plan

Findings from the 2026-05-28 codebase-wide comment audit + code-smell
review. The comment cleanup and the trivial fixes (json-tag typo, wrong
`--listen` help default, unprobed-`opkg` doc, dead `toInt`/`decodeBase64`)
are already done. This file tracks what's left, by priority.

Each item: `file:line` — problem — suggested fix. Severity is about risk of
a real bug / maintenance cost, not effort.

---

## P1 — correctness risk, do soon

> **DONE** (commits `41c1506`, `6f9d2a1`): all three items fixed —
> `Job.cancel` is now `*atomic.Bool` (vet clean), `ringBuf.Write`
> reports the real consumed count (with a regression test), and error
> classification uses `errors.Is(syscall.EXDEV/ENOTEMPTY)` throughout
> (hand-rolled `contains` and the string fallbacks removed).

- **`internal/bulkops/bulkops.go:93` — `Job` embeds `atomic.Bool` but is
  copied by value everywhere** (snapshot, emit, onUpdate, ConflictInfo).
  `go vet` flags ~20 "copies lock value" sites. Only saved today because
  `snapshot()` happens to omit the `cancel` field, so copies silently carry
  a zeroed atomic — any code that reads `cancel` off a snapshot is wrong.
  *Fix:* pull the live-mutable cancel flag out of the value-copied struct
  (e.g. `cancel *atomic.Bool` or a separate control handle), so `Job` is a
  safe value type. Then `go vet ./...` should be clean here.

- **`internal/router/spawn.go:146` — `ringBuf.Write` return value is
  convoluted and likely violates the `io.Writer` contract** after the
  full-buffer slice path (`return len(p) + (len(r.buf) - len(p)), nil`). The
  trailing `// n` comment suggests the author was unsure too.
  *Fix:* return the count actually consumed from the original input; add a
  table test for the wrap/overflow case. Also note two ring-buffer impls
  coexist (`ringBuf` here vs `ringBuffer` in `internal/router/ringbuf.go`) —
  collapse to one.

- **Error classification by `err.Error()` substring instead of `errors.Is`.**
  `internal/bulkops/bulkops.go:903 isCrossDevice` string-matches "cross-device"
  while the sibling `internal/fs/mutate.go:classifyMutateErr` correctly uses
  `errors.Is(err, syscall.EXDEV)`. `fs/mutate.go:136` "directory not empty"
  also relies purely on the message string (locale/impl-fragile, no sentinel).
  *Fix:* use `errors.Is` with `syscall.EXDEV` / `syscall.ENOTEMPTY`
  everywhere; delete the string scans (and the hand-rolled `contains()` at
  `bulkops.go:910` that reimplements `strings.Contains`).

---

## P2 — structural / maintainability

- **FE monolith closures.** Single giant `App()` closures holding all signals
  + dozens of nested handlers, untestable in isolation:
  - `apps/fm/fe/src/main.tsx:83` — ~1718-line closure (2661-line file, largest
    in repo)
  - `apps/edit/fe/src/main.tsx:221` — ~2325-line closure
  - `apps/session/fe/src/main.tsx:170` — ~750-line closure (taskbar, palette,
    pager, banner, widgets all in one)
  - `apps/top/fe/src/main.tsx:175` — 1461-line file mixing App logic + ~10
    chart components
  *Fix:* extract logical sub-modules (handlers, sub-components) and lift shared
  state into stores/context so pieces are independently testable.

- **`edit` and `fm` FE copy-paste an entire file-tree subsystem.** Near-identical
  `sendWithReply`/`pendingReplies`, `readDragPaths`, `onRow/ListDragOver/Drop`,
  `startRename`/`cancelRename`, `toggleExpand`, `walk`, `watching`,
  `scheduleRefresh`, `commitMove`, `followSymlink`, `dirOfSelection`.
  *Fix:* extract a shared tree/fs-watch module (candidate first tenant for the
  deferred `web/lib` package — see MEMORY `wash_ui_lib_deferred`).

- **SDK correlation/pending-call duplication.** Four hand-rolled
  "mint id → register pending map under mutex → send → select on chan/ctx →
  defer-delete" patterns: `internal/sdk/bus.go:649` (requestIDs),
  `outbound.go` priv replies, `channel.go` pendingOpens, `sdk.go`
  pendingClipboardGet. `Conn` carries five separate mutexes guarding five
  pending registries.
  *Fix:* one generic `pendingCall[T]` helper (map + mutex + timeout + ctx);
  collapse the five registries.

- **`internal/sdk/bus.go:586` — struct→`map[string]any`→struct JSON round-trip
  on every Emit/Call/reply** (double-encode on the hot path; `decodeInto` does
  the reverse inbound). Acknowledged in the doc comment but still real cost +
  a leaky abstraction (typed structs flattened to maps).
  *Fix:* if perf matters, marshal typed envelopes directly; otherwise leave but
  stop pretending it's typed.

- **`priv/be` sudo arg construction copy-pasted 3×** across `runSudo`
  (`queue.go:1066`), `startInlineRun` (`inline.go:55`), `startInlinePTY`
  (`inline_pty.go:27`) with **subtly different `--preserve-env` lists**
  (runSudo: WASH_DISPLAY/PROTO/APP_ID/INSTANCE_ID/ATTACH_TOKEN; inline:
  WASH_DISPLAY/PROTO/CONTROL_SOCKET/BIN_DIR). Easy to update one and forget the
  others. Plus three near-identical env helpers (`filterEnv`/`mergeEnv`/
  `overrideEnv`) across two files.
  *Fix:* single `sudoArgv(pw, preserveEnv, argv)` + a shared env-utils file.

- **`internal/router/qos.go:140 Scheduler.Next` lists the QoS class set 3×**
  (fast-path drainOrder, the explicit slow-path cases, the drainOrder var).
  Adding a 5th class silently mis-prioritises if you miss one.
  *Fix:* drive all three from one ordered class list.

- **Stringly-typed `map[string]any` message envelopes** with unchecked casts:
  `cmd/wash-sudo/main.go` (unchecked `req["req_id"].(string)`, `"error"`
  handler duplicated 3×, accepts both `result`/`priv.result`);
  `web/shell/src/main.tsx:228` WS dispatch is a `switch` of `msg as ShellX`
  casts because `Conn.CtrlHandler` is typed `any`; `apps/top/fe/src/main.tsx`
  `handleBE(m: any)`.
  *Fix:* typed request structs (Go) and a discriminated union on `t` (TS) so a
  field typo is a compile error.

- **`internal/sdk/bus.go:447 classifyKind`** upgrades certain kinds to Bulk QoS
  by hardcoded string-suffix matching — renaming a kind silently changes its
  class with no compile-time signal.
  *Fix:* make QoS class an explicit property of the kind registration.

---

## P3 — low: tests, naming, cosmetics

- **e2e fixed-sleep flakiness.** `e2e/tests/viewport.spec.ts:68/73/86/97`
  `waitForTimeout(280)` tied to a 220ms CSS transition + asserts hardcoded
  `rgb(42,42,74)`; `packages.spec.ts` / `fm-shortcuts-clipboard.spec.ts` /
  `fm-watch.spec.ts:181` sleep before *negative* log assertions (a late event
  passes the test falsely); `priv.spec.ts` (523 lines) has ad-hoc
  `setTimeout` sleeps.
  *Fix:* wait on a testid/attribute end-state or poll a log assertion; replace
  magic colors with semantic attributes; consider splitting `priv.spec.ts`.
- **`e2e/tests/fm-replace.spec.ts:137`** — test "…is now a symlink" only
  asserts `existsSync`, skips `readlinkSync`; never verifies it's a symlink.
- **Reinvented stdlib** (replace with stdlib): `cmd/wash/main.go:152` &
  `internal/router/runtime_stats.go:331` hand-rolled insertion sorts to dodge
  importing `sort`; `apps/syslogs/be/syslog.go:485 indexByte` vs
  `bytes.IndexByte`; `apps/priv/be/inline.go wipeBytes`; `bulkops_test.go:575
  itoa` + unused-`sort`-import scaffolding.
- **Dead test scaffolding.** `internal/login/picker_test.go:18 fakeSpawner`
  (never instantiated); `fakeSpawnTrap` records nothing asserted on.
- **`internal/login/sessions.go:255 sessIDFromSock`** takes an unused `uid`
  param.
- **`internal/sdk/cbor.go`** — filename says CBOR but contains only JSON
  helpers (stale name from a former encoding); rename.
- **`internal/router/control.go:107 controlReq`** is a catch-all union of
  mutually-exclusive optional fields for two protocols (launch/msg vs
  priv.run) — easy to read a field off the wrong message type.
- **Pre-existing `gofmt` drift** (struct-field/const alignment, not introduced
  by the audit): `internal/sdk/{bus,sdk,frame,manifest}.go`,
  `internal/wire/{msgs_ctrl,msgs_shell}.go`, `internal/runner/login/runner.go`
  const block, `apps/top/be/app.go`, `apps/priv/be/queue.go`,
  `apps/journal/be/journal.go`, several `internal/router/*.go`, `internal/fs/*.go`.
  *Fix:* a single `gofmt -w ./...` sweep in its own commit (so it doesn't bury
  real changes).
