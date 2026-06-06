// Pure firewall-matrix kernel: the zone×zone access policy behind the Firewall
// screen (NET-ROUTER-UI.md §7.2), extracted so it's unit-testable without Solid.
// The matrix is the security source of truth — rows = traffic source zone, columns
// = destination zone (+ a "Router" column = the zone's Input policy: may it reach
// the router's own DNS/DHCP/admin). A cell is:
//   block  — no forwarding, no rules (the default; inter-zone is denied)
//   allow  — a blanket Forwarding{src,dest}
//   custom — selective FirewallRule(s) for that src→dest pair (edited in Advanced)
// Editing stages Forwarding / zone-Input changes into the same draft as the
// segment bundle, applied in one commit-confirm transaction.

import type { Cfg } from "./segment-model.ts";

export type CellState = "block" | "allow" | "custom";
export type MZone = { name: string; masq: boolean; input: string };

// matrixZones lists the firewall zones (the grid axes) with the bits the matrix
// surfaces: masq marks a WAN/egress zone; input is the Router-column policy.
export function matrixZones(cfg: Cfg): MZone[] {
  return (cfg.Zones ?? []).map((z: any) => ({
    name: z.Name as string,
    masq: !!z.Masq,
    input: (z.Input as string) ?? "ACCEPT",
  }));
}

// cellState classifies src→dest: custom (specific rules) wins over allow (blanket
// forwarding) wins over block. The diagonal (src===dest) is intra-zone, not a
// matrix edge — callers render it as N/A.
export function cellState(cfg: Cfg, src: string, dest: string): CellState {
  const rules = (cfg.FwRules ?? []).filter((r: any) => r.Src === src && r.Dest === dest);
  if (rules.length > 0) return "custom";
  const fwd = (cfg.Forwardings ?? []).some((f: any) => f.Src === src && f.Dest === dest);
  return fwd ? "allow" : "block";
}

// setForward turns a src→dest cell to allow (a blanket Forwarding) or block
// (removing it). It never touches FirewallRules — a custom cell must be edited via
// the rule list (Advanced), so callers should not call this on a custom cell.
export function setForward(cfg: Cfg, src: string, dest: string, on: boolean): Cfg {
  const next = structuredClone(cfg) as Cfg;
  next.Forwardings = (next.Forwardings ?? []).filter((f: any) => !(f.Src === src && f.Dest === dest));
  if (on) next.Forwardings = [...next.Forwardings, { Src: src, Dest: dest }];
  return next;
}

// toggleForward flips a block↔allow cell (a no-op on custom — guard at the call
// site / UI).
export function toggleForward(cfg: Cfg, src: string, dest: string): Cfg {
  return setForward(cfg, src, dest, cellState(cfg, src, dest) === "block");
}

// setInput sets a zone's Input policy (the Router column): ACCEPT lets that zone
// reach the router's services, REJECT locks it out.
export function setInput(cfg: Cfg, zone: string, policy: "ACCEPT" | "REJECT"): Cfg {
  const next = structuredClone(cfg) as Cfg;
  next.Zones = (next.Zones ?? []).map((z: any) => (z.Name === zone ? { ...z, Input: policy } : z));
  return next;
}
