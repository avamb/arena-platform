/**
 * Twin test "Resend tickets" on the three roles of one organization, against
 * a REAL arena-api (task 4-1). It walks the scenario of the bot's e2e
 * (httpserver/bot_e2e_resend_integration_test.go): choose the address, confirm,
 * the answer; a one-time address; the seller-site refusal; and the people who
 * may not do it.
 *
 * The truth about each role comes from the API
 * (GET /v1/organizations/{org_id}/me, A1). Owner and manager hold ticket.update
 * and order.read; the agent holds neither: the button is absent, the order card
 * is not even readable, and the API itself answers 403 to the resend call.
 *
 * It asserts through the API only (the harness has no database access): the
 * delivery[] recipient and state, the order's buyer_email and the 409 of a
 * seller site's order. The worker_jobs and order_events rows are proven by the
 * Go e2e bot_e2e_resend_integration_test.go.
 *
 * Data: the paid orders of apps/backend/cmd/arena-e2e-seed/resend_order_fixture.go
 * (fixed ids, repeated below). The harness runs no worker, so a resend leaves the
 * ticket's delivery job pending: the card says "in the queue".
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();
const ORDER = "fe0000e2-0000-7000-8000-000000000111"; // the platform's own sales page
const SITE_ORDER = "fe0000e2-0000-7000-8000-000000000112"; // sold through a seller's own site
const BUYER_EMAIL = "resend-buyer@example.test";
const OTHER_EMAIL = "other-person@example.test";

const ROLES: { role: Role; label: string; may: boolean }[] = [
  { role: "org_admin", label: "owner", may: true },
  { role: "organizer", label: "manager", may: true },
  { role: "agent", label: "agent", may: false },
];

const orgUrl = (path: string) => `${env.api_base_url}/v1/organizations/${env.org_id}${path}`;
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

async function permissionsOf(request: APIRequestContext, role: Role): Promise<string[]> {
  const res = await request.get(orgUrl("/me"), { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return ((await res.json()) as { permissions: string[] }).permissions;
}

/** The recipients of the delivery jobs the order card reports. */
async function deliveryRecipients(request: APIRequestContext, orderId: string): Promise<string[]> {
  const res = await request.get(orgUrl(`/orders/${orderId}`), { headers: auth("org_admin") });
  expect(res.status(), "GET order as owner").toBe(200);
  const body = (await res.json()) as { delivery: { recipient_email?: string | null }[] };
  return body.delivery.map((d) => d.recipient_email ?? "");
}

function trackCalls(page: Page): string[] {
  const calls: string[] = [];
  page.on("request", (req) => {
    if (req.url().startsWith(env.api_base_url)) calls.push(`${req.method()} ${new URL(req.url()).pathname}`);
  });
  return calls;
}

async function noHorizontalScroll(page: Page) {
  const width = page.viewportSize()?.width ?? 375;
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
}

test.describe("resend tickets on the three roles (live)", () => {
  for (const { role, label, may } of ROLES) {
    test(`${label} (${role}) ${may ? "resends to the order's address and to another one" : "has no button and no access"}`, async ({ page, request }) => {
      // What the server says this person may do here.
      const permissions = await permissionsOf(request, role);
      expect(permissions.includes("ticket.update"), "ticket.update per the server").toBe(may);
      const resendPath = `/v1/organizations/${env.org_id}/orders/${ORDER}/resend-tickets`;
      const calls = trackCalls(page);

      if (!may) {
        // The server refuses the agent: hiding the button is not the only guard.
        const direct = await request.post(orgUrl(`/orders/${ORDER}/resend-tickets`), { headers: auth(role), data: {} });
        expect(direct.status(), "agent POST resend-tickets").toBe(403);

        await signInAs(page, person(env, role));
        await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
        await page.goto(`/o/${env.org_id}/orders/${ORDER}`);
        // The agent may not read orders, so the card is not drawn and no button is.
        await expect(page.getByTestId("resend-open")).toHaveCount(0);
        await expect(page.getByTestId("resend-retry-open")).toHaveCount(0);
        await expect(page.getByTestId("order-letters")).toHaveCount(0);
        expect(calls.includes(`POST ${resendPath}`), "the agent's browser never sends a resend").toBe(false);
        await noA11yViolations(page);
        return;
      }

      await signInAs(page, person(env, role));
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
      await page.goto(`/o/${env.org_id}/orders/${ORDER}`);
      await expect(page.getByRole("heading", { level: 1 })).toContainText("Order №");

      // The letters block: a status line and the button. An earlier test may have
      // queued a resend already, so both "sent" and "queued" are fine here.
      const state = page.getByTestId("delivery-state");
      await expect(state).toHaveAttribute("data-letter-state", /^(sent|queued)$/);
      await noHorizontalScroll(page);

      // 1. To the order's own address.
      await page.getByTestId("resend-open").click();
      const dialog = page.getByTestId("resend-dialog");
      await expect(dialog).toBeVisible();
      await expect(page.getByTestId("resend-choose")).toContainText("Tickets to send: 1");
      await noA11yViolations(page);
      await page.getByTestId("resend-same").click();
      // Confirmation: nothing is queued before it.
      await expect(page.getByTestId("resend-confirm-text")).toContainText("(the order's address)");
      expect(calls.includes(`POST ${resendPath}`), "nothing sent before the confirmation").toBe(false);
      await page.getByTestId("resend-send").click();
      await expect(page.getByTestId("resend-done")).toContainText("on their way");
      await expect(page.getByTestId("resend-done")).not.toContainText("We used this address once");
      await page.getByTestId("resend-close").click();
      await expect(dialog).toHaveCount(0);
      // The card read the order again and shows the letter in the queue.
      await expect(state).toHaveAttribute("data-letter-state", "queued");
      expect(await deliveryRecipients(request, ORDER)).toEqual([BUYER_EMAIL]);

      // 2. To another address: a bad one is stopped before the server, a good one goes once.
      await page.getByTestId("resend-open").click();
      await page.getByTestId("resend-other").click();
      await page.getByTestId("resend-email").fill("not-an-email");
      await page.getByTestId("resend-continue").click();
      await expect(page.getByTestId("resend-email-note")).toContainText("does not look like an e-mail address");
      await page.getByTestId("resend-email").fill(OTHER_EMAIL);
      await page.getByTestId("resend-continue").click();
      await expect(page.getByTestId("resend-confirm-text")).toContainText(OTHER_EMAIL);
      await expect(page.getByTestId("resend-confirm-text")).toContainText("One-time address");
      await page.getByTestId("resend-send").click();
      await expect(page.getByTestId("resend-done")).toContainText("We used this address once");
      await page.getByTestId("resend-close").click();
      // The typed address lives on the delivery job only: the order keeps its own.
      expect(await deliveryRecipients(request, ORDER)).toEqual([OTHER_EMAIL]);
      const order = await request.get(orgUrl(`/orders/${ORDER}`), { headers: auth("org_admin") });
      expect(((await order.json()) as { buyer_email: string }).buyer_email).toBe(BUYER_EMAIL);
      expect(calls.filter((c) => c === `POST ${resendPath}`).length).toBe(2);

      // 3. A seller site's order: refused by the API, said honestly, nothing queued.
      const siteDirect = await request.post(orgUrl(`/orders/${SITE_ORDER}/resend-tickets`), { headers: auth(role), data: {} });
      expect(siteDirect.status()).toBe(409);
      expect(((await siteDirect.json()) as { error: { code: string } }).error.code).toBe("order.seller_site_order");
      await page.goto(`/o/${env.org_id}/orders/${SITE_ORDER}`);
      await page.getByTestId("resend-open").click();
      await page.getByTestId("resend-same").click();
      await page.getByTestId("resend-send").click();
      await expect(page.getByTestId("resend-refused-site")).toContainText("sold through the seller's own site");
      await expect(page.getByTestId("resend-refused-site")).toContainText("Nothing was queued");
      await noA11yViolations(page);
      await page.getByTestId("resend-close").click();
      // The site order's letter state is as it was: sent, nothing in the queue.
      await expect(page.getByTestId("delivery-state")).toHaveAttribute("data-letter-state", "sent");
    });
  }
});
