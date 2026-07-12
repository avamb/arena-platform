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
  // The mocked Palac Akropolis suite is scaffolding, not acceptance: it
  // drives page.route() fixtures whose contracts drift from the real
  // backend. Wave WID-R3 (09_autoforge/widget_backlog.md §8) replaces it
  // with real compose-backend E2E; until then it is excluded from the CI
  // gate so the gate only enforces suites that exercise the real UI
  // (a11y, keyboard).
  testIgnore: ['**/palac-akropolis.e2e.ts'],
  timeout: 30_000,
  retries: process.env['CI'] ? 2 : 0,
  reporter: process.env['CI'] ? 'github' : 'list',
  use: {
    baseURL: 'http://localhost:4173',
    trace: 'on-first-retry',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  // Start a static demo server that serves demo/ and dist/ from the widget root.
  // The server is a plain Node HTTP server — no Vite, no hot reload.
  webServer: {
    command: 'node scripts/serve-demo.cjs',
    url: 'http://localhost:4173',
    reuseExistingServer: !process.env['CI'],
    timeout: 30_000,
  },
});
