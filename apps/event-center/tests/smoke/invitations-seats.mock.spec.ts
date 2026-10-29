/**
 * Smoke of the seat-map invitations screen (spec 46, INV-04) against a MOCKED
 * arena-api. The twin that goes through the real API is
 * live/invitations-seats.spec.ts.
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { EVENT, ORG_A, SESSION, mockApi } from "./mockApi";
import { mockInvitationsSeats } from "./invitationsSeatsMock";

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
}

const url = `/o/${ORG_A}/events/${EVENT}/sessions/${SESSION}/invitations`;
const seat = (page: Page, key: string) => page.locator(`[data-k="${key}"]`);
const picked = (page: Page) => page.getByTestId("inv-picked-seat");

async function start(page: Page) {
  await page.setViewportSize({ width: 1280, height: 900 });
  await mockApi(page);
  const state = await mockInvitationsSeats(page);
  await signIn(page);
  await page.goto(url);
  await expect(page.getByTestId("inv-hall")).toBeVisible();
  return state;
}

test.describe("invitations on the seat map (mock)", () => {
  // The first test meets a cold dev server and walks a long scenario.
  test.describe.configure({ timeout: 60_000 });
  test("a seat held back and a free seat are issued in one request, a sold one is refused, the list follows the tickets", async ({ page }) => {
    const state = await start(page);

    await seat(page, "A|1|3").click(); // sold
    await expect(page.getByTestId("inv-note")).toContainText("cannot be given away");
    await expect(picked(page)).toHaveCount(0);

    await seat(page, "A|2|3").click(); // held back by the organizer
    await seat(page, "A|2|2").click();
    await expect(picked(page)).toHaveCount(2);
    await expect(page.locator('[data-testid="inv-picked-seat"][data-seat="A|2|3"]')).toHaveAttribute("data-origin", "unavailable");
    await expect(page.getByTestId("inv-from-reserve")).toContainText("1");

    // No name: nothing is sent.
    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-name-error")).toBeVisible();
    expect(state.issued).toHaveLength(0);

    await page.getByTestId("inv-name").fill("Ana López");
    await page.getByTestId("inv-email").fill("ana@example.com");
    await page.getByTestId("inv-delivery-my_email").check();
    await page.getByTestId("inv-submit").click();

    await expect(page.getByTestId("inv-result")).toContainText("2 places for Ana López");
    expect(state.issued).toHaveLength(1);
    const body = state.issued[0] as { request_id: string; seats: unknown; guest: Record<string, unknown>; delivery: string };
    expect(body.request_id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    expect(body.seats).toEqual([{ seat_key: "A|2|2" }, { seat_key: "A|2|3" }]);
    expect(body.guest).toMatchObject({ name: "Ana López", email: "ana@example.com", phone: null, locale: "en" });
    expect(body.delivery).toBe("my_email");

    // The pick is empty again, the way of sending stays for the next guest, the name does not.
    await expect(picked(page)).toHaveCount(0);
    await expect(page.getByTestId("inv-name")).toHaveValue("");
    await expect(page.getByTestId("inv-delivery-my_email")).toBeChecked();

    // The list: first "issuing", then valid once the tickets exist.
    const row = page.getByTestId("inv-row").first();
    await expect(row).toContainText("Ana López");
    await expect(row).toContainText("Marta Gil");
    await expect(row).toHaveAttribute("data-state", "active", { timeout: 15_000 });
  });

  test("a seat taken meanwhile leaves the pick and is outlined, the form stays, the next send goes through", async ({ page }) => {
    const state = await start(page);
    await seat(page, "A|2|2").click();
    await seat(page, "A|2|4").click();
    await page.getByTestId("inv-name").fill("Carlos Ruiz");
    await page.getByTestId("inv-delivery-pdf_only").check();

    state.takeOnNextIssue = "A|2|2";
    await page.getByTestId("inv-submit").click();

    await expect(page.getByTestId("inv-conflict")).toContainText("Parter, row 2, seat 2");
    await expect(picked(page)).toHaveCount(1);
    await expect(page.locator('[data-testid="inv-picked-seat"][data-seat="A|2|4"]')).toBeVisible();
    await expect(page.getByTestId("inv-name")).toHaveValue("Carlos Ruiz");
    await expect(page.getByTestId("inv-delivery-pdf_only")).toBeChecked();
    // The map was read again: the taken seat is outlined in red.
    await expect(seat(page, "A|2|2")).toHaveAttribute("stroke", "#dc2626");

    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-result")).toBeVisible();
    expect(state.issued).toHaveLength(2);
    // The refused attempt and the new one are different requests.
    expect((state.issued[0] as { request_id: string }).request_id).not.toBe((state.issued[1] as { request_id: string }).request_id);
  });

  test("a lost answer locks the form and map, and Retry sends the SAME request, the server answers the first invitation", async ({ page }) => {
    const state = await start(page);
    await seat(page, "B|1|2").click();
    await page.getByTestId("inv-name").fill("Marta Gil");
    await page.getByTestId("inv-delivery-pdf_only").check();

    // The server commits the invitation, the answer is lost.
    state.dropNextAnswer = true;
    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-stalled")).toBeVisible();
    await expect(page.getByTestId("inv-error")).toBeVisible();
    // Locked: the fields are disabled, a tap on the map picks nothing, "send" is gone.
    await expect(page.getByTestId("inv-name")).toBeDisabled();
    await expect(page.getByTestId("inv-submit")).toHaveCount(0);
    await seat(page, "B|1|3").click();
    await expect(picked(page)).toHaveCount(1);

    // The hall is read again in the background: the seat is sold now (by our own invitation), yet it stays in the pick.
    await page.waitForResponse((r) => r.url().includes("/seats/admin") && r.request().method() === "GET", { timeout: 15_000 });
    await expect(picked(page)).toHaveCount(1);
    await expect(page.getByTestId("inv-conflict")).toHaveCount(0);

    await page.getByTestId("inv-retry").click();
    await expect(page.getByTestId("inv-result")).toBeVisible();
    await expect(page.getByTestId("inv-replayed")).toBeVisible();
    expect(state.issued).toHaveLength(2);
    expect((state.issued[0] as { request_id: string }).request_id).toBe((state.issued[1] as { request_id: string }).request_id);
    // The retry is the same body, byte for byte.
    expect(state.issued[1]).toEqual(state.issued[0]);
    await expect(page.getByTestId("inv-row")).toHaveCount(1);
    await expect(page.getByTestId("inv-name")).toBeEnabled();
  });

  test("Discard gives up an open attempt and unlocks the form", async ({ page }) => {
    const state = await start(page);
    await seat(page, "B|1|2").click();
    await page.getByTestId("inv-name").fill("Marta Gil");
    await page.getByTestId("inv-delivery-pdf_only").check();
    state.dropNextAnswer = true;
    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-stalled")).toBeVisible();
    await page.getByTestId("inv-discard").click();
    await expect(page.getByTestId("inv-name")).toBeEnabled();
    await expect(page.getByTestId("inv-submit")).toBeVisible();
    // The invitation did go through on the server: the list shows it.
    await expect(page.getByTestId("inv-row")).toHaveCount(1);
  });

  test("one ticket of an invitation can be canceled, the rest stays valid", async ({ page }) => {
    const state = await start(page);
    state.issuingReads = 0;
    await seat(page, "A|2|3").click(); // from the reserve
    await seat(page, "A|2|2").click(); // from sale
    await page.getByTestId("inv-name").fill("Ana López");
    await page.getByTestId("inv-delivery-pdf_only").check();
    await page.getByTestId("inv-submit").click();
    const row = page.getByTestId("inv-row").first();
    await expect(row).toHaveAttribute("data-state", "active", { timeout: 15_000 });
    await expect(row.getByTestId("inv-row-place")).toHaveCount(2);

    // The place from the reserve: the question names only that one place.
    await row.locator('[data-testid="inv-row-place"]', { hasText: "seat 3" }).getByTestId("inv-cancel-place").click();
    const dialog = page.getByTestId("inv-cancel-dialog");
    await expect(dialog).toContainText("1 seats go back to your reserve");
    await expect(dialog).not.toContainText("go back on sale");
    await page.getByTestId("inv-cancel-dialog-submit").click();
    await expect(page.getByTestId("inv-cancelled")).toBeVisible();
    expect(state.cancelled).toEqual([{ ticket_ids: ["tk-A|2|3"], notify_guest: true }]);

    // The row lists only the live place and counts the canceled one. No per-ticket cancel on the last place.
    await expect(row).toHaveAttribute("data-state", "partial");
    await expect(row.getByTestId("inv-row-place")).toHaveCount(1);
    await expect(row.getByTestId("inv-row-canceled")).toContainText("1");
    await expect(row.getByTestId("inv-cancel-place")).toHaveCount(0);
    expect(state.seats.get("A|2|3")).toBe("unavailable");
    expect(state.seats.get("A|2|2")).toBe("sold");

    // The whole-invitation question now counts only the live place.
    await row.getByTestId("inv-cancel").click();
    await expect(page.getByTestId("inv-cancel-dialog")).toContainText("1 places go back on sale");
    await expect(page.getByTestId("inv-cancel-dialog")).not.toContainText("your reserve");
  });

  test("cancel asks where the seats go back, tells the guest by default, and the list shows it canceled", async ({ page }) => {
    const state = await start(page);
    state.issuingReads = 0;
    await seat(page, "A|2|3").click(); // from the reserve
    await seat(page, "A|2|2").click(); // from sale
    await page.getByTestId("inv-name").fill("Ana López");
    await page.getByTestId("inv-delivery-pdf_only").check();
    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-result")).toBeVisible();

    const row = page.getByTestId("inv-row").first();
    await expect(row).toHaveAttribute("data-state", "active", { timeout: 15_000 });
    await row.getByTestId("inv-cancel").click();
    const dialog = page.getByTestId("inv-cancel-dialog");
    await expect(dialog).toContainText("1 seats go back to your reserve");
    await expect(dialog).toContainText("1 places go back on sale");
    await expect(page.getByTestId("inv-cancel-notify")).toBeChecked();

    // Closing the question changes nothing.
    await page.getByTestId("inv-cancel-dialog-cancel").click();
    await expect(dialog).toHaveCount(0);
    expect(state.cancelled).toHaveLength(0);

    await row.getByTestId("inv-cancel").click();
    await page.getByTestId("inv-cancel-notify").uncheck();
    await page.getByTestId("inv-cancel-dialog-submit").click();
    await expect(page.getByTestId("inv-cancelled")).toBeVisible();
    expect(state.cancelled).toEqual([{ notify_guest: false }]);
    // A fully canceled invitation keeps the order status paid: the row says canceled from the ticket counts.
    await expect(row).toHaveAttribute("data-state", "cancelled");
    await expect(row.getByTestId("inv-cancel")).toHaveCount(0);
    // The seat from the reserve is closed again, the one from sale is free: the map was read again.
    expect(state.seats.get("A|2|3")).toBe("unavailable");
    expect(state.seats.get("A|2|2")).toBe("available");
    await expect(seat(page, "A|2|3")).toHaveAttribute("fill", "url(#sm-hatch)");
  });

  test("the PDF button says plainly when the server does not have the function yet", async ({ page }) => {
    const state = await start(page);
    state.issuingReads = 0;
    await seat(page, "B|1|2").click();
    await page.getByTestId("inv-name").fill("Ana López");
    await page.getByTestId("inv-delivery-pdf_only").check();
    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-result")).toBeVisible();
    const result = page.getByTestId("inv-result");
    await expect(result.getByTestId("inv-pdf")).toBeEnabled({ timeout: 15_000 });
    await result.getByTestId("inv-pdf").click();
    await expect(result.getByTestId("inv-action-say")).toContainText("not available in this version yet");
  });

  test("a person without the right to issue sees the list but no form", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await mockApi(page);
    await mockInvitationsSeats(page, ["event.read", "session.read", "complimentary.read"]);
    await signIn(page);
    await page.goto(url);
    await expect(page.getByTestId("inv-list")).toBeVisible();
    await expect(page.getByTestId("inv-issue")).toHaveCount(0);
  });

  test("a person who cannot open the hall is told so, and the screen does not ask for the hall", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    const api = await mockApi(page);
    await mockInvitationsSeats(page, ["event.read", "session.read", "tier.read", "complimentary.read", "complimentary.issue"]);
    await signIn(page);
    await page.goto(url);
    await expect(page.getByTestId("inv-no-hall-access")).toBeVisible();
    await expect(page.getByTestId("inv-hall")).toHaveCount(0);
    await expect(page.getByTestId("inv-list")).toBeVisible();
    expect(api.requests.filter((r) => r.includes("/seats/admin") || r.includes("/schema"))).toEqual([]);
    // The session page still links here for a person who may issue.
    await page.goto(`/o/${ORG_A}/events/${EVENT}/sessions/${SESSION}`);
    await expect(page.getByTestId("session-invitations-link")).toBeVisible();
  });

  test("phone: nothing scrolls sideways and axe finds nothing", async ({ page }) => {
    await mockApi(page);
    await mockInvitationsSeats(page);
    await signIn(page);
    await page.goto(url);
    await expect(page.getByTestId("inv-hall")).toBeVisible();
    const width = page.viewportSize()?.width ?? 375;
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
    const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
  });
});
