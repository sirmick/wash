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
  carrierKind: "vlan" | "port";
  parent: string; vid: number; // vlan tag on a trunk
  port: string;                // untagged port
  proto: "static" | "dhcp";    // wan uplink proto (lan is always static)
  address: string;             // lan gateway / wan static CIDR, e.g. 10.0.20.1/24
  dhcp: boolean; start: number; limit: number; lease: string; dns: string; // lan DHCP server
  isolate: boolean;            // lan zone forward REJECT (default) vs ACCEPT
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

// materializeSegment returns a NEW config with the segment bundle staged
// (Device if VLAN + Interface static gateway + Zone + DHCPPool), replacing any
// objects the old (orig) or new segment owned. Pure: it clones the input.
export function materializeSegment(cfg: Cfg, f: SegForm, orig?: Segment): Cfg {
  const next = structuredClone(cfg) as Cfg;
  const names = new Set([f.name, orig?.name].filter(Boolean) as string[]);
  const oldDev = (next.Interfaces ?? []).find((i) => names.has(i.Name))?.Device;
  next.Interfaces = (next.Interfaces ?? []).filter((i) => !names.has(i.Name));
  next.Pools = (next.Pools ?? []).filter((p: any) => !names.has(p.Interface));
  // Only drop the segment's OWN single-network zone (LAN). A WAN zone may be
  // multi-network/shared (the stock wan zone spans wan+wan6) — leave those.
  next.Zones = (next.Zones ?? []).filter((z: any) => !((z.Networks ?? []).length === 1 && names.has(z.Networks[0])));

  // Carrier (shared by both roles): a VLAN device, or an untagged port.
  let device = f.port;
  if (f.carrierKind === "vlan") {
    device = `${f.parent}.${f.vid}`;
    next.Devices = (next.Devices ?? []).filter((dev) => dev.Name !== oldDev && dev.Name !== device);
    next.Devices = [...(next.Devices ?? []), { Name: device, Type: "8021q", Ifname: f.parent, VID: f.vid }];
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
  return next;
}

// removeSegment returns a NEW config with a segment's whole bundle stripped.
export function removeSegment(cfg: Cfg, seg: Segment): Cfg {
  const next = structuredClone(cfg) as Cfg;
  const dev = (next.Interfaces ?? []).find((i) => i.Name === seg.name)?.Device;
  next.Interfaces = (next.Interfaces ?? []).filter((i) => i.Name !== seg.name);
  if (dev) next.Devices = (next.Devices ?? []).filter((x) => x.Name !== dev);
  next.Zones = (next.Zones ?? []).filter((z: any) => !((z.Networks ?? []).length === 1 && z.Networks[0] === seg.name));
  next.Pools = (next.Pools ?? []).filter((p: any) => p.Interface !== seg.name);
  return next;
}

// segFormFrom builds the wizard's initial state from an existing segment by
// reading the objects it owns out of cfg; parents/ports seed the carrier kind the
// segment isn't currently using.
export function segFormFrom(cfg: Cfg, seg: Segment, parents: string[], ports: string[]): SegForm {
  const iface = (cfg.Interfaces ?? []).find((i) => i.Name === seg.name);
  const pool = (cfg.Pools ?? []).find((p: any) => p.Interface === seg.name);
  const zone = (cfg.Zones ?? []).find((z: any) => (z.Networks ?? []).length === 1 && z.Networks[0] === seg.name);
  return {
    name: seg.name,
    role: seg.role === "wan" ? "wan" : "lan",
    carrierKind: seg.carrier.kind === "vlan" ? "vlan" : "port",
    parent: seg.carrier.kind === "vlan" ? (seg.carrier.port ?? "") : (parents[0] ?? ""),
    vid: seg.carrier.vid ?? 10,
    port: seg.carrier.kind === "vlan" ? (ports[0] ?? "") : (seg.carrier.port ?? iface?.Device ?? ""),
    proto: iface?.Proto?._tag === "dhcp" ? "dhcp" : "static",
    address: iface?.Proto?.IPAddr?.[0] ?? "",
    dhcp: !!pool,
    start: pool?.Start ?? 100, limit: pool?.Limit ?? 150, lease: pool?.LeaseTime ?? "12h",
    dns: (pool?.DHCPOption ?? []).find((o: string) => o.startsWith("6,"))?.slice(2) ?? "",
    isolate: (zone?.Forward ?? "REJECT") !== "ACCEPT",
  };
}
