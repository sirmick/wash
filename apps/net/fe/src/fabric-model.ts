// FE mirror of the Go L2-fabric lens (internal/washnet/fabric, NET-ROUTER-UI.md
// §4c): the box-global port×VLAN Plan ⟷ the OpenWRT device layer (bridges,
// bridge-vlan sections, 8021q sub-interfaces). Same idiom-by-topology rules as
// the Go lens; the app edits the locally-staged draft without round-tripping to
// netd (like segment-model.ts). Framework-free + unit-tested.

import type { Cfg } from "./segment-model.ts";

export const NATIVE = 1; // untagged/native domain (default PVID)
const BRIDGE = "br-lan"; // the single auto-named switch bridge

export type FVlan = { id: number; routed: boolean };
export type FPort = { name: string; untagged: number; tagged: number[] }; // untagged 0 = none
export type Plan = { vlans: FVlan[]; ports: FPort[] };
export type CellState = "none" | "untagged" | "tagged";

type Dev = { Name: string; Type?: string; Ports?: string[]; Ifname?: string; VID?: number; VLANFiltering?: boolean };
type BVlan = { Device?: string; VLAN?: number; Ports?: string[]; Local?: string };

const sortNums = (xs: number[]) => [...xs].sort((a, b) => a - b);
const findPort = (ports: FPort[], name: string): FPort => ports.find((p) => p.name === name) ?? { name, untagged: 0, tagged: [] };

// parseEntry decodes a bridge-vlan Ports entry: "<p>"|"<p>:t" tagged, "<p>:u"|"<p>:u*" untagged (* = PVID).
const parseEntry = (e: string): { name: string; tagged: boolean; pvid: boolean } => {
  const i = e.indexOf(":");
  if (i < 0) return { name: e, tagged: true, pvid: false };
  const name = e.slice(0, i);
  const suf = e.slice(i + 1);
  if (suf.startsWith("u")) return { name, tagged: false, pvid: suf.includes("*") };
  return { name, tagged: true, pvid: false };
};

// --- the lens ---------------------------------------------------------------

// filteringOn: the bridge is VLAN-aware once any numbered VLAN exists (even an
// empty one — so its column persists) or any port is tagged. Native-only is a
// plain bridge.
function filteringOn(plan: Plan): boolean {
  return plan.vlans.some((v) => v.id !== NATIVE) || plan.ports.some((p) => p.tagged.length > 0);
}

// materialize: every port with a membership joins br-lan (the table is the LAN
// switch — a trunk port is a tagged member, not a sub-interface; uplink tagging
// stays in the Networks "VLAN tag" carrier). When filtering, emit a bridge-vlan
// for every VLAN incl. portless ones (persisting the column), the native domain
// being VLAN 1. A single native port stays a (plain) bridge — never bare — so a
// stock single-port br-lan round-trips to itself.
export function materialize(plan: Plan): { devices: Dev[]; bridgeVlans: BVlan[] } {
  const routed = new Map<number, boolean>([[NATIVE, true]]);
  for (const v of plan.vlans) routed.set(v.id, v.routed);
  const brPorts = plan.ports.filter((p) => p.untagged !== 0 || p.tagged.length > 0).map((p) => p.name).sort();
  const filtering = filteringOn(plan);

  const devices: Dev[] = [];
  if (brPorts.length > 0 || filtering) {
    const dev: Dev = { Name: BRIDGE, Type: "bridge", Ports: brPorts };
    if (filtering) dev.VLANFiltering = true;
    devices.push(dev);
  }

  const bridgeVlans: BVlan[] = [];
  if (filtering) {
    const vids = new Set<number>([NATIVE]);
    for (const v of plan.vlans) if (v.id !== NATIVE) vids.add(v.id);
    for (const pt of plan.ports) {
      if (pt.untagged) vids.add(pt.untagged);
      for (const v of pt.tagged) vids.add(v);
    }
    for (const v of sortNums([...vids])) {
      const ports: string[] = [];
      for (const name of brPorts) {
        const pt = findPort(plan.ports, name);
        if (pt.untagged === v) ports.push(`${name}:u*`);
        else if (pt.tagged.includes(v)) ports.push(`${name}:t`);
      }
      const bv: BVlan = { Device: BRIDGE, VLAN: v, Ports: ports };
      if (routed.get(v) === false) bv.Local = "0";
      bridgeVlans.push(bv);
    }
  }
  return { devices, bridgeVlans };
}

export function project(devices: Dev[], bvlans: BVlan[]): { plan: Plan; leftovers: Dev[] } {
  const ports = new Map<string, FPort>();
  const port = (name: string): FPort => {
    let p = ports.get(name);
    if (!p) { p = { name, untagged: 0, tagged: [] }; ports.set(name, p); }
    return p;
  };
  const vlans = new Map<number, boolean>();
  const setVlan = (id: number, routed: boolean) => {
    const was = vlans.get(id);
    if (was === undefined || (was && !routed)) vlans.set(id, routed);
  };

  const leftovers: Dev[] = [];
  let br: Dev | undefined;
  for (const d of devices) {
    if (d.Type === "bridge" && d.Name === BRIDGE) br = d;
    else leftovers.push(d); // 8021q sub-ifaces + everything else are not table-owned
  }
  if (br) {
    if (!br.VLANFiltering) {
      for (const n of br.Ports ?? []) port(n).untagged = NATIVE;
      setVlan(NATIVE, true);
    } else {
      for (const n of br.Ports ?? []) port(n); // member rows show even if a bridge-vlan omits them
      for (const bv of bvlans) {
        if (bv.Device !== BRIDGE) continue;
        setVlan(bv.VLAN ?? 0, bv.Local !== "0");
        for (const entry of bv.Ports ?? []) {
          const { name, tagged, pvid } = parseEntry(entry);
          const pt = port(name);
          if (tagged) { if (!pt.tagged.includes(bv.VLAN ?? 0)) pt.tagged.push(bv.VLAN ?? 0); }
          else if (pvid) pt.untagged = bv.VLAN ?? 0;
        }
      }
    }
  }
  const plan: Plan = { vlans: [], ports: [] };
  for (const name of [...ports.keys()].sort()) {
    const pt = ports.get(name)!;
    pt.tagged = sortNums(pt.tagged);
    plan.ports.push(pt);
  }
  for (const id of sortNums([...vlans.keys()])) plan.vlans.push({ id, routed: vlans.get(id)! });
  return { plan, leftovers };
}

// --- draft helpers (project from / splice into the staged Config) -----------

export function projectFabric(cfg: Cfg): { plan: Plan; leftovers: Dev[] } {
  return project((cfg.Devices ?? []) as Dev[], (cfg.BridgeVLANs ?? []) as BVlan[]);
}

// setFabric materializes a Plan back into the draft: it replaces the lens-owned
// devices (the br-lan bridge + every 8021q sub-iface) + all bridge-vlans, while
// preserving leftover devices and the rest of the config (interfaces, zones…).
export function setFabric(cfg: Cfg, plan: Plan): Cfg {
  const { devices, bridgeVlans } = materialize(plan);
  const next = structuredClone(cfg) as Cfg;
  const leftovers = ((next.Devices ?? []) as Dev[]).filter((d) => !(d.Type === "bridge" && d.Name === BRIDGE));
  next.Devices = [...leftovers, ...devices];
  next.BridgeVLANs = bridgeVlans;
  // Rebind the native LAN interface as filtering toggles: a plain bridge
  // terminates at br-lan, a filtering bridge at br-lan.1 (NET-ROUTER-UI §4c) — so
  // enabling VLANs doesn't orphan the interface bound to the bridge.
  const brDev = devices.find((d) => d.Name === BRIDGE);
  if (brDev) {
    const nativeDev = brDev.VLANFiltering ? `${BRIDGE}.${NATIVE}` : BRIDGE;
    for (const i of (next.Interfaces ?? []) as Array<{ Device?: string }>) {
      if (i.Device === BRIDGE || i.Device === `${BRIDGE}.${NATIVE}`) i.Device = nativeDev;
    }
  }
  return next;
}

// --- table editing (pure Plan transforms the UI calls) ----------------------

const stateOf = (pt: FPort, vlan: number): CellState => (pt.untagged === vlan ? "untagged" : pt.tagged.includes(vlan) ? "tagged" : "none");

export function cellOf(plan: Plan, port: string, vlan: number): CellState {
  return stateOf(findPort(plan.ports, port), vlan);
}

// vlanColumns: the Native column (always) + every other VLAN, ascending.
export function vlanColumns(plan: Plan): number[] {
  const ids = new Set<number>([NATIVE]);
  for (const v of plan.vlans) ids.add(v.id);
  return sortNums([...ids]);
}

export function isRouted(plan: Plan, vlan: number): boolean {
  if (vlan === NATIVE) return true;
  return plan.vlans.find((v) => v.id === vlan)?.routed ?? true;
}

const ensurePort = (plan: Plan, name: string): FPort => {
  let pt = plan.ports.find((p) => p.name === name);
  if (!pt) { pt = { name, untagged: 0, tagged: [] }; plan.ports.push(pt); }
  return pt;
};
const ensureVlan = (plan: Plan, vlan: number) => {
  if (vlan !== NATIVE && !plan.vlans.some((v) => v.id === vlan)) plan.vlans.push({ id: vlan, routed: true });
};

// cycle advances a cell none → untagged(PVID) → tagged → none. Untagged sets the
// port's single PVID (overwriting any prior native/PVID on that port).
export function cycle(plan: Plan, port: string, vlan: number): Plan {
  const next = structuredClone(plan) as Plan;
  ensureVlan(next, vlan);
  const pt = ensurePort(next, port);
  const s = stateOf(pt, vlan);
  if (pt.untagged === vlan) pt.untagged = 0;
  pt.tagged = pt.tagged.filter((v) => v !== vlan);
  if (s === "none") pt.untagged = vlan;
  else if (s === "untagged") pt.tagged = sortNums([...pt.tagged, vlan]);
  return next;
}

export function addVlan(plan: Plan, vlan: number): Plan {
  const next = structuredClone(plan) as Plan;
  ensureVlan(next, vlan);
  return next;
}

export function removeVlan(plan: Plan, vlan: number): Plan {
  const next = structuredClone(plan) as Plan;
  next.vlans = next.vlans.filter((v) => v.id !== vlan);
  for (const pt of next.ports) {
    if (pt.untagged === vlan) pt.untagged = 0;
    pt.tagged = pt.tagged.filter((v) => v !== vlan);
  }
  return next;
}

export function setRouted(plan: Plan, vlan: number, routed: boolean): Plan {
  const next = structuredClone(plan) as Plan;
  const v = next.vlans.find((x) => x.id === vlan);
  if (v) v.routed = routed;
  return next;
}
