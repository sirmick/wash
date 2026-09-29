// Component test (Tier B) for Button's variants, and specifically that
// `primary` exists and paints differently from `default`.
//
// It did not exist for a long time: six call sites across the Agent app
// asked for variant="primary" over five separate commits, each author
// assuming it was there, and every one of them silently fell through the
// switch to `default`. vite does not typecheck, so nothing said so until
// the frontends were made to compile.

import { test, expect, afterEach } from 'vitest';
import { render, cleanup } from '@solidjs/testing-library';
import { Button } from './button.tsx';
import { tokens } from './tokens.ts';

afterEach(cleanup);

const styleOf = (variant: 'default' | 'primary' | 'danger') => {
  const { getByTestId } = render(() => (
    <Button variant={variant} data-testid="b">go</Button>
  ));
  return getByTestId('b').getAttribute('style') ?? '';
};

test('primary is its own variant, not a silent fallback to default', () => {
  const primary = styleOf('primary');
  expect(primary).not.toBe(styleOf('default'));
  // Filled with the accent, lettered in the window surface — the pairing
  // that inverts correctly across the five theme packs.
  expect(primary).toContain(tokens.accentBlue);
  expect(primary).toContain(tokens.bgWindow);
});

test('an unknown variant still falls back to default rather than rendering bare', () => {
  // The switch's default arm is load-bearing: a variant this build has not
  // heard of must still paint a button.
  const { getByTestId } = render(() => (
    // @ts-expect-error — deliberately not a ButtonVariant
    <Button variant="no-such-variant" data-testid="b">go</Button>
  ));
  expect(getByTestId('b').getAttribute('style')).toBe(styleOf('default'));
});

test('every variant carries the interaction layer', () => {
  // check-interactive polices this repo-wide; pinned here too because the
  // attribute is what hit.ts keys its hover/press/focus treatment on.
  for (const v of ['default', 'primary', 'danger'] as const) {
    const { getByTestId } = render(() => (
      <Button variant={v} data-testid={`b-${v}`}>go</Button>
    ));
    expect(getByTestId(`b-${v}`).hasAttribute('data-wash-hit')).toBe(true);
  }
});
