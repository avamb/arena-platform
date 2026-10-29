/**
 * The shop screens against a MOCKED arena-api, on a phone viewport (spec 47,
 * section 6). The products list, the product form with its variants (the stock the
 * person saw goes back as expected_stock, a 409 reloads the rows), the orders list
 * and its filters, the order card with ship, cancel, refund and the courier text, and
 * the settings. Every screen is checked for axe violations and for a sideways scroll.
 *
 * Shopify parity (A-02, A-07, A-08, A-10, A-11): ONE Save on the product card with the
 * unsaved-changes question, the stock edited in the products list from a phone, the quick
 * tabs over the orders with their counts, the internal note of an order and the repeat e-mail.
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

    await expect(page.getByTestId("shop-product-save")).toBeDisabled();
    await page.getByTestId("shop-variant-stock-0").fill("8");
    await expect(page.getByTestId("shop-product-save")).toBeEnabled();
    state.stockConflict = true;
    await page.getByTestId("shop-product-save").click();
    await expect(page.getByTestId("shop-stock-changed")).toContainText("The stock changed while you were editing");
    // The rows now show the server's number, and nothing was written over it.
    await expect(page.getByTestId("shop-variant-stock-0")).toHaveValue("9");
    await expect(page.getByTestId("shop-stock-moved")).toContainText("you saw 12, now 9");
    expect(state.writes[0]).toContain('"expected_stock":12');

    await page.getByTestId("shop-variant-stock-0").fill("5");
    await page.getByTestId("shop-product-save").click();
    await expect(page.getByTestId("shop-savebar-status")).toContainText("Everything is saved.");
    expect(state.writes[1]).toContain('"expected_stock":9');
    expect(state.writes[1]).toContain('"stock":5');
  });

  test("a price that is not a number is named before anything is sent", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products/${PRODUCT_WINE}`);
    await page.getByTestId("shop-variant-price-1").fill("lots");
    await page.getByTestId("shop-product-save").click();
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
    await page.getByTestId("shop-product-save").click();
    await expect(page.getByTestId("shop-savebar-status")).toContainText("Everything is saved.");
    expect(state.writes[0]).toContain('"price_minor":1700');
    expect(state.writes[0]).toContain('"track_stock":false');
    expect(state.writes[0]).not.toContain("expected_stock");
  });

  test("the product card has ONE Save: it sends the details, then the variants, and says one thing", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products/${PRODUCT_WINE}`);
    await expect(page.getByTestId("shop-product-save")).toHaveCount(1);
    await expect(page.getByTestId("shop-variants-save")).toHaveCount(0);
    await expect(page.getByTestId("shop-product-save")).toBeDisabled();
    await page.getByTestId("shop-product-title").fill("Riesling Reserve");
    await page.getByTestId("shop-variant-stock-1").fill("6");
    await expect(page.getByTestId("shop-savebar-status")).toContainText("There are unsaved changes.");
    await page.getByTestId("shop-product-save").click();
    await expect(page.getByTestId("shop-savebar-status")).toContainText("Everything is saved.");
    expect(state.writes).toHaveLength(2);
    expect(state.writes[0]).toContain("PATCH");
    expect(state.writes[0]).toContain('"title":"Riesling Reserve"');
    expect(state.writes[1]).toContain("PUT");
    expect(state.writes[1]).toContain('"stock":6');
    await expect(page.getByTestId("shop-product-save")).toBeDisabled();
  });

  test("the Save bar sits at the bottom of a phone, where the thumb reaches it", async ({ page }) => {
    await mockApi(page);
    await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products/${PRODUCT_WINE}`);
    await page.getByTestId("shop-product-title").fill("Riesling Reserve");
    const bar = await page.getByTestId("shop-savebar").boundingBox();
    const height = page.viewportSize()?.height ?? 812;
    expect(bar).not.toBeNull();
    expect((bar?.y ?? 0) + (bar?.height ?? 0)).toBeGreaterThanOrEqual(height - 1);
    const button = await page.getByTestId("shop-product-save").boundingBox();
    expect(button?.height ?? 0).toBeGreaterThanOrEqual(44);
    // It stays there while the page scrolls.
    await page.evaluate(() => window.scrollTo(0, 800));
    const after = await page.getByTestId("shop-savebar").boundingBox();
    expect((after?.y ?? 0) + (after?.height ?? 0)).toBeGreaterThanOrEqual(height - 1);
    await noHorizontalScroll(page);
    await noA11yViolations(page);
  });

  test("leaving the card with unsaved changes asks first, and a clean card is left at once", async ({ page }) => {
    await mockApi(page);
    await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products/${PRODUCT_WINE}`);
    // Clean: the menu works without a question.
    await page.getByTestId("shop-nav-orders").click();
    await expect(page).toHaveURL(new RegExp("/shop/orders"));

    await page.goto(`/o/${ORG_A}/shop/products/${PRODUCT_WINE}`);
    await page.getByTestId("shop-product-title").fill("Riesling Reserve");
    await page.getByTestId("shop-nav-orders").click();
    await expect(page.getByTestId("unsaved-dialog")).toContainText("There are unsaved changes. Leave?");
    await noA11yViolations(page);
    await page.getByTestId("unsaved-stay").click();
    await expect(page).toHaveURL(new RegExp("/shop/products/"));
    await expect(page.getByTestId("shop-product-title")).toHaveValue("Riesling Reserve");
    // Closing the tab asks the browser to confirm.
    const prevented = await page.evaluate(() => {
      const e = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(e);
      return e.defaultPrevented;
    });
    expect(prevented).toBe(true);
    await page.getByTestId("shop-nav-orders").click();
    await page.getByTestId("unsaved-leave").click();
    await expect(page).toHaveURL(new RegExp("/shop/orders"));
  });

  test("the products list edits the stock of a single-variant product in the row, with a big field", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products`);
    const row = page.getByTestId("shop-product-row").filter({ hasText: "Tote bag" });
    const field = row.getByTestId("shop-stock-input");
    await expect(field).toHaveValue("5");
    const box = await field.boundingBox();
    expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
    await noHorizontalScroll(page);
    await noA11yViolations(page);

    await field.fill("11");
    await field.press("Enter");
    await expect(row.getByTestId("shop-stock-status")).toContainText("Saved");
    expect(state.writes).toHaveLength(1);
    expect(state.writes[0]).toContain("PUT");
    expect(state.writes[0]).toContain('"stock":11');
    expect(state.writes[0]).toContain('"expected_stock":5');
    await expect(field).toHaveValue("11");

    // A value that is not a number is named and nothing is sent.
    await field.fill("lots");
    await field.blur();
    await expect(row.getByTestId("shop-stock-status")).toContainText("Type a whole number");
    expect(state.writes).toHaveLength(1);
  });

  test("a product with several variants shows the total and the stock of each under a disclosure, and a 409 shows the new number", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/products`);
    const row = page.getByTestId("shop-product-row").filter({ hasText: "Riesling" });
    await expect(row.getByTestId("shop-stock-total")).toContainText("Total: 12");
    await expect(row.getByTestId("shop-stock-input").first()).toBeHidden();
    await row.getByText("Stock", { exact: true }).click();
    await expect(row.getByTestId("shop-stock-input")).toHaveCount(2);
    await noA11yViolations(page);

    state.stockConflict = true;
    await row.getByTestId("shop-stock-input").first().fill("8");
    await row.getByTestId("shop-stock-input").first().blur();
    const line = row.getByTestId("shop-stock-moved-line");
    await expect(line).toContainText("Someone changed the stock first.");
    await expect(line).toContainText("you saw 12, now 9");
    await expect(line).toContainText("You typed 8");
    await expect(row.getByTestId("shop-stock-input").first()).toHaveValue("9");
    expect(state.writes[0]).toContain('"expected_stock":12');
  });

  test("orders: the To ship tab counts paid orders with something to hand over and lists only them", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders`);
    await expect(page.getByTestId("shop-orders-tab-to_ship")).toContainText("To ship (1)");
    await expect(page.getByTestId("shop-orders-tab-unpaid")).toContainText("Unpaid (1)");
    await expect(page.getByTestId("shop-orders-tab-all")).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByTestId("shop-order-row")).toHaveCount(4);
    const tab = await page.getByTestId("shop-orders-tab-to_ship").boundingBox();
    expect(tab?.height ?? 0).toBeGreaterThanOrEqual(44);

    await page.getByTestId("shop-orders-tab-to_ship").click();
    await expect(page.getByTestId("shop-orders-tab-to_ship")).toHaveAttribute("aria-pressed", "true");
    const rows = page.getByTestId("shop-order-row");
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText("№1042");
    expect(page.url()).toContain("view=to_ship");
    expect(state.orderQueries[state.orderQueries.length - 1]).toContain("view=to_ship");
    await noA11yViolations(page);

    // The status filter still works: picking a status leaves the tab.
    await page.getByTestId("shop-orders-status").selectOption("manual_review");
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText("№1043");
    await expect(page.getByTestId("shop-orders-tab-all")).toHaveAttribute("aria-pressed", "true");

    // A shipped order leaves the list and the number follows.
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_PAID}`);
    await page.getByTestId("shop-order-ship").click();
    await page.getByTestId("shop-ship-submit").click();
    await expect(page.getByTestId("shop-order-note")).toContainText("marked as shipped");
    await page.getByTestId("shop-nav-orders").click();
    await expect(page.getByTestId("shop-orders-tab-to_ship")).toContainText("To ship (0)");
    await page.getByTestId("shop-orders-tab-to_ship").click();
    await expect(page.getByText("Nothing to ship. Every paid order has gone out.")).toBeVisible();
  });

  test("order card: the internal note saves when the person leaves the field and names its author", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_PAID}`);
    const note = page.getByTestId("shop-order-note-input");
    await expect(note).toHaveValue("");
    await noHorizontalScroll(page);
    await noA11yViolations(page);
    await note.fill("Called, deliver after 18:00");
    await note.blur();
    await expect(page.getByTestId("shop-order-note-state")).toContainText("Note saved.");
    await expect(page.getByTestId("shop-order-note-by")).toContainText("Dana Levi");
    expect(state.writes).toHaveLength(1);
    expect(state.writes[0]).toContain("PATCH");
    expect(state.writes[0]).toContain("Called, deliver after 18:00");
    // It is still there after a reload.
    await page.reload();
    await expect(page.getByTestId("shop-order-note-input")).toHaveValue("Called, deliver after 18:00");
    await expect(page.getByTestId("shop-order-note-by")).toContainText("Dana Levi");
  });

  test("a reader sees the note as text and has no repeat e-mail button", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page, ["event.read", "shop.read"]);
    state.orders[0].staff_note = { text: "Ring twice", by: "Dana Levi", at: "2026-10-29T15:30:00Z" };
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_PAID}`);
    await expect(page.getByTestId("shop-order-note-text")).toHaveText("Ring twice");
    await expect(page.getByTestId("shop-order-note-input")).toHaveCount(0);
    await expect(page.getByTestId("shop-order-resend")).toHaveCount(0);
  });

  test("order card: the e-mail is sent again, and a second try inside the minute is explained", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_PAID}`);
    await noA11yViolations(page);
    await page.getByTestId("shop-order-resend").click();
    await expect(page.getByTestId("shop-order-note")).toHaveText("The e-mail is queued.");
    expect(state.resent).toEqual(["received"]);
    await page.getByTestId("shop-order-resend").click();
    await expect(page.getByTestId("shop-order-note")).toContainText("less than a minute ago");
    expect(state.resent).toEqual(["received"]);
  });

  test("order card: a shipped order asks which e-mail to send again", async ({ page }) => {
    await mockApi(page);
    const state = await mockShop(page);
    state.orders[0].status = "shipped";
    state.orders[0].shipped_at = "2026-10-06T09:00:00Z";
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_PAID}`);
    await page.getByTestId("shop-order-resend").click();
    await expect(page.getByTestId("shop-resend-panel")).toContainText("anna@example.test");
    await noA11yViolations(page);
    await page.getByTestId("shop-resend-shipped").click();
    await expect(page.getByTestId("shop-order-note")).toHaveText("The e-mail is queued.");
    expect(state.resent).toEqual(["shipped"]);
  });

  test("an order nobody paid, or of digital goods, has no shipping letter to send again", async ({ page }) => {
    await mockApi(page);
    await mockShop(page);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_UNPAID}`);
    await expect(page.getByTestId("shop-order-resend")).toHaveCount(0);
    await page.goto(`/o/${ORG_A}/shop/orders/${ORDER_DIGITAL}`);
    await page.getByTestId("shop-order-resend").click();
    await expect(page.getByTestId("shop-resend-panel")).toHaveCount(0);
    await expect(page.getByTestId("shop-order-note")).toHaveText("The e-mail is queued.");
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
    // The shop has products: the currency is shown, not typed, and the note says who changes it.
    await expect(page.getByTestId("shop-settings-currency")).toHaveCount(0);
    await expect(page.getByTestId("shop-settings-currency-value")).toContainText("EUR");
    await expect(page.getByTestId("shop-settings-currency-locked")).toContainText("through support");
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
