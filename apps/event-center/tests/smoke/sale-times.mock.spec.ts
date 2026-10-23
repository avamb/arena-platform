/**
 * 5-3 smoke: the sales end and doors block of the session card against a
 * MOCKED arena-api, on a phone viewport. The twin test that walks the bot's
 * scenario through the real API is live/sale-times.spec.ts.
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { EVENT, ORG_A, SESSION, mockApi } from "./mockApi";
import { mockSaleTimes } from "./saleTimesMock";

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
}

const url = `/o/${ORG_A}/events/${EVENT}/sessions/${SESSION}`;

test.describe("5-3 sales end and doors (mock)", () => {
  test("a preset sets the sales end, the clock time sets the doors, Remove clears them", async ({ page }) => {
    await mockApi(page);
    const state = await mockSaleTimes(page);
    await signIn(page);
    await page.goto(url);
    await expect(page.getByTestId("sale-times")).toBeVisible();
    await expect(page.getByTestId("sales-end-value")).toContainText("the start of the session");
    await expect(page.getByTestId("doors-value")).toContainText("19:30");

    const width = page.viewportSize()?.width ?? 375;
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
    const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    expect(result.violations.map((v) => v.id)).toEqual([]);

    // Sales end: one hour before the start. Prague is UTC+1 in November.
    await page.getByTestId("sales-end-change").click();
    await page.getByTestId("sales-end-preset-before1h").click();
    await expect(page.getByTestId("sales-end-time")).toHaveValue("19:00");
    await page.getByTestId("sales-end-save").click();
    await expect(page.getByTestId("sales-end-done")).toHaveText("Saved.");
    expect(state.writes[0]).toEqual({ sales_end_at: "2026-11-20T18:00:00Z" });

    // Doors: a typed time on the session's day.
    await page.getByTestId("doors-change").click();
    await page.getByTestId("doors-time").fill("18:45");
    await page.getByTestId("doors-save").click();
    await expect(page.getByTestId("doors-value")).toContainText("18:45");
    expect(state.writes[1]).toEqual({ doors_open_at: "2026-11-20T17:45:00Z" });

    // Doors after the start are refused before any call.
    await page.getByTestId("doors-change").click();
    await page.getByTestId("doors-time").fill("21:00");
    await page.getByTestId("doors-save").click();
    await expect(page.getByTestId("doors-problem")).toContainText("Doors cannot open after the session starts");
    expect(state.writes).toHaveLength(2);
    await page.getByTestId("doors-cancel").click();

    await page.getByTestId("doors-remove").click();
    await expect(page.getByTestId("doors-value")).toHaveText("Not set");
    expect(state.writes[2]).toEqual({ doors_open_at: null });
  });

  test("an API refusal is told in words and the editor stays open", async ({ page }) => {
    await mockApi(page);
    const state = await mockSaleTimes(page);
    state.failWith = "session.doors_after_start";
    await signIn(page);
    await page.goto(url);
    await page.getByTestId("doors-change").click();
    await page.getByTestId("doors-time").fill("18:00");
    await page.getByTestId("doors-save").click();
    await expect(page.getByTestId("doors-error")).toContainText("Doors cannot open after the session starts");
    await expect(page.getByTestId("doors-editor")).toBeVisible();
  });

  test("a person without session.update sees no editor", async ({ page }) => {
    await mockApi(page);
    const state = await mockSaleTimes(page, ["event.read", "order.read", "session.read", "tier.read"]);
    await signIn(page);
    await page.goto(url);
    await expect(page.locator("#session-title")).toBeVisible();
    await expect(page.getByTestId("sale-times")).toHaveCount(0);
    expect(state.writes).toHaveLength(0);
  });
});
