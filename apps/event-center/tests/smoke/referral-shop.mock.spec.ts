/**
 * "Refer a friend" for the organizer's SHOP (spec 52) against a MOCKED arena-api, on a phone viewport. The same
 * section as on the event card, drawn under the shop settings: rules before the first start, pause / resume / end,
 * results, the closed form and the read-only view. The mock is referralMock.ts on top of shopMock.ts.
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { ORG_A, mockApi } from "./mockApi";
import { SHOP_MANAGER, SHOP_READER, mockReferral } from "./referralMock";
import { mockShop } from "./shopMock";

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByRole("heading", { name: "Select an organization" })).toBeVisible();
}

async function noA11yViolations(page: Page) {
  const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  const found = result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`);
  expect(found, "axe accessibility violations").toEqual([]);
}

async function noHorizontalScroll(page: Page) {
  const width = page.viewportSize()?.width ?? 375;
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
}

const settingsUrl = "/o/" + ORG_A + "/shop/settings";

const LIVE = {
  id: "0193f01a-0000-7000-8000-0000000000c1",
  event_id: null,
  status: "active" as const,
  friend_discount_pct: 10,
  cashback_pct: 20,
  cashback_cap_pct: 100,
  max_friends: null,
  starts_at: null,
  ends_at: null,
  terms_accepted_at: "2026-11-01T10:00:00Z",
  created_at: "2026-11-01T10:00:00Z",
  updated_at: "2026-11-01T10:00:00Z",
};

test.describe("spec 52 refer a friend for the shop (mock)", () => {
  test("start: the section is under the settings, the shop's words, the rules first, the body carries accept_terms", async ({ page }) => {
    await mockApi(page);
    await mockShop(page);
    const state = await mockReferral(page, { permissions: SHOP_MANAGER });
    await signIn(page);
    await page.goto(settingsUrl);
    // The settings form is still there and the programme comes after it.
    await expect(page.getByTestId("shop-settings-form")).toBeVisible();
    const section = page.getByTestId("referral");
    await expect(section).toHaveAttribute("data-target", "shop");
    await expect(page.getByTestId("referral-none")).toContainText("did not start for the shop");
    await expect(section).toContainText("A shop buyer who paid gets a personal code");
    await noHorizontalScroll(page);

    await page.getByTestId("referral-friend").fill("10");
    await page.getByTestId("referral-cashback").fill("20");
    await page.getByTestId("referral-save").click();
    await expect(page.getByTestId("referral-rules")).toBeVisible();
    await expect(page.getByTestId("referral-rules-list").locator("li")).toHaveCount(4);
    await noA11yViolations(page);
    await page.getByTestId("referral-rules-submit").click();
    await expect(page.getByTestId("referral-rules")).toContainText("You did not accept the rules");
    expect(state.writes).toEqual([]);

    await page.getByTestId("referral-rules-tick").check();
    await page.getByTestId("referral-rules-submit").click();
    await expect(page.getByTestId("referral-status")).toHaveText("Active");
    expect(state.writes).toHaveLength(1);
    expect(state.writes[0]).toMatch(/^PUT shop referral-program /);
    expect(state.writes[0]).toContain('"accept_terms":true');
    expect(state.writes[0]).toContain('"friend_discount_pct":10');
    // Only shop routes were used: nothing went to an event.
    expect(state.referralCalls.every((c) => c.includes("shop "))).toBe(true);
    await noHorizontalScroll(page);
  });

  test("pause, resume and end (after a question) go to the shop routes and ended is final", async ({ page }) => {
    await mockApi(page);
    await mockShop(page);
    const state = await mockReferral(page, { permissions: SHOP_MANAGER, program: LIVE });
    await signIn(page);
    await page.goto(settingsUrl);
    await expect(page.getByTestId("referral-status")).toHaveText("Active");
    await page.getByTestId("referral-pause").click();
    await expect(page.getByTestId("referral-status")).toHaveText("On pause");
    await page.getByTestId("referral-resume").click();
    await expect(page.getByTestId("referral-status")).toHaveText("Active");
    await page.getByTestId("referral-end").click();
    await expect(page.getByTestId("referral-end-dialog")).toBeVisible();
    expect(state.writes.filter((w) => w.endsWith("/end"))).toEqual([]);
    await page.getByTestId("referral-end-dialog-submit").click();
    await expect(page.getByTestId("referral-status")).toHaveText("Ended");
    await expect(page.getByTestId("referral-actions")).toHaveCount(0);
    expect(state.writes).toEqual(["POST shop referral-program/pause", "POST shop referral-program/resume", "POST shop referral-program/end"]);
  });

  test("when the shop's channel cannot pay the cashback the form is closed and says it for the shop", async ({ page }) => {
    await mockApi(page);
    await mockShop(page);
    await mockReferral(page, { permissions: SHOP_MANAGER, supported: false });
    await signIn(page);
    await page.goto(settingsUrl);
    const blocked = page.getByTestId("referral-blocked");
    await expect(blocked).toContainText("We cannot pay the cashback for the shop");
    await expect(blocked).toContainText("payment channel of the shop");
    await expect(page.getByTestId("referral-form")).toHaveCount(0);
    await noHorizontalScroll(page);
    await noA11yViolations(page);
  });

  test("a reader sees the results and the shop's order numbers but has no form and no buttons", async ({ page }) => {
    await mockApi(page);
    await mockShop(page, SHOP_READER);
    const state = await mockReferral(page, { permissions: SHOP_READER, program: { ...LIVE, status: "paused" } });
    await signIn(page);
    await page.goto(settingsUrl);
    await expect(page.getByTestId("referral-status")).toHaveText("On pause");
    await expect(page.getByTestId("referral-readonly")).toBeVisible();
    await expect(page.getByTestId("referral-form")).toHaveCount(0);
    await expect(page.getByTestId("referral-actions")).toHaveCount(0);
    await expect(page.getByTestId("referral-summary")).toBeVisible();
    await expect(page.getByTestId("referral-currency")).toHaveCount(2);
    await expect(page.getByTestId("referral-reward")).toHaveCount(20);
    await expect(page.getByTestId("referral-reward-friend-order").first()).toContainText("№ 10000006");
    await page.getByTestId("referral-rewards-more").click();
    await expect(page.getByTestId("referral-reward")).toHaveCount(23);
    expect(await page.getByTestId("referral").innerText()).not.toContain("@");
    expect(state.writes).toEqual([]);
    await noHorizontalScroll(page);
    await noA11yViolations(page);
  });

  test("a person who can read the shop but not the programme does not even ask", async ({ page }) => {
    await mockApi(page);
    await mockShop(page, ["shop.read", "shop.manage"]);
    const state = await mockReferral(page, { permissions: ["event.read", "shop.read", "shop.manage"] });
    await signIn(page);
    await page.goto(settingsUrl);
    await expect(page.getByTestId("shop-settings-form")).toBeVisible();
    await expect(page.getByTestId("referral")).toHaveCount(0);
    expect(state.referralCalls).toEqual([]);
  });
});
