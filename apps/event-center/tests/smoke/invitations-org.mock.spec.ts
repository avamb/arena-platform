/**
 * Smoke of the organization-wide invitations screen on the staff-invitation
 * routes (spec 46, INV-04b) against a MOCKED arena-api. The twin that goes
 * through the real API is live/invitations.spec.ts.
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { EVENT, ORG_A, SESSION, mockApi } from "./mockApi";
import { SEATED, mockInvitationsOrg } from "./invitationsOrgMock";

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
}

const url = `/o/${ORG_A}/invitations`;
const rows = (page: Page) => page.getByTestId("org-inv-row");
const row = (page: Page, guest: string) => rows(page).filter({ hasText: guest });
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

async function start(page: Page, width = 1280) {
  await page.setViewportSize({ width, height: 900 });
  await mockApi(page);
  const state = await mockInvitationsOrg(page);
  await signIn(page);
  await page.goto(url);
  await expect(page.getByTestId("org-inv-list")).toBeVisible();
  await expect(rows(page).first()).toBeVisible();
  return state;
}

/** Picks the general admission date and its category in the issue form. */
async function pickCategory(page: Page) {
  await page.getByTestId("invite-event").selectOption({ label: "Serebryanoe kopyttse" });
  const session = page.getByTestId("invite-session");
  await expect(session.locator("option")).toHaveCount(3);
  await session.selectOption(SESSION);
  const tier = page.getByTestId("invite-tier");
  await expect(tier.locator("option")).toHaveCount(2);
  await tier.selectOption({ index: 1 });
}

test.describe("organization invitations (mock)", () => {
  test.describe.configure({ timeout: 60_000 });

  test("the list pages by cursor, the tabs split active from canceled, each row says who what and when", async ({ page }) => {
    const state = await start(page);

    // The first page is 20 rows of the 24 active ones. The fully canceled one is not there.
    await expect(rows(page)).toHaveCount(20);
    expect(state.listQueries[0]).toContain("state=active");
    expect(state.listQueries[0]).toContain("limit=20");
    expect(state.listQueries[0]).not.toContain("cursor=");
    await expect(row(page, "Guest 04")).toHaveCount(0);

    // A row: guest, order number, event and the venue's date, the issuer, the delivery.
    const first = row(page, "Guest 01");
    await expect(first).toContainText("Order 1000000100");
    await expect(first).toContainText("Serebryanoe kopyttse");
    await expect(first).toContainText("Palác Akropolis");
    await expect(first).toContainText("20:00"); // 19:00 UTC in Prague, the venue's zone
    await expect(first).toContainText("Issued by Marta Gil");
    await expect(first).toContainText("Delivery: Send to the guest's e-mail");
    await expect(first.getByTestId("org-inv-state")).toContainText("Valid");
    await expect(first.getByTestId("org-inv-csv")).toHaveCount(0);

    // From a CSV file, and a VIP place.
    await expect(row(page, "Guest 03").getByTestId("org-inv-csv")).toContainText("From a CSV file");
    await expect(row(page, "Guest 02").getByTestId("org-inv-vip")).toBeVisible();
    await expect(row(page, "Guest 02").getByTestId("org-inv-place")).toHaveCount(2);

    // The next page follows the cursor.
    await page.getByTestId("org-inv-more").click();
    await expect(rows(page)).toHaveCount(24);
    expect(state.listQueries.some((q) => q.includes("cursor=c20"))).toBe(true);
    await expect(page.getByTestId("org-inv-more")).toHaveCount(0);

    // Canceled: the one fully canceled invitation, no actions, its places kept as history.
    await page.getByTestId("org-inv-tab-cancelled").click();
    await expect(rows(page)).toHaveCount(1);
    const gone = row(page, "Guest 04");
    await expect(gone.getByTestId("org-inv-state")).toContainText("Canceled");
    await expect(gone.getByTestId("org-inv-cancel")).toHaveCount(0);
    await expect(gone.getByTestId("inv-pdf")).toHaveCount(0);

    // All: both pages again.
    await page.getByTestId("org-inv-tab-all").click();
    await expect(rows(page)).toHaveCount(20);
    await page.getByTestId("org-inv-more").click();
    await expect(rows(page)).toHaveCount(25);
  });

  test("the filters by event and date go to the server and drop the date with the event", async ({ page }) => {
    const state = await start(page);
    await page.getByTestId("org-inv-event").selectOption(EVENT);
    await expect.poll(() => state.listQueries.at(-1)).toContain(`event_id=${EVENT}`);
    await page.getByTestId("org-inv-session").selectOption(SEATED);
    await expect.poll(() => state.listQueries.at(-1)).toContain(`session_id=${SEATED}`);
    // Only the invitation of the seated date is left.
    await expect(rows(page)).toHaveCount(1);
    await expect(rows(page).first()).toContainText("Guest 05");
    await expect(rows(page).first().getByTestId("org-inv-place")).toContainText("Parter, row 2, seat 3");

    // Back to every event: the date goes, the list is whole again.
    await page.getByTestId("org-inv-event").selectOption("");
    await expect(page.getByTestId("org-inv-session")).toHaveCount(0);
    // The whole list may come from the cache, so judge it by what is shown.
    await expect(rows(page)).toHaveCount(20);
    await expect(row(page, "Guest 05")).toHaveCount(1);
  });

  test("general admission goes through the new route, one order per guest, with a VIP mark and a request id", async ({ page }) => {
    const state = await start(page);
    await pickCategory(page);
    await page.getByTestId("invite-guests").fill("Anna Ivanova, anna@example.com\nbob@example.com");
    await expect(page.getByTestId("invite-count")).toContainText("2");
    await page.getByTestId("invite-review").click();
    await page.getByTestId("invite-vip").check();
    expect(state.issued).toHaveLength(0);
    await page.getByTestId("invite-send").click();
    await expect(page.getByTestId("invite-done")).toBeVisible();

    expect(state.issued).toHaveLength(2);
    const [a, b] = state.issued as { request_id: string; ga: unknown; seats: unknown; guest: Record<string, unknown>; delivery: string; vip?: boolean }[];
    expect(a.request_id).toMatch(UUID);
    expect(b.request_id).toMatch(UUID);
    expect(a.request_id).not.toBe(b.request_id);
    expect(a.ga).toEqual([{ tier_id: "0193f01a-0000-7000-8000-0000000000f1", qty: 1 }]);
    expect(a.seats).toEqual([]);
    expect(a.guest).toMatchObject({ name: "Anna Ivanova", email: "anna@example.com" });
    // An e-mail only is printed under the address.
    expect(b.guest).toMatchObject({ name: "bob@example.com", email: "bob@example.com" });
    expect(a.delivery).toBe("guest_email");
    expect(a.vip).toBe(true);

    // The list follows: the new orders are on top, with the VIP mark on the new place.
    await expect(rows(page).first()).toContainText("bob@example.com");
    await expect(row(page, "Anna Ivanova").getByTestId("org-inv-vip")).toBeVisible();
  });

  test("a lost answer is retried with the SAME request id and never issues twice, a refusal gets a new id", async ({ page }) => {
    const state = await start(page);
    await pickCategory(page);
    await page.getByTestId("invite-guests").fill("carol@example.com");
    await page.getByTestId("invite-review").click();

    // The connection drops after the server processed the request.
    state.dropNextAnswer = true;
    await page.getByTestId("invite-send").click();
    await expect(page.getByTestId("invite-partial")).toBeVisible();
    // The form is locked while the attempt is open.
    await expect(page.getByTestId("invite-guests")).toBeDisabled();
    await page.getByTestId("invite-retry").click();
    await expect(page.getByTestId("invite-done")).toBeVisible();
    expect(state.issued).toHaveLength(2);
    const ids = (state.issued as { request_id: string }[]).map((x) => x.request_id);
    expect(ids[0]).toBe(ids[1]);
    // One order, not two.
    await expect(rows(page).filter({ hasText: "carol@example.com" })).toHaveCount(1);

    // A refusal by the server ends the attempt: the next try is a new request.
    await page.getByTestId("invite-again").click();
    await pickCategory(page);
    await page.getByTestId("invite-guests").fill("dave@example.com");
    await page.getByTestId("invite-review").click();
    state.refuseNextIssue = true;
    await page.getByTestId("invite-send").click();
    await expect(page.getByTestId("invite-error")).toContainText("not enough places");
    await page.getByTestId("invite-retry").click();
    await expect(page.getByTestId("invite-done")).toBeVisible();
    const refused = state.issued.at(-2) as { request_id: string };
    const retried = state.issued.at(-1) as { request_id: string };
    expect(retried.request_id).not.toBe(refused.request_id);
  });

  test("one place is canceled with the guest told by default, a whole invitation with that switched off", async ({ page }) => {
    const state = await start(page);

    // Guest 02 has two places: cancel one.
    const two = row(page, "Guest 02");
    await two.getByTestId("org-inv-cancel-place").first().click();
    const dialog = page.getByTestId("inv-cancel-dialog");
    await expect(dialog).toBeVisible();
    await expect(page.getByTestId("inv-cancel-notify")).toBeChecked();
    await expect(dialog).toContainText("go back on sale");
    await dialog.getByTestId("inv-cancel-dialog-submit").click();
    await expect(page.getByTestId("org-inv-cancelled")).toContainText("canceled");
    expect(state.cancelled).toHaveLength(1);
    expect(state.cancelled[0]).toMatchObject({ order_id: expect.any(String), ticket_ids: ["tk-1a"], notify_guest: true });
    // The ticket left the row, the other place stays and is now the last one: no per-place cancel.
    await expect(two.getByTestId("org-inv-place")).toHaveCount(1);
    await expect(two.getByTestId("org-inv-cancel-place")).toHaveCount(0);
    await expect(two.getByTestId("org-inv-state")).toContainText("Partly canceled");

    // Guest 01, the whole invitation, the guest not told.
    await row(page, "Guest 01").getByTestId("org-inv-cancel").click();
    await page.getByTestId("inv-cancel-notify").uncheck();
    await page.getByTestId("inv-cancel-dialog-submit").click();
    await expect(page.getByTestId("org-inv-cancelled")).toContainText("1000000100");
    expect(state.cancelled[1]).toEqual({ order_id: expect.any(String), notify_guest: false });
    // It left the active tab and shows on the canceled one.
    await expect(row(page, "Guest 01")).toHaveCount(0);
    await page.getByTestId("org-inv-tab-cancelled").click();
    await expect(row(page, "Guest 01")).toHaveCount(1);
  });

  test("earlier invitations stay visible with their old annul, and the section hides when there are none", async ({ page }) => {
    const state = await start(page);
    const section = page.getByTestId("earlier-invitations");
    await expect(section).toContainText("Earlier invitations");
    const old = section.getByTestId("invitation-row");
    await expect(old).toHaveCount(1);
    await expect(old).toContainText("Old Guest");
    await expect(old).toHaveAttribute("data-state", "valid");

    await old.getByTestId("invitation-annul").click();
    await expect(page.getByTestId("confirm-word")).toContainText("ANNUL");
    await page.getByTestId("confirm-word").getByLabel("Type the word").fill("annul");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("invitation-annulled")).toBeVisible();
    expect(state.revoked).toEqual(["0193f01a-0000-7000-8000-0000000e0001"]);
    await expect(old).toHaveAttribute("data-state", "revoked");
  });

  test("no earlier section for an organization that never used the old route", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await mockApi(page);
    const state = await mockInvitationsOrg(page);
    state.oldRows = 0;
    await signIn(page);
    await page.goto(url);
    await expect(rows(page).first()).toBeVisible();
    await expect(page.getByTestId("earlier-invitations")).toHaveCount(0);
  });

  test("a seated date is not issued here: the form links to its seat screen", async ({ page }) => {
    const state = await start(page);
    await page.getByTestId("invite-event").selectOption({ label: "Serebryanoe kopyttse" });
    await page.getByTestId("invite-session").selectOption(SEATED);
    await expect(page.getByTestId("invite-seated")).toBeVisible();
    await expect(page.getByTestId("invite-guests")).toHaveCount(0);
    await expect(page.getByTestId("invite-tier")).toHaveCount(0);
    await expect(page.getByTestId("invite-seated-link")).toHaveAttribute("href", `/o/${ORG_A}/events/${EVENT}/sessions/${SEATED}/invitations`);
    expect(state.issued).toHaveLength(0);
  });

  test("a person who may read but not issue or cancel sees the list and nothing to press", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await mockApi(page);
    await mockInvitationsOrg(page, ["event.read", "session.read", "tier.read", "complimentary.read"]);
    await signIn(page);
    await page.goto(url);
    await expect(rows(page).first()).toBeVisible();
    await expect(page.getByTestId("invite-form")).toHaveCount(0);
    await expect(page.getByTestId("org-inv-cancel")).toHaveCount(0);
    await expect(page.getByTestId("org-inv-cancel-place")).toHaveCount(0);
    await expect(page.getByTestId("inv-resend")).toHaveCount(0);
    await expect(page.getByTestId("inv-pdf").first()).toBeVisible();
  });

  test("phone: nothing scrolls sideways and axe finds nothing", async ({ page }) => {
    await start(page, 375);
    await page.getByTestId("org-inv-more").scrollIntoViewIfNeeded();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(0);
    const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(" | ")}`)).toEqual([]);
  });
});
