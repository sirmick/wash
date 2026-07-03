# Implementation prompt: wash-fm/wash-edit access-denied status + "root" relaunch (issue #6)

Two independent parts. **Part A** (surface access-denied in the status bar) is small and safe —
do it first, it can ship on its own. **Part B** (relaunch as root) reuses an existing privilege
mechanism (wash-priv) — it's UI wiring plus one **confinement design decision you must confirm
with the user** before building.

## First action

```
git -C /home/mick/wash worktree add branches/fix-fm-root -b fix-fm-root
cd /home/mick/wash/branches/fix-fm-root
pnpm -C e2e install --ignore-workspace   # if you add e2e coverage
```

## Ground rules

1. One worktree branch under `branches/`; merge back to local `main` when green; then remove it.
2. Green gate: `make build` + `make unit-test` green per commit; `make test-race` +
   `make e2e-test` before merge. Push only if the user asks.
3. One commit per logical change, `fix(fm)/fix(edit)/feat(...)` matching the log.
4. Reuse `@wash/ui` and `@wash/fs-client`; go through @wash/ui tokens for any chrome.
5. **Do not add the router `CapPrepareSpawn` capability to fm/edit** (Part B security invariant —
   see below). fm/edit only *request* a root spawn via wash-priv; they never fork root
   themselves.

---

## Part A — surface access-denied errors in the status bar

Permission-denied is **already propagated** to both FEs; this is purely an FE-surfacing gap.

- The BE maps `os.ErrPermission` (EACCES) → code `"denied"` in `internal/fs/fs.go:251-258`
  (`ErrCode()`), wrapped into `sdk.Err{Code,Msg}` and sent as `<op>_err` (fm via `fsErr()` at
  `apps/fm/be/app.go:574`; edit inline at `apps/edit/be/app.go:215,346,357`). So both FEs already
  receive `{kind:"list_err"|"read_err"|"write_err", code:"denied", msg:"…permission denied"}`.

### fm (mostly wired — one gap)
- Status bar is `@wash/ui` `StatusBar` (`web/lib/src/status-bar.tsx`) driven by a
  `statusOverride` signal (`apps/fm/fe/src/main.tsx:288-296`). `list_err` already renders there
  (`main.tsx:511-517`, deliberately swallowing `outside_root` — keep that), as do the mutation
  errors (rename/create/delete/chmod/chown/upload/download, ~`main.tsx:870-1026,1462-1629`).
- **Gap:** `read_err` renders only into the inline preview pane (`main.tsx:525-527`), not the
  status bar. Route a `read_err` with `code:"denied"` (a real access denial, vs a decode error)
  to `setStatusOverride('error: …')` as well. Also note the double-click "open" path
  (`conn.OpenPath`, BE `apps/fm/be/app.go:307-313`) is fire-and-forget, so a launch-time denial
  never returns — decide whether to surface open-denials (optional; the directory case is
  already covered by `list_err`).

### edit (the real Part-A gap)
- Status bar exists but only shows tab path / "modified" / "binary"
  (`apps/edit/fe/src/main.tsx:2566-2578`, `data-testid="edit-status"`). There is **no**
  `statusOverride`-style error signal, and the FE surfaces almost no `_err` (only
  `delete_err`/`link_err`). `read_err`/`list_err`/`write_err` are invisible to the user.
- **Do:** mirror fm's `statusOverride` pattern into edit — add an error signal, render it in
  `edit-status`, and route `read/list/write _err` (esp. `code:"denied"`) into it.

### Part A tests
- Unit (FE): given a `read_err`/`write_err` `{code:"denied"}` message, the status bar shows the
  error text (both apps). Follow the existing FE unit-test setup (`--conditions=browser` for
  Solid reactivity).
- e2e (optional but ideal): open a `0000`-perm file/dir in fm and in edit (create it in the test
  fixture), assert the status bar reads the denial. Skip in a `beforeEach` if the fixture can't
  create an unreadable path as the test uid.

---

## Part B — relaunch fm/edit as root (full access)

**The escalation mechanism already exists — do not build a new one.** The SDK one-liner is
`Conn.PrivSpawn(appID, reason)` (`internal/sdk/outbound.go:97`): a fire-and-forget message to
`com.wash.priv` asking it to launch the named wash app as root. The async result returns as
`{kind:"spawned"}` / `{kind:"rejected"}` from `sdk.PrivAppID`. The CLI equivalent already ships:
`wash-sudo --app com.wash.fm` (`cmd/wash-sudo/main.go:48,80,193`).

Pipeline (for context — you wire the request, wash-priv owns the rest): priv handles
`{kind:"spawn"}` → `EnqueueSpawn` (`apps/priv/be/queue.go:481`) → approve → `executeSpawn` →
`requestPrepareSpawn`/`PrepareSpawn` (`queue.go:957-978`) → `runSudo` execs
`sudo -S -k --preserve-env=… -- <binary> <args>` (or the bare binary if priv is already root,
`queue.go:1126-1133`). The root child re-handshakes with the router using the preserved
`WASH_APP_ID/WASH_INSTANCE_ID/WASH_ATTACH_TOKEN` (`apps/priv/be/sudoargv.go:36`,
`queue.go:1147-1157`), so it appears as a normal second fm/edit window running uid 0.

### Do
- Add a "Relaunch as root" affordance (menu item / button) to both FEs → a BE handler → call
  `c.PrivSpawn("com.wash.fm", reason)` / `c.PrivSpawn("com.wash.edit", reason)`. Handle the
  async `{kind:"spawned"|"rejected"}` (match `from.AppID == sdk.PrivAppID`) → surface success /
  rejection in the status bar (reuse Part A's override).

### Confinement — the design decision (confirm with the user BEFORE building)
Every app's FS sandbox is `Session.Root`, shipped by the router from
`internal/router/router.go:521` (`Root: r.cfg.FSRoot`) and applied via `wfs.New(root)`+`Confine`
(`apps/fm/be/app.go:114`, `apps/edit/be/app.go:128`). A root-spawned instance **inherits the same
FSRoot**. In a normal per-user desktop `FSRoot` is empty → root fm gets true full access; but in
a confined (VM/sandbox) deployment the root instance is *still* path-jailed — root lifts uid
EACCES, not the root-jail. The current `PrivSpawn`/`PrepareSpawn` path does **not** provide an
unconfined `Session.Root`. **Ask the user:** should "relaunch as root" guarantee full access even
in confined deployments (→ needs a new unconfined-root spawn option through PrepareSpawn), or is
"lifts uid permissions within the existing root" sufficient? Do not add the unconfined path
speculatively.

### Part B security invariants (do not violate)
1. **`CapPrepareSpawn`** (the fork+exec-as-root primitive) is router-gated at
   `internal/router/app_session.go:806` and declared **only** by wash-priv
   (`apps/priv/be/app.go:65`). fm/edit must **not** declare it — they only send `PrivSpawn`.
2. **Human consent + credential gate live entirely in wash-priv**, not the caller: per-request
   Approve (or a standing `appGrants` grant, `queue.go:107-114`), plus the password/idle gate
   (`skipPassword()`/modal, `queue.go:156-179`). Note `autoApprove()` (`queue.go:179`) bypasses
   the UI **only when wash-priv itself runs as root** (single-tenant VM) — so on an embedded-VM
   desktop a "relaunch as root" fires with no prompt; call this out in the PR.
3. **Audit:** every privileged spawn is logged (`apps/priv/be/audit.go`, `queue.go:1166-1178`)
   with the router-attested sender app-id/instance (not spoofable). Don't weaken this.

### Part B tests
- Unit: fm/edit BE handler emits a well-formed `PrivSpawn` for its own app-id on the FE action;
  FE renders `spawned`/`rejected` into the status bar. If wash-priv has a seam-test for
  `EnqueueSpawn`, extend it to cover an fm/edit-originated request.
- e2e (if priv approval is drivable in the harness): trigger "Relaunch as root", approve, assert
  a second (root) window appears. If sudo/approval can't run in the e2e env, note it and rely on
  the unit coverage.

## Out of scope / stop-and-ask
- Any change to how wash-priv approves/authenticates, or to `CapPrepareSpawn` gating.
- The unconfined-root `Session.Root` override — only if the user asks for guaranteed full access
  in confined deployments; otherwise leave it.
