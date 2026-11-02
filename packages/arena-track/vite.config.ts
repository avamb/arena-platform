import { defineConfig } from "vite";

// The browser bundle for pages that have no bundler of their own (the site copies it).
// The event center imports the TypeScript source directly (the @arena-track alias).
export default defineConfig({
  build: {
    target: "es2020",
    minify: "esbuild",
    lib: { entry: "src/index.ts", name: "ArenaTrack", formats: ["es", "iife"], fileName: (f) => `arena-track.${f}.js` },
  },
});
