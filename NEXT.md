# Resume prompt — wash remote apps (R2)

Paste the block below into a fresh session (run from `/home/mick/wash`, the
worktree is `branches/wash-remote`). Memory (`wash_remote_plan.md`), the design
doc (`docs/REMOTE.md`), and the plan file
(`~/.claude/plans/joyful-wibbling-duckling.md`) have the full context.

---

You're continuing the **remote-apps (R2)** feature on the worktree
`branches/wash-remote` (off `main`; main is clean). Run all `make` from the
worktree root. Read memory `wash_remote_plan.md` + `docs/REMOTE.md` first.

**State (17 commits, working tree clean; all build+unit+component green, full
e2e not yet run):**
- **M1 done + proven** — multi-homed shell (RouterClient, per-origin window
  store, tag-mangle, host stripe). `e2e/tests/remote-apps.spec.ts` green.
- **M2a done** — `com.wash.remote` background supervisor (`apps/remote/be`):
  BatchMode `ssh -L`, brings up B's `wash-router --allow-cross-origin
  --no-session --no-auth`, publishes `{status, local_endpoint, code}`.
- **M3 done — `wash-connect` (`com.wash.connect`) window app shipped:**
  - Plumbing: `window.wash.catalogFor/onRemoteCatalog`, `shell.launch` ctrl
    verb + `launchOn`, `attachRemote/detachRemote` + `wm.dropOrigin`.
  - wash-connect `apps/connect/{be,fe}`: BE relays to the supervisor; FE does
    host input → per-host colour card → attach → app list → launch.
    `e2e/tests/connect-launch.spec.ts` proves attach→catalog→launch→detach.
  - Interactive SSH auth (mechanism a): supervisor reports auth refusal as
    down+`code:"auth"`; wash-connect spawns `ssh-add` in a pty (`internal/pty`)
    rendered in a `@wash/ui` Terminal overlay; on close it retries connect.
  - Bookmarks: disk-persisted (`$XDG_CONFIG_HOME/wash/connect.json`); chip →
    connect (+ auto-launch a bookmarked app); ☆ to add.
  - Sidebar: `RemoteWidget` glanceable open-sessions list (fed by the session
    BE's existing `remote.state` forwarder), Manage → opens wash-connect.

**NEXT TASKS (in order):**
1. **M2e — persist B's router across an SSH drop + reconnect.** The supervisor
   today ties B's router lifetime to the `ssh` process (`run()` in
   `apps/remote/be/supervisor.go`); a blip kills B's apps. Start B's router
   detached (`systemd-run --user` or a tiny supervisor) so an SSH drop is a
   transport blip: re-dial with backoff, report `reconnecting`, windows freeze
   then thaw (docs/REMOTE.md §2/§9). The FE freeze-on-blip is part of this.
2. **Capstone two-VM Playwright e2e.** VM-A = desktop (browser via the wash-vm
   proxy, reuse `e2e/fixtures/vm.ts`), VM-B = sshd + wash. A brings up B over
   *real* ssh via com.wash.remote; assert a striped B window composites and that
   the ssh-add widget unlocks a passphrased key. Needs VM images with sshd+wash.
3. **M4/M5** — multi-host notify/bulk/priv merge + priv host attribution;
   clipboard sync hub; remote **raw-channel** apps (term/file-stream/video are
   still local-keyed — see the gap note in `wash_remote_plan.md`); cross-origin
   **z-band** (focused-host windows on top, kept below chrome z 9999/10000).
4. **M6** — hardening (multi-tenancy, provenance/priv-phishing review,
   reconnect-audit alignment, B router teardown/linger policy).

Then **full e2e** → merge to main (ask local-up-a-level vs remote, clean up).

**The full ssh-auth + bookmark-add loops need a real ssh-agent/key/host**, so
they're manual two-VM verification; the BE classification + bookmark disk
round-trip + the widget renderers are unit/component-tested.

**Discipline:** worktree workflow; commit on build+unit green; run the FULL e2e
before any push. FE checks: `make web-shell`, `make fe-unit`, `make component`.
e2e: `cd e2e && pnpm install --ignore-workspace` then `pnpm exec playwright test
<spec>` (needs the out/ tree built — `make TEST_APP=1 out/wash-<app>`).

**Manual two-VM test that works TODAY (no wash-connect needed):**
```
# VM-B:
wash-router --listen 127.0.0.1:11000 --allow-cross-origin --no-session --no-auth
# from A's host:
ssh -L 11001:127.0.0.1:11000 user@vmB
# browser on A's desktop:  ?peer=vmB@ws://127.0.0.1:11001/ws
# on VM-B:
wash-launch --app com.wash.about      # → striped window composites into A
```
With wash-connect: launch it, type the host, Connect, pick an app to launch.
