# wash-net — networking / firewall / routing / wifi as a wash app

Status: **design** (2026-05-29). Plan of record for wash's network-management
app: a single declarative model of a box's networking (interfaces, firewall,
DHCP/DNS, wifi, routing, VPN) that renders to whatever the host actually runs —
OpenWRT/UCI, systemd-networkd, or NetworkManager — fronted by a schema-driven
UI and applied through a commit-confirm transaction with a live log terminal.
Tested end-to-end inside real microvms, driven entirely from outside the VM over
an out-of-band serial control plane.

Related: [ARCHITECTURE.md](ARCHITECTURE.md), [WIRE.md](WIRE.md),
[DISPLAY.md](DISPLAY.md) (sibling "native capability as a wash app" track),
and `wash-vm/` (the tinyemu browser-VM whose virtio-console transport and frame
codec this plan reuses host-side).

Staged (see [§11 Commit ladder](#11-commit-ladder)) so the pure, heavily tested
core lands long before any privileged backend touches a kernel, and so the test
harness (`wash-vm/vm`) is itself built and proven early as a reusable dev tool.

---

## 1. Goal & non-goals

**Goal.** wash administers a Linux box's real networking across two postures:

- **Type 1 — router/gateway.** The box sits between WAN and LAN: NAT, firewall
  zones, DHCP+DNS for the LAN, an AP, static/policy routing, WireGuard. wash
  *owns* the box's networking. The distinctive product; natural fit for wash's
  embedded/router targets (mips/arm/riscv64).
- **Type 2 — workstation.** Connect to wifi, bring up a VPN, see/manage
  connections. wash *coexists* with the desktop's existing manager.

One internal model, **UCI-shaped**, is the source of truth. Three renderer
backends (UCI, systemd-networkd, NetworkManager) plus wash's own direct backends
(netlink/nftables/dnsmasq/hostapd/wireguard) apply it. The UI is generated from
the model where it can be and handcrafted where it must be.

**Non-goals (v1).** Dynamic routing (FRR/BGP/OSPF); multi-WAN/failover (mwan3);
QoS/cake; UPnP; PPPoE; IPsec/OpenVPN; mobile broadband (ModemManager); enterprise
802.1X/EAP beyond PSK/SAE; kea/unbound (dnsmasq only). See [§12 Deferred](#12-deferred).

---

## 2. Why this shape (settled decisions)

Worked through in design discussion; recorded so they are not relitigated
without cause.

1. **The model is UCI-shaped.** UCI (OpenWRT's config vocabulary) is the proven
   router config model. Our internal objects map ~1:1 to it, so the OpenWRT
   adapter is nearly a rename, and a future "own-it on plain Alpine" renderer
   would be the same shape.
2. **Target backends, not front-ends.** Distro config tools diverge
   (netplan/NM/nmstate/ifupdown); the *workers* (nftables, dnsmasq, hostapd,
   wpa_supplicant/iwd, wireguard, netlink) are a small shared set present on
   Fedora/Debian/Ubuntu/Alpine alike. We drive the workers.
3. **There is exactly one universal renderer: NetworkManager** (the only one on
   all four distros incl. Alpine). systemd-networkd covers the three systemd
   distros (not Alpine). UCI covers OpenWRT and the *whole* gateway
   (fw4=nftables, dnsmasq, hostapd all UCI-driven). So: **type-2 → NM**;
   **type-1/systemd → networkd** for the IP layer + wash's direct
   nftables/dnsmasq/hostapd; **type-1/router → UCI** (delegates everything).
4. **wash supervises daemons itself** (the netifd model) rather than relying on
   the init system. Collapses systemd-vs-OpenRC: identical code on all distros,
   and Alpine (no systemd) becomes *easier*, not harder.
5. **Validation is wash's job and server-authoritative.** UCI has no real
   schema; validation today is split-brain (LuCI JS + `datatypes.sh` + each
   daemon's parser), late and field-level only. wash owns one schema with the
   *relational* invariants UCI never had, validated in the daemon; the FE is a
   typed client that renders returned diagnostics.
6. **Go, no cgo.** Per [ARCHITECTURE.md](ARCHITECTURE.md): `CGO_ENABLED=0`,
   pure-Go, cross-compiled to embedded targets. Every backend has a pure-Go path
   (netlink/nft/dbus libraries that speak the wire protocol directly) or is a
   supervised child process. **No linking libnl/libnftnl/libnm.** Consequence:
   the wash payload in any test image is three static binaries — copying files,
   not a build environment.
7. **Capabilities drive both gating and delivery.** The `Capabilities` a backend
   advertises gate the runtime UI (grey out the unsupported) *and* define the
   delivery phases. Build the full pure core (model/schema/codec/Advanced UI for
   *every* object) up front; then grow the *product* — recipes + bespoke screens
   — one backend at a time, smallest capability set first (NM → networkd → UCI),
   greying out everything the live backend can't yet do. Every phase is a
   coherent, fully tested product; greying-out is designed, not a stub.
8. **Test (and VM-target manage) over an out-of-band serial plane.** The thing
   under test is the network, so the control plane must not ride it. The
   `wash-vm/vm` tool boots a microvm and a host-side proxy bridges HTTP/WS to the
   guest over **virtio-serial** (reusing wash-vm's multi-port virtio-console
   transport + frame codec). e2e is **microvm-everywhere** — no netns — so every
   distro, init system, and the serial control plane are exercised faithfully.
9. **Commit-confirm triggers on VERIFY-failure / confirm-timeout, not on a
   dropped management link.** More robust in production (a lost socket is a
   symptom, not the trigger) and fully testable over serial. See §7.

---

## 3. Architecture in one picture (production)

```
browser (wash shell, Solid)
  └─ wash-net FE:  generated <ObjectForm> (Advanced)  +  bespoke screens
        (Overview · Devices · Firewall matrix · Wireless · DHCP/DNS · VPN · Diagnostics)
        +  <apply-terminal>  (xterm.js streaming the apply job)
        ▲ typed model + UI descriptor (codegen)     ▼ validate / stage / apply (WS over :11000)
┌──────────────────────────── host ────────────────────────────────────┐
│  wash-router (Go, existing)  — treats wash-net BE as an ordinary app   │
│  wash-net BE (Go, UNPRIVILEGED)  — app surface; proxies to washnetd     │
│        │  unix socket (SO_PEERCRED)                                      │
│  washnetd (Go, PRIVILEGED)  — links the washnet library                 │
│     ├─ washnet/ (PURE) ....... model · codec · validate · recipe · txn · caps │
│     └─ backends (IMPURE, pure-Go libs / child procs):                   │
│         netlink · nftables · wireguard(wgctrl) · dnsmasq(child)          │
│         hostapd(child) · wpa_supplicant|iwd · dbus→NM(godbus)            │
│         uci(write+ubus) · networkd(render+reload)                       │
└────────────────────────────────────────────────────────────────────────┘
```

In **test/VM-target** topology the FE↔router transport swaps to `virtio-console`
and a host-side proxy presents HTTP/WS (see §8). washnetd, the router, and the FE
are otherwise identical to production.

**Privilege boundary.** Only `washnetd` is privileged. The app BE is unprivileged
and proxies over a unix socket (`SO_PEERCRED`); the FE reaches the BE over :11000.

---

## 4. The `washnet` library (pure core)

Pure Go — no I/O, no kernel, no D-Bus — linked into `washnetd`. All the dangerous
logic lives here; ~all tests run here, fast.

```
washnet/
  model/      typed UCI-shaped objects + Config         (source of truth)
  codec/      reflect-based UCI (de)serializer (uci tags)
  validate/   field datatypes (free from types) + RELATIONAL invariants → []Diagnostic
  recipe/     outcome-focused intents → ChangeSet
  txn/        stage · diff · plan · commit-confirm lifecycle
  caps/       Capabilities (which object kinds + features a backend covers)
  backend/    Applier INTERFACE only (the seam; implementations live in washnetd)
```

### 4.1 Object vocabulary (`model/`)

| UCI package | Objects | Screen |
|---|---|---|
| `network` | `Interface` (proto union: static/dhcp/dhcpv6/pppoe/wireguard/none), `Device` (bridge/vlan/macvlan), `Route`, `Rule` (policy), `Globals` | Interfaces, Routing |
| `firewall` | `Defaults`, `Zone`, `Forwarding`, `Redirect` (DNAT/port-fwd), `Rule`, `NAT` (SNAT), `IPSet` | Firewall |
| `dhcp` | `Dnsmasq` (global), `DHCPPool`, `Host` (static lease), `Domain` (A), `CNAME` | DHCP & DNS |
| `wireless` | `WifiDevice` (radio), `WifiIface` (SSID; encryption union: none/psk2/sae/eap) | Wireless |
| `wireguard` | `WGInterface`, `WGPeer` (under `Interface` proto=wireguard) | VPN |

Field types carry datatype validation for free: `netip.Addr/Prefix`, `MAC`,
`Port`, `PortRange`, enums. Sum types (proto, encryption, rule target) are tagged
unions (discriminator + per-variant struct) — repaid in §6 (drives form
conditional-visibility for free).

```go
type Interface struct {
    Name   string       `uci:",name"   ui:"group=general"`
    Proto  Proto        `uci:"proto"   ui:"group=general"`   // tagged union
    Device string       `uci:"device"  ui:"group=general,ref=device"`
    IPAddr netip.Prefix `uci:"ipaddr"  ui:"group=addressing"`
    DNS    []netip.Addr `uci:"dns,list" ui:"group=addressing,advanced"`
}
func (Interface) UCISection() string { return "interface" }
```

### 4.2 Validation — relational invariants are the point

Field datatypes are free (types) or trivial (ranges). The value is the
*relational* set, which UCI/LuCI do **not** check. Initial set (also the §9
layer-1 test table):

- firewall `Rule`/`Forwarding`/`Redirect` references a `Zone` that exists
- `Interface.Device` / `WifiIface.Network` references an object that exists
- `Host` static lease IP inside its interface subnet, outside the dynamic range,
  unique
- no two `Interface`s on overlapping subnets
- `Redirect` dest host has a lease or static host
- `WifiIface` radio band matches `WifiDevice`; channel legal for country
- firewall rule shadowing (later rule unreachable behind earlier drop/accept)
- `Device` bridge port membership disjoint; VLAN ids unique per bridge
- WireGuard `AllowedIPs` non-overlapping across peers on an interface
- capability gating: config needing a kind/feature the active backend can't do →
  diagnostic, never silently dropped (§5)

`func Validate(c Config, caps Capabilities) []Diagnostic`, pure and total. Each
`Diagnostic` carries a **field path** so the FE maps it onto a widget.

### 4.3 Recipes — outcome-focused, multi-object transactions

`func(Config, Params) (ChangeSet, error)`, pure, all-or-nothing. Contract: a
recipe's output always passes `Validate` (property test, §9). Initial set:
`AddGuestNetwork`, `ExposeService`, `ReserveLease`, `IsolateDevice`,
`AddWireGuardPeer` (keypair + QR payload).

### 4.4 Transaction — staging + commit-confirm

```go
type Txn interface {
  Stage(ChangeSet) error
  Diff() Diff                          // human summary ⇄ raw per-backend
  Plan(caps Capabilities) RenderPlan   // ordered backend steps
  Apply(Applier) (RollbackToken, error)// arms VERIFY + auto-revert timer
  Confirm(RollbackToken) error
  Rollback(RollbackToken) error
}
```

Autonomous in washnetd: it snapshots prior state, applies, runs VERIFY, and
auto-reverts on VERIFY-failure or confirm-timeout **without the FE** (§7, §2.9).

---

## 5. Backends & platform profiles

A backend covers a **subset of object kinds** and advertises `Capabilities`. A
**platform profile** composes backends so all kinds are covered; the reconcile
engine routes each object kind to its backend.

| Backend | Covers | Pure-Go path |
|---|---|---|
| `netlink` | Interface/Device/Route/Rule | `vishvananda/netlink`, `mdlayher/netlink` |
| `nftables` | Zone/Forwarding/Redirect/Rule/NAT | `google/nftables` |
| `dnsmasq` | DHCPPool/Host/Domain/CNAME/Dnsmasq | render conf + supervise child |
| `hostapd` | WifiIface (AP) | render conf + supervise child |
| `wireguard` | WGInterface/WGPeer | `wgctrl` |
| `uci` | **everything** (OpenWRT) | write `/etc/config/*` + `ubus` reload |
| `networkd` | Interface/Device/Route | render `.network`/`.netdev` + reload |
| `nm` | Interface/wifi-client/VPN | `godbus/dbus` → NetworkManager |

**Profiles:** *openwrt* (`uci` covers all); *systemd-router* (`networkd` for
link/route + direct nft/dnsmasq/hostapd/wg; Alpine-router uses `netlink` direct);
*nm-workstation* (`nm` for link/wifi/VPN; firewall/AP disabled or direct on demand).

`Capabilities` = union of composed backends' kinds + feature flags (`CanAP`,
`CanDHCPServer`, `CanNAT`, `CanZones`, `CanWireGuard`, `CanVLAN`, …). The UI greys
what the active profile can't do; validation emits diagnostics for over-reach.

---

## 6. Schema-driven UI

`Go structs → codegen → { TS types, UI descriptor JSON }`. One generic renderer
plus bespoke screens. **Widget = f(type); annotations augment.**

- **Type → widget** (free): bool→toggle, `netip.Prefix`→CIDR, `MAC`→mac+device
  picker, enum→select, `[]T`→list editor, Port→number.
- **`ui` tag** supplies only `group`, `order`, `advanced`, `widget` override,
  `ref=<kind>` (cross-object picker).
- **Unions drive conditional visibility for free:** the form switches sub-forms on
  the discriminator (proto=pppoe reveals user/pass) — no hand-written `visibleWhen`.
- **Display strings out of tags:** an i18n catalog keyed by field path; the
  descriptor references message keys.
- **Validation closes server-side:** `<ObjectForm>` submits the staged object to
  washnetd, maps returned diagnostics (field path) onto widgets. The form never
  computes rules — which is *why* it can be generic.

### 6.1 What generates vs what's handcrafted

| Tier | What | Examples |
|---|---|---|
| **A — fully generated** | single object, no cross-refs | DHCPPool, Host, Domain, Route, WGPeer, WifiDevice |
| **B — generated form, bespoke container** | object form inside hand-built layout | Firewall `Rule` (ordered, shadow-detecting list), `Interface` (+ proto union + live-state pane), `WifiIface` |
| **C — fully bespoke** | visual / multi-object / transactional | Overview, Devices list, **Firewall zone matrix**, leases table w/ Reserve, topology, WG QR, **recipe wizards**, Diagnostics, apply terminal |

The generated tier *is* the "Advanced" escape hatch promised on every screen.
Craft is spent on ~6–8 Tier-C screens, the differentiators.

### 6.2 Signature bespoke screens

- **Firewall zone matrix** — rows=src zone, cols=dst zone, cell=accept/reject/
  drop/NAT; *is* `Forwarding` + zone defaults made legible.
- **Devices** — live client list (leases + ARP + conntrack); per-device Reserve /
  Block / port-forward actions invoke recipes.
- **DHCP leases → reservation** — one-click promote a dynamic lease to `Host`.
- **VPN** — generate WG keypair; render peer config as a **QR** for phones.

---

## 7. Apply pipeline + embedded terminal

Apply runs as a streamed **job** in washnetd; the FE renders it in an embedded
**xterm.js** terminal alongside a progress rail and the commit-confirm countdown.

### 7.1 Job phases

```
PLAN ─→ RENDER ─→ PRE-CHECK ─→ APPLY ─→ VERIFY ─→ AWAIT-CONFIRM ─→ COMMITTED
                                          │            └─(timeout)──┐
                                          └─(verify fails)──────────┴─→ REVERTED
```

- **PLAN/RENDER** — compute ordered backend steps; render artifacts (UCI text,
  `.network` files, nft ruleset, dnsmasq/hostapd conf).
- **PRE-CHECK** — run each renderer's own validator as a gate (`fw4 check` /
  `nft -c -f`, `systemd-analyze verify`, `uci import` dry-run). Abort before
  touching anything if a renderer would reject.
- **APPLY** — execute steps; stream raw stdout/stderr of `uci commit` + reload,
  `networkctl reload`, `nft -f`, daemon (re)starts.
- **VERIFY** — read kernel state back via netlink/nft + an active health check
  (e.g. can the box still reach its configured gateway?). **This — not a dropped
  management link — is the auto-revert trigger** (§2.9). On VERIFY-failure: revert.
- **AWAIT-CONFIRM** — stream the countdown; **Keep changes** confirms, else
  auto-revert. Protects the case where VERIFY passed on the box but a human admin
  is now locked out: no confirm → revert. Always armed regardless of whether the
  management plane is in-band (physical box) or out-of-band (VM/serial).

### 7.2 Stream protocol & FE

washnetd emits framed events `{job, seq, phase, level, msg, ts}` interleaved with
raw log bytes, over the BE→router transport (WS in production, the LOG serial
plane in VM-target/test, §8). FE:

- **xterm.js** renders raw log bytes (honest, familiar, reuses wash terminal
  infra) — the gory reload/`nft` output lives here.
- a **progress rail** shows phase chips + countdown + Keep/Discard.
- **Reconnect-safe:** events carry `seq`; on reconnect the FE replays from the
  job buffer and shows the COMMITTED/REVERTED outcome washnetd reached on its own.

---

## 8. `wash-vm/vm` — the test & dev harness

A host-side qemu supervisor + proxy that boots a microvm and presents the guest's
wash over HTTP/WS, bridged over an out-of-band serial plane. Used by tests **and**
as a vite-style dev server; it is also the product connector for the VM-target
shape (wash managing a guest VM's networking). It is the host-side counterpart of
`wash-vm`'s `tinyemu-bridge.ts` (which bridges a *wasm* VM's serial to the shell
in-browser) — same role, **shared frame codec**, different host.

### 8.1 Why all-serial is correct *and* faithful

- The network is the system under test, so the control plane must be out-of-band.
  **virtio-serial** is a reliable, flow-controlled, shared-memory virtqueue stream
  (not a UART — no baud/loss/flow issues), physically independent of the guest
  netstack. It survives any network teardown — the only way to *observe* the
  commit-confirm/lockout path.
- The proxy **presents HTTP/WS**, so the FE's real transport code is exercised;
  serial is just the backhaul. The browser can't tell.
- Commit-confirm triggers on **VERIFY/timeout** (§2.9), not link-drop, so the
  auto-revert mechanism is fully testable over serial.
- The only thing not exercised is the real-network WS path (TLS, listener,
  tailscale) — that is **wash-router's** concern, covered by wash's existing WS
  e2e, not wash-net's.

### 8.2 The tool

Library-first (tests are the demanding consumer), CLI as a thin shell:

```go
h, _ := vm.Launch(vm.Opts{Image: "wash-alpine.qcow2", Kernel: kImg, Mem: "512M"})
defer h.Close()
h.WaitReady(ctx)                            // serial handshake; washnetd up
url := h.URL                                // Playwright points here (real HTTP/WS)
out, _ := h.Ctl.Exec("nft list ruleset")    // OUT-OF-BAND assertion (ctl plane)
h.Snapshot("ready"); h.Restore("ready")     // per-test reset (§8.5)
```

```bash
wash-vm/vm run --image wash-alpine.qcow2 --port 8080 -- -smp 2 -m 512
# → "ready at http://localhost:8080"; guest log streams to the terminal
```

- **Embedded proxy** (a goroutine, not a separate process) → one process tree.
- **Arg passthrough via `--`:** promote `--image/--kernel/--port/--mem/--snapshot`;
  everything after `--` is raw qemu, overriding wash defaults (microvm machine,
  direct-kernel, virtio-serial multiport, virtio-net, kvm, `-nographic`).
- **Named virtio-serial ports**, not `hvc` ordinals: `virtserialport,name=wash.data
  |wash.log|wash.ctl` → `/dev/virtio-ports/wash.*` in-guest, bound by name.
- **Two layers of value:** *generic* (boot any image, browser console over serial)
  and *wash-aware* (also bridge the router WS + log planes when the guest is wash).

### 8.3 The proxy (what crosses which plane)

```
  browser / Playwright
     │  HTTP (assets, host-served)  +  WS (router wire)  +  /logs (apply terminal)
     ▼
  ┌─ proxy (embedded in wash-vm/vm) ────────────────────────────┐
  │  • serves the wash-net FE bundle from host (instant, vite-like)│
  │  • /ws   ⇄ guest DATA plane  (wash.data) ─┐                    │
  │  • /logs ⇄ guest LOG  plane  (wash.log)  ─┤ virtio-serial      │
  │  • /ctl  exec/observe/snapshot (wash.ctl)─┘ chardev unix sockets│
  │  • owns qemu lifecycle (boot · reset · snapshot/restore via QMP)│
  └──────────────────────────┬─────────────────────────────────────┘
                          qemu microvm: washnetd + wash-router + wash-net
```

It is a frame transcoder: the existing `transport=virtio-console` codec guest-side,
the wash-router WS protocol browser-side. FE assets are host-served — only the
router *wire* and logs enter the guest.

### 8.4 Image pipeline

Payload = three static binaries (§2.6). Work is OS assembly + init wiring + disk
format. Per-distro build script (`scripts/build-vm-image-<distro>.sh`); image
*building* is file ops only (**no kvm needed** — runs anywhere in CI); only
*running* wants nested virt.

**Alpine (Phase A/B target) — `apk --root`:**

```bash
R=$(mktemp -d)
apk --root "$R" --initdb -X "$MIRROR/main" -X "$MIRROR/community" -U --allow-untrusted add \
    alpine-base busybox openrc \
    iproute2 nftables dnsmasq hostapd wireguard-tools networkmanager \
    linux-virt                                   # VM-optimized kernel + modules
install -m755 out/{washnetd,wash-router,wash-net} "$R/usr/bin/"
install scripts/openrc/washnetd "$R/etc/init.d/" && ln -s … default/washnetd
# kernel for direct-boot: $R/boot/vmlinuz-virt (+ $R/lib/modules/* incl. mac80211_hwsim)
# disk:
#   qcow2 (rw, savevm-native):  tar -C $R -cf - . | virt-make-fs --format=qcow2 --type=ext4 - wash-alpine.qcow2
#   or squashfs (immutable):    mksquashfs $R rootfs.squashfs   (boot ro + tmpfs overlay)
```

(`alpine-make-vm-image` is a turnkey wrapper of the same; hand-rolled `apk --root`
once we need control.) Combine **squashfs OS layer + small qcow2 overlay** for
minimal-and-snapshottable, or one qcow2 for simplicity.

**Cross-distro — same payload, different assembler + init** (the 3-case init shim):

| Phase | Distro | Build tool | Init |
|---|---|---|---|
| A/B | **Alpine** | `apk --root` | OpenRC service |
| C | minimal **systemd** distro | **mkosi** (declarative, qcow2 out, systemd-native) | `.service` |
| D | **OpenWRT** | **Image Builder** (`make image PACKAGES=… FILES=…`) / released qemu image | procd `/etc/init.d` |

### 8.5 Dev vs CI

- **Dev:** don't bake. **9p-share** the host build dir (`-virtfs local,path=$PWD/out,
  mount_tag=washbin,security_model=none`; guest `mount -t 9p -o trans=virtio`).
  Loop = `go build` on host → `rc-service washnetd restart` over the ctl plane →
  reload browser. Seconds, no image step (the VM equivalent of vite-from-source).
- **CI:** baked, content-hashed qcow2 (hermetic, reproducible, version-pinned).
  **Layer** with qcow2 backing files: `base-<distro>.qcow2` (OS, cached by package
  hash) + a thin wash overlay (binaries + init); rebuild = regenerate the overlay.
- **Snapshot/restore:** `Snapshot`/`Restore` are in the handle **API from day one**
  (harness written against them, never changes); implementation starts as
  boot-fresh and upgrades to QMP `savevm`/`loadvm` (boot once → snapshot "ready" →
  `loadvm` per test in ~ms, byte-identical baseline) when boots get expensive.
  Caveats verified then: microvm+savevm device-model quirks (fallback q35 or warm
  reboot); multi-VM topologies snapshot as a coordinated set.

Bonus: microvm e2e is self-contained — no host child/inotify orphans like the
fs.watch-based fm/session e2e (cf. the orphan-accumulation hazard), and teardown
is killing one qemu.

---

## 9. Testing — unit (pure, host, every commit)

Pure core ⇒ most tests are fast and kernel-free.

**`washnet` library:**
1. **Validation golden tables** — every relational invariant (§4.2): accept case
   + violating case asserting the exact diagnostic + field path. *Center of gravity.*
2. **Recipe soundness** (property): `∀ valid c, recipe r → Validate(r(c)).ok`.
3. **Recipe transactionality** — ChangeSet all-or-nothing.
4. **UCI round-trip** (property): `Parse(Render(c)) == c`.
5. **Render fidelity** (golden): `model → UCI` diffed against real OpenWRT UCI.
6. **Capability gating** — over-reach → diagnostic, never silent drop.
7. **Commit-confirm** with a **fake Applier**: VERIFY-fail → auto-revert restores
   base; confirm → persists; confirm-timeout → revert.
8. **Diff minimality** — `Diff(c,c)==∅`; minimal/stable.

**Codegen:** golden tests on emitted TS types + UI descriptor (labels resolve via
i18n keys; unions emit discriminators; `ref` widgets resolve).

**FE component:** `<ObjectForm>` units — widget-from-type, union switching,
diagnostic→field mapping, advanced toggle.

## 10. Testing — integration & e2e (microvm-everywhere, via `wash-vm/vm`)

Two tiers only: **pure** (§9, host) and **microvm** (everything impure). No netns
— a shared host kernel can't give real PID1, cross-distro, systemd-networkd,
OpenWRT, or the serial control plane.

**Renderer-as-oracle** (CI): every config that validates renders to output the
real renderer accepts (`fw4 check`, `nft -c`, `networkd verify`, `uci import`).
The lib never emits config a renderer rejects.

**Type-1 truth test** (multi-VM via `wash-vm/vm`, `-netdev socket` L2 — separate
kernels, far more honest than veth on one kernel):

```
  [WAN vm]──sockL2──[ROUTER vm: washnetd]──sockL2──[CLIENT vm]
       each vm has its own out-of-band ctl serial plane
  assert (over ctl): client lease in-pool · NAT out works ·
                     guest→lan dropped by zone matrix · nft == declared model
```

**Wifi:** `mac80211_hwsim` in the router vm → "AP comes up + STA associates";
`wmediumd` if cross-VM wifi is ever needed.

**Lockout/commit-confirm truth test:** drive a WAN-breaking apply; assert VERIFY
fails in-guest and REVERTED fires autonomously, witnessed over the ctl plane;
prior kernel state restored.

**e2e — black-box, driven entirely from outside the VM** (this is the §11
Phase-B exit): Playwright runs on the host against the proxy's HTTP/WS; assertions
read guest kernel state over the ctl plane; the apply terminal streams over the
log plane. The full stack — FE + router + washnetd + NM + real kernel — runs
inside a real Alpine microvm. Mirrors wash's e2e pattern (test driver + Playwright
FE + router-log BE assertions), with the router log arriving on the log serial
plane instead of :11000.

---

## 11. Commit ladder

Each commit is independently green before the next. `Capabilities` is the
throughline (§2.7): full pure core first, then product grows NM → networkd → UCI.
Detail is densest through the **Phase-B target** (Alpine + NM + full advanced UI,
externally tested by `wash-vm/vm`); C/D are sketched.

### Phase A — full pure core (no kernel, backend-agnostic)

- **A1 — scaffold + first round-trip.** `wash-net/` tree; `washnet/model/` with
  `Config` + `Interface`,`Zone`; `codec/` UCI ser/deser for those.
  *Test:* round-trip property. *Commit gate:* `Parse(Render(c))==c` green.
- **A2 — full vocabulary.** All network/firewall/dhcp/wireless/wireguard objects;
  field types; unions. *Test:* round-trip all + field datatypes + OpenWRT-fidelity
  goldens. *Commit gate:* goldens green.
- **A3 — caps + field validation.** `caps/`; `validate/` field level.
  *Commit gate:* field-validation tests green.
- **A4 — relational invariants.** `validate/` relational set + golden tables.
  *Commit gate:* every invariant has accept+violate test, green.
- **A5 — codegen.** Go→TS types + UI descriptor + i18n scaffold.
  *Test:* codegen goldens. *Commit gate:* descriptor matches fixtures.
- **A6 — generic `<ObjectForm>`.** widget-from-type, unions, refs, advanced.
  *Test:* `<ObjectForm>` units + Tier-A edit against an in-memory `Config` (no
  backend). *Commit gate:* every object has a working Advanced editor offline.
- *End A:* full schema + Advanced UI + validation, pure, no kernel.

### Phase B — Alpine microvm + NM, the shippable type-2 target

- **B0 — `wash-vm/vm` harness + base Alpine image.** `scripts/build-vm-image-
  alpine.sh` (base, no wash yet); `vm.Launch` + named-serial wiring + embedded
  proxy serving a static page + `Ctl.Exec` over the ctl plane; CLI `run`.
  *Test:* boot base Alpine, proxy serves, `Ctl.Exec("uname -a")` returns over
  serial. *Commit gate:* harness boots a real VM and talks to it out-of-band.
- **B1 — wash in the VM, FE over the proxy.** Bake wash (washnetd stub + router +
  FE) into the image; OpenRC service; 9p dev-share mode. Proxy bridges the router
  WS (data plane) so the **FE loads via the proxy and reaches the in-guest router**.
  *Test:* external Playwright loads the FE through the proxy and round-trips a
  model edit to washnetd. *Commit gate:* the external-test path works end-to-end.
- **B2 — transaction + commit-confirm.** `txn/` stage/diff/plan; VERIFY +
  autonomous auto-revert; wire to a fake Applier in washnetd.
  *Test:* fake-Applier commit-confirm (VERIFY-fail→revert; confirm→persist;
  timeout→revert) — unit (§9.7) **and** one in-VM run over serial.
  *Commit gate:* both green.
- **B3 — apply terminal.** washnetd streamed job (phase events + raw logs) over
  the log plane; FE `<apply-terminal>` (xterm + progress rail + countdown);
  reconnect-safe replay. *Test:* external e2e — Apply shows PLAN→…→COMMITTED in
  the xterm, countdown renders; BE job-phase sequence asserted over the log plane.
  *Commit gate:* external apply-terminal e2e green.
- **B4 — NM backend.** `nm` backend (`godbus`→NetworkManager) for NM-covered
  kinds (Interface/wifi-client/VPN); capability gating wired so non-NM kinds emit
  diagnostics / grey out. *Test:* in the Alpine VM (NM running), apply an
  interface + a WireGuard peer; assert real state over the ctl plane (`nmcli`,
  `ip`, `wg`); renderer-as-oracle for NM. *Commit gate:* NM apply verified in-VM.
- **B5 — NM recipes + bespoke screens.** Wifi-connect, WG up/down + QR, Devices/
  connections view; router features (firewall matrix, DHCP server, AP, zones)
  **greyed** per caps. *Test:* external e2e — connect-wifi recipe, VPN up; gating
  e2e (router screens read-only/hidden). *Commit gate:* type-2 journeys green.
- **B6 — Phase-B exit: full external e2e in CI.** Alpine microvm + NM + the full
  Phase-A Advanced UI/schema/validation, driven **entirely from outside the VM**
  via `wash-vm/vm` (Playwright→proxy HTTP/WS; assertions over ctl; logs over log
  plane); snapshot/restore per test. *Commit gate:* the suite is green in CI on a
  kvm runner. **A complete, tested, shippable type-2 product.**

### Phase C — systemd-networkd router (type-1 on systemd distros)

`networkd` (render+reload) + direct backends (`netlink`,`nftables`,`dnsmasq`,
`hostapd`,`wireguard`); platform-profile composition; un-grey router screens as
caps land (firewall zone matrix, DHCP pools + leases→Reserve, AddGuestNetwork,
wireless/AP, static routing). New image: minimal systemd distro via mkosi.
*Proven by:* multi-VM type-1 truth test, mac80211_hwsim wifi, renderer-as-oracle
(`nft -c`,`networkd verify`), lockout truth test — all via `wash-vm/vm`.
*Exit:* complete, tested type-1 systemd-router product.

### Phase D — OpenWRT UCI mode (full type-1)

`uci` backend (write `/etc/config/*` + ubus reload); all caps enabled, nothing
greyed. New image: OpenWRT via Image Builder. *Proven by:* OpenWRT-VM apply,
renderer-as-oracle (`fw4 check`,`uci import`), full type-1 truth test on real
OpenWRT — via `wash-vm/vm`. *Exit:* full router product on OpenWRT.

---

## 12. Deferred

Dynamic routing (FRR/bird) · multi-WAN (mwan3) · QoS (tc/cake) · UPnP · PPPoE ·
IPsec/OpenVPN · ModemManager · 802.1X/EAP · kea/unbound · the "own-it on plain
Alpine" pure-netlink renderer (same shape as the UCI adapter; build when a
non-systemd non-OpenWRT router target demands it).

## 13. Resolved / open

**Resolved.**
- *Posture & order* — full pure core, then product NM → networkd → UCI, smallest
  caps first, greying the rest (§2.7, §11).
- *Test architecture* — all-serial, microvm-everywhere via `wash-vm/vm`; no netns
  (§2.8, §8, §10).
- *Commit-confirm trigger* — VERIFY/confirm-timeout, not link-drop (§2.9, §7).
- *Image* — Alpine first via `apk --root`; qcow2 (savevm) ± squashfs overlay; 9p
  dev-share; baked+layered for CI (§8.4–8.5).
- *AI* — deferred from v1; model + diagnostics + recipe layers kept introspectable
  so natural-language rules and "why can't X reach Y" can attach later.

**Open.** None blocking A1.
