/**
 * Twin test "VIP mark of a ticket" (task V1) on the three roles of one
 * organization, against a REAL arena-api. It walks the scenario of the bot's
 * e2e (bot_e2e_vip_orders_integration_test.go and TestBotE2E_Invitations_VIPTick):
 * the card marks one ticket and takes the mark off, a repeated call changes
 * nothing, the other ticket is untouched, an invitation can be issued as VIP,
 * and the agent has no button and is refused by the API.
 *
 * The truth about each role comes from GET /v1/organizations/{org_id}/me (A1).
 * Owner and manager hold ticket.update; the agent holds no order.read either, so
 * the order card is not drawn for them.
 *
 * Data: the paid two-ticket order of apps/backend/cmd/arena-e2e-seed/vip_fixture.go
 * (fixed ids, repeated below) and the invitations fixture of invitations.spec.ts.
 * Every test puts the marks back through the API first, so reruns start equal.
 * NOT covered here: that the mark is absent from the PDF and the letters, which
 * the Go unit tests of pdf, templates and the presentation query prove.
 */
import { expect, test, type APIRequestContext } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();
const ORDER = "fe0000e2-0000-7000-8000-0000000001a1";
const TICKETS = ["fe0000e2-0000-7000-8000-0000000001b1", "fe0000e2-0000-7000-8000-0000000001b2"];

const EVENT_NAME = "E2E Invitations Show";

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

async function setVip(request: APIRequestContext, role: Role, ticket: string, vip: boolean) {
  return request.patch(orgUrl(`/tickets/${ticket}/vip`), { headers: auth(role), data: { vip } });
}

/** The marks of the order's tickets as the order card reports them. */
async function marks(request: APIRequestContext): Promise<Record<string, boolean>> {
  const res = await request.get(orgUrl(`/orders/${ORDER}`), { headers: auth("org_admin") });
  expect(res.status(), "GET order as owner").toBe(200);
  const body = (await res.json()) as { tickets: { id: string; vip: boolean }[] };
  return Object.fromEntries(body.tickets.map((t) => [t.id, t.vip]));
}

async function clearMarks(request: APIRequestContext) {
  for (const t of TICKETS) expect((await setVip(request, "org_admin", t, false)).status()).toBe(200);
}

const here = dirname(fileURLToPath(import.meta.url));
const appDir = resolve(here, "..", "..", "..");
const repoRoot = resolve(appDir, "..", "..");

function seedBinary(): string {
  const given = process.env.ARENA_E2E_SEED_BIN;
  if (given !== undefined && given !== "") return given;
  const built = join(appDir, ".live", process.platform === "win32" ? "arena-e2e-seed.exe" : "arena-e2e-seed");
  if (!existsSync(built)) throw new Error(`the seed binary ${built} is missing: run through the live harness`);
  return built;
}

function invitationsOp(op: "reset" | "issue") {
  const r = spawnSync(seedBinary(), ["--invitations-op", op], { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed --invitations-op ${op} failed: ${r.stderr}`);
}
const resetInvitations = () => invitationsOp("reset");
/** Mints the tickets of the staff invitations, as arena-worker's checkout.issue_tickets job would (the harness runs arena-api only). */
const issueTickets = () => invitationsOp("issue");

test.describe("VIP mark of a ticket on the three roles (live)", () => {
  for (const { role, label, may } of ROLES) {
    test(`${label} (${role}) ${may ? "marks one ticket and takes the mark off" : "has no button and is refused"}`, async ({ page, request }) => {
      await clearMarks(request);
      const permissions = await permissionsOf(request, role);
      expect(permissions.includes("ticket.update"), "ticket.update per the server").toBe(may);

      if (!may) {
        // The API itself refuses the agent: hiding the button is not the only guard.
        const direct = await setVip(request, role, TICKETS[0], true);
        expect(direct.status(), "agent PATCH vip").toBe(403);
        expect((await marks(request))[TICKETS[0]]).toBe(false);

        await signInAs(page, person(env, role));
        await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
        await page.goto(`/o/${env.org_id}/orders/${ORDER}`);
        await expect(page.getByTestId("ticket-vip-on")).toHaveCount(0);
        await expect(page.getByTestId("ticket-vip-off")).toHaveCount(0);
        return;
      }

      await signInAs(page, person(env, role));
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
      await page.goto(`/o/${env.org_id}/orders/${ORDER}`);
      await expect(page.getByTestId("ticket-row")).toHaveCount(2);
      await expect(page.getByTestId("ticket-vip-badge")).toHaveCount(0);

      // Mark the first ticket: the badge appears on it, the API agrees, the other ticket is untouched.
      const first = page.getByTestId("ticket-row").nth(0);
      await first.getByTestId("ticket-vip-on").click();
      await expect(first.getByTestId("ticket-vip-badge")).toBeVisible();
      await expect(first.getByTestId("ticket-vip-off")).toBeVisible();
      await expect(page.getByTestId("ticket-vip-badge")).toHaveCount(1);
      expect(await marks(request)).toEqual({ [TICKETS[0]]: true, [TICKETS[1]]: false });
      await noA11yViolations(page);

      // The same wanted value again changes nothing (the API answers changed=false).
      const again = await setVip(request, role, TICKETS[0], true);
      expect(again.status()).toBe(200);
      expect(((await again.json()) as { changed: boolean }).changed).toBe(false);

      // Take it off.
      await first.getByTestId("ticket-vip-off").click();
      await expect(page.getByTestId("ticket-vip-badge")).toHaveCount(0);
      await expect(first.getByTestId("ticket-vip-on")).toBeVisible();
      expect(await marks(request)).toEqual({ [TICKETS[0]]: false, [TICKETS[1]]: false });
    });
  }

  test("owner issues an invitation as VIP and the list marks it", async ({ page, request }) => {
    resetInvitations();
    const stamp = Date.now();
    const guest = `vip-guest-${stamp}@example.test`;
    const name = `VIP Guest ${stamp}`;
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/invitations`);
    await page.getByTestId("invite-event").selectOption({ label: EVENT_NAME });
    await page.getByTestId("invite-session").selectOption({ index: 1 });
    await page.getByTestId("invite-tier").selectOption({ index: 1 });
    await page.getByTestId("invite-guests").fill(`${name}, ${guest}`);
    await page.getByTestId("invite-review").click();
    await page.getByTestId("invite-vip").check();
    await page.getByTestId("invite-send").click();
    await expect(page.getByTestId("invite-done")).toBeVisible();

    // The staff-invitation route (spec 46) stores the mark on the order and puts it on every ticket when they are minted.
    issueTickets();
    const res = await request.get(orgUrl("/invitations?state=all&limit=50"), { headers: auth("org_admin") });
    expect(res.status()).toBe(200);
    const body = (await res.json()) as { items: { guest_name: string | null; places: { vip: boolean; ticket_status: string | null }[] }[] };
    const mine = body.items.find((i) => i.guest_name === name);
    expect(mine, "the invitation is listed").toBeDefined();
    expect(mine?.places.length ?? 0).toBeGreaterThan(0);
    expect(mine?.places.every((p) => p.vip && p.ticket_status === "active"), "every ticket of the invitation carries the mark").toBe(true);

    await page.reload();
    const row = page.getByTestId("org-inv-row").filter({ hasText: name });
    await expect(row.getByTestId("org-inv-vip").first()).toBeVisible();
  });
});
