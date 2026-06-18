# Resume prompt — wash remote apps (R2)

Paste the block below into a fresh session (run `make` from the worktree root
`/home/mick/wash/branches/wash-remote`). Full context: memory
`wash_remote_plan.md` + `wash_remote_vm_topology.md`, the design doc
`docs/REMOTE.md`, and `git log`.

---

You're continuing **remote-apps (R2)** on worktree `branches/wash-remote`
(off `main`). Read memory `wash_remote_plan.md` + `docs/REMOTE.md §2/§4/§7`
first. **30 commits ahead of main, 35 behind; working tree clean.** HEAD =
`3da93a9`.

## State — the one-port relay is DONE and green

Everything below is committed and verified (go test wire+router, fe-unit 272,
host-process relay e2e 3/3 `connect-launch`+`remote-apps`, **2-VM real-ssh relay
2/2** `remote-vm.spec.ts` — composite + pty round-trip):

- **One-port relay.** Browser keeps ONE connection to A; B's wire is muxed over
  a `kind:"peer"` channel and A splices it **verbatim** to an `ssh -L`'d unix
  socket. `internal/router/peer.go`, `runner/router --listen-raw`,
  `apps/remote/be/supervisor.go`. Not federation (§13).
- **Origin-aware FE raw channels (`1cc7dc2`).** Remote **term/edit/fm** work:
  `deliverRaw/subscribeRaw` key on `compoundChannelId(origin, ch)`;
  `data-wash-origin` stamped on every app host element; `origin` threaded to
  `openRawChannelFor/writeRawFor/rawBufferedAmountFor` + the @wash/ui Terminal.
- **Creditless class-preserving relay (`3da93a9`, the connection-path audit).**
  The relay is a pure byte conduit: per-frame CLASS preserved (interactive jumps
  bulk in A's scheduler), but **no A-side credit window** — flow control is B's
  per-channel credit, end to end. Killed a head-of-line block (the old
  per-frame credit `Reserve` ran in the single pump goroutine) and the per-frame
  decode/re-encode/copy (new `wire.DecodeFrameRaw`/`ReadFrameRaw`). Regression:
  `TestPeerRelayNoHeadOfLineBlocking`.

## NEXT — recommended order

1. **MERGE `wash-remote` → `main`.** The relay work is at a clean green stop.
   Ask local-up-a-level vs remote; then clean up the worktree. Do this FIRST —
   it's the prerequisite for (2)/(3), because the **download feature lives on
   main** (commit `c6cb47f`, NOT on this branch), and the next items are
   downloads × relay.

2. **R3 (on main, after merge) — stream downloads to disk, not to a Blob.**
   This *completes the backpressure chain the creditless relay opened.* Today
   `apps/fm/fe` concatenates every download chunk into an in-memory array → one
   `Blob`. With the relay now creditless, the FE's writable sink is the LAST
   place backpressure can live — and an infinite RAM sink credits B at full
   speed, pulling whole files into the browser. Switch to the File System Access
   API (`showSaveFilePicker` → `createWritable` → stream chunks; Blob fallback):
   a slow disk → writable backpressure → slower absorb → slower B credit → B
   paces itself. Fixes both the memory ceiling under many/large concurrent
   downloads AND closes the flow-control loop end to end. Test it as a **remote**
   download over the relay (the thing the whole audit was about).

3. **R4 (after R3, only when measurable) — window/segment sizing.** A single
   remote download is RTT-bound to `DefaultChannelCredit` (64 KiB) per credit
   round-trip, and over the relay that RTT is the ssh hop. Larger window and/or
   a write chunk smaller than the window lets one download fill the pipe. It's a
   knob (`internal/router/credit.go` `DefaultChannelCredit` + the fm bufio size)
   — **measure first** on a real post-merge remote download; don't guess.

## Older R2 milestones still open (lower priority than 1–3)

- **M2e — persist B's router across an SSH drop.** Supervisor ties B's router to
  the `ssh` process (`apps/remote/be/supervisor.go`); a blip kills B's apps.
  Start B's router detached so a drop is a transport blip: re-dial with backoff,
  report `reconnecting`, freeze→thaw windows (docs/REMOTE.md §2/§9).
- **M4/M5** — multi-host notify/bulk/priv merge + priv host attribution;
  clipboard sync hub; cross-origin **z-band** (focused-host windows on top,
  below chrome z 9999/10000).
- **M6** — hardening (multi-tenancy, provenance/priv-phishing review,
  reconnect-audit alignment, B-router teardown/linger policy).

## OPEN THREAD — unfinished bug bash

User reported "something serious funky gone wrong with **rendering content**"
and took a screenshot "with the feature you just made" — but the image never
reached the session (not attached, none on disk). NOT diagnosed. On resume: ask
for the screenshot again (or a path), and which app + local-or-relay. A repro
harness (`?peer=` two local routers, launch remote term/fm, screenshot) was 90%
working — `e2e/tests/remote-apps.spec.ts` + the `startRouter` fixture is the
pattern.

## Discipline & commands

- Worktree workflow; commit on build+unit green; FULL e2e before any push.
- BE: `go test ./internal/...`. FE: `make web-shell fe-unit component`.
- Host-process relay e2e: `cd e2e && pnpm exec playwright test
  connect-launch.spec.ts remote-apps.spec.ts` (needs `out/` built —
  `make out/wash web-shell test-app`).
- **2-VM real-ssh e2e** (the capstone): `make vm-image vm-chrome
  out/washvm-remote-run` then `cd e2e && pnpm exec playwright test
  remote-vm.spec.ts`. BOTH `vm-image` AND `vm-chrome` — `vm-chrome` serves the
  `@wash/ui` vendor (`out/vm-chrome`), `vm-image` bakes `shell.js`; a stale
  `vm-chrome` silently uses an old `defineWashApp` (this bit us once).

## Manual two-VM test (no wash-connect)

```
# VM-B:  wash-router --listen-raw unix:/tmp/b.sock --no-session --no-auth --control-socket /tmp/b.sock.ctl
# A host: ssh -L /tmp/relay.sock:/tmp/b.sock -o AllowTcpForwarding=yes -o AllowStreamLocalForwarding=yes user@vmB
# (com.wash.remote does this for you; type the host into wash-connect → Connect → launch an app)
```
