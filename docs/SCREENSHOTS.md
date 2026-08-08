# Screenshots

The marketing shots in `docs/screenshots/` (and the README hero montage)
are **generated, not hand-shot**. `make screenshots` boots a throwaway
router, poses real app windows, and captures them with Playwright. The
source is `e2e/capture/screenshots.cap.ts`.

```bash
make screenshots          # rebuild test-app, capture → docs/screenshots/*.png
# iterate on one shot (binaries already built):
cd e2e && pnpm exec playwright test -c playwright.screenshots.config.ts -g imageview
```

This is driven by its **own** config (`e2e/playwright.screenshots.config.ts`,
`workers: 1`) and is **not** part of `make e2e-test`. Layout is deterministic
(seeded PRNG), so re-running only moves pixels when the UI actually changed.

## Adding a shot

Each test follows one shape: boot the desktop, open the app from the start
menu, wait for its window, screenshot it.

```ts
test('top', async ({ page, router }) => {
  await bootThemed(page, router, 'top');          // applies the assigned pack
  await openApp(page, 'com.wash.top');            // start-menu launch
  const w = win(page, 'wash-app-top');            // window selector
  await expect(w).toBeVisible();
  await w.locator('[data-testid="top-statusbar"]').waitFor({ timeout: 8000 }).catch(() => {});
  await settle(page, 900);                        // let live content land
  await w.screenshot({ path: join(SHOTS, 'top.png') });
});
```

- Window selector is `wash-app-<name>`; the start-menu id is `com.wash.<name>`.
- Stage the app binary in the `STAGE` array (passing `apps` **replaces** the
  fixture default set — the shell's own `session`/`about`/`notify` must stay
  listed, and a windowed app that needs a background sibling must list it too,
  e.g. `connect` needs `remote`).
- Wrap content `waitFor`s in `.catch(() => {})` so a missing testid degrades to
  a slightly emptier shot instead of a hard failure — **except** a `.click()`
  on an element that may never exist, which blocks until the 60 s test timeout
  (this is exactly how the imageview "No images" bug manifested).
- An app whose content is short leaves the frame half empty at the default
  window size. Either give it more to say (the Agent shots) or drag the
  bottom-right grip (`[data-testid="window-resize"]`) down to the content —
  `resizeWinTo(page, w, width, height)` in the montage does this.

## Non-obvious things (read before you debug a blank shot)

### The fs-root sandbox confines *every* app, not just wash-fm

The router honours **`WASH_FM_ROOT`** as the **global** filesystem sandbox —
`internal/runner/router` resolves the fs-root as
`firstNonEmpty(--fs-root, WASH_FS_ROOT, WASH_FM_ROOT)` and ships it as
`Session.Root` to **all** apps. So any app that calls `Confine` (imageview,
edit, …) rejects paths outside that root.

The capture fixture's `fmRoot: true` makes a **random** tmpdir, so you can't
point an app at a known folder inside it. The capture instead uses a **fixed,
pre-seeded root** handed to the router via `extraEnv.WASH_FM_ROOT`, with the
image folder at `<root>/Pictures` and `WASH_IMAGEVIEW_DIR=<root>/Pictures`.
imageview's default scan order is `$WASH_IMAGEVIEW_DIR → ~/Pictures → ~`, but
`~` is outside the sandbox — hence the explicit dir.

### Theming is seeded through desktop.json, before connect

To shoot an app under a given theme pack, write
`<xdgConfigHome>/wash/desktop.json` = `{"pack":"<id>"}` **before** `page.goto`.
wash-session reads it at spawn and the shell applies the scheme to the document
root, so every open window re-skins. The five packs: `midnight` (default dark),
`tokyo`, `seoul` (light), `copland` (Mac OS 9 light), `oslo` (dark slate). The
`THEME` map in the capture assigns one per app so the README grid shows the
range — same desktop, different packs.

### The Agent shots run a real agent — with scripted words

`agent.png` and the Agent window in the montage drive the **real**
`com.wash.ai` + `agentd` stack against the e2e fake ACP adapter
(`e2e/fixtures/acp-fake`, staged by `make` at `out/e2e/codex-acp` — hence
`screenshots:` depends on that target). Everything in the frame is genuine:
the transcript renderer, the markdown/table paths, the status bar, the
session folder. Only the replies are posed.

The seam is **`ACP_FAKE_SCRIPT=<file>`**, a JSON array of strings; the
adapter streams the next entry per turn instead of its fixed test text. It
is env-gated and off by default, so `make e2e-test` — which asserts on that
fixed text — is unaffected. Keyword turns (`runcmd`, `readfile`, `writefile`,
anything containing "ask") still take their own paths, so a script can mix
posed prose with real capability exercises.

Two rules learned the hard way:

- **Wait for the turn to END** (`agent-stop` gone), not just for the reply
  text. Shooting mid-turn puts a "working… / Stop" row in the frame, which
  reads as a hung agent.
- The Agent capture lives in its **own `test.describe`** with no
  `WASH_FM_ROOT`. The folder picked in the launcher becomes the adapter's
  real working directory, so it has to be a genuine host path — under the
  shared sandbox root the session starts in a confined path that the
  adapter process cannot actually chdir into.

### Deterministic discovery for the Connect shot

`WASH_DISCOVERY_STATIC="name=host[:port],…"` injects fake "On your network"
mDNS peers, and `WASH_DISCOVERY_NO_ADVERTISE=1` stops headless runs from
broadcasting. (Same seam as `e2e/tests/discovery.spec.ts`.)

### The PNGs are committed past a broad .gitignore

`.gitignore` ignores `screenshots/` (runtime capture output from
`display-probe.cap.ts` and `--screenshot-dir`). The curated
`docs/screenshots/` shots are committed via a `!docs/screenshots/` negation —
new shots land automatically, no `git add -f` needed.

### Privacy

The montage hides the desktop banner (it prints the real hostname + interface
addresses, incl. a public IPv6) right before capture via a shadow-piercing
walk. Per-app shots may still show host paths in a process list (System
Monitor) — fine for a dev shot, but don't pose anything with secrets.

### Gotcha when verifying env reaches an app

Spawned apps inherit the router env (`spawn.go` = `append(os.Environ(), …)`).
When checking `/proc/<pid>/environ`, make sure you're inspecting the app the
**current** router spawned — orphaned app processes from a previous capture run
give false "env missing" negatives.
