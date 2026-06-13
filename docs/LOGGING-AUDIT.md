# Logging audit — 2026-06-12

Scope: all Go backends (`internal/`, `apps/*/be`, `cmd/`), wash-display C++.
Method: per-subsystem sweep for (1) silent failures, (2) missing context,
(3) noise, (4) inconsistent mechanisms, (5) unlogged lifecycle/state
transitions. ~230 log call sites exist across ~2900 Go files — the dominant
problem is *absence*, not noise.

## The five repo-wide patterns

1. **Error-to-FE-only.** Handlers return errors to the frontend (toast,
   dismissed) with zero server-side trace. Once the toast is gone the
   failure is unrecoverable. Worst in fm, disks, netd.
2. **`_ =` in goroutines.** `WriteEvt`/`WriteCtrl`/`SendAppMsg` results
   discarded inside spawned goroutines — the requester hangs or misses an
   event and nothing anywhere says why. ~10 sites in router, ~5 in sdk.
3. **Decision points unlogged.** Backend autodetection (washnet), reload
   ordering, skip/fallback choices execute without recording what was
   decided or why. The known UCI reload-ordering bug currently leaves no
   trail.
4. **exec results discarded.** `networkctl reconfigure`, smartctl stderr,
   wifi-scan stderr — command output thrown away exactly where a failed
   command is the thing you'd need to see.
5. **No shared convention.** Router has an injected `Logger` with a good
   idiom (`"app %s crashed: code=%d signal=%q uptime=%s instance=%s"`);
   everything else is ad-hoc `log.Printf` / `fmt.Fprintf(os.Stderr)` /
   nothing. App stdout/stderr *is* captured into the router log, so plain
   `log.Printf` in apps does reach a debugger — apps just don't call it.

Convention to standardize on (matches the router's best lines):
`<component>: <verb-phrase> key=value key=value: %v` — entity ids first
(app, instance=, win=, path=, dev=, iface=), then the event, error last.

---

## P0 — destructive/privileged ops with no audit trail

| Where | Problem |
|---|---|
| `apps/fm/be/app.go:241-253` (+write 234, chmod 269, chown 394, symlink 427) | delete/rename/write/chmod/chown/symlink: result never logged server-side. Log op, path(s), replace flag, and outcome — success and failure. |
| `apps/services/be/app.go:292-306` `dispatchAction` | systemctl start/stop/enable/disable via priv with no services-side record. Log `services: %s %s exit=%d stderr=%q`. |
| `apps/disks/be/app.go:201-211` + `smart.go:76-97` | LVM/ZFS/btrfs provider failures unlogged; smartctl stderr discarded on error. A failed mkfs/mdadm with no captured stderr is undebuggable. |
| `cmd/washvm-rootexec/main.go:20-27` | setuid(0)+exec with no pre-exec trace. One stderr/syslog line with argv before `Exec`. |
| `apps/priv/be/queue.go:1099` | only failed sudo runs logged; clean ones invisible → no positive audit trail. Also log CliOrigin per request (`queue.go:818`). |
| `internal/login/server.go:708-717` | cookie missing/expired/tampered → silent redirect to /login. Log reason + remote IP (not the token). |
| `internal/login/auth_unix.go:128-141` + `spawn.go:206` | auth rejection reason and router-spawn failure lack uid/gid/remote context. |

## P1 — networking applier forensics (washnet/netd)

The applier can take a box offline; it currently records almost nothing.

- `cmd/washnet-apply/main.go:61` (also washnet-read:28, washnet-edit:36):
  `name, _ = backendsel.Autodetect()` — log chosen backend **and the
  discarded reason**, plus source (flag / env / autodetect).
- `apps/netd/be/networkd/applier.go:82-92`: log unit files written
  (names + dir), `networkctl reload` output, and per-link reconfigure
  results — best-effort is fine, *silent* best-effort is not
  (same in Rollback, line ~156).
- `cmd/washnet-apply/main.go:84`: `_ = a.Rollback(token)` — if rollback
  itself fails after a failed verify, that's the worst state the box can
  be in and nothing says so.
- `internal/washnet/txn/txn.go:69-90`: emit the actual diff summary
  (interfaces/zones added/removed/updated), not just a count; log verify
  window/decision ("had default route before; sampling 14s").
- Reload ordering: log the planned reload sequence before executing —
  direct trail for the known ordering bug.
- Parse-layer silent defaults: `nmprofile/parse.go:141,160,213`
  (`strconv.Atoi` with `_`), `netplanprofile/parse.go:122-127`
  (invalid CIDR silently dropped). Warn, don't omit.

## P2 — infrastructure (sdk / fswatch / pty / router)

- `internal/fswatch/fswatch.go:259`: watch-creation failure is returned
  but never logged at creation site; add a `log.Printf` naming the path
  and hinting at the inotify instance limit (this exact silence has cost
  hours before).
- `internal/pty/pty.go:65-128`: three goroutines (pty→ch, ch→pty, reaper)
  drop copy errors and exit status (`:102,113,124`). Log non-EIO copy
  errors and shell exit status.
- `internal/sdk/heartbeat.go:55-64`: heartbeat write failure exits the
  loop silently — About-panel stats just stop. Log once on stop.
- `internal/sdk/bus.go:562-576`: `_ = c.SendAppMsg…` reply errors ×3 —
  requester hangs with no trace. `bus.go:246,528`: decode/encode error
  logs lack app/window/request id.
- `internal/sdk/dispatch.go:34`: bytes on unknown channel silently
  dropped — log channel id (should-be-unreachable paths are exactly
  where a log earns its keep).
- Router `_ = WriteEvt/WriteCtrl` in goroutines: `router.go:575,626`,
  `app_session.go:767-770,816`, `control.go:440-462` (priv forwarding).
  Log on failure with instance id.
- Router missing context: `attach.go:304` loop error lacks instance id;
  `http.go:140` shell-session error lacks remote addr; no log at all on
  successful app attach (can't see which instances are registered).
- Router noise: `shell_session.go:217` logs every shell→BE app_msg on the
  hot path — make it failure-only or drop it.
- `internal/medialib/medialib.go:82,145`: WalkDir errors swallowed; HTTP
  server `Serve` error discarded in goroutine.
- `internal/bulkops/bulkops.go:633`: merge leaves source dir non-empty,
  error intentionally dropped with only a comment — log it.

## P3 — startup identity & misc

- `wash-display/src/main.cpp:150-169`: no version/identity line at
  startup (stale-binary debugging is a recurring pain — memory says so);
  dial failure lacks errno; handshake failure says only "failed" with no
  cause (token? proto version?).
- `cmd/washvm-agent/main.go:58-70`: hello-write and frame errors exit/
  return with no stderr line, and the binary has no log prefix at all.
- `wash-vm/vm/vm.go:173`: boot timeout doesn't include console tail
  (agent-readiness failure does — make timeout match).
- `cmd/wash-sudo/main.go:156`: reqID generated *after* dial, so dial
  errors can't be correlated; generate it first.
- `apps/journal/be/journal.go:289-291`: unparseable journalctl JSON lines
  dropped silently — count and log once per stream.
- `apps/disks/be/collect.go:40,152,183,213`: /sys, diskstats, mounts,
  by-uuid read failures return empty silently — a disks app showing zero
  disks should say why in the log.

## Non-goals / leave alone

- `internal/wire` (pure codec), `internal/proc` (pure parsing),
  `internal/fs` (errors propagate to callers that respond) — fine as-is.
- Don't add per-event/per-frame logging anywhere (fswatch fanout, pty
  bytes, audio chunks, shell relay) — failure-only at those layers.
- Tokens, passwords, cookie values never appear in logs; log presence/
  validation-failure-reason only.

## Suggested execution order

1. P0 destructive-op audit lines (fm, services, disks, priv, rootexec,
   login) — small diffs, immediate debug value.
2. P1 washnet applier forensics — riskiest subsystem, and directly aids
   the open reload-ordering bug.
3. P2 sdk/fswatch/pty/router — fixes inherit into every app.
4. P3 startup identity lines.

e2e note: e2e asserts on router-log output — adding lines is safe,
changing/removing existing lines (e.g. the `shell_session.go:217` noise
line) needs a grep through `e2e/` first.
