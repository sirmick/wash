// Pure kernel for the L2 port/VLAN matrix (Interfaces tab, NET-ROUTER-UI.md §4b):
// reads/writes `config bridge-vlan` for a VLAN-aware bridge. A cell is one port's
// membership in one VLAN — none / untagged(+PVID) / tagged — encoded in the
// bridge-vlan Ports list as "<port>" or "<port>:t" (tagged), "<port>:u"
// (untagged), "<port>:u*" (untagged + PVID, the port's native VLAN). Mutations
// keep one PVID per port and flip the bridge's vlan_filtering as VLANs come/go.
//
// Mirrors model.BridgeVLAN; the app edits the locally-staged draft, applied via
// the same commit-confirm txn as everything else. Framework-free + unit-tested.

import type { Cfg } from "./segment-model.ts";

export type CellState = "none" | "untagged" | "tagged";
type BVlan = { Device?: string; VLAN?: number; Ports?: string[]; Local?: string };

const parseEntry = (e: string): { port: string; state: "untagged" | "tagged"; pvid: boolean } => {
  const i = e.indexOf(":");
  if (i < 0) return { port: e, state: "tagged", pvid: false };
  const port = e.slice(0, i);
  const suf = e.slice(i + 1);
  if (suf.startsWith("u")) return { port, state: "untagged", pvid: suf.includes("*") };
  return { port, state: "tagged", pvid: false };
};
const makeEntry = (port: string, state: CellState, pvid: boolean): string | null =>
  state === "none" ? null : state === "tagged" ? `${port}:t` : pvid ? `${port}:u*` : `${port}:u`;

const bvlans = (cfg: Cfg): BVlan[] => (cfg.BridgeVLANs ?? []) as BVlan[];

// VLAN ids defined on a bridge, ascending.
export function bridgeVlans(cfg: Cfg, bridge: string): number[] {
  return bvlans(cfg)
    .filter((b) => b.Device === bridge && typeof b.VLAN === "number")
    .map((b) => b.VLAN as number)
    .sort((a, b) => a - b);
}

// The bridge device's member ports (the matrix rows).
export function bridgePorts(cfg: Cfg, bridge: string): string[] {
  const d = (cfg.Devices ?? []).find((x: any) => x.Name === bridge);
  return ((d?.Ports ?? []) as string[]).slice();
}

// A bridge is VLAN-aware when its device has vlan_filtering set.
export function isVlanAware(cfg: Cfg, bridge: string): boolean {
  const d = (cfg.Devices ?? []).find((x: any) => x.Name === bridge);
  return !!(d as any)?.VLANFiltering;
}

export function cellState(cfg: Cfg, bridge: string, port: string, vlan: number): { state: CellState; pvid: boolean } {
  const b = bvlans(cfg).find((x) => x.Device === bridge && x.VLAN === vlan);
  const e = (b?.Ports ?? []).map(parseEntry).find((p) => p.port === port);
  return e ? { state: e.state, pvid: e.pvid } : { state: "none", pvid: false };
}

function setFiltering(cfg: Cfg, bridge: string, on: boolean): void {
  const d = (cfg.Devices ?? []).find((x: any) => x.Name === bridge);
  if (d) (d as any).VLANFiltering = on;
}

// setCell writes a port's state in a VLAN. `untagged` always implies PVID (a
// port's native VLAN) and is unique per port — making one untagged strips the
// port's untagged membership from every other VLAN on the bridge.
export function setCell(cfg: Cfg, bridge: string, port: string, vlan: number, state: CellState): Cfg {
  const next = structuredClone(cfg) as Cfg;
  next.BridgeVLANs = bvlans(next);
  const list = next.BridgeVLANs as BVlan[];
  if (state === "untagged") {
    for (const b of list) {
      if (b.Device !== bridge || b.VLAN === vlan) continue;
      b.Ports = (b.Ports ?? []).filter((e) => !(parseEntry(e).port === port && parseEntry(e).state === "untagged"));
    }
  }
  let bv = list.find((b) => b.Device === bridge && b.VLAN === vlan);
  if (!bv) {
    bv = { Device: bridge, VLAN: vlan, Ports: [] };
    list.push(bv);
  }
  bv.Ports = (bv.Ports ?? []).filter((e) => parseEntry(e).port !== port);
  const entry = makeEntry(port, state, true);
  if (entry) bv.Ports.push(entry);
  return next;
}

// cycleCell advances a cell none → untagged(PVID) → tagged → none (the matrix click).
export function cycleCell(cfg: Cfg, bridge: string, port: string, vlan: number): Cfg {
  const { state } = cellState(cfg, bridge, port, vlan);
  const nextState: CellState = state === "none" ? "untagged" : state === "untagged" ? "tagged" : "none";
  return setCell(cfg, bridge, port, vlan, nextState);
}

// isTransit reports whether a VLAN is switch-only (local '0') — not terminated
// at L3 here, so no `<bridge>.<vid>` adapter and no Network. Default is routed.
export function isTransit(cfg: Cfg, bridge: string, vlan: number): boolean {
  return bvlans(cfg).find((b) => b.Device === bridge && b.VLAN === vlan)?.Local === "0";
}

// setTransit flips a VLAN routed⇄transit. transit=true writes local '0' (the
// kernel then never creates the sub-device); routed clears it (OpenWRT default 1).
export function setTransit(cfg: Cfg, bridge: string, vlan: number, transit: boolean): Cfg {
  const next = structuredClone(cfg) as Cfg;
  const b = bvlans(next).find((x) => x.Device === bridge && x.VLAN === vlan);
  if (b) b.Local = transit ? "0" : "";
  return next;
}

// addVlan adds an (empty) VLAN column and turns the bridge VLAN-aware.
export function addVlan(cfg: Cfg, bridge: string, vlan: number): Cfg {
  const next = structuredClone(cfg) as Cfg;
  next.BridgeVLANs = bvlans(next);
  if (!(next.BridgeVLANs as BVlan[]).some((b) => b.Device === bridge && b.VLAN === vlan)) {
    (next.BridgeVLANs as BVlan[]).push({ Device: bridge, VLAN: vlan, Ports: [] });
  }
  setFiltering(next, bridge, true);
  return next;
}

// removeVlan drops a VLAN column; if it was the last, the bridge stops filtering.
export function removeVlan(cfg: Cfg, bridge: string, vlan: number): Cfg {
  const next = structuredClone(cfg) as Cfg;
  next.BridgeVLANs = bvlans(next).filter((b) => !(b.Device === bridge && b.VLAN === vlan));
  if (bridgeVlans(next, bridge).length === 0) setFiltering(next, bridge, false);
  return next;
}
