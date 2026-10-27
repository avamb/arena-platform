import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

// Every build gets its own id. The page registers /sw.js?v=<id>, so a deploy
// installs a new service worker with its own cache and the old cache goes
// (public/sw.js). A build may pass its own id (the commit) in VITE_BUILD_ID.
process.env.VITE_BUILD_ID ??= Date.now().toString(36);

// The event center is a thin client of arena-api. It shares only the
// generated OpenAPI types with admin-web (the @openapi alias), never
// admin-web's screens: admin-web is the platform superadmin console.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
      "@openapi": path.resolve(__dirname, "../backend/openapi/clients/ts"),
      // The seat map core of the widget (pure TypeScript): pan and zoom, the decor
      // sanitizer, the per-seat category overlay and the geometry types. One copy, so
      // the organizer sees the hall the way the buyer does (spec 26, principle P7).
      "@seatmap": path.resolve(__dirname, "../widget/src"),
    },
  },
  server: {
    port: 5176,
    strictPort: false,
  },
  build: {
    outDir: "dist",
    sourcemap: process.env.VITE_ENABLE_SOURCEMAPS === "true",
    target: "es2022",
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (
            id.includes("node_modules/react") ||
            id.includes("node_modules/react-dom") ||
            id.includes("node_modules/scheduler")
          ) {
            return "vendor-react";
          }
          if (id.includes("node_modules/@tanstack")) {
            return "vendor-tanstack";
          }
          if (id.includes("node_modules")) {
            return "vendor-misc";
          }
        },
      },
    },
  },
});
