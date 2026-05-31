# wash-display — display env propagation (DISPLAY / WAYLAND_DISPLAY)

Status: **design** (2026-05-30). This is the contract that makes
`xclock` (or any X/Wayland client) **just work when typed in a wash
terminal** — by getting the compositor's socket names into the
environment of apps the router spawns.

Goal scenario: open Firefox → wash desktop → launch term → type
`xclock` → an xclock window appears as a wash window. Today this fails
with `Can't open display:` because the shell's env has no `DISPLAY`.

Related: [DISPLAY.md](DISPLAY.md) (§2 architecture, §6 input).

---

## 1. Why it doesn't work today

- The compositor (`com.wash.display`) brings up Xwayland and, **in its
  own process env only**, sets `DISPLAY=:N` and `WAYLAND_DISPLAY=wayland-N`
  (compositor.cpp `setenv`). Env flows parent→child, so this never
  reaches sibling processes like `wash-term`.
- The compositor already announces `app_msg{kind:"display_ready",
  wayland_display:...}` on the event channel — but **nobody consumes it**,
  and the router **deliberately never parses `app_msg` data** (it's an
  opaque `json.RawMessage` relayed FE↔BE; see `handleEvt`/`EvtAppMsg`).
  So `app_msg` is the wrong carrier for something the router must read.
- `wash-term`'s shell env is built by `pty.WithWashEnv` (TERM, PATH).
  It has no notion of a display.

The fix is a small, explicit **compositor → router → spawned-app** env
relay, plus a term mapping step.

---

## 2. The variable contract

Two layers of names, deliberately distinct:

### 2a. Router-published, namespaced (every spawned app gets these)

The router republishes the compositor's sockets to **all** apps it
spawns, under a `WASH_`-namespaced prefix so they are inert unless an app
opts in. Added in `Router.spawnEnv()` alongside the existing
`WASH_CONTROL_SOCKET` / `WASH_BIN_DIR`:

| Variable | Example | Meaning |
|---|---|---|
| `WASH_WAYLAND_DISPLAY` | `wayland-0` | the compositor's Wayland socket name |
| `WASH_X_DISPLAY` | `:2` | the Xwayland X11 display |
| `WASH_XDG_RUNTIME_DIR` | `/run/user/1000` | dir holding the `wayland-N` socket (clients need this to resolve `WAYLAND_DISPLAY`) |

Namespaced (not raw `DISPLAY`/`WAYLAND_DISPLAY`) **on purpose**: wash's
own Go apps must NOT accidentally inherit a GUI display and try to render
to it. Only an app that explicitly maps them becomes a display client.

### 2b. Term-mapped, real (only the terminal's shell gets these)

`wash-term`'s `pty.WithWashEnv` maps the namespaced vars to the real
ones for the **interactive shell** it spawns (and only there):

```
WASH_WAYLAND_DISPLAY  → WAYLAND_DISPLAY
WASH_X_DISPLAY        → DISPLAY
WASH_XDG_RUNTIME_DIR  → XDG_RUNTIME_DIR   (only if not already set)
```

After this, `xclock` typed at the prompt finds the X server and maps as
a wash window. `weston-terminal`, GTK apps, etc. find the Wayland socket
the same way.

Mapping rules:
- Only set each real var if the corresponding `WASH_*` is non-empty.
- Don't clobber a pre-existing `WAYLAND_DISPLAY`/`DISPLAY` the user
  exported themselves (respect an explicit override).
- `XDG_RUNTIME_DIR` is only overridden if unset, since it has broader
  meaning than display.

---

## 3. The transport: a typed control event (NOT app_msg)

Because the router must *read* this (and `app_msg` is opaque by
contract), add a dedicated control-channel event the router handles
explicitly.

```jsonc
// compositor → router, on the control channel (ch 0), after Xwayland is up
{ "t":"env.publish", "env": {
    "WASH_WAYLAND_DISPLAY":"wayland-0",
    "WASH_X_DISPLAY":":2",
    "WASH_XDG_RUNTIME_DIR":"/run/user/1000"
} }
```

- **Wire:** new `EvtEnvPublish{ T, Env map[string]string }` in
  `internal/wire`. Round-trips in `msgs_test`.
- **Router:** `handleEvt` case `TEvtEnvPublish` → **capability gate**
  (see §4) → store `Env` on the Router under a mutex
  (`r.publishedEnvMu`, `r.publishedEnv map[string]string`).
- **spawnEnv():** append every `k=v` from `r.publishedEnv` to the
  returned slice, so subsequently-spawned apps inherit them.
- **Compositor (C++):** after `wlr_xwayland` is up and the Wayland
  socket is bound, call a new `WireConn::publish_env({...})` that writes
  the `env.publish` frame on `CH_CONTROL`. Keep the existing
  `display_ready` app_msg too (harmless; other apps may still want it).

---

## 4. Security gate

Publishing env to *every* future app is privileged — a rogue app could
inject `LD_PRELOAD`, `PATH`, etc. Gate it:

- **Capability:** only an instance whose manifest declares a new
  `env-publish` capability may send `env.publish`; others get
  `env.publish.err code=forbidden` (mirrors the `windows`/`spawn` gate).
  `com.wash.display`'s manifest adds `"env-publish"` to its
  capabilities.
- **Key allowlist:** the router accepts only keys matching `^WASH_[A-Z0-9_]+$`
  from a published map — so even a capable app can't set `PATH` or
  `LD_PRELOAD`, only `WASH_*` names. Term's mapping (§2b) is the only
  thing that turns those into real display vars, and it only maps the
  three known display keys.

This keeps the blast radius to "a capable app can set WASH_* hints,"
which is exactly what the display server needs and nothing more.

---

## 5. Known limitations (v1)

- **Timing / ordering.** `spawnEnv()` is read at each app's spawn time.
  The compositor publishes after it starts (on first shell connect). An
  app spawned *before* the publish lands won't have the vars. In
  practice term is opened after the desktop is up, so the compositor has
  usually published by then — but it's a race. Mitigations (later):
  the router could re-push env to running apps, or term could request
  current env on each new tab. v1 accepts "open a terminal after the
  display is up" and documents it.
- **Single compositor.** One display instance (singleton); the contract
  assumes one set of sockets. Multi-seat is out of scope.
- **No live re-publish on compositor restart.** If the compositor is
  killed/restarted, already-spawned shells keep stale vars until a new
  tab. Tied to the manager-lifecycle/settings work, not this change.

---

## 6. Commit plan

1. `wire: env.publish event + env-publish capability` — `internal/wire`
   (`EvtEnvPublish`, `TEvtEnvPublish`, `env.publish.err`), `msgs_test`.
   ✅ `go test ./internal/wire/...`
2. `router: capture env.publish (gated) + merge into spawnEnv` —
   `internal/router`. Unit test: a capable app publishes, a later
   spawn's env contains the WASH_* keys; an uncapable app is rejected.
3. `pty: map WASH_*_DISPLAY → DISPLAY/WAYLAND_DISPLAY in WithWashEnv` —
   `internal/pty`. Unit test on the mapping.
4. `display: publish_env over the control channel after Xwayland up` —
   `wash-display/src` (C++), + `"env-publish"` in the manifest.
5. (verify) real-stack: type `xclock` in a wash terminal → window with a
   live clock. The `real_e2e` harness can drive this headlessly.

Steps 1–3 are pure Go and unit-testable in CI without a compositor;
step 4 needs `WASH_DISPLAY=1`; step 5 needs the full stack.
