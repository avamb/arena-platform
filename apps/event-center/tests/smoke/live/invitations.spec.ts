/**
 * Twin test "invitations: issue, list with the state of the letter, annul"
 * (task 4-4), on the three roles of one organization, against a REAL
 * arena-api. It walks the scenario of the bot's invitations e2e
 * (bot_e2e_invite_integration_test.go): a bad guest line is named and nothing
 * is sent, one call per guest takes one place each, a category that runs out
 * half way keeps the guests issued before it and names the ones left, the list
 * shows the state of each guest's letter, annulling takes the typed word (a
 * wrong word annuls nothing) and returns the place, and a role that holds no
 * invitation rights sees no screen.
 *
 * The truth about each person comes from GET /v1/organizations/{org_id}/me.
 * The database rows are read through the seed binary's `--invitations-op
 * inspect`, because this process has no SQL driver; `reset` puts the date back
 * to six free places before every test.
 *
 * NOT covered here: a real letter. The harness runs arena-api only, no
 * arena-worker, so no invitation is mailed by itself. A fresh invitation shows
 * its letter as "queued" (the server read through the delivery job), and the
 * `deliver` / `fail` seed ops stand in for the mail relay and for Brevo's
 * "delivered" event so the list is proven to show the other states.
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

function seed(op: "reset" | "inspect" | "deliver" | "fail", email?: string): Record<string, number | string> {
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

/** Issues one guest straight through the API, as the web does (qty 1, a batch id per guest). */
async function apiIssue(request: APIRequestContext, role: Role, email: string, batch: string) {
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
  id: string;
  state: string;
  tickets: { holder_email: string | null }[];
  letters: { kind: string; status: string; ticket_ids: string[] }[];
}
async function apiList(request: APIRequestContext, role: Role): Promise<ListItem[]> {
  const res = await request.get(`${env.api_base_url}${orgPath}/complimentary?session_id=${SESSION_ID}`, { headers: auth(role) });
  expect(res.status(), "GET complimentary").toBe(200);
  return ((await res.json()) as { issuances: ListItem[] }).issuances;
}

function trackApi(page: Page): string[] {
  const calls: string[] = [];
  page.on("request", (req) => {
    if (req.url().startsWith(env.api_base_url)) calls.push(`${req.method()} ${new URL(req.url()).pathname}`);
  });
  return calls;
}
const issuePath = `POST ${orgPath}/complimentary`;

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

const row = (page: Page, email: string) => page.getByTestId("invitation-row").filter({ hasText: email });

async function typeWord(page: Page, text: string) {
  await page.getByTestId("confirm-word").getByLabel("Type the word").fill(text);
  await page.getByTestId("confirm-word-submit").click();
}

test.beforeEach(() => {
  seed("reset");
});

test.describe("invitations (live)", () => {
  test("owner: a bad line is named, two guests are issued one by one, the list shows each letter, annul takes the word", async ({
    page,
    request,
  }) => {
    const me = await orgMe(request, "org_admin");
    for (const p of ["complimentary.read", "complimentary.issue", "session.read", "tier.read"]) {
      expect(me.permissions.includes(p), `${p} per the server`).toBe(true);
    }
    expect(rows()).toEqual({ issuances: 0, revoked: 0, tickets: 0, free_places: PLACES });

    const calls = trackApi(page);
    await openScreen(page, "org_admin");
    await expect(page.getByTestId("invite-form")).toBeVisible();
    await expect(page.getByTestId("invitation-list")).toBeVisible();
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
    expect(rows().issuances).toBe(0);
    await page.getByTestId("invite-send").click();
    await expect(page.getByTestId("invite-done")).toBeVisible();

    // One call per guest, one place each.
    expect(calls.filter((c) => c === issuePath)).toHaveLength(2);
    expect(rows()).toEqual({ issuances: 2, revoked: 0, tickets: 2, free_places: PLACES - 2 });

    // The list: both guests, both letters queued (the harness runs no worker).
    const anna = row(page, "inv-anna@example.test");
    const bob = row(page, "inv-bob@example.test");
    await expect(anna).toHaveCount(1);
    await expect(bob).toHaveCount(1);
    await expect(anna).toHaveAttribute("data-letter", "queued");
    await expect(bob).toHaveAttribute("data-letter", "queued");
    await expect(anna).toContainText("Anna Ivanova");
    await noA11yViolations(page);

    // The relay takes Anna's letter and Brevo says delivered, Bob's job fails.
    seed("deliver", "inv-anna@example.test");
    seed("fail", "inv-bob@example.test");
    await page.reload();
    await expect(row(page, "inv-anna@example.test")).toHaveAttribute("data-letter", "delivered");
    await expect(row(page, "inv-bob@example.test")).toHaveAttribute("data-letter", "failed");
    await expect(row(page, "inv-bob@example.test").getByTestId("invitation-attention")).toBeVisible();
    await expect(row(page, "inv-anna@example.test").getByTestId("invitation-attention")).toHaveCount(0);
    // The API says the same: the web did not invent the status.
    const items = await apiList(request, "org_admin");
    const byMail = (mail: string) => items.find((i) => i.tickets[0]?.holder_email === mail);
    expect(byMail("inv-anna@example.test")?.letters.map((l) => [l.kind, l.status])).toEqual([["invitation", "delivered"]]);
    expect(byMail("inv-bob@example.test")?.letters.map((l) => [l.kind, l.status])).toEqual([["invitation", "failed"]]);

    // The filter by state.
    await page.getByTestId("invitation-filter-revoked").click();
    await expect(page.getByTestId("invitation-row")).toHaveCount(0);
    await page.getByTestId("invitation-filter-all").click();
    await expect(page.getByTestId("invitation-row")).toHaveCount(2);

    // Annul Bob: a wrong word and the words of other actions annul nothing.
    await row(page, "inv-bob@example.test").getByTestId("invitation-annul").click();
    const dialog = page.getByTestId("confirm-word");
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText("ANNUL");
    await expect(dialog).toContainText("Arena does not tell the guest");
    await noA11yViolations(page);
    for (const other of ["yes please", "CANCEL ORDER", "CANCEL SESSION", "REFUND", "DELETE"]) {
      await typeWord(page, other);
      await expect(page.getByTestId("confirm-word-note")).toBeVisible();
      await expect(dialog, `the dialog stays after "${other}"`).toBeVisible();
    }
    expect(rows().revoked).toBe(0);
    const revokes = () => calls.filter((c) => c.startsWith("POST /v1/complimentary/") && c.endsWith("/revoke"));
    expect(revokes(), "no revoke request was sent").toEqual([]);
    await typeWord(page, "  annul. ");
    await expect(page.getByTestId("invitation-annulled")).toBeVisible();
    await expect(page.getByTestId("confirm-word")).toHaveCount(0);
    expect(revokes()).toHaveLength(1);
    expect(rows()).toEqual({ issuances: 2, revoked: 1, tickets: 1, free_places: PLACES - 1 });
    await expect(row(page, "inv-bob@example.test")).toHaveAttribute("data-state", "revoked");
    await expect(row(page, "inv-bob@example.test").getByTestId("invitation-annul")).toHaveCount(0);
    await expect(row(page, "inv-anna@example.test").getByTestId("invitation-annul")).toBeVisible();
    // A valid-only filter now shows Anna alone.
    await page.getByTestId("invitation-filter-valid").click();
    await expect(page.getByTestId("invitation-row")).toHaveCount(1);
  });

  test("owner: the Russian word annuls too", async ({ page, request }) => {
    expect((await apiIssue(request, "org_admin", "inv-ru@example.test", "twin-ru-1")).status()).toBe(201);
    await openScreen(page, "org_admin");
    await page.getByLabel("Language").selectOption("ru");
    await row(page, "inv-ru@example.test").getByTestId("invitation-annul").click();
    await expect(page.getByTestId("confirm-word")).toContainText("АННУЛИРОВАТЬ");
    await typeWord2(page, "аннулировать");
    await expect(page.getByTestId("invitation-annulled")).toBeVisible();
    expect(rows()).toEqual({ issuances: 1, revoked: 1, tickets: 0, free_places: PLACES });
  });

  test("manager issues and annuls too (0129 grants complimentary.issue and read)", async ({ page, request }) => {
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
    expect(rows()).toEqual({ issuances: 1, revoked: 0, tickets: 1, free_places: PLACES - 1 });
    const r = row(page, "inv-manager@example.test");
    await expect(r).toHaveCount(1);
    await r.getByTestId("invitation-annul").click();
    await typeWord(page, "ANNUL");
    await expect(page.getByTestId("invitation-annulled")).toBeVisible();
    expect(rows()).toEqual({ issuances: 1, revoked: 1, tickets: 0, free_places: PLACES });
  });

  test("agent: the screen offers nothing the server refuses", async ({ page, request }) => {
    const me = await orgMe(request, "agent");
    expect(me.role).toBe("agent");
    const canRead = me.permissions.includes("complimentary.read");
    const canIssue = me.permissions.includes("complimentary.issue");
    // The API is the truth: whatever the agent lacks is refused.
    const list = await request.get(`${env.api_base_url}${orgPath}/complimentary`, { headers: auth("agent") });
    expect(list.status() === 200, "GET complimentary per the permissions").toBe(canRead);
    const issue = await apiIssue(request, "agent", "inv-agent@example.test", "twin-agent-1");
    expect(issue.status() === 201, "POST complimentary per the permissions").toBe(canIssue);
    expect(rows().issuances).toBe(canIssue ? 1 : 0);

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
    await expect(page.getByTestId("invitation-list")).toHaveCount(canRead ? 1 : 0);
    if (!canRead) {
      await expect(page.getByTestId("invitations-no-access")).toBeVisible();
      expect(calls.filter((c) => c === `GET ${orgPath}/complimentary`), "no list request without the right").toEqual([]);
    }
    await expect(page.getByTestId("invitation-annul")).toHaveCount(0);
    expect(calls.filter((c) => c === issuePath)).toEqual([]);
  });

  test("a retry of the same guest never takes a second place, and the answer carries the letters", async ({ request }) => {
    const first = await apiIssue(request, "org_admin", "inv-retry@example.test", "twin-retry-1");
    expect(first.status()).toBe(201);
    const body = (await first.json()) as { idempotent_replay: boolean; letters: unknown[] };
    expect(body.idempotent_replay).toBe(false);
    expect(Array.isArray(body.letters), "letters is an array on the issue answer").toBe(true);

    const again = await apiIssue(request, "org_admin", "inv-retry@example.test", "twin-retry-1");
    expect(again.status()).toBe(200);
    expect(((await again.json()) as { idempotent_replay: boolean }).idempotent_replay).toBe(true);
    expect(rows()).toEqual({ issuances: 1, revoked: 0, tickets: 1, free_places: PLACES - 1 });

    const items = await apiList(request, "org_admin");
    expect(items).toHaveLength(1);
    expect(items[0].letters.map((l) => [l.kind, l.status, l.ticket_ids.length])).toEqual([["invitation", "queued", 1]]);
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
    expect(rows().issuances).toBe(0);
  });

  test("a category that runs out half way keeps the guests issued and names the ones left", async ({ page, request }) => {
    await openScreen(page, "org_admin");
    await pickCategory(page); // the page now shows six free places
    // Five places go elsewhere while the form is open.
    for (let i = 0; i < PLACES - 1; i++) {
      expect((await apiIssue(request, "org_admin", `inv-other-${i}@example.test`, `twin-other-${i}`)).status()).toBe(201);
    }
    await page.getByTestId("invite-guests").fill("inv-first@example.test\ninv-second@example.test\ninv-third@example.test");
    await page.getByTestId("invite-review").click();
    await page.getByTestId("invite-send").click();

    const partial = page.getByTestId("invite-partial");
    await expect(partial).toBeVisible();
    await expect(partial).toContainText("Sent 1 of 3");
    await expect(page.getByTestId("invite-error")).toContainText("no free places");
    await expect(page.getByTestId("invite-left")).toContainText("inv-second@example.test");
    await expect(page.getByTestId("invite-left")).toContainText("inv-third@example.test");
    await expect(page.getByTestId("invite-left")).not.toContainText("inv-first@example.test");
    expect(rows()).toEqual({ issuances: PLACES, revoked: 0, tickets: PLACES, free_places: 0 });
    await expect(row(page, "inv-first@example.test")).toHaveCount(1);
    await expect(row(page, "inv-second@example.test")).toHaveCount(0);
    // "Send the rest" meets the same full category and sends nothing more.
    await page.getByTestId("invite-retry").click();
    await expect(page.getByTestId("invite-error")).toContainText("no free places");
    expect(rows().issuances).toBe(PLACES);
  });

  test("a stale annul (annulled elsewhere meanwhile) is shown honestly", async ({ page, request }) => {
    expect((await apiIssue(request, "org_admin", "inv-stale@example.test", "twin-stale-1")).status()).toBe(201);
    await openScreen(page, "org_admin");
    await expect(row(page, "inv-stale@example.test")).toHaveAttribute("data-state", "valid");
    // Somebody else annuls it while this page is open.
    const id = (await apiList(request, "org_admin"))[0].id;
    const rev = await request.post(`${env.api_base_url}/v1/complimentary/${id}/revoke`, { headers: auth("org_admin"), data: {} });
    expect(rev.status()).toBe(200);

    await row(page, "inv-stale@example.test").getByTestId("invitation-annul").click();
    await typeWord(page, "ANNUL");
    await expect(page.getByTestId("error-notice")).toContainText("already annulled");
    await expect(page.getByTestId("confirm-word")).toBeVisible();
    expect(rows()).toEqual({ issuances: 1, revoked: 1, tickets: 0, free_places: PLACES });
    // The list was reloaded behind the dialog and shows the truth.
    await page.getByTestId("confirm-word-cancel").click();
    await expect(row(page, "inv-stale@example.test")).toHaveAttribute("data-state", "revoked");
  });
});

/** The Russian dialog labels its field in Russian. */
async function typeWord2(page: Page, text: string) {
  const dialog = page.getByTestId("confirm-word");
  await dialog.getByRole("textbox").fill(text);
  await page.getByTestId("confirm-word-submit").click();
}
