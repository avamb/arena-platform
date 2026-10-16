import { defineConfig } from "@playwright/test";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * The LIVE twin tests: the same phone viewport as the mock smoke, but against
 * a real arena-api on a real (migrated) database. scripts/live-harness.mjs
 * starts the API and writes tests/smoke/.live-env.json (the fixture and the
 * three people); it is the first webServer and the specs run once its
 * readiness port answers. DATABASE_URL must be set (see tests/README.md).
 *
 * Ports differ from the mock smoke (5176) on purpose, so both can run on one
 * machine. The app throws on boot without VITE_API_BASE_URL (AGENTS.md), so
 * the dev server gets the live API's address.
 */
const smokeDir = dirname(fileURLToPath(import.meta.url));
const appDir = resolve(smokeDir, "..", "..");
const apiPort = Number(process.env.ARENA_LIVE_API_PORT ?? 18086);
const readyPort = Number(process.env.ARENA_LIVE_READY_PORT ?? 18087);
const uiPort = Number(process.env.ARENA_LIVE_UI_PORT ?? 5177);

export default defineConfig({
  testDir: resolve(smokeDir, "live"),
  testMatch: "*.spec.ts",
  // The three people share one organization and one database: run in order.
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"], ["junit", { outputFile: "event-center-live.xml" }]],
  use: {
    baseURL: `http://127.0.0.1:${uiPort}`,
    viewport: { width: 375, height: 812 },
    hasTouch: true,
    isMobile: true,
    deviceScaleFactor: 2,
    locale: "en-GB",
    timezoneId: "Europe/Madrid",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: [
    {
      command: "node scripts/live-harness.mjs",
      cwd: appDir,
      url: `http://127.0.0.1:${readyPort}/`,
      // Builds two Go binaries when none are prebuilt, then seeds and boots.
      timeout: 240_000,
      reuseExistingServer: false,
      stdout: "pipe",
      stderr: "pipe",
    },
    {
      command: `npm run dev -- --host 127.0.0.1 --port ${uiPort} --strictPort`,
      cwd: appDir,
      url: `http://127.0.0.1:${uiPort}/`,
      timeout: 60_000,
      reuseExistingServer: !process.env.CI,
      // VITE_FEATURE_WIZARD: the event wizard is behind a build flag (6-1a); the live build has it on.
      env: { VITE_API_BASE_URL: `http://127.0.0.1:${apiPort}`, VITE_FEATURE_WIZARD: "1" },
    },
  ],
});
