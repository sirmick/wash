import { defineConfig } from 'vite';
import { washAppConfig } from '@wash/ui/vite-app';

// Library build for the wash-app-services element / panel. Shared config
// lives in @wash/ui/vite-app; only per-app overrides are passed here.
export default defineConfig(washAppConfig());
