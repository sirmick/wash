# Router UI plan (type-1 / UCI)

The plan for wash's **router-plane** UI — the screens UCI unlocks (firewall, DHCP/DNS
server, wireless AP). It's the second half of the UCI work: the backend
(`apps/netd/be/uci`) is a near-mechanical port because the model is UCI-shaped;
the UI is the real design effort. This doc fixes the shape so the build is
forms-and-wiring, not open questions.

**Revision 2026-06-07 — tabbed architecture.** The router plane is reorganised from
one long scroll into **capability-gated tabs** (Interfaces · Networks · Firewall ·
Hosts & DNS · Advanced), built on an explicit **carrier (L2) vs segment (L3)
split**, with **create/delete as closure operations**. See §4 (where it lives),
§4b (tab architecture) and §8c (create/delete closure). The §7 screen specs stand
— they become the *content* of the tabs.

Companion to `docs/NET.md` (§11 Phase D) and `docs/NET-BACKENDS.md`.

## 1. The canonical config we're designing for

A typical prosumer/SMB router, used as the north star for every UI decision:

- **Multiple VLANs**, each a network segment with its own subnet + **DHCP/DNS**.
- **DHCP leases register in DNS** — `printer` resolves as `printer.lan`.
- **A WAN uplink** (occasionally more, but no smart multi-WAN *routing*).
- **Per-segment egress** — a segment routes out the normal WAN **or** out a **VPN
  tunnel** (WireGuard) on its own VLAN. This is the *only* "multi-WAN" wash targets:
  not failover/balance across physical links (mwan3), but "send the privacy VLAN out
  the VPN." Reuses the WireGuard interface (done) + policy routing.
- **Firewall per VLAN** — each VLAN is a zone; inter-VLAN access is **selectively
  allowed, granularly, or not** (default: isolated).
- **NAT** (masquerade out the WAN).
- **Hairpin NAT** / **split-horizon DNS** for reaching internal services by name.
- **Static hosts** (DHCP reservations) and **static DNS entries**.

Single shared DNS resolver across all VLANs is fine — per-VLAN DNS views are **not**
wanted (confirmed).

Everything below maps back to making *this* config easy and safe to express.

## 2. Status & prerequisites

- **UCI backend — done + proven live (`wash-uci`):** `Render`=`codec.Render`,
  `Apply` writes `/etc/config/{network,firewall,dhcp,wireless}` + reloads, `Live`
  reads→`codec.Parse`, snapshot rollback, lock-out `Verify`, OpenWRT `Detect`,
  `Capabilities`=`caps.Full()`, wired into `backendsel`.
- **Proven end-to-end against real OpenWRT** (`wash-vm/vm`, qemu socket-mcast
  hub, no host bridge/root): **M0** read+apply a live gateway · **M1** multi-VM
  L2/VLAN · **M2** router serves a real DHCP lease · **M3** two-VLAN router with
  per-VLAN DHCP, inside DNS, and inter-VLAN firewall (blocked→opened). Interactive
  sibling `make net-demo` keeps that 3-VM topology alive with a browser console
  per VM. Known UCI-applier nit: it reloads fw4/dnsmasq right after the *async*
  network reload, so on a fresh interface they bind before netifd creates the
  device — sequence the service reload after the link is up.
- **Model — complete for the canonical config + harbor.** Every kind exists
  (firewall: `Zone`/`Forwarding`/`Redirect`/`FirewallRule`/`NAT`; dhcp/dns:
  `Dnsmasq`/`DHCPPool`/`Host`/`Domain`/`CNAME`; wireless: `WifiDevice`/`WifiIface`),
  rendered/parsed generically (reflection over `uci` tags). **Tier A additions
  done** (the three gaps the real-router fixture `harbor.config` exposed):
  `Route.Type` (blackhole VPN kill-switch), `DHCPPool.DHCPOption` (per-segment
  DHCP options incl. DNS=opt 6 / NTP=42), and `StaticProto.IPAddr` as a list
  (multiple addresses per interface; `Primary()` for single-addr consumers). The
  one remaining model field is `Redirect.Reflection` (§6). `harbor.config` (repo
  root, MikroTik export — contains live secrets, do not echo/store) is the
  migration/acceptance fixture: ~8 segments + 4 egress modes + the isolation
  matrix map; only Tier-B/C services (DDNS→Route53, UPnP, ZeroTier, SMB) and
  designed-out items (ACME-L7, port-knock, IDS-mirror) fall outside the model.
- **The UI is the gap.** `com.wash.net` already stubs the locked sections
  (`Firewall 🔒 / DHCP server 🔒 / Access point 🔒`); they un-grey when the
  backend advertises the capability (UCI does).

## 3. Design principles

1. **Segment-first, not object-first.** The user thinks "a VLAN" = subnet + DHCP +
   DNS + firewall policy as one unit. The model is *normalized* — that VLAN is an
   `Interface` + `Device` + `Zone` + `DHCPPool` + `Forwarding`s across three UCI
   packages. The UI presents the **bundle**; the FE composes the objects under the
   hood and stages them in one commit-confirm transaction. Raw per-kind objects
   live behind an **Advanced** view as the escape hatch.
2. **The zone grid is the security source of truth.** Inter-segment access is a
   **zone × zone matrix**. Every "can A reach B" decision lives there — nothing
   else is allowed to quietly contradict it (see hairpin, §6).
3. **Hide OpenWRT vocabulary.** Users say "device," "reservation," "DNS name,"
   "allow IoT → NAS:445" — not `config redirect` / `config domain` / `option masq`.
4. **Prefer firewall-respecting mechanisms.** Where two mechanisms achieve the same
   end, pick the one the zone grid governs. Split-DNS over hairpin; direct routes
   over reflection.
5. **Server-authoritative validation.** Relational invariants (a zone references a
   live interface, a forwarding references live zones, a pool's range fits its
   subnet) are validated in netd, surfaced in the FE — never trusted from the FE.

## 4. Where it lives — one app, capability-gated tabs

Same app — `com.wash.net` — never a separate router app. A box is type-2 *or*
type-1 by which backend is active, and caps drive what shows. The window is
**tabbed, and the tab strip is itself capability-gated**:

- **Workstation (type-2, NM/networkd).** `routerCaps()` false → **no tab strip at
  all**: just the Interfaces plane (today's Connections view) with in-place
  addressing and `+Ethernet/VLAN/Bridge/WireGuard/Wi-Fi`. *Unchanged from today* —
  the workstation experience **is** "tab 1, tabs hidden."
- **Router (type-1, UCI).** Router caps unlock tabs 2–5 above the same content. No
  mode toggle, no second app, no migration — the same `wash-app-net`, more tabs.

This supersedes the earlier flat left-nav (Networks · Firewall · Hosts · Port
forwards · DNS/DHCP · Wireless · Advanced): those screens become the *content* of
the ordered tabs in §4b. The locked-section un-greying (`Firewall 🔒` etc.) still
applies — caps reveal tabs rather than inline sections.

## 4b. Tab architecture — the carrier/segment split

The router plane read as confusing because one scroll mixed **two altitudes**: the
*segment/intent* layer (a LAN = bridge + IP + zone + DHCP) and the raw *link* layer
(bare adapters, unconfigured ports) — stacked vertically with nothing saying
they're a stack. Workstation mode feels clean precisely because it only has the
link layer. Tabs make the stack **explicit** and walk it left→right, bottom→top.

**Governing rule — addressing can only live in one place.** UCI already hands us
the seam: `config device` (the carrier) holds no IP; `config interface`
(proto/ipaddr) holds all of it. We adopt it as law:

- **Carrier = L2, never an IP.** Adapters, bridges, VLANs, bonds, **VPN/WireGuard
  tunnel *devices***. These live on **Interfaces**.
- **Segment = L3, always the IP.** A segment = "one carrier + addressing (+ zone +
  DHCP)." An "interface with an IP" is just a **degenerate segment** (one carrier,
  addressing, no zone/pool) — the same object type as a full LAN, emptier.

So a double-assignment is **structurally impossible** — only one object holds
proto/ipaddr. What changes per posture is *which tab renders it*, not where it's
stored: workstation renders the degenerate segment **inline** on the interface row;
router renders the same object as a **Networks** row, and Interfaces shows that IP
**read-only** as context (`br-lan.10 → serves LAN · 10.10.0.1/24`, deep-linked to
the Networks row). The mode flip is free — the object never moves, the view does.

### The tabs (left→right = bottom of the stack up)

| # | Tab | Owns (edit authority) | Subsumes (old §7) |
|---|-----|-----------------------|-------------------|
| 1 | **Interfaces** | carriers: adapters, **bridges, VLANs, bonds, VPN tunnels** + link-level config (membership, VID, MTU, key/endpoint, up/down). Incl. the **L2 port/VLAN matrix** (below). No L3. | new (workstation Connections view, promoted) |
| 2 | **Networks** | segments: carrier + addressing + zone + DHCP pool + egress | §7.1 |
| 3 | **Firewall** | zone×zone matrix + port-forwards + rules/NAT | §7.2, §7.4 |
| 4 | **Hosts & DNS** | reservations + static DNS + DNS/DHCP defaults | §7.3, §7.5 |
| 5 | **Advanced** | raw-object escape hatch | §7.7 |

Order = the dependency chain: no Network without a carrier, no firewalling a zone
that doesn't exist, no reserving a host on a network that isn't up. Wireless AP
(§7.6) is a carrier that joins a segment — **placement open** (§10): a 6th tab, or a
section of Interfaces. Leaning 6th tab, to keep Interfaces about wired carriers.

### The one genuinely new screen: the L2 port/VLAN matrix

Bridge-VLAN tagging — "for each member port, tagged/untagged per VLAN" (trunk vs
access) — is richer than today's flat bridge editor and wants its own **port × VLAN
matrix** on Interfaces. Pleasing symmetry: an **L2 port/VLAN matrix on Interfaces**
and the **L3 zone×zone matrix on Firewall** — the same interaction idiom at two
layers of the stack. Everything else on each tab is the existing §7 forms, routed
to the right tab.

## 5. Requirement → model → UI map

| Requirement | Model kinds | UI surface | Status |
|---|---|---|---|
| VLAN segments | `Device`(8021q) + `Interface` | **Networks** (segment bundle) | ✓ |
| Per-VLAN DHCP | `DHCPPool` | inside the segment | ✓ |
| DHCP→DNS register | `Dnsmasq{ExpandHosts,Domain}` + `Host.Hostname` | DNS/DHCP defaults + Hosts | ✓ |
| WAN uplink | `Interface` in `wan` zone | **Networks** (type=WAN) | ✓ |
| Per-segment VPN egress | WireGuard `Interface` + `PolicyRule` + `Route`(table) + `Zone` | segment **Egress** = WAN \| VPN | ✓ (no new model) |
| VPN kill-switch (no leak) | `Route{Type:blackhole}` in the tunnel table | implicit with Egress=VPN | ✓ (Tier A) |
| Per-segment DHCP DNS/NTP | `DHCPPool.DHCPOption` (opt 6 / 42) | inside the segment's DHCP | ✓ (Tier A) |
| Multiple addresses per iface | `StaticProto.IPAddr` (list) | segment address list | ✓ (Tier A) |
| Firewall per VLAN | `Zone` per segment | auto with the segment | ✓ |
| Inter-VLAN granular | `Forwarding` + `FirewallRule` | **Firewall matrix** | ✓ |
| NAT / masquerade | `Zone.Masq`, `NAT` | WAN segment toggle | ✓ |
| Static reservations | `Host{MAC,IP,Hostname}` | **Hosts** (with MAC) | ✓ |
| Static DNS / split-horizon | `Domain`, `CNAME` | **Hosts** (no MAC) | ✓ |
| Port forward (DNAT) | `Redirect` | **Port forwards** | ✓ |
| Hairpin NAT | `Redirect` + reflection | port-forward "internal access" | **needs model field** |
| Multi-WAN failover/balance | — (mwan3) | — | **deferred — VPN egress covers the real need** |
| Per-VLAN DNS *views* | — (one dnsmasq) | — | **out of scope (confirmed)** |

## 6. Model additions

**Tier A — done** (the three real-router gaps `harbor.config` exposed): `Route.Type`
(blackhole VPN kill-switch), `DHCPPool.DHCPOption` (per-segment DHCP options, incl.
DNS via option 6 and NTP via 42 — `StaticProto`/segment types DNS on top), and
`StaticProto.IPAddr` as a list (`Primary()` for single-address backends; codec
`normalizeIPAddr` folds a stock box's split `option ipaddr`+`netmask` and promotes a
scalar `ipaddr` into the list, render always emits `list ipaddr`).

**Remaining — one field, only for port forwards (step 5):**

1. **`Redirect.Reflection bool` (+ `ReflectionSrc string`)** — to *control* hairpin
   NAT explicitly (fw4 defaults reflection on; today the model can't turn it off or
   scope it). Required so the port-forward "internal access" choice is real.

Everything else is expressible. Hairpin is intentionally demoted (§7); split-DNS is
the default path and needs no new model.

## 7. The screens

### 7.1 Networks (segment bundle) — the primary create/edit unit

A "Network" is a segment. One create/edit flow; the FE materializes the objects.

- **Fields:** Name (e.g. `IoT`) · Type (`LAN segment` / `WAN uplink` / `VPN uplink`) ·
  for a LAN segment: carrier (untagged port / **VLAN tag N on <port>** / bridge of
  ports) · subnet + router IP (e.g. `10.0.20.1/24`) · **DHCP** (on/off, range, lease
  time) · **Egress** (`WAN` default, or a **VPN tunnel** — see below) · default
  isolation (on — the safe default; cross-segment access is added in the matrix).
  For a WAN uplink: port + proto (DHCP / static / PPPoE) + **masquerade** (on).
- **VPN uplink** is a `WireGuard` interface (the wizard we built) presented as an
  egress: its own zone (`Masq:true`) that LAN segments can route out of.
- **Egress = VPN** materializes the policy-routing trio with **no new model**: a
  `PolicyRule{in: <segment>, lookup: <table>}` + a `Route{interface: <wg>, target:
  0.0.0.0/0, table: <table>}` (default route via the tunnel for that table) + a
  `Forwarding{<segment> → <vpn zone>}`. So "send IoT out the VPN" is one toggle on
  the segment; everything else keeps the WAN default.
- **Materializes to:** `Device` (if VLAN/bridge) + `Interface` (L3) + `Zone`
  (Networks=[this interface], Forward=REJECT by default) + `DHCPPool` (if DHCP on) +
  (if Egress=VPN) the policy-routing trio above. WAN: `Interface` +
  `Zone{Masq:true, Input:REJECT}` + the default `lan→wan` forwarding.
- **Staging:** extends the existing draft model — the VLAN wizard already stages a
  `Device`+`Interface` together; a segment stages 3–4 objects, applied in one
  commit-confirm txn.

### 7.2 Firewall matrix — the centerpiece

A **zone × zone grid**. Rows = traffic *source*, columns = *destination*. Columns
include every segment, each **WAN**, and **Router** (the firewall host itself — the
zone `Input` policy: which segments may reach DNS/DHCP/admin).

Each cell's state:

- **Blocked** (default for LAN×LAN and WAN→LAN) — no forwarding, no rules.
- **Allow all** — a `Forwarding{src,dest}`.
- **Custom** — drill in to an ordered `FirewallRule` list for that source→dest pair
  (proto/ports/target ACCEPT|REJECT), e.g. *allow IoT → NAS tcp/445 only*.
- WAN column auto-reflects masquerade; **Router** column row = per-zone `Input`
  (e.g. Guest may use DNS/DHCP but not the admin UI).

The grid *is* the security policy in one view. Clicking a cell cycles
block → allow-all → custom (custom opens the rule editor). This is the one bespoke,
high-design screen; the rest are forms.

### 7.3 Hosts — unified reservations + static DNS

One list. Each row: **name → IP**, with an **optional MAC**:

- **MAC present** → DHCP reservation → `Host{MAC,IP,Hostname}` (and DNS comes free).
- **MAC empty** → pure DNS record → `Domain{Name,IP}` (static-IP boxes,
  split-horizon overrides, aliases). `CNAME` available for aliases.

Name handling: a bare name (`printer`) gets the local domain appended
(`printer.lan`); a dotted FQDN (`nas.example.com`) is used verbatim — which is
exactly how a split-horizon override is entered. So "reserve my printer" and "make
the NAS reachable internally by its public name" are the **same gesture in the same
list**. *(Open: bare-vs-FQDN auto-detect by the dot, or an explicit toggle — §10.)*

### 7.4 Port forwards — with the internal-access choice

A `Redirect` editor (WAN:port → internal host:port). The interesting field is
**"internal access"** — how a LAN client reaches this service by its public name:

- **Split-DNS (recommended)** — stamps a `Domain` (public name → internal IP). The
  client connects *direct*; the **firewall matrix governs it**. No isolation bypass.
- **Hairpin (override)** — sets `Redirect.Reflection`. Works regardless of the
  client's resolver (DoH-proof), but **warns**: "reflection lets <sources> reach
  <dest>:<port>, bypassing your firewall rules." Scoped via `ReflectionSrc`.
- **None** — external only.

Default split-DNS; hairpin is the explicit, warned escape hatch (§3.4).

### 7.5 DNS / DHCP defaults

Global `Dnsmasq` form: local domain, `ExpandHosts` (lease→DNS, on), upstream
`Server`s, `DomainNeeded`/`BogusPriv`. Small, list-like.

### 7.6 Wireless AP

`WifiDevice` (radio: channel/width/band) + `WifiIface` in AP mode (SSID, encryption
incl. the `eap`/WPA-Enterprise the model already names, assigned to a segment). This
is the client Wi-Fi UI inverted — *you're* the AP. Lower design risk than the matrix.

### 7.7 Advanced (raw objects)

The schema-driven `ObjectForm` over the raw kinds (zones, forwardings, redirects,
rules, pools, domains…) — the power-user escape hatch and the safety net for
anything the bundled flows don't yet cover. Already mostly free from the generated
editor.

## 8. Orchestration (multi-object staging)

The existing draft/apply machinery already stages multi-object changes (VLAN = Device
+ Interface) and commits them in one txn. Segment bundles and matrix edits extend
this: a segment edit may touch a `Device`+`Interface`+`Zone`+`DHCPPool`; a matrix
cell touches `Forwarding`/`FirewallRule`. All land in the same staged draft, diffed
and applied as **one commit-confirm transaction** — so a half-applied router config
never exists, and a bad firewall edit auto-reverts (the lock-out `Verify` already in
the UCI applier).

## 8b. The segment projection (Go lens — NOT a CLI)

The "segment-first" principle (§3.1) is formalized as a **pure, bidirectional lens
over the model**, not a parallel source of truth and **not a CLI** (the raw model +
the UCI codec/apply path stay the one truth; the lens only re-shapes them):

```
Project(model.Config)                    → (segments, policy, leftovers)
Materialize(segments, policy, leftovers) → model.Config
```

- **segments** are the nodes: a `Segment` = name · role (LAN/WAN/VPN) · carrier
  (untagged port / VLAN-tag-on-trunk / bridge-of-members) · subnet+router IP · DHCP
  (range, lease, per-pool DNS/NTP via `DHCPOption`) · egress (WAN / VPN-tunnel +
  blackhole kill-switch). Each owns one `Interface` (+`Device` if tagged/bridged) +
  one `Zone` + maybe a `DHCPPool`.
- **policy** is the edges: the zone×zone matrix (`Forwarding`/`FirewallRule`), kept
  separate from the nodes (N×N relations don't belong inside a segment).
- **leftovers** is the honesty valve: anything that doesn't fit the segment pattern
  (custom routes, ipsets, exotic objects) passes through untouched → the **Advanced**
  raw `ObjectForm` (§7.7). Projection is *total* even when it isn't *pretty*.

Lives in Go (`internal/washnet/segment`); **netd exposes the projection to the FE**
alongside the raw config, so the segment-bundle (§7.1) and matrix (§7.2) screens
read/write the same projection — there's no FE-only segment logic to drift. The
governing law is the round-trip `Materialize(Project(c)…) == c` for every `c` the
model can produce: a **pure Go property test, zero VMs** (the live `wash-vm/vm`
suite already covers the model→UCI→netifd last mile). `harbor.config` is the golden
acceptance fixture: "parses to N segments + M edges + a WG egress + ~90 DNS records,
K quarantined." No `washnet-seg` CLI — the consumers are the UI and the test.

## 8c. Create & delete as closure operations

A segment is a **bundle with a dependency closure**, so create and delete are
mirror images over that closure — both pure functions in the segment lens
(`internal/washnet/segment`); the UI just stages what they return into the existing
draft/diff/apply path.

**Delete = tear down the owned closure (reference-aware, not flat).** Deleting LAN:

- *deletes what the segment owns* — its L3 `Interface`, `DHCPPool`, `Zone`, and the
  `Host`/`Domain` records scoped to it;
- *cascades now-dangling references* — every `Forwarding`/`FirewallRule`/`Redirect`
  whose src/dest was the `lan` zone, routes with `device=lan` (a forwarding to a
  deleted zone is just garbage);
- ***detaches but keeps the carrier*** — `br-lan.10`/`eth0` is not owned; deleting
  the network frees its carrier back to **Interfaces** as a bare/unconfigured
  adapter. **Orphan carriers linger — decided** (reusable; the user may rewire; a
  wizard-auto-created VLAN is cheap to leave). A *shared* carrier (`br-lan` backing
  three VLANs) obviously survives — only an exclusive VLAN sub-interface goes.

No special "are you sure" modal: the delete **stages the whole closure as dirty
changes**, and the existing pending-bar/diff shows the blast radius ("removing LAN:
zone lan, pool, 2 forwardings, 2 reservations") before Apply.

**Create = materialize a role template (the inverse).** `role` is the template
selector (already on `Segment`):

- **+WAN** → DHCP-client v4 + DHCPv6 v6 (`reqprefix` for PD, §10b) interface,
  `Zone{wan, Input:REJECT, Masq, MTUFix}`, the standard wan allow-rules, **and
  forwardings from existing internal zones → wan** so existing LANs get internet at
  once. **Retroactive wiring — decided:** creating WAN *after* the LANs auto-adds
  the internal→wan forwardings (wizard checkbox "give internet to: ☑ LAN ☑ IoT ☐
  Cams").
- **+LAN** → static `192.168.N.1/24`, a DHCP pool, `Zone{lan, Input:ACCEPT}`, a
  LAN→WAN forwarding if a WAN exists.
- **+IoT/guest role** → same shape, firewall stance flips to **isolated**: forward
  to `wan` only. The role picks both the addressing defaults *and* the firewall
  posture.

Aggressive boilerplate is safe because every scaffold stages as a visible diff and
applies through commit-confirm — a lock-out auto-reverts on the confirm timeout.

`materializeSegment(role, ctx)` / `removeSegment(seg, model)` already exist (§8b);
this extends them with cross-segment wiring (create) and the reference closure
(delete). Keeping it in the kernel keeps it unit-testable; the round-trip law (§8b)
still governs.

## 9. Delivery order

Ship one screen at a time; each is independently useful and testable. Backend, caps,
and model are done (§2); the segment lens is the next foundation.

1. ~~**UCI backend** — `backendsel` + OpenWRT test.~~ **Done**, proven by the live
   M0–M3 e2e + `net-demo` (§2).
2. **Tab chrome + Interfaces tab** (§4, §4b) — the capability-gated tab strip
   (hidden when `routerCaps()` false, so workstation is unchanged), and promote
   today's Connections view to the **Interfaces** tab (carriers only; addressing
   shown read-only as segment context in router mode).
3. **Segment projection** (§8b) — `Project`/`Materialize` + the round-trip property
   test + the `harbor.config` golden. The foundation the bundle and matrix render.
4. **Create/delete closure** (§8c) — extend `materializeSegment`/`removeSegment`
   with role templates (retroactive wan-forwarding wiring) and the delete reference
   closure (orphan carriers linger). Pure-kernel, unit-tested.
5. **Networks (segments)** — create/edit VLAN + WAN bundles over the projection.
6. **Hosts & DNS** — unified reservations + static DNS + DNS/DHCP defaults.
7. **Firewall matrix** — the centerpiece; needs zones from step 5.
8. **Port forwards + internal-access** — needs the `Reflection` model field + split-DNS (folds into the Firewall tab).
9. **L2 port/VLAN matrix** (§4b) — the one new screen: trunk/access tagging on the Interfaces tab. Needed for the multi-port + trunk case.
10. **Wireless AP** (placement §10), then **Advanced** raw view — mostly free; polish last.

## 10. Open questions

- **Wireless AP placement (§4b):** its own 6th tab, or a section of the Interfaces
  tab? An AP is a carrier that joins a segment (L2 access into it), so it fits
  Interfaces, but a radio/SSID screen is substantial enough to warrant its own tab.
  Leaning 6th tab.
- **Hosts name entry:** auto-detect bare-name vs FQDN by the dot, or an explicit
  "local name / full domain" field?
- **Matrix scale:** with many segments the grid grows N²; at what point do we need a
  collapsed/filtered view, and is a flat ordered rule list ever the better mental
  model for power users?
- **WAN/Router row semantics:** confirm the "Router" column (zone `Input`) is how we
  want to express "which segments can reach router services," vs a separate
  "Services" screen.

**Resolved (2026-06-07):** carrier/segment split — addressing lives only on the
segment, carriers are L2-only (§4b). Workstation = tabs-hidden degenerate case, one
window for both (§4). Orphan carriers linger on delete; retroactive wan-forwarding
wiring on create (§8c).

## 10b. IPv6 (router plane)

Captured requirements for downstream router IPv6 (separate from the *workstation*
IPv6 — autoconfig + static — which the type-2 backends already do). All of this
is the router plane, gated on the UCI/router backend.

1. **RA/ND server on the LAN/VLAN side** — the router answers Router Solicitations
   and sends Router Advertisements (+ Neighbor Discovery) so segment clients
   SLAAC-autoconfigure. Model: `DHCPPool.RA` / `DHCPPool.DHCPv6` (OpenWRT odhcpd
   `option ra 'server'` / `dhcpv6 'server'`) — already on the pool; surfaces in the
   segment's DHCP settings as "hand out IPv6 (RA/DHCPv6)".
2. **DHCPv6 *client* + RA on the WAN side** — the WAN is a client: accept upstream
   RA and run DHCPv6, **including prefix delegation request**. Model: `proto
   'dhcpv6'` on the WAN (parse/render done — codec `normalizeDHCPv6`), plus a
   **reqprefix** field (PD request) — *not modeled yet* (small add).
3. **Firewall = NAT-equivalent protection, configurable off.** IPv6 has no NAT, so
   the *firewall* must give the same posture NAT gave IPv4: **default-deny inbound
   from WAN to internal segments**, as stateful rules — and a per-segment toggle to
   open inbound IPv6 (for publicly-reachable hosts). This lands in the **zone
   matrix** (WAN→segment defaults to block; the toggle adds the forwarding/rule).
4. **Prefix delegation to VLANs from a wider WAN prefix.** The WAN gets a delegated
   prefix (e.g. /56) and each segment carves a /64 from it. Model: `Interface.
   IP6Assign` (added) per segment + the WAN `reqprefix`. Surfaces as the segment's
   "IPv6: assign a /64 from the WAN prefix" — automatic once the WAN has PD.

Net: most of this is already modeled (`DHCPPool.RA/DHCPv6`, `IP6Assign`, zones);
the gaps are the WAN **reqprefix** (PD request) field and wiring the **inbound-IPv6
toggle** into the firewall matrix. Folds into the Networks (segment), DHCP, and
Firewall screens — no new screen.

## 11. Deferred

- **Multi-WAN routing across physical links** (mwan3 — failover/balance). Out of
  scope; the real need ("route a VLAN out a VPN") is in scope via per-segment VPN
  egress (§7.1), built on the existing WireGuard + policy-routing kinds.
- **Per-VLAN DNS *views*** (name-hiding per subnet) — **confirmed not wanted.** One
  shared resolver; the firewall handles access.
- **QoS/SQM, dynamic routing, UPnP, IPsec/OpenVPN** — already deferred in NET.md §12.
