import { defineConfig } from 'vite'

// Two library builds (spec 44, RT-10):
//   vite build               ESM  dist/rich-editor.js         for apps/event-center
//   vite build --mode iife   IIFE dist/arena-rich-editor.iife.js  global ArenaRichEditor, for WordPress
// Both are self-contained (Tiptap is bundled) and share one stylesheet,
// dist/rich-editor.css, so a host needs no CSS injection and no inline style.
export default defineConfig(({ mode }) => {
  const iife = mode === 'iife'
  return {
    publicDir: false,
    build: {
      lib: {
        entry: 'src/entry.ts',
        name: 'ArenaRichEditor',
        formats: [iife ? 'iife' : 'es'],
        fileName: () => (iife ? 'arena-rich-editor.iife.js' : 'rich-editor.js'),
      },
      emptyOutDir: !iife,
      sourcemap: false,
      minify: 'esbuild',
      target: 'es2020',
      rollupOptions: {
        output: {
          assetFileNames: (info) =>
            info.name && info.name.endsWith('.css') ? 'rich-editor.css' : 'assets/[name]-[hash][extname]',
        },
      },
    },
  }
})
