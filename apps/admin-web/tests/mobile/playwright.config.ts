/**
 * Wave M-8 (#301) — Mobile-Responsive Admin quality gate.
 *
 * This Playwright config drives the mobile-smoke set against the
 * organizer/agent routes that Waves M-2..M-7 converted to the
 * responsive layout. It runs the smoke set at the two viewports the
 * Mobile-Responsive Admin spec pins:
 *
 *   - 360 x  640  (smallest supported phone in portrait)
 *   - 768 x 1024  (small tablet in portrait, the desktop/mobile cut)
 *
 * CI gates enforced by this config (see `M8_GATE_THRESHOLDS` in
 * `gateThresholds.ts`):
 *
 *   1. NO horizontal scroll at 360 px on any organizer/agent route in
 *      `M8_SMOKE_ROUTES`. The smoke spec asserts
 *      `document.documentElement.scrollWidth <= window.innerWidth`
 *      after navigation.
 *
 *   2. The PRIMARY action on every smoke route is at least 44 x 44
 *      CSS px (the Apple HIG / WCAG 2.5.5 Level AAA tap target).
 *      The smoke spec walks
 *      `[data-testid$="-primary-action"], button[type="submit"]:first-of-type`
 *      and asserts `boundingBox.width >= 44 && boundingBox.height >= 44`.
 *
 *   3. Lighthouse accessibility score >= 90 on the three highest-
 *      traffic routes (`M8_LIGHTHOUSE_ROUTES`): /orders, /tickets,
 *      /events. The companion `lighthouse.config.json` pins the
 *      threshold and the route list so a single source of truth
 *      drives both Playwright and Lighthouse runs.
 *
 * NOTE on scope: M-8 is responsive-web only. No native-app runner is
 * introduced; the test runner here is Playwright (browser automation
 * against the existing admin-web bundle).
 *
 * NOTE on runtime: this config is loaded only when @playwright/test is
 * installed in the environment running the gate. The executable contract
 * the rest of the repo can rely on is pinned by
 * `apps/admin-web/src/routes/m8AccessibilityGate.test.ts`, which is
 * what `npm test` enforces on every push.
 */
import {
  M8_LIGHTHOUSE_ROUTES,
  M8_SMOKE_ROUTES,
  M8_VIEWPORTS,
  M8_GATE_THRESHOLDS,
} from "./gateThresholds";

// We intentionally avoid a top-level `import { defineConfig } from
// "@playwright/test"` so that this file remains type-checkable from
// the vitest gate even when @playwright/test is not installed in the
// admin-web workspace. The CI job that runs the gate installs
// @playwright/test on demand and then `require()`s this module.
type PlaywrightConfigShape = {
  testDir: string;
  fullyParallel: boolean;
  forbidOnly: boolean;
  retries: number;
  reporter: Array<[string, Record<string, unknown>?] | string>;
  use: Record<string, unknown>;
  projects: Array<{
    name: string;
    use: Record<string, unknown>;
  }>;
};

const config: PlaywrightConfigShape = {
  testDir: __dirname,
  fullyParallel: true,
  // Block `test.only` from sneaking past code review.
  forbidOnly: !!process.env.CI,
  // Mobile networks are flaky; one retry on CI, none locally.
  retries: process.env.CI ? 1 : 0,
  reporter: [
    ["list"],
    ["junit", { outputFile: "playwright-mobile-report.xml" }],
  ],
  use: {
    baseURL: process.env.ADMIN_WEB_BASE_URL ?? "http://127.0.0.1:5173",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: M8_VIEWPORTS.map((viewport) => ({
    name: `mobile-${viewport.label}`,
    use: {
      viewport: { width: viewport.widthPx, height: viewport.heightPx },
      // `hasTouch: true` so Playwright synthesises touch events; the
      // 44 px tap-target gate is meaningless without it.
      hasTouch: true,
      isMobile: viewport.widthPx < 768,
      deviceScaleFactor: 2,
    },
  })),
};

// Re-export the constants so consumers of the config (the smoke spec
// and the Lighthouse runner) read from a single source of truth.
export {
  M8_GATE_THRESHOLDS,
  M8_LIGHTHOUSE_ROUTES,
  M8_SMOKE_ROUTES,
  M8_VIEWPORTS,
};

export default config;
