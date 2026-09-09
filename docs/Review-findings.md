# Review findings — 2026-09-08 sweep: agent, term, edit, fm workflows

Scope: the four daily-driver apps (`com.wash.ai`/`com.wash.agentd`,
`com.wash.term`, `com.wash.edit`, `com.wash.fm`) plus the seams between
them. Method: full read of each app's FE + BE, the as-built docs, and the
e2e roster; every item below carries a file:line. **Verified** means the
code path was traced end to end; **suspicion** means the mechanism is
visible but the symptom was not reproduced. Items
already in `Todo.md` (edit Save-As tab drop, issue #6 access-denied, R3
streamed downloads, WebKit anchoring) are not repeated.

The one-line verdict: the *plumbing* (watch, reconnect, DnD, clipboard,
routing, ACP) is far ahead of the *ordinary editor/file-manager/terminal
verbs* a user reaches for in the first ten minutes. The P0 list is short
and all of it is data loss.

---

## P0 — data loss or silent corruption (verified)

**All seven fixed on the same day**, each with a both-halves e2e:
#1/#2/#5/#6/#7 in `44c0cfeb` (edit; `edit-save-guards.spec.ts`,
`edit-close-guard.spec.ts`), #3 in `fdcbcb29` (bulk + fm;
`fm-paste-self.spec.ts`), #4 in `e2f2c05d` (fs; `mutate_test.go`).

| # | App | Finding | Evidence |
|---|-----|---------|----------|
| 1 | edit | **Ctrl+S on a binary tab truncates the file to 0 bytes.** A binary tab is seeded with `baseline:''`, the CM view is created with `doc: t.baseline`, and `saveActive` has no binary guard, so the write carries `''`. Only the File-menu Save is gated, and only on "no tab". Open any image/tarball from the tree, press Ctrl+S: file destroyed. | `apps/edit/fe/src/main.tsx:431-441, 562-583, 2177-2179, 2326`; `apps/edit/be/app.go` write handler |
| 2 | edit | **Files over 4 MiB open truncated and save back the truncated prefix.** BE sets `Truncated: true`; the FE never reads it (no `truncated` hit in main.tsx); baseline is the first 4 MiB; the write is under the cap so it succeeds. | `apps/edit/be/app.go:44-47, 378-389` |
| 3 | fm | **Paste into the same folder, or into the pasted folder's own subtree, destroys the source.** `planPaste`/`pasteFilesClipboard` enqueue bulk with no same-parent or descendant filter (the DnD paths have both); bulkops has none either. Copy → paste here → conflict → Replace: `os.RemoveAll(dst)` where dst==src. Cut variant: same, then rename ENOENT. Copy a folder and paste inside it: `copyTree` recurses into its own output until ENAMETOOLONG. | `apps/fm/fe/src/main.tsx:1761-1774` vs `:1439-1446, 1786-1791`; `apps/fm/fe/src/clipboard.ts:42-50`; `internal/bulkops/bulkops.go:565-621, 806-836`; `apps/bulk/be/app.go:170` |
| 4 | edit + fm | **Atomic save clobbers file identity.** tmp created 0644, renamed over the target: executable bit lost, owner/group become the editor's uid, **a symlink is replaced by a regular file at the link path** (the real target is orphaned), hard links split. Shared helper, so both apps. | `internal/fs/mutate.go:31-68` |
| 5 | edit | **No unsaved-changes guard anywhere.** `closeTab` is documented as silent; Ctrl+W and the tab × go straight to it; `AppDef` sets no `OnCloseRequested`, so the WM close button / `wash kill` / logout tear down the window and the router drops the persisted buffer with it. term has the reference pattern. | `apps/edit/fe/src/main.tsx:513-516, 2200`; `apps/edit/be/app.go:76-100`; cf. `apps/term/be/app.go:160, 509` |
| 6 | edit | **Failed saves are invisible.** `saveActive` acts only on `write_ok`; `pickerConfirm` returns on anything else; the 5 s `timeout_err` is dropped the same way. EACCES, read-only file, ENOSPC, the 4 MiB cap, a detached BE: dirty dot stays, no toast, no status text. `read_err` on open is equally silent (double-clicking an unreadable file does nothing). | `apps/edit/fe/src/main.tsx:430, 581-596, 642`; `web/…/bus.ts:52-58` |
| 7 | edit | **CRLF files are silently converted to LF and never go clean.** CM splits on `\r\n` and joins with `\n`; `baseline` keeps the raw bytes, so `text !== baseline` from the first keystroke, undo cannot reach clean, and every save rewrites LF. Non-UTF-8 files are the same shape: `string(buf)` + JSON replaces invalid bytes with U+FFFD and the save writes the replacement bytes back. | `apps/edit/fe/src/main.tsx:433, 1793`; `apps/edit/be/app.go:384`; `pkg/wire/msgs_event.go:926` |

## P1 — workflows that break mid-use

**All fixed 2026-09-09**, four parallel tracks merged as `b80313a4` (edit +
shell), `770e5248` (fm), `652c1e6a` (term) and `7557bd21` (agent), every
item with a both-halves e2e. Along the way these P2 entries also landed:
edit tab-switch scroll restore, the picker Ctrl+W guard, Show diff on the
reload prompt, drop-to-open on the editor body, Reveal in Files at the
folder, tabs following renames; term COLORTERM, Alt tab bindings, exit-code
hold, smarter tab labels; fm Backspace-up, truncation notice, cross-device
drag fallback, failed jobs kept on screen; agent composer file drop, Bash
"always" rules scoped to the project, mid-turn prompts queued. The
Chromium-reserved-shortcut suspicion stands unverified; the Alt bindings
sidestep it either way.

### agent
- **Transcript freezes after 60 s for any viewer that never called `agent_started`/`attach`.** agentd expires a watcher not re-affirmed within `watcherTTL`; `keepWatching` is started only on those two paths in the Agent app, and the shared `internal/agentclient` (used by wash-edit's agent tabs) subscribes once and never re-affirms. Open the Agent app from the start menu, click a running row: the status line keeps moving (roster sub is separate) while the transcript stops at the first event after 60 s. Edit agent tabs: same. e2e only observes within 20 s. *Verified.* `apps/agentd/be/transcript.go:44-45, 455-470`; `apps/ai/be/app.go:370-391, 557, 592, 676-692`; `internal/agentclient/agentclient.go:103, 112`. (Also: each `agent_started`/`attach` starts another `keepWatching` goroutine on the same conn — leak, harmless.)
- **No adapter-exit watcher.** Nothing selects on `client.Done()` outside acpterm; a crashed adapter keeps its roster row and idle-hold, and the next prompt fails silently (below). Pending `RequestPermission` uses `context.Background()`, so the "turn cancelled" arm at `acp.go:540` is dead code and an ask outlives both the adapter and `session/cancel`. *Verified by grep + read.* `apps/agentd/be/acp.go:540`; `internal/acp/conn.go:307`.
- **Prompt/turn errors are swallowed.** `promptHosted` logs and sets `failed` but writes nothing into the transcript; expired auth, rate limit or a dead adapter shows only a red dot. No retry/restart affordance. `apps/agentd/be/adapters.go:271-275, 693-699`.
- **End session leaves agent-spawned terminals running and asks pending.** `retire()` calls `stop` (kills only the adapter process, no process group), never closes `termAll` ptys for the key, and never deletes the row's `asks`, so the rail keeps "claude wants to run…" for a dead session, the attention badge counts it, and "Always allow" still writes a rule. *Verified.* `apps/agentd/be/acp.go:196-223`; `adapters.go:213-221`; `acpterm.go:63-71, 117`; `ask.go:241, 373` are the only deletes.
- **History rows older than the in-memory 100 are inert.** Panel lists every `.jsonl` on disk; `resumeSession` looks up the capped slice and logs "resume unknown session" with no toast. After a failed resume `forgetSession` removes the entry but not the file, so the row stays and never works. `apps/agentd/be/history.go:37, 230-241, 255`; `transcript_store.go:768-790`.
- **Concurrent prompt mid-turn.** Composer never disables; `agent_prompt` spawns a second `promptHosted` unconditionally and `beginTurn/endTurn` flip out of order. `web/lib/src/agent-session.tsx:615`; `apps/agentd/be/acp.go:124-140, 796-804`.
- **"Allow always" for Bash leaks across projects.** `SuggestRule` scopes only Write/Edit by cwd; `Append` writes `Cwd:""`. `internal/agentpolicy/agentpolicy.go:122, 184-187`.
- Transcript files grow quadratically (every chunk appends the whole accumulated message; `transcript.go:259-268`); no prune/delete UI; 30 s soft ask TTL still expires while you read another window; a turn blocked on an ask can only be Denied, not stopped (`agent-session.tsx:539`, `agent-roster.tsx:520`); the 4th parallel tool call is auto-cancelled (`ask.go:84`); elicitation always declined (`acp.go:993-997`).

### term
- **The agent-status UI in term is dead code.** The intercept tier was deleted 2026-08-04 (eadeb2b3, AGENT_APP.md §10), but the FE `agent_status` handler, tab dots and ticker remain with no producer; `pty.ForegroundUser.Agent` is declared, never assigned. Consequence worth stating plainly: **running `claude` inside a wash terminal is no longer agent-aware at all** — no roster row, no desktop asks. Only Agent-app sessions are. *Verified.* `apps/term/fe/src/main.tsx:99, 735-802, 903-925`; `internal/pty/pty.go:174, 201-231`; README.md:307 and Todo's "term does not answer `wash.focus`" / "cannot express `stale`" describe the deleted tier.
- **Ctrl+Shift+T / Ctrl+Shift+W / Ctrl+Tab are browser-reserved in Chromium** (Linux/Windows) and cannot be intercepted from a page; the handlers exist but should never fire in a normal tab. e2e passes because CDP-injected keys bypass the reservation. *Suspicion — confirm in a real browser.* `apps/term/fe/src/main.tsx:1083-1100`. Split menu also labels Ctrl+Shift+W "Close Pane" while the key closes a tab (`:1413`).
- `splitIntents` leaks on `tab_error`: the next plain New Tab lands as a split; `reconcile()` and agentd `exec_tab` also consume whatever intent is queued. `apps/term/fe/src/main.tsx:355, 506, 882-886, 977`; `apps/term/be/app.go:357`.
- Every exit closes the tab instantly and the last one closes the window; exit code is recorded but never shown, so a `--exec`/`wash-sudo --window` command that fails fast vanishes. `apps/term/be/app.go:459-482`; `internal/pty/pty.go:623-635`.
- Tab labels head-truncate at 12 chars, so bash's default `user@host: /path` makes every tab read `mick@ai: ~/…`. `apps/term/fe/src/main.tsx:185, 708-711`.
- Paste overlay counts a trailing `\n`, so a Windows-copied one-liner (`ls\r\n`) triggers the multi-line ask. `web/lib/src/paste-analyze.ts:202, 230-233`.
- New tab spawns at 80×24 then resizes (first prompt at the wrong width). `apps/term/fe/src/main.tsx:485`; `app.go:318-329`.

### fm
- **Reload re-lists the parent of the folder you are looking at.** After navigating into a folder `path()` *is* that folder, and the button calls `invalidateAndList(parentPath(path()))`. *Verified.* `apps/fm/fe/src/main.tsx:2197, 695`.
- **Selection goes stale after rename, single delete and single drag-move.** `applySelection` is never written by `commitRename`, the `delete_ok` branch or `commitMove`; the next F2/Delete/Ctrl+C acts on the old path and errors `not_found`. This is the mechanism the "ghost selection" invariant has been logging. `apps/fm/fe/src/main.tsx:866-895, 985-1000, 1818-1829` vs the nine `applySelection` writers.
- **Every watch/refresh tears the subtree down and remounts it.** `invalidateAndList` deletes the listing before re-requesting; `flattenTree` stops at a missing listing, so rows unmount and come back as new DOM: flicker, scroll jump, clicks racing the rebuild, spurious ghost logs, and re-rooting if `/` is the one refreshed. *Mechanism verified, severity a suspicion.* `:823-826, 1889-1900`.
- **Listing truncation is silent** (5,000-entry cap; FE ignores `truncated`). `apps/fm/be/app.go:~48`; `internal/fs/fs.go:155-158`; FE `:506-509`.
- **Backspace = delete** (every other FM: up). Confirm dialog is the only backstop. `:2065`.
- Cross-device single-file drag fails `cross_device` with no copy+delete fallback (bites FUSE mounts); only bulk degrades. `internal/fs/mutate.go:176-184`; `bulkops.go:648-660`. Failed bulk jobs vanish from the strip (`:308-310`). `list_err` on a path-bar typo still commits the bad path into history (`:695`).

### cross-app
- **Dropping a file from fm onto the editor MOVES it** into the editor's project dir (tree drop → `commitMove`); dropping on the code area does nothing. `apps/edit/fe/src/main.tsx:1361-1400`. *Suspicion:* since fm's dragstart also sets `text/plain`, CM's default drop inserts the path as text.
- **Agent composer promises "drop a file from wash-fm…" but has no drop/paste handler** anywhere in the Agent app or `agent-session.tsx`. `web/lib/src/agent-session.tsx:616`.
- **OS file dropped on the wallpaper navigates the browser tab away** — no `dragover`/`drop`/`beforeunload` in the shell. *Suspicion.* `web/shell/src/main.tsx`, `desktop.tsx` (grep count 0).
- **Edit's "Open in fm" passes no path**; fm opens at its default root, and fm has no `--open`/`--root` argv. `apps/edit/fe/src/main.tsx:1468-1470`; `apps/edit/be/app.go:260-268`.
- Edit rename/move of an open file (sidebar, DnD, external `mv`) leaves the tab on the old path; next Ctrl+S recreates it. `apps/edit/fe/src/main.tsx:1413, 1457, 1688`.

## P2 — missing everyday workflows

**fm**: keyboard row navigation (no Arrow/Home/End/PgUp/Space/type-ahead; `onKey` handles only F2/Del/Enter/Esc/Ctrl combos, `:2053-2140`); Enter on a file previews rather than opens; undo for move/rename/paste (trash itself is a won't-do, decided 2026-09-08); search/filter/recursive find; Open-with chooser and any handler for archives/audio/video/pdf (only edit and imageview register `Opens`); **Open terminal here**; Duplicate / "keep both" on conflict; archive create/extract (zip download drops symlinks and empty dirs, `download.go:218-250`); bookmarks/places/drives, breadcrumb, tabs/dual pane; cut items not dimmed; drag-out to OS; sort-by-type sorts `dir/file/symlink` not kind; tree unvirtualised (5k rows of DOM); Ctrl+A ignores the grid; no Ctrl+L/H/N.

**edit**: Save All / Close All / Revert; Ctrl+Tab cycling, tab drag-reorder, middle-click close; recent files / Ctrl+P quick-open; go-to-line only as CM's Ctrl+Alt+G; indentation fixed at 2 spaces (Tab in Go inserts spaces), no detect/width/tabs option, no trailing-whitespace/final-newline; status bar has no Ln/Col, language, indent or EOL; font zoom; split view; read-only awareness (mode bits never checked); rendered markdown preview (only WYSIWYG-or-source); drag-drop onto the editor body to open; "send selection to agent"; word-wrap and language override not persisted; tab switch loses scroll (`captureActiveState` records it, the active-tab effect never restores it, `:1109, 2308-2340`); reload prompt shows only a name, no diff, and Keep→Save is a one-click clobber of the external change (`:1743, 2848`); Ctrl+H unbound on source tabs (browser History); Ctrl+W fires while the FilePicker is focused (`:2174-2203`); WYSIWYG normalises the whole file on any edit and its dirty flag is a latch (`wysiwyg.ts:8-12, 339-345`).

**term**: find in scrollback (no search addon); clickable plain-text URLs and file paths (no web-links; OSC 8 http only); **cwd**: no OSC 7 or `/proc/<pid>/cwd`, new tab never inherits cwd, `execTabReq.Cwd` parsed and ignored (`app.go:289, 357`), no `--cwd`; unicode11 (emoji/CJK widths wrong under Claude Code/starship prompts); bell/activity indicators; preferences are per-window (font/size/theme/smart-paste in the instance blob, so every new window resets), no Ctrl+±/wheel zoom; manual tab rename; scrollback size / save-to-file; file drop from fm (fm sets newline-joined unquoted `text/plain`, term has no drop handler); sixel/kitty images; OSC 52; cursor style; `macOptionIsMeta`; **COLORTERM never set**; profiles/custom command; window title never reflects the active tab (`window.set_title` exists, unused); restart-hung-shell verb; DOM renderer only.

**agent**: rename a session; delete/prune history; copy a single code block; Esc-to-cancel, Ctrl+Enter, ↑ prompt history; syntax highlighting in fences, mermaid/LaTeX; attach files/images or paste an image; @-mention; **diffs the agent made are not viewable** (`ContentBlock` drops `diff`, tool rows are one-liners, and the Agent app passes no `onOpenTool` so rows are inert; only edit's agent tab opens the file — `internal/acp/types.go:244-250, 324-333`; `apps/ai/fe/src/main.tsx:921-930`); per-session cost; custom adapter command/args/env, MCP servers (always `[]`, `adapters.go:565`); fs/terminal confined to the session cwd with no override (monorepo sibling dirs unreadable, `acpfs.go:358`); fork; "open in terminal" / "send to agent" in either direction; desktop-operating intents (`docs/AGENT.md`, `CONTROL_BUS.md`) are design-only; no e2e for adapter crash/kill/auth failure.

**cross-app**: `wash open <path>`, `xdg-open`, `$EDITOR`, `$BROWSER` from a wash terminal (only `wash-edit --open <abs>` works, undocumented; URLs have no browser routing); "reveal in fm" / "terminal at file's dir" from edit; term → "open cwd in fm"; second fm double-click spawns a second edit window (edit is `InstancingMulti`; retargeting declared out of scope in IMAGES.md); `Instancing:single` via `EvtSpawnRequest` still duplicates (already in Todo); drag text out of term/edit into fm; terminal bell → attention (`EvtWindowAttention` used only by wash-ai); window state does not survive a router restart (persist is router-memory only, `app_session.go:540-543`); start menu has no recent files/pinned; no Alt+Tab; cross-host open-with/DnD/clipboard are documented non-goals.

## Stale docs and backlog entries found on the way

- `Todo.md` "wash-term split panes … not started": M1 + M2 shipped (`3134e55e`, `term-split.spec.ts`, 14 tests).
- `Todo.md` "wash-term does not answer `wash.focus`" and "wash-term cannot express `stale`": describe the deleted intercept tier.
- `README.md:307` "watches wash terminals for agent CLIs": no longer true.
- Issue #21: the rail now has End and Detach (`agent-roster-verbs.spec.ts`); only fork remains, deliberately.
- Issue #22: still open pending a re-sighting on a build with 21f98e97.

## Verified OK (spot checks, so they are not re-audited next time)

fm/edit watch-driven refresh and the own-save suppression; edit reload prompt when dirty (10 e2e cases); clipboard hub with system-preferring paste in term and edit; open-routing fm → edit/imageview with the no-handler fallback; agent answer routing by global ask id; `claimDetached` atomic on reattach; transcript index reseed on resume; stale-session frame guard; scroll pinning only when at bottom; failed sessions render red; the wire-side quadratic transcript emit is fixed (`transcript_emit.go`); fm DnD self/descendant guards on the drag paths; term reattach/reconnect/wedge recovery (e2e'd).
