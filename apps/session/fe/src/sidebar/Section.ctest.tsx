// Component test (Tier B): the rail's clickable divs are keyboard controls.
// A section header and the hidden sidebar's tab each toggle on click; as
// plain divs they could not be reached or activated from the keyboard.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { Section } from './Section.tsx';
import { Sidebar } from './Sidebar.tsx';

afterEach(cleanup);

test('a section header is a keyboard stop that Enter and Space toggle', () => {
  let toggles = 0;
  const { getByTestId } = render(() => (
    <Section id="agents" title="Agents" state="expanded" onToggle={() => toggles++}>
      <div />
    </Section>
  ));
  const header = getByTestId('sidebar-section-header-agents');
  expect(header.getAttribute('role')).toBe('button');
  expect(header.getAttribute('tabindex')).toBe('0');
  expect(header.getAttribute('aria-expanded')).toBe('true');
  fireEvent.keyDown(header, { key: 'Enter' });
  fireEvent.keyDown(header, { key: ' ' });
  fireEvent.keyDown(header, { key: 'x' });
  expect(toggles).toBe(2);
});

test('the hidden sidebar tab is a keyboard stop that Enter opens', () => {
  let toggles = 0;
  const { getByTestId } = render(() => (
    <Sidebar mode="hidden" taskbarPos="bottom" taskbarHeight={32} onToggle={() => toggles++} />
  ));
  const tab = getByTestId('sidebar-tab');
  expect(tab.getAttribute('role')).toBe('button');
  expect(tab.getAttribute('tabindex')).toBe('0');
  fireEvent.keyDown(tab, { key: 'Enter' });
  expect(toggles).toBe(1);
});
