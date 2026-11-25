import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Arena Tickets widget — single-file custom element build (Shadow DOM)
// Output: dist/v1/arena-tickets.js (ES module, ~<150 KB gzip)
export default defineConfig({
  plugins: [
    svelte({
      compilerOptions: {
        // Required for ArenaTickets.svelte's <svelte:options customElement>.
        // Sub-components (SessionList.svelte, SeatMapView.svelte) must NOT
        // include <svelte:options customElement> — they are regular Svelte
        // components compiled with this flag set and do not need a tag.
        customElement: true,
      },
    }),
  ],
  build: {
    lib: {
      entry: 'src/index.ts',
      formats: ['es'],
      // Produces dist/arena-tickets.js
      // Using a string so Vite automatically appends the format extension (.js).
      fileName: 'arena-tickets',
    },
    outDir: 'dist/v1',
    // Do not externalize anything — the bundle must be fully self-contained
    // so it can be served from a CDN as a single script tag.
    rollupOptions: {
      external: [],
      preserveEntrySignatures: 'allow-extension',
      output: {
        format: 'es',
        // The thank-you blocks load by dynamic import from the paid view; the
        // chunk lands next to arena-tickets.js and is found relative to it.
        chunkFileNames: 'arena-tickets-[name]-[hash].js',
      },
    },
    // Inline all assets (fonts, tiny SVGs) so the output is truly one file.
    assetsInlineLimit: 1024 * 32,
    minify: true,
    sourcemap: false,
  },
});
