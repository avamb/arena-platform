/**
 * 4-3 smoke: the promo-code screens against a MOCKED arena-api, on a phone
 * viewport. The twin test that walks the bot's e2e scenario through the real
 * API is live/promo.spec.ts; this one keeps the screens honest in the plain
 * `npm run test:e2e` run.
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { EVENT, ORG_A, SESSION, mockApi } from "./mockApi";
import { PROMO_EUR, PROMO_USED, mockPromo } from "./promoMock";

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

test.describe("4-3 promo codes (mock)", () => {
  test("list shows state, discount and usage; the card says several currencies and never prints an e-mail", async ({ page }) => {
    await mockApi(page);
    await mockPromo(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/promo`);
    await expect(page.getByTestId("promo-count")).toHaveText("Codes: 2");
    const rows = page.getByTestId("promo-row");
    await expect(rows.first()).toContainText("SPRING");
    await expect(rows.first()).toContainText("10% off");
    await expect(rows.first()).toContainText("several currencies");
    await expect(rows.nth(1)).toContainText("EURFIVE");
    await noHorizontalScroll(page);
    await noA11yViolations(page);

    await page.getByTestId("promo-code").first().click();
    await expect(page.getByTestId("promo-card-code")).toHaveText("SPRING");
    await expect(page.getByTestId("promo-usage")).toContainText("several currencies");
    await expect(page.getByTestId("promo-usage-row")).toHaveCount(2);
    await expect(page.getByTestId("promo-usage-table")).toContainText("Alice Mock");
    // The buyer's NAME is shown, never the e-mail.
    const body = await page.locator("body").innerText();
    expect(body).not.toContain("alice.mock@example.test");
    expect(body).not.toContain("example.test");
    await noHorizontalScroll(page);
    await noA11yViolations(page);
  });

  test("create: a name that is taken is refused before the call, then a club code is created", async ({ page }) => {
    await mockApi(page);
    const state = await mockPromo(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/promo/new`);
    // Case does not matter for a name.
    await page.getByTestId("promo-f-code").fill("spring");
    await page.getByTestId("promo-f-value").fill("15");
    await page.getByTestId("promo-review").click();
    await expect(page.getByRole("alert")).toContainText("already have a code with this name");
    expect(state.writes).toEqual([]);

    await page.getByTestId("promo-f-code").fill("club15");
    await page.getByTestId("promo-review").click();
    await expect(page.getByTestId("promo-review")).toBeVisible();
    await page.getByTestId("promo-create-go").click();
    await expect(page.getByTestId("promo-card-code")).toHaveText("CLUB15");
    // An empty list is a club code: every session, future ones too.
    expect(state.writes).toHaveLength(1);
    expect(state.writes[0]).toContain('"applies_to_session_ids":[]');
    expect(state.writes[0]).toContain('"code":"CLUB15"');
    expect(state.writes[0]).toContain('"discount_value":15');
  });

  test("create: a fixed discount names its currency and chosen sessions need a pick", async ({ page }) => {
    await mockApi(page);
    const state = await mockPromo(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/promo/new`);
    await page.getByTestId("promo-f-code").fill("five");
    await page.getByTestId("promo-f-type-fixed_amount").check();
    await page.getByTestId("promo-f-currency").fill("eur");
    await page.getByTestId("promo-f-value").fill("5,50");
    await page.getByTestId("promo-f-scope-pick").check();
    await page.getByTestId("promo-review").click();
    await expect(page.getByRole("alert")).toContainText("Choose at least one session");
    await page.getByTestId("promo-pick-event").selectOption({ index: 1 });
    await page.getByTestId("promo-pick-session").first().check();
    await page.getByTestId("promo-review").click();
    await page.getByTestId("promo-create-go").click();
    await expect(page.getByTestId("promo-card-code")).toHaveText("FIVE");
    expect(state.writes[0]).toContain('"discount_type":"fixed_amount"');
    expect(state.writes[0]).toContain('"discount_value":550');
    expect(state.writes[0]).toContain('"currency":"EUR"');
    expect(state.writes[0]).toContain(`"applies_to_session_ids":["${SESSION}"]`);
  });

  test("pause, all sessions, and delete only after the word", async ({ page }) => {
    await mockApi(page);
    const state = await mockPromo(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/promo/${PROMO_EUR}`);
    await page.getByTestId("promo-pause").click();
    await expect(page.getByTestId("promo-activate")).toBeVisible();
    expect(state.writes.at(-1)).toContain('"status":"paused"');
    await page.getByTestId("promo-activate").click();
    await expect(page.getByTestId("promo-pause")).toBeVisible();

    await page.getByTestId("promo-sessions-open").click();
    // Nothing chosen would silently mean "all sessions": saving is off until a pick.
    await page.getByTestId("promo-pick-event").selectOption({ value: EVENT });
    await page.getByTestId("promo-pick-none").click();
    await expect(page.getByTestId("promo-sessions-save")).toBeDisabled();
    await page.getByTestId("promo-sessions-all").click();
    expect(state.writes.at(-1)).toContain('"applies_to_session_ids":[]');
    await expect(page.getByTestId("promo-sessions")).toContainText("All sessions");

    await page.getByTestId("promo-delete").click();
    await page.getByRole("textbox").fill("nope");
    await page.getByRole("button", { name: "Delete the code" }).last().click();
    expect(state.writes.some((w) => w.startsWith("DELETE"))).toBe(false);
    await page.getByRole("textbox").fill("delete");
    await page.getByRole("button", { name: "Delete the code" }).last().click();
    await expect(page.getByTestId("promo-notice")).toContainText("EURFIVE is deleted");
    expect(state.writes.some((w) => w.startsWith("DELETE"))).toBe(true);
    await expect(page.getByTestId("promo-count")).toHaveText("Codes: 1");
  });

  test("a role without promo permissions sees no entry and a plain explanation", async ({ page }) => {
    await mockApi(page);
    await mockPromo(page, ["event.read"]);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events`);
    await expect(page.getByTestId("nav-promo")).toHaveCount(0);
    await page.goto(`/o/${ORG_A}/promo`);
    await expect(page.getByTestId("promo-no-access")).toContainText("does not include promo codes");
    await page.goto(`/o/${ORG_A}/promo/${PROMO_USED}`);
    await expect(page.getByTestId("promo-no-access")).toBeVisible();
  });
});
