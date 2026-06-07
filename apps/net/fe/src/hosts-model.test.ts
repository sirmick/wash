import { test } from "node:test";
import assert from "node:assert";
import { projectHosts, upsertHost, removeHost, type HostEntry } from "./hosts-model.ts";
import type { Cfg } from "./segment-model.ts";

const has = (xs: any[] | undefined, pred: (x: any) => boolean) => (xs ?? []).some(pred);

test("MAC present → a DHCP reservation (config host)", () => {
  const c = upsertHost({}, { name: "printer", ip: "10.0.0.20", mac: "aa:bb:cc:dd:ee:ff" });
  assert.ok(has(c.Hosts, (h) => h.Hostname === "printer" && h.MAC === "aa:bb:cc:dd:ee:ff" && h.IP === "10.0.0.20"));
  assert.equal((c.Domains ?? []).length, 0);
});

test("MAC empty → a static DNS record (config domain)", () => {
  const c = upsertHost({}, { name: "nas.example.com", ip: "10.0.0.5" });
  assert.ok(has(c.Domains, (d) => d.Name === "nas.example.com" && d.IP === "10.0.0.5"));
  assert.equal((c.Hosts ?? []).length, 0);
});

test("projectHosts flattens both kinds into the unified list", () => {
  let c: Cfg = {};
  c = upsertHost(c, { name: "printer", ip: "10.0.0.20", mac: "aa:bb:cc:dd:ee:ff" });
  c = upsertHost(c, { name: "nas", ip: "10.0.0.5" });
  const list = projectHosts(c);
  const by = Object.fromEntries(list.map((e) => [e.name, e]));
  assert.equal(by["printer"].mac, "aa:bb:cc:dd:ee:ff");
  assert.equal(by["printer"].ip, "10.0.0.20");
  assert.equal(by["nas"].mac, undefined);
  assert.equal(by["nas"].ip, "10.0.0.5");
});

test("edit replaces in place (no duplication)", () => {
  let c: Cfg = upsertHost({}, { name: "nas", ip: "10.0.0.5" });
  c = upsertHost(c, { name: "nas", ip: "10.0.0.6" }, "nas");
  assert.equal((c.Domains ?? []).length, 1);
  assert.equal((c.Domains as any[])[0].IP, "10.0.0.6");
});

test("flipping reservation↔DNS moves the entry between kinds, no dup", () => {
  // start as a reservation
  let c: Cfg = upsertHost({}, { name: "nas", ip: "10.0.0.5", mac: "aa:bb:cc:dd:ee:ff" });
  assert.equal((c.Hosts ?? []).length, 1);
  // drop the MAC → becomes a pure DNS record; the host entry must be gone
  c = upsertHost(c, { name: "nas", ip: "10.0.0.5" }, "nas");
  assert.equal((c.Hosts ?? []).length, 0, "reservation removed");
  assert.equal((c.Domains ?? []).length, 1, "now a domain");
  // add a MAC again → back to a reservation, domain gone
  c = upsertHost(c, { name: "nas", ip: "10.0.0.5", mac: "aa:bb:cc:dd:ee:ff" }, "nas");
  assert.equal((c.Hosts ?? []).length, 1);
  assert.equal((c.Domains ?? []).length, 0);
});

test("remove strips the entry of either kind", () => {
  let c: Cfg = {};
  c = upsertHost(c, { name: "printer", ip: "10.0.0.20", mac: "aa:bb:cc:dd:ee:ff" });
  c = upsertHost(c, { name: "nas", ip: "10.0.0.5" });
  c = removeHost(c, { name: "printer", ip: "10.0.0.20" });
  c = removeHost(c, { name: "nas", ip: "10.0.0.5" });
  assert.equal((c.Hosts ?? []).length, 0);
  assert.equal((c.Domains ?? []).length, 0);
});

test("upsert leaves unrelated records untouched", () => {
  let c: Cfg = {};
  c = upsertHost(c, { name: "a", ip: "10.0.0.1" });
  c = upsertHost(c, { name: "b", ip: "10.0.0.2" });
  c = upsertHost(c, { name: "a", ip: "10.0.0.9" }, "a");
  const list = projectHosts(c);
  assert.equal(list.length, 2);
  assert.equal(list.find((e: HostEntry) => e.name === "b")!.ip, "10.0.0.2");
});
