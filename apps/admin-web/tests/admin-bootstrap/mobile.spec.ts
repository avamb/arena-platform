import { expect, test } from "@playwright/test";

const OWNER_WORKFLOW_ROUTES = ["/organizations", "/users", "/networks"] as const;
const MIN_TAP_TARGET_PX = 44;

test.describe("AB-6 owner phone workflows", () => {
  for (const route of OWNER_WORKFLOW_ROUTES) {
    test(`${route} fits a 375x812 phone viewport`, async ({ page }) => {
      await page.goto(route);
      await page.waitForLoadState("networkidle");

      await expect(page.locator("[data-testid='shell-mobile-header']")).toBeVisible();
      await expect(page.locator("[data-testid='shell-mobile-hamburger']")).toBeVisible();

      const viewportWidth = page.viewportSize()?.width ?? 375;
      const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
      expect(scrollWidth).toBeLessThanOrEqual(viewportWidth);

      const hamburger = await page
        .locator("[data-testid='shell-mobile-hamburger']")
        .boundingBox();
      expect(hamburger).not.toBeNull();
      expect(hamburger?.width).toBeGreaterThanOrEqual(MIN_TAP_TARGET_PX);
      expect(hamburger?.height).toBeGreaterThanOrEqual(MIN_TAP_TARGET_PX);
    });
  }
});
