// Pure segment-bundle kernel for the router UI: the form↔objects logic behind the
// Networks panel, extracted from main.tsx so it's unit-testable without Solid
// (mirrors objectform-model.ts). A "segment" = the model objects one network ties
// together (Interface + carrier Device + firewall Zone + DHCPPool); these
// functions group a config into segments (projectDraft) and materialize / strip a
// segment bundle (materializeSegment / removeSegment).
//
// This mirrors the Go segment.Project/Materialize grouping rules (single-network
// zone, pool by interface, device by name, role from wireguard/masq). The Go lens
// stays the authoritative round-trip + materialize spec; this is the FE's
// optimistic view of the locally-edited draft (the app manipulates the draft
// without round-tripping). If it sprawls, promote to a netd "project/materialize
// this draft" round-trip (single source).

export type Carrier = { kind: "untagged" | "vlan" | "bridge"; port?: string; vid?: number; members?: string[] };
export type Segment = { name: string; role: "lan" | "wan" | "vpn"; carrier: Carrier; device?: string; zone?: string; pool?: string; addrs?: string[] };

// SegForm is the editable shape of one segment (the wizard's state). role=lan is a
// downstream network (static gateway + optional DHCP server + isolation); role=wan
// is an uplink (DHCP/static client + masquerade, no DHCP server).
export type SegForm = {
  name: string;
  role: "lan" | "wan";
  carrierKind: "vlan" | "port" | "bridge" | "switch";
  parent: string; vid: number; // vlan tag on a trunk
  port: string;                // untagged port
  members: string[];           // bridge member ports (carrierKind === "bridge")
  proto: "static" | "dhcp";    // wan uplink proto (lan is always static)
  address: string;             // lan gateway / wan static CIDR, e.g. 10.0.20.1/24
  dhcp: boolean; start: number; limit: number; lease: string; dns: string; // lan DHCP server
  isolate: boolean;            // lan zone forward REJECT (default) vs ACCEPT
  egress?: string;             // "" / "wan" = normal WAN; a WireGuard iface name = route out that VPN tunnel
};

// Loose structural shapes — segment logic only touches these fields; the app's
// richer Config (full model via an index signature) is assignable to Cfg.
type Iface = { Name: string; Device?: string; Proto?: any };
type Dev = { Name: string; Type?: string; Ports?: string[]; Ifname?: string; VID?: number };
export type Cfg = { Interfaces?: Iface[]; Devices?: Dev[]; Zones?: any[]; Pools?: any[]; [k: string]: any };

export const carrierLabel = (c: Carrier): string => {
  switch (c.kind) {
    case "vlan": return `VLAN ${c.vid} on ${c.port}`;
    case "bridge": return `bridge of ${(c.members ?? []).join(", ")}`;
    default: return c.port ? `port ${c.port}` : "untagged";
  }
};

// projectDraft groups a config into the segment view (loopback omitted).
export function projectDraft(cfg: Cfg): Segment[] {
  const devByName = new Map((cfg.Devices ?? []).map((x) => [x.Name, x] as const));
  const out: Segment[] = [];
  for (const i of cfg.Interfaces ?? []) {
    if (i.Device === "lo" || i.Name === "loopback") continue;
    const dev = devByName.get(i.Device ?? "");
    const zone = (cfg.Zones ?? []).find((z: any) => (z.Networks ?? []).length === 1 && z.Networks[0] === i.Name);
    const pool = (cfg.Pools ?? []).find((p: any) => p.Interface === i.Name);
    const tag = i.Proto?._tag;
    // WAN = a masquerading zone contains this interface. Check ALL zones, not just
    // the single-network one grouped above — the stock 'wan' zone spans wan+wan6,
    // so single-net grouping misses it.
    const masq = (cfg.Zones ?? []).some((z: any) => z.Masq && (z.Networks ?? []).includes(i.Name));
    const role: Segment["role"] = tag === "wireguard" ? "vpn" : masq || zone?.Masq ? "wan" : "lan";
    let carrier: Carrier;
    if (dev?.Type === "8021q") carrier = { kind: "vlan", port: dev.Ifname, vid: dev.VID };
    else if (dev?.Type === "bridge") carrier = { kind: "bridge", members: dev.Ports };
    else carrier = { kind: "untagged", port: i.Device };
    out.push({
      name: i.Name, role, carrier, device: dev?.Name, zone: zone?.Name, pool: pool?.Name,
      addrs: tag === "static" ? (i.Proto.IPAddr ?? []) : [],
    });
  }
  return out;
}

// ensureForwarding adds a blanket zone→zone Forwarding if absent (idempotent).
const ensureForwarding = (c: Cfg, src: string, dest: string) => {
  c.Forwardings = c.Forwardings ?? [];
  if (!c.Forwardings.some((x: any) => x.Src === src && x.Dest === dest)) c.Forwardings.push({ Src: src, Dest: dest });
};

// materializeSegment returns a NEW config with the segment bundle staged
// (Device if VLAN + Interface static gateway + Zone + DHCPPool), replacing any
// objects the old (orig) or new segment owned. Pure: it clones the input.
//
// On CREATE (no orig), it also scaffolds the cross-segment internet forwardings
// (NET-ROUTER-UI.md §8c): a new LAN forwards out every existing WAN, and a new
// WAN retroactively gives every existing internal network internet. Edits never
// touch forwardings — the user's firewall matrix is preserved.
export function materializeSegment(cfg: Cfg, f: SegForm, orig?: Segment): Cfg {
  const next = structuredClone(cfg) as Cfg;
  const names = new Set([f.name, orig?.name].filter(Boolean) as string[]);
  const oldDev = (next.Interfaces ?? []).find((i) => names.has(i.Name))?.Device;
  next.Interfaces = (next.Interfaces ?? []).filter((i) => !names.has(i.Name));
  next.Pools = (next.Pools ?? []).filter((p: any) => !names.has(p.Interface));
  // Only drop the segment's OWN single-network zone (LAN). A WAN zone may be
  // multi-network/shared (the stock wan zone spans wan+wan6) — leave those.
  next.Zones = (next.Zones ?? []).filter((z: any) => !((z.Networks ?? []).length === 1 && names.has(z.Networks[0])));

  // Carrier (shared by both roles): a switch VLAN (an existing br-lan.<vid> from
  // the fabric table — bind, create nothing), a classic VLAN device, a bridge of
  // ports, or an untagged port.
  let device = f.port;
  if (f.carrierKind === "switch") {
    device = `br-lan.${f.vid}`; // the fabric (§4c) already owns this sub-device
    if (oldDev && oldDev !== device) next.Devices = (next.Devices ?? []).filter((dev) => dev.Name !== oldDev);
  } else if (f.carrierKind === "vlan") {
    device = `${f.parent}.${f.vid}`;
    next.Devices = (next.Devices ?? []).filter((dev) => dev.Name !== oldDev && dev.Name !== device);
    next.Devices = [...(next.Devices ?? []), { Name: device, Type: "8021q", Ifname: f.parent, VID: f.vid }];
  } else if (f.carrierKind === "bridge") {
    device = `br-${f.name}`;
    const members = f.members ?? [];
    const inBridge = new Set(members);
    // a port can't be both a bare interface and a bridge member — absorb any
    next.Interfaces = (next.Interfaces ?? []).filter((i) => !inBridge.has(i.Device ?? ""));
    next.Devices = (next.Devices ?? []).filter((dev) => dev.Name !== oldDev && dev.Name !== device);
    next.Devices = [...(next.Devices ?? []), { Name: device, Type: "bridge", Ports: members }];
  } else if (oldDev && oldDev !== device) {
    next.Devices = (next.Devices ?? []).filter((dev) => dev.Name !== oldDev);
  }

  if (f.role === "wan") {
    // WAN uplink: a DHCP/static client + a masquerading zone (no DHCP server, no
    // isolate). Reuse an existing zone that already lists this interface (e.g. the
    // stock wan zone) rather than duplicating it.
    const proto = f.proto === "static" ? { _tag: "static", IPAddr: [f.address] } : { _tag: "dhcp", IPv4: true };
    next.Interfaces = [...(next.Interfaces ?? []), { Name: f.name, Device: device, Proto: proto }];
    if (!(next.Zones ?? []).some((z: any) => (z.Networks ?? []).includes(f.name))) {
      next.Zones = [...(next.Zones ?? []), { Name: f.name, Networks: [f.name], Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true }];
    }
    // Retroactive wiring: a WAN created after the LANs lights them up — forward
    // every existing internal (non-masq) network out this new uplink.
    if (!orig) {
      for (const z of next.Zones ?? []) {
        if (z.Masq || z.Name === f.name || (z.Networks ?? []).length === 0) continue;
        ensureForwarding(next, z.Name, f.name);
      }
    }
    return next;
  }

  // LAN segment: static gateway + isolation + optional DHCP server.
  next.Interfaces = [...(next.Interfaces ?? []), { Name: f.name, Device: device, Proto: { _tag: "static", IPAddr: [f.address] } }];
  next.Zones = [...(next.Zones ?? []), { Name: f.name, Networks: [f.name], Input: "ACCEPT", Output: "ACCEPT", Forward: f.isolate ? "REJECT" : "ACCEPT" }];
  if (f.dhcp) {
    const pool: any = { Name: f.name, Interface: f.name, Start: f.start, Limit: f.limit, LeaseTime: f.lease };
    if (f.dns) pool.DHCPOption = [`6,${f.dns}`];
    next.Pools = [...(next.Pools ?? []), pool];
  }
  applyEgress(next, f);
  return next;
}

// egressTable is a stable routing-table id per VPN tunnel (so several segments
// out the same tunnel share its table, and different tunnels don't collide).
function egressTable(wg: string): number {
  let h = 0;
  for (const c of wg) h = (h + c.charCodeAt(0)) % 90;
  return 100 + h;
}

// ensureVpnZone gives a WireGuard tunnel a masquerading firewall zone so segments
// can egress (NAT) out of it.
function ensureVpnZone(cfg: Cfg, wg: string): void {
  cfg.Zones = cfg.Zones ?? [];
  if (!(cfg.Zones as any[]).some((z) => z.Name === wg)) {
    (cfg.Zones as any[]).push({ Name: wg, Networks: [wg], Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true });
  }
}

// applyEgress routes a LAN segment to the internet: out the WAN (default) or out a
// VPN tunnel (NET-ROUTER-UI §7.1). VPN egress materializes the policy-routing trio
// with NO new model — a `PolicyRule{in: seg, lookup: table}`, a default `Route` via
// the tunnel in that table, and a blackhole `Route` (the leak-proof kill-switch).
// It first strips the segment's prior egress (the egress forwarding + its policy
// rule) so switching WAN⇄VPN is clean; non-egress forwardings (the matrix) survive.
function applyEgress(cfg: Cfg, f: SegForm): void {
  const seg = f.name;
  const wgIfaces = new Set((cfg.Interfaces ?? []).filter((i: any) => i.Proto?._tag === "wireguard").map((i: any) => i.Name));
  const masqZones = new Set((cfg.Zones ?? []).filter((z: any) => z.Masq).map((z: any) => z.Name as string));
  const egressDest = new Set<string>([...masqZones, ...wgIfaces]);
  // strip this segment's previous egress (forwarding to a WAN/VPN zone + its rule)
  cfg.Forwardings = (cfg.Forwardings ?? []).filter((x: any) => !(x.Src === seg && egressDest.has(x.Dest)));
  cfg.PolicyRules = (cfg.PolicyRules ?? []).filter((r: any) => r.In !== seg);

  const wg = f.egress;
  if (wg && wg !== "wan" && wgIfaces.has(wg)) {
    const table = String(egressTable(wg));
    ensureVpnZone(cfg, wg);
    ensureForwarding(cfg, seg, wg); // segment → the VPN zone
    cfg.PolicyRules = [...(cfg.PolicyRules ?? []), { In: seg, Lookup: table }];
    cfg.Routes = cfg.Routes ?? [];
    const R = cfg.Routes as any[];
    if (!R.some((r) => r.Interface === wg && r.Table === table)) R.push({ Interface: wg, Target: "0.0.0.0/0", Table: table });
    if (!R.some((r) => r.Type === "blackhole" && r.Table === table)) R.push({ Target: "0.0.0.0/0", Table: table, Type: "blackhole", Metric: 100 });
    return;
  }
  // WAN egress: (re-)assert the segment forwards out every WAN zone. The egress
  // forwarding is owned by this choice; non-egress (inter-segment) forwardings —
  // the user's matrix — are untouched.
  for (const z of masqZones) if (!wgIfaces.has(z)) ensureForwarding(cfg, seg, z);
}

// removeSegment returns a NEW config with a segment's dependency closure torn
// down (NET-ROUTER-UI.md §8c): the L3 interface + its DHCP pool + the firewall
// zone it owns, AND a cascade of every reference to that zone (forwardings,
// firewall rules, redirects) and routing tied to the interface (routes, policy
// rules) — so no dangling reference to a deleted zone/interface is left behind.
//
// The CARRIER is deliberately KEPT: deleting a network frees its VLAN/bridge
// device (or bare port) back to the Interfaces plane as an orphan, reusable
// adapter — it is not owned by the segment ("keep orphan", §8c).
export function removeSegment(cfg: Cfg, seg: Segment): Cfg {
  const next = structuredClone(cfg) as Cfg;
  const ifaceName = seg.name;
  // The zone(s) this segment exclusively owns (single-network, this interface).
  // A WAN may share a zone (the stock wan zone spans wan+wan6) — leave shared
  // zones, only cascade the ones solely this interface's.
  const ownedZones = new Set(
    (next.Zones ?? [])
      .filter((z: any) => (z.Networks ?? []).length === 1 && z.Networks[0] === ifaceName)
      .map((z: any) => z.Name as string),
  );
  const refsZone = (o: any) => ownedZones.has(o.Src) || ownedZones.has(o.Dest);

  // L3 + DHCP.
  next.Interfaces = (next.Interfaces ?? []).filter((i) => i.Name !== ifaceName);
  next.Pools = (next.Pools ?? []).filter((p: any) => p.Interface !== ifaceName);
  // Owned zone + the cascade of everything that referenced it.
  next.Zones = (next.Zones ?? []).filter((z: any) => !ownedZones.has(z.Name));
  next.Forwardings = (next.Forwardings ?? []).filter((f: any) => !refsZone(f));
  next.FwRules = (next.FwRules ?? []).filter((r: any) => !refsZone(r));
  next.Redirects = (next.Redirects ?? []).filter((r: any) => !refsZone(r));
  // Routing tied to the interface (e.g. a VPN-egress route + policy-rule trio),
  // and a tunnel's WireGuard peers.
  next.Routes = (next.Routes ?? []).filter((r: any) => r.Interface !== ifaceName);
  next.PolicyRules = (next.PolicyRules ?? []).filter((r: any) => r.In !== ifaceName && r.Out !== ifaceName);
  next.WGPeers = (next.WGPeers ?? []).filter((p: any) => p.Interface !== ifaceName);
  return next;
}

// removeCarrier drops a constructed L2 carrier (bridge / VLAN / bond device) from
// the Interfaces tab. If a network still sits on it, that segment's bundle is torn
// down first (removeSegment — which keeps the carrier as an orphan), then the
// now-free device and any interface directly on it are removed. Member ports
// return to the free-adapter pool automatically (they're derived from links()).
export function removeCarrier(cfg: Cfg, device: string, servesName?: string): Cfg {
  let next: Cfg = servesName ? removeSegment(cfg, { name: servesName } as Segment) : cfg;
  next = structuredClone(next) as Cfg;
  next.Devices = (next.Devices ?? []).filter((d) => d.Name !== device);
  next.Interfaces = (next.Interfaces ?? []).filter((i) => i.Device !== device);
  return next;
}

// --- carrier inventory (Interfaces tab, NET-ROUTER-UI.md §4b) -----------------
// The L2 view: every link the box has (physical adapters, bridges, VLANs, bonds,
// VPN tunnels) with its link-level detail and a READ-ONLY note of which segment
// it serves (addressing lives on the segment, shown here only as context). A
// carrier with no L3 interface bound is an `orphan` — a free, reusable adapter
// (incl. the carrier a deleted network left behind, §8c "keep orphan").
export type CarrierKind = "Adapter" | "Bridge" | "VLAN" | "Bond" | "Tunnel";
export type CarrierRow = {
  name: string;
  kind: CarrierKind;
  device: string; // the device/port name an action targets
  detail: string; // members / parent.vid / "bridged in X" / "trunk"
  serves?: { name: string; role: Segment["role"]; addr: string }; // read-only L3 context
  orphan: boolean; // no interface bound — a free/reusable carrier
};

export function projectCarriers(cfg: Cfg, links: string[] = []): CarrierRow[] {
  const ifaces = cfg.Interfaces ?? [];
  const devs = cfg.Devices ?? [];
  const ifByDev = new Map<string, Iface>();
  for (const i of ifaces) if (i.Device) ifByDev.set(i.Device, i);
  const segByName = new Map(projectDraft(cfg).map((s) => [s.name, s] as const));
  const servesOf = (devName: string): CarrierRow["serves"] | undefined => {
    const i = ifByDev.get(devName);
    if (!i || i.Name === "loopback") return undefined;
    const s = segByName.get(i.Name);
    const addr = s?.addrs?.[0] ?? (i.Proto?._tag === "dhcp" ? "DHCP" : "");
    return { name: i.Name, role: s?.role ?? "lan", addr };
  };
  const bridgeOf = new Map<string, string>(); // member port → bridge name
  const vlanParents = new Set<string>();
  for (const d of devs) {
    if (d.Type === "bridge") (d.Ports ?? []).forEach((p) => bridgeOf.set(p, d.Name));
    if (d.Type === "8021q" && d.Ifname) vlanParents.add(d.Ifname);
  }

  const rows: CarrierRow[] = [];
  // Physical adapters first (the box's hardware), each classified by how it's used.
  for (const l of links) {
    const serves = servesOf(l);
    if (serves) rows.push({ name: l, kind: "Adapter", device: l, detail: "", serves, orphan: false });
    else if (bridgeOf.has(l)) rows.push({ name: l, kind: "Adapter", device: l, detail: `bridged in ${bridgeOf.get(l)}`, orphan: false });
    else if (vlanParents.has(l)) rows.push({ name: l, kind: "Adapter", device: l, detail: "trunk (carries VLANs)", orphan: false });
    else rows.push({ name: l, kind: "Adapter", device: l, detail: "", orphan: true });
  }
  // Constructed L2 devices.
  for (const d of devs) {
    if (d.Type === "bridge") {
      const serves = servesOf(d.Name);
      rows.push({ name: d.Name, kind: "Bridge", device: d.Name, detail: `ports: ${(d.Ports ?? []).join(", ") || "—"}`, serves, orphan: !serves });
    } else if (d.Type === "8021q") {
      const serves = servesOf(d.Name);
      rows.push({ name: d.Name, kind: "VLAN", device: d.Name, detail: `VLAN ${d.VID} on ${d.Ifname}`, serves, orphan: !serves });
    } else if (d.Type === "bond") {
      const serves = servesOf(d.Name);
      rows.push({ name: d.Name, kind: "Bond", device: d.Name, detail: `ports: ${(d.Ports ?? []).join(", ") || "—"}`, serves, orphan: !serves });
    }
  }
  // VPN tunnels: a WireGuard interface IS its own carrier (no backing Device).
  for (const i of ifaces) {
    if (i.Proto?._tag !== "wireguard") continue;
    rows.push({
      name: i.Name, kind: "Tunnel", device: i.Device ?? i.Name, detail: "WireGuard",
      serves: { name: i.Name, role: "vpn", addr: (i.Proto?.Addresses ?? [])[0] ?? "" }, orphan: false,
    });
  }
  return rows;
}

// segFormFrom builds the wizard's initial state from an existing segment by
// reading the objects it owns out of cfg; parents/ports seed the carrier kind the
// segment isn't currently using.
export function segFormFrom(cfg: Cfg, seg: Segment, parents: string[], ports: string[]): SegForm {
  const iface = (cfg.Interfaces ?? []).find((i) => i.Name === seg.name);
  const pool = (cfg.Pools ?? []).find((p: any) => p.Interface === seg.name);
  const zone = (cfg.Zones ?? []).find((z: any) => (z.Networks ?? []).length === 1 && z.Networks[0] === seg.name);
  // egress = the WireGuard tunnel this segment forwards out of, else WAN.
  const wgIfaces = new Set((cfg.Interfaces ?? []).filter((i: any) => i.Proto?._tag === "wireguard").map((i: any) => i.Name));
  const egress = (cfg.Forwardings ?? []).find((f: any) => f.Src === seg.name && wgIfaces.has(f.Dest))?.Dest ?? "wan";
  return {
    name: seg.name,
    egress,
    role: seg.role === "wan" ? "wan" : "lan",
    carrierKind: seg.carrier.kind === "vlan" ? "vlan" : seg.carrier.kind === "bridge" ? "bridge" : "port",
    parent: seg.carrier.kind === "vlan" ? (seg.carrier.port ?? "") : (parents[0] ?? ""),
    vid: seg.carrier.vid ?? 10,
    port: seg.carrier.kind === "vlan" ? (ports[0] ?? "") : (seg.carrier.port ?? iface?.Device ?? ""),
    members: seg.carrier.kind === "bridge" ? (seg.carrier.members ?? []) : [],
    proto: iface?.Proto?._tag === "dhcp" ? "dhcp" : "static",
    address: iface?.Proto?.IPAddr?.[0] ?? "",
    dhcp: !!pool,
    start: pool?.Start ?? 100, limit: pool?.Limit ?? 150, lease: pool?.LeaseTime ?? "12h",
    dns: (pool?.DHCPOption ?? []).find((o: string) => o.startsWith("6,"))?.slice(2) ?? "",
    isolate: (zone?.Forward ?? "REJECT") !== "ACCEPT",
  };
}
