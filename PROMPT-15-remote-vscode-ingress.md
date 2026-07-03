# Implementation prompt: VSCode (and other `/app/` ingress apps) over the remote relay (issue #15)

You are closing an **architectural gap**, not fixing a one-line bug. This is the largest of the
three and it is a **design-then-build** task. **Before writing any code, read this whole prompt,
confirm the approach with the user (Approach A vs B below), and only then start.** Do not
improvise a third design without asking.

## First action

```
git -C /home/mick/wash worktree add branches/fix-remote-ingress -b fix-remote-ingress
cd /home/mick/wash/branches/fix-remote-ingress
pnpm -C e2e install --ignore-workspace   # e2e/ is not a workspace member
```

## Ground rules

1. One worktree branch under `branches/`; merge back to local `main` when green; then remove it.
2. Green gate: `make build` + `make unit-test` green per commit; before merge run
   `make test-race` and `make e2e-test` (NOT raw playwright — the login fixture needs the
   multicall layout). Push only if the user asks.
3. Logical commits, `fix(...)`/`feat(...)` matching the log.
4. CBOR pitfall: never use `json.RawMessage` / `[]byte` for structured BE→FE fields (the router
   base64-encodes byte strings). Any new wire message follows the existing typed pattern.
5. Every behavioural change gets a test; the acceptance test is the two-VM remote harness
   (`make e2e-remote-vm`) — see below.

## The gap (crux)

When a browser attached to **host A** opens a vscode-workbench window whose backend (code-server)
lives on **host B** via the remote relay, the workbench iframe loads `/app/<token>/…` **against
A's origin** — but the token was minted by **B's** router. A's ingress registry has never heard
of it, so A returns **410 "unknown or expired ingress"**
(`internal/router/ingress.go:209-212`). The remote relay is a raw *shell-wire* byte conduit
(`internal/router/peer.go` — `pumpPeerToShell` forwards B's frames verbatim, `:163-187`); it
carries no HTTP, and B's `--listen-raw` router serves no HTTP at all
(`internal/runner/router/router.go:203`). So there is no path today by which A's `/app/` request
reaches B's code-server.

### How it works locally (the pieces)

- **Ingress registry / serve / error:** `internal/router/ingress.go` — registry `byToken`
  (`:49-53`), `lookup` (`:135-140`), `publish` mints `/app/<token>/` + reverse-proxy
  (`:79-110`), `handleIngress` serves it and emits the 410 (`:209-212`), publish RPC handler
  `handleIngressPublish` (`:222-229`). The `/app/` route is mounted only on A-side front ends:
  `internal/router/http.go:99` and `internal/router/unix_listener.go:335`, both dialing the
  **local** registry — no peer awareness anywhere.
- **vscode mints/uses the token:** BE `apps/vscode/be/server.go:133`
  (`PublishIngress(ctx, "unix", sock)` for the code-server socket) → reply
  `{kind:"ready",path}` at `apps/vscode/be/app.go:216-228` → FE forms the iframe src
  `apps/vscode-workbench/fe/src/main.tsx:26-31,108` → `web/lib/src/ingress-frame.tsx:44`
  resolves it **same-origin against the shell (A)** (see the header comment
  `ingress-frame.tsx:13-17`).
- **Relay:** `internal/router/peer.go` — `handlePeerRegister` (`:37-55`, gated to
  `com.wash.remote`), `handlePeerAttach` dials B's ssh-`-L`'d socket and splices a channel
  (`:85-136`). B's endpoint is `--listen-raw`, `cfg.Relayed=true`
  (`internal/runner/router/router.go:82-90,203`).
- `docs/REMOTE.md` never designed ingress. `TODO.md:26-46` is a *different* single-host
  stale-token case — its "auto re-ensure on 401/410" does **not** fix this (re-minting on B
  still yields a B-token A can't resolve).

## Pick an approach (confirm with the user first)

### Approach A — HTTP-over-relay (general, bigger)
Teach A's `handleIngress` that a token belongs to a **remote origin**; tunnel the HTTP/WS
request as a new muxed channel to B's router, which reverse-proxies to B's code-server socket.
Needs: (1) A-side registration of remote ingress tokens, origin-tagged (so `lookup` can route
to a peer instead of a local backend); (2) a new wire message class / channel kind for an
"ingress HTTP request/response" (streamed, respecting the CBOR pitfall and the existing
Bulk/credit framing); (3) B's relayed router (or the B-side remote supervisor) growing an
HTTP-proxy entry point — it currently serves none. Generalises to **all** `/app/` ingress apps
(vscode, music, radio, washamp — the ones the browser-VM note and TODO call out).

### Approach B — forward B's socket to an A-local socket (lighter, vscode-shaped)
Have `com.wash.remote` also `ssh -L` B's code-server unix socket (or B's `/app/` HTTP) to an
A-local socket, then `publish` it into **A's** registry as a *local* unix/tcp backend so A's
existing reverse-proxy (`ingressBackend`) handles it unchanged. Lighter — reuses the proxy —
but requires the remote supervisor to learn each launch's socket + token and register an A-side
ingress on the workbench's behalf (a new coordination message between B's vscode service, the
B/A supervisors, and A's ingress registry), plus lifecycle teardown wiring (unpublish on window
close / SSH drop — see the existing `sdk.OnTerminate` / relay-teardown paths).

**Recommendation to put to the user:** Approach A if remote ingress should be a first-class,
reusable capability (it's the architecturally consistent one — everything else already rides the
muxed relay); Approach B if the goal is just "make remote vscode work" with minimal surface.
State the trade-off and let them choose before building.

## Tests / acceptance

- Two-VM remote harness: `make e2e-remote-vm` (composites B's window into A's browser). Add a
  case: attach B, launch vscode on B, assert the workbench iframe loads (no 410) and renders the
  editor; then kill+restore the SSH tunnel and assert it recovers (ties into the existing
  M2e/reconnect work).
- Unit: ingress `lookup`/routing for a remote-origin token; the new wire message
  encode/decode (Approach A) or the supervisor register/unpublish coordination (Approach B).
- If the two-VM harness can't run in your environment, say so and cover the routing/coordination
  logic with unit tests, and note the manual verification steps.

## Out of scope / stop-and-ask

- Do not invent new message types silently — if the chosen approach needs them, describe them
  and confirm. If mid-build the design proves materially bigger than described, stop and ask.
- The single-host wash-login-restart stale-token self-heal (`TODO.md:26-46`) is a *separate*
  issue; don't fold it in.
