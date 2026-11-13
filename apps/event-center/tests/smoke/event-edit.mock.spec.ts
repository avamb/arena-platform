/**
 * The event screen against a MOCKED arena-api: the "Edit" button (owner and manager), the "Sales on the site"
 * switch, the "?" help and the shared "Save changes" bar of the edit page. Regression for the missing Edit button
 * reported on 2026-11-08. "Create event" lives on the Events heading only, the header has no create button.
 */
import { expect, test, type Page } from "@playwright/test";
import { EVENT, ORG_A, mockApi } from "./mockApi";
import { MANAGER, OWNER, mockEventEdit } from "./eventEditMock";

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByRole("heading", { name: "Select an organization" })).toBeVisible();
}

const cases = [
  { who: "owner", role: "org_admin" as const, permissions: OWNER },
  { who: "manager", role: "organizer" as const, permissions: MANAGER },
];

test.describe("event screen: Edit, sales switch, Save changes (mock)", () => {
  for (const c of cases) {
    test(`${c.who}: the card has Edit and the Sales on the site switch`, async ({ page }) => {
      await mockApi(page);
      await mockEventEdit(page, c.role, c.permissions);
      await signIn(page);
      await page.goto(`/o/${ORG_A}/events/${EVENT}`);
      await expect(page.getByTestId("event-edit-link")).toBeVisible();
      await expect(page.getByTestId("event-edit-link")).toHaveText("Edit");
      await expect(page.getByTestId("event-sales-switch")).toContainText("Sales on the site");
      await expect(page.getByRole("radiogroup")).toBeVisible();
      await expect(page.getByTestId("sales-state-set-open")).toHaveAttribute("aria-checked", "true");
      await expect(page.getByTestId("sales-state-set-soon")).toHaveAttribute("aria-checked", "false");
      await expect(page.getByTestId("sales-state-set-sold_out")).toBeVisible();
      // The round "?" opens the explanation and Escape closes it.
      await page.getByTestId("help-sales-switch-button").click();
      await expect(page.getByTestId("help-sales-switch-text")).toContainText("SOLD OUT");
      await page.keyboard.press("Escape");
      await expect(page.getByTestId("help-sales-switch-text")).toBeHidden();
    });
  }

  test("the card is five sections, only the first is open, and every block lives inside its section", async ({ page }) => {
    await mockApi(page, undefined, { sections: "default" });
    await mockEventEdit(page, "org_admin", OWNER);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events/${EVENT}`);
    const sections = page.getByTestId("event-sections");
    await expect(sections).toBeVisible();
    // The names and the order of the Telegram bot's card. The owner of the mock holds no link, promo or invitation rights.
    const order = ((await sections.getAttribute("data-order")) ?? "").split(" ");
    expect(order[0]).toBe("sales");
    expect(order.at(-1)).toBe("more");
    expect(order).toContain("edit");
    const names: Record<string, string> = { link: "Link for buyers", sales: "Sales and orders", edit: "Edit", promo: "Promotion", more: "More" };
    for (const id of order) await expect(page.getByTestId(`event-section-toggle-${id}`)).toContainText(names[id]);
    // Only the first section is open: its buttons are visible, the blocks of the others are not even in the page.
    await expect(page.getByTestId("event-section-toggle-sales")).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByTestId("event-section-toggle-edit")).toHaveAttribute("aria-expanded", "false");
    await expect(page.getByTestId("event-orders-link")).toBeVisible();
    await expect(page.getByTestId("event-edit-link")).toHaveCount(0);
    await expect(page.getByTestId("event-status")).toHaveCount(0);

    // At 375 px the five headings fit in one screen once everything is closed, with no horizontal overflow.
    const height = page.viewportSize()?.height ?? 812;
    await page.getByTestId("event-section-toggle-sales").click();
    const bottoms = await page
      .locator("[data-testid^='event-section-toggle-']")
      .evaluateAll((els) => els.map((e) => e.getBoundingClientRect().bottom));
    expect(bottoms).toHaveLength(order.length);
    expect(Math.max(...bottoms)).toBeLessThanOrEqual(height);
    // Pressing a heading opens the section: the Edit button and the sessions list are inside Edit.
    await page.getByTestId("event-section-toggle-edit").click();
    await expect(page.getByTestId("event-section-panel-edit").getByTestId("event-edit-link")).toBeVisible();
    await expect(page.getByTestId("event-section-panel-edit").getByTestId("session-list")).toBeVisible();
    await expect(page.getByTestId("event-section-panel-edit").getByTestId("event-sales-switch")).toBeVisible();
    await page.getByTestId("event-section-toggle-more").click();
    await expect(page.getByTestId("event-section-panel-more").getByTestId("event-status")).toBeVisible();
    const width = page.viewportSize()?.width ?? 375;
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
  });

  test("without event.update there is no Edit button", async ({ page }) => {
    await mockApi(page);
    await mockEventEdit(page, "organizer", ["event.read", "order.read", "session.read"]);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events/${EVENT}`);
    await expect(page.getByTestId("event-sales-switch")).toBeVisible();
    await expect(page.getByTestId("event-edit-link")).toHaveCount(0);
    await expect(page.getByTestId("sales-state-set-soon")).toHaveAttribute("aria-disabled", "true");
  });

  test("a press on a segment asks to confirm, changes the state and highlights it", async ({ page }) => {
    await mockApi(page);
    const state = await mockEventEdit(page, "org_admin", OWNER);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events/${EVENT}`);
    // Open while nothing is set by hand: nothing to do, and it says so instead of failing silently.
    await page.getByTestId("sales-state-set-open").click();
    await expect(page.getByTestId("sales-state-done")).toContainText("there is nothing to open");
    expect(state.salesStateWrites).toEqual([]);
    await page.getByTestId("sales-state-set-sold_out").click();
    await expect(page.getByTestId("sales-state-confirm")).toBeVisible();
    await page.getByTestId("sales-state-confirm-submit").click();
    await expect(page.getByTestId("sales-state-set-sold_out")).toHaveAttribute("aria-checked", "true");
    await expect(page.getByTestId("sales-state-set-open")).toHaveAttribute("aria-checked", "false");
    expect(state.salesStateWrites).toHaveLength(1);
  });

  test("Edit opens the editor, Save changes is disabled until something changed and saves in place", async ({ page }) => {
    await mockApi(page);
    const state = await mockEventEdit(page, "org_admin", OWNER);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events/${EVENT}`);
    await page.getByTestId("event-edit-link").click();
    await expect(page.getByTestId("event-details")).toBeVisible();
    const save = page.getByTestId("event-edit-save");
    await expect(save).toHaveText("Save changes");
    await expect(save).toBeDisabled();
    await page.getByLabel("Name of the event").fill("Silver Hoof");
    await expect(save).toBeEnabled();
    await save.click();
    await expect(page.getByTestId("event-edit-save-status")).toContainText("saved");
    await expect(save).toBeDisabled();
    expect(state.writes).toEqual([{ name: "Silver Hoof" }]);
    await expect(page).toHaveURL(new RegExp(`/events/${EVENT}/edit$`));
  });
});
