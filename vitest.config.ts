import { defineConfig } from 'vitest/config';
import solid from 'vite-plugin-solid';

// Component-test tier (Tier B). vitest + vite-plugin-solid reuses the exact
// Solid JSX transform the app builds use, and jsdom gives a real DOM so we
// can mount a component, fire events, and assert rendered output — the
// reactive WIRING that a node:test reactive-logic test can't reach.
//
// Scope is *.ctest.tsx ONLY. The framework-free + reactive-logic units are
// *.test.ts and run under `node --test --conditions=browser` (test.sh's
// fe-unit step); they must NOT be picked up here (they use the node:test
// API, not vitest), hence the explicit include that overrides vitest's
// default `**/*.{test,spec}` glob.
export default defineConfig({
  plugins: [solid()],
  // Force solid-js to its client (reactive) build in the test runtime.
  resolve: { conditions: ['development', 'browser'] },
  test: {
    environment: 'jsdom',
    include: ['{apps,web}/**/src/**/*.ctest.{ts,tsx}'],
    watch: false,
    // lucide-solid (and other Solid component libs) ship untransformed .jsx
    // sources; inline them so they go through vite-plugin-solid instead of
    // Node trying to load .jsx raw ("Unknown file extension .jsx").
    server: { deps: { inline: [/lucide-solid/] } },
  },
});
