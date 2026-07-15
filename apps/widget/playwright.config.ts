import { defineConfig, devices } from '@playwright/test';

/**
 * Arena Tickets widget — Playwright end-to-end configuration.
 *
 * Tests load demo pages via a lightweight Node static file server that
 * serves the entire apps/widget directory (demo/ and dist/).
 *
 * Prerequisite: `npm run build` must be run before `npm run test:e2e`
 * so that dist/v1/arena-tickets.js exists.
 *
 * Run locally:
 *   npm run build && npm run test:e2e
 *
 * CI:
 *   npm run build
 *   npx playwright install --with-deps chromium
 *   npm run test:e2e
 */
export default defineConfig({
  testDir: './tests',
  testMatch: ['**/*.e2e.ts'],
  // palac-akropolis.e2e.ts — MOCK SMOKE SUITE ONLY.
  // All /v1/* calls are intercepted via page.route() fixtures.
  // This is NOT acceptance: it was replaced by palac-akropolis-real.e2e.ts
  // (WID-R3, feature #332) which drives the ACTUAL rendered element against
  // the LOCAL COMPOSE BACKEND. The real suite runs via playwright.config.real.ts
  // in the widget-acceptance CI job. This mock suite stays as a fast offline
  // smoke check (run manually with --config playwright.config.ts if needed)
  // but is excluded from the CI gate.
  testIgnore: ['**/palac-akropolis.e2e.ts', '**/palac-akropolis-real.e2e.ts'],
  timeout: 30_000,
  retries: process.env['CI'] ? 2 : 0,
  reporter: process.env['CI'] ? 'github' : 'list',
  use: {
    baseURL: 'http://localhost:4173',
    // Retain trace and screenshot for every failed test regardless of retry
    // count so failure artifacts are always available (PR-08 requirement).
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  // Start a static demo server that serves demo/ and dist/ from the widget root.
  // The server is a plain Node HTTP server — no Vite, no hot reload.
  //
  // reuseExistingServer: false — always start a fresh server owned by this
  // Playwright run and always terminate it when tests finish.  This guarantees
  // deterministic behaviour in both local and CI environments and prevents the
  // run from accidentally attaching to a stale server from a previous session
  // (PR-08: "stale-server reuse" fix).  The server's SIGTERM handler in
  // scripts/serve-demo.cjs ensures it exits promptly once Playwright sends the
  // termination signal after all tests complete.
  webServer: {
    command: 'node scripts/serve-demo.cjs',
    url: 'http://localhost:4173',
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
