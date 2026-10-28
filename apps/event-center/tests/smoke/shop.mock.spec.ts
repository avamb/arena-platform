/**
 * The shop screens against a MOCKED arena-api, on a phone viewport (spec 47,
 * section 6). The products list, the product form with its variants (the stock the
 * person saw goes back as expected_stock, a 409 reloads the rows), the orders list
 * and its filters, the order card with ship, cancel, refund and the courier text, and
 * the settings. Every screen is checked for axe violations and for a sideways scroll.
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { ORG_A, mockApi } from "./mockApi";
import { ORDER_DIGITAL, ORDER_PAID, ORDER_REVIEW, ORDER_UNPAID, PRODUCT_GUIDE, PRODUCT_WINE, mockShop } from "./shopMock";

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

async function typeWord(page: Page, text: string) {
  await page.getByTestId("confirm-word").getByLabel("Type the word").fill(text);
  await page.getByTestId("confirm-word-submit").click();
}

async function confirmPassword(page: Page) {
  const dialog = page.getByTestId("step-up");
  await expect(dialog).toBeVisible();
  await dialog.getByLabel("Password").fill("right-password");
  await dialog.getByRole("button", { name: "Confirm" }).click();
}

test.describe("shop (mock)", () => {
  test("the menu offers the shop, and the products list shows kind, status, price range and stock", async ({ page }) => {
    await mockApi(page);
    await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events`);
    await expect(page.getByTestId("nav-shop")).toBeVisible();
    await page.getByTestId("nav-shop").click();
    await expect(page).toHaveURL(new RegExp(`/o/${ORG_A}/shop/products`));
    const rows = page.getByTestId("shop-product-row");
    await expect(rows).toHaveCount(3);
    await expect(rows.first()).toContainText("Riesling");
    await expect(rows.first()).toContainText("Physical goods");
    await expect(rows.first()).toContainText("On sale");
    await expect(rows.first()).toContainText("to");
    await expect(rows.first()).toContainText("12");
    await expect(rows.filter({ hasText: "Tote bag" })).toContainText("Draft");
    await expect(rows.filter({ hasText: "Wine guide" })).toContainText("Digital file");
    await noHorizontalScroll(page);
    await noA11yViolations(page);

    await page.getByTestId("shop-products-status").selectOption("draft");
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText("Tote bag");
  });

  test("a person without shop.read sees no menu entry and an explanation", async ({ page }) => {
    await mockApi(page);
    await mockShop(page, ["event.read", "order.read"]);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events`);
    await expect(page.getByTestId("nav-shop")).toHaveCount(0);
    await page.goto(`/o/${ORG_A}/shop/products`);
    await expect(page.getByTestId("shop-no-access")).toContainText("no access to the shop");
  });

  test("a reader cannot change anything: no create, save or delete buttons", async ({ page }) => {
    await mockApi(page);
    await mockShop(page, ["event.read", "shop.read"]);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products`);
    await expect(page.getByTestId("shop-product-row").first()).toBeVisible();
    await expect(page.getByTestId("shop-product-new")).toHaveCount(0);
    await page.goto(`/o/${ORG_A}/shop/products/${PRODUCT_WINE}`);
    await expect(page.getByTestId("shop-read-only")).toBeVisible();
    await expect(page.getByTestId("shop-product-save")).toHaveCount(0);
    await expect(page.getByTestId("shop-variants-save")).toHaveCount(0);
    await expect(page.getByTestId("shop-product-delete")).toHaveCount(0);
  });

  test("variants: the stock the person saw goes back, and a 409 reloads the numbers", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products/${PRODUCT_WINE}`);
    await expect(page.getByTestId("shop-product-title")).toHaveValue("Riesling");
    await expect(page.getByTestId("shop-product-slug")).toHaveAttribute("readonly", "");
    await expect(page.getByTestId("shop-variant-row")).toHaveCount(2);
    await expect(page.getByTestId("shop-variant-stat-0")).toContainText("In unpaid orders: 2");
    await noHorizontalScroll(page);
    await noA11yViolations(page);

    await expect(page.getByTestId("shop-variants-save")).toBeDisabled();
    await page.getByTestId("shop-variant-stock-0").fill("8");
    await expect(page.getByTestId("shop-variants-save")).toBeEnabled();
    state.stockConflict = true;
    await page.getByTestId("shop-variants-save").click();
    await expect(page.getByTestId("shop-stock-changed")).toContainText("The stock changed while you were editing");
    // The rows now show the server's number, and nothing was written over it.
    await expect(page.getByTestId("shop-variant-stock-0")).toHaveValue("9");
    await expect(page.getByTestId("shop-stock-moved")).toContainText("you saw 12, now 9");
    expect(state.writes[0]).toContain('"expected_stock":12');

    await page.getByTestId("shop-variant-stock-0").fill("5");
    await page.getByTestId("shop-variants-save").click();
    await expect(page.getByTestId("shop-variants-notice")).toContainText("The variants are saved.");
    expect(state.writes[1]).toContain('"expected_stock":9');
    expect(state.writes[1]).toContain('"stock":5');
  });

  test("a price that is not a number is named before anything is sent", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products/${PRODUCT_WINE}`);
    await page.getByTestId("shop-variant-price-1").fill("lots");
    await page.getByTestId("shop-variants-save").click();
    await expect(page.getByTestId("shop-variant-price-1-error")).toContainText("Type a price, for example 12.50.");
    expect(state.writes).toEqual([]);
  });

  test("create: the product is made with its variant, then the card opens", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products/new`);
    await page.getByTestId("shop-product-create").click();
    await expect(page.getByTestId("shop-product-title-error")).toBeVisible();
    expect(state.writes).toEqual([]);

    await page.getByTestId("shop-product-title").fill("Gift card");
    await page.getByTestId("shop-product-kind").selectOption("service");
    await page.getByTestId("shop-variant-price-0").fill("25");
    await page.getByTestId("shop-variant-stock-0").fill("100");
    await noA11yViolations(page);
    await page.getByTestId("shop-product-create").click();
    await expect(page.getByTestId("shop-product-created")).toBeVisible();
    await expect(page.getByTestId("shop-product-title")).toHaveValue("Gift card");
    expect(state.writes).toHaveLength(1);
    expect(state.writes[0]).toContain('"title":"Gift card"');
    expect(state.writes[0]).toContain('"kind":"service"');
    expect(state.writes[0]).toContain('"price_minor":2500');
    expect(state.writes[0]).toContain('"stock":100');
  });

  test("orders: filters go to the API, and the search text never goes into the address", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders`);
    const rows = page.getByTestId("shop-order-row");
    await expect(rows).toHaveCount(4);
    await expect(rows.first()).toContainText("№1042");
    await noHorizontalScroll(page);
    await noA11yViolations(page);

    await page.getByTestId("shop-orders-status").selectOption("manual_review");
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText("Needs a manual check");
    await page.getByTestId("shop-orders-reset").click();
    await expect(rows).toHaveCount(4);

    await page.getByTestId("shop-orders-q").fill("boris");
    await page.getByTestId("shop-orders-from").fill("2026-10-01");
    await page.getByTestId("shop-orders-to").fill("2026-10-31");
    await page.getByTestId("shop-orders-apply").click();
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText("Boris Mock");
    expect(page.url()).not.toContain("boris");
    const last = state.orderQueries[state.orderQueries.length - 1];
    expect(last).toContain("q=boris");
    expect(last).toContain("from=2026-10-01");
    expect(last).toContain("to=2026-11-01");
  });

  test("order card: a parked payment says why, the courier text is copied, the order is shipped", async ({ page, context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_REVIEW}`);
    await expect(page.getByTestId("shop-order-review")).toContainText("needs a check");
    await expect(page.getByTestId("shop-order-lines")).toContainText("Riesling");
    await noHorizontalScroll(page);
    await noA11yViolations(page);

    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_PAID}`);
    await expect(page.getByTestId("shop-order-address")).toContainText("Calle Mayor 1");
    await expect(page.getByTestId("shop-order-comment")).toContainText("Ring twice");
    await expect(page.getByTestId("shop-order-payment")).toContainText("stripe");
    await page.getByTestId("shop-order-courier").click();
    await expect(page.getByTestId("shop-order-note")).toContainText("Copied");
    expect(await page.evaluate(() => navigator.clipboard.readText())).toContain("Calle Mayor 1, 2B");

    await page.getByTestId("shop-order-ship").click();
    await page.getByTestId("shop-ship-note").fill("1Z999AA10123456784");
    await noA11yViolations(page);
    await page.getByTestId("shop-ship-submit").click();
    await expect(page.getByTestId("shop-order-note")).toContainText("marked as shipped");
    await expect(page.getByTestId("shop-order-tracking")).toContainText("1Z999AA10123456784");
    expect(state.writes[0]).toContain("/ship");
    expect(state.writes[0]).toContain("1Z999AA10123456784");
  });

  test("refund: part of the money, the word and the password, then the card shows the rest", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_PAID}`);
    await page.getByTestId("shop-order-refund").click();
    await page.getByTestId("shop-refund-amount").fill("99999");
    await page.getByTestId("shop-refund-continue").click();
    await expect(page.getByTestId("shop-refund-amount-error")).toContainText("more than what is left");
    expect(state.writes).toEqual([]);

    await page.getByTestId("shop-refund-amount").fill("10");
    await page.getByTestId("shop-refund-reason").fill("Broken bottle");
    await page.getByTestId("shop-refund-continue").click();
    await expect(page.getByTestId("confirm-word")).toContainText("REFUND");
    await typeWord(page, "refund");
    await confirmPassword(page);
    await expect(page.getByTestId("shop-order-note")).toContainText("The money went back");
    expect(state.writes).toHaveLength(1);
    expect(state.writes[0]).toContain("su-token");
    expect(state.writes[0]).toContain('"amount_minor":1000');
    expect(state.writes[0]).toContain("Broken bottle");
    await expect(page.getByTestId("shop-order-totals")).toContainText("Left to refund");
    await expect(page.getByTestId("shop-order-timeline")).toContainText("Money returned");
  });

  test("cancel: only an unpaid order, behind the order word and the password", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_PAID}`);
    await expect(page.getByTestId("shop-order-cancel")).toHaveCount(0);

    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_UNPAID}`);
    await expect(page.getByTestId("shop-order-refund")).toHaveCount(0);
    await page.getByTestId("shop-order-cancel").click();
    await typeWord(page, "nonsense");
    await expect(page.getByTestId("confirm-word-note")).toBeVisible();
    expect(state.writes).toEqual([]);
    await typeWord(page, "cancel order");
    await confirmPassword(page);
    await expect(page.getByTestId("shop-order-note")).toContainText("The order is canceled.");
    expect(state.writes[0]).toContain("su-token");
  });

  test("a digital product has a file and no stock controls, and saves its variant without stock", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products/${PRODUCT_GUIDE}`);
    await expect(page.getByTestId("shop-product-kind")).toHaveValue("digital");
    await expect(page.getByTestId("shop-product-digital-state")).toContainText("A file is attached.");
    await expect(page.getByTestId("shop-variants-digital-note")).toBeVisible();
    await expect(page.getByTestId("shop-variant-track-0")).toHaveCount(0);
    await expect(page.getByTestId("shop-variant-stock-0")).toHaveCount(0);
    await noA11yViolations(page);
    await page.getByTestId("shop-variant-price-0").fill("17");
    await page.getByTestId("shop-variants-save").click();
    await expect(page.getByTestId("shop-variants-notice")).toBeVisible();
    expect(state.writes[0]).toContain('"price_minor":1700');
    expect(state.writes[0]).toContain('"track_stock":false');
    expect(state.writes[0]).not.toContain("expected_stock");
  });

  test("the option names label the variant columns", async ({ page }) => {
    await mockApi(page);
    await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products/${PRODUCT_WINE}`);
    await expect(page.getByTestId("shop-option-name-1")).toHaveValue("Vintage");
    await expect(page.getByTestId("shop-variants").getByLabel("Vintage").first()).toHaveValue("2021");
    await expect(page.getByTestId("shop-variants").getByLabel("Size").first()).toHaveValue("0.75 l");
  });

  test("an order of digital goods has no address block and a line that says its kind", async ({ page }) => {
    await mockApi(page);
    await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders`);
    await expect(page.getByTestId("shop-order-row").filter({ hasText: "№1045" })).toContainText("No delivery");
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_DIGITAL}`);
    await expect(page.getByTestId("shop-order-card")).toBeVisible();
    await expect(page.getByTestId("shop-order-address")).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Delivery address" })).toHaveCount(0);
    await expect(page.getByTestId("shop-order-line-kind")).toContainText("Digital file");
    await noA11yViolations(page);
  });

  test("refund needs the refund rights too: a manager without them sees no Refund button", async ({ page }) => {
    await mockApi(page);
    await mockShop(page, ["event.read", "shop.read", "shop.manage"]);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_PAID}`);
    await expect(page.getByTestId("shop-order-ship")).toBeVisible();
    await expect(page.getByTestId("shop-order-refund")).toHaveCount(0);
  });

  test("settings: values are shown, the domain is read only, a change is saved as a whole row", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/settings`);
    await expect(page.getByTestId("shop-settings-currency")).toHaveValue("EUR");
    await expect(page.getByTestId("shop-settings-fee")).toHaveValue("4.90");
    await expect(page.getByTestId("shop-settings-channel")).toHaveValue("ch1");
    await expect(page.getByTestId("shop-settings-domain-value")).toContainText("shop.example.test");
    await expect(page.getByTestId("shop-settings-domain")).toHaveCount(0);
    await noHorizontalScroll(page);
    await noA11yViolations(page);

    await page.getByTestId("shop-settings-fee").fill("x");
    await page.getByTestId("shop-settings-save").click();
    await expect(page.getByTestId("shop-settings-fee-error")).toBeVisible();
    expect(state.writes).toEqual([]);

    await page.getByTestId("shop-settings-fee").fill("12,5");
    await page.getByTestId("shop-settings-age").fill("18");
    await page.getByTestId("shop-settings-maxqty").fill("10");
    await page.getByTestId("shop-settings-save").click();
    await expect(page.getByTestId("shop-settings-saved")).toBeVisible();
    expect(state.writes).toHaveLength(1);
    expect(state.writes[0]).toContain('"shipping_fee_minor":1250');
    expect(state.writes[0]).toContain('"age_gate_min_age":18');
    expect(state.writes[0]).toContain('"max_line_qty":10');
    expect(state.writes[0]).toContain('"sales_channel_id":"ch1"');
    expect(state.writes[0]).not.toContain("shop_domain");
  });
});

test.describe("shop (mock, desktop)", () => {
  test.use({ viewport: { width: 1280, height: 800 }, isMobile: false, hasTouch: false, deviceScaleFactor: 1 });

  test("every shop screen has no axe violations and no sideways scroll", async ({ page }) => {
    await mockApi(page);
    await mockShop(page);
    await signIn(page);
    for (const path of ["shop/products", `shop/products/${PRODUCT_WINE}`, "shop/orders", `shop/orders/${ORDER_REVIEW}`, `shop/orders/${ORDER_DIGITAL}`, "shop/settings"]) {
      await page.goto(`/o/${ORG_A}/${path}`);
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
      await expect(page.getByTestId("loading")).toHaveCount(0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth), path).toBeLessThanOrEqual(1280);
      await noA11yViolations(page);
    }
  });

  test("the orders table is a table with six columns on a wide screen", async ({ page }) => {
    await mockApi(page);
    await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders`);
    await expect(page.getByTestId("shop-orders-table").getByRole("columnheader")).toHaveCount(6);
    await expect(page.getByTestId("shop-order-row")).toHaveCount(4);
  });
});
