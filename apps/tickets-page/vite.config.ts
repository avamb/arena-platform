import { defineConfig } from 'vite';

// Arena Sold Out hosted sales page — a tiny static Vite + vanilla TypeScript
// app. No framework: the page's only job is to resolve /{org_slug}/{event_slug}
// against the public API and mount the self-hosted <arena-tickets> widget
// (served from /widget/v1/arena-tickets.js by the same nginx container —
// see Dockerfile).
export default defineConfig({
  build: {
    outDir: 'dist',
    sourcemap: false,
  },
});
