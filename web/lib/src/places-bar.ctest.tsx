// Component test for the Places icon bar (docs/PLACES.md §4.4).

import { afterEach, describe, expect, test } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import {
  PlacesBar,
  groupTint,
  groupTintIndex,
  PLACES_AGENT,
  PLACES_EDITOR,
  PLACES_FILES,
  PLACES_TERMINAL,
  type PlacesView,
} from './places-bar';

afterEach(cleanup);

const alone: PlacesView = { group: '', members: {} };

describe('groupTint', () => {
  test('no group, no tint', () => {
    expect(groupTint('')).toBeUndefined();
    expect(groupTintIndex('')).toBe(-1);
  });

  test('is a pure function of the group id', () => {
    // Every member computes its own tint from the id it holds. If two calls
    // could disagree, two windows of one group could show different colours.
    for (const g of ['a3f09c1e22d4b870', 'x', '0000000000000000']) {
      expect(groupTint(g)).toBe(groupTint(g));
      expect(groupTintIndex(g)).toBe(groupTintIndex(g));
    }
  });

  test('is a low-alpha wash, not a fill', () => {
    // Legible behind the bar's own content in both themes.
    expect(groupTint('abc')).toMatch(/^color-mix\(in srgb, var\(--wash-accent-[a-z]+.*\) 16%, transparent\)$/);
  });

  test('never uses an app accent colour or red', () => {
    // A group tinted Files-blue would read as "this is about Files".
    const banned = ['blue', 'green', 'amber', 'violet', 'red'];
    for (let i = 0; i < 400; i++) {
      const t = groupTint(i.toString(16).padStart(16, '0'))!;
      for (const b of banned) expect(t).not.toContain(`--wash-accent-${b},`);
    }
  });

  test('spreads random ids across the palette', () => {
    // Group ids are 16 random hex chars. A hash that clustered them would
    // give most groups the same colour and defeat the point of tinting.
    const seen = new Map<number, number>();
    const ids = new Set<string>();
    // An LCG's HIGH bits: the low bits of a power-of-two LCG repeat every
    // 16 steps, which made every 16-char id identical and this test measure
    // one string 600 times.
    let seed = 12345;
    const rnd = () => ((seed = (Math.imul(seed, 1103515245) + 12345) >>> 0) >>> 28).toString(16);
    for (let i = 0; i < 600; i++) {
      const id = Array.from({ length: 16 }, rnd).join('');
      ids.add(id);
      const k = groupTintIndex(id);
      seen.set(k, (seen.get(k) ?? 0) + 1);
    }
    expect(ids.size).toBe(600); // the inputs really are distinct
    expect(seen.size).toBe(6);
    for (const n of seen.values()) expect(n).toBeGreaterThan(600 / 6 / 2);
  });
});

describe('PlacesBar', () => {
  test('shows the other three apps, never itself, in a fixed order', () => {
    const r = render(() => <PlacesBar self={PLACES_FILES} view={alone} onOpen={() => {}} />);
    const ids = [...r.getByTestId('places-bar').querySelectorAll('[data-testid^="places-"]')].map((e) => e.getAttribute('data-testid'));
    expect(ids).toEqual(['places-ai', 'places-edit', 'places-term']);
  });

  test('a window in no group has no tint and every icon unbound', () => {
    const r = render(() => <PlacesBar self={PLACES_AGENT} view={alone} onOpen={() => {}} />);
    const bar = r.getByTestId('places-bar');
    expect(bar.getAttribute('data-group')).toBeNull();
    expect(bar.style.background).toBe('');
    for (const id of ['places-fm', 'places-edit', 'places-term']) {
      expect(r.getByTestId(id).getAttribute('data-bound')).toBe('false');
      expect(r.getByTestId(id).getAttribute('aria-pressed')).toBe('false');
    }
  });

  test('bound members are marked, and the bar carries the group tint', () => {
    const view: PlacesView = { group: 'g1', members: { [PLACES_TERMINAL]: 't1' } };
    const r = render(() => <PlacesBar self={PLACES_AGENT} view={view} onOpen={() => {}} />);
    expect(r.getByTestId('places-bar').getAttribute('data-group')).toBe('g1');
    expect(r.getByTestId('places-term').getAttribute('data-bound')).toBe('true');
    expect(r.getByTestId('places-term').getAttribute('aria-pressed')).toBe('true');
    expect(r.getByTestId('places-fm').getAttribute('data-bound')).toBe('false');
  });

  test('a click reports the target app', () => {
    const opened: string[] = [];
    const r = render(() => <PlacesBar self={PLACES_TERMINAL} view={alone} onOpen={(t) => opened.push(t)} />);
    fireEvent.click(r.getByTestId('places-ai'));
    fireEvent.click(r.getByTestId('places-edit'));
    expect(opened).toEqual([PLACES_AGENT, PLACES_EDITOR]);
  });

  test('tooltips say what a click will do', () => {
    const view: PlacesView = { group: 'g1', members: { [PLACES_FILES]: 'f1' } };
    const r = render(() => (
      <PlacesBar
        self={PLACES_TERMINAL}
        view={view}
        onOpen={() => {}}
        describe={(t) => (t === PLACES_AGENT ? 'anthropic / frontier' : undefined)}
      />
    ));
    expect(r.getByTestId('places-fm').getAttribute('title')).toMatch(/^Show Files/);
    expect(r.getByTestId('places-edit').getAttribute('title')).toBe('Open Editor here');
    // The agent's line names what will start, so a new session is no surprise.
    expect(r.getByTestId('places-ai').getAttribute('title')).toBe('Open Agent here · anthropic / frontier');
  });

  test('disabled explains itself and does not fire', () => {
    const opened: string[] = [];
    const r = render(() => (
      <PlacesBar self={PLACES_AGENT} view={alone} onOpen={(t) => opened.push(t)} disabled="This session has no folder yet" />
    ));
    const b = r.getByTestId('places-term') as HTMLButtonElement;
    expect(b.disabled).toBe(true);
    expect(b.getAttribute('title')).toBe('This session has no folder yet');
    fireEvent.click(b);
    expect(opened).toEqual([]);
  });
});
