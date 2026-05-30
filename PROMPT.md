# wash-net — continuation prompt (handoff for after reboot)

You are resuming work on **wash-net**, wash's networking / firewall / routing /
wifi app. The full design + commit ladder is the plan of record in
[`docs/NET.md`](docs/NET.md). A persistent memory note (`wash-net-plan`) also
tracks this. Read `docs/NET.md` first; this file is just the resume state.

## Where this lives

- Worktree: `/home/mick/wash/branches/wash-net` (git worktree, branch `wash-net`,
  based on `main`).
- Do all work here. `out/` holds VM build artifacts and is git-ignored.

## What's DONE and green (commits, newest first)

```
75f47cd wash-vm/vm: HTTP/WS proxy — the inside-out wash-vm (rest of B0/B1)
3c6f9a4 wash-vm/vm: boot an Alpine microvm + Ctl.Exec over serial (B0 core)
34b8862 wash-net: outcome-focused recipes (B5, pure half)
85b2b39 wash-net: change/diff + Applier seam + commit-confirm engine (B2, pure half)
6d6aed2 wash-net: generic ObjectForm (Advanced UI) + codegen command (A6)
86864be wash-net: UI codegen — descriptor + TS types + i18n scaffold (A5)
941455f wash-net: relational validation invariants (A4)
c3c7613 wash-net: capabilities + field-level validation (A3)
4c39e0d wash-net: full UCI object vocabulary + tagged unions (A2)
390013e wash-net: model.Config + UCI codec round-trip (A1)
(15f7888 docs: NET.md — on main)
```

- **Phase A complete** (pure Go core): `internal/washnet/{model,codec,validate,
  caps,uigen,change,backend,txn,recipe}` + `cmd/washnet-uigen` + `apps/net/fe`
  (ObjectForm + pure model). Validation (field + relational), commit-confirm
  engine, recipes (soundness property), UI codegen — all unit-tested.
- **B0 harness complete & proven on real hardware**: `wash-vm/vm` boots an
  Alpine 3.23 microvm under qemu+kvm in ~1.1s; `Ctl.Exec` runs commands in-guest
  over an out-of-band serial control plane; the proxy serves HTTP + bridges a
  browser WebSocket ⟷ guest data plane (a wash wire frame round-trips, -race
  clean). Guest agent (`cmd/washvm-agent`) currently **echoes** frames as a
  router stand-in.

## THE DECISION just made (drives B1) — see docs/NET.md §2.10, §8.3

**The VM serves everything; the proxy becomes a transparent tunnel.** A real
wash box serves its UI from the box, so the faithful VM-target does too: the
in-guest `wash-router` serves the FE *and* the wire over HTTP/WS, and the proxy
just exposes that to a browser over the serial link (TCP/HTTP+WS-over-serial).
The host serves only a **minimal chrome** (a few KB JS/HTML) mirroring the
existing in-browser `wash-vm` demo UI: tabs for **kernel log** (LOG plane),
a **terminal** (a guest shell), and the **wash tab** (the real wash UI, loaded
*from the VM*). The browser loads the wash UI from the running VM, not a host
bundle. The current proxy serves a host `Static` dir — that is the thing to
replace.

## Environment facts (this machine)

- qemu 8.2.2 (`qemu-system-x86_64`, microvm machine available), `/dev/kvm`
  present, user **in the `kvm` group**. Alpine CDN reachable.
- **No** `apk`, `virt-make-fs`, or `guestfish` on the host. Have `mksquashfs`,
  `cpio`, `go` 1.25, `gzip`. Host `/boot` kernels are NOT readable → use Alpine's
  `vmlinuz-virt` (the build script downloads it).
- FE: Solid + vite; unit tests are **`node:test` via `tsx`** (`npx tsx --test`),
  NOT vitest. Existing examples: `web/shell/src/*.test.ts`.

## Build & test

```sh
cd /home/mick/wash/branches/wash-net

# pure Go core (fast, no kernel):
go test ./internal/washnet/...

# FE pure model (node:test):
npx --yes tsx@4 --test apps/net/fe/src/objectform-model.test.ts

# build the VM image (downloads Alpine assets, builds static agent, packs initramfs):
./scripts/build-vm-image-alpine.sh        # -> out/vm/{vmlinuz,initramfs.gz}

# VM integration tests (need qemu+kvm+built image; auto-skip otherwise):
go test ./wash-vm/vm/ -run 'TestBootAndExec|TestProxyServesAndBridges' -v

# regenerate FE codegen artifacts after model changes:
go run ./cmd/washnet-uigen -out apps/net/fe/src/generated
```

## Hard-won gotchas (already fixed; don't regress)

- **Guest serial MUST be raw** (`golang.org/x/sys/unix` termios in the agent).
  Cooked/canonical mode line-buffers and CR/LF-mangles binary frames in both
  directions — frames never arrive.
- **Startup FIFO race**: host→guest hits a 16-byte UART FIFO that overflows if
  the host writes before the guest opens the port. Fix: the guest sends a
  `hello` first (guest→host is buffered by qemu); the host waits to receive it
  before sending anything.
- Wire frames: `internal/wire` (8-byte header: flags + 3-byte channel + 4-byte
  BE length + payload). One frame per WS message, matching `web/shell/src/
  virtio.ts` `VirtioConsoleSocket`.

## NEXT STEPS (B1, then B4, B6)

1. **B1 — VM serves everything (the decision above):**
   - Extend `scripts/build-vm-image-alpine.sh` to bake real `wash-router` +
     `washnetd` (+ built FE bundle) into the image. They're static no-cgo
     binaries → cross-build + copy + an init hook (initramfs `/init`, or move to
     an OpenRC service / a real rootfs). Note: current image is an *initramfs*
     (ramdisk); baking a web server may want a small writable rootfs or just run
     from the initramfs.
   - Build `wash-router` for the guest; have it serve the FE + wire on a guest
     port (or directly on the data serial plane via its `transport=virtio-console`
     support).
   - Rework `wash-vm/vm/proxy.go` from asset-server → **transparent tunnel**:
     forward browser HTTP/WS to the guest's server over the serial data plane
     (TCP-over-serial). Drop `ProxyOpts.Static` (or repurpose it to serve ONLY
     the minimal chrome).
   - Build the **minimal host chrome** (kernel-log / term / wash tabs). Mirror
     `wash-vm/web/src/tinyemu-bridge.ts` + the demo chrome, but pointed at the
     proxy's planes instead of an in-browser tinyemu. The wash tab embeds/loads
     the wash UI served by the VM.
   - `washnetd` itself still needs writing (the privileged daemon that links the
     `internal/washnet` library + a backend + serves the app). For B1 it can be a
     stub that serves the Advanced UI over the model + a fake/echo backend; the
     real NM backend is B4.
   - Gate: browser loads the wash tab *from the VM* through the tunnel and
     round-trips a model edit to in-guest washnetd.

2. **B4 — NM backend:** `apk add networkmanager` into the image; implement the
   `nm` backend (`godbus/dbus` → NetworkManager) satisfying `backend.Applier`;
   wire it into washnetd; capability-gate to NM-supported features.

3. **B6 — external e2e:** Playwright (or Go) drives the whole stack from outside
   the VM via the proxy; assertions over the ctl plane; snapshot/restore per test.

## Working style notes

- Each rung is a commit; keep tests green before moving on.
- Discuss design in open prose (no multiple-choice prompts).
- Don't claim something works without verifying it; VM tests must actually boot.
