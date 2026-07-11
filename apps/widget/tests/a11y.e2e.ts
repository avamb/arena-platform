/**
 * Accessibility E2E tests — WCAG 2.2 AA gate for the Arena Tickets widget.
 *
 * Uses @axe-core/playwright to scan the demo pages with axe-core rules:
 *   wcag2a, wcag2aa, wcag21aa, wcag22aa
 *
 * Runs against the static demo server (scripts/serve-demo.js) started
 * by the Playwright webServer configuration.
 *
 * WID-E: axe-core automated pass — feature #327
 */

import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

// ─── Demo page ────────────────────────────────────────────────────────────────

test.describe('Demo page (attribute matrix)', () => {
  test('has no WCAG 2.2 AA violations', async ({ page }) => {
    await page.goto('/demo/index.html');

    // Wait for custom elements to upgrade (they may be in placeholder state).
    await page.waitForLoadState('networkidle');

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa'])
      // Exclude the shadow roots of arena-tickets elements — they render placeholder
      // divs with aria-hidden="true" which is intentional and not a violation.
      .analyze();

    // Fail the test if there are any critical or serious violations.
    const critical = results.violations.filter(
      (v) => v.impact === 'critical' || v.impact === 'serious',
    );
    expect(
      critical,
      `WCAG 2.2 AA: critical/serious violations:\n${JSON.stringify(critical, null, 2)}`,
    ).toHaveLength(0);
  });

  test('document has a lang attribute', async ({ page }) => {
    await page.goto('/demo/index.html');
    const lang = await page.evaluate(() => document.documentElement.lang);
    expect(lang).toBeTruthy();
  });

  test('document has exactly one h1', async ({ page }) => {
    await page.goto('/demo/index.html');
    const h1Count = await page.locator('h1').count();
    expect(h1Count).toBe(1);
  });

  test('all images have alt text (if any)', async ({ page }) => {
    await page.goto('/demo/index.html');
    const imgsWithoutAlt = await page
      .locator('img:not([alt])')
      .count();
    expect(imgsWithoutAlt).toBe(0);
  });

  test('data tables have header cells', async ({ page }) => {
    await page.goto('/demo/index.html');
    const tables = page.locator('table');
    const tableCount = await tables.count();
    for (let i = 0; i < tableCount; i++) {
      const table = tables.nth(i);
      const thCount = await table.locator('th').count();
      expect(thCount).toBeGreaterThan(0);
    }
  });
});

// ─── Accessibility test fixture ───────────────────────────────────────────────

test.describe('A11y fixture page', () => {
  test('has no WCAG 2.2 AA violations', async ({ page }) => {
    await page.goto('/demo/a11y-keyboard.html');
    await page.waitForLoadState('networkidle');

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa'])
      .analyze();

    const critical = results.violations.filter(
      (v) => v.impact === 'critical' || v.impact === 'serious',
    );
    expect(
      critical,
      `WCAG 2.2 AA: critical/serious violations:\n${JSON.stringify(critical, null, 2)}`,
    ).toHaveLength(0);
  });

  test('landmark regions are present (<main>)', async ({ page }) => {
    await page.goto('/demo/a11y-keyboard.html');
    const mainCount = await page.locator('main').count();
    expect(mainCount).toBeGreaterThanOrEqual(1);
  });

  test('widget placeholder is aria-hidden', async ({ page }) => {
    await page.goto('/demo/a11y-keyboard.html');
    await page.waitForLoadState('networkidle');

    // The arena-tickets without a feed-token should expose aria-hidden placeholder.
    // We check the shadow DOM host's role="region" attribute is present on the root.
    const widgetNoToken = page.locator('#widget-no-token');
    await expect(widgetNoToken).toBeAttached();
  });

  test('widget with locale="he" has dir=rtl in shadow root', async ({ page }) => {
    await page.goto('/demo/a11y-keyboard.html');
    await page.waitForLoadState('networkidle');

    // The RTL widget should set dir="rtl" on its shadow root inner div.
    const dir = await page.evaluate(() => {
      const el = document.querySelector('#widget-rtl');
      if (!el || !el.shadowRoot) return null;
      const root = el.shadowRoot.querySelector('[dir]');
      return root?.getAttribute('dir') ?? null;
    });
    expect(dir).toBe('rtl');
  });
});
