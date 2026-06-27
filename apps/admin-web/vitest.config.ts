import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import path from "node:path";

// Vitest reuses the Vite resolver so @/ + @openapi/ aliases work in
// tests without duplicating the alias map.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
      "@openapi": path.resolve(__dirname, "../backend/openapi/clients/ts"),
    },
  },
  test: {
    environment: "node",
    globals: false,
    setupFiles: ["./src/test-setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    env: {
      VITE_API_BASE_URL: "http://test.invalid",
    },
  },
});
