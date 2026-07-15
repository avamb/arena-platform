import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
      "@openapi": path.resolve(__dirname, "../backend/openapi/clients/ts"),
    },
  },
  server: {
    port: 5174,
    strictPort: false,
  },
  build: {
    outDir: "dist",
    // PR-07: Disable source maps in production builds.
    // Set VITE_ENABLE_SOURCEMAPS=true to generate them as private CI
    // artifacts (never serve them from the public container).
    sourcemap: process.env.VITE_ENABLE_SOURCEMAPS === "true",
    target: "es2022",
    // PR-07: Bundle size budget — split into route + vendor chunks so
    // the initial load fetches only the auth/shell code.
    // Documented budget: vendor ≤ 400 kB, each route chunk ≤ 50 kB.
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          // React core + DOM
          if (id.includes("node_modules/react") || id.includes("node_modules/react-dom") || id.includes("node_modules/scheduler")) {
            return "vendor-react";
          }
          // TanStack Router
          if (id.includes("node_modules/@tanstack/react-router") || id.includes("node_modules/@tanstack/router")) {
            return "vendor-router";
          }
          // TanStack Query
          if (id.includes("node_modules/@tanstack/react-query") || id.includes("node_modules/@tanstack/query-core")) {
            return "vendor-query";
          }
          // TanStack Table
          if (id.includes("node_modules/@tanstack/react-table")) {
            return "vendor-table";
          }
          // Form / validation
          if (id.includes("node_modules/react-hook-form") || id.includes("node_modules/zod")) {
            return "vendor-forms";
          }
          // Everything else in node_modules → shared vendor chunk
          if (id.includes("node_modules")) {
            return "vendor-misc";
          }
        },
      },
    },
  },
});
