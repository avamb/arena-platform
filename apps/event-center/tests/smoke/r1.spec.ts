/**
 * R1 smoke: every read-only screen against a mocked arena-api, on a phone
 * viewport (375x812). Checks the flow, the per-organization permissions
 * (A1), the downloads and the basic accessibility rules of the spec.
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { EVENT, ORG_A, ORG_B, ORG_C, ORDER, SESSION, mockApi, newAuthState } from "./mockApi";

const MIN_TAP = 44;

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
}

/** WCAG 2.1 A and AA rules of axe on the screen as it is now (review finding L6). */
async function noA11yViolations(page: Page) {
  const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  const found = result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`);
  expect(found, "axe accessibility violations").toEqual([]);
}

async function noHorizontalScroll(page: Page) {
  const width = page.viewportSize()?.width ?? 375;
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(scrollWidth).toBeLessThanOrEqual(width);
}

// Against the built container (EVENT_CENTER_BASE_URL) the real CSP is on:
// a refused script, style or request fails the test instead of a blank page.
let cspViolations: string[] = [];
test.beforeEach(async ({ page }) => {
  cspViolations = [];
  page.on("console", (msg) => {
    if (msg.type() === "error" && /Content Security Policy|Refused to/i.test(msg.text())) cspViolations.push(msg.text());
  });
});
test.afterEach(() => {
  expect(cspViolations, "Content-Security-Policy violations").toEqual([]);
});

test.describe("R1 read-only event center", () => {
  test("wrong password is explained, right one opens the organization chooser", async ({ page }) => {
    await mockApi(page);
    await page.goto("/login");
    await page.getByLabel("E-mail").fill("manager@example.test");
    await page.getByLabel("Password").fill("wrong");
    await page.getByTestId("login-submit").click();
    await expect(page.getByRole("alert")).toContainText("does not match");
    // Review finding L6: the error is tied to the fields, not to the form.
    await expect(page.getByLabel("E-mail")).toHaveAttribute("aria-describedby", "login-error");
    await expect(page.getByLabel("Password")).toHaveAttribute("aria-describedby", "login-error");
    await noA11yViolations(page);
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

  test("the invitation letter's link sets the first password", async ({ page }) => {
    const state = await mockApi(page);
    await page.goto("/accept-invite?token=tok-1&email=anna%40example.test");
    await expect(page.getByRole("heading", { name: "Welcome to the Arena event center" })).toBeVisible();
    await expect(page.getByTestId("accept-invite")).toContainText("anna@example.test");
    // Password managers save the new password under a visible username field.
    const email = page.getByLabel("E-mail");
    await expect(email).toBeVisible();
    await expect(email).toHaveValue("anna@example.test");
    await expect(email).toHaveAttribute("readonly", "");
    await expect(email).toHaveAttribute("autocomplete", "username");
    await page.getByLabel("New password").fill("a long phrase");
    await page.getByLabel("Repeat the password").fill("a long phrase!");
    await page.getByTestId("password-submit").click();
    await expect(page.getByTestId("password-error")).toContainText("The two passwords are different");
    expect(state.requests).not.toContain("POST /v1/auth/password-reset/confirm");
    await page.getByLabel("Repeat the password").fill("a long phrase");
    await page.getByTestId("password-submit").click();
    await expect(page.getByTestId("password-done")).toBeVisible();
    expect(state.requests).toContain("POST /v1/auth/password-reset/confirm");
    await noA11yViolations(page);
    await noHorizontalScroll(page);
  });

  test("a used reset link says what to do next", async ({ page }) => {
    await mockApi(page);
    await page.goto("/reset-password?token=used-token");
    await expect(page.getByRole("heading", { name: "Choose a new password" })).toBeVisible();
    await page.getByLabel("New password").fill("a long phrase");
    await page.getByLabel("Repeat the password").fill("a long phrase");
    await page.getByTestId("password-submit").click();
    await expect(page.getByTestId("password-error")).toContainText("This link was already used");
    await expect(page.getByRole("link", { name: "Ask for a new letter" })).toBeVisible();
    await page.goto("/reset-password");
    await expect(page.getByTestId("password-missing")).toBeVisible();
    await noA11yViolations(page);
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
    await noA11yViolations(page);

    // Search filters by name.
    await page.getByPlaceholder("Search by name").fill("nothing like this");
    await expect(page.getByText("No event matches «nothing like this».")).toBeVisible();
    await page.getByPlaceholder("Search by name").fill("kopyt");

    // Event card.
    await page.getByRole("link", { name: /Serebryanoe kopyttse/ }).click();
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Serebryanoe kopyttse");
    // Review finding L6: the focus moves to the new screen's heading.
    await expect(page.getByRole("heading", { level: 1 })).toBeFocused();
    await expect(page.getByTestId("sales-figures").first()).toContainText("Paid: €540.00");
    await expect(page.getByTestId("session-row")).toContainText("(Prague)");
    await noHorizontalScroll(page);
    await noA11yViolations(page);

    const csv = page.waitForEvent("download");
    await page.getByTestId("csv-event-sales").click();
    expect((await csv).suggestedFilename()).toBe("sales-2026-11-20.csv");
    expect(state.requests.some((r) => r.startsWith(`GET /v1/organizations/${ORG_A}/events/${EVENT}/sales.csv`))).toBe(true);

    // Session.
    await page.getByTestId("session-row").getByRole("link").click();
    await expect(page.getByRole("heading", { level: 1 })).toContainText("20:00");
    await expect(page.getByRole("heading", { level: 1 })).toBeFocused();
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
    expect(state.requests.some((r) => r.startsWith("POST ") && r.includes("/orders/search") && r.includes('"q":"nobody"'))).toBe(true);
    // The search travels in the body: no request URL carries it.
    expect(state.requests.some((r) => r.split(" ")[1]?.includes("nobody"))).toBe(false);
    // Review finding L5: the buyer search never goes into the page address.
    expect(page.url()).not.toContain("nobody");
    await noA11yViolations(page);

    await page.goto(`/o/${ORG_A}/orders/${ORDER}`);
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Order №1000000512");
    await expect(page.getByTestId("delivery-state")).toContainText("Ticket e-mail: sent");
    await expect(page.getByTestId("ticket-delivery")).toContainText("Sent");
    await expect(page.getByTestId("buyer")).toContainText("anna@example.test");
    await expect(page.getByTestId("buyer").locator("a[href^=mailto]")).toHaveAttribute("href", "mailto:anna%40example.test");
    await noA11yViolations(page);
    await noHorizontalScroll(page);
  });

  test("4-1: resend the tickets of an order, to its address and to another one", async ({ page }) => {
    const state = await mockApi(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/orders/${ORDER}`);
    await page.getByTestId("resend-open").click();
    await expect(page.getByTestId("resend-dialog")).toBeVisible();
    await noA11yViolations(page);
    await page.getByTestId("resend-same").click();
    expect(state.requests.some((r) => r.includes("resend-tickets"))).toBe(false);
    await page.getByTestId("resend-send").click();
    await expect(page.getByTestId("resend-done")).toContainText("on their way");
    await page.getByTestId("resend-close").click();
    await page.getByTestId("resend-open").click();
    await page.getByTestId("resend-other").click();
    await page.getByTestId("resend-email").fill("site@example.test");
    await page.getByTestId("resend-continue").click();
    await page.getByTestId("resend-send").click();
    await expect(page.getByTestId("resend-refused-site")).toContainText("sold through the seller's own site");
    await noHorizontalScroll(page);
  });

  test("the orders search survives a visit elsewhere and is forgotten at sign-out", async ({ page }) => {
    const state = await mockApi(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/orders`);
    await page.getByLabel("Search for an order").fill("anna@example.test");
    await page.getByRole("button", { name: "Search" }).click();
    await expect(page.getByTestId("order-row")).toBeVisible();

    // In-app navigation keeps the text.
    await page.getByRole("navigation").getByRole("link", { name: "Events" }).click();
    await page.getByRole("navigation").getByRole("link", { name: "Orders" }).click();
    await expect(page.getByLabel("Search for an order")).toHaveValue("anna@example.test");

    // Sign-out wipes it, without a page reload in between.
    await page.getByTestId("logout").click();
    await expect(page).toHaveURL(/\/login/);
    await page.getByLabel("E-mail").fill("manager@example.test");
    await page.getByLabel("Password").fill("right-password");
    const before = state.requests.length;
    await page.getByTestId("login-submit").click();
    await expect(page).toHaveURL(new RegExp(`/o/${ORG_A}/orders`));
    await expect(page.getByLabel("Search for an order")).toHaveValue("");
    await expect(page.getByTestId("order-row").first()).toBeVisible();
    const after = state.requests.slice(before).filter((r) => r.includes("/orders/search"));
    expect(after.length).toBeGreaterThan(0);
    expect(after.some((r) => r.includes('"q"'))).toBe(false);
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
    // Review finding L2: the agent's event card says what the role can see.
    await page.getByTestId("event-row").first().getByRole("link").first().click();
    await expect(page.getByTestId("no-sales-note")).toContainText("This screen shows the event only.");
    await expect(page.getByTestId("session-list")).toHaveCount(0);
    await expect(page.getByTestId("csv-event-sales")).toHaveCount(0);
    await noA11yViolations(page);
    // Review finding M2: a 403 names the organization and leads back.
    await page.goto(`/o/${ORG_B}/orders`);
    const error = page.getByTestId("error");
    await expect(error).toContainText("You have no access to this in Vino & Co.");
    await expect(error).not.toContainText("{org}");
    await error.getByTestId("back-to-list").click();
    await expect(page).toHaveURL(new RegExp(`/o/${ORG_B}/events$`));
  });

  // Review finding L1: a superadmin works in admin-web, member or not.
  test("a superadmin who is also a member gets the admin console hint", async ({ page }) => {
    const state = await mockApi(page, newAuthState(), { superadmin: true });
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events`);
    await expect(page.getByTestId("superadmin-note")).toBeVisible();
    expect(state.requests.some((r) => r.startsWith(`GET /v1/organizations/${ORG_A}/events`))).toBe(false);
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

  // Review finding M3: "Duplicate tab" copies sessionStorage. The mock API
  // rotates the refresh token and revokes everything when an old one comes
  // back, like arena-api. Short access tokens force a rotation per use.
  test("a duplicated tab never reuses the rotated refresh token", async ({ page, context }) => {
    const auth = newAuthState(20_000);
    await mockApi(page, auth);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events`);
    await expect(page.getByTestId("event-row").first()).toBeVisible();

    const copied = await page.evaluate(() => JSON.stringify(Object.entries(sessionStorage)));
    const dup = await context.newPage();
    await dup.addInitScript((entries) => {
      if (sessionStorage.length === 0) for (const [k, v] of JSON.parse(entries) as [string, string][]) sessionStorage.setItem(k, v);
    }, copied);
    await mockApi(dup, auth);
    await dup.goto(`/o/${ORG_A}/events`);
    await expect(dup.getByTestId("event-row").first()).toBeVisible();

    // The first tab reloads (it loses its lead), then the duplicate reloads.
    await page.reload();
    await expect(page.getByTestId("event-row").first()).toBeVisible();
    await dup.reload();
    await expect(dup.getByTestId("event-row").first()).toBeVisible();

    expect(auth.refreshes).toBeGreaterThanOrEqual(2);
    expect(auth.compromised, "an old refresh token was sent again").toBe(false);
    await dup.close();
  });

  // Review finding M1: sign-out revokes the server session even after the
  // access token expired.
  test("sign-out sends the latest refresh token with a valid access token", async ({ page }) => {
    const auth = newAuthState(20_000);
    const state = await mockApi(page, auth);
    await signIn(page);
    const logout = page.waitForRequest((r) => r.url().endsWith("/v1/auth/logout"));
    await page.getByRole("button", { name: "Sign out" }).click();
    const req = await logout;
    expect(req.postDataJSON()).toEqual({ refresh_token: auth.current });
    expect(req.headers()["authorization"]).toBe(`Bearer at-${auth.refreshes + 1}`);
    expect(state.requests.filter((r) => r.startsWith("POST /v1/auth/refresh")).length).toBeGreaterThanOrEqual(1);
    await expect(page).toHaveURL(/\/login/);
  });
});
