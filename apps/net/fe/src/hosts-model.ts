// Pure hosts kernel: the unified "reservations + static DNS" list behind the Hosts
// screen (NET-ROUTER-UI.md §7.3), extracted so it's unit-testable without Solid.
// One list, each row name → IP with an optional MAC:
//   MAC present → a DHCP reservation (config host; the name also resolves in DNS
//                 for free via dnsmasq expandhosts)
//   MAC empty   → a pure static DNS record (config domain) — also how a
//                 split-horizon override is entered (a dotted FQDN used verbatim)
// So "reserve my printer" and "make the NAS reachable by name" are the same gesture.
// Edits stage into the same draft as segments/matrix → one commit-confirm txn.

import type { Cfg } from "./segment-model.ts";

export type HostEntry = { name: string; ip: string; mac?: string };

// projectHosts flattens config host + domain records into the unified list.
export function projectHosts(cfg: Cfg): HostEntry[] {
  const out: HostEntry[] = [];
  for (const h of (cfg.Hosts ?? []) as any[]) {
    out.push({ name: h.Hostname || h.Name || "", ip: h.IP ?? "", mac: h.MAC || undefined });
  }
  for (const d of (cfg.Domains ?? []) as any[]) {
    out.push({ name: d.Name ?? "", ip: d.IP ?? "" });
  }
  return out;
}

// upsertHost adds/updates a unified entry: with a MAC it's a reservation (config
// host), without it a static DNS record (config domain). It first removes any
// prior entry of EITHER kind for the old or new name, so editing — including
// flipping reservation↔DNS by adding/removing the MAC — never duplicates.
export function upsertHost(cfg: Cfg, e: HostEntry, origName?: string): Cfg {
  const next = structuredClone(cfg) as Cfg;
  const names = new Set([e.name, origName].filter(Boolean) as string[]);
  next.Hosts = ((next.Hosts ?? []) as any[]).filter((h) => !names.has(h.Hostname || h.Name));
  next.Domains = ((next.Domains ?? []) as any[]).filter((d) => !names.has(d.Name));
  if (e.mac) next.Hosts = [...(next.Hosts as any[]), { Name: e.name, Hostname: e.name, MAC: e.mac, IP: e.ip }];
  else next.Domains = [...(next.Domains as any[]), { Name: e.name, IP: e.ip }];
  return next;
}

// removeHost strips a unified entry (whichever kind it is) by name.
export function removeHost(cfg: Cfg, e: HostEntry): Cfg {
  const next = structuredClone(cfg) as Cfg;
  next.Hosts = ((next.Hosts ?? []) as any[]).filter((h) => (h.Hostname || h.Name) !== e.name);
  next.Domains = ((next.Domains ?? []) as any[]).filter((d) => d.Name !== e.name);
  return next;
}
