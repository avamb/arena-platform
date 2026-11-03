import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import path from "node:path";

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
      "@openapi": path.resolve(__dirname, "../backend/openapi/clients/ts"),
      "@seatmap": path.resolve(__dirname, "../widget/src"),
      "@arena-track": path.resolve(__dirname, "../../packages/arena-track/src"),
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
