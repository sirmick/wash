// The launcher's preselection kernel. Pure, so it is tested directly
// rather than through the form.

import { describe, expect, test } from 'vitest';
import { defaultCatalog, defaultCwd } from './default-catalog.ts';

const cat = (id: string, available = true) => ({ id, available });
const ran = (catalog: string, cwd = '') => ({ catalog, cwd });

describe('defaultCatalog', () => {
  test('falls back to history, then to the first that can start', () => {
    expect(defaultCatalog([cat('a'), cat('b')], [ran('b')])).toBe('b');
    expect(defaultCatalog([cat('a'), cat('b')], [])).toBe('a');
    expect(defaultCatalog([], [ran('b')])).toBe('');
  });

  test('a catalog that can no longer start never wins', () => {
    expect(defaultCatalog([cat('a'), cat('b', false)], [ran('b')])).toBe('a');
  });

  test('an explicit default beats history', () => {
    // The person said what "start an agent" means; the last session they
    // happened to run does not override it (docs/PLACES.md §4.5).
    expect(defaultCatalog([cat('a'), cat('b')], [ran('b')], 'a')).toBe('a');
  });

  test('an explicit default that cannot start falls through', () => {
    // Its key was cleared or its adapter uninstalled — preselecting it
    // would open the form on a row that fails.
    expect(defaultCatalog([cat('a'), cat('b', false)], [ran('a')], 'b')).toBe('a');
  });

  test('no default set is the old behaviour', () => {
    expect(defaultCatalog([cat('a'), cat('b')], [ran('b')], '')).toBe('b');
  });
});

describe('defaultCwd', () => {
  test('is the newest folder in history, else empty for Home', () => {
    expect(defaultCwd([ran('a', '/tmp/x'), ran('b', '/tmp/y')])).toBe('/tmp/x');
    expect(defaultCwd([ran('a')])).toBe('');
    expect(defaultCwd([])).toBe('');
  });
});
