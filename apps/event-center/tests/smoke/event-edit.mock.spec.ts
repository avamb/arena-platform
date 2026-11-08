/**
 * The event screen against a MOCKED arena-api: the "Edit" button (owner and manager), the "Sales on the site"
 * switch, the "?" help and the shared "Save changes" bar of the edit page. Regression for the missing Edit button
 * reported on 2026-11-08. The header "+ Event" rule is covered by AppNav.test.tsx (showNewEventButton).
 */
import { expect, test, type Page } from "@playwright/test";
import { EVENT, ORG_A, mockApi } from "./mockApi";
import { MANAGER, OWNER, mockEventEdit } from "./eventEditMock";

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
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
      await expect(page.getByTestId("sales-state-set-soon")).toBeVisible();
      await expect(page.getByTestId("sales-state-set-sold_out")).toBeVisible();
      // The round "?" opens the explanation and Escape closes it.
      await page.getByTestId("help-sales-switch-button").click();
      await expect(page.getByTestId("help-sales-switch-text")).toContainText("SOLD OUT");
      await page.keyboard.press("Escape");
      await expect(page.getByTestId("help-sales-switch-text")).toBeHidden();
    });
  }

  test("without event.update there is no Edit button", async ({ page }) => {
    await mockApi(page);
    await mockEventEdit(page, "organizer", ["event.read", "order.read", "session.read"]);
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events/${EVENT}`);
    await expect(page.getByTestId("event-sales-switch")).toBeVisible();
    await expect(page.getByTestId("event-edit-link")).toHaveCount(0);
    await expect(page.getByTestId("sales-state-set-soon")).toHaveCount(0);
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
