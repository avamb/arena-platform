import { defineConfig } from "@playwright/test";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";

const testDirectory = dirname(fileURLToPath(import.meta.url));
const port = Number(process.env.EVENT_CENTER_PORT ?? 5176);

// The app throws on boot without VITE_API_BASE_URL (AGENTS.md). The smoke
// points it at a host that every test intercepts with page.route().
export default defineConfig({
  testDir: testDirectory,
  testMatch: "*.spec.ts",
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"], ["junit", { outputFile: "event-center-smoke.xml" }]],
  use: {
    baseURL: process.env.EVENT_CENTER_BASE_URL ?? `http://127.0.0.1:${port}`,
    viewport: { width: 375, height: 812 },
    hasTouch: true,
    isMobile: true,
    deviceScaleFactor: 2,
    locale: "en-GB",
    timezoneId: "Europe/Madrid",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: process.env.EVENT_CENTER_BASE_URL
    ? undefined
    : {
        command: `npm run dev -- --host 127.0.0.1 --port ${port} --strictPort`,
        port,
        reuseExistingServer: !process.env.CI,
        timeout: 60_000,
        env: { VITE_API_BASE_URL: "http://api.mock.test" },
      },
});
