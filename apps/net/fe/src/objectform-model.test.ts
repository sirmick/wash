// Unit tests for the pure ObjectForm model, run against the REAL generated
// descriptor (apps/net/fe/src/generated/descriptor.json).
//   npx tsx --test apps/net/fe/src/objectform-model.test.ts

import { test } from "node:test";
import { strict as assert } from "node:assert";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import {
  buildForm,
  descriptorFor,
  type Descriptor,
  type FieldView,
  type GroupView,
} from "./objectform-model.ts";

const here = dirname(fileURLToPath(import.meta.url));
const desc: Descriptor = JSON.parse(readFileSync(join(here, "generated/descriptor.json"), "utf8"));

function field(groups: GroupView[], name: string): FieldView | undefined {
  for (const g of groups) {
    const f = g.fields.find((x) => x.field.name === name);
    if (f) return f;
  }
  return undefined;
}

test("every descriptor object builds without throwing", () => {
  for (const o of desc.objects) {
    const fv = buildForm(o, {}, [], { pathPrefix: `${o.goType}[0]` });
    assert.ok(fv.groups.length >= 0);
  }
});

test("interface static proto: union resolves, variant fields appear", () => {
  const iface = descriptorFor(desc, "network/interface")!;
  const value = {
    Name: "lan",
    Device: "br-lan",
    Proto: { _tag: "static", IPAddr: "192.168.1.1/24" },
  };
  const form = buildForm(iface, value, [], { pathPrefix: "Interfaces[0]" });

  const proto = field(form.groups, "Proto")!;
  assert.ok(proto.union, "Proto should carry a union view");
  assert.equal(proto.union!.tag, "static");
  assert.ok(proto.union!.options.includes("dhcp") && proto.union!.options.includes("wireguard"));

  const ipaddr = proto.union!.fields.find((f) => f.field.name === "IPAddr");
  assert.ok(ipaddr, "static variant should expose IPAddr");
  assert.equal(ipaddr!.value, "192.168.1.1/24");
  assert.equal(ipaddr!.path, "Interfaces[0].Proto.IPAddr");
  assert.equal(ipaddr!.field.widget, "cidr");
});

test("union switches variant by _tag", () => {
  const iface = descriptorFor(desc, "network/interface")!;
  const form = buildForm(
    iface,
    { Name: "wan", Proto: { _tag: "pppoe", Username: "u" } },
    [],
    { pathPrefix: "Interfaces[0]" },
  );
  const proto = field(form.groups, "Proto")!;
  assert.equal(proto.union!.tag, "pppoe");
  const names = proto.union!.fields.map((f) => f.field.name);
  assert.ok(names.includes("Username"));
  assert.ok(!names.includes("IPAddr"), "should not show static fields under pppoe");
});

test("formerly-advanced fields always show (no basic/advanced split)", () => {
  const iface = descriptorFor(desc, "network/interface")!;
  const value = { Name: "lan", Proto: { _tag: "static", IPAddr: "10.0.0.1/24", DNS: ["1.1.1.1"] } };

  // DNS carries advanced:true in the descriptor; it must still render — the
  // distinction was removed, every field is always visible.
  const form = buildForm(iface, value, [], { pathPrefix: "Interfaces[0]" });
  const dns = field(form.groups, "Proto")!.union!.fields.find((f) => f.field.name === "DNS");
  assert.ok(dns, "DNS (advanced) should always show");
});

test("diagnostics map onto fields by path (incl. union subfields)", () => {
  const iface = descriptorFor(desc, "network/interface")!;
  const diags = [
    { path: "Interfaces[0].Name", code: "required", message: "Name is required", severity: "error" as const },
    { path: "Interfaces[0].Proto.IPAddr", code: "required", message: "needs an ipaddr", severity: "error" as const },
  ];
  const form = buildForm(iface, { Proto: { _tag: "static" } }, diags, { pathPrefix: "Interfaces[0]" });

  assert.equal(field(form.groups, "Name")!.error, "Name is required");
  const ipaddr = field(form.groups, "Proto")!.union!.fields.find((f) => f.field.name === "IPAddr")!;
  assert.equal(ipaddr.error, "needs an ipaddr");
  assert.equal(ipaddr.severity, "error");
});

test("error preferred over warning on the same path; int severity tolerated", () => {
  const zone = descriptorFor(desc, "firewall/zone")!;
  const diags = [
    { path: "Zones[0].Input", code: "x", message: "warn", severity: 1 },
    { path: "Zones[0].Input", code: "y", message: "err", severity: 0 },
  ];
  const form = buildForm(zone, { Name: "lan" }, diags, { pathPrefix: "Zones[0]" });
  const input = field(form.groups, "Input")!;
  assert.equal(input.error, "err");
  assert.equal(input.severity, "error");
});

test("widget + ref metadata flow through (mac, list-ref)", () => {
  const host = descriptorFor(desc, "dhcp/host")!;
  const hf = buildForm(host, {}, [], { pathPrefix: "Hosts[0]" });
  assert.equal(field(hf.groups, "MAC")!.field.widget, "mac");

  const zone = descriptorFor(desc, "firewall/zone")!;
  const zf = buildForm(zone, {}, [], { pathPrefix: "Zones[0]" });
  const nets = field(zf.groups, "Networks")!;
  assert.equal(nets.field.ref, "interface");
  assert.equal(nets.field.list, true);
});

test("fields are grouped; section-name lands in a group", () => {
  const iface = descriptorFor(desc, "network/interface")!;
  const form = buildForm(iface, { Name: "lan", Proto: { _tag: "dhcp" } }, [], { pathPrefix: "Interfaces[0]" });
  assert.ok(field(form.groups, "Name"), "Name (section-name) should be present");
  assert.ok(form.groups.length >= 1);
});

test("fieldless variant (none) builds without throwing", () => {
  const iface = descriptorFor(desc, "network/interface")!;
  // NoneProto has no fields → variant.fields is null in the descriptor; buildForm
  // must not throw (a throw here freezes the whole reactive form).
  const form = buildForm(iface, { Proto: { _tag: "none" } }, [], { pathPrefix: "Interfaces[0]" });
  const proto = field(form.groups, "Proto")!;
  assert.equal(proto.union!.tag, "none");
  assert.deepEqual(proto.union!.fields, []);
});
