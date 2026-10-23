import { defineConfig } from "@playwright/test";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * The poster acceptance against the REAL production image: nginx with the real
 * Content-Security-Policy serves the built app, and the app talks to a REAL
 * arena-api (scripts/live-harness.mjs is started by hand, see
 * tests/README.md, "Posters under the real CSP"). Only the vision reader is scripted through the test-only backend build;
 * uploads, authorization, event creation and attachment use the real API.
 *
 * EVENT_CENTER_BASE_URL is the container (for example http://127.0.0.1:8092);
 * the image must have been built with VITE_API_BASE_URL = the live API.
 */
const smokeDir = dirname(fileURLToPath(import.meta.url));
const baseURL = process.env.EVENT_CENTER_BASE_URL;
if (baseURL === undefined || baseURL === "") throw new Error("EVENT_CENTER_BASE_URL (the container) is required");

export default defineConfig({
  testDir: resolve(smokeDir, "container"),
  testMatch: "*.spec.ts",
  workers: 1,
  reporter: [["list"]],
  use: {
    baseURL,
    viewport: { width: 375, height: 812 },
    hasTouch: true,
    isMobile: true,
    deviceScaleFactor: 2,
    locale: "en-GB",
    timezoneId: "Europe/Madrid",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
});
