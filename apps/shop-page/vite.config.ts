import { defineConfig } from 'vite';

// Arena Sold Out organizer storefront - a static Vite + vanilla TypeScript SPA.
// Dawn's own CSS is imported from src/theme (see src/main.ts) and its custom
// elements (header drawer, search modal, quantity input, slider, gallery) are
// served untouched from public/theme and loaded by index.html.
export default defineConfig({
  build: {
    outDir: 'dist',
    sourcemap: false,
  },
});
