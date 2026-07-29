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
- **`wash agent-hooks install|remove|status` CLI** for headless/remote boxes.

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

- **M1 — status channel.** `internal/pty/agentosc.go` + T0 agent table in
  the foreground poll; `agent_status` app_msg; term FE tab dots + status
  line; `wash-agent-hook status`; Claude Code hook install (panel + CLI).
  Acceptance: run Claude Code in a wash term → dot flips working /
  needs-input / done live; `vi` alone → plain T0 "agent: none".
- **M2 — notifications.** Transition toasts + rate limit + taskbar badge;
  click-to-focus verified e2e.
- **M3 — approval policy.** Socket + `decide` mode + policy rules +
  Agents settings panel + audit logging. Closes #19 item 2 (the right way;
  legacy typed-`y` mode ships OFF).
- **M4 — roster.** agentd + session gateway + AgentsWidget + liveness.
  Remote merge deferred to REMOTE §6.2 scheduling.
- **M5 — smart paste** (§10). Closes #19 item 3 and with it the whole issue.
  Independent of M1–M4; can be built at any point in the train.

Each milestone ships standalone value; M1 is small and immediately visible.

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

## 11. Non-goals / later

- Prompt library / context feed (send selection/diff to a session) — future,
  files-based, outside terminal chrome.
- Codex/Gemini/Aider hook adapters — after M3 proves the shape on Claude Code.
- Agent session restore-after-reboot (`--resume` orchestration) — natural M5
  once the roster knows session ids; not committed yet.
- Cross-agent fleet orchestration (autopilot.sh integration) — separate
  discussion.
