# Router UI plan (type-1 / UCI)

The plan for wash's **router-plane** UI — the screens UCI unlocks (firewall, DHCP/DNS
server, wireless AP). It's the second half of the UCI work: the backend
(`apps/netd/be/uci`) is a near-mechanical port because the model is UCI-shaped;
the UI is the real design effort. This doc fixes the shape so the build is
forms-and-wiring, not open questions.

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

- **UCI backend — done (Phase 1, `wash-uci`):** `Render`=`codec.Render`, `Apply`
  writes `/etc/config/{network,firewall,dhcp,wireless}` + reloads, `Live`
  reads→`codec.Parse`, snapshot rollback, lock-out `Verify`, OpenWRT `Detect`,
  `Capabilities`=`caps.Full()`. Unit-tested. *Remaining:* wire into `backendsel`
  + an OpenWRT-container backend test.
- **Model — ~complete.** Every kind the canonical config needs already exists
  (firewall: `Zone`/`Forwarding`/`Redirect`/`FirewallRule`/`NAT`; dhcp/dns:
  `Dnsmasq`/`DHCPPool`/`Host`/`Domain`/`CNAME`; wireless: `WifiDevice`/`WifiIface`).
  The codec renders/parses them generically (reflection over `uci` tags).
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

## 4. Where it lives

Same app — `com.wash.net` — **un-greying** the existing locked sections, gated on
caps. Not a separate router app: a box is type-2 *or* type-1 by which backend is
active, and the caps already drive what shows. On UCI/OpenWRT the router sections
light up; on NM/networkd they stay hidden. The app's left nav grows from
"Connections" to: **Networks · Firewall · Hosts · Port forwards · DNS/DHCP ·
Wireless · Advanced**.

## 5. Requirement → model → UI map

| Requirement | Model kinds | UI surface | Status |
|---|---|---|---|
| VLAN segments | `Device`(8021q) + `Interface` | **Networks** (segment bundle) | ✓ |
| Per-VLAN DHCP | `DHCPPool` | inside the segment | ✓ |
| DHCP→DNS register | `Dnsmasq{ExpandHosts,Domain}` + `Host.Hostname` | DNS/DHCP defaults + Hosts | ✓ |
| WAN uplink | `Interface` in `wan` zone | **Networks** (type=WAN) | ✓ |
| Per-segment VPN egress | WireGuard `Interface` + `PolicyRule` + `Route`(table) + `Zone` | segment **Egress** = WAN \| VPN | ✓ (no new model) |
| Firewall per VLAN | `Zone` per segment | auto with the segment | ✓ |
| Inter-VLAN granular | `Forwarding` + `FirewallRule` | **Firewall matrix** | ✓ |
| NAT / masquerade | `Zone.Masq`, `NAT` | WAN segment toggle | ✓ |
| Static reservations | `Host{MAC,IP,Hostname}` | **Hosts** (with MAC) | ✓ |
| Static DNS / split-horizon | `Domain`, `CNAME` | **Hosts** (no MAC) | ✓ |
| Port forward (DNAT) | `Redirect` | **Port forwards** | ✓ |
| Hairpin NAT | `Redirect` + reflection | port-forward "internal access" | **needs model field** |
| Multi-WAN failover/balance | — (mwan3) | — | **deferred — VPN egress covers the real need** |
| Per-VLAN DNS *views* | — (one dnsmasq) | — | **out of scope (confirmed)** |

## 6. Model additions needed

Small, and only one is required for the canonical config:

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

## 9. Delivery order

Ship one screen at a time; each is independently useful and testable. Backend +
caps first so every screen has something real to talk to.

1. **Finish the UCI backend** — `backendsel` wiring + OpenWRT-container test. *(Phase 1 tail.)*
2. **Networks (segments)** — create/edit VLAN + WAN bundles; the foundation everything refs.
3. **Hosts** — unified reservations + static DNS (small, high-value, unblocks DHCP→DNS).
4. **Firewall matrix** — the centerpiece; needs zones from step 2.
5. **Port forwards + internal-access** — needs the `Reflection` model field + split-DNS.
6. **DNS/DHCP defaults**, then **Wireless AP**.
7. **Advanced** raw view — mostly free; polish last.

## 10. Open questions

- **Hosts name entry:** auto-detect bare-name vs FQDN by the dot, or an explicit
  "local name / full domain" field?
- **Matrix scale:** with many segments the grid grows N²; at what point do we need a
  collapsed/filtered view, and is a flat ordered rule list ever the better mental
  model for power users?
- **WAN/Router row semantics:** confirm the "Router" column (zone `Input`) is how we
  want to express "which segments can reach router services," vs a separate
  "Services" screen.

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
   'dhcpv6'` on the WAN (the parse/render fix in flight), plus a **reqprefix** field
   (PD request) — *not modeled yet* (small add).
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
