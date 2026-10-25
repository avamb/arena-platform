import { defineConfig } from 'vite'

// Library builds (spec 44, RT-10, RT-11):
//   vite build               ESM  dist/rich-editor.js, dist/text.js (+ one shared chunk)  for apps/event-center
//   vite build --mode iife   IIFE dist/arena-rich-editor.iife.js  global ArenaRichEditor, for WordPress
// `text.js` is the editor-free entry (counting, sanitizing, FromLegacy): a host that only needs those
// does not load Tiptap. Both builds are self-contained and share one stylesheet, dist/rich-editor.css,
// so a host needs no CSS injection and no inline style.
export default defineConfig(({ mode }) => {
  const iife = mode === 'iife'
  return {
    publicDir: false,
    build: {
      lib: {
        entry: iife ? 'src/entry.ts' : { 'rich-editor': 'src/entry.ts', text: 'src/text.ts' },
        name: 'ArenaRichEditor',
        formats: [iife ? 'iife' : 'es'],
        fileName: (_format: string, name: string) => (iife ? 'arena-rich-editor.iife.js' : `${name}.js`),
      },
      emptyOutDir: !iife,
      sourcemap: false,
      minify: 'esbuild',
      target: 'es2020',
      rollupOptions: {
        output: {
          chunkFileNames: 'chunks/[name]-[hash].js',
          assetFileNames: (info) =>
            info.name && info.name.endsWith('.css') ? 'rich-editor.css' : 'assets/[name]-[hash][extname]',
        },
      },
    },
  }
})
