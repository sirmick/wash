# Agent-aware terminals — design + implementation plan

Goal, in one line: **wash terminals know when a coding agent is running in
them, what state it's in, and can answer its permission prompts by policy —
surfaced through the existing badge/notification/sidebar machinery, not a new
terminal app.**

This is the *inverse* of docs/AGENT.md (AI operating the desktop): here wash is
the cockpit for CLI agents (Claude Code, Codex, Gemini CLI, Aider, …) running
in ordinary wash-term tabs, local or remote. The two plans share nothing yet;
keep them decoupled.

Decisions already made (discussion 2026-07-28):

- **No new terminal app.** wash-term grows the integration; the pty layer and
  the cross-app bus carry it. tty7's daemon/GUI split maps 1:1 onto wash's
  existing router/term-BE/FE split — we build the feature set, not the app.
- **Roster is a sidebar widget** (the audio/net pattern), fed by a small
  background service, built *as we go* (M4) — the bus events it consumes are
  designed in from M1 so it's a renderer, never a rework.
- **Notifications ride the existing notify service** (sdk.Info/Warn helpers,
  tray, click-to-focus). No parallel channel.
- **Approval is a policy engine, not `y`-typing.** Claude Code's PreToolUse
  hook can return a programmatic allow/deny; wash answers from per-session
  policy. Issue #19 item 2 lands here.
- **Smart paste (issue #19 item 3) rides along as M5** — pure FE, no shared
  mechanism with the agent work, but same theme (AI-workflow terminal ergonomics)
  and same milestone train. See §10.
- **Prompt library: out of scope.** If it earns existence later it's a
  files-based fm/edit-adjacent surface, not terminal chrome.

## 1. Architecture

```
agent CLI (claude/codex/…)                    ┌─ session FE sidebar
  │ hooks (installed by wash)                 │   AgentsWidget (M4)
  │                                           │        ▲ agent.state
  ├─ wash-agent-hook status ──► OSC 7770 ─┐   │  session BE gateway
  │    (stdin JSON → /dev/tty)            │   │        ▲
  └─ wash-agent-hook decide ◄─► unix sock │   │  com.wash.agentd (M4)
       ($WASH_AGENT_SOCK, M3)     │       │   │   StateService roster
                                  │       ▼   │        ▲ cross-app bus
                     wash-term BE ┴── pty tee: OSC parser (M1)
                                  │  + foreground poll (exists today)
                                  ├──► agent_status app_msg → term FE
                                  │      tab dot / status line (M1)
                                  └──► sdk.Info/Warn → notify tray (M2)
```

The pty is owned by wash-term's BE process; the OSC parser tees the output
stream *there* — before the router's scrollback ring — so replays/resyncs
never re-fire events, and remote terminals (wash-remote) work unchanged
because the parse happens where the pty lives.

## 2. Detection tiers

| Tier | Mechanism | Gives | Needs install? |
|---|---|---|---|
| T0 | existing 1Hz foreground-process poll (`pty.Session.ForegroundUser` seam) — match comm against a small agent table (`claude`, `codex`, `gemini`, `aider`, `amp`, …) | "an agent is running here" — brand icon on tab, roster row exists | no |
| T1 | OSC 7770 status events emitted by installed hooks | working / needs-input / done, session id, turn timing, cwd | yes (per agent CLI) |
| T2 | PreToolUse decide callback over `$WASH_AGENT_SOCK` | policy-driven allow/deny/ask | yes + opt-in policy |

T0 alone is useful (it's how the root/ssh badges already work) and covers
agents we never write hooks for. T1 refines T0's row; T2 is strictly opt-in.

## 3. OSC status channel

Grammar (private-use OSC id, terminated BEL or ST):

```
OSC 7770 ; v=1 ; ev=<event> [; k=v]… BEL
ev ∈ start | working | needs-input | done | end
keys: agent=<slug> session=<id> reason=<permission|idle> turn_ms=<n> cwd=<path> mode=<permission_mode>
```

Parser rules (internal/pty, new `agentosc.go`):

- Tee-scan the pty output copy path; bounded accumulator (1 KiB max per
  sequence, drop on overflow); values %-decoded; unknown keys/events ignored
  (forward compat).
- **Do not strip** the sequence from the stream — xterm.js silently ignores
  unknown OSC ids, and stripping would mean rewriting the byte stream that
  PTY_ROBUST just made byte-exact. The ring therefore contains old OSC events;
  harmless, because parsing never happens FE-side or on replay.
- **Advisory only.** Anything running in the pty can emit these (they're just
  bytes). State drives UI — dots, toasts, roster rows — and must NEVER drive
  input into a pty or a policy decision. The decide path (M3) is a separate
  socket with its own trust story.

## 4. `wash-agent-hook` helper (new multicall entry)

FE-less helper following the fswatch pattern (registry entry + Makefile
.PHONY — see the FE-less-service gotcha). Two modes, both reading the hook's
stdin JSON:

- `wash-agent-hook status`: map the hook event to an OSC 7770 event and write
  it to `/dev/tty` (lands in the pty stream wherever it runs — local, ssh,
  wash-remote — no wash coupling at all). Exit 0 always; \<100ms.
- `wash-agent-hook decide`: PreToolUse only. Connect `$WASH_AGENT_SOCK`, send
  `{session_id, tool_name, tool_input, cwd, permission_mode}`, print Claude's
  `hookSpecificOutput` JSON with the returned `permissionDecision`
  (`allow|deny|ask`). No socket / timeout / any error → print
  `permissionDecision:"defer"` (Claude Code falls through to its normal
  interactive flow — fail-open to *asking the human*, never to allowing).

### Claude Code hook matrix (verified against docs v2.1)

| Hook event | matcher | helper mode | async | maps to |
|---|---|---|---|---|
| SessionStart | `*` (source in payload: startup/resume/clear/compact/fork) | status | yes | ev=start |
| UserPromptSubmit | — | status | yes | ev=working |
| Notification | `permission_prompt`, `idle_prompt` | status | yes | ev=needs-input reason=… |
| Stop | — | status | yes | ev=done |
| SessionEnd | — | status | yes | ev=end (1.5s budget — helper is well under) |
| PreToolUse | `*` (policy narrows, not the matcher) | decide | no (inherently sync) | M3 |

All status hooks `async: true` so they never block a turn. Session identity
comes free: every hook payload carries `session_id`, `cwd`,
`permission_mode`; `--resume <id> --fork-session` makes roster rows
actionable later (copy-session-id, relaunch-after-reboot).

Codex / Gemini / Aider / others: T0 detection only at first; each gets a
hook/wrapper adapter behind the same helper when it earns the effort.

### Install

Two paths, one implementation (additive JSON merge, never destructive,
idempotent, marked entries so removal only touches ours):

- **Settings → Agents panel** (define-settings-panel): opt-in toggle per
  agent CLI; shows exactly what will be written to `~/.claude/settings.json`.
  Deferred to M3 (§9.1), where the same panel carries the policy rules.
- **`wash agent-hooks install|remove|status` CLI** for headless/remote boxes.
  Shipped in M1: `--dry-run` prints the merged file, `--path` targets a
  non-default settings file, and the first write leaves a `.wash-bak` copy
  of the original.

Claude Code hot-reloads settings files, so install applies to running
sessions without restart.

## 5. Term integration (M1+M2)

- BE: OSC events + T0 poll fold into the existing `tab_status` push pattern
  (send-on-change, dedupe key per channel) as a new `agent_status` app_msg:
  `{channel_id, agent, state, since_ms, session_id, reason}`.
- FE: tab chip gets a state dot (blue working / amber needs-input / green
  done) beside the user badge, driven off a side map like tabStatus — no
  TabMeta object churn (the `<For>`-remount hazard). Status line appends
  "· claude working 4m".
- Notifications (M2), on *transitions only*, rate-limited per channel
  (≥5s between toasts, needs-input always wins over done):
  - → needs-input: `sdk.Warn` "Claude needs your input (wash · main)"
  - working → done: `sdk.Info` "Claude finished after 43s"
  Toast carries the instance id → existing click-to-focus lands on the
  window. Taskbar amber badge rides the same event via the session app.
- Ephemeral state: nothing persisted; a reattach re-seeds from the next poll
  tick / next OSC event (same story as tab_status today).

## 6. Approval policy (M3)

- Per-session unix socket, `$XDG_RUNTIME_DIR/wash/agent-<pid>-<chan>.sock`,
  mode 0600, exported as `WASH_AGENT_SOCK` via the existing `pty.WithWashEnv`
  injection. Term BE owns the listener; one JSON request/response per
  connection.
- Policy model (persisted via the Agents settings panel):
  - default: `ask` (= defer to Claude's own prompt)
  - rules: ordered matchers on `tool_name` + arg patterns
    (`Bash(git status*) → allow`, `Read → allow`, `Bash(rm *) → deny`,
    `Edit → ask`), scoped global / per-cwd-prefix.
  - kill-switch: policy off ⇒ helper defers everything.
- Every non-`ask` decision is logged (`term: agent-decide session=… tool=…
  decision=… rule=…` per the logging convention) and auto-denies also toast.
- Trust: the socket answers *questions*; it never initiates. A malicious
  pty process can ask it things — the answer leaks only the policy verdict.
  The spoofable OSC channel (§3) has no path into decisions.
- Hookless agents: an explicit opt-in "legacy auto-approve" that
  pattern-matches the prompt and types `y` remains OFF by default and is
  documented as spoofable-by-design. It exists because #19 asked for it; the
  policy path is the recommended mechanism.

## 7. Roster service + sidebar (M4)

- Term BE additionally publishes `agent_status` to `com.wash.agentd`
  (SendAppMsgTo, fire-and-forget) — same event, second consumer.
- `com.wash.agentd`: surface=background, sdk.StateService holding roster
  rows keyed `(origin, term_instance, channel_id, session_id)`:
  `{agent, state, since, cwd, session_id, term_instance, window_id}`.
  Liveness: explicit remove on tab close + TTL sweep (rows not refreshed in
  60s go stale-grey, then drop) — a crashed term never leaves a ghost row.
  Justifies service-hood day 1: N term processes producing, sidebar + policy
  audit consuming (the no-premature-service rule is satisfied).
- Session BE: `registerAgentGateway` (subscribe/unsubscribe forwarders,
  the exact netd/audio shape at apps/session/be/app.go).
- Session FE: `AgentsWidget` in the sidebar — rows sorted needs-input-first,
  each with brand dot, repo dir + branch, state + elapsed; click →
  focusWindow on the owning terminal. Git branch/±diff comes from agentd
  shelling `git -C cwd` lazily (never from hooks).
- Remote: the widget is REMOTE.md §6.2 "merge" class — A's session FE
  subscribes to B's agentd over the B RouterClient, rows host-coloured.
  Local-first; the merge lands with the wider §6.2 work, not before.

## 8. Testing

- Unit: OSC parser (bounds, torn sequences across read chunks, %-decode,
  unknown-key tolerance — fuzz the accumulator); policy matcher table; helper
  decide-path fallback (no socket → defer).
- e2e (full-stack, per the test-app pattern: Playwright FE assertions +
  router-log BE assertions): a fake agent — a shell script that printf's
  OSC 7770 events — drives tab dot testids and a needs-input toast; an
  approval e2e runs the real helper against a term-BE socket with a canned
  policy. No real agent CLIs in CI.
- `make test-race` on the new term-BE fan-out (StateService copy-on-write
  rule applies to roster snapshots).

## 9. Milestones

- **M1 — status channel. DONE** (see §9.1 for what shipped).
  `internal/pty/agentosc.go` + T0 agent table in
  the foreground poll; `agent_status` app_msg; term FE tab dots + status
  line; `wash-agent-hook status`; Claude Code hook install (CLI; the
  panel moved to M3 — see §9.1).
  Acceptance: run Claude Code in a wash term → dot flips working /
  needs-input / done live; `vi` alone → plain T0 "agent: none".
- **M2 — notifications. DONE** (see §9.2). Transition toasts + rate limit +
  taskbar badge; click-to-focus verified e2e.
- **M3 — approval policy. DONE** (see §9.3). Socket + `decide` mode +
  policy rules + Agents settings pane + audit logging. Closes #19 item 2
  (the right way; legacy typed-`y` mode ships OFF).
- **M4 — roster. DONE** (see §9.4). agentd + session gateway +
  AgentsWidget + liveness. Remote merge deferred to REMOTE §6.2
  scheduling.
- **M5 — smart paste. DONE** (§10, see §9.5). Closes #19 item 3 and with it
  the whole issue. Independent of M1–M4.

Each milestone ships standalone value; M1 is small and immediately visible.

### 9.1 M1 as built

Map of the shipped code, and the few places the implementation pinned down
a detail the design left open:

| Piece | Where |
|---|---|
| OSC 7770 scanner + T0 agent table | `internal/pty/agentosc.go` (+ `pty.Session.SetAgentHandler`, the tee in `pty.Open`) |
| per-tab merge, `agent_status` push | `apps/term/be/agent.go` |
| tab dot + status-line clause | `apps/term/fe/src/main.tsx` (`agentStatus` side map) |
| hook helper (`status` mode) | `internal/agenthook/agenthook.go`, `cmd/wash-agent-hook/` |
| hook install/remove/status | `internal/agenthook/settings.go` + `cli.go` (`wash agent-hooks …`) |
| tests | `internal/pty/agentosc_test.go` (+ fuzz), `internal/agenthook/*_test.go`, `apps/term/be/agent_test.go`, `e2e/tests/term-agent.spec.ts` |

- **Wire states are four, not five.** `agent_status.state` is
  `running | working | needs-input | done`; `ev=start` maps to `running`,
  which is also what T0 alone reports. `ev=end` clears the record (an
  empty `state` on the wire = "no agent here"), and the FE colours
  running with a muted dot, so "an agent is here but not reporting" reads
  differently from all three live states.
- **T0 is the liveness signal for T1.** An agent killed with SIGKILL never
  fires `SessionEnd`, so an OSC state whose agent has been absent from the
  foreground for 30s is dropped — except while the foreground is ssh,
  where T0 can't see a remote agent by construction.
- **The FE keeps a side map keyed by channel id** (like `tabStatus`),
  never a field on `TabMeta`: the term-host `<For>` is keyed by object
  identity, so touching a tab object remounts its xterm.
- **Hook entries are marked by their command string** containing
  `wash-agent-hook`, not by a custom JSON key — agent settings schemas
  validate hook entries, and an unknown field risks the whole block being
  rejected. Removal only ever touches marked entries.
- **The Settings → Agents panel is deferred to M3**, where it has the
  policy rules to render as well; `wash agent-hooks install|remove|status`
  is the M1 install path and the panel will call the same
  `agenthook.Install/Remove/Status` functions.
- **e2e stand-ins**: a shell script printf'ing OSC 7770 for T1, and an
  executable named `claude` for T0 (comm of a shebang script is the
  script's own name) — plus the negative case (`sleep` is not an agent),
  which is where a loose agent table would show up.

### 9.2 M2 as built

| Piece | Where |
|---|---|
| toast decision + rate limit | `apps/term/be/agenttoast.go` |
| click-to-focus | `web/shell/src/notify.ts` (`onActivate`) + `focusInstance` in `web/shell/src/main.tsx` |
| toast source attribution | `wire.EvtNotify.Source` → `relayNotify` → `sdk.Conn.NotifyFrom` → `apps/notify/be` |
| taskbar attention badge | `apps/session/fe/src/main.tsx` (`wantsAttention` / `WindowPill`) |
| tests | `apps/term/be/agenttoast_test.go`, `internal/router/notify_forward_test.go`, `e2e/tests/term-agent-notify.spec.ts` |

- **The toast's instance id was wrong before this milestone.** §5 assumed
  click-to-focus fell out of the existing `instance_id`, but the notify
  service is the single authority for toasts and its re-emit stamped *its
  own* instance — every toast in wash pointed at `com.wash.notify`. Fixed
  at the source: `EvtNotify` gained an optional `Source`, honoured **only**
  on the notify service's own emit (every other app's notify takes the
  forward-to-service path, which rebuilds the payload), so an app cannot
  pin its toast on a window it doesn't own. Click-to-focus now works for
  every app's notifications, not just agents'.
- **Click-to-focus snaps the viewport** to the window's cell before
  focusing — focusing a window one cell over is otherwise invisible.
- **The taskbar badge is generic, not agent-specific.** A pill wears the
  amber dot while its instance has an unread warn/error notification, and
  visiting the window marks those read. That is the same event §5 asks for
  (the needs-input toast) without inventing plumbing M4 would replace:
  `com.wash.session` is `InstancingSingle`, so a term BE *cannot* address
  it by app id — the agent-shaped roster feed genuinely needs M4's agentd
  singleton. The badge outliving the toast is the point: a toast fades in
  4.5s, an agent waiting on you does not.
- **Toast rules**: needs-input (warn) on arrival at that state from
  anywhere; done (info, with the turn length) only from `working`, so a
  session tidying up is silent. One toast per tab per 5s, except a
  needs-input warn may interrupt a preceding info — the human is blocked,
  and "it finished" must not swallow "it needs you". The limit is
  per-tab, so one chatty agent can't mute another.

### 9.3 M3 as built

| Piece | Where |
|---|---|
| policy model + matcher | `apps/term/be/policy.go` |
| per-tab decision socket | `apps/term/be/agentsock.go` (+ `WASH_AGENT_SOCK` via `withAgentSock`) |
| `decide` helper mode | `internal/agenthook/decide.go` |
| legacy typed-`y` (opt-in, off) | `apps/term/be/autoapprove.go` (+ `pty.Session.SetOutputTap` / `Inject`) |
| Agents settings pane | `apps/settings/fe/src/AgentsPane.tsx` + the `agents` domain in `apps/settings/be` |
| tests | `apps/term/be/{policy,agentsock,autoapprove}_test.go`, `internal/agenthook/decide_test.go`, `e2e/tests/term-agent-policy.spec.ts` |

- **The fail-open answer is silence, not `defer`.** §4 specified
  `permissionDecision:"defer"`; Claude Code 2.1 accepts that in print mode
  only and logs *"returned permissionDecision=defer in interactive mode;
  ignoring (defer is print-mode only)"* — interactive being exactly the
  wash-terminal case. The helper therefore prints **nothing** for ask / no
  socket / timeout / garbled answer, which leaves the agent's own prompt
  precisely as it would have been without wash. Printing `"ask"` was
  rejected as the alternative: it would override the user's own allowlist
  and make wash's presence more annoying than its absence.
- **PreToolUse is installed with the status hooks** (matcher `*`, NOT
  async — the agent is blocked on the answer), because it is inert without
  a policy: no file, or `enabled:false`, and the helper says nothing.
- **Policy lives in a settings domain, not a service.** `agents.json` is
  written by the pane through the settings host and read by every
  wash-term with a 500ms mtime cache, so a rule change applies to running
  terminals with no restart and no IPC.
- **The Agents surface is a native settings pane, not a
  define-settings-panel bundle.** The settings host addresses panels by
  app id, and the router only resolves an app id to an instance for
  *singletons* — wash-term is `InstancingMulti`, so a term-owned panel
  could never receive a message. M4's agentd is a singleton and can host a
  real panel later; the domain file doesn't change, so that is a UI move.
  Hook install stays the CLI (`wash agent-hooks install`), which the pane
  shows with a copy button.
- **Auto-deny toasts.** A denial is the case where an agent looks stuck
  for a reason the user can't see, so `deny` raises a warn toast beside
  the audit line.
- **Legacy typed-`y` has a fourth gate the design didn't ask for**: it
  fires only while a T0-detected agent is the tab's *foreground* program.
  A `(y/n)` printed by a shell, a build, or `cat`ting a source file can
  never be typed into. Together with the opt-in flag, the policy kill
  switch, and end-of-output anchoring, that is what keeps a
  spoofable-by-design feature from being reckless — and every injection is
  logged and toasted.

### 9.4 M4 as built

| Piece | Where |
|---|---|
| roster service | `apps/agentd/be/{app,service,git}.go` (+ `cmd/`, `SVC_APPS`) |
| terminal → roster | `publishRoster` in `apps/term/be/agent.go` |
| session gateway | `registerAgentGateway` + `serviceFEKind` in `apps/session/be/app.go` |
| sidebar widget | `apps/session/fe/src/sidebar/AgentsWidget.tsx` (+ Agents section) |
| tests | `apps/agentd/be/{service,git}_test.go`, `AgentsWidget.ctest.tsx`, `e2e/tests/term-agent-roster.spec.ts` |

- **Terminals heartbeat, they don't just report.** §7's liveness only
  works if silence means death, so a tab with a live agent re-states it to
  agentd every 15s (`rosterKeepalive`) on top of every change — inside the
  60s stale window. A same-state keepalive must NOT restart the elapsed
  clock, or "waiting 5 minutes" would read as "waiting 15 seconds"
  forever, which is the number the roster exists to show.
- **Three liveness layers, in order of speed**: `ev=end` clears the row,
  tab close retracts it explicitly, and the sweep (stale at 60s, dropped
  at 2m) catches a terminal that died without saying either.
- **Rows are sorted server-side** into attention order (needs-input,
  longest-waiting first) so every consumer — sidebar today, anything else
  later — agrees on what matters most without re-deriving it.
- **The row key is `<term instance>:<channel id>`** with the instance
  taken from the router-attested sender, so one terminal can never
  describe another's tabs.
- **git runs in the service, never in a hook**: `git -C <cwd>` for branch
  + dirty, cached 30s per directory, collapsed across concurrent tabs in
  one repo, off the dispatch path, and best-effort (a non-repo renders as
  just the directory name).
- **The Agents settings pane stays where M3 put it.** agentd is a
  singleton and could host a real define-settings-panel now, but the
  policy file is already the contract and moving the UI buys nothing this
  milestone — the note in §9.3 stands as the follow-up.
- **The M2 taskbar badge also stays generic.** With agentd in place an
  agent-specific taskbar feed is now possible; the unread-warn badge
  covers the same case and is one fewer moving part, so it survives until
  something wants more.

## 10. Smart paste (M5, issue #19 item 3)

Pastes copied out of AI chats arrive broken in two ways, and every paste path
already funnels through one choke point (`TerminalAPI.paste` in the shared
terminal component — menu, right-click, Ctrl+Shift+V), so both are fixed
there, FE-only:

- **Wrap artifacts**: the chat's soft line-wrap becomes hard newlines, so one
  logical command pastes as N lines and the shell runs each separately.
  Fingerprint of "one wrapped command" (vs a genuine multi-line script):
  line lengths clustered near the longest (a common wrap column), breaks
  landing mid-token or before a `--flag`, no shell structure at line starts
  (`if`/`for`/`#`/`&&`/trailing `\`). Repair = join the lines back into one
  (the newlines are artifacts; joining ≡ the issue's trailing-`\` ask, minus
  the noise). Real scripts are left structurally intact.
- **Smuggled junk**, which bites single lines too: leading `$ ` prompt
  markers, curly quotes where the shell needs straight ones, non-breaking
  spaces (U+00A0 — the invisible "command not found" generator), zero-width
  chars, stray code-fence backticks. Mechanical normalization, always safe.

UX — never silently rewrite structure:

- Single-line pastes with junk: cleaned silently (stripping U+00A0 is never
  wrong).
- Multi-line or ambiguous: a small preview overlay — cleaned result with the
  original diff-highlighted — **Paste cleaned / Paste as-is / Cancel**, plus
  a term-menu setting `Smart paste: ask · always · off`.
- Paste-jacking guard for free: `TermModes` already tracks the shell's
  bracketed-paste state (DEC 2004), so the overlay warns specifically when a
  multi-line paste is about to hit a shell that will execute it line-by-line
  immediately.

Implementation: pure `analyzePaste(text) → {verdict, cleaned, issues}` kernel
(decision-kernel pattern, exhaustively unit-tested — wrap detection is all
heuristics and the tests ARE the spec), overlay in the term FE, hook into
`paste()`. No BE/protocol change. e2e: clipboard-inject wrapped and junked
payloads, assert overlay verdicts and what actually reached the pty
(router-log side).

### 9.5 M5 as built

| Piece | Where |
|---|---|
| `analyzePaste` kernel | `web/lib/src/paste-analyze.ts` (exported from `@wash/ui`) |
| paste choke point | `beforePaste` prop + `pasteFiltered` in `web/lib/src/terminal.tsx` |
| preview overlay + policy menu | `apps/term/fe/src/PasteOverlay.tsx`, `apps/term/fe/src/main.tsx` |
| tests | `web/lib/src/paste-analyze.test.ts` (21 cases), `e2e/tests/term-smart-paste.spec.ts` |

- **The kernel's verdict is three-valued**, which is what makes the UX rule
  from §10 expressible: `as-is` (nothing found), `clean` (invisible junk on
  a single line — applied silently, because showing a dialog for a
  non-breaking space is worse than fixing it), `ask` (anything structural,
  or multi-line, or a paste-jacking warning).
- **Wrap detection says no by default.** Four independent vetoes — shell
  structure at a line start, a trailing continuation/operator, no wrap
  column (lines not clustered, or under 40 chars), and a too-short tail
  line — because joining a real two-command script is a much worse failure
  than leaving a wrapped command unjoined. The "must never join" table in
  the tests is the specification.
- **Two join rules, not one**: a line that stopped exactly at the wrap
  column and continues a token rejoins with nothing (a URL cut mid-path);
  anything else rejoins with a space (word wrap replaced the space). The
  wrap column is derived from the head lines only — with two lines, the
  last one says nothing about the width.
- **The component holds no policy.** `beforePaste` is a prop; with it
  absent (wash-edit's embedded terminal) every paste goes through
  untouched. wash-term owns the analysis, the overlay and the
  ask/always/off setting, which persists with the rest of its window state.
- **Native paste is intercepted too.** §10 said all paste paths funnel
  through `TerminalAPI.paste`, but a plain Ctrl+V (or the browser's own
  Edit▸Paste, or middle-click) is delivered straight to xterm's hidden
  textarea and would have bypassed the filter. When (and only when) a
  `beforePaste` is installed, the component takes the DOM paste event in
  the capture phase and re-enters through the same choke point — covered
  by its own e2e case.
- **The e2e asserts bytes, not dialogs**: every case pastes into
  `cat > file` and reads the file back, so what is being checked is
  literally what the shell would have run.

## 11. Non-goals / later

- Prompt library / context feed (send selection/diff to a session) — future,
  files-based, outside terminal chrome.
- Codex/Gemini/Aider hook adapters — after M3 proves the shape on Claude Code.
- Agent session restore-after-reboot (`--resume` orchestration) — natural M5
  once the roster knows session ids; not committed yet.
- Cross-agent fleet orchestration (autopilot.sh integration) — separate
  discussion.
