import { defineConfig } from 'vite';
import { washAppConfig } from '@wash/ui/vite-app';
export default defineConfig(washAppConfig({ entry: 'src/panel.tsx', fileName: 'panel.js' }));
