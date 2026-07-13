import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  testMatch: ['**/palac-akropolis-real.e2e.ts'],
  timeout: 60_000,
  retries: 0,
  reporter: process.env['CI'] ? 'github' : 'list',
  use: {
    baseURL: 'http://localhost:4174',
    trace: 'on-first-retry',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'node scripts/serve-demo-real.cjs',
    url: 'http://localhost:4174',
    reuseExistingServer: !process.env['CI'],
    timeout: 30_000,
  },
});
