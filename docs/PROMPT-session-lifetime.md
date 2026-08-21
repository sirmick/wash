# Session prompt — a wash session must outlive the browser that opened it

Goal, in one line: **closing the lid must not destroy the session.** A wash
session either keeps running while nobody is watching, or it suspends and
resumes with its work intact — it never silently deletes itself.

Paste this as the opening prompt for a dedicated session. It is self-contained.

---

## The evidence

`/run/wash/1000/router-s-8364ed00.log`, the last three lines of a session that
had been in active use all evening:

```
2026/08/19 23:09:44 wash-router shell: disconnect conn=103 dur=44.991s rx_frames=8 tx_frames=353 idle=28.123s
2026/08/19 23:39:48 wash-router idle for 30m0s — exiting
2026/08/19 23:39:48 wash-router shutdown complete
```

A laptop lid closed at 23:09:44. Thirty minutes later the router self-exited
and took the whole session with it — `wash-display`, `wash-netd`, `agentd`,
both live ACP agent sessions, every child process. On reopen at 06:28 the next
morning `wash-login` spawned a brand-new session, `s-394818dc`.

The agent did not fail to reconnect. There was nothing left to reconnect to.

Partial credit where due: agentd persists session IDs, so
`agentd: acp session resumed key=acp:1 session=e59971fa-…` brought the previous
night's conversation back under a new key. **History survived; the in-flight
turn did not.** A disconnect is currently survivable if the agent happened to be
idle when you walked away, and destructive if it was mid-task — which is
backwards, since mid-task is exactly when walking away is the point.

## The mechanism

| # | Fact | Where |
|---|---|---|
| 1 | `IdleTimeout` = "no-attached-shell period after which the router self-exits" | `internal/router/router.go:97-100` |
| 2 | Reaper polls `ShellCount() == 0` every 5s; on threshold returns nil → `listenCancel()` → full teardown | `internal/router/unix_listener.go:523-565` |
| 3 | 30m default applied **iff `--listen-unix` is set** | `internal/runner/router/router.go:271-273` |
| 4 | `ReapWhenIdle` is only called in the `cfg.ListenUnix != ""` switch branch — the `ws` and stream branches never reap | `internal/runner/router/router.go:617-624` |

Scope: this affects **only** routers spawned by `wash-login` (the only caller
that sets `--listen-unix`). A dev router started by hand never reaps — the one
on this box has been up since Aug 19 through the same overnight disconnect.
Do not "fix" this by disabling the reaper globally; see the next section.

## Why it is wrong

The reaper exists for a real reason, stated in its own doc comment:

> A router that never receives a handoff is considered idle from t=0 —
> wash-login spawning a router that no browser connects to must not leak
> indefinitely.

`wash-login` listens on `0.0.0.0:9000` and spawns a full session per hit. Every
port scan, every abandoned login, every closed tab would otherwise leak a
compositor and a dozen processes forever. Something must reap those.

**The design error is that one timer serves two opposite populations.** The
comment names the distinction and then throws it away:

- **Never-attached** — nobody ever connected. Wants an *aggressive* timeout;
  two minutes is arguably generous.
- **Established** — ran for hours, did real work, is momentarily unwatched.
  Wants a long timeout, or none.

With a single `IdleTimeout`, the leak case sets the policy and the real-work
case eats it.

The deeper mismatch: `ShellCount() == 0` means "no browser attached," which is a
sound proxy for "nothing of value is happening" **for a desktop** — a desktop
with nobody looking at it genuinely is idle. It is the exact inverse for an
agent, where nobody attached is precisely when the work matters most. The
`desktop-is-an-app` premise leaks here: the router is both the machine and the
display, so disconnecting the screen powers off the box.

Note also how out of proportion this is with the rest of the codebase.
`readIdleTimeout` is 90s with a long comment on why it is deliberately generous
(`internal/router/transport.go:25-47`); the FE heartbeat runs on a dedicated
Worker thread to avoid false reaps; `behindWatchdogLoop` retries resync every
3s; PTY_ROBUST.md Fixes A–D exist entirely to survive disconnects. All of that
buys seconds-to-minutes of resilience, and then an unguarded timer deletes the
session at thirty minutes. The resilience budget went to the disconnects that
barely matter.

## Secondary bug — `--idle-timeout=0` cannot cross the wash-login boundary

`wash-login`'s flag defaults to `0` (`internal/runner/login/runner.go:158`), and
the spawner only forwards it when non-zero:

```go
// internal/login/spawn.go:179-180
if s.IdleTimeout > 0 {
    args = append(args, "--idle-timeout", s.IdleTimeout.String())
}
```

So `wash-login --idle-timeout=0` forwards *nothing*, the router falls through to
its own `0`, sees `--listen-unix`, and applies 30m anyway. The router's
documented "zero disables idle reaping" escape hatch is unreachable through the
only process that spawns those routers — the one value meaning "never reap" is
the one value that cannot be expressed. Today the only workaround is forwarding
an absurd duration (`--idle-timeout=8760h`), which contradicts the flag's own
help text.

---

## The work order

### M0 — make the failure legible (do this first; it is how you verify the rest)

Today the router logs its own death into a file that dies with the session, and
the replacement session starts with no reference to what preceded it. That is
why this took an hour of log archaeology to find.

- `wash-login`: on spawn, log the **previous** session id and why it ended
  (`idle-exit` / `crash` / `first-boot`). `s-8364ed00 → s-394818dc` is currently
  only inferable by comparing file mtimes.
- Reaper: log a countdown at 50% and 80% of `IdleTimeout`, and include the
  last-disconnect timestamp and shell count in the exit line. `idle for 30m0s —
  exiting` does not say what it was waiting for.
- `agentd` startup: log which rows were resumed from disk vs started fresh, with
  the prior session id and transcript age — so "your agent came back" vs "your
  agent is new" is explicit rather than inferred.
- `agentd` resume: log per row whether an in-flight turn was found and whether it
  was recoverable or dropped.

### M1 — split the two idle populations

Give the reaper two thresholds: a short one for routers that have **never** had a
shell attached, and a long one (or none) once a session has been established.
`ReapWhenIdle` already distinguishes the t=0 case implicitly — `idleSince` is set
at entry when `ShellCount() == 0` — so this is mostly making that explicit and
threading a second duration through `Config`. This alone fixes the lunch-break
case and makes the leak case *tighter* than it is today.

### M2 — make `--idle-timeout=0` mean what it says

Forward the flag unconditionally from `spawn.go`, or use a `*time.Duration` /
sentinel so "unset" and "explicitly zero" are distinguishable. Small, same
afternoon as M1.

### M3 — agent-aware idleness

`agentd` already tracks per-row state (`running` / `needs-input` / `done`) and
already has a `detached` concept (`apps/agentd/be/acp.go:90-92`). Let it veto the
reap: a session with an actively-running agent does not reap, full stop.

Decide deliberately what `needs-input` means, because it cuts both ways. It is
the state you most want to survive a disconnect — the agent is blocked on a human
who is by definition absent — and also the state that could pin a session
forever. This is not hypothetical: on 2026-08-20 ten consecutive asks expired
unanswered (`askTTL = 30s`, `apps/agentd/be/ask.go:36`, a wall-clock
`time.AfterFunc` that keeps running while nobody can see the prompt). Recommend a
high-but-finite ceiling for `needs-input`.

The `askTTL` half of that is **not this doc's work**.
`docs/PROMPT-ask-timeout.md` owns `apps/agentd/be/ask.go` and covers why an
expired ask silently becomes a denial; pausing the TTL while no viewer is
attached belongs there. Coordinate before both branches edit that file.

### M4 — mid-turn checkpoint and resume (separate piece of work)

The real answer, and the one that makes a 30-minute timeout harmless. Session IDs
already persist and `resumeHosted` (`apps/agentd/be/adapters.go:343-367`) already
reopens them; what is missing is enough state to resume a turn that was executing
when the process died. Once idle-exit is a *suspend* rather than data loss, the
whole argument above stops mattering. Do not attempt this before M1–M3.

## Traps

- **Do not set `IdleTimeout = 0` globally.** That restores the leak the reaper
  was built to prevent. The fix is two policies, not zero policies.
- **Do not conflate this with the FE heartbeat bug** (below). They produce
  similar-looking reconnect noise and have nothing to do with each other.
- Another agent session may be live in this repo (`apps/ai/**` was under active
  edit on 2026-08-20). Check before touching shared files.

## Appendix — unrelated bug found in the same logs, worth its own branch

The FE heartbeat stops firing after arming the router's watchdog, so **every**
connection is reaped at ~90s and redials, indefinitely:

```
06:34:27 shell: connect conn=5
06:34:27 shell: heartbeat armed conn=5
06:36:02 shell: read-idle reap conn=5 idle=1m34.984s — closing zombie connection
06:36:02 shell: disconnect conn=5 dur=1m35.285s rx_frames=6 tx_frames=184
```

`heartbeat armed` proves at least one `TShellPing` arrived; `livenessTransport`
stamps `lastReadAtNanos` on *every* frame read
(`internal/router/shell_session.go:376`), so `idle` reaching 1m35 proves zero
inbound frames for the full window — the 15s heartbeat is not ticking. Rates:
6 reaps/9 connects, 51/64, 68/103 across three session logs.

Ruled out: the worker chunk ships correctly
(`internal/shellassets/assets/assets/heartbeat-worker-*.js`, hash matches the URL
in `shell.js`, `//go:embed all:assets` picks it up).

Two candidates remain in `web/shell/src/ws.ts`: the worker ticks but `sendPing`
bails at its `this.state !== 'open'` guard (line 506), or the worker died after
first tick — `ensureHbWorker` (line 492) sets `onmessage` but **no `onerror`**, so
a worker that throws post-load is silent and never falls back to `hbTimer`. Add
the `onerror` handler regardless; a silently dead heartbeat worker is a 90s
reconnect loop with no diagnostic.
