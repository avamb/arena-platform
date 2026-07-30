/**
 * Playwright configuration for Feature #412 (AB-26):
 * Sales Channels and Payment Configs — org picker + create flow browser tests.
 *
 * Runs against the admin-web dev server (port 5176 by default, or
 * ADMIN_WEB_BASE_URL when provided by CI). Expects the backend API to be
 * running at VITE_API_BASE_URL (default http://localhost:18080) with a seeded
 * DB (arena-seed) so that super@test.arena.local and the test orgs exist.
 */
import { defineConfig } from "@playwright/test";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";

const testDirectory = dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  testDir: testDirectory,
  testMatch: "channels-payments.spec.ts",
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [
    ["list"],
    ["junit", { outputFile: "channels-payments-results.xml" }],
  ],
  use: {
    baseURL: process.env.ADMIN_WEB_BASE_URL ?? "http://127.0.0.1:5176",
    viewport: { width: 1280, height: 800 },
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    actionTimeout: 15_000,
  },
  webServer: process.env.ADMIN_WEB_BASE_URL
    ? undefined
    : {
        command: "npm run dev -- --host 127.0.0.1 --port 5176",
        port: 5176,
        reuseExistingServer: true,
        timeout: 60_000,
        env: {
          VITE_API_BASE_URL:
            process.env.VITE_API_BASE_URL ?? "http://127.0.0.1:18080",
        },
      },
});
