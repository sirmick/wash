// Component test (Tier B): mounts the real ObjectForm in jsdom and drives
// the Proto discriminator <select> with real DOM events — the reactive
// wiring net-app.spec.ts:106 covers, but here with no router, no chromium,
// in milliseconds. Complements objectform-reactive.test.ts (which checks
// the same variant switch at the pure-reactive layer).

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { createSignal } from 'solid-js';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { ObjectForm } from './ObjectForm.tsx';
import { descriptorFor, type Descriptor } from './objectform-model.ts';

const here = dirname(fileURLToPath(import.meta.url));
const desc: Descriptor = JSON.parse(readFileSync(join(here, 'generated/descriptor.json'), 'utf8'));
const iface = descriptorFor(desc, 'network/interface')!;

afterEach(cleanup);

test('Proto <select> reactively swaps variant fields and emits the right path', () => {
  const [value, setValue] = createSignal<Record<string, any>>({
    Name: 'lan',
    Device: 'eth0',
    Proto: { _tag: 'dhcp' },
  });
  const calls: Array<[string, unknown]> = [];

  const { container } = render(() => (
    <ObjectForm
      object={iface}
      value={value()}
      pathPrefix="Interfaces[0]"
      onChange={(path, v) => {
        calls.push([path, v]);
        // Apply the edit so the form re-derives — the union lives directly
        // under the interface, so the path is "<prefix>.Proto".
        if (path === 'Interfaces[0].Proto') setValue((prev) => ({ ...prev, Proto: v as any }));
      }}
    />
  ));

  const select = () => container.querySelector('.wash-net-method select') as HTMLSelectElement;
  const cidrs = () => container.querySelectorAll('input[data-widget="cidr"]');
  const lists = () => container.querySelectorAll('textarea');
  const checks = () => container.querySelectorAll('input[type="checkbox"]');

  // DHCP: per-family toggles, no static address fields.
  expect(select().value).toBe('dhcp');
  expect(cidrs().length).toBe(0);
  expect(checks().length).toBe(2); // IPv4 + IPv6

  // → static: the select emits onChange at the Proto path, and once applied
  // the cidr address inputs render (reactive re-derivation through the DOM).
  fireEvent.change(select(), { target: { value: 'static' } });
  expect(calls.at(-1)).toEqual(['Interfaces[0].Proto', { _tag: 'static' }]);
  expect(select().value).toBe('static');
  expect(cidrs().length).toBe(1); // IP6Addr (IPAddr is a list-cidr textarea now)
  expect(lists().length).toBe(2); // IPAddr (list-cidr) + DNS (list-ip)

  // → none ("Disabled"): no variant fields at all.
  fireEvent.change(select(), { target: { value: 'none' } });
  expect(select().value).toBe('none');
  expect(cidrs().length).toBe(0);
  expect(lists().length).toBe(0);
  expect(checks().length).toBe(0);

  // → back to DHCP: toggles return.
  fireEvent.change(select(), { target: { value: 'dhcp' } });
  expect(checks().length).toBe(2);
  expect(cidrs().length).toBe(0);
});
