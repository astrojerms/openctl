/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import path from 'node:path';

// The controller embeds this directory via `//go:embed all:uiassets` in
// internal/controller/server/http.go. Vite's `outDir` writes directly into
// that path so `make ui && go build` produces a binary with the live UI
// baked in — no copy step.
//
// `base: '/ui/'` matches the controller's URL mount point, so generated
// asset URLs in index.html (`/ui/assets/index-...js`) line up with what
// the embed.FS handler serves.
//
// In dev (`npm run dev`), Vite proxies /v1/* to the controller's HTTP
// gateway (default 127.0.0.1:9445). Cookies are same-origin from the
// browser's perspective, so the session cookie set by Login round-trips
// just like production.
const HTTP_GATEWAY = process.env.OPENCTL_HTTP_GATEWAY ?? 'http://127.0.0.1:9445';

export default defineConfig({
  plugins: [svelte()],
  base: '/ui/',
  build: {
    outDir: path.resolve(__dirname, '../internal/controller/server/uiassets/dist'),
    emptyOutDir: true,
    sourcemap: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/v1': {
        target: HTTP_GATEWAY,
        changeOrigin: false,
      },
    },
  },
  test: {
    environment: 'happy-dom',
    include: ['src/**/*.test.ts'],
    globals: false,
  },
});
