import { defineConfig, devices } from '@playwright/test';

/**
 * Arena Tickets widget — Playwright end-to-end configuration.
 * Tests load the demo page via a static preview server (`vite preview`).
 *
 * Run locally: npm run build && npm run test:e2e
 */
export default defineConfig({
  testDir: './tests',
  testMatch: ['**/*.e2e.ts'],
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
  // Start a static preview server before running tests.
  webServer: {
    command: 'npm run preview',
    url: 'http://localhost:4173',
    reuseExistingServer: !process.env['CI'],
    timeout: 30_000,
  },
});
