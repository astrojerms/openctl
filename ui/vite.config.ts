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
// just like production. The gateway now serves HTTPS (for HTTP/2), so the
// target is https:// and `secure: false` tells Vite's proxy to accept the
// controller's self-signed cert.
const HTTP_GATEWAY = process.env.OPENCTL_HTTP_GATEWAY ?? 'https://127.0.0.1:9445';

export default defineConfig({
  plugins: [svelte()],
  base: '/ui/',
  build: {
    outDir: path.resolve(__dirname, '../internal/controller/server/uiassets/dist'),
    emptyOutDir: true,
    sourcemap: true,
    // Monaco's editor.api + yaml.contribution legitimately blow past
    // the 500 KB default; the index chunk stays small (~180 KB) and the
    // editor chunks only load when /edit is visited.
    chunkSizeWarningLimit: 1500,
  },
  server: {
    port: 5173,
    proxy: {
      '/v1': {
        target: HTTP_GATEWAY,
        changeOrigin: false,
        // The gateway's cert is self-signed by the controller CA; the dev
        // proxy talks to localhost, so skip cert verification here.
        secure: false,
      },
    },
  },
  test: {
    environment: 'happy-dom',
    include: ['src/**/*.test.ts'],
    globals: false,
  },
});
