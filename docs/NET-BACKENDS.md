# wash-net backends: netplan (Ubuntu) + ifupdown (Debian)

Status: **in progress** (2026-06-02). Adds two config-layer backends to
`com.wash.netd` alongside the existing NetworkManager and systemd-networkd
ones, so wash reads and owns networking on Ubuntu (netplan) and classic
Debian (`/etc/network/interfaces`).

Related: [NET.md](NET.md) (the UCI model + commit-confirm engine),
`apps/netd/be/` (the backends), `internal/washnet/` (model, codec,
profile renderers).

---

## 0. Why — the bug this fixes

On a stock **Ubuntu/Debian-with-netplan** box, wash's NM backend reads
keyfiles from `/etc/NetworkManager/system-connections` — which is **empty**,
because netplan renders its NM keyfiles into `/run/NetworkManager/system-
connections` (tmpfs, regenerated each boot). So `netd`'s `current` returns
an empty model: wash is blind to the real config. The fix is not a patch on
the NM reader — it's recognising that **netplan is the authority layer** and
reading/owning it directly.

## 1. Decisions (locked)

- **CLI-only.** Both backends shell out (the `runner` seam, like networkd's
  `networkctl`): netplan uses `netplan get` / `netplan generate` /
  `netplan apply`; ifupdown uses `ifup` / `ifdown`. No D-Bus, no
  `netplan try` (it's interactive) — commit-confirm uses the generic
  snapshot-dir + `Verify`(route-probe) + restore-on-rollback that networkd
  already implements.
- **Takeover.** Wash *owns* the box's authoritative config dir, like any
  desktop environment owns networking. `Apply` snapshots the whole dir,
  writes wash's rendered config as the sole authority, reloads; `Rollback`
  restores the snapshot (bringing the user's original files back). Safe
  because the commit-confirm engine auto-reverts on lost connectivity.
- **Two independent backends.** Ubuntu/netplan and Debian/ifupdown share
  nothing but the `backend.Applier` interface — developed together, no
  common "Debian" abstraction.
- **VM-based e2e.** Real Ubuntu (netplan) and Debian (ifupdown) VMs, booted
  via the `washvm-run` harness, with `washnet-read`/apply run inside and
  asserted against live `ip addr`/`ip route` + rollback.

## 2. Layered autodetect (commit 1, DONE)

`chooseBackend` (`apps/netd/be/select.go`) becomes layered, not flat:

```
auto:
  netplan.Active   → netplan     # authority layer: renders TO nm/networkd,
                                 # so it wins even when NM/networkd are active
  nm.Active        → nm
  networkd.Active  → networkd
  ifupdown.Active  → ifupdown
  <available tier, same order>
  → fake
```

netplan wins over an *active* NM/networkd because they're its render
targets — writing them directly is clobbered on the next `netplan apply`.
This single change makes `current` read correctly on netplan boxes (§0).
Detection signals (per backend `Detect()`):
- **netplan** — `/etc/netplan/*.yaml` present + `netplan` binary (Active);
  binary only (Available).
- **ifupdown** — `/etc/network/interfaces` with real stanzas + `ifup`
  binary, and the links aren't NM/networkd-managed (Active).

## 3. netplan backend (Ubuntu)

- **Live:** `netplan get` → merged YAML → `netplanprofile.Parse` → model.
  (Sees the whole config regardless of where keyfiles render.)
- **Render:** model → a single canonical netplan YAML (wash owns it).
- **Apply (takeover):** snapshot `/etc/netplan` → write wash's YAML as the
  sole file (foreign files captured in the snapshot) → `netplan apply`.
- **Verify / Confirm / Rollback:** generic — default-route lock-out probe;
  Confirm drops the snapshot; Rollback restores `/etc/netplan` + `netplan
  apply`.
- **Capabilities:** interface/device/route/rule/wireguard, bridge, vlan,
  wireguard. (No AP / dhcp-server / NAT / zones — out of netplan scope.)
- **Renderer-agnostic:** netplan handles whether NM or networkd is the
  under-renderer; wash doesn't care.

## 4. ifupdown backend (Debian)

- **Live:** parse `/etc/network/interfaces` (+ `interfaces.d/*`) → model.
  Stanza parser (not INI): `auto`/`allow-hotplug` + `iface X inet
  static|dhcp|manual`, `netmask`↔CIDR, `gateway`, `dns-nameservers`,
  `bridge_ports`.
- **Render:** model → `/etc/network/interfaces` stanzas; bridges via
  bridge-utils (`bridge_ports`), VLANs via the `vlan` pkg (`eth0.10`).
- **Apply (takeover):** snapshot file → write → `ifdown`/`ifup` the changed
  interfaces (down-old before up-new). **Rollback:** restore + re-`ifup`.
- **Capabilities (small):** interface/device, bridge; **VLAN conditional**
  on the `vlan` package; **no** wireguard / zones / NAT / AP / dhcp-server /
  policy-routing. The UI greys out unsupported kinds via the existing
  capability gate.
- **DNS caveat:** `dns-nameservers` needs `resolvconf`; surfaced as a
  capability detail.

## 5. Tests

- **Profile round-trip (pure):** `netplanprofile` / `ifupdownprofile` get
  the golden-corpus treatment used by `nmprofile`/`networkdprofile` —
  reuse the same `.uci` scenarios → `.yaml` / `interfaces` goldens, assert
  `Render∘Parse` and `Parse∘Render` identity.
- **Backend appliers (hermetic):** `fakeRunner`-injected Apply/Verify/
  Confirm/Rollback over a `t.TempDir()`, like `networkd/applier_test.go`.
- **VM e2e (§6).**

## 6. VM e2e

New `scripts/build-vm-image-ubuntu.sh` (netplan) and
`scripts/build-vm-image-debian.sh` (ifupdown), modeled on
`build-vm-image-fedora.sh`, each baking the box's native net stack + the
wash binaries. Booted via `washvm-run`; the test, inside the VM:
1. `washnet-read` → assert the model matches the image's known config
   (a static bridge / a dhcp iface), proving Live reads the real box.
2. apply a changed config via netd → assert `ip -j addr`/`ip -j route`
   reflect it.
3. trigger the commit-confirm timeout (or explicit revert) → assert the
   live state reverts.

This is the only layer that proves `netplan apply` / `ifup` actually drive
a real kernel; the profile round-trips cover the pure transform.

## 7. Commit ladder

| # | Commit | Touches |
|---|---|---|
| 1 | `netd: layered backend autodetect (netplan authority; ifupdown) + NET-BACKENDS.md` | `apps/netd/be/select.go`, docs |
| 2 | `washnet/netplanprofile: model↔netplan-YAML render+parse (+golden corpus)` | `internal/washnet/netplanprofile` |
| 3 | `netd/netplan: takeover backend (Live via netplan get; Apply via netplan apply; Detect)` — wire into selection. **Fixes §0.** | `apps/netd/be/netplan`, `app.go` |
| 4 | `washnet/ifupdownprofile: /etc/network/interfaces stanza render+parse (+goldens)` | `internal/washnet/ifupdownprofile` |
| 5 | `netd/ifupdown: takeover backend (file render + ifup/ifdown; snapshot rollback; Detect)` | `apps/netd/be/ifupdown`, `app.go` |
| 6 | `settings/netd: backend dropdown gains netplan + ifupdown (capability-gated)` | `apps/netd/fe`, `app.go` |
| 7 | `vm-images: build-vm-image-ubuntu.sh (netplan) + build-vm-image-debian.sh (ifupdown)` | `scripts` |
| 8 | `e2e(vm): netplan + ifupdown read round-trip + apply + rollback in booted VMs` | `e2e`, `scripts` |
| 9 | `docs: NET.md backend matrix + cross-links` | docs |

1–3 deliver the headline (Ubuntu/Debian-netplan read correctly); 4–5 add
classic Debian; 6 surfaces the choice in the Network panel; 7–8 the VM
e2e; 9 finalizes docs.
