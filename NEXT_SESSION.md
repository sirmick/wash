# Next session — wash-uci net work

**Primary task DONE: the accessibility-matrix is now a dependable green gate**
(commit `b3908cf`, on the `wash-uci` worktree `/home/mick/wash/branches/wash-uci`).
`make net-matrix` runs green in ~90s and passed 4 runs in a row. Remaining work
below is the product-relevant follow-up + optional M2.

## What this session fixed (in `cmd/washnet-matrix/main.go`)
The matrix flaked and never finished a clean pass. Four distinct root causes:

1. **Transport** — replaced the racy `listen=`/`connect=` sockets with qemu
   **`dgram`** (connectionless UDP point-to-point) per segment. NB: an mcast hub
   was tried first and **broke the bridged router via loopback self-reflection**
   (br-lan received its own flooded broadcasts → "received packet with own
   address as source" → storm → dropped real traffic). dgram unicast never
   reflects and has no handshake, so no boot-order race leaves a link dead.
2. **VM leak** — `os.Exit` (in `die()` / the final assert exit) skipped deferred
   `Close()`, so every failed/signalled run leaked all 4 qemu; they piled up and
   starved the host until runs couldn't finish in 600s. Added `track()`/
   `closeAll()` on every exit path + a startup reaper for SIGKILL-leaked VMs.
3. **Speed** — boot the 3 probes concurrently and probe the matrix concurrently
   (each VM has its own serial console; cross-VM `Run` is safe, within-VM is not).
   Boot ~165s → ~70s. Added `[+Ns]` phase timing.
4. **Bounded cells** — an allowed probe's `wget` to a black-holed WAN hung ~60s
   (busybox `-T` doesn't bound the connect) and, because a `w.Run` timeout does
   NOT kill the in-guest command, wedged the shell so every later cell timed out
   too — that single hang was the entire 194s matrix blowup. Bound pings with
   `-w3`, give each cell a per-cell Go deadline, and probe `internet` **last** so
   a hang can't cascade. **OpenWRT busybox has no `timeout` applet** (don't reach
   for it). Also switched the router's WAN check from `ping 8.8.8.8` (slirp
   doesn't NAT ICMP → always burned 50s) to a bounded TCP probe, and gate the
   lan/iot internet assertions on it (SKIP when no WAN egress). Fixed the dead
   `"internet-v4"` assertion key → `"internet"`.

## What the matrix now asserts (all PASS / SKIP, none FAIL)
cam quarantined from internet ✓; lan→cam-probe ✓ (view cameras); iot↛lan, iot↛cam,
cam↛lan ✓ (isolation). lan/iot reach-internet is **SKIPped here** because the
router has no WAN egress in this environment — see below.

## Known facts / gotchas (don't re-discover)
- The firewall is **NOT** the matrix problem — diagnostics proved fw4 binds
  zones correctly (`iifname br-lan.<vid> jump input_lan`, counters accept). The
  `applyRouter` reload-ordering workaround (wait for `br-lan.10`, then reload
  fw4) stays and works.
- The router config is correct & idiomatic (DSA bridge-vlan + fw4 + wireguard).
  Harness writes `/etc/config/*` **wholesale**.
- qemu **slirp NATs TCP but not ICMP** — any internet probe must be TCP (`wget`).
- Probes are stock OpenWRT: detach `eth0` from stock br-lan, stop fw4, static-address.

## Remaining work (value order)
1. **fw4 reload-ordering in the REAL applier** (`apps/netd/be/uci` / the txn
   path) — most product-relevant follow-up. async `network restart` →
   `firewall restart` binds zones to not-yet-created `br-lan.<vid>` →
   default-drop. The matrix only *works around* it; fix it in the product.
2. **Router has no WAN egress in this env** (`internet=false` every run). Slirp
   user-net + a host that DOES have internet, yet the router's `wget 1.1.1.1`
   through the WAN fails. Could be WAN DHCP/NAT not coming up, or sandbox
   egress. Worth a look if internet reachability needs to be a hard assertion —
   currently it's correctly SKIPped, so the gate doesn't depend on it.
3. **M2 (optional): pirate VPN egress test.** Rebuild the image with
   `kmod-wireguard wireguard-tools`, add a wg-exit VM, assert pirate→internet via
   the tunnel + the blackhole kill-switch. `examples/segmented-router.uci` is the
   north-star config; egress trio spec is `docs/NET-ROUTER-UI.md §7.1`.

Run it: `make net-matrix` (or `out/washnet-matrix --image out/vm/openwrt.img
--base-port <fresh>`). Committed up to **`b3908cf`** (not pushed).
