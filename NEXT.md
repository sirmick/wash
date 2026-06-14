# Resume prompt — wash remote apps (R2)

Paste the block below into a fresh session (run from `/home/mick/wash`, the
worktree is `branches/wash-remote`). Memory (`wash_remote_plan.md`), the design
doc (`docs/REMOTE.md`), and the plan file
(`~/.claude/plans/joyful-wibbling-duckling.md`) have the full context.

---

You're continuing the **remote-apps (R2)** feature on the worktree
`branches/wash-remote` (off `main`; main is clean). Run all `make` from the
worktree root. Read memory `wash_remote_plan.md` + `docs/REMOTE.md` first.

**State (11 commits, working tree clean, all green except full-e2e not yet run):**
- **M1 done + proven** — multi-homed shell: RouterClient, per-origin window-store
  merge, per-origin tag-mangle, per-host colour stripe, real 2nd client.
  `e2e/tests/remote-apps.spec.ts` (two routers) passes: an app launched on B
  composites into A's desktop as a host-striped window.
- **M2a done** — `com.wash.remote` background service (`apps/remote/be`): SSHes
  out, brings up B's router (`--allow-cross-origin --no-session --no-auth`),
  forwards it locally, publishes `{status, local_endpoint}` via StateService.
- Router gained `--allow-cross-origin`. Session BE gateway (`fda1d8b`) is now
  UNUSED after the pivot below (leave harmless or revert).

**Decision/pivot:** the connect UI is a dedicated window app **`wash-connect`
(`com.wash.connect`)**, NOT a sidebar widget. It fronts the `com.wash.remote`
background supervisor. (docs/REMOTE.md §6.1.)

**NEXT TASK — build `wash-connect` (M3):**
1. New app `apps/connect/{be,fe}` (`com.wash.connect`, surface=window,
   singleton). BE subscribes cross-app to `com.wash.remote`, relays
   state/connect/disconnect to its FE. FE: host input → Connect → per-host
   status (colour-coded) → list of the host's wash apps → click to launch.
2. **Un-guard B's catalog per-origin** — M1f guarded non-local `catalog` off in
   `makeHandlers` (web/shell/src/main.tsx); store it per-origin and expose to the
   FE so wash-connect can list B's apps.
3. **`ShellLaunch{app_id}` shell→router ctrl verb** (B runs `--no-session`, so no
   session BE to route a launch through; the router already spawns via its
   control socket) + `window.wash.launchOn(origin, appID)`.
4. **`window.wash.attachRemote(origin,url)` / `detachRemote(origin)`** +
   `wm.dropOrigin(origin)` so the FE attaches the endpoint the supervisor reports
   and drops a host's windows on disconnect.
Then **M2e** (detach/persist B's router across an SSH drop + reconnect), and the
**capstone two-VM Playwright e2e** (one VM via the wash-vm proxy; reuse
`e2e/fixtures/vm.ts`).

**Confirm one fork first:** keep `com.wash.remote` background so remote sessions
persist when you close `wash-connect` (recommended) — vs fold the SSH
supervision into `wash-connect`'s BE (simpler, but closing the window drops all
remote sessions).

**Discipline:** worktree workflow (merge back to main when done, ask
local-up-a-level vs remote, clean up). Commit on build+unit green; run the FULL
e2e before any push. FE checks: `make web-shell`, `make fe-unit`, `make
component` (from worktree root). For e2e: `cd e2e && pnpm install
--ignore-workspace` then `pnpm exec playwright test <spec>` (e2e isn't a
workspace member; needs `wash-test` built via `make TEST_APP=1 out/wash-test`).

**Manual two-VM test that works TODAY:**
```
# VM-B:
wash-router --listen 127.0.0.1:11000 --allow-cross-origin --no-session --no-auth
# from A's host:
ssh -L 11001:127.0.0.1:11000 user@vmB
# browser on A's desktop:  ?peer=vmB@ws://127.0.0.1:11001/ws
# on VM-B:
wash-launch --app com.wash.about      # → striped window composites into A
```
