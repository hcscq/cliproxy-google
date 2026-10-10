import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Dev: `HOME_URL=http://127.0.0.1:8327 npm run dev` proxies the Management API.
const target = process.env.HOME_URL || 'http://127.0.0.1:8327';

export default defineConfig({
  plugins: [react()],
  base: '/',
  build: { outDir: 'dist', assetsDir: 'assets', sourcemap: false, chunkSizeWarningLimit: 800 },
  server: { proxy: { '/v8/management': { target, changeOrigin: true } } },
  test: { include: ['src/**/*.test.ts'], environment: 'node' },
} as never);
