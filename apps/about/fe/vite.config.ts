import { defineConfig } from 'vite';
import { washAppConfig } from '@wash/ui/vite-app';

// Library build: a single self-contained ES module defining the
// <wash-app-about> custom element. Shared config lives in @wash/ui/vite-app.
export default defineConfig(washAppConfig());
