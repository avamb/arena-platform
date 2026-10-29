/**
 * Twin test "invitations: issue, list, cancel" (task 4-4, moved to the
 * staff-invitation routes by INV-04b), on the three roles of one organization,
 * against a REAL arena-api. It walks the scenario of the bot's invitations e2e
 * (bot_e2e_invite_integration_test.go): a bad guest line is named and nothing
 * is sent, one call per guest takes one place each through
 * POST .../sessions/{id}/invitations, a category that runs out half way keeps
 * the guests issued before it and names the ones left, the organization list
 * (GET .../invitations) shows each order and cancels it, and a role that holds
 * no invitation rights sees no screen. The invitations of the OLD route stay in
 * the "Earlier invitations" section and are annulled there with the typed word.
 *
 * The truth about each person comes from GET /v1/organizations/{org_id}/me.
 * The database rows are read through the seed binary's `--invitations-op
 * inspect`, because this process has no SQL driver; `reset` puts the date back
 * to six free places before every test.
 *
 * NOT covered here: a real letter. The harness runs arena-api only, no
 * arena-worker, so no invitation is mailed by itself.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();

const EVENT_NAME = "E2E Invitations Show";
const SESSION_ID = "fe0000e2-0000-7000-8000-00000000d402";
const TIER_ID = "fe0000e2-0000-7000-8000-00000000d403";
const PLACES = 6;

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

function seed(op: "reset" | "inspect", email?: string): Record<string, number | string> {
  const args = ["--invitations-op", op];
  if (email !== undefined) args.push("--invitation-email", email);
  const r = spawnSync(seedBinary(), args, { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed ${args.join(" ")} failed: ${r.stderr}`);
  return JSON.parse(r.stdout.trim()) as Record<string, number | string>;
}

interface Rows {
  issuances: number;
  revoked: number;
  tickets: number;
  free_places: number;
}
const rows = () => seed("inspect") as unknown as Rows;

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/me`, {
    headers: { Authorization: `Bearer ${person(env, role).access_token}` },
  });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

const orgPath = `/v1/organizations/${env.org_id}`;
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

/** Issues one guest straight through the API on the staff-invitation route, as the web does (one place, a request id). */
async function apiIssue(request: APIRequestContext, role: Role, email: string, requestId: string) {
  return request.post(`${env.api_base_url}${orgPath}/sessions/${SESSION_ID}/invitations`, {
    headers: auth(role),
    data: {
      request_id: requestId,
      ga: [{ tier_id: TIER_ID, qty: 1 }],
      guest: { name: email, email, phone: null, locale: "en" },
      delivery: "guest_email",
    },
  });
}

/** The OLD route (complimentary_issuances), kept alive for the sites: its rows show under "Earlier invitations". */
async function apiIssueOld(request: APIRequestContext, role: Role, email: string, batch: string) {
  return request.post(`${env.api_base_url}${orgPath}/complimentary`, {
    headers: auth(role),
    data: {
      session_id: SESSION_ID,
      tier_id: TIER_ID,
      qty: 1,
      recipients: [email],
      recipient_names: ["API Guest"],
      batch_id: batch,
    },
  });
}

interface ListItem {
  order_id: string;
  guest_name: string | null;
  tickets_active: number;
  session: { id: string };
}
async function apiList(request: APIRequestContext, role: Role): Promise<ListItem[]> {
  const res = await request.get(`${env.api_base_url}${orgPath}/invitations?state=all`, { headers: auth(role) });
  expect(res.status(), "GET invitations").toBe(200);
  return ((await res.json()) as { items: ListItem[] }).items;
}

function trackApi(page: Page): string[] {
  const calls: string[] = [];
  page.on("request", (req) => {
    if (req.url().startsWith(env.api_base_url)) calls.push(`${req.method()} ${new URL(req.url()).pathname}`);
  });
  return calls;
}
const issuePath = `POST ${orgPath}/sessions/${SESSION_ID}/invitations`;

async function openScreen(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/invitations`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
}

/** Picks the fixture's event, its one date and its category. */
async function pickCategory(page: Page) {
  await page.getByTestId("invite-event").selectOption({ label: EVENT_NAME });
  const session = page.getByTestId("invite-session");
  await expect(session.locator("option")).toHaveCount(2);
  await session.selectOption({ index: 1 });
  const tier = page.getByTestId("invite-tier");
  await expect(tier.locator("option")).toHaveCount(2);
  await expect(tier.locator("option").nth(1)).toContainText(`${PLACES} free`);
  await tier.selectOption({ index: 1 });
}

/** A row of the organization list, found by the guest's name (an e-mail-only guest is listed under the address). */
const row = (page: Page, guest: string) => page.getByTestId("org-inv-row").filter({ hasText: guest });
/** A row of the "Earlier invitations" section (the old route), found by the guest's e-mail. */
const oldRow = (page: Page, email: string) => page.getByTestId("earlier-invitations").getByTestId("invitation-row").filter({ hasText: email });

async function typeWord(page: Page, text: string) {
  await page.getByTestId("confirm-word").getByLabel("Type the word").fill(text);
  await page.getByTestId("confirm-word-submit").click();
}

test.beforeEach(() => {
  seed("reset");
});

test.describe("invitations (live)", () => {
  test("owner: a bad line is named, two guests are issued one by one through the new route, listed, one is canceled", async ({
    page,
    request,
  }) => {
    const me = await orgMe(request, "org_admin");
    for (const p of ["complimentary.read", "complimentary.issue", "complimentary.revoke", "session.read", "tier.read"]) {
      expect(me.permissions.includes(p), `${p} per the server`).toBe(true);
    }
    expect(rows().free_places).toBe(PLACES);

    const calls = trackApi(page);
    await openScreen(page, "org_admin");
    await expect(page.getByTestId("invite-form")).toBeVisible();
    await expect(page.getByTestId("org-inv-list")).toBeVisible();
    await pickCategory(page);
    await noA11yViolations(page);

    // A line without an address is named, the second copy of an address too, and review stays off.
    const guests = page.getByTestId("invite-guests");
    await guests.fill("Anna Ivanova, inv-anna@example.test\nnot an address\nBob, INV-BOB@example.test\nBob again, inv-bob@example.test");
    const problems = page.getByTestId("invite-problems");
    await expect(problems).toContainText("Line 2");
    await expect(problems).toContainText("Line 4");
    await expect(page.getByTestId("invite-review")).toBeDisabled();
    expect(calls.filter((c) => c === issuePath), "nothing is sent for a bad list").toEqual([]);

    // Fixed: two guests.
    await guests.fill("Anna Ivanova, inv-anna@example.test\nBob, inv-bob@example.test");
    await expect(problems).toHaveCount(0);
    await expect(page.getByTestId("invite-count")).toContainText("2");
    await page.getByTestId("invite-review").click();
    await expect(page.getByTestId("invite-confirm")).toContainText("Invitations to send: 2");
    await expect(page.getByTestId("invite-confirm")).toContainText(EVENT_NAME);
    // Nothing went out before the send press.
    expect(rows().free_places).toBe(PLACES);
    await page.getByTestId("invite-send").click();
    await expect(page.getByTestId("invite-done")).toBeVisible();

    // One call per guest on the staff-invitation route, one place each, and the old route is not used.
    expect(calls.filter((c) => c === issuePath)).toHaveLength(2);
    expect(calls.filter((c) => c === `POST ${orgPath}/complimentary`)).toEqual([]);
    expect(rows().free_places).toBe(PLACES - 2);
    const api = await apiList(request, "org_admin");
    expect(api.map((i) => i.guest_name).sort()).toEqual(["Anna Ivanova", "Bob"]);

    // The organization list shows both, newest first.
    const anna = row(page, "Anna Ivanova");
    const bob = row(page, "Bob");
    await expect(anna).toHaveCount(1);
    await expect(bob).toHaveCount(1);
    await expect(anna).toContainText(EVENT_NAME);
    await noA11yViolations(page);

    // The state tabs: nothing is canceled yet.
    await page.getByTestId("org-inv-tab-cancelled").click();
    await expect(page.getByTestId("org-inv-row")).toHaveCount(0);
    await page.getByTestId("org-inv-tab-active").click();
    await expect(page.getByTestId("org-inv-row")).toHaveCount(2);

    // Cancel Bob: the guest is told by default, the place goes back on sale.
    await expect(bob).toHaveAttribute("data-state", "active", { timeout: 20_000 });
    await bob.getByTestId("org-inv-cancel").click();
    await expect(page.getByTestId("inv-cancel-notify")).toBeChecked();
    await noA11yViolations(page);
    await page.getByTestId("inv-cancel-dialog-submit").click();
    await expect(page.getByTestId("org-inv-cancelled")).toBeVisible();
    expect(rows().free_places).toBe(PLACES - 1);
    await expect(row(page, "Bob")).toHaveCount(0); // left the active tab
    await page.getByTestId("org-inv-tab-cancelled").click();
    await expect(row(page, "Bob")).toHaveAttribute("data-state", "cancelled");
    await expect(row(page, "Bob").getByTestId("org-inv-cancel")).toHaveCount(0);
  });

  test("earlier invitations (the old route) stay listed and the Russian word annuls them", async ({ page, request }) => {
    expect((await apiIssueOld(request, "org_admin", "inv-ru@example.test", "twin-ru-1")).status()).toBe(201);
    await openScreen(page, "org_admin");
    const old = page.getByTestId("earlier-invitations");
    await expect(old).toContainText("Earlier invitations");
    await expect(oldRow(page, "inv-ru@example.test")).toHaveCount(1);
    await page.getByLabel("Language").selectOption("ru");
    await oldRow(page, "inv-ru@example.test").getByTestId("invitation-annul").click();
    await expect(page.getByTestId("confirm-word")).toContainText("АННУЛИРОВАТЬ");
    await typeWord2(page, "аннулировать");
    await expect(page.getByTestId("invitation-annulled")).toBeVisible();
    expect(rows()).toMatchObject({ issuances: 1, revoked: 1, tickets: 0, free_places: PLACES });
  });

  test("earlier invitations: a wrong word annuls nothing, the right one does", async ({ page, request }) => {
    expect((await apiIssueOld(request, "org_admin", "inv-old@example.test", "twin-old-1")).status()).toBe(201);
    const calls = trackApi(page);
    await openScreen(page, "org_admin");
    await oldRow(page, "inv-old@example.test").getByTestId("invitation-annul").click();
    const dialog = page.getByTestId("confirm-word");
    for (const other of ["yes please", "CANCEL ORDER", "REFUND"]) {
      await typeWord(page, other);
      await expect(dialog, `the dialog stays after "${other}"`).toBeVisible();
    }
    const revokes = () => calls.filter((c) => c.startsWith("POST /v1/complimentary/") && c.endsWith("/revoke"));
    expect(revokes(), "no revoke request was sent").toEqual([]);
    expect(rows().revoked).toBe(0);
    await typeWord(page, "  annul. ");
    await expect(page.getByTestId("invitation-annulled")).toBeVisible();
    expect(revokes()).toHaveLength(1);
    expect(rows()).toMatchObject({ issuances: 1, revoked: 1, tickets: 0, free_places: PLACES });
    await expect(oldRow(page, "inv-old@example.test")).toHaveAttribute("data-state", "revoked");
  });

  test("manager issues and cancels too (0129 grants complimentary read, issue and revoke)", async ({ page, request }) => {
    const me = await orgMe(request, "organizer");
    for (const p of ["complimentary.read", "complimentary.issue", "session.read", "tier.read"]) {
      expect(me.permissions.includes(p), `${p} per the server`).toBe(true);
    }
    await openScreen(page, "organizer");
    await pickCategory(page);
    await page.getByTestId("invite-guests").fill("inv-manager@example.test");
    await page.getByTestId("invite-review").click();
    await page.getByTestId("invite-send").click();
    await expect(page.getByTestId("invite-done")).toBeVisible();
    expect(rows().free_places).toBe(PLACES - 1);
    // A guest typed as an e-mail only is listed under the address.
    const r = row(page, "inv-manager@example.test");
    await expect(r).toHaveCount(1);
    if (me.permissions.includes("complimentary.revoke")) {
      await expect(r).toHaveAttribute("data-state", "active", { timeout: 20_000 });
      await r.getByTestId("org-inv-cancel").click();
      await page.getByTestId("inv-cancel-dialog-submit").click();
      await expect(page.getByTestId("org-inv-cancelled")).toBeVisible();
      expect(rows().free_places).toBe(PLACES);
    }
  });

  test("agent: the screen offers nothing the server refuses", async ({ page, request }) => {
    const me = await orgMe(request, "agent");
    expect(me.role).toBe("agent");
    const canRead = me.permissions.includes("complimentary.read");
    const canIssue = me.permissions.includes("complimentary.issue");
    // The API is the truth: whatever the agent lacks is refused.
    const list = await request.get(`${env.api_base_url}${orgPath}/invitations`, { headers: auth("agent") });
    expect(list.status() === 200, "GET invitations per the permissions").toBe(canRead);
    const issue = await apiIssue(request, "agent", "inv-agent@example.test", "11111111-1111-4111-8111-111111111111");
    expect(issue.status() === 201, "POST invitations per the permissions").toBe(canIssue);
    expect(rows().free_places).toBe(canIssue ? PLACES - 1 : PLACES);

    const calls = trackApi(page);
    await signInAs(page, person(env, "agent"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    // Inside the main menu only: the event "E2E Invitations Show" is a link too.
    await expect(
      page.getByRole("navigation", { name: "Main menu" }).getByRole("link", { name: "Invitations", exact: true }),
    ).toHaveCount(canRead ? 1 : 0);
    await page.goto(`/o/${env.org_id}/invitations`);
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByTestId("invite-form")).toHaveCount(canIssue ? 1 : 0);
    await expect(page.getByTestId("org-inv-list")).toHaveCount(canRead ? 1 : 0);
    if (!canRead) {
      await expect(page.getByTestId("invitations-no-access")).toBeVisible();
      expect(calls.filter((c) => c === `GET ${orgPath}/invitations`), "no list request without the right").toEqual([]);
    }
    await expect(page.getByTestId("org-inv-cancel")).toHaveCount(0);
    expect(calls.filter((c) => c === issuePath)).toEqual([]);
  });

  test("a retry of the same request never takes a second place, and the old route refuses a seated date", async ({ request }) => {
    const id = "22222222-2222-4222-8222-222222222222";
    const first = await apiIssue(request, "org_admin", "inv-retry@example.test", id);
    expect(first.status()).toBe(201);
    expect(((await first.json()) as { replayed: boolean }).replayed).toBe(false);

    const again = await apiIssue(request, "org_admin", "inv-retry@example.test", id);
    expect(again.status()).toBe(200);
    expect(((await again.json()) as { replayed: boolean }).replayed).toBe(true);
    expect(rows().free_places).toBe(PLACES - 1);

    const items = await apiList(request, "org_admin");
    expect(items).toHaveLength(1);
    expect(items[0].session.id).toBe(SESSION_ID);
  });

  test("more guests than free places is refused before anything is sent, and 51 guests are too many", async ({ page }) => {
    const calls = trackApi(page);
    await openScreen(page, "org_admin");
    await pickCategory(page);
    const many = (n: number) => Array.from({ length: n }, (_, i) => `inv-many-${i}@example.test`).join("\n");
    await page.getByTestId("invite-guests").fill(many(PLACES + 1));
    await expect(page.getByTestId("invite-block")).toContainText(`free places (${PLACES})`);
    await expect(page.getByTestId("invite-review")).toBeDisabled();
    await page.getByTestId("invite-guests").fill(many(51));
    await expect(page.getByTestId("invite-block")).toContainText("more than 50");
    await expect(page.getByTestId("invite-review")).toBeDisabled();
    expect(calls.filter((c) => c === issuePath)).toEqual([]);
    expect(rows().free_places).toBe(PLACES);
  });

  test("a category that runs out half way keeps the guests issued and names the ones left", async ({ page, request }) => {
    await openScreen(page, "org_admin");
    await pickCategory(page); // the page now shows six free places
    // Five places go elsewhere while the form is open.
    for (let i = 0; i < PLACES - 1; i++) {
      const id = `33333333-3333-4333-8333-33333333330${i}`;
      expect((await apiIssue(request, "org_admin", `inv-other-${i}@example.test`, id)).status()).toBe(201);
    }
    await page.getByTestId("invite-guests").fill("inv-first@example.test\ninv-second@example.test\ninv-third@example.test");
    await page.getByTestId("invite-review").click();
    await page.getByTestId("invite-send").click();

    const partial = page.getByTestId("invite-partial");
    await expect(partial).toBeVisible();
    await expect(partial).toContainText("Sent 1 of 3");
    await expect(page.getByTestId("invite-error")).toContainText("not enough places");
    await expect(page.getByTestId("invite-left")).toContainText("inv-second@example.test");
    await expect(page.getByTestId("invite-left")).toContainText("inv-third@example.test");
    await expect(page.getByTestId("invite-left")).not.toContainText("inv-first@example.test");
    expect(rows().free_places).toBe(0);
    await expect(row(page, "inv-first@example.test")).toHaveCount(1);
    await expect(row(page, "inv-second@example.test")).toHaveCount(0);
    // "Send the rest" meets the same full category and sends nothing more.
    await page.getByTestId("invite-retry").click();
    await expect(page.getByTestId("invite-error")).toContainText("not enough places");
    expect(rows().free_places).toBe(0);
  });
});

/** The Russian dialog labels its field in Russian. */
async function typeWord2(page: Page, text: string) {
  const dialog = page.getByTestId("confirm-word");
  await dialog.getByRole("textbox").fill(text);
  await page.getByTestId("confirm-word-submit").click();
}
