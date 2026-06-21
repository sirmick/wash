# wash remote apps — run an app on another host, displayed in this desktop

Status: **implemented** (2026-06-14; branch `wash-remote`). M1 (multi-homed
shell), M2a (`com.wash.remote` supervisor), M3 (`wash-connect` app), the **one-
port relay** (§2), and a two-VM real-ssh e2e are done and green. M2e (durable
B-router across an ssh drop), M4/M5 (multi-host widgets + services), and M6
(hardening) remain. Launching wash apps on a *remote* machine entirely from the
local desktop, windows appearing live + interactive **in the local desktop** —
colour-striped per host. SSH is the transport and the trust boundary; no new
network listener, no recursive router; the browser keeps **one** connection (§2).

UI / polish (2026-06-17): the `wash-connect` window is a two-section host list
(connected on top, bookmarks below) with a per-host **Launch** dropdown (the
host's live catalog, icon + name) and connected rows bordered in the host's
colour; the desktop **Remote** sidebar widget carries the same per-host launch
dropdown (a connected host stays attached in the shell after the window closes,
so `catalogFor`/`launchOn` work straight from the sidebar). Two cross-origin
correctness fixes landed alongside: WM intents (move/focus/resize/state/close)
are now origin-addressed, and stacking uses an FE-arbitrated global `gz` instead
of the colliding per-router `z` (so focusing a remote window raises it above
local ones). Window/instance/raw-channel/display-video registries are all
origin-scoped — see §"Remote wash-display".

The end effect: sitting in desktop **A**, with SSH credentials to host **B**, you
pick a host + an app and its window opens among your local windows. Refreshing
the browser reattaches exactly like today, because the SSH link and B's router
stay up between the wash hosts. Remote windows carry a coloured stripe naming
their host; the sidebar gains a host-connections widget and its existing widgets
become multi-host aware.

Related: [ARCHITECTURE.md](ARCHITECTURE.md) (flat-router rule, the background
service tier), [WIRE.md](WIRE.md) (frames, channels, identity/attach),
[MULTIUSER.md](MULTIUSER.md) (`wash-login`, per-uid/sessid routers — the model
this rides on), [RECONNECT-AUDIT.md](RECONNECT-AUDIT.md) (reconnect invariants),
[DISPLAY.md](DISPLAY.md) (the WebRTC media side-channel the audio/GUI tracks reuse),
[AUDIO.md](AUDIO.md), [NET.md](NET.md). The transparent-tunnel-to-a-remote-router
pattern is the same one `wash-vm/` already proves for VMs — a VM is just "another
host"; B is literally another host.

---

## 1. Goal & non-goals

**Goal.** From desktop A, run native wash apps on host B and use them as ordinary
windows in A's desktop, with correct services, durable reconnect, and clear host
attribution — all driven from A, requiring nothing on B beyond a normal wash
install and SSH access.

**The chosen model is R2: a multi-homed shell, never a recursive router.** A's
browser is a client of *two* routers — its local one and B's — and composites B's
windows into A's window manager. The merge lives in the **frontend**, not in
either router. The browser holds **one** physical connection (to A): B's wire is
multiplexed over it as a relay channel that A splices **verbatim** to an `ssh -L`'d
socket reaching B (§2, the "one port" rule). This is deliberate (see §13): the
alternatives (A's router hosting a remote *process*, or A's router *federating*
B's router) drag in `/proc`-based attach, no-local-pid lifecycle, cross-host
service misrouting, and — for federation — making the trusted core *interpret* B's
wire and breaking the verbatim-splice invariant. R2 avoids all of it because **B
is just normal wash** and **A's router never decodes B's frames** — it copies
opaque bytes, the same dumb-pipe role the ssh client already plays.

**Non-goals (v1).**
- **Audio output from remote apps** — sound is physical and comes out of *B's*
  speakers; making it audible at A needs an audio *stream*, not a widget. Deferred
  to its own track riding the display WebRTC side-channel ([DISPLAY.md](DISPLAY.md),
  [AUDIO.md](AUDIO.md)). The mixer *state* may surface in the sidebar; the sound
  does not.
- **Remote GUI / X11 apps** — these need the `wash-display` compositor + graphics
  stack running on B; once present they are "just another wash app" whose frames
  relay, but deploying the compositor on B is out of scope here.
- **Cross-host app interaction** — A's apps and B's apps cannot message each other,
  and "open with" / file associations do not cross hosts. A remote app is a
  self-contained island talking only to its own FE and its own B-side services.
- **Cross-origin drag-and-drop** between an A window and a B window (it is a real
  byte transfer, scp-like — deferred). Until then fm **rejects** such a drop with
  a status flash rather than attempting it: a drag carries its source window's
  origin (`application/x-wash-origin`, from the app's `props.origin`), and the
  drop handler bails when it differs from the dropping window's origin — without
  this the destination router would run `rename()` on the source paths in *its*
  filesystem (wrong file on a same-path collision, else not-found). Same-origin
  drops (incl. between two windows on one host) are unaffected.
- **Recursive / federating routers.** Never. If cross-host *control-plane*
  integration is ever wanted, it is a separate gateway process, not the router
  (see §13).

---

## 2. Topology & transport — the "one port" relay

The browser keeps **exactly one** connection — to A. B's wire is **multiplexed
over that single connection** as a relay channel, which A's router **splices
verbatim** to an `ssh -L`'d socket reaching B. The browser never opens a second
socket and never names a B address — it only ever talks to A (the wash "one
port" rule).

```
  Browser ──one connection──▶ A's router ──demux──┬─ ch0 + A's channels → A (HandleShell)
                                                  └─ peer-channel(B) ─io.Copy─▶ unix socket
                                                          (verbatim; A never decodes B's frames)
                                                                          │ ssh -L
                                                                          ▼
                                                          B's wash-router  --listen-raw
```

So there are still three *logical* legs — the browser↔A connection, the `ssh -L`
A→B, and the ssh session — but the browser↔B "leg" is **carried inside** the
browser↔A connection, not a separate WebSocket. This is what makes R2 work for a
remote browser, the wash-vm proxy, or an in-browser VM, none of which can reach
A's loopback: everything funnels through the one connection to A.

**Why a relay, not a recursive router (§13):** A copies opaque bytes between the
peer channel and the socket — the same dumb-pipe role the ssh client already
plays. A's *router* never parses, re-namespaces, or owns B's frames; the merge
stays in the FE. "A never interprets B's wire" is the invariant, not "B's bytes
never touch A" (they always do, via ssh).

**The pieces.** B serves the raw wire on a socket with `wash-router
--listen-raw unix:/path | tcp:host:port` (no HTTP/WebSocket, no token). The A-side
`com.wash.remote` supervisor runs `ssh -L <unix-socket>:127.0.0.1:RP host
wash-router --listen-raw tcp:127.0.0.1:RP` and registers (origin → that socket)
with A's router via a capability-gated `peer.register` (only the supervisor may
register). A browser attaches with `peer.attach{origin}`; A's router dials the
socket, allocates a `kind:"peer"` channel, and splices. **B's sshd must permit
port forwarding** (`AllowTcpForwarding`/`AllowStreamLocalForwarding`) — the relay
is an `ssh -L`; a remote-side refusal is silent on A.

**SSH is the transport and the access control.** B's router binds **loopback on
B with no token**; reaching it requires the SSH session, full stop. No wash port
faces the network on B. SSH provides mutual auth (host keys defeat MITM, user
keys/agent for identity), encryption, and — via `authorized_keys`
`command=…,restrict` — the ability to pin a key to *only* the launcher. The user
lands on B as a real uid, so `/proc`, filesystem permissions, and `sudo` are all
ordinary OS facts on B.

**B's router must be persistent and decoupled from the SSH session.** Start it
detached (`systemd-run --user`, or a small supervisor) — *not* as the foreground
process of `ssh B wash-router …`. Then an SSH drop is a transport blip: B's
router keeps running and keeps the apps alive. This is the mosh-vs-ssh
distinction, and it is what makes "browser refresh just reattaches" and "a
network blip freezes rather than kills" both true.

**The A-side tunnel supervisor.** A new local component owns the SSH lifecycle:
it dials B (honouring `ProxyJump`/bastions — see below), maintains the `-L`
forward, re-dials with backoff (`ServerAliveInterval`), and reports per-host
status (`up` / `reconnecting` / `down`) to the shell. The browser only ever sees
"the B WS dropped"; it cannot itself distinguish tunnel-down from router-down, so
the supervisor is what disambiguates and drives the sidebar's host state.

**Unreachable hosts use SSH, not router relay.** If C is only reachable through a
bastion B, use SSH `ProxyJump` (`ssh -J B C`): the *tunnel* chains at the SSH
layer while the router protocol stays flat fan-out — the browser still gets a
direct logical connection to C's router. We never chain routers to reach a host.

---

## 3. Launch flow — invoked entirely from A

1. **Pick.** From A's launcher (a per-host section or a host picker — see §6) the
   user chooses host B + an app id.
2. **Bring up B's session.** If the tunnel supervisor has no live B router for
   this user, it SSHes in and starts a persistent `wash-router` (via B's
   `wash-login`, reusing the per-uid/sessid session model — see
   [MULTIUSER.md](MULTIUSER.md)), then establishes the `-L` forward. The browser
   opens its second WS to it.
3. **Catalog + bundle arrive on connect — no separate probe.** Because A's browser
   connects to **B's own router** as a normal shell client, B's router has already
   probed its *local* apps (ordinary local `--wash-manifest` exec) and serves B's
   matched manifest + FE bundle through the existing `replayBundleToShell` path over
   the tunnel — exactly as it would to a browser on B. The bundle is always whatever
   B's binary emits, so version skew is structurally impossible. There is **no
   probe-over-SSH** and **no remote app-attach**: the launcher's remote section is
   populated from B's `catalog`, which arrives on connect. (See §10 on the trust
   implication of running B's FE code.)
4. **Launch.** A normal spawn request the shell sends to B's router over its WS.
   B's router spawns the app **locally and normally** — ordinary fork, inherited fd,
   `/proc` validation, all working because it is all on B (no `/proc`-skip token
   path needed). The app attaches to B's **local** router; B emits `app.declared` +
   window + bundle over the tunnel exactly as it would to a local browser.
5. **Composite.** A's shell mounts the element, tags the window origin = B, draws
   the host stripe (§5/§11), and places it in A's window manager.

Nothing happens on B that a local launch would not do; the only new machinery is
"start it over SSH and point it back through the tunnel."

---

## 4. The frontend — one shell, N routers

The heart of R2 and the bulk of the work. Today the shell is implicitly a
single-router client (one WS, one channel table, one instance/window/channel
namespace). R2 introduces:

- **`RouterClient`** — one per connection, owning its codec, channel table,
  catalog, and session view. The local router becomes just client #0; A is not
  special except that it owns the desktop chrome.
- **Origin-namespaced IDs.** Every router-supplied id is keyed by its origin:
  `(origin, instance_id)`, `(origin, window_id)`, `(origin, channel_id)`. Channel
  42 on A ≠ channel 42 on B; `i-3` on A ≠ `i-3` on B. This pass touches the whole
  shell and is the main risk — single-router assumptions are baked in (e.g. the
  `wash.viewport` localStorage key; `SaveState` keyed by bare `instance_id`).
- **Per-origin app wiring.** The app-context handed to a mounted element is bound
  to *its* `RouterClient`, so all of its I/O (app_msg, raw channels, future video)
  flows on the right connection.
- **Per-origin transport.** A remote `RouterClient`'s "socket" is a
  `RelayChannelSocket` — a `SocketLike` that sends each B frame as a raw frame on
  the relay channel of A's connection and reassembles B's length-prefixed frames
  from A's arbitrarily-chunked relay bytes (the same role `VirtioConsoleSocket`
  plays for the v86 transport). B speaks ordinary shell-wire end to end; only the
  *carrier* differs from the local client (a muxed channel vs. a real WebSocket).
- **Element-tag collision handling.** Custom-element tags are global by name in
  the DOM: A's and B's wash-fm both `customElements.define('wash-app-fm', …)`.
  Resolve by per-origin tag mangling or scoped/realm-isolated bundle loading.
  (This affects *app windows* only — the sidebar is immune; see §6.) No choice of
  merge-location avoids this; it is a DOM fact.

**Guardrail:** A multi-homed shell keeps every router flat. The relay channel
makes A's router carry B's bytes, but **opaquely** (`io.Copy` between the channel
and the socket) — A's router still never decodes B's wire, so it stays verbatim
transport, not an interpreter. A-router-*terminating*-B-router (federation) would
be the forbidden recursion ([ARCHITECTURE.md](ARCHITECTURE.md)); a byte splice is
not (§13).

---

## 5. Window manager & host striping

A's session app is the **single compositor**. Each `RouterClient` contributes
windows tagged by origin; A owns global geometry, z-order, and focus, placing
B's windows like local ones. The split:

- **Placement** (position in A's virtual desktop, z-order, focus) → **A**.
- **Intrinsic** window facts (size hints, title, close-requested) → originate at
  **B**, and A honours them; app-initiated resize/title round-trip to B's router.

**The host stripe.** Every window from a non-local origin carries a coloured
stripe (a thin band along the title bar / window edge) in that host's assigned
accent (§11). Local (A) windows have no stripe (or a neutral one). The stripe is
chrome the session app draws from the window's origin tag — purely an A-side
decoration, no protocol change.

**To confirm before building:** whether window geometry/z-order is authoritative
in the *router* (`internal/router/wmstate.go`) or the *session app*. Geometry is
router-held but **ephemeral** — it survives a browser reload (router replays
`session.snapshot`) but **not** an A-router restart (no disk persistence). That
split decides whether §5 is "merge two window models" or "the session app already
owns placement and just spans origins." This is the single load-bearing
verification for the WM work.

---

## 6. The sidebar — host-aware, colour-coded

The sidebar is **entirely A's chrome**: all 8 widgets are Solid components in the
session app's own bundle (`apps/session/fe/src/sidebar/`), rendered *from data*.
The services (notify/bulk/audio/priv/netd) are BE-only and ship **zero** FE.
Consequence: **the sidebar has no element-collision problem** — A already owns
the widget code; it just needs more data, tagged by host.

### 6.1 Connecting to hosts — the `wash-connect` app (supersedes the Hosts sidebar widget)

**Decision (2026-06-13):** the connect UI is a dedicated window app
**`wash-connect` (`com.wash.connect`)**, not a sidebar widget. A normal app is
self-contained (its own BE+FE), has room for a real UI, follows the standard
wash pattern, and avoids the session-FE coupling + BE-gateway + cross-element
subscribe dance a sidebar widget needs. (The session BE gateway built in
`fda1d8b` becomes unused — leave harmless or revert.)

`wash-connect` is the user-facing face of the **`com.wash.remote` background
supervisor** (§3/§9 — already built):

- a sidebar button / launcher entry opens the `wash-connect` window;
- **enter a hostname → Connect** → its BE sends `remote_connect{host}` cross-app
  to `com.wash.remote`, which SSHes out and brings up B's router and reports a
  local endpoint + status (`starting`/`up`/`reconnecting`/`down`), shown in the
  host colour (§11);
- once up, **`wash-connect` lists B's wash apps** (B's catalog, delivered over
  the shell's second RouterClient) and **you pick one to launch**;
- disconnect / reconnect per host.

**Architecture:** `com.wash.remote` stays the **background** supervisor so remote
sessions persist when you close the `wash-connect` window; `wash-connect` is its
window front-end (BE subscribes to `com.wash.remote` cross-app). Host colour
assignment + override (§11) moves into `wash-connect`.

**Three shared backend bits make "list → launch" real (needed regardless of the
UI surface):**
1. **Un-guard B's catalog per-origin** (M1f guarded non-local catalogs off) so the
   app can list the remote host's apps; expose it to the FE per origin.
2. A **shell→router `ShellLaunch{app_id}` ctrl verb** + `window.wash.launchOn(
   origin, appID)` — B runs `--no-session`, so there's no session BE to route a
   launch through; the router grows a direct launch verb (it already spawns via
   its control socket).
3. **`window.wash.attachRemote(origin,url)` / `detachRemote(origin)`** + a
   `wm.dropOrigin(origin)` so the FE attaches the endpoint the supervisor reports
   and drops a host's windows on disconnect.

*(Open question to confirm on resume: the recommended split is background
supervisor + window front-end, as above. The simpler-but-non-persistent
alternative is to fold the SSH supervision into `wash-connect`'s own BE and drop
`com.wash.remote` — then closing the window drops all remote sessions.)*

### 6.2 Existing widgets become multi-host aware

Today each widget's data flows: service `StateService` → **session BE gateway**
(re-brands to `notify.state`/`bulk.state`/…) → session FE signal → widget. B has
no session app, so that gateway does not exist on B. Therefore **A's session FE
subscribes directly to B's singletons over the B `RouterClient`** and merges their
state into the widgets, each entry tagged + tinted by host colour. The
session-BE-gateway role collapses into A's shell for remote routers.

Per-widget disposition (this is the "services case by case" of §7, seen from the
sidebar):

| Widget | Disposition | Notes |
|---|---|---|
| **Notify** | merge | A + B toasts in one list, host-coloured; actions route back to the originating host |
| **Bulk** | merge | A + B file-op queues, host-coloured |
| **Priv** | merge | pending escalations from each host; prompt must name the host (§10) |
| **Net** | per-host | `netd` manages *its own* host's network; show B's status when managing B |
| **About** | per-host | CPU/mem/identity is a machine; A's by default, optional B card |
| **Audio** | special | state may show, but sound is at B — see §7; effectively deferred |
| **Viewport** | A-only | the virtual desktop is the seat's |
| **Clipboard (history)** | A-only | the system clipboard is the seat's (sync in §7) |

---

## 7. Services — case by case

The organising principle is the **tty model**: a service has a *machine half*
(runs where the resource is) and a *presentation half* (surfaces where the human
is). In R2 the app's services resolve against **B's** router (resolution is
router-local — `resolveRecipient`/`r.singletons`), so the machine half is
correctly B-local; the presentation half attaches at A's shell, because wash's
presentation services already "broadcast to the connected shell" (`shellList()`),
and A's tunnelled shell *is* connected.

| Service | Class | In R2 |
|---|---|---|
| **bulk** (file ops) | resource | Runs on B against B's files. Correct, zero bridging. Progress shown in A's bulk widget. |
| **(spawn capability)** | resource | B's router spawns the child on B; its window composites into A. "Open with" within B works. |
| **wash-term pty** | resource→presentation | pty on B (B's shell/processes); glyphs stream to A's xterm. Literally ssh-as-a-composited-window. Long-running processes survive an SSH blip (tmux-like). |
| **priv** (escalation) | resource + sensitive prompt | Escalates on B (B's sudo). Prompt rendered by A's shell; password **encrypted to B's priv `be_pubkey`** so A's seat can't read it. See §10. |
| **notify** | presentation | B's notify broadcasts; A's shell receives over the tunnel and merges into the tray, host-coloured. |
| **clipboard** | presentation (split) | Two router-held clipboards (A's + B's). A's shell is the sync hub: mirror the single browser system clipboard into both routers, bidirectionally, so copy-in-A/paste-in-B works. |
| **netd** | machine-global | Manages a specific host's network; show/edit per-host. Two sessions on one host contend over one stack (inherent to sharing a box). |
| **audio** | physical | Sound exits **B's** speakers. The mixer state can surface; the audio itself needs a stream to A (deferred, rides display WebRTC). Do **not** assume parity with file-ops. |

**Caveat across all of them:** `sdk.StateService` is **live-only** — subscribers
get a fresh snapshot on (re)subscribe, not a replay of events missed while
disconnected. A notification or job-completion that fires during an SSH blip is
lost. Acceptable; note it in the UI where it matters.

**Relay QoS — the relay is class-aware, and creditless.** A forwards B's wire
**one frame at a time, preserving each frame's wire CLASS** (it reads the 8-byte
header for class + length; the payload is forwarded verbatim — header yes,
payload never; not federation, §13). So B's *interactive* frames (keystrokes,
focus, control, the bundle) jump A's strict-priority scheduler ahead of B's
*bulk* (pty output, file streams), which yields to A's **local** interactive
traffic — a remote `cp` flooding output can't make the remote terminal choppy.

The relay peer channel carries **no credit window of its own** (`channelBinding.
noCredit`). Flow control is end to end and B's job: B already paces *each* of its
inner channels by the FE's per-channel credit (the FE's B-`RouterClient` grants
credit on B's channels as it absorbs them, §4). An A-side aggregate window would
only **double-gate** the same flow — and worse, its blocking credit `Reserve`
runs in the single `pumpPeerToShell` goroutine, so once that one window emptied
(routine under any sustained download) the pump couldn't read B's *next*
interactive frame: the very head-of-line block the class-preservation exists to
prevent, reintroduced at the read boundary. Creditless, the pump never blocks
reading B's socket. A-side memory stays bounded because B can only have its
per-channel windows' worth in flight, and the FE throttles B by withholding
credit on B's inner channels the moment it stops absorbing; the only remaining
backstop is A's `scheduler.Submit` blocking when its Bulk queue fills, which
happens only when the FE is genuinely wedged (and B is throttled by then anyway).

This also makes **concurrent B bulk streams** fair for free: two downloads (or a
download + video) are both Bulk, interleave FIFO in A's scheduler, and each is
independently paced by *its own* B-side per-channel window — no shared relay
window couples them, and neither can starve the other or the remote terminal. No
per-B-channel relay demux is needed; per-class scheduling + B's per-channel
credit already deliver the isolation a demux was going to add. (History: the v0
relay was an opaque single Bulk lane; then class-preserving but A-credit-gated;
this is the creditless successor — same single channel, simpler and HOL-free.)

---

## 8. State & persistence — where the backing stores sit

The browser is only a renderer; almost all persistence is router-side, so it
follows the app to B.

- **App FE backing store** (`conn.SaveState` → router, keyed by `instance_id`,
  shipped in `session.snapshot`; e.g. fm view-mode/folder, edit cursor/tabs):
  **sits in B's router** for a B app. A's shell routes the `wash:state` event to
  the B-origin element. So content *and* view state persist with the app on B;
  A holds only the window's *placement*.
- **Window geometry / z-order:** router-held, ephemeral (survives reload, not
  router restart).
- **Viewport (camera pan):** `localStorage` `wash.viewport` on A — **single key,
  A-only** (B has no desktop, so the multi-router viewport collision the codebase
  warns about does not arise here).
- **Settings:** files on disk per host. Desktop appearance (`desktop.json`) = A;
  anything a B app configures about B's machine (`network.json`, …) = B.
- **Service state** (notify/bulk/audio): ephemeral, in-memory in the owning
  router; restarting a B service drops B's history (the tray re-subscribes for a
  fresh snapshot).

Consequence: a B app's state is keyed by **B's** `instance_id`. Reconnecting to
B replays it (durability). *Moving* the same app A↔B would mint a new
`instance_id` and lose the blob — correlate by `app_id` only if migration ever
matters (out of scope).

---

## 9. Reconnection & lifecycle

- **Browser reload (everything up):** both connections re-establish; A and B each
  replay `session.snapshot`. Because nothing restarted, B's `instance_id`s are
  stable, so B's windows restore exactly (view state from B's router; placement
  re-correlated by A's session app). **Full recovery, both sides** — the
  requirement "refresh just reattaches like now."
- **SSH blip (B router up):** A's shell freezes B's windows (last frame, dimmed,
  non-interactive, host badge → "reconnecting"); **the work continues on B**
  (a copy keeps copying, a pty process keeps running). The supervisor re-dials;
  on success B replays and windows thaw to current state. **Freeze, don't queue**
  input — never replay stale clicks/keys.
- **SSH down long / give up:** windows stay frozen until a timeout or user
  eviction; if B's persistent router survives, a later fresh reconnect still
  recovers (B kept the app).
- **A's seat crashes:** takes the desktop down; B's app survives on B and is
  recoverable on reconnect, but window *placement* is lost if A's router restarted
  (pre-existing geometry-not-persisted limitation).
- **No token on B reconnect:** B's router is loopback + SSH-gated; a fresh tunnel
  needs no auth handshake.
- **B router teardown policy (to pick):** default "router lives while its apps
  live; on last-app exit, linger briefly then exit" — durable across blips,
  self-cleaning when actually done.

Align with [RECONNECT-AUDIT.md](RECONNECT-AUDIT.md): the per-`RouterClient`
reconnect loop is the existing shell reconnect logic, instantiated per origin.

---

## 10. Security & trust

- **SSH is the boundary.** Reaching B's router requires the SSH session; B exposes
  no wash port. Scope the key with `command=…,restrict` to the launcher only.
- **B's raw listener is a `0600` unix socket, NOT loopback TCP.** A loopback TCP
  listener (`127.0.0.1:RP`) is reachable by *any local user on B* — an
  unauthenticated full wash session, a local privilege escalation on a shared
  host. `--listen-raw unix:/path` (chmod `0600` by the router after bind) makes
  the listener uid-only, so "reaching it requires the SSH session" is actually
  true. The relay's flow control is end-to-end (B's per-channel credit, §7) and
  Bulk yields to the local desktop in A's scheduler; A's buffering is bounded by
  B's per-channel windows (a remote flood can't OOM A), and the FE caps a
  declared frame length at `wire.MaxPayload` so a hostile stream can't balloon
  memory. Only `com.wash.remote` may register a relay socket (router-attested
  app id), and the FE references origins, never paths — no arbitrary-dial surface.
- **Code provenance — the real escalation.** A runs **B's FE JavaScript inside the
  shell origin**. A is no longer relaying opaque frames; it executes remote-authored
  code in the user's desktop. This is fine *only* under SSH-gated, user-named hosts
  (you already trust B enough to SSH in); it would **not** be acceptable for any
  open/discovered-host model. The bundle is matched to B's running binary
  (§3) and re-probed on version change.
- **priv prompt is a phishing surface.** A remote host can pop a password modal in
  your local desktop. The modal **must** attribute the origin host and show the
  command ("hostB wants to run `apt …` as root"), in the host's colour. Mitigated:
  the password is encrypted to B's priv `be_pubkey`, so A's shell and A's router
  never see B's plaintext sudo password — R2 priv is as safe as local priv against
  the seat harvesting credentials.
- **Input attribution generally.** Keyboard focus, global shortcuts, and clipboard
  shortcuts must route to the focused window's origin; the host stripe + colour are
  the user's constant cue for which machine they are acting on.

---

## 11. Host identity & colour

Each host gets a stable **accent colour** drawn from a palette consistent with
`@wash/ui` tokens (the per-hue sidebar convention — see the UX-tokens note). The
colour is the single thread tying the experience together:

- **Window stripe** in the host colour (§5).
- **`wash-connect` app** host entries and status dots in the host colour (§6.1).
- **Merged-widget entries** (notify/bulk/priv) tinted/tagged by host colour (§6.2).
- **priv modal** banded in the host colour (§10).

Colour assignment (deterministic hash of hostname → palette slot, with manual
override in `wash-connect`) lives in `wash-connect`. Local (A) is neutral /
unstriped so "no stripe" reads unambiguously as "this machine."

---

## 12. Multi-tenancy (consequences, not new work)

B is an ordinary multi-user/multi-session wash host; this is wash's existing model
([MULTIUSER.md](MULTIUSER.md)), not new machinery:

- **Multiple machines → B as the same user:** each gets its own B session (router,
  own `sessid`) by default — isolated singletons/clipboard/windows. Opt-in shared
  session (attach to an existing router → two shells on one router) gives
  collaboration / screen-share.
- **Different users → B:** OS-isolated per uid.
- **B's own local browser:** just another session — independent by default, or the
  same session for screen-share.
- **Machine-global resources** (netd, the sound device, ports, the filesystem) are
  shared across all sessions on B and can contend — inherent to sharing a host, not
  introduced by R2. Per-session UI state stays isolated.

---

## 13. Why R2 and not a multi-level router (rejected)

Considered and rejected: making A's router *terminate and translate* B's router,
presenting B's apps as local (a recursive/federating router).

- **Its only real wins** are a single window model (no multi-origin FE WM) and
  cross-host app_msg — neither of which we need for "a remote app as a window."
- **Its costs are severe:** it makes the trusted core *interpret* B's wire on its
  hot path, breaks the verbatim-splice invariant, forks the router into
  flat/gateway modes, couples failure domains, grows the most security-critical
  component's attack surface — and **does not even solve the element-collision DOM
  problem**, which is orthogonal to where the merge lives.
- **It violates the architecture's central rule** ("flat router + supervision tree,
  never a recursive router" — [ARCHITECTURE.md](ARCHITECTURE.md)).

**The one-port relay (§2) is NOT this.** A's router copies B's bytes between the
peer channel and the ssh socket with `io.Copy` — it never decodes a B frame,
never re-namespaces, never forks into a gateway mode. That is the same dumb-pipe
role A's ssh *client* already plays, and the FE still does the merge. The
distinguishing line is **"does A's router parse B's frames?"** — relay: no;
federation: yes. The relay stays on the right side of it. (B's bytes transiting
machine A was always true — via ssh — so "B's frames never touch A" was never the
real invariant; "A never interprets them" is.)

If cross-host *control-plane* integration (A's app messaging B's service, unified
services) ever becomes a hard requirement, build it as a **separate federation
gateway process for the control plane only** — never recursive-route the core, and
keep the data plane (windows, raw channels, video) on the direct R2 path. The rule
protects "the router is verbatim transport, not an interpreter," and that is worth
keeping even when going multi-host.

---

## 14. Commit ladder

Staged so the FE refactor lands behind a working spike, and each milestone is
independently testable (e2e per [TESTING.md](TESTING.md): Playwright on the FE,
router-log assertions on the BE).

- **M0 — transport spike.** Manually start a B router; `ssh -L`; A's shell opens a
  second connection and renders one B window (no chrome integration). Proves the
  wire end to end over the tunnel.
- **M1 — `RouterClient` + ID namespacing.** The core FE refactor. Single remote,
  window-surface-only. B window composited with host stripe; input routed to the
  right origin; element-tag collision resolved.
- **M2 — persistent B router + tunnel supervisor + launch flow.** Invoked from A;
  probe-over-SSH bundle (version-keyed cache); browser-refresh reattach;
  freeze/thaw on SSH blip.
- **M3 — `wash-connect` window app.** Host input → connect → app list → launch;
  per-host status + colour; fronts `com.wash.remote`. (Supersedes the Hosts
  sidebar widget. Needs: catalog un-guard per-origin, `ShellLaunch` verb,
  `window.wash.attachRemote/launchOn`.) Connect/disconnect/status; host colour;
  remote launcher entry.
- **M4 — multi-host-aware widgets.** Notify/bulk/priv merged + colour-coded; priv
  prompt host attribution.
- **M5 — services case-by-case.** Clipboard sync hub; notify action routing; net
  per-host. (Audio explicitly excluded — its own track.)
- **M6 — hardening.** Multi-tenancy behaviours, security review (provenance, priv
  phishing), reconnect-audit alignment, orphan/teardown policy.

---

## 15. Deferred

- **Remote audio output** — needs an audio stream to A (ride [DISPLAY.md](DISPLAY.md)
  WebRTC; coordinate with [AUDIO.md](AUDIO.md)).
- **Remote GUI/X apps** — see "Remote wash-display" below: no architectural
  blocker, a small enablement + perf path.
- **Cross-origin DnD** — real byte transfer between hosts.
- **Cross-host app_msg / "open with" across hosts** — would require the control-plane
  gateway of §13.
- **App migration A↔B** preserving FE state — needs `app_id`-correlated state
  transfer (§8).

### 15.1 Remote wash-display (GUI / X / video)

There is **no architectural reason a remote `wash-display` window wouldn't
work** — the relay (§2) is content-agnostic. It carries *any* of B's raw
channels verbatim and class-preserving, and a display window's video is just a
raw channel (`wire.ChannelKindVideo`). The merge state is already origin-safe:
the display↔video registry is keyed by `(origin, windowID)` and the built-in
`<wash-app-display>` element subscribes to its frames on its **own** origin
(not a hardcoded local one). So a remote display window can't collide with a
local one, and its frames would deliver over the relay's origin-scoped raw path.

FE enablement (steps 1–2) **DONE** (2026-06-18); deployment + test remain:

1. **Un-gate the video bind. DONE.** `main.tsx`'s `channel.bind` binds video +
   video-popup for any origin now (`bindVideoChannel(client.origin, …)` /
   `bindPopupChannel`); `channel.unbind` runs `forgetVideoChannel(client.origin,
   …)` for remote too. Local display e2e unchanged (10/10 green).
2. **Origin-scope display *input*. DONE.** `<wash-app-display>` posts its input
   batch via `window.wash.sendAppMsg(this.instanceID, …)` (the compound,
   origin-tagged instance id) instead of `sendAppMsgTo` over the local conn — so
   a remote display window's pointer/keyboard reaches its OWN host's
   wash-display. (Video B→A was already origin-scoped via the registry.)
3. **Deploy + launch `wash-display` on B.** Its X/Wayland env propagation is
   B-local and already works (a terminal on B launches X clients into B's
   compositor); nothing remote-specific there. NOTE the VM image bakes no
   display stack today — a remote-display VM test needs wlroots + Xwayland +
   wash-display + an X app (xlogo) baked into the guest image (the heavy lift).
4. **Transport / perf.** With the above it runs **WebP-frames-over-relay-over-
   ssh** — correct and fine for light GUI, but heavier than the planned
   **VP9-over-WebRTC** media side-channel ([DISPLAY.md](DISPLAY.md)). WebRTC
   stays the eventual path for high-FPS / video-heavy remote surfaces; the relay
   is the zero-extra-infrastructure default.

In short: it's an enablement + an input-channel port + a test, not new
architecture. WebRTC is an optimization on top, not a prerequisite.

---

## 16. Open questions

1. **WM authority** (§5): router vs session app for geometry/z-order — sets the
   shape of the compositing work. The one verification to do first.
2. **Element-tag mangling mechanism** (§4): rename at load vs realm/iframe isolation.
3. **B-side session manager:** reuse `wash-login` over SSH vs a thin per-uid session
   spawner (SSH already authenticated the user).
4. **Host colour palette source** (§11): which `@wash/ui` slots; hash vs manual.
5. **B router teardown policy** (§9): linger duration / idle timeout.
