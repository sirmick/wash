# Mission Commander — activity journal, observation, and summaries

Status: design (2026-09-16); **§9 step 1 (journal + Timeline) built
2026-09-16** — `internal/activity`, `internal/router/activity.go`,
`pkg/wire/activity.go`, `web/shell/src/activity.ts`, the session sidebar's
Timeline, notes from agentd/priv/bulk, `e2e/tests/activity-journal.spec.ts`.
Supersedes the observation and Mission Commander sections of
`AI_PROVIDER.md` (§7–9) where they differ; the provider service, its request
contract and its security rules stand as shipped in 0.16.0.

## 1. What it is for

Three questions a person asks of their desktop, in the order they come up:

1. **What is this doing?** — one window, now.
2. **What happened?** — today, the last hour, while I was away.
3. **Where was I?** — pick up again: the windows, the sessions, the next step.

The first is answered from a window's current content. The second and third
are answered from *time*, and wash keeps almost no durable record of time
today: agent transcripts are on disk, Recent remembers a few paths, priv keeps
an audit log, and everything else is gone on reload. A summarizer that
re-reads windows can never say what happened while nobody was watching — and
"while nobody was watching" is when detached agents do the interesting work.

So the design has a durable spine and an AI layer over it, and they are
separate things:

```text
   apps ──activity.note──▶ ┌────────────── router ──────────────┐
   (attested, bounded)     │  journal   append-only, on disk    │──activity.query/tail──▶ Timeline, Commander (shell)
   router's own events ──▶ │  observe   tails, state, exports   │──observe──────────────▶ commander service ──▶ inference ──▶ briefs, rollups
                           └───────────── deterministic ────────┘                              ▲                        │
                                                                                               └── writes back as journal entries
```

The router **records and answers**; it never calls a model. Everything that
spends provider quota or leaves the machine lives outside it.

## 2. Decisions

- **The journal lives in the router.** The router already witnesses most of
  the timeline on its frame path — windows opening, closing, retitling and
  focusing; every open it routes and spawn it performs; sessions attaching
  and detaching; remote hosts joining and leaving. A separate service would
  receive a forwarded copy one hop later from a process that must autoboot,
  be packaged, and can be down when the router is not. There is no
  cross-process coordination to justify a service (the "no premature
  service" rule); there are facts and a file.
- **Observation is mostly automatic, from what the router already holds.**
  Every raw pty channel bound to a shell keeps a 256 KiB scrollback ring for
  reconnect replay (`ChannelScrollbackBytes`); every instance's persisted
  `app_state` blob is in `windowSession.appState`. A tail of the ring, or the
  state blob, is the default observation of any window. An app that knows
  better **refines** it with a backend-side export; it never has to, and
  nothing is unobservable because an app has not done the work yet.
- **The router stays deterministic.** Capture, redaction, storage and query
  are code with tests. Scheduling, consent and inference belong to a
  separate, optional background service (`com.wash.commander`) and to the
  already-shipped `com.wash.inference`.
- **Automatic understanding is on-box by default.** Scheduled summarization
  runs only when the selected provider connection is local (Ollama or a CLI
  adapter), or when the person has explicitly allowed a hosted provider for
  it in Settings. On-request summaries follow the rule already shipped: they
  go wherever the selected provider is, after a click.
- **Pointers, never bodies.** The journal stores a bounded line per event
  and a reference (transcript seq, session id, path, window). It never stores
  transcript text, scrollback, or file contents. Observations are served
  live from the router and are not persisted. Generated briefs and rollups
  are the only text the journal keeps beyond one line, and they are what the
  person asked for.
- **Every row is a jump.** Journal entries carry an intent — focus this
  window, resume this session, open this path — using the machinery the
  start menu's Recent pop-outs already built. A rollup ends in actions, not
  prose.
- **Per host, aggregated per seat.** Each router journals what happens on
  its host. The shell reads every connected origin and merges, as the rail
  does for awareness (`SIDEBAR.md` §3). Nothing crosses hosts unasked.

## 3. The journal

### 3.1 Entry

```json
{"ts":1789603200123,"seq":4182,"host":"local",
 "kind":"agent.turn","app":"com.wash.agentd","instance":"i-7","window":3,
 "title":"Fix the reconnect banner race",
 "line":"turn 12 done: edited web/shell/src/ws.ts, ran make unit-test (ok)",
 "ref":{"session_id":"…","seq":812},
 "intent":{"kind":"resume","session_id":"…"}}
```

- `ts` unix ms, `seq` per-host monotonic; `(host, seq)` is the identity.
- `kind` is dotted, namespaced by source: `window.open|close|focus|title`,
  `open.routed`, `spawn`, `session.attach|detach`, `peer.up|down`,
  `agent.start|turn|tool|ask|answer|end|resume`, `priv.escalate`,
  `bulk.start|done|fail`, `term.cmd.begin|end` (later), `brief`, `rollup`.
- `line` is ≤ 200 bytes, plain text, written by the source. The router
  truncates at a rune boundary and marks it.
- `ref` and `intent` are optional; intents are exactly the shapes
  `recent.open` / `agent_open` / focus already accept.
- `app`/`instance` are the router-attested sender for app-noted events and
  the router itself for its own.

### 3.2 Sources, v1

| source | events | how |
|---|---|---|
| router | window.*, open.routed, spawn, session.*, peer.* | appended where the fact is already known (the same places `noteOpenRouted` fires) |
| agentd | agent.* as pointers into its transcript | `activity.note` on each turn end, tool completion, ask/answer, start/end/resume |
| priv | priv.escalate | `activity.note` beside the existing audit line |
| fm / bulk | bulk.* | `activity.note` on job transitions |
| term | term.cmd.* | **later**, needs shell integration (§4.4) |

Not in v1: file contents, terminal command lines, clipboard, keystrokes.

### 3.3 Ingest

`activity.note` is a wire event from an app's backend to the router (not an
app message to the session app): the router stamps `ts`, `host`, `app`,
`instance`, `window`, and refuses a note that names another app. It is
rate-limited per instance (a chatty agent must not be able to write the
journal full) and bounded per line. A refused note is logged, never fatal.

### 3.4 Store

- `$XDG_STATE_HOME/wash/activity/YYYY-MM-DD.jsonl`, one directory per host
  (each router writes its own; there is no sharing).
- Written by one goroutine behind a bounded queue. The frame path only
  enqueues; if the queue is full the entry is dropped and a `dropped` counter
  bumps (surfaced in About). A slow disk can never stall a frame — the same
  rule QoS applies to the socket.
- Retention: 30 days by default, plus a size cap (64 MiB); oldest day first.
  Both are settings. `activity.clear` deletes everything.
- Off switch: `--no-activity` (kiosk, CI); the query verbs answer empty.
- Router restart: the file is the truth; in-memory holds the current day's
  index for tail and recent queries.

### 3.5 Query

On the shell's control channel, per origin:

- `activity.query {from, to, kinds?, apps?, text?, limit, cursor}` →
  entries newest-first with a cursor. Text search is over `title` and `line`.
- `activity.tail` subscribes to new entries, pushed Control/Background-class
  like `link.stats`, so a Timeline view stays live without polling.
- `activity.stats` → counts by kind/day, dropped, bytes on disk (About).
- `activity.clear`.

An app backend may query too (attested), which is how the commander service
reads.

## 4. Observation

### 4.1 Verb

`observe {instance_id}` (control channel, and attested app-to-router) →

```json
{"source":"export|pty-tail|app-state|none","revision":"…",
 "content_type":"text/plain|application/json","content":"…",
 "truncated":false,"captured_at":1789603200123,
 "window":{"app":"…","title":"…","state":"normal","focused":true}}
```

Resolution order: an app export if the app has one and it is fresh; else the
pty tail for an instance that owns a pty channel; else the state blob; else
`none`. The response names which, so a consumer can say "from the terminal's
own report" versus "from what was on screen".

### 4.2 pty tail (automatic)

- Only channels of pty kind (what wash-term and agent terminals open); never
  file, asset, bundle, video or peer channels.
- The last N KiB of the ring (default 16, cap 64), then: strip CSI/OSC/DCS
  and C0 controls, resolve carriage-return overwrites and backspaces within a
  line, collapse blank runs, drop lines that are only cursor noise.
  Full-screen programs (vim, htop) redraw cells rather than append lines, so
  their tail is poor; that is the case §4.4 exists for.
- Redaction before it leaves the router: the same `secretish` shapes the
  inference service scrubs from CLI diagnostics (bearer tokens, `key=`/`token=`
  assignments, vendor key prefixes). A local model never sees a pasted token
  either.
- `revision` is the channel's bytes-seen counter: cheap change detection for
  the scheduler.

### 4.3 State blob (automatic)

The instance's persisted `app_state`, as is, with `revision` its version. The
apps that persist private content in it today (edit persists every tab's
text) are exactly the ones that should refine with an export (§4.5); until
they do, the blob is what a summary sees, under the consent rules of §5.3.

### 4.4 Manifest eligibility

A manifest field `observation: auto | export | none`:

- `auto` (default for wash's own apps): tail or blob as above.
- `export`: the app answers `observe` itself (§4.5); the router falls back to
  `auto` when the app does not answer within 250 ms or is gone.
- `none` (default for third-party apps, permanent for login, priv, settings'
  provider panel, and inference itself): `observe` answers `none`.

### 4.5 App exports (refinement)

Backend-side only. `sdk.HandleObserve(bus, func() sdk.Observation)` answers
the router's request from the app's own knowledge:

- **term**: the current command, cwd, last exit status and duration from
  shell integration (OSC 133 prompt/command markers, the mechanism VS Code
  and Kitty use), plus a clean tail it keeps from the pty it owns. This is
  also what makes `term.cmd.*` journal events possible.
- **agentd**: structured turns and compact tool summaries for a session,
  no images, no file bodies.
- **edit**: path, language, selection or a bounded document snapshot, and
  never for a tab the person marked private.

Exports are synchronous, side-effect free, and bounded (128 KiB); the router
truncates and marks what it must.

## 5. Summaries

### 5.1 The commander service

`com.wash.commander` is an optional background service (surface=background,
autoboot, `sdk.StateService` for its settings). It owns the schedule and the
consent; it calls `com.wash.inference` for every model call and never talks
to a provider itself. It is the only component that spends anything.

### 5.2 Two shapes

- **Brief** — one window, now. `ActivityBrief` as in `AI_PROVIDER.md` §9
  (goal, state, now, done, in progress, blockers, next), from one
  `observe` result, one bounded request. Session Summary as shipped is
  "brief the live windows now" and becomes a consumer of this path.
- **Rollup** — a span of the journal. Input is the entries in `[from, to]`
  (lines and refs only; bounded to the request cap by dropping the oldest
  low-kind events first), optionally the briefs already generated in the
  span. Output:

  ```go
  type Rollup struct {
      Span        [2]int64 `json:"span"`
      Headline    string   `json:"headline"`
      Workstreams []struct {
          Title string   `json:"title"`
          State string   `json:"state"` // active|waiting|blocked|done
          Now   string   `json:"now,omitempty"`
          Next  []string `json:"next,omitempty"`
          Refs  []Ref    `json:"refs"`   // journal (host, seq) → intents
      } `json:"workstreams"`
      Blockers []string `json:"blockers,omitempty"`
  }
  ```

  Refs are journal identities, so every workstream resolves to jumps.

Both are written back into the journal (`kind: brief|rollup`, with the
generating provider/model, span and revision), so the Timeline shows
summaries in place and nothing needs a second store.

### 5.3 Consent and modes

- **On request**: Summarize / Refresh / Roll up buttons. Goes to the
  selected provider, whatever it is. Shipped rule; unchanged.
- **Automatic**: a Settings switch, off by default. When on, the commander
  polls `observe` revisions and the journal tail on an idle-aware schedule
  (default: every 5 minutes while the seat is active, a rollup at each
  disconnect and at day end), with a per-hour request budget. It runs only
  if the selected connection is local (Ollama, a CLI adapter) — or the
  person has ticked "also with hosted providers", which is its own switch
  with its own warning.
- **Never**: the router serves an observation to a caller that is not the
  commander, the session app, or an app asking about its own instance.
  Automatic mode never observes `none` apps and never reads an instance the
  manifest excludes.
- Disabling automatic mode cancels queued work and stops the schedule; the
  briefs already in the journal stay until cleared, because they are the
  person's record.

### 5.4 Resume

On a shell connecting after more than a configurable gap (default 30
minutes), the commander produces a rollup over `[last disconnect, now]`
merged with the live roster, and the session app shows it as a card:
headline, workstreams with jump buttons (focus window, resume session, open
folder), and what agents did while away. With AI off, the card is the same
thing without prose: the windows that changed, the agents that finished or
are waiting, the last few journal rows.

## 6. Views (shell / session app)

- **Timeline**: a searchable list, grouped by hour and host, every row a
  jump; filters by kind and app; live via `activity.tail`. Useful with no
  provider configured at all.
- **Mission Commander**: one card per live window (title, icon, host, state,
  attention), ordered by attention then recent focus, each with its latest
  brief and a stale marker when the revision moved; a Roll up button; the
  Timeline beneath; the Resume card on top after a gap. Selecting a card
  uses the existing focus path.
- Session Summary's window becomes MC's "brief the live windows now".

## 7. Security and privacy rules

1. The router never calls a model and never sends anything off the machine.
2. The journal holds one bounded line and pointers per event; never bodies.
3. Observations are served live and never persisted by the router.
4. Tails are redacted for credential shapes before they leave the router.
5. Sensitive surfaces are permanently `none`; third-party apps default to
   `none`.
6. Automatic mode is off by default and on-box by default; hosted automatic
   is a separate, warned switch.
7. `activity.clear` and the retention window are the person's; About shows
   what is stored and what was dropped.
8. Only attested backends may note events, and only about themselves.
9. Generated output informs a person; it never executes commands, edits
   files, or answers permission prompts.

## 8. Tests

- Router unit tests: journal append/rotate/retention/drop-on-full; the
  ANSI stripper and CR-overwrite resolver on fixtures; redaction; query and
  cursor; `observe` resolution order and manifest gates; note attestation
  and rate limit.
- Both halves e2e (the house rule): open windows, run an agent turn with
  the fake adapter, assert journal rows in `activity.query` AND the router
  log; brief and rollup against the fake OpenAI-compatible provider from
  `session-summary.spec.ts`; automatic mode with a fake local connection;
  resume card after a simulated disconnect gap; `--no-activity` answers
  empty. CI never calls a real provider.
- Race: the writer goroutine and the ring reads under `make test-race`.

## 9. Delivery

1. **Journal + Timeline** (router store, query, tail; router-native and
   agentd/priv/bulk notes; the Timeline view). No AI. Useful on day one.
2. **Observe + briefs on request** (`observe` verb with tail and blob;
   manifest field; Session Summary re-based on it; `ActivityBrief` in
   `pkg/inference/activity`).
3. **Commander service** (schedule, consent switches, rollups written back,
   Resume card; on-box default).
4. **Term refinement** (shell integration, `term.cmd.*` events, term export).
5. **Agent and edit exports**; MC polish; remote-host labelling.

## 10. Open decisions

- Retention default (proposed 30 days, 64 MiB).
- Whether `term.cmd.*` lines are journaled by default once shell
  integration exists (proposed: yes, redacted, with a per-host switch).
- The automatic schedule and budget defaults.
