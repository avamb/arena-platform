/**
 * Twin test "Refund on the order card" (task 8-1) on the three roles of one
 * organization, against a REAL arena-api and the REAL refund engine. It walks
 * the scenario of the bot's e2e (httpserver/bot_e2e_refund_integration_test.go):
 * the card offers the refund and the options are the server's; nothing is
 * ticked at the start; a wrong word refunds nothing; the right word, then the
 * password again, returns the money and cancels only the chosen ticket; a
 * part of one ticket's amount; the provider's refusal; an order of a seller's
 * own site only gets an explanation; the people who may not do it.
 *
 * Stripe is the harness's stub (scripts/live-harness.mjs, STRIPE_API_BASE_URL):
 * it records every refund call, answers the same Idempotency-Key with the
 * first refund, and declines an amount of 7.77. Database rows are read
 * through the seed binary (`--refund-op inspect`), because the test process
 * has no SQL driver.
 *
 * Data: apps/backend/cmd/arena-e2e-seed/refund_fixture.go (fixed ids,
 * repeated below), put back before every test (`--refund-op reset`).
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();

// Fixed ids of cmd/arena-e2e-seed/refund_fixture.go.
const ORDER_OWNER = "fe0000e2-0000-7000-8000-000000008111"; // three tickets, the third scanned
const ORDER_MANAGER = "fe0000e2-0000-7000-8000-000000008112"; // one ticket
const ORDER_SITE = "fe0000e2-0000-7000-8000-000000008113"; // sold through a seller's own site
const TICKET_1 = "fe0000e2-0000-7000-8000-000000008121";
const TICKET_2 = "fe0000e2-0000-7000-8000-000000008122";
const TICKET_SCANNED = "fe0000e2-0000-7000-8000-000000008123";

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

interface OrderRows {
  refunds: number;
  states: string[];
  amounts: number[];
  active_tickets: number;
  status: string;
  notify_buyer: boolean[];
}

function seed(op: "reset" | "inspect" | "scan"): Record<string, OrderRows> {
  const r = spawnSync(seedBinary(), ["--refund-op", op], { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed --refund-op ${op} failed: ${r.stderr}`);
  return JSON.parse(r.stdout.trim()) as Record<string, OrderRows>;
}

const rows = (orderId: string): OrderRows => seed("inspect")[orderId];

interface StripeCall {
  amount: number;
  key: string;
  order: string | null;
}

async function stripeCalls(request: APIRequestContext): Promise<StripeCall[]> {
  const res = await request.get(`${env.stripe_stub_url}/__calls`);
  expect(res.status()).toBe(200);
  return (await res.json()) as StripeCall[];
}

const orgUrl = (path: string) => `${env.api_base_url}/v1/organizations/${env.org_id}${path}`;
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const createPath = (orderId: string) => `POST /v1/organizations/${env.org_id}/orders/${orderId}/refunds`;

async function permissionsOf(request: APIRequestContext, role: Role): Promise<string[]> {
  const res = await request.get(orgUrl("/me"), { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return ((await res.json()) as { permissions: string[] }).permissions;
}

function trackApi(page: Page): string[] {
  const calls: string[] = [];
  page.on("request", (req) => {
    if (req.url().startsWith(env.api_base_url)) calls.push(`${req.method()} ${new URL(req.url()).pathname}`);
  });
  return calls;
}

async function openCard(page: Page, role: Role, orderId: string) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/orders/${orderId}`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
}

async function typeWord(page: Page, text: string) {
  const dialog = page.getByTestId("confirm-word");
  await dialog.getByLabel("Type the word").fill(text);
  await page.getByTestId("confirm-word-submit").click();
}

async function confirmPassword(page: Page, role: Role) {
  const dialog = page.getByTestId("step-up");
  await expect(dialog).toBeVisible();
  await dialog.getByLabel("Password").fill(person(env, role).password);
  await dialog.getByRole("button", { name: "Confirm" }).click();
}

async function noHorizontalScroll(page: Page) {
  const width = page.viewportSize()?.width ?? 375;
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
}

const ticketBox = (page: Page, id: string) => page.locator(`[data-testid="refund-ticket"][data-ticket="${id}"]`);

test.beforeEach(async ({ request }) => {
  seed("reset");
  expect((await request.post(`${env.stripe_stub_url}/__reset`)).status()).toBe(200);
});

test.describe("refund an order (live)", () => {
  test("owner: nothing ticked, a wrong word refunds nothing, the word and the password return one ticket", async ({ page, request }) => {
    const permissions = await permissionsOf(request, "org_admin");
    for (const p of ["refund.create", "refund.read", "refund.approve"]) expect(permissions, p).toContain(p);
    expect(rows(ORDER_OWNER)).toMatchObject({ refunds: 0, active_tickets: 3, status: "paid" });

    const calls = trackApi(page);
    await openCard(page, "org_admin", ORDER_OWNER);
    await page.getByTestId("refund-open").click();
    const panel = page.getByTestId("refund-panel");
    await expect(panel).toBeVisible();
    await expect(page.getByTestId("refund-pick")).toContainText("Stripe");

    // The options are the server's: two tickets, the scanned one apart, none ticked.
    await expect(page.getByTestId("refund-ticket")).toHaveCount(2);
    await expect(ticketBox(page, TICKET_SCANNED)).toHaveCount(0);
    await expect(page.getByTestId("refund-scanned-note")).toBeVisible();
    for (const id of [TICKET_1, TICKET_2]) await expect(ticketBox(page, id)).not.toBeChecked();
    await expect(page.getByTestId("refund-letter")).toBeChecked();
    await noA11yViolations(page);
    await noHorizontalScroll(page);

    // Nothing ticked: the screen says so and sends nothing.
    await page.getByTestId("refund-continue").click();
    await expect(page.getByTestId("refund-problem")).toContainText("Select at least one ticket");
    // A ticket but no reason.
    await ticketBox(page, TICKET_1).check();
    await page.getByTestId("refund-continue").click();
    await expect(page.getByTestId("refund-problem")).toContainText("reason");

    await page.getByTestId("refund-reason-buyer").check();
    await page.getByTestId("refund-letter").uncheck();
    await page.getByTestId("refund-continue").click();

    // The final check states the amount, the ticket, the letter and the warning.
    const final = page.getByTestId("refund-final");
    await expect(final).toBeVisible();
    await expect(page.getByTestId("refund-final-ticket")).toHaveCount(1);
    await expect(page.getByTestId("refund-final-amount")).toContainText(/25[.,]00/);
    await expect(page.getByTestId("refund-final-letter")).toContainText("No letter");
    await expect(final).toContainText("stop being valid");
    await expect(page.getByTestId("refund-needs-approval")).toHaveCount(0);
    await noA11yViolations(page);

    // The word dialog: nothing, a wrong word and the other actions' words refund nothing.
    await page.getByTestId("refund-go").click();
    const dialog = page.getByTestId("confirm-word");
    await expect(dialog).toContainText("REFUND");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("confirm-word-note")).toBeVisible();
    for (const other of ["yes", "CANCEL ORDER", "DELETE", "ANNUL"]) {
      await typeWord(page, other);
      await expect(page.getByTestId("confirm-word-note")).toBeVisible();
    }
    expect(calls.filter((c) => c === createPath(ORDER_OWNER)), "no refund request was sent").toEqual([]);
    expect(calls.includes("POST /v1/auth/step-up"), "no password was asked for a wrong word").toBe(false);
    expect(await stripeCalls(request)).toEqual([]);
    expect(rows(ORDER_OWNER).refunds).toBe(0);

    // The refund word (any case), then the password: the money goes back.
    await typeWord(page, " refund. ");
    await confirmPassword(page, "org_admin");
    const result = page.getByTestId("refund-result");
    await expect(result).toBeVisible();
    await expect(page.getByTestId("refund-result-line")).toHaveCount(1);
    await expect(page.getByTestId("refund-result-line")).toHaveAttribute("data-state", "succeeded");
    await expect(page.getByTestId("confirm-word")).toHaveCount(0);
    await noA11yViolations(page);

    const after = rows(ORDER_OWNER);
    expect(after).toMatchObject({ refunds: 1, states: ["succeeded"], amounts: [2500], active_tickets: 2, status: "partially_refunded" });
    expect(after.notify_buyer).toEqual([false]);
    const sent = await stripeCalls(request);
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({ amount: 2500, order: ORDER_OWNER });
    expect(sent[0].key.length, "the provider got an idempotency key").toBeGreaterThan(0);
    expect(calls.filter((c) => c === createPath(ORDER_OWNER))).toHaveLength(1);

    // The card shows the new state; the refunded ticket is no longer offered.
    await page.getByTestId("refund-close").click();
    await expect(page.getByText("Partly refunded").first()).toBeVisible();
    await page.getByTestId("refund-open").click();
    await expect(page.getByTestId("refund-ticket")).toHaveCount(1);
    await expect(ticketBox(page, TICKET_1)).toHaveCount(0);
  });

  test("owner: closing the password question sends nothing", async ({ page, request }) => {
    const calls = trackApi(page);
    await openCard(page, "org_admin", ORDER_OWNER);
    await page.getByTestId("refund-open").click();
    await page.getByTestId("refund-select-all").click();
    await page.getByTestId("refund-reason-event").check();
    await page.getByTestId("refund-continue").click();
    await expect(page.getByTestId("refund-final-amount")).toContainText(/50[.,]00/);
    await page.getByTestId("refund-go").click();
    await typeWord(page, "REFUND");
    await expect(page.getByTestId("step-up")).toBeVisible();
    await page.getByTestId("step-up").getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByTestId("step-up")).toHaveCount(0);
    await expect(page.getByTestId("confirm-word")).toBeVisible();
    expect(calls.filter((c) => c === createPath(ORDER_OWNER))).toEqual([]);
    expect(await stripeCalls(request)).toEqual([]);
    expect(rows(ORDER_OWNER).refunds).toBe(0);
  });

  test("owner: a lost answer survives a reload and is checked with the same key, never refunding twice", async ({ page, request }) => {
    await openCard(page, "org_admin", ORDER_OWNER);
    // The first create reaches arena-api, but its answer never reaches the page.
    let dropped = 0;
    await page.route(`**/v1/organizations/${env.org_id}/orders/${ORDER_OWNER}/refunds`, async (route) => {
      if (dropped === 0) {
        dropped += 1;
        await route.fetch();
        await route.abort("connectionfailed");
        return;
      }
      await route.fallback();
    });
    await page.getByTestId("refund-open").click();
    await ticketBox(page, TICKET_2).check();
    await page.getByTestId("refund-reason-duplicate").check();
    await page.getByTestId("refund-continue").click();
    await page.getByTestId("refund-go").click();
    await typeWord(page, "REFUND");
    await confirmPassword(page, "org_admin");

    // Not "failed": the screen says the answer is missing and offers a check.
    await expect(page.getByTestId("refund-unknown")).toBeVisible();
    await expect(page.getByTestId("refund-result")).toHaveCount(0);
    expect(rows(ORDER_OWNER)).toMatchObject({ refunds: 1, states: ["succeeded"] });
    await noA11yViolations(page);

    // The page is reloaded: the tab kept the operation's key, so the card opens
    // on "Check the refund" again instead of offering a new refund.
    await page.reload();
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByTestId("refund-unknown")).toBeVisible();
    await expect(page.getByTestId("refund-pick")).toHaveCount(0);

    await page.getByTestId("refund-check").click();
    // The step-up token lived in memory only: after a reload the password is asked again.
    await confirmPassword(page, "org_admin");
    await expect(page.getByTestId("refund-result")).toBeVisible();
    await expect(page.getByTestId("refund-result")).toContainText("Nothing was refunded twice");
    await expect(page.getByTestId("refund-result-line")).toHaveAttribute("data-state", "succeeded");
    // One refund, one provider call: the check replayed the first operation.
    expect(rows(ORDER_OWNER)).toMatchObject({ refunds: 1, amounts: [2500], active_tickets: 2 });
    expect(await stripeCalls(request)).toHaveLength(1);

    // The final answer cleared the stored key: another reload offers nothing to check.
    await page.reload();
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByTestId("refund-open")).toBeVisible();
    await expect(page.getByTestId("refund-unknown")).toHaveCount(0);
  });

  test("manager: a ticket scanned while the screen is open is refused, and the owners are told", async ({ page, request }) => {
    await openCard(page, "organizer", ORDER_MANAGER);
    await page.getByTestId("refund-open").click();
    await page.getByTestId("refund-ticket").check();
    await page.getByTestId("refund-reason-buyer").check();
    await page.getByTestId("refund-continue").click();
    await expect(page.getByTestId("refund-final")).toBeVisible();

    // The ticket is scanned at the entrance now, after the options were read.
    seed("scan");
    await page.getByTestId("refund-go").click();
    await typeWord(page, "REFUND");
    await confirmPassword(page, "organizer");

    // The API refused it (refund.ticket_scanned): the text follows owners_notice.
    const scanned = page.getByTestId("refund-scanned");
    await expect(scanned).toBeVisible();
    await expect(scanned).toContainText("already scanned at the entrance");
    await expect(scanned).toContainText("owners got a notice");
    await expect(page.getByTestId("confirm-word")).toHaveCount(0);
    // The panel shows the latest options: nothing left that can be refunded.
    await expect(page.getByTestId("refund-explain")).toHaveAttribute("data-entry", "scanned_all");
    await noA11yViolations(page);

    expect(rows(ORDER_MANAGER)).toMatchObject({ refunds: 0, active_tickets: 1, status: "paid" });
    expect(await stripeCalls(request)).toEqual([]);
  });

  test("manager: the provider's refusal, then part of one ticket's amount", async ({ page, request }) => {
    const permissions = await permissionsOf(request, "organizer");
    for (const p of ["refund.create", "refund.read", "refund.approve"]) expect(permissions, p).toContain(p);

    await openCard(page, "organizer", ORDER_MANAGER);
    await page.getByTestId("refund-open").click();
    await expect(page.getByTestId("refund-select-all")).toHaveCount(0); // one ticket only
    await page.getByTestId("refund-ticket").check();
    await page.getByTestId("refund-partial").check();
    await page.getByTestId("refund-reason-other").check();
    await page.getByTestId("refund-reason-text").fill("Late start, part of the price back");

    // A part above the ticket's amount is refused on the screen.
    await page.getByTestId("refund-part").fill("30");
    await page.getByTestId("refund-continue").click();
    await expect(page.getByTestId("refund-problem")).toBeVisible();

    // 7.77: the stub provider declines it.
    await page.getByTestId("refund-part").fill("7,77");
    await page.getByTestId("refund-continue").click();
    await expect(page.getByTestId("refund-final-amount")).toContainText(/7[.,]77/);
    await page.getByTestId("refund-go").click();
    await typeWord(page, "REFUND");
    await confirmPassword(page, "organizer");
    await expect(page.getByTestId("refund-result-line")).toHaveAttribute("data-state", "failed");
    await expect(page.getByTestId("refund-result-line")).toContainText("not returned");
    expect(rows(ORDER_MANAGER)).toMatchObject({ refunds: 1, states: ["failed"], active_tickets: 1, status: "paid" });

    // The ticket is still refundable: return 10.00 of it.
    await page.getByTestId("refund-close").click();
    await page.getByTestId("refund-open").click();
    await page.getByTestId("refund-ticket").check();
    await page.getByTestId("refund-partial").check();
    await page.getByTestId("refund-part").fill("10");
    await page.getByTestId("refund-reason-buyer").check();
    await page.getByTestId("refund-continue").click();
    await expect(page.getByTestId("refund-final-amount")).toContainText(/10[.,]00/);
    await page.getByTestId("refund-go").click();
    await typeWord(page, "REFUND");
    // The password was confirmed a minute ago: the token still covers this request.
    await expect(page.getByTestId("refund-result-line")).toHaveAttribute("data-state", "succeeded");

    // A part of the price still ends the ticket (owner decision 13.10).
    expect(rows(ORDER_MANAGER)).toMatchObject({ refunds: 2, states: ["failed", "succeeded"], amounts: [777, 1000], active_tickets: 0 });
    const sent = await stripeCalls(request);
    expect(sent.map((c) => c.amount)).toEqual([777, 1000]);
    expect(sent[0].key, "a new operation has a new key").not.toBe(sent[1].key);
  });

  test("an order of the seller's own site only gets an explanation", async ({ page }) => {
    const calls = trackApi(page);
    await openCard(page, "org_admin", ORDER_SITE);
    await page.getByTestId("refund-open").click();
    const explain = page.getByTestId("refund-explain");
    await expect(explain).toHaveAttribute("data-entry", "site");
    await expect(explain).toContainText("seller's own site");
    await expect(page.getByTestId("refund-pick")).toHaveCount(0);
    expect(calls.filter((c) => c === createPath(ORDER_SITE))).toEqual([]);
    await noA11yViolations(page);
  });

  test("the API refuses a web money request without the password again", async ({ request }) => {
    // The harness signed in through the web channel, so the token says so.
    const res = await request.post(orgUrl(`/orders/${ORDER_OWNER}/refunds`), {
      headers: { ...auth("org_admin"), "Idempotency-Key": "web-live-no-step-up" },
      data: { items: [{ ticket_id: TICKET_1, amount: 2500 }], reason: "test" },
    });
    expect(res.status()).toBe(403);
    expect(((await res.json()) as { error: { code: string } }).error.code).toBe("auth.step_up_required");
    expect(rows(ORDER_OWNER).refunds).toBe(0);
    expect(await stripeCalls(request)).toEqual([]);
  });

  test("agent: no refund button, and the API refuses the agent", async ({ page, request }) => {
    const permissions = await permissionsOf(request, "agent");
    for (const p of ["refund.create", "refund.read", "refund.approve"]) expect(permissions, p).not.toContain(p);

    const options = await request.get(orgUrl(`/orders/${ORDER_OWNER}/refund-options`), { headers: auth("agent") });
    expect(options.status(), "agent GET refund-options").toBe(403);
    const create = await request.post(orgUrl(`/orders/${ORDER_OWNER}/refunds`), {
      headers: { ...auth("agent"), "Idempotency-Key": "web-live-agent" },
      data: { items: [{ ticket_id: TICKET_1, amount: 2500 }], reason: "test" },
    });
    expect(create.status(), "agent POST refunds").toBe(403);

    const calls = trackApi(page);
    await signInAs(page, person(env, "agent"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/orders/${ORDER_OWNER}`);
    // The agent may not read orders, so the card is not drawn and no refund button is.
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("refund-open")).toHaveCount(0);
    await expect(page.getByTestId("refund-panel")).toHaveCount(0);
    expect(calls.some((c) => c.includes("/refund-options") || c === createPath(ORDER_OWNER))).toBe(false);
    expect(rows(ORDER_OWNER).refunds).toBe(0);
    expect(await stripeCalls(request)).toEqual([]);
  });
});
