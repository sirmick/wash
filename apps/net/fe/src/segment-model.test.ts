import { test } from "node:test";
import assert from "node:assert";
import {
  materializeSegment, removeSegment, projectDraft, segFormFrom, carrierLabel,
  type Cfg, type SegForm, type Segment,
} from "./segment-model.ts";

const lanForm = (over: Partial<SegForm> = {}): SegForm => ({
  name: "iot", carrierKind: "vlan", parent: "switch", vid: 6, port: "", members: [],
  address: "192.168.15.1/24", dhcp: true, start: 50, limit: 150, lease: "12h", dns: "", isolate: true,
  ...over,
});

const find = <T,>(xs: T[] | undefined, pred: (x: T) => boolean): T => {
  const x = (xs ?? []).find(pred);
  assert.ok(x, "expected to find a matching element");
  return x!;
};

test("materialize a VLAN LAN segment → Device + Interface + Zone + Pool", () => {
  const c = materializeSegment({}, lanForm({ dns: "192.168.15.1" }));

  const dev = find(c.Devices, (d) => d.Name === "switch.6");
  assert.equal(dev.Type, "8021q");
  assert.equal(dev.Ifname, "switch");
  assert.equal(dev.VID, 6);

  const iface = find(c.Interfaces, (i) => i.Name === "iot");
  assert.equal(iface.Device, "switch.6");
  assert.equal(iface.Proto._tag, "static");
  assert.deepEqual(iface.Proto.IPAddr, ["192.168.15.1/24"]);

  const zone = find(c.Zones, (z: any) => z.Name === "iot");
  assert.deepEqual(zone.Networks, ["iot"]);
  assert.equal(zone.Forward, "REJECT", "isolate=true → forward REJECT");

  const pool = find(c.Pools, (p: any) => p.Interface === "iot");
  assert.equal(pool.Start, 50);
  assert.equal(pool.Limit, 150);
  assert.equal(pool.LeaseTime, "12h");
  assert.deepEqual(pool.DHCPOption, ["6,192.168.15.1"], "per-client DNS → dhcp_option 6");
});

test("isolate=false → zone forward ACCEPT; dhcp=false → no pool", () => {
  const c = materializeSegment({}, lanForm({ isolate: false, dhcp: false }));
  assert.equal(find(c.Zones, (z: any) => z.Name === "iot").Forward, "ACCEPT");
  assert.equal((c.Pools ?? []).length, 0);
});

test("untagged port carrier → no device added, interface on the port", () => {
  const c = materializeSegment({}, lanForm({ carrierKind: "port", port: "eth1" }));
  assert.equal((c.Devices ?? []).length, 0);
  assert.equal(find(c.Interfaces, (i) => i.Name === "iot").Device, "eth1");
});

test("bridge carrier → bridge Device of N ports + interface/zone/pool; round-trips", () => {
  // a LAN spanning several adapters = a bridge of ports
  const c = materializeSegment({}, lanForm({ name: "lan", carrierKind: "bridge", members: ["eth1", "eth2", "eth3"], address: "10.0.0.1/24" }));
  const dev = find(c.Devices, (d) => d.Type === "bridge");
  assert.equal(dev.Name, "br-lan");
  assert.deepEqual(dev.Ports, ["eth1", "eth2", "eth3"]);
  const iface = find(c.Interfaces, (i) => i.Name === "lan");
  assert.equal(iface.Device, "br-lan", "interface sits on the bridge, not a raw port");
  find(c.Zones, (z: any) => z.Name === "lan");
  find(c.Pools, (p: any) => p.Interface === "lan");
  // the full bundle round-trips back to a bridge segment with its members
  const seg = projectDraft(c)[0];
  assert.equal(seg.carrier.kind, "bridge");
  assert.deepEqual(seg.carrier.members, ["eth1", "eth2", "eth3"]);
});

test("bridge carrier absorbs a bare interface that sat on a member port", () => {
  // eth1 had its own interface; bridging it should remove that interface
  const c0: Cfg = { Interfaces: [{ Name: "old1", Device: "eth1", Proto: { _tag: "dhcp" } }] };
  const c = materializeSegment(c0, lanForm({ name: "lan", carrierKind: "bridge", members: ["eth1", "eth2"], address: "10.0.0.1/24" }));
  assert.equal((c.Interfaces ?? []).filter((i) => i.Name === "old1").length, 0, "the bare member interface is absorbed");
  assert.equal(find(c.Interfaces, (i) => i.Name === "lan").Device, "br-lan");
});

test("edit replaces (no duplication); changing the VID swaps the vlan device", () => {
  const seg0 = projectDraft(materializeSegment({}, lanForm()))[0];
  const c0 = materializeSegment({}, lanForm());
  // re-edit the same segment: change range + VID
  const c1 = materializeSegment(c0, lanForm({ vid: 7, start: 100 }), seg0);

  assert.equal((c1.Interfaces ?? []).filter((i) => i.Name === "iot").length, 1, "one interface");
  assert.equal((c1.Zones ?? []).filter((z: any) => z.Name === "iot").length, 1, "one zone");
  assert.equal((c1.Pools ?? []).filter((p: any) => p.Interface === "iot").length, 1, "one pool");
  assert.equal(find(c1.Pools, (p: any) => p.Interface === "iot").Start, 100);
  // old switch.6 gone, new switch.7 present
  assert.equal((c1.Devices ?? []).filter((d) => d.Name === "switch.6").length, 0, "old vlan device removed");
  assert.equal((c1.Devices ?? []).filter((d) => d.Name === "switch.7").length, 1, "new vlan device added");
  assert.equal(find(c1.Interfaces, (i) => i.Name === "iot").Device, "switch.7");
});

test("remove strips the whole bundle", () => {
  const c0 = materializeSegment({}, lanForm());
  const seg = projectDraft(c0)[0];
  const c1 = removeSegment(c0, seg);
  assert.equal((c1.Interfaces ?? []).length, 0);
  assert.equal((c1.Devices ?? []).length, 0);
  assert.equal((c1.Zones ?? []).length, 0);
  assert.equal((c1.Pools ?? []).length, 0);
});

test("projectDraft round-trips a materialized segment; loopback omitted", () => {
  let c: Cfg = { Interfaces: [{ Name: "loopback", Device: "lo", Proto: { _tag: "static", IPAddr: ["127.0.0.1/8"] } }] };
  c = materializeSegment(c, lanForm());
  const segs = projectDraft(c);
  assert.equal(segs.length, 1, "loopback omitted");
  const s = segs[0];
  assert.equal(s.name, "iot");
  assert.equal(s.role, "lan");
  assert.deepEqual(s.carrier, { kind: "vlan", port: "switch", vid: 6 });
  assert.equal(s.zone, "iot");
  assert.equal(s.pool, "iot");
  assert.deepEqual(s.addrs, ["192.168.15.1/24"]);
});

test("segFormFrom prefills the wizard from an existing segment", () => {
  const c = materializeSegment({}, lanForm({ isolate: false, dns: "10.0.0.53" }));
  const seg = projectDraft(c)[0];
  const f = segFormFrom(c, seg, ["switch", "eth0"], ["eth1"]);
  assert.equal(f.name, "iot");
  assert.equal(f.carrierKind, "vlan");
  assert.equal(f.parent, "switch");
  assert.equal(f.vid, 6);
  assert.equal(f.address, "192.168.15.1/24");
  assert.equal(f.dhcp, true);
  assert.equal(f.dns, "10.0.0.53");
  assert.equal(f.isolate, false);
});

// The target: many segments, distinct per-zone policy, all in one config.
test("multi-segment with different per-zone policies", () => {
  let c: Cfg = {};
  c = materializeSegment(c, lanForm({ name: "iot", vid: 6, address: "192.168.15.1/24", isolate: true }));
  c = materializeSegment(c, lanForm({ name: "cam", vid: 3, address: "192.168.3.1/24", isolate: true, dhcp: false }));
  c = materializeSegment(c, lanForm({ name: "lan", carrierKind: "port", port: "eth1", address: "10.0.0.1/24", isolate: false }));

  const segs = projectDraft(c);
  assert.equal(segs.length, 3);
  const byName = Object.fromEntries(segs.map((s: Segment) => [s.name, s]));
  assert.equal(byName["iot"].carrier.kind, "vlan");
  assert.equal(byName["cam"].pool, undefined, "cam has no DHCP pool");
  assert.equal(byName["lan"].carrier.kind, "untagged");

  // distinct zone forward policies survive side by side
  const forward = (name: string) => find(c.Zones, (z: any) => z.Name === name).Forward;
  assert.equal(forward("iot"), "REJECT");
  assert.equal(forward("cam"), "REJECT");
  assert.equal(forward("lan"), "ACCEPT");

  // three vlan/port carriers, two pools (lan has dhcp on by default in lanForm; cam off)
  assert.equal((c.Pools ?? []).length, 2);
  assert.equal((c.Devices ?? []).filter((d) => d.Type === "8021q").length, 2, "iot + cam vlan devices");
});

test("carrierLabel renders each carrier kind", () => {
  assert.equal(carrierLabel({ kind: "vlan", port: "switch", vid: 6 }), "VLAN 6 on switch");
  assert.equal(carrierLabel({ kind: "bridge", members: ["eth1", "eth2"] }), "bridge of eth1, eth2");
  assert.equal(carrierLabel({ kind: "untagged", port: "eth0" }), "port eth0");
});

const wanForm = (over: Partial<SegForm> = {}): SegForm => ({
  ...lanForm(), name: "wan", role: "wan", carrierKind: "port", port: "eth1", proto: "dhcp", dhcp: false,
  ...over,
});

test("WAN uplink: DHCP interface + masq zone, no pool, no device", () => {
  const c = materializeSegment({}, wanForm());
  const iface = find(c.Interfaces, (i) => i.Name === "wan");
  assert.equal(iface.Device, "eth1");
  assert.equal(iface.Proto._tag, "dhcp");
  assert.equal((c.Pools ?? []).length, 0, "WAN has no DHCP server");
  const zone = find(c.Zones, (z: any) => z.Name === "wan");
  assert.equal(zone.Masq, true);
  assert.equal(projectDraft(c)[0].role, "wan");
});

test("WAN static: static interface + masq zone", () => {
  const c = materializeSegment({}, wanForm({ proto: "static", address: "203.0.113.2/24" }));
  const iface = find(c.Interfaces, (i) => i.Name === "wan");
  assert.equal(iface.Proto._tag, "static");
  assert.deepEqual(iface.Proto.IPAddr, ["203.0.113.2/24"]);
});

test("WAN reuses an existing (multi-network) wan zone rather than duplicating it", () => {
  // mimic the stock OpenWRT wan zone (spans wan + wan6, masq) with no wan iface yet
  const stock: Cfg = { Zones: [{ Name: "wan", Networks: ["wan", "wan6"], Input: "REJECT", Masq: true }] };
  const c = materializeSegment(stock, wanForm());
  assert.equal((c.Zones ?? []).filter((z: any) => z.Name === "wan").length, 1, "no duplicate wan zone");
  assert.deepEqual((c.Zones as any[])[0].Networks, ["wan", "wan6"], "stock zone untouched");
  // the new wan interface makes it a WAN segment (masq zone contains it)
  assert.equal(projectDraft(c).find((s) => s.name === "wan")!.role, "wan");
});
