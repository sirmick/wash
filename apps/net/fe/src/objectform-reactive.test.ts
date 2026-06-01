// Reactive-logic test (Tier A): exercises Solid reactivity directly under
// node:test — no DOM, no JSX, no router/chromium. It requires
// `--conditions=browser` (test.sh's fe-unit step passes it); under the
// default Node conditions solid-js resolves its non-reactive SSR build and
// the memo below would NOT recompute, so this test also pins that the
// runner keeps the flag.
//
// What it guards: the ObjectForm variant switch that net-app.spec.ts:106
// drives through the DOM — here at the reactive-derivation layer, in
// milliseconds. (The DOM event wiring itself is covered by the component
// test, objectform.ctest.tsx.)

import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { createRoot, createSignal, createMemo } from 'solid-js';
import {
  buildForm,
  descriptorFor,
  type Descriptor,
  type FieldView,
  type GroupView,
} from './objectform-model.ts';

const here = dirname(fileURLToPath(import.meta.url));
const desc: Descriptor = JSON.parse(readFileSync(join(here, 'generated/descriptor.json'), 'utf8'));
const iface = descriptorFor(desc, 'network/interface')!;

function protoField(groups: GroupView[]): FieldView {
  for (const g of groups) {
    const f = g.fields.find((x) => x.field.name === 'Proto');
    if (f) return f;
  }
  throw new Error('Proto field not found');
}
const widgets = (fv: FieldView) => (fv.union?.fields ?? []).map((f) => f.field.widget).sort();

test('Proto union re-derives reactively when the discriminator tag changes', () => {
  createRoot((dispose) => {
    const [value, setValue] = createSignal<Record<string, unknown>>({
      Name: 'lan',
      Device: 'eth0',
      Proto: { _tag: 'dhcp' },
    });
    const form = createMemo(() => buildForm(iface, value(), [], { pathPrefix: 'Interfaces[0]' }));

    // DHCP: per-family toggles + hostname, no static address fields.
    let proto = protoField(form().groups);
    assert.equal(proto.union!.tag, 'dhcp');
    assert.deepEqual(widgets(proto), ['text', 'toggle', 'toggle']);
    assert.ok(!widgets(proto).includes('cidr'));

    // Switch to static: the memo MUST recompute and surface cidr fields.
    setValue((v) => ({ ...v, Proto: { _tag: 'static', IPAddr: '192.168.1.2/24' } }));
    proto = protoField(form().groups);
    assert.equal(proto.union!.tag, 'static');
    assert.ok(widgets(proto).includes('cidr'), 'static variant exposes cidr fields after the reactive switch');

    // Switch to none (the "Disabled" choice): no variant fields at all.
    setValue((v) => ({ ...v, Proto: { _tag: 'none' } }));
    proto = protoField(form().groups);
    assert.equal(proto.union!.tag, 'none');
    assert.deepEqual(proto.union!.fields, []);

    dispose();
  });
});
