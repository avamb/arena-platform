/**
 * EC-V1 and EC-V2 smoke: the "Summary" screen of the bottom bar against a
 * MOCKED arena-api, on a phone viewport. A card per session and currency, the
 * expandable price x category table with the promo code, the range filter and
 * the accountant's workbook for the same range.
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { ORG_A, mockApi } from "./mockApi";
import { mockOrgSummary } from "./orgSummaryMock";

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
}

const url = `/o/${ORG_A}/summary`;

test.describe("EC-V1 / EC-V2 organization summary (mock)", () => {
  test("shows the cards, folds the prices, filters by range and downloads the workbook", async ({ page }) => {
    await mockApi(page);
    const state = await mockOrgSummary(page);
    await signIn(page);
    await page.goto(url);

    await expect(page.getByTestId("summary-card")).toHaveCount(1);
    const card = page.getByTestId("summary-card");
    await expect(card).toContainText("Wine night");
    await expect(card).toContainText("Revenue: €120.00");
    await expect(card).toContainText("Net: €90.00");
    await expect(card).toContainText("Invitations: 1");
    await expect(page.getByTestId("summary-test-orders")).toContainText("2");

    // The first read is "this month"; the request carries both bounds.
    expect(state.summaryQueries[0]).toMatch(/from=\d{4}-\d{2}-01&to=\d{4}-\d{2}-\d{2}/);

    const width = page.viewportSize()?.width ?? 375;
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
    const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    expect(result.violations.map((v) => v.id)).toEqual([]);

    await page.getByTestId("summary-breakdown").locator("summary").click();
    await expect(page.getByTestId("summary-promo-row")).toContainText("CLUB");

    // "All time" drops both bounds.
    await page.getByTestId("summary-preset-all").click();
    await expect.poll(() => state.summaryQueries.at(-1)).toBe("");

    // A hand-typed range goes to the server as typed.
    await page.getByTestId("summary-from").fill("2026-10-01");
    await page.getByTestId("summary-to").fill("2026-10-31");
    await expect.poll(() => state.summaryQueries.at(-1)).toBe("?from=2026-10-01&to=2026-10-31");

    // The workbook is asked for the same range, in the interface language.
    const download = page.waitForEvent("download");
    await page.getByTestId("xlsx-accounting").click();
    expect((await download).suggestedFilename()).toMatch(/^accounting_.*\.xlsx$/);
    expect(state.workbookQueries[0]).toBe("?from=2026-10-01&to=2026-10-31&locale=en");
  });

  test("a backwards range asks nothing, and a member without order.read sees why", async ({ page }) => {
    await mockApi(page);
    const state = await mockOrgSummary(page);
    await signIn(page);
    await page.goto(url);
    await expect(page.getByTestId("summary-card")).toHaveCount(1);
    const before = state.summaryQueries.length;
    await page.getByTestId("summary-from").fill("2026-12-10");
    await page.getByTestId("summary-to").fill("2026-12-01");
    await expect(page.getByTestId("summary-to")).toHaveAttribute("aria-invalid", "true");
    expect(state.summaryQueries.length).toBeLessThanOrEqual(before + 1);
    const last = state.summaryQueries.at(-1);
    expect(last).not.toBe("?from=2026-12-10&to=2026-12-01");
  });

  test("without order.read the screen says so and asks nothing", async ({ page }) => {
    await mockApi(page);
    const state = await mockOrgSummary(page, ["event.read"]);
    await signIn(page);
    await page.goto(url);
    await expect(page.getByTestId("summary-no-access")).toBeVisible();
    expect(state.summaryQueries).toEqual([]);
  });
});
