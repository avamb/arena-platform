/**
 * Twin test "Door scans" on the three roles of one organization, against a
 * REAL arena-api (task M2). It walks the scenario of the bot's e2e
 * (httpserver/bot_e2e_scans_integration_test.go): the order card lists how the
 * door treated each ticket (who reported it, which gate), and the date lists
 * the tickets that have not come in.
 *
 * The truth about each role comes from the API (GET /v1/organizations/{org_id}/me).
 * Owner and manager hold order.read; the agent does not, so the date shows no
 * ticket list and the API itself refuses the call.
 *
 * Data: apps/backend/cmd/arena-e2e-seed/scans_fixture.go (fixed ids, repeated
 * below). Ticket 1 was turned away by arena's scanner and then let in by MACS
 * (gate 2, scanner 7), ticket 2 was turned away and never let in, ticket 3 was
 * never scanned, ticket 4 is an invitation (no order).
 */
import { expect, test, type APIRequestContext } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();
const EVENT = "fe0000a2-0000-7000-8000-000000000002";
const SESSION = "fe0000a2-0000-7000-8000-000000000003";
const ORDER = "fe0000a2-0000-7000-8000-000000000006";
const TICKET_MACS = "fe0000a2-0000-7000-8000-0000000000a1";
const TICKET_DENIED = "fe0000a2-0000-7000-8000-0000000000a2";
const TICKET_NEVER = "fe0000a2-0000-7000-8000-0000000000a3";
const TICKET_INVITE = "fe0000a2-0000-7000-8000-0000000000a4";

const ROLES: { role: Role; label: string }[] = [
  { role: "org_admin", label: "owner" },
  { role: "organizer", label: "manager" },
  { role: "agent", label: "agent" },
];

const orgUrl = (path: string) => `${env.api_base_url}/v1/organizations/${env.org_id}${path}`;
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

async function permissionsOf(request: APIRequestContext, role: Role): Promise<string[]> {
  const res = await request.get(orgUrl("/me"), { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return ((await res.json()) as { permissions: string[] }).permissions;
}

interface ApiTicket {
  ticket_id: string;
  entered: boolean;
  order_id: string | null;
  last_scan: { source: string; result: string; gate: string; scanner_label: string } | null;
}

async function ticketsOf(request: APIRequestContext, role: Role, query: string) {
  const res = await request.get(orgUrl(`/sessions/${SESSION}/tickets${query}`), { headers: auth(role) });
  expect(res.status(), `GET session tickets ${query}`).toBe(200);
  return (await res.json()) as { total: number; items: ApiTicket[] };
}

test.describe("door scans on the three roles (live)", () => {
  for (const { role, label } of ROLES) {
    test(`${label} (${role}) sees the door's work only with order.read`, async ({ page, request }) => {
      const may = (await permissionsOf(request, role)).includes("order.read");
      await signInAs(page, person(env, role));
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));

      if (!may) {
        const direct = await request.get(orgUrl(`/sessions/${SESSION}/tickets`), { headers: auth(role) });
        expect(direct.status(), "the API refuses a caller without order.read").toBe(403);
        await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}`);
        await expect(page.getByTestId("session-tickets")).toHaveCount(0);
        await noA11yViolations(page);
        return;
      }

      // ── what the API says ──────────────────────────────────────────────
      const notEntered = await ticketsOf(request, role, "?entered=false");
      expect(notEntered.items.map((i) => i.ticket_id).sort()).toEqual([TICKET_DENIED, TICKET_NEVER, TICKET_INVITE].sort());
      const entered = await ticketsOf(request, role, "?entered=true");
      expect(entered.items.map((i) => i.ticket_id)).toEqual([TICKET_MACS]);
      expect(entered.items[0]?.last_scan).toMatchObject({ source: "macs", result: "admitted", gate: "2", scanner_label: "Scanner 7" });
      const all = await ticketsOf(request, role, "");
      expect(all.total).toBe(4);
      expect(all.items.find((i) => i.ticket_id === TICKET_INVITE)?.order_id).toBeNull();

      // ── the date: the list and its filter ──────────────────────────────
      await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}`);
      const box = page.getByTestId("session-tickets");
      await expect(box).toBeVisible();
      await expect(box.getByTestId("session-ticket")).toHaveCount(4);

      await box.getByTestId("session-tickets-filter-notEntered").click();
      await expect(box.getByTestId("session-ticket")).toHaveCount(3);
      await expect(box.locator('[data-entered="true"]')).toHaveCount(0);
      await expect(box.getByTestId("session-tickets-total")).toContainText("3");
      // The ticket the gate turned away says so, with who reported it and where.
      const denied = box.getByTestId("session-ticket").filter({ hasText: "Turned away" });
      await expect(denied).toHaveCount(1);
      await expect(denied).toContainText("Arena scanner · gate North");
      await expect(box.getByTestId("session-ticket").filter({ hasText: "Invitation" })).toHaveCount(1);

      await box.getByTestId("session-tickets-filter-entered").click();
      await expect(box.getByTestId("session-ticket")).toHaveCount(1);
      await expect(box.getByTestId("session-ticket")).toContainText("MACS · gate 2");
      await expect(box.getByTestId("session-ticket")).toContainText("Scans Buyer");
      await noA11yViolations(page);

      // ── the order card: the scans of each ticket ───────────────────────
      await page.goto(`/o/${env.org_id}/orders/${ORDER}`);
      await expect(page.getByRole("heading", { level: 1 })).toContainText("Order №");
      const rows = page.getByTestId("ticket-row");
      await expect(rows).toHaveCount(3);
      const withScans = page.getByTestId("ticket-scans");
      await expect(withScans).toHaveCount(2); // the never-scanned ticket draws none
      const scans = rows.first().getByTestId("ticket-scan");
      await expect(scans).toHaveCount(2);
      // Newest first: MACS let it in after arena's scanner turned it away.
      await expect(scans.nth(0)).toHaveAttribute("data-source", "macs");
      await expect(scans.nth(0)).toContainText("Entered");
      await expect(scans.nth(0)).toContainText("MACS · gate 2");
      await expect(scans.nth(1)).toHaveAttribute("data-source", "arena_scanner");
      await expect(scans.nth(1)).toContainText("Turned away");
      await expect(scans.nth(1)).toContainText("Arena scanner · gate North");
      await noA11yViolations(page);
      const width = page.viewportSize()?.width ?? 375;
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
    });
  }
});
