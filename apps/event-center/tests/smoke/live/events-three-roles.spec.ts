/**
 * Twin test "the events list" on the three roles of one organization,
 * against a REAL arena-api (task 0-9; plan 8.2 rule of stages 4-5: every
 * screen is proven on owner, manager and agent).
 *
 * The truth about each role comes from the API itself
 * (GET /v1/organizations/{org_id}/me, A1); the screen must agree with it.
 * Owner and manager hold order.read: they see the event, its sales state and
 * the sales figures, and the Orders entry. The agent sees the event and its
 * state but no money and no Orders entry.
 */
import { expect, test, type APIRequestContext } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();

async function orgMe(request: APIRequestContext, role: Role) {
  const p = person(env, role);
  const res = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/me`, {
    headers: { Authorization: `Bearer ${p.access_token}` },
  });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

const ROLES: { role: Role; label: string; sees_money: boolean }[] = [
  { role: "org_admin", label: "owner", sees_money: true },
  { role: "organizer", label: "manager", sees_money: true },
  { role: "agent", label: "agent", sees_money: false },
];

test.describe("events list on the three roles (live)", () => {
  for (const { role, label, sees_money } of ROLES) {
    test(`${label} (${role}) sees the event and ${sees_money ? "the sales figures" : "no money"}`, async ({ page, request }) => {
      // What the server says this person may do in this organization.
      const me = await orgMe(request, role);
      expect(me.role).toBe(role);
      expect(me.permissions.includes("order.read"), "order.read per the server").toBe(sees_money);
      // The server itself refuses the agent what it does not hold: the screen
      // hiding the figures is not the only guard.
      const summary = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/events/${env.event_id}/summary`, {
        headers: { Authorization: `Bearer ${person(env, role).access_token}` },
      });
      expect(summary.status(), `GET event summary as ${role}`).toBe(sees_money ? 200 : 403);

      const apiCalls: string[] = [];
      page.on("request", (req) => {
        if (req.url().startsWith(env.api_base_url)) apiCalls.push(`${req.method()} ${new URL(req.url()).pathname}`);
      });

      await signInAs(page, person(env, role));
      // One organization: the chooser is skipped and the events open.
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
      await expect(page.getByTestId("org-name")).toHaveText(env.org_name);

      const row = page.getByTestId("event-row").first();
      await expect(row).toContainText(env.event_name);
      await expect(row).toContainText("On sale");

      const orders = page.getByRole("link", { name: "Orders" });
      if (sees_money) {
        await expect(row.getByTestId("sales-figures")).toContainText("Sold 0 of 30");
        await expect(orders).toBeVisible();
        expect(apiCalls.some((c) => c === `GET /v1/organizations/${env.org_id}/events/${env.event_id}/summary`)).toBe(true);
      } else {
        await expect(page.getByTestId("sales-figures")).toHaveCount(0);
        await expect(orders).toHaveCount(0);
        expect(apiCalls.some((c) => c.endsWith("/summary")), "the agent never asks for a summary").toBe(false);
      }
      // The screen asked the per-organization access route, never only /v1/me.
      expect(apiCalls).toContain(`GET /v1/organizations/${env.org_id}/me`);

      const width = page.viewportSize()?.width ?? 375;
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
      await noA11yViolations(page);
    });
  }
});
