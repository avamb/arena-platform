/**
 * "Refer a friend" (spec 52) against a MOCKED arena-api, on a phone viewport: the programme section of
 * the event card, the rules before the first start, pause / resume / end, the results, and the plate on
 * the refund of a friend order. The mock is referralMock.ts. There is no live twin yet.
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { EVENT, ORDER, ORG_A, mockApi } from "./mockApi";
import { READER, mockReferral } from "./referralMock";

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
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

const eventUrl = `/o/${ORG_A}/events/${EVENT}`;

const LIVE = {
  id: "0193f01a-0000-7000-8000-0000000000b1",
  event_id: EVENT,
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

test.describe("spec 52 refer a friend (mock)", () => {
  test("start: the rules come first, the tick is required, the body carries accept_terms", async ({ page }) => {
    await mockApi(page);
    const state = await mockReferral(page);
    await signIn(page);
    await page.goto(eventUrl);
    await expect(page.getByTestId("referral-none")).toBeVisible();
    await noHorizontalScroll(page);

    // A wrong number is told in the field and nothing is sent.
    await page.getByTestId("referral-friend").fill("0");
    await page.getByTestId("referral-cashback").fill("20");
    await page.getByTestId("referral-save").click();
    await expect(page.getByTestId("referral-friend-error")).toContainText("whole number from 1 to 100");
    expect(state.writes).toEqual([]);

    await page.getByTestId("referral-friend").fill("10");
    await page.getByTestId("referral-save").click();
    await expect(page.getByTestId("referral-rules")).toBeVisible();
    await expect(page.getByTestId("referral-rules-list").locator("li")).toHaveCount(4);
    await expect(page.getByTestId("referral-rules")).toContainText("not taken back");
    await expect(page.getByTestId("referral-rules")).toContainText("fee");
    await noA11yViolations(page);

    // Without the tick the dialog stays and says what to do, and nothing is sent.
    await page.getByTestId("referral-rules-submit").click();
    await expect(page.getByTestId("referral-rules")).toContainText("rules are not accepted");
    expect(state.writes).toEqual([]);

    await page.getByTestId("referral-rules-tick").check();
    await page.getByTestId("referral-rules-submit").click();
    await expect(page.getByTestId("referral-status")).toHaveText("Active");
    expect(state.writes).toHaveLength(1);
    expect(state.writes[0]).toContain('"accept_terms":true');
    expect(state.writes[0]).toContain('"friend_discount_pct":10');
    expect(state.writes[0]).toContain('"cashback_pct":20');
    expect(state.writes[0]).toContain('"cashback_cap_pct":100');
    expect(state.writes[0]).toContain('"max_friends":null');
    await expect(page.getByTestId("referral-terms")).toContainText("10% off");
    await noHorizontalScroll(page);
  });

  test("a change of a live programme sends no accept_terms and says it affects new codes only", async ({ page }) => {
    await mockApi(page);
    const state = await mockReferral(page, { program: LIVE });
    await signIn(page);
    await page.goto(eventUrl);
    await expect(page.getByTestId("referral-status")).toHaveText("Active");
    await expect(page.getByTestId("referral-form")).toContainText("new codes only");
    await page.getByTestId("referral-cashback").fill("25");
    await page.getByTestId("referral-max").fill("3");
    await page.getByTestId("referral-save").click();
    await expect(page.getByTestId("referral-status-note")).toContainText("saved");
    expect(state.writes).toHaveLength(1);
    expect(state.writes[0]).toContain('"cashback_pct":25');
    expect(state.writes[0]).toContain('"max_friends":3');
    expect(state.writes[0]).not.toContain("accept_terms");
  });

  test("pause, resume and end (after a question) change the state and ended is final", async ({ page }) => {
    await mockApi(page);
    const state = await mockReferral(page, { program: LIVE });
    await signIn(page);
    await page.goto(eventUrl);
    await expect(page.getByTestId("referral-status")).toHaveText("Active");
    await page.getByTestId("referral-pause").click();
    await expect(page.getByTestId("referral-status")).toHaveText("Paused");
    await page.getByTestId("referral-resume").click();
    await expect(page.getByTestId("referral-status")).toHaveText("Active");

    await page.getByTestId("referral-end").click();
    await expect(page.getByTestId("referral-end-dialog")).toBeVisible();
    // Nothing is ended before the question is answered.
    expect(state.writes.filter((w) => w.endsWith("/end"))).toEqual([]);
    await page.getByTestId("referral-end-dialog-submit").click();
    await expect(page.getByTestId("referral-status")).toHaveText("Ended");
    // Ended is final: no buttons, and the form now starts a NEW programme.
    await expect(page.getByTestId("referral-actions")).toHaveCount(0);
    await expect(page.getByTestId("referral-form")).toContainText("Start the programme");
    expect(state.writes).toEqual(["POST referral-program/pause", "POST referral-program/resume", "POST referral-program/end"]);
  });

  test("when no channel can pay the cashback the form is closed and says why", async ({ page }) => {
    await mockApi(page);
    await mockReferral(page, { supported: false });
    await signIn(page);
    await page.goto(eventUrl);
    await expect(page.getByTestId("referral-blocked")).toBeVisible();
    await expect(page.getByTestId("referral-blocked")).toContainText("cannot be paid");
    await expect(page.getByTestId("referral-blocked-channel")).toContainText("returns whole payments only");
    await expect(page.getByTestId("referral-form")).toHaveCount(0);
    await noHorizontalScroll(page);
    await noA11yViolations(page);
  });

  test("a reader sees the results but has no form and no buttons", async ({ page }) => {
    await mockApi(page);
    const state = await mockReferral(page, { permissions: READER, program: { ...LIVE, status: "paused" } });
    await signIn(page);
    await page.goto(eventUrl);
    await expect(page.getByTestId("referral-status")).toHaveText("Paused");
    await expect(page.getByTestId("referral-readonly")).toBeVisible();
    await expect(page.getByTestId("referral-form")).toHaveCount(0);
    await expect(page.getByTestId("referral-actions")).toHaveCount(0);
    await expect(page.getByTestId("referral-summary")).toBeVisible();
    expect(state.writes).toEqual([]);
  });

  test("an event card without promo.read does not even ask", async ({ page }) => {
    await mockApi(page);
    const state = await mockReferral(page, { permissions: ["event.read", "order.read", "session.read"] });
    await signIn(page);
    await page.goto(eventUrl);
    await expect(page.getByTestId("sales-figures").first()).toBeVisible();
    await expect(page.getByTestId("referral")).toHaveCount(0);
    expect(state.referralCalls).toEqual([]);
  });

  test("results: money per currency, rewards with a filter and Show more, no buyer contact", async ({ page }) => {
    await mockApi(page);
    await mockReferral(page, { program: LIVE });
    await signIn(page);
    await page.goto(eventUrl);
    await expect(page.getByTestId("referral-codes")).toHaveText("Personal codes issued: 14");
    await expect(page.getByTestId("referral-friends")).toHaveText("Friends who used a code: 23");
    await expect(page.getByTestId("referral-reversals")).toContainText("1");
    // Each currency is its own block: nothing is added across them.
    await expect(page.getByTestId("referral-currency")).toHaveCount(2);
    const eur = page.locator('[data-testid="referral-currency"][data-currency="EUR"]');
    await expect(eur).toContainText("Cashback paid");
    await expect(eur).not.toContainText("CZK");
    await expect(page.locator('[data-testid="referral-currency"][data-currency="CZK"]')).toContainText("could not be paid");

    await expect(page.getByTestId("referral-reward")).toHaveCount(20);
    await expect(page.getByTestId("referral-reward").first()).toContainText("The friend opened a dispute");
    await page.getByTestId("referral-rewards-more").click();
    await expect(page.getByTestId("referral-reward")).toHaveCount(23);
    await expect(page.getByTestId("referral-rewards-more")).toHaveCount(0);

    await page.getByTestId("referral-filter").selectOption("capped");
    await expect(page.getByTestId("referral-reward")).toHaveCount(1);
    await expect(page.getByTestId("referral-reward-reason")).toContainText("already got the most");
    const body = await page.getByTestId("referral").innerText();
    expect(body).not.toContain("@");
    await noHorizontalScroll(page);
    await noA11yViolations(page);
  });

  test("the refund of a friend order shows the cashback plate before the buttons and does not block", async ({ page }) => {
    await mockApi(page);
    await mockReferral(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/orders/${ORDER}`);
    await page.getByTestId("refund-open").click();
    const plate = page.getByTestId("refund-referral-cashback");
    await expect(plate).toBeVisible();
    await expect(plate).toContainText("ANNA-7K2Q");
    await expect(plate).toContainText("9.00");
    await expect(plate).toContainText("does not take that money back");
    // The refund itself is not blocked: the form and its Continue button are there.
    await expect(page.getByTestId("refund-pick")).toBeVisible();
    await noHorizontalScroll(page);
  });
});
