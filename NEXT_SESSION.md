# Next session — wash-uci net work

**Continue the wash-uci net work — harden the integration test into a real green gate.**

Context: on the `wash-uci` worktree (`/home/mick/wash/branches/wash-uci`). The router
net app is feature-complete and **Advanced-free** (fabric table + segments + firewall
matrix + the per-segment **Egress: WAN/VPN** dropdown). The accessibility-matrix
integration test (`cmd/washnet-matrix`) — wash's analogue of the Proxmox `test-net.py`
(repo root) — boots a router microVM + a probe per segment and asserts the isolation
matrix. It *works on a good run* (cam quarantined incl. internet, iot/cam ↛ lan,
lan → cam ✓) but is **flaky run-to-run**.

## Primary task — make the matrix a dependable green gate
1. **Fix the transport flakiness.** The qemu point-to-point socket links
   (`listen=`/`connect=`) don't reliably establish for all 3 probes — bad runs show
   some probes' links dead (asymmetric all-✗ per probe). Switch to a more robust L2:
   a dedicated **hub** (one shared mcast group is reliable — `net-demo` uses it) with
   the probes **VLAN-tagged on a trunk** so isolation still holds (needs `kmod-8021q`
   in the image — one genuine rebuild), OR add carrier-verify-and-reconnect. Pick
   whichever's cleaner.
2. **Always clean up qemu** between runs — orphan VMs leak (their `defer Close` doesn't
   run on kill) and hold listen ports / OOM the host. Kill `qemu-system` parents before
   each run; use a fresh `--base-port`.
3. Then make it a `make` target + assert green reliably (3 runs in a row).

## Known facts / gotchas (don't re-discover)
- The router config is correct & idiomatic (DSA bridge-vlan + fw4 + wireguard), applies
  stand-alone via stock netifd/fw4. The harness now writes `/etc/config/*` **wholesale**.
- **fw4 reload-ordering is a real wash applier bug**: async `network restart` →
  `firewall restart` binds zones to not-yet-created `br-lan.<vid>` → default-drop. The
  harness waits for `br-lan.10` then reloads fw4. **Consider fixing this in the real
  wash UCI applier** (`apps/netd/be/uci` / the txn path) — most product-relevant follow-up.
- qemu **slirp NATs TCP but not ICMP** — the internet probe must be TCP
  (it uses `wget http://1.1.1.1`).
- Probes are stock OpenWRT: detach `eth0` from stock br-lan, stop their fw4,
  static-address them.

## Then M2 (optional): the pirate VPN egress test
Rebuild the image with `kmod-wireguard wireguard-tools`, add a wg-exit VM, assert
pirate→internet via the tunnel + the blackhole kill-switch. `examples/segmented-router.uci`
is the north-star config; the egress trio spec is `docs/NET-ROUTER-UI.md §7.1`.

Everything's committed/pushed up to **`d922b6f`**. Start by reading
`cmd/washnet-matrix/main.go` and running it once to see the current flaky behavior.
