import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

// The event center is a thin client of arena-api. It shares only the
// generated OpenAPI types with admin-web (the @openapi alias), never
// admin-web's screens: admin-web is the platform superadmin console.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
      "@openapi": path.resolve(__dirname, "../backend/openapi/clients/ts"),
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
