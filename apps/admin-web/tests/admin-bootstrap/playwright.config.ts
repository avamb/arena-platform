import { defineConfig } from "@playwright/test";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";

const testDirectory = dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  testDir: testDirectory,
  testMatch: "mobile.spec.ts",
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"], ["junit", { outputFile: "admin-bootstrap-mobile.xml" }]],
  use: {
    baseURL: process.env.ADMIN_WEB_BASE_URL ?? "http://127.0.0.1:5174",
    viewport: { width: 375, height: 812 },
    hasTouch: true,
    isMobile: true,
    deviceScaleFactor: 2,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: process.env.ADMIN_WEB_BASE_URL
    ? undefined
    : {
        command: "npm run dev -- --host 127.0.0.1",
        port: 5174,
        reuseExistingServer: !process.env.CI,
        timeout: 30_000,
      },
});
