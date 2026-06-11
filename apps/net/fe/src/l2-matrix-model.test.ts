import { test } from "node:test";
import assert from "node:assert";
import {
  bridgeVlans, bridgePorts, isVlanAware, cellState, setCell, cycleCell, addVlan, removeVlan, isTransit, setTransit,
} from "./l2-matrix-model.ts";
import type { Cfg } from "./segment-model.ts";

const bridge = (): Cfg => ({
  Devices: [{ Name: "br-lan", Type: "bridge", Ports: ["eth1", "eth2", "eth3", "eth4"] }],
});

test("addVlan turns the bridge VLAN-aware; removeVlan of the last turns it off", () => {
  let c = bridge();
  assert.equal(isVlanAware(c, "br-lan"), false);
  c = addVlan(c, "br-lan", 10);
  c = addVlan(c, "br-lan", 20);
  assert.deepEqual(bridgeVlans(c, "br-lan"), [10, 20]);
  assert.equal(isVlanAware(c, "br-lan"), true);
  c = removeVlan(c, "br-lan", 10);
  assert.equal(isVlanAware(c, "br-lan"), true, "still has VLAN 20");
  c = removeVlan(c, "br-lan", 20);
  assert.equal(isVlanAware(c, "br-lan"), false, "last VLAN gone → filtering off");
});

test("cycleCell: none → untagged(PVID) → tagged → none", () => {
  let c = addVlan(bridge(), "br-lan", 10);
  assert.deepEqual(cellState(c, "br-lan", "eth1", 10), { state: "none", pvid: false });
  c = cycleCell(c, "br-lan", "eth1", 10);
  assert.deepEqual(cellState(c, "br-lan", "eth1", 10), { state: "untagged", pvid: true });
  c = cycleCell(c, "br-lan", "eth1", 10);
  assert.deepEqual(cellState(c, "br-lan", "eth1", 10), { state: "tagged", pvid: false });
  c = cycleCell(c, "br-lan", "eth1", 10);
  assert.deepEqual(cellState(c, "br-lan", "eth1", 10), { state: "none", pvid: false });
});

test("one PVID per port: untagging a port in a new VLAN clears its untagged elsewhere", () => {
  let c = addVlan(addVlan(bridge(), "br-lan", 10), "br-lan", 20);
  c = setCell(c, "br-lan", "eth1", 10, "untagged");
  assert.equal(cellState(c, "br-lan", "eth1", 10).state, "untagged");
  // now make eth1 untagged in VLAN 20 → it can't be untagged in two VLANs
  c = setCell(c, "br-lan", "eth1", 20, "untagged");
  assert.equal(cellState(c, "br-lan", "eth1", 20).state, "untagged");
  assert.equal(cellState(c, "br-lan", "eth1", 10).state, "none", "untagged membership moved, not duplicated");
});

test("tagged in many VLANs is fine (a trunk port)", () => {
  let c = addVlan(addVlan(bridge(), "br-lan", 10), "br-lan", 20);
  c = setCell(c, "br-lan", "eth4", 10, "tagged");
  c = setCell(c, "br-lan", "eth4", 20, "tagged");
  assert.equal(cellState(c, "br-lan", "eth4", 10).state, "tagged");
  assert.equal(cellState(c, "br-lan", "eth4", 20).state, "tagged");
});

test("setTransit flips routed⇄transit via the local flag; default is routed", () => {
  let c = addVlan(bridge(), "br-lan", 30);
  assert.equal(isTransit(c, "br-lan", 30), false, "new VLANs default routed");
  c = setTransit(c, "br-lan", 30, true);
  assert.equal(isTransit(c, "br-lan", 30), true);
  assert.equal((c.BridgeVLANs as any[]).find((b) => b.VLAN === 30).Local, "0");
  c = setTransit(c, "br-lan", 30, false);
  assert.equal(isTransit(c, "br-lan", 30), false);
  assert.equal((c.BridgeVLANs as any[]).find((b) => b.VLAN === 30).Local, "", "routed clears local (→ OpenWRT default 1)");
});

test("reads existing bridge-vlan config (incl. external :u / :t / bare entries)", () => {
  const c: Cfg = {
    Devices: [{ Name: "br-lan", Type: "bridge", Ports: ["eth1", "eth4"], VLANFiltering: true }],
    BridgeVLANs: [
      { Device: "br-lan", VLAN: 1, Ports: ["eth1:u*", "eth4:u*"] },
      { Device: "br-lan", VLAN: 20, Ports: ["eth4"] }, // bare = tagged
    ],
  };
  assert.deepEqual(bridgePorts(c, "br-lan"), ["eth1", "eth4"]);
  assert.deepEqual(bridgeVlans(c, "br-lan"), [1, 20]);
  assert.deepEqual(cellState(c, "br-lan", "eth1", 1), { state: "untagged", pvid: true });
  assert.deepEqual(cellState(c, "br-lan", "eth4", 20), { state: "tagged", pvid: false });
  assert.deepEqual(cellState(c, "br-lan", "eth1", 20), { state: "none", pvid: false });
});
