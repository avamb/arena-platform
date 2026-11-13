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

    test(`${label} (${role}) sees the five sections of the event card, every block inside its section, with no overflow`, async ({
      page,
      request,
    }) => {
      const me = await orgMe(request, role);
      const can = (p: string) => me.permissions.includes(p);
      // What the server allowed, in the order of an event on sale. A section with nothing the person may use is not drawn.
      const expected: string[] = [];
      if (can("sales_link.manage")) expected.push("link");
      if (can("order.read") || can("report.read") || can("event.read")) expected.push("sales");
      expected.push("edit");
      if (can("promo.read") || can("complimentary.read") || can("sales_link.read")) expected.push("promo");
      if (can("event.publish") || can("event.delete")) expected.push("more");

      // The real defaults: only the first section is open.
      await signInAs(page, person(env, role), { sections: "default" });
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
      await page.getByTestId("event-row").first().getByRole("link", { name: env.event_name }).first().click();
      await expect(page.getByRole("heading", { level: 1, name: env.event_name })).toBeVisible();

      const sections = page.getByTestId("event-sections");
      await expect(sections).toHaveAttribute("data-order", expected.join(" "));
      for (const id of expected) {
        await expect(page.getByTestId(`event-section-toggle-${id}`)).toHaveAttribute("aria-expanded", id === expected[0] ? "true" : "false");
      }
      // At 375 px the closed headings are all in the first screen and only the first section is open.
      const tops = async () =>
        page.locator("[data-testid^='event-section-toggle-']").evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().bottom)));
      const height = page.viewportSize()?.height ?? 812;
      // Close the first one too: with ALL sections closed the five headings fit in one screen.
      await page.getByTestId(`event-section-toggle-${expected[0]}`).click();
      for (const id of expected) await expect(page.getByTestId(`event-section-toggle-${id}`)).toHaveAttribute("aria-expanded", "false");
      const closed = await tops();
      expect(closed.length).toBe(expected.length);
      expect(Math.max(...closed), "all headings of the closed sections fit in one screen").toBeLessThanOrEqual(height);
      // And the first section open again: the last heading is still within a reasonable height (two screens).
      await page.getByTestId(`event-section-toggle-${expected[0]}`).click();
      const onlyFirst = await tops();
      expect(Math.max(...onlyFirst), "headings with only the first section open").toBeLessThanOrEqual(height * 2);
      const width = page.viewportSize()?.width ?? 375;
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);

      // Open every section: each block sits inside its own section, and a block the server did not allow is absent.
      for (const id of expected) {
        if ((await page.getByTestId(`event-section-toggle-${id}`).getAttribute("aria-expanded")) !== "true") {
          await page.getByTestId(`event-section-toggle-${id}`).click();
        }
      }
      const inside = (section: string, testId: string) => page.getByTestId(`event-section-panel-${section}`).getByTestId(testId);
      await expect(inside("edit", "event-edit-link")).toHaveCount(can("event.update") ? 1 : 0);
      await expect(inside("edit", "event-poster")).toHaveCount(1);
      // The sessions list needs order.read or session.read (the agent has neither: it reads the explanation instead).
      await expect(inside("edit", "session-list")).toHaveCount(can("order.read") || can("session.read") ? 1 : 0);
      await expect(page.getByTestId("event-sales-switch")).toHaveCount(can("session.read") ? 1 : 0);
      await expect(inside("edit", "materials-section")).toHaveCount(1);
      await expect(inside("edit", "email-note")).toHaveCount(can("event.update") ? 1 : 0);
      await expect(page.getByTestId("event-qr-link")).toHaveCount(can("sales_link.manage") ? 1 : 0);
      if (expected.includes("link")) await expect(inside("link", "event-qr-link")).toHaveCount(1);
      await expect(page.getByTestId("event-funnel-link")).toHaveCount(can("report.read") ? 1 : 0);
      await expect(page.getByTestId("event-orders-link")).toHaveCount(can("order.read") ? 1 : 0);
      if (can("order.read")) await expect(inside("sales", "event-orders-link")).toHaveCount(1);
      await expect(page.getByTestId("event-promo-link")).toHaveCount(can("promo.read") ? 1 : 0);
      await expect(page.getByTestId("event-invitations-link")).toHaveCount(can("complimentary.read") ? 1 : 0);
      await expect(page.getByTestId("event-links-link")).toHaveCount(can("sales_link.read") ? 1 : 0);
      await expect(page.getByTestId("referral")).toHaveCount(can("promo.read") ? 1 : 0);
      if (can("promo.read")) await expect(inside("promo", "referral")).toHaveCount(1);
      // The status block of a published event is last, under "More" (only for a person who may move the event).
      if (can("event.publish") || can("event.delete")) {
        await expect(inside("more", "event-status")).toBeVisible();
        expect(expected.at(-1)).toBe("more");
      } else {
        await expect(page.getByTestId("event-status")).toHaveCount(0);
      }

      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
      await noA11yViolations(page);
    });

    test(`${label} (${role}): a section remembers its state, and a hash opens a closed section`, async ({ page }) => {
      await signInAs(page, person(env, role), { sections: "default" });
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
      await page.getByTestId("event-row").first().getByRole("link", { name: env.event_name }).first().click();
      await expect(page.getByRole("heading", { level: 1, name: env.event_name })).toBeVisible();

      // The edit section is closed on an event on sale: its blocks are not even in the page yet.
      const edit = page.getByTestId("event-section-toggle-edit");
      await expect(edit).toHaveAttribute("aria-expanded", "false");
      await expect(page.getByTestId("event-poster")).toHaveCount(0);
      await edit.click();
      await expect(edit).toHaveAttribute("aria-expanded", "true");
      await expect(page.getByTestId("event-poster")).toBeVisible();
      // Remembered on the device, per section: it is still open after a reload.
      await page.reload();
      await expect(page.getByTestId("event-section-toggle-edit")).toHaveAttribute("aria-expanded", "true");
      await page.getByTestId("event-section-toggle-edit").click();
      await page.reload();
      await expect(page.getByTestId("event-section-toggle-edit")).toHaveAttribute("aria-expanded", "false");

      // An address with a hash that points into the closed section opens it and brings the block into the view.
      await page.goto(`/o/${env.org_id}/events/${env.event_id}#event-item-materials`);
      await expect(page.getByTestId("event-section-toggle-edit")).toHaveAttribute("aria-expanded", "true");
      await expect(page.getByTestId("materials-section")).toBeInViewport();
    });
  }
});
