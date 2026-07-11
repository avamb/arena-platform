import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Arena Tickets widget — single-file custom element build (Shadow DOM)
// Output: dist/arena-tickets.js (ES module, ~<150 KB gzip)
export default defineConfig({
  plugins: [
    svelte({
      compilerOptions: {
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
    outDir: 'dist',
    // Do not externalize anything — the bundle must be fully self-contained
    // so it can be served from a CDN as a single script tag.
    rollupOptions: {
      external: [],
      output: {
        format: 'es',
      },
    },
    // Inline all assets (fonts, tiny SVGs) so the output is truly one file.
    assetsInlineLimit: 1024 * 32,
    minify: true,
    sourcemap: false,
  },
});
