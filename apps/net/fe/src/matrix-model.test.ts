import { test } from "node:test";
import assert from "node:assert";
import { matrixZones, cellState, setForward, toggleForward, setInput } from "./matrix-model.ts";
import type { Cfg } from "./segment-model.ts";

// A small multi-zone router: two LAN segments + a masquerading WAN.
const base = (): Cfg => ({
  Zones: [
    { Name: "iot", Networks: ["iot"], Input: "ACCEPT", Forward: "REJECT" },
    { Name: "lan", Networks: ["lan"], Input: "ACCEPT", Forward: "REJECT" },
    { Name: "wan", Networks: ["wan"], Input: "REJECT", Forward: "REJECT", Masq: true },
  ],
  Forwardings: [{ Src: "lan", Dest: "wan" }],
});

test("matrixZones surfaces name/masq/input", () => {
  const zs = matrixZones(base());
  assert.deepEqual(zs.map((z) => z.name), ["iot", "lan", "wan"]);
  assert.equal(zs.find((z) => z.name === "wan")!.masq, true);
  assert.equal(zs.find((z) => z.name === "wan")!.input, "REJECT");
});

test("cellState: blanket forwarding = allow, otherwise block", () => {
  const c = base();
  assert.equal(cellState(c, "lan", "wan"), "allow"); // the seeded forwarding
  assert.equal(cellState(c, "iot", "wan"), "block"); // iot has no internet
  assert.equal(cellState(c, "iot", "lan"), "block"); // inter-LAN denied
});

test("cellState: a src→dest FirewallRule reads as custom", () => {
  const c = base();
  c.FwRules = [{ Name: "iot-nas", Src: "iot", Dest: "lan", Proto: "tcp", DestPort: "445", Target: "ACCEPT" }];
  assert.equal(cellState(c, "iot", "lan"), "custom");
});

test("setForward adds/removes the blanket forwarding (idempotent, no dups)", () => {
  let c = base();
  c = setForward(c, "iot", "wan", true);
  assert.equal(cellState(c, "iot", "wan"), "allow");
  // re-allow must not duplicate
  c = setForward(c, "iot", "wan", true);
  assert.equal((c.Forwardings ?? []).filter((f: any) => f.Src === "iot" && f.Dest === "wan").length, 1);
  // block removes it; the unrelated lan→wan survives
  c = setForward(c, "iot", "wan", false);
  assert.equal(cellState(c, "iot", "wan"), "block");
  assert.equal(cellState(c, "lan", "wan"), "allow");
});

test("toggleForward flips block↔allow", () => {
  let c = base();
  assert.equal(cellState(c, "iot", "lan"), "block");
  c = toggleForward(c, "iot", "lan");
  assert.equal(cellState(c, "iot", "lan"), "allow");
  c = toggleForward(c, "iot", "lan");
  assert.equal(cellState(c, "iot", "lan"), "block");
});

test("setForward never disturbs a custom cell's rules", () => {
  let c = base();
  c.FwRules = [{ Name: "iot-nas", Src: "iot", Dest: "lan", DestPort: "445", Target: "ACCEPT" }];
  c = setForward(c, "iot", "wan", true); // edit a different cell
  assert.equal(cellState(c, "iot", "lan"), "custom", "rules untouched");
  assert.equal((c.FwRules ?? []).length, 1);
});

test("setInput flips the Router-column policy for one zone only", () => {
  let c = base();
  c = setInput(c, "iot", "REJECT");
  assert.equal(matrixZones(c).find((z) => z.name === "iot")!.input, "REJECT");
  assert.equal(matrixZones(c).find((z) => z.name === "lan")!.input, "ACCEPT", "other zones unchanged");
});

// The target: distinct per-zone policy expressed as a matrix — guest internet-only,
// cam fully isolated, lan full + reaches a service on iot.
test("multi-zone distinct policies compose in the matrix", () => {
  let c: Cfg = {
    Zones: [
      { Name: "lan", Input: "ACCEPT", Forward: "REJECT" },
      { Name: "guest", Input: "REJECT", Forward: "REJECT" },
      { Name: "cam", Input: "REJECT", Forward: "REJECT" },
      { Name: "iot", Input: "ACCEPT", Forward: "REJECT" },
      { Name: "wan", Input: "REJECT", Forward: "REJECT", Masq: true },
    ],
  };
  c = setForward(c, "lan", "wan", true);    // lan: internet
  c = setForward(c, "guest", "wan", true);  // guest: internet-only
  c = setForward(c, "iot", "wan", true);    // iot: internet
  c = setForward(c, "lan", "iot", true);    // lan → iot (manage devices)
  // cam: nothing (no forwardings) — fully isolated

  assert.equal(cellState(c, "guest", "wan"), "allow");
  assert.equal(cellState(c, "guest", "lan"), "block"); // guest can't reach lan
  assert.equal(cellState(c, "cam", "wan"), "block");   // cam no internet
  assert.equal(cellState(c, "cam", "lan"), "block");   // cam isolated
  assert.equal(cellState(c, "lan", "iot"), "allow");
  assert.equal(cellState(c, "iot", "lan"), "block");   // not reciprocal
  assert.equal((c.Forwardings ?? []).length, 4);
});
