/**
 * 5-1 smoke: the "Status" block of the event card against a MOCKED arena-api,
 * on a phone viewport. The twin test that walks the bot's e2e scenario through
 * the real API is live/event-status.spec.ts; this one keeps the screen honest
 * in the plain `npm run test:smoke` run.
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { EVENT, ORG_A, mockApi } from "./mockApi";
import { mockStatus } from "./statusMock";

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

async function open(page: Page) {
  await signIn(page);
  await page.goto(`/o/${ORG_A}/events/${EVENT}`);
  await expect(page.getByTestId("event-status")).toBeVisible();
}

test.describe("5-1 event status (mock)", () => {
  test("a published event: take off sale after one confirmation that says tickets stay valid", async ({ page }) => {
    await mockApi(page);
    const state = await mockStatus(page);
    await open(page);
    await expect(page.getByTestId("event-status-current")).toContainText("Published");
    await expect(page.getByTestId("event-status-publish")).toHaveCount(0);
    await noA11yViolations(page);

    await page.getByTestId("event-status-off").click();
    await expect(page.getByTestId("event-off-dialog")).toContainText("Tickets already sold stay valid");
    expect(state.writes).toEqual([]);
    await page.getByTestId("event-off-dialog-cancel").click();
    expect(state.writes).toEqual([]);

    await page.getByTestId("event-status-off").click();
    await page.getByTestId("event-off-dialog-submit").click();
    await expect(page.getByTestId("event-status-done")).toContainText("off sale");
    await expect(page.getByTestId("event-status-current")).toContainText("Draft");
    expect(state.writes).toEqual([`POST /events/${EVENT}/status {"status":"draft"}`]);
    // A draft can be published again at once.
    await page.getByTestId("event-status-publish").click();
    await expect(page.getByTestId("event-status-current")).toContainText("Published");
  });

  test("archive says it is final; a refused publish says what to do", async ({ page }) => {
    await mockApi(page);
    const state = await mockStatus(page, { status: "draft" });
    await open(page);
    state.failNext = { status: 422, code: "event.publish_requires_priced_tier" };
    await page.getByTestId("event-status-publish").click();
    await expect(page.getByTestId("event-status-error")).toContainText("priced category");
    await page.getByTestId("event-status-publish").click();
    await expect(page.getByTestId("event-status-current")).toContainText("Published");

    await page.getByTestId("event-status-archive").click();
    await expect(page.getByTestId("event-archive-dialog")).toContainText("This is final");
    await page.getByTestId("event-archive-dialog-submit").click();
    await expect(page.getByTestId("event-status-current")).toContainText("Archived");
    await expect(page.getByTestId("event-status-publish")).toHaveCount(0);
    await expect(page.getByTestId("event-status-off")).toHaveCount(0);
  });

  test("delete: the word comes after the dry run, and a wrong word deletes nothing", async ({ page }) => {
    await mockApi(page);
    const state = await mockStatus(page);
    await open(page);
    await page.getByTestId("event-status-delete").click();
    await expect(page.getByTestId("dry-run-result")).toContainText("Nothing was sold");
    await page.getByTestId("dry-run-confirm").click();
    await expect(page.getByRole("alertdialog")).toBeVisible();
    await page.getByRole("textbox").fill("nope");
    await page.getByRole("button", { name: "Delete the event" }).click();
    await expect(page.getByRole("alertdialog")).toContainText("nothing was changed");
    expect(state.writes).toEqual([]);
    await page.getByRole("textbox").fill("delete");
    await page.getByRole("button", { name: "Delete the event" }).click();
    await expect(page).toHaveURL(new RegExp(`/o/${ORG_A}/events$`));
    expect(state.writes).toEqual([`DELETE /events/${EVENT}`]);
  });

  test("delete of a sold event is refused with the numbers and the archive is offered", async ({ page }) => {
    await mockApi(page);
    const state = await mockStatus(page, { impact: { sessions: 2, paid_orders: 3, tickets: 5 } });
    await open(page);
    await page.getByTestId("event-status-delete").click();
    await expect(page.getByTestId("dry-run-blocked")).toContainText("paid orders: 3, tickets: 5");
    await expect(page.getByTestId("dry-run-confirm")).toHaveCount(0);
    await noA11yViolations(page);
    await page.getByTestId("event-delete-archive-instead").click();
    await page.getByTestId("event-archive-dialog-submit").click();
    await expect(page.getByTestId("event-status-current")).toContainText("Archived");
    expect(state.writes).toEqual([`POST /events/${EVENT}/status {"status":"archived"}`]);
  });

  test("without event.publish and event.delete there is no block", async ({ page }) => {
    await mockApi(page);
    await mockStatus(page, { permissions: ["event.read", "order.read", "session.read"] });
    await signIn(page);
    await page.goto(`/o/${ORG_A}/events/${EVENT}`);
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByTestId("event-status")).toHaveCount(0);
  });
});
