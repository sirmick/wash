import { defineConfig } from 'vite';
import { washAppConfig } from '@wash/ui/vite-app';

// Library build for the settings "Remote" panel element / bundle. wash-remote
// is a windowless background service; this is the only FE it ships — the panel
// the settings app hosts (docs/SETTINGS.md, docs/REMOTE.md). Shared config
// lives in @wash/ui/vite-app; only per-app overrides are passed here.
export default defineConfig(washAppConfig({ entry: 'src/panel.tsx', fileName: 'panel.js' }));
