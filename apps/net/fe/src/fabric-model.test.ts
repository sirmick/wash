import { test } from "node:test";
import assert from "node:assert";
import {
  materialize, project, projectFabric, setFabric, cycle, cellOf, vlanColumns, isRouted,
  addVlan, removeVlan, setRouted, type Plan,
} from "./fabric-model.ts";
import type { Cfg } from "./segment-model.ts";

// stable: materialize → project → materialize reproduces the device layer.
function stable(name: string, plan: Plan) {
  const m1 = materialize(plan);
  const { plan: pp, leftovers } = project(m1.devices, m1.bridgeVlans);
  assert.equal(leftovers.length, 0, `${name}: no leftovers`);
  const m2 = materialize(pp);
  assert.deepEqual(m2, m1, `${name}: lens not stable`);
}

test("lens is stable across the canonical shapes", () => {
  stable("bare native", { vlans: [{ id: 1, routed: true }], ports: [{ name: "eth0", untagged: 1, tagged: [] }] });
  stable("plain bridge", { vlans: [{ id: 1, routed: true }], ports: [{ name: "eth1", untagged: 1, tagged: [] }, { name: "eth2", untagged: 1, tagged: [] }] });
  stable("lone tagged uplink", { vlans: [{ id: 100, routed: true }], ports: [{ name: "eth0", untagged: 0, tagged: [100] }] });
  stable("filtering: access + trunk + transit", {
    vlans: [{ id: 1, routed: true }, { id: 20, routed: true }, { id: 30, routed: false }],
    ports: [
      { name: "eth1", untagged: 1, tagged: [] },
      { name: "eth2", untagged: 20, tagged: [] },
      { name: "eth3", untagged: 30, tagged: [] },
      { name: "eth4", untagged: 1, tagged: [20, 30] },
    ],
  });
});

test("materialize: native-only is a plain bridge; any VLAN/tag turns on filtering", () => {
  // plain bridge: no filtering, no bridge-vlan
  let m = materialize({ vlans: [], ports: [{ name: "eth1", untagged: 1, tagged: [] }, { name: "eth2", untagged: 1, tagged: [] }] });
  assert.equal(m.devices.length, 1);
  assert.equal(m.devices[0].Name, "br-lan");
  assert.equal(m.devices[0].VLANFiltering, undefined);
  assert.equal(m.bridgeVlans.length, 0);
  // a tagged trunk port stays in br-lan (no sub-interface) with filtering on
  m = materialize({ vlans: [{ id: 100, routed: true }], ports: [{ name: "eth0", untagged: 1, tagged: [100] }] });
  assert.equal(m.devices.length, 1, "no eth0.100 sub-interface — it's a trunk member");
  assert.equal(m.devices[0].Name, "br-lan");
  assert.equal(m.devices[0].VLANFiltering, true);
  // transit → local '0'
  m = materialize({ vlans: [{ id: 30, routed: false }], ports: [{ name: "eth1", untagged: 1, tagged: [] }, { name: "eth2", untagged: 30, tagged: [] }] });
  assert.equal(m.bridgeVlans.find((b) => b.VLAN === 30)!.Local, "0");
});

test("single-port plain bridge round-trips to itself (no bare-ify, no orphan)", () => {
  const cfg: Cfg = { Devices: [{ Name: "br-lan", Type: "bridge", Ports: ["eth0"] }], Interfaces: [{ Name: "lan", Device: "br-lan", Proto: {} }] };
  const { plan } = projectFabric(cfg);
  const out = setFabric(cfg, plan); // identity edit
  assert.ok((out.Devices ?? []).some((d: any) => d.Name === "br-lan" && (d.Ports ?? []).includes("eth0")), "br-lan kept");
  assert.equal((out.Interfaces ?? [])[0].Device, "br-lan", "lan still bound");
});

test("adding a VLAN persists its (empty) column + rebinds native br-lan → br-lan.1", () => {
  const cfg: Cfg = { Devices: [{ Name: "br-lan", Type: "bridge", Ports: ["eth0"] }], Interfaces: [{ Name: "lan", Device: "br-lan", Proto: {} }] };
  const { plan } = projectFabric(cfg);
  const out = setFabric(cfg, addVlan(plan, 20));
  // VLAN 20 persists even with no ports, so the column stays
  assert.ok((out.BridgeVLANs ?? []).some((b: any) => b.VLAN === 20), "empty VLAN 20 persisted");
  assert.equal((out.Devices ?? []).find((d: any) => d.Name === "br-lan").VLANFiltering, true);
  // the LAN interface was rebound to the native sub-device
  assert.equal((out.Interfaces ?? [])[0].Device, "br-lan.1", "native interface rebound under filtering");
  // and the column survives a project round-trip
  assert.ok(vlanColumns(projectFabric(out).plan).includes(20), "VLAN 20 column survives round-trip");
});

test("cycle: none → untagged(PVID) → tagged → none; one PVID per port", () => {
  let p: Plan = { vlans: [], ports: [] };
  p = cycle(p, "eth1", 10);
  assert.equal(cellOf(p, "eth1", 10), "untagged");
  p = cycle(p, "eth1", 10);
  assert.equal(cellOf(p, "eth1", 10), "tagged");
  p = cycle(p, "eth1", 10);
  assert.equal(cellOf(p, "eth1", 10), "none");
  // untagging in a second VLAN moves the PVID
  p = cycle(p, "eth1", 10); // untagged 10
  p = cycle(p, "eth1", 20); // untagged 20 → 10 must drop
  assert.equal(cellOf(p, "eth1", 20), "untagged");
  assert.equal(cellOf(p, "eth1", 10), "none");
});

test("vlanColumns always include Native; isRouted defaults true; transit toggles", () => {
  let p = addVlan({ vlans: [], ports: [] }, 20);
  assert.deepEqual(vlanColumns(p), [1, 20]);
  assert.equal(isRouted(p, 20), true);
  p = setRouted(p, 20, false);
  assert.equal(isRouted(p, 20), false);
  p = removeVlan(p, 20);
  assert.deepEqual(vlanColumns(p), [1]);
});

test("setFabric splices materialized L2 in, preserving leftovers + the rest", () => {
  const cfg: Cfg = {
    Interfaces: [{ Name: "lan", Device: "br-lan.10", Proto: { _tag: "static" } }],
    Devices: [
      { Name: "br-guest", Type: "bridge", Ports: ["eth9"] }, // a leftover bridge
      { Name: "br-lan", Type: "bridge", Ports: ["eth1"], VLANFiltering: true },
    ],
    BridgeVLANs: [{ Device: "br-lan", VLAN: 10, Ports: ["eth1:u*"] }],
    Zones: [{ Name: "lan", Networks: ["lan"] }],
  };
  const { plan } = projectFabric(cfg);
  const p2 = cycle(plan, "eth2", 10); // add eth2 untagged in 10
  const out = setFabric(cfg, p2);
  // leftover bridge + interfaces + zones preserved
  assert.ok((out.Devices ?? []).some((d: any) => d.Name === "br-guest"), "leftover bridge kept");
  assert.equal((out.Interfaces ?? []).length, 1, "interfaces untouched");
  assert.equal((out.Zones ?? []).length, 1, "zones untouched");
  // br-lan now has both ports
  const br = (out.Devices ?? []).find((d: any) => d.Name === "br-lan");
  assert.deepEqual(br.Ports.sort(), ["eth1", "eth2"]);
});
