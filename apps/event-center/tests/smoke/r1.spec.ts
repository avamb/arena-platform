/**
 * R1 smoke: every read-only screen against a mocked arena-api, on a phone
 * viewport (375x812). Checks the flow, the per-organization permissions
 * (A1), the downloads and the basic accessibility rules of the spec.
 */
import { expect, test, type Page } from "@playwright/test";
import { EVENT, ORG_A, ORG_B, ORG_C, ORDER, SESSION, mockApi } from "./mockApi";

const MIN_TAP = 44;

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
}

async function noHorizontalScroll(page: Page) {
  const width = page.viewportSize()?.width ?? 375;
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(scrollWidth).toBeLessThanOrEqual(width);
}

test.describe("R1 read-only event center", () => {
  test("wrong password is explained, right one opens the organization chooser", async ({ page }) => {
    await mockApi(page);
    await page.goto("/login");
    await page.getByLabel("E-mail").fill("manager@example.test");
    await page.getByLabel("Password").fill("wrong");
    await page.getByTestId("login-submit").click();
    await expect(page.getByRole("alert")).toContainText("does not match");
    await page.getByLabel("Password").fill("right-password");
    await page.getByTestId("login-submit").click();
    await expect(page.getByTestId("org-list")).toContainText("Teatr Kolybel");
    await expect(page.getByTestId("org-list")).toContainText("Your role: Agent");
    await noHorizontalScroll(page);
  });

  test("forgot password always answers the same way", async ({ page }) => {
    const state = await mockApi(page);
    await page.goto("/forgot-password");
    await page.getByLabel("E-mail").fill("someone@example.test");
    await page.getByRole("button", { name: "Send the link" }).click();
    await expect(page.getByTestId("forgot-sent")).toBeVisible();
    expect(state.requests).toContain("POST /v1/auth/password-reset/request");
  });

  test("a manager sees events with figures, the event, the session and downloads", async ({ page }) => {
    const state = await mockApi(page);
    await signIn(page);
    await page.getByRole("link", { name: /Teatr Kolybel/ }).click();
    await expect(page.getByTestId("org-name")).toHaveText("Teatr Kolybel");
    expect(state.requests).toContain(`GET /v1/organizations/${ORG_A}/me`);

    // Events: running first, figures from the summary.
    const row = page.getByTestId("event-row").first();
    await expect(row).toContainText("Serebryanoe kopyttse");
    await expect(row).toContainText("On sale");
    await expect(row.getByTestId("sales-figures")).toContainText("Sold 12 of 80");
    await expect(page.getByTestId("events-tab-arc")).toContainText("Archive (1)");
    await noHorizontalScroll(page);

    // Search filters by name.
    await page.getByPlaceholder("Search by name").fill("nothing like this");
    await expect(page.getByText("No event matches «nothing like this».")).toBeVisible();
    await page.getByPlaceholder("Search by name").fill("kopyt");

    // Event card.
    await page.getByRole("link", { name: /Serebryanoe kopyttse/ }).click();
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Serebryanoe kopyttse");
    await expect(page.getByTestId("sales-figures").first()).toContainText("Paid: €540.00");
    await expect(page.getByTestId("session-row")).toContainText("(Prague)");
    await noHorizontalScroll(page);

    const csv = page.waitForEvent("download");
    await page.getByTestId("csv-event-sales").click();
    expect((await csv).suggestedFilename()).toBe("sales-2026-11-20.csv");
    expect(state.requests.some((r) => r.startsWith(`GET /v1/organizations/${ORG_A}/events/${EVENT}/sales.csv`))).toBe(true);

    // Session.
    await page.getByTestId("session-row").getByRole("link").click();
    await expect(page.getByRole("heading", { level: 1 })).toContainText("20:00");
    await expect(page.getByText("Doors open")).toBeVisible();
    await expect(page.getByTestId("tiers-table")).toContainText("Parter");
    const pdf = page.waitForEvent("download");
    await page.getByTestId("sample-ticket").click();
    expect((await pdf).suggestedFilename()).toBe("sample-ticket.pdf");
    expect(state.requests.some((r) => r.startsWith(`GET /v1/organizations/${ORG_A}/sessions/${SESSION}/sample-ticket`))).toBe(true);
    await noHorizontalScroll(page);
  });

  test("orders: tabs, one search box, the order card with the letter status", async ({ page }) => {
    const state = await mockApi(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/orders`);
    await expect(page.getByTestId("order-row")).toContainText("№1000000512");
    await page.getByLabel("Search for an order").fill("nobody");
    await page.getByRole("button", { name: "Search" }).click();
    await expect(page.getByText("No order matches «nobody».")).toBeVisible();
    expect(state.requests.some((r) => r.includes("/orders?") && r.includes("q=nobody"))).toBe(true);

    await page.goto(`/o/${ORG_A}/orders/${ORDER}`);
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Order №1000000512");
    await expect(page.getByTestId("delivery-state")).toContainText("All ticket letters are sent.");
    await expect(page.getByTestId("ticket-delivery")).toContainText("Sent");
    await expect(page.getByTestId("buyer")).toContainText("anna@example.test");
    await noHorizontalScroll(page);
  });

  test("an agent sees no orders and no sales figures", async ({ page }) => {
    const state = await mockApi(page);
    await signIn(page);
    await page.goto(`/o/${ORG_B}/events`);
    await expect(page.getByTestId("org-name")).toHaveText("Vino & Co");
    await expect(page.getByRole("link", { name: "Orders" })).toHaveCount(0);
    await expect(page.getByTestId("event-row").first()).toBeVisible();
    await expect(page.getByTestId("sales-figures")).toHaveCount(0);
    expect(state.requests.some((r) => r.includes(`/organizations/${ORG_B}/events/`) && r.includes("/summary"))).toBe(false);
  });

  test("a foreign organization says how to get access", async ({ page }) => {
    await mockApi(page);
    await signIn(page);
    await page.goto(`/o/${ORG_C}/events`);
    await expect(page.getByTestId("not-member")).toContainText("not a member");
  });

  test("the language switch changes the texts and the tap targets are large enough", async ({ page }) => {
    await mockApi(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events`);
    await page.getByTestId("language-select").selectOption("ru");
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Мероприятия");
    await expect(page.locator("html")).toHaveAttribute("lang", "ru");
    for (const name of ["Мероприятия", "Заказы", "Уведомления"]) {
      const box = await page.getByRole("navigation").getByRole("link", { name }).boundingBox();
      expect(box?.height ?? 0).toBeGreaterThanOrEqual(MIN_TAP);
    }
  });

  test("a signed-out visitor is sent to sign in and comes back", async ({ page }) => {
    await mockApi(page);
    await page.goto(`/o/${ORG_A}/orders`);
    await expect(page).toHaveURL(/\/login\?redirect=/);
    await page.getByLabel("E-mail").fill("manager@example.test");
    await page.getByLabel("Password").fill("right-password");
    await page.getByTestId("login-submit").click();
    await expect(page).toHaveURL(new RegExp(`/o/${ORG_A}/orders`));
  });
});
