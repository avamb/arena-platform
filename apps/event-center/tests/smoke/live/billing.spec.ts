/**
 * Twin test "Billing → Usage" (task B2-7), on the three roles of one
 * organization, against a REAL arena-api. The seed binary writes a fixed set
 * of billing facts (internal/platform/billing/e2eseed): in the current UTC
 * month three sales of the main event (one of them reversed) and one sale of
 * a second event, and one sale the day before the month. Every sale costs
 * 0.59 EUR.
 *
 * The owner (billing.read) sees the month total per currency, the events, the
 * charge rows (a reversal marked) and the preliminary note; can open one
 * event and read it by rule and by session; and can go to the previous month.
 * The manager and the agent have no menu entry; if they open the address they
 * are told it is the owner's, no billing request is sent, and the API refuses
 * them (403).
 */
import { expect, test, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs as signInRaw, type Role } from "./liveEnv";

const env = liveEnv();
const MAIN_EVENT = env.event_name;
const SECOND_EVENT = "E2E Billing Second Show";

async function signInAs(page: Page, role: Role) {
  await signInRaw(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
}

const billingUrl = `/o/${env.org_id}/billing`;

test.describe("billing usage", () => {
  test("owner: totals per currency, events, rows with a reversal, one event, the previous month", async ({ page }) => {
    await signInAs(page, "org_admin");
    await page.getByTestId("nav-billing").click();
    await expect(page).toHaveURL(new RegExp(`${billingUrl}$`));
    await expect(page.getByTestId("billing-preliminary")).toContainText("preliminary");

    // 4 direct charges of 0.59, one reversal of -0.59.
    const total = page.getByTestId("billing-total-EUR");
    await expect(total).toContainText("€1.77");
    await expect(total).toContainText("Charged €2.36");
    await expect(total).toContainText("Reversed -€0.59");
    await expect(total).toContainText("Charges: 4");

    await expect(page.getByTestId("billing-row")).toHaveCount(5);
    const reversal = page.locator('[data-testid="billing-row"][data-reversal="true"]');
    await expect(reversal).toHaveCount(1);
    await expect(reversal.getByTestId("billing-row-amount")).toHaveText("-€0.59");
    await expect(reversal).toContainText("Reversal");

    const events = page.getByTestId("billing-event-row");
    await expect(events).toHaveCount(2);
    await expect(events.filter({ hasText: MAIN_EVENT })).toContainText("Charges: 3");
    await expect(events.filter({ hasText: MAIN_EVENT })).toContainText("€1.18");
    await expect(events.filter({ hasText: SECOND_EVENT })).toContainText("€0.59");
    await noA11yViolations(page);

    // One event: its summary by rule and by session, and the rows narrow down.
    await events.filter({ hasText: MAIN_EVENT }).getByRole("button").click();
    const summary = page.getByTestId("billing-event-summary");
    await expect(summary).toContainText(MAIN_EVENT);
    // The event summary is for the whole life of the event: its four direct
    // charges (one of the day before the month) less the one reversal.
    await expect(summary.getByTestId("billing-event-alltime")).toBeVisible();
    await expect(summary.getByTestId("billing-total-EUR")).toContainText("€1.77");
    await expect(summary.getByTestId("billing-total-EUR")).toContainText("Charges: 4");
    await expect(summary.getByTestId("billing-by-rule")).toContainText("Fixed fee per ticket");
    await expect(summary.getByTestId("billing-by-session").locator("li")).toHaveCount(1);
    await expect(page.getByTestId("billing-row")).toHaveCount(4);
    await page.getByTestId("billing-event-clear").click();
    await expect(page.getByTestId("billing-row")).toHaveCount(5);

    // The previous month holds the one sale of the day before; the next month
    // button is off in the current month.
    await expect(page.getByTestId("billing-month-next")).toBeDisabled();
    await page.getByTestId("billing-month-prev").click();
    await expect(page.getByTestId("billing-total-EUR")).toContainText("€0.59");
    await expect(page.getByTestId("billing-row")).toHaveCount(1);
    await expect(page.getByTestId("billing-month-next")).toBeEnabled();
  });

  for (const role of ["organizer", "agent"] as const) {
    test(`${role}: no menu entry, an explanation, no billing request, and the API says 403`, async ({ page, request }) => {
      await signInAs(page, role);
      await expect(page.getByTestId("nav-billing")).toHaveCount(0);

      const billingCalls: string[] = [];
      page.on("request", (req) => {
        if (req.url().includes("/billing/")) billingCalls.push(req.url());
      });
      await page.goto(billingUrl);
      await expect(page.getByTestId("billing-no-access")).toBeVisible();
      await expect(page.getByTestId("billing-total-EUR")).toHaveCount(0);
      expect(billingCalls, "a person without billing.read must not send a billing request").toEqual([]);

      const headers = { Authorization: `Bearer ${person(env, role).access_token}` };
      for (const path of ["/billing/charges", `/billing/events/${env.event_id}/summary`]) {
        const res = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}${path}`, { headers });
        expect(res.status(), `GET ${path} as ${role}`).toBe(403);
      }
    });
  }

  test("owner: a foreign event id is a 404 and a malformed filter a 400", async ({ request }) => {
    const headers = { Authorization: `Bearer ${person(env, "org_admin").access_token}` };
    const base = `${env.api_base_url}/v1/organizations/${env.org_id}/billing`;
    const foreign = "00000000-0000-4000-8000-00000000dead";
    expect((await request.get(`${base}/events/${foreign}/summary`, { headers })).status()).toBe(404);
    expect((await request.get(`${base}/charges?event_id=${foreign}`, { headers })).status()).toBe(404);
    expect((await request.get(`${base}/charges?limit=0`, { headers })).status()).toBe(400);
  });
});
