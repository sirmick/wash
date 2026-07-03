# Implementation prompt: the 2026-07-01 review LEFTOVERS (follow-up pass)

You are implementing the lower-priority items deferred by the first fix pass
(REVIEW-FIX-PROMPT.md, now COMPLETE). The confirmed high/medium correctness
bugs are already fixed and merged; this pass mops up the remainder.

**Source of truth** — read the relevant section BEFORE each fix:
- `REVIEW-DATAPATH.md` — findings F9–F11 (+ F5's deferred half). Its
  "Resolution status" table shows what's already done.
- `REVIEW-RECONNECT.md` — M5–M7, L2–L3, the "smaller notes" list.
- `REVIEW-X11-WAYLAND.md` — finding 13's remainder, finding 5 (open), and the
  "Protocol-coverage gaps" section.
- `TODO.md` §"2026-07-01 correctness-review leftovers" — the same items as a
  short backlog.

Line numbers in the review docs are PRE-fix-pass and have drifted (compositor.cpp
especially — grep the named symbol). Work through the phases **in order**; do not
attempt fixes not listed here without asking the user first.

## Ground rules (unchanged from the first pass — do not skip)

1. **One worktree branch per phase** under `branches/` (e.g.
   `git worktree add branches/fix-hardening-e -b fix-hardening-e`); do ALL work
   for a phase inside it; merge back to local `main` when the phase is fully
   green, then `git worktree remove` it before the next phase. `e2e/` is not a
   pnpm workspace member — in a fresh worktree run `pnpm install --ignore-workspace`
   inside `e2e/`. Some suites also need staged FE assets in a fresh worktree
   (`make apps/<app>/be/assets/.stamp`, `make internal/shellassets/assets/.stamp`)
   and `make wash-display` for the compositor.
2. **Green gate**: commit only on build + unit green; before merging a phase run
   the FULL suite incl. `make test-race` and `make e2e-test` (NOT raw playwright).
   Never pipe a long build/test through `| tail`/`| head` — SIGPIPE kills it and
   masks the real exit code (bit the first pass). Push only if the user asks.
3. **Each numbered fix = one commit**, message style `fix(<component>): <what>`.
4. **Do not refactor** beyond what a fix requires; respect the invariants listed
   in each review's "What looks solid" section.
5. **Every fix gets a test** (unit or e2e). If a test is impractical, say so in
   the commit message rather than silently skipping. The wash-display e2e is
   flaky in the FULL suite (concurrent compositors contend) but passes in
   isolation — verify a display failure in isolation before treating it as real;
   kill orphan compositors and rebuild `out/wash-display` first.
6. If a fix turns out larger/riskier than described, **stop and ask the user**.
7. CBOR pitfall: never introduce `json.RawMessage` / `[]byte` in structured
   BE→FE fields (the router base64-encodes byte strings).

---

## Phase E — data-plane hardening (branch `fix-hardening-e`)

Low-risk, high-confidence correctness. All latent today (no live producer hits
them), so they're safety nets, not user-visible bugs.

### E1. ReadLoop reader-goroutine leak at teardown (REVIEW-DATAPATH F9)
- `internal/wire/readloop.go`: the reader goroutine sends into a 1-buffered
  channel (`ch <- rr{f, err}`). If `ReadLoop` returns while a result is already
  buffered, the goroutine's next send blocks forever — one leaked goroutine +
  one pinned frame per affected session teardown, under EVERY app/shell loop.
- Fix: `select` the send against a `done` channel closed by `ReadLoop` on return.
- **Test**: unit — start a ReadLoop, have the handler return an error while a
  second frame is already read/buffered, assert the reader goroutine exits
  (e.g. a `sync.WaitGroup` / goroutine-count check within a deadline).

### E2. Max-size frame kills the peer relay (REVIEW-DATAPATH F10)
- `internal/router/peer.go` `pumpPeerToShell` + `web/shell/src/relay-socket.ts`
  `send`: the relay wraps B's whole frame (8-byte header + payload) as ONE
  A-frame payload. A legal 16 MiB B-frame → 16 MiB + 8 > `MaxPayload`
  (`internal/wire/frame.go:36`) → `EncodeFrame` fails → the pump breaks and the
  relay channel tears down. Latent (all producers chunk ≤256 KiB) but bites the
  first near-cap stream over the relay.
- Fix (pick one, state which in the commit): split an oversized relay payload
  across multiple raw frames (B's deframer reassembles), OR cap B-side producers
  at `MaxPayload − 8`.
- **Test**: unit — feed the pump a max-size B-frame; assert the relay survives
  (channel not torn down; bytes delivered intact).

### E3. F11 minor notes (REVIEW-DATAPATH F11) — one commit, do the safe subset
- `internal/wire/frame.go` `DecodeFrameRaw` not covered by `FuzzDecodeFrame`:
  add it to the fuzz corpus/target (its validation hand-duplicates `DecodeFrame`
  and can silently desync).
- `internal/router/qos.go` `Scheduler.TrySubmit`/`SubmitTelemetry` enqueue after
  `Close()` (no closed-check on the fast path): add the check so a post-Close
  submit is a no-op, not a frame stranded in an undrained queue.
- `internal/router/credit.go` `Reserve` single-waiter wakeup: add the explanatory
  comment the review asked for (all channels are single-producer today — a code
  comment, not a behavior change).
- **Test**: extend `frame_test.go` fuzz + a `qos_test.go` case for submit-after-close.

Phase E merge gate: full unit + race + e2e green, then merge.

---

## Phase F — router/reconnect lifecycle edges (branch `fix-lifecycle-f`)

### F1. Post-confirm window close has no kill escalation (REVIEW-RECONNECT M7)
- `internal/router/shell_session.go` `handleWindowCloseClicked` (grep the fn):
  on a confirmed close the window is destroyed immediately then only SIGTERM is
  sent — no grace→Kill ladder (unlike `restartBackgroundApp`). An app that
  confirms then hangs in shutdown stays in `r.apps` with its window gone →
  `launchOrRaise` "raises" a destroyed window, and `requestClose`'s in-progress
  guard blocks a retry → unopenable until router restart.
- Fix: arm a grace timer that escalates to `Process.Kill()` (copy the
  `restartBackgroundApp` ladder).
- **Test**: unit — confirm-close an app whose process ignores SIGTERM; assert it
  is SIGKILLed and fully removed from `r.apps` within the grace window.

### F2. Teardown gated on cmd.Wait while a grandchild holds the pipe (REVIEW-RECONNECT L3)
- `internal/router/spawn.go` (stdout is an OS pipe via MultiWriter) +
  `internal/router/router.go` `cmd.Wait()` sites: `Wait` blocks until every
  inheritor of the stdout pipe exits, so a grandchild outliving a killed app
  delays tearDown indefinitely (window lingers, singleton slot pinned).
- Fix: set `cmd.WaitDelay` (Go 1.20+) so Wait returns once the process exits
  even if a grandchild holds the pipe, OR don't gate tearDown on Wait.
- **Test**: unit — a fake spawned cmd whose grandchild keeps the pipe open;
  assert tearDown completes within a bounded time.

### F3. Deferred mountWhenReady resurrects a deleted window as a ghost (REVIEW-RECONNECT M5)
- `web/shell/src/wm.ts` (`applySessionSnapshot` / `upsertWindow` / bundle-wait
  deferral): a snapshot filters the store synchronously but inserts
  asynchronously (bundle wait up to 10s). A window omitted by a reconnect
  snapshot/delete while its bundle is still in flight isn't filtered (not in the
  store yet); the deferred `upsertWindow` lands it after the delete → unclosable
  ghost until the next reconnect.
- Fix: a per-origin snapshot epoch; drop deferred upserts from a superseded epoch.
- **Test**: `ws.test.ts`/`wm` unit — a deferred upsert from an old epoch is
  dropped after a newer snapshot/delete.

### F4. Self-closed relay socket wedges a remote client forever (REVIEW-RECONNECT M6) — PLAUSIBLE
- `web/shell/src/relay-socket.ts` + `web/shell/src/main.tsx` (B's Conn factory
  `() => sock`): if the relay socket self-closes (oversize/corrupt frame length),
  B's Conn redials into the same now-closed socket whose events never fire →
  `'reconnecting'` forever, B's windows frozen, no banner.
- Fix: treat a non-detach relay-socket close as fatal for the origin
  (`detachClient` + re-attach) instead of retrying an unretryable transport.
- Note: this composes with E2 (which removes the oversize-frame trigger); confirm
  the interaction. **Stop and ask** if the fix needs new message types.
- **Test**: FE unit — a self-closed relay socket detaches + re-attaches the origin.

### F5. Silent input loss on `unauthenticated` + silent head-steal (REVIEW-RECONNECT L2)
- `web/shell/src/ws.ts` (`clearPending()` on the auth-gone path): keystrokes
  queued during the outage vanish without a `lost-input` event (the overflow
  path emits one). Also: any new shell connection steals ALL terminal channels;
  the losing tab goes dark with only a router log line.
- Fix: emit `lost-input` before `clearPending()` on `unauthenticated`; add a
  minimal UI affordance for multi-tab head-steal (a banner "opened elsewhere").
- **Test**: `ws.test.ts` — going `unauthenticated` with queued frames emits
  `lost-input`.

Phase F merge gate: full unit + race + e2e green, then merge.

---

## Phase G — display correctness gaps (branch `fix-display-g`)

C++ in `wash-display/src` (single-threaded wlroots loop; wire I/O off the reader
thread). Manual checks are largely visual — report them to the user.

### G1. Asset streaming off the dispatch loop (REVIEW-DATAPATH F5 deferred half)
- `internal/router/shell_session.go` `handleAssetRead` (see the `TODO(review F5)`
  comment): it streams a whole file through blocking `Submit` calls inline on the
  single shell dispatch loop, so a multi-MB asset on a slow link freezes the
  desktop's input for seconds. (The read-side liveness stamp already prevents the
  false idle-reap; this is the input-stall half.)
- Fix: run the asset stream on its own goroutine (it's already transaction-framed;
  size-completed so class reordering can't truncate it).
- **Test**: unit — a large asset read must not block a concurrent unrelated frame
  on the same shell; the healthy conn stays responsive.

### G2. Keyboard layout hint (REVIEW-X11-WAYLAND #13, keymap half)
- Keymap is hard-coded to the server default (`xkb_keymap_new_from_names` in
  `compositor.cpp`, grep it). The FE sends physical `KeyboardEvent.code`s, so a
  browser user on a non-US host layout gets the SERVER's layout → wrong chars.
- Fix: send a layout hint from the FE (or a `KeyboardEvent.key`-based fallback
  mapping) and compile the guest keymap to match. **Stop and ask** before adding
  a new wire message if one is needed.
- **Test**: manual — from a non-US host layout, typing produces the right chars.

### G3. Leaked selection-reader thread (REVIEW-X11-WAYLAND #13, selection half)
- `handle_set_selection` (`compositor.cpp`, grep it): its reader thread blocks
  forever if the selection owner exits without writing — leaked thread + fd per
  occurrence.
- Fix: bound the read (poll/deadline) and join/close on timeout.
- **Test**: manual/scripted — a guest that claims the selection then exits without
  writing leaves no leaked thread/fd.

### G4. min/max size not consulted (REVIEW-X11-WAYLAND protocol gap 7)
- `toplevel->current.min/max_width/height` is never read; the FE's interactive
  resize clamps only to 100×60 (`wash-app-display.ts`), and the router can
  configure below the client minimum. Self-heals (client clamps + geometry
  re-syncs) but feels rubber-bandy for Qt apps with hard minimums.
- Fix: honour min/max in the resize clamp (BE reports them; FE clamps to them).
- **Test**: manual — a Qt app with a hard minimum resizes smoothly.

### G5. Popover over-match (REVIEW-X11-WAYLAND #5) — INVESTIGATE ONLY, likely leave
- `toplevel_is_popover`: an untitled GTK message dialog is rendered as a
  pointer-grabbing overlay, not a movable window. The first pass tried and
  REVERTED the review's discriminators (no-decoration / no-min-max /
  recent-pointer) — each regresses real Qt menus (Qt sets decoration+min/max, and
  serial-less programmatic menus have no pointer event; the tested Qt menu broke).
  A TITLED dialog already stays a window.
- Task: INVESTIGATE whether any reliable signal separates an untitled menu from an
  untitled dialog (e.g. GTK app_id patterns, surface commit timing, content
  probing). If none is robust against `display-qt-popover.spec.ts`, **leave it**
  and update the TODO note — do NOT re-introduce a heuristic that fails that spec.

Phase G merge gate: builds green, Go unit green, display e2e green (isolation-
verify any flake), plus the manual checks reported.

---

## Phase H — display protocol coverage (branch `fix-display-h`) — FEATURES

These are NET-NEW protocol support, not bug fixes. Each is additive and several
are independently valuable; **confirm scope/priority with the user before
starting each** (this phase is the most likely to be trimmed or split). Ranked by
blast radius (REVIEW-X11-WAYLAND "Protocol-coverage gaps"):

- **H1 wl_data_device drag-and-drop** — `seat.events.request_start_drag` unhandled
  → in-APP DnD is dead (GTK text drag, Qt file-view drag, Chromium tab gesture).
  Highest blast radius. Wire `wlr_seat_start_pointer_drag` + the FE drag surface.
- **H2 primary selection** — advertise
  `zwp_primary_selection_device_manager_v1` + seat `request_set_primary_selection`
  → middle-click paste in native-Wayland terminals; bridge X↔Wayland primary.
- **H3 wp_viewporter** — `wlr_viewporter_create` (cheap; SDL2/mpv fractional paths
  degrade gracefully without it).
- **H4 presentation-time** — improves mpv A/V sync (Chromium/Firefox already fall
  back to frame callbacks fine).
- **H5 xdg-activation** — "focus me" / URL-handoff requests are silently dropped.
- **H6 Xwayland `-auth`** — wlroots starts Xwayland with no access control; any
  local uid can connect to `:N`. Security-relevant on the multi-user estate —
  pair with the D4 per-user runtime-dir work. Treat as SECURITY, prioritise.
- **H7 request_minimize / set_app_id / icons** — CSD minimize buttons do nothing;
  wash windows can't show per-app icons. Also pass the xdg parent to
  `create_window` so titled dialogs stack above their parent.

Each H item: its own commit, a manual verification with a real toolkit app, and a
note in the commit if the check can't run headlessly. **H6 (Xwayland -auth) is the
one to do first** — it's a real multi-user security gap, not a nicety.

---

## Explicitly OUT of scope (do not do without being asked)

- Any VP9/WebRTC transport work (a separate, larger effort).
- Touch input (the FE only synthesizes pointer events).
- The "smaller notes" in REVIEW-RECONNECT beyond L2/L3/M5–M7 (login first-spawn
  flock re-List, bundle re-ship on reconnect, devreload escalation, etc.) —
  cosmetic/low-value; leave in TODO.md unless a user hits one.

When phases are merged and green, prune the corresponding lines from TODO.md's
"correctness-review leftovers" section and flip the matching rows in each review
doc's Resolution status table to Fixed (with the commit), so the record stays
accurate — exactly as the first pass did.
