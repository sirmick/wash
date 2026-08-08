# ACP terminals: agentd, wash-term, and who owns the pty

Turning on ACP's `terminal` capability, so an agent's `npm test` becomes a
real wash terminal instead of captured text in a transcript.

Status: **M1–M3 shipped.** The capability is advertised, agentd owns the
ptys, and the transcript renders them live. M4 (adopting one as a tab in
wash-term or wash-edit) is not built; §6's decisions bite then, not before.

## 1. What the protocol asks for

Five methods (`internal/acp/client.go:44`), all client-side — the agent
asks, wash does:

```go
CreateTerminal(command, args, env, cwd, outputByteLimit) → terminalId
TerminalOutput(ref)  → {output, truncated, exitStatus?}
WaitForExit(ref)     → {exitCode?, signal?}
KillTerminal(ref)
ReleaseTerminal(ref)
```

Three properties fall out of that shape and drive everything below:

1. **`create` returns immediately.** The agent gets a handle, not a pipe,
   and then polls `output` or blocks on `wait_for_exit`. So the process is
   ours, running on the agent's behalf, referenced by id.
2. **A terminal outlives its process.** `output` still answers after exit —
   that is what `exitStatus` on the output response is for — and the thing
   is gone only when the agent calls `release`. Lifetime is owned by the
   agent, not by whoever is looking at it.
3. **Truncation is expected.** `outputByteLimit` on create, `truncated` on
   read. The agent knows it may be reading a window, not a river.

## 2. What already exists

More than it looks, and it is why this is a day rather than a rewrite.

| Piece | Where | Fit |
| --- | --- | --- |
| `pty.Open` as a **library** | `internal/pty` | Any BE calls it — wash-edit does for its pane, wash-connect for ssh. Owning a pty is not a wash-term privilege. |
| `exec_tab` | `apps/term/be/app.go:339` | agentd → wash-term, "open a tab running THIS argv", router-attested and guarded to agentd alone. `terminal/create` minus the handle. |
| Per-channel ring buffer | `internal/router/ringbuf.go` | `Snapshot / Len / Truncated` — literally `TerminalOutputResponse`'s shape, built for scrollback replay. |
| `openRawChannel(id, onBytes)` | `web/shell/src/main.tsx:1490` | **Keyed by channel id alone, not by instance.** Any FE can render any channel it is told about. |
| A BE handing its channel to an FE | `apps/connect/fe/src/main.tsx:137` | "when the BE opens an ssh-copy-id pty it sends the raw channel id; we mount a Terminal on it". The pattern already ships. |
| Transcript as a shared buffer | `apps/agentd/be/transcript.go:14` | Written for this: "so that a reload, a second window, and **M6's terminal pane** are all just another subscriber". |

## 3. Who should own the pty

The question that decides the rest. Both options work; they differ in what
breaks.

### Option A — wash-term owns it

`exec_tab` opens the tab; add `tab_output` / `tab_wait` / `tab_kill`,
guarded the same way, and agentd drives them.

- **+** The tab UI comes free: strip, scrollback, search, fonts, close
  confirmation, per-pane status.
- **+** Inherits the reconnect/replay hardening `PTY_ROBUST.md` already did
  for wash-term's channels.
- **−** Three more privileged doors into wash-term. The `exec_tab` comment
  says "one caller deserves one door"; this makes it four.
- **−** **Requires a terminal window to exist.** Headless, kiosk, or simply
  "the user never opened Terminal" all fail, and agentd would have to spawn
  a window as a side effect of an agent running `ls`.
- **−** Lifetime is wrong. Closing the terminal window kills terminals the
  agent is blocked on, and §1.2 says the agent owns that lifetime.

### Option B — agentd owns it (recommended)

agentd calls `pty.Open` itself, exactly as edit and connect do, and puts the
channel id where the FE can find it.

- **+** No new privileged verbs. It is a library call.
- **+** Works with no terminal window open at all.
- **+** Lifetime matches ACP: the terminal belongs to the session, `release`
  ends it, and it dies with the session. `kill` is a method call rather than
  a cross-app request.
- **+** agentd already owns the adapter process, the permission queue and
  the transcript. A terminal is one more resource of the session.
- **+** Output capture is a local bounded buffer sized by
  `outputByteLimit` — no protocol at all.
- **−** "It appears as a tab in my terminal" now needs wash-term to *adopt*
  a channel it did not open; its tab model assumes it called `pty.Open`.
- **−** agentd becomes a process spawner — though it already spawns
  adapters, so not new in kind.

**Take B.** The deciding argument is the one that cannot be designed
around: these terminals must work when no terminal window is open.
Ownership should follow lifetime.

The cost is the good kind. Adoption becomes an FE capability ("mount this
channel as a tab") rather than a BE privilege — and that is the same
capability wash-edit's agent tabs need (`docs/AGENT_TABS.md`). Build it
once, two features use it.

## 4. The one unknown to spike first

`pty.Open(ctx, conn, windowID, …)` takes a window id, and agentd is
`SurfaceBackground` with no windows — `WindowID()` is 0. The router binds a
raw channel to the SHELL (`router.go:1469`, "channel binding, sending
ShellChannelBind + a scrollback replay"), which suggests window 0 is fine
and the bind is shell-wide. That is an inference, not a fact.

**Spike: DONE, and it passes** (`e2e/tests/agent-pty-spike.spec.ts`, 513ms).
agentd opens a pty on window 0 and logs the channel id; the page mounts that
channel with `window.wash.openRawChannel` and the bytes arrive. Option B is
confirmed end to end — the bind really is shell-wide, and a windowless
service can own a pty the browser renders.

Two things fell out of it that shape the milestones below:

- **The router replays a channel's buffered bytes at subscribe time.** The
  first attempt at the spike crashed on a temporal dead zone because the
  callback fired *during* `openRawChannel` — output produced before anyone
  was watching was not lost. That is exactly what `terminal/output` needs
  (create, then poll), and it largely answers §6.5: the ring is not
  wash-term-specific.
- **The handle problem solves itself.** `sess.ID()` — the channel id — is
  the natural `terminalId`, and it is already the one thing an FE needs in
  order to render the terminal.

## 5. Milestones

### M1 — pty capture + exit status (no protocol) — **DONE**

`internal/pty` gains what ACP needs and nothing else:

- An optional bounded capture buffer, sized by the caller. Reuse the
  router's ring semantics — `Snapshot`, and a `truncated` flag once it has
  wrapped — rather than inventing a second kind of ring.
- Retained exit status. The reaper at `pty.go:348` already calls
  `cmd.Wait()` and **logs the result then throws it away**; keep
  `ProcessState` on the Session and extend `onClose` (or add `ExitStatus()`)
  so a caller can report `{exitCode, signal}`.

Testable alone, useful alone (wash-term's own "why did my shell die" story
improves), and no agent involved.

### M2 — agentd's `Terminals` implementation — **DONE**

`CreateTerminal` → `pty.Open` with argv/env/cwd from the request and the
capture buffer sized by `outputByteLimit`; the terminal id is
`"<key>:<channel>"`, matching the roster-key shape agentd already uses.
`TerminalOutput` reads the buffer. `WaitForExit` blocks on the close
signal. `Kill` closes the pty; `Release` drops the record.

Confinement mirrors the fs capability just shipped: `cwd` resolves through
the session root, so an agent cannot spawn a shell somewhere it cannot read.

`Terminal: true` was flipped at the end of M2, with M3 following
immediately — a half-built implementation is worse than none, because the
agent gives up its own fallback the moment we claim the capability.

Shipped as described, plus two things the writing did not anticipate:

- **The terminal id IS the channel id.** No second identifier, so there is
  no mapping to get out of sync, and the id an agent holds is already the
  one an FE needs to render it.
- **The cd happens inside the child**, via an `sh -c 'cd … && exec …'`
  wrapper. agentd hosts several sessions and must not chdir on behalf of
  one; `exec` keeps signals and exit codes coming from the command rather
  than from a shell standing in front of it.

### M3 — see it — **DONE**

The channel id goes on a transcript event (`EventTerminal`) and
`AgentSession` mounts a real `<Terminal>` on it — the same component
wash-term uses, so it scrolls and takes Ctrl+C. Input is deliberately left
enabled: it is a real pty, and interrupting a runaway command is the point.

Watching matters more than it looks. Moving execution behind wash's
boundary is worth having on its own, but "I can see what it did" is the
fallback for not reading every approval — and host-side yolo made that
fallback load-bearing.

One bug fell out of it: these callers built events with `appendPrompt`,
which stores `Kind=user`, then mutated only the pushed copy — so a live
window and a reloaded one disagreed about what the event was. Invisible for
a note, destructive for a terminal, whose channel id was simply lost on
reload. `appendEvent` stores what wash actually originated.

### M4 — adopt it as a tab — **not started**

wash-term learns to mount a channel it did not open: a tab whose pty
belongs to someone else. Needs a tab kind (`owner: 'agent'`), provenance in
the strip so it is obvious whose it is, and the close path routed to the
owner rather than to `pty.Close`. This is the same tab-kind work as
`docs/AGENT_TABS.md` M3, and should be done once for both.

## 6. Decisions that need answers before M2 lands

1. **The user closes an agent's terminal.** Do not hang the agent: answer
   `wait_for_exit` with `{signal: "SIGHUP"}` and let `output` keep serving
   the captured buffer. Silence is the one unacceptable outcome.
2. **`terminal/kill` vs the close confirmation.** We just made closing a tab
   ask (`docs/TERM_LAYOUT.md`). An agent killing its OWN terminal must not
   prompt — the confirmation protects the user's work, and this is not it.
3. **`exec_tab`'s autoclose.** `openTabExec` closes an overridden tab when
   the command exits (`app.go:435`, `--exec` semantics). ACP needs the
   opposite (§1.2). If any of this ends up going through `exec_tab`,
   autoclose must be suppressible.
4. **Permission requests naming a terminal.** A `request_permission` for a
   command the user can *watch* is a materially better question than a
   command string judged blind — and it matters more now that host-side
   yolo exists, because the fallback for "I am not reading every approval"
   should be "I can see what it did".
5. **Does the ring survive reconnect for a non-term channel?** The router's
   replay is per raw channel and looks generic, but wash-term is the only
   consumer today. Worth asserting in M3 rather than discovering later.
