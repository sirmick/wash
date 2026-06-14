// host-colors imports @wash/ui (tokens), so it runs under vitest (which
// resolves the workspace package) rather than node:test.

import { describe, it, expect } from 'vitest';
import { hostColor, setHostColor } from './host-colors.ts';

describe('hostColor', () => {
  it('LOCAL origin has no stripe (null)', () => {
    expect(hostColor('local')).toBeNull();
  });

  it('a remote origin gets a stable, non-null accent', () => {
    const a = hostColor('hostB');
    expect(a).toBeTruthy();
    expect(hostColor('hostB')).toBe(a);
  });

  it('an explicit override wins', () => {
    setHostColor('hostC', '#123456');
    expect(hostColor('hostC')).toBe('#123456');
  });
});
