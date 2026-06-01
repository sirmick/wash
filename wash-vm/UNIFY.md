# Unifying the two wash VMs (browser RISC-V + wemu QEMU)

Status: in progress on branch `vm-unify`. Goal: the in-browser WASM VM and
the `wash-vm/vm` ("wemu") QEMU microvm share one mechanism — same in-guest
server, same wire, same FE-above-the-channel, same login — with only the
channel *substrate* differing, expressed as a matched pair of adapters.

## The seam

Both surfaces are: `browser ⟷ channel (wash wire frames) ⟷ in-guest server`.
Everything above the channel and below it is shared; only the substrate that
*carries* the channel differs by nature (a WASM RISC-V emulator driven by JS in
the page vs. a host `qemu` process with a Go proxy). That difference is
irreducible — you can't run qemu in a browser tab, and the static WASM deploy
has no host process at all. So we don't try to share it; we pin it behind two
fixed contracts and share everything else.

```
SHARED CORES (single-sourced)
  internal/wire .................. Go frame codec (whole repo)
  wash-vm/web/channel ............ TS frame codec + Channel iface
  wash-vm/web/boot ............... FE bootstrap: login client + asset.read
                                   shell loader + view chrome (Channel-only)
  wash-vm/guest .................. in-guest login front: auth → fork wash-router
  out/wash (wash-router) ......... unchanged except --session-preopened

THE PAIR (one interface, two impls)
  wash-vm/browser  (today web/)    wash-vm/wemu  (today vm/)
  ├─ tinyemu WASM + RISC-V image   ├─ qemu supervisor (vm.go)
  ├─ virtio-console substrate      ├─ transparent proxy (proxy.go) + Alpine image
  └─ Channel impl: virtio-console  └─ Channel impl: WebSocket
```

### Seam A — FE `Channel`

```ts
interface Channel { send(bytes: Uint8Array): void; onBytes(cb: (b: Uint8Array) => void): () => void; close(): void; }
```

Carries wash wire frames. Browser impl: virtio-console over tinyemu. Wemu impl:
a WebSocket to `proxy.go`. The bootstrap (`wash-vm/web/boot`) depends only on
this interface.

### Seam B — guest device

A raw blocking fd carrying the same wire frames, handed to the login front and
then to `wash-router`. Browser: `/dev/vport0p0` (the supervisor pre-opens it as
`fd:3`). Wemu: `/dev/vport0p1`. The shared front takes the device/fd via a flag.

## Login: basic protocol, suid/fork to wash-router

Single user, serial, root/diagnostic tool — so NO multi-session registry, NO
picker, NO SCM_RIGHTS. The host proxies serve a small static `login.html`
(the one asset deliberately NOT delivered over the channel — it's fixed and
app-agnostic). The login handshake is end-to-end **browser ⟷ in-guest front**
through the *transparent* proxy; the proxy never parses it.

Protocol (JSON ctrl frames on channel 0, same envelope as `asset.read`):

```
FE  → front : {"t":"login","user":"…","pass":"…"}
front → FE  : {"t":"login.ok"}                 // or {"t":"login.err","msg":"…"}
```

On `login.ok` the front (running as root) forks
`wash-router --transport=fd:3 --session-preopened` with the resolved uid/gid
(`SysProcAttr.Credential`) and the channel fd as fd 3 (`ExtraFiles`), then
waits. `wash-router` runs the normal shell — serves `shell.js` over `asset.read`
and pushes the catalog/apps. On session end the front loops for the next login.

### Why `--session-preopened`

`wash-router`'s session splitter (`router.go:478`) starts a viewer on a
`SessionOpen` frame and ends it on `SessionClose`. The proxy emits those per
browser. But the login front must read the *login* frame, and `SessionOpen`
arrives first on the wire — so the front consumes it. `--session-preopened`
tells the router "the leading SessionOpen was already consumed: run ONE
HandleShell directly on the fd and return on SessionClose/EOF" (instead of the
splitter loop). The front owns the per-viewer loop; the router owns one shell
per fork. The login frame is read with `wire.DecodeFrame` (`io.ReadFull`, no
buffering) so no bytes are stranded when the fd is handed to the child.

## Commit ladder

1. **UNIFY.md** (this doc) + task list.
2. **`wash-vm/guest` front** — `Serve(ctx, transport, auth, spawnRouter)` login
   loop + unit tests (in-memory pipe, stub spawn). Reuses `internal/login`.
3. **`wash-router --session-preopened`** — one-shot preopened session.
4. **Multicall/cmd wiring** — open device raw-blocking, build Authenticator,
   real SpawnRouter (fork as uid + ExtraFiles).
5. **Shared FE** — `Channel` iface + frame codec (collapse the
   `shell-bootstrap.ts` ⇄ `shell/src/virtio.ts` dup) + login client + boot.
6. **Wire both images + proxies** — one launcher invoking the front (device via
   flag); `login.html` static from both proxies; proxies stay dumb. e2e green
   on wemu first, then RISC-V.
7. (later) **rename** `vm/`→`wemu`, `web/`→`browser` once green.

Every commit gates on a green build (MAKE_OK).
