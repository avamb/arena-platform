/**
 * Twin test "Replace tickets" (spec 63, stage R-3) on the three roles of one
 * organization, against a REAL arena-api. It walks the scenario of the bot's
 * reissue dialog: the case question (from the order's address leads to the
 * resend, the barcodes stay), the address, the reason, the check screen, the
 * typed word, the answer; a wrong word changes nothing; a lost answer is
 * checked with the SAME Idempotency-Key and replays; a reissue waiting for its
 * letter offers "Send the letter again"; a seller site's order is explained;
 * the agent has no button and the API refuses it.
 *
 * Data: a FRESH paid order per test (`--reissue-op new`, two tickets, the
 * second already used at the door, paid through AllPay so the check screen
 * says the payment system keeps the old address). A reissue cannot be undone
 * and its journal is never deleted, so nothing is reset. The harness runs no
 * worker: the letter stays in the queue, the reissue stays `pending` and the
 * order keeps its old address, exactly as the API says before the letter leaves.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();
const SITE_ORDER = "fe0000e2-0000-7000-8000-000000009111"; // reissue_fixture.go
const RIGHT_EMAIL = "reissue-buyer@example.test";

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

function seed<T>(args: string[]): T {
  const r = spawnSync(seedBinary(), args, { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed ${args.join(" ")} failed: ${r.stderr}`);
  return JSON.parse(r.stdout.trim()) as T;
}

interface FreshOrder {
  order_id: string;
  number: number;
  email: string;
  tickets: { number: number; status: string }[];
}

interface Inspect {
  buyer_email: string;
  reissues: { state: string; new_email: string; reason: string; note: string; items: number }[];
  tickets: { number: number; status: string }[];
}

const freshOrder = (): FreshOrder => seed<FreshOrder>(["--reissue-op", "new"]);
const inspect = (orderId: string): Inspect => seed<Inspect>(["--reissue-op", "inspect", "--order-id", orderId]);

const orgUrl = (path: string) => `${env.api_base_url}/v1/organizations/${env.org_id}${path}`;
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

async function permissionsOf(request: APIRequestContext, role: Role): Promise<string[]> {
  const res = await request.get(orgUrl("/me"), { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return ((await res.json()) as { permissions: string[] }).permissions;
}

function trackCalls(page: Page): string[] {
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
  await expect(page.getByRole("heading", { level: 1 })).toContainText("Order №");
}

/** From the card to the check screen, typing `email` and choosing the typo reason. */
async function walkToCheck(page: Page, email: string, note = "") {
  await page.getByTestId("reissue-open").click();
  await expect(page.getByTestId("reissue-case")).toBeVisible();
  await page.getByTestId("reissue-from-other").click();
  await page.getByTestId("reissue-email").fill(email);
  await page.getByTestId("reissue-continue").click();
  await page.getByTestId("reissue-reason-sent_to_wrong_address").check();
  if (note !== "") await page.getByTestId("reissue-note").fill(note);
  await page.getByTestId("reissue-continue").click();
  await expect(page.getByTestId("reissue-check")).toBeVisible();
}

async function typeWord(page: Page, word: string) {
  const dialog = page.getByTestId("confirm-word");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("textbox").fill(word);
  await page.getByTestId("confirm-word-submit").click();
}

const ROLES: { role: Role; label: string; may: boolean }[] = [
  { role: "org_admin", label: "owner", may: true },
  { role: "organizer", label: "manager", may: true },
  { role: "agent", label: "agent", may: false },
];

test.describe("replace tickets on the three roles (live)", () => {
  for (const { role, label, may } of ROLES) {
    test(`${label} (${role}) ${may ? "replaces every ticket of a mistyped order" : "has no button and no access"}`, async ({ page, request }) => {
      const order = freshOrder();
      const permissions = await permissionsOf(request, role);
      expect(permissions.includes("ticket.update"), "ticket.update per the server").toBe(may);
      const postPath = `/v1/organizations/${env.org_id}/orders/${order.order_id}/reissue-tickets`;
      const calls = trackCalls(page);

      if (!may) {
        const direct = await request.post(orgUrl(`/orders/${order.order_id}/reissue-tickets`), {
          headers: { ...auth(role), "Idempotency-Key": "web-agent-try" },
          data: { email: RIGHT_EMAIL, reason: "other" },
        });
        expect(direct.status(), "agent POST reissue-tickets").toBe(403);
        await signInAs(page, person(env, role));
        await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
        await page.goto(`/o/${env.org_id}/orders/${order.order_id}`);
        await expect(page.getByTestId("reissue-open")).toHaveCount(0);
        expect(calls.includes(`POST ${postPath}`), "the agent's browser never sends a reissue").toBe(false);
        expect(inspect(order.order_id).reissues).toEqual([]);
        await noA11yViolations(page);
        return;
      }

      await openCard(page, role, order.order_id);
      await expect(page.getByTestId("order-reissues")).toHaveCount(0);

      // 0. "From the order's address" leads into the resend: same barcodes, nothing replaced.
      await page.getByTestId("reissue-open").click();
      await expect(page.getByTestId("reissue-case")).toContainText("From which address does the buyer write?");
      await noA11yViolations(page);
      await page.getByTestId("reissue-from-order").click();
      await expect(page.getByTestId("reissue-dialog")).toHaveCount(0);
      await expect(page.getByTestId("resend-dialog")).toBeVisible();
      await page.getByTestId("resend-cancel").click();
      expect(inspect(order.order_id).reissues).toEqual([]);

      // 1-3. Another address: a bad one is stopped before the server, then the check screen.
      await page.getByTestId("reissue-open").click();
      await page.getByTestId("reissue-from-other").click();
      await expect(page.getByTestId("reissue-compare")).toContainText("Reissue Buyer");
      await expect(page.getByTestId("reissue-compare")).toContainText("+34600111222");
      await page.getByTestId("reissue-email").fill("not-an-email");
      await page.getByTestId("reissue-continue").click();
      await expect(page.getByTestId("reissue-email-note")).toContainText("This is not an e-mail address");
      await page.getByTestId("reissue-email").fill(RIGHT_EMAIL);
      await page.getByTestId("reissue-continue").click();
      // O-4: the typo reason is chosen at the start, and the person may change it.
      await expect(page.getByTestId("reissue-reason-sent_to_wrong_address")).toBeChecked();
      await page.getByTestId("reissue-reason-other").check();
      await page.getByTestId("reissue-reason-sent_to_wrong_address").check();
      await page.getByTestId("reissue-note").fill(`Checked the name by phone (${label})`);
      await page.getByTestId("reissue-continue").click();
      await expect(page.getByTestId("reissue-check-tickets")).toContainText(`order №${order.number} to replace: 2`);
      await expect(page.getByTestId("reissue-check-to")).toContainText(RIGHT_EMAIL);
      await expect(page.getByTestId("reissue-check-scanned")).toContainText(`The scanner read ticket №${order.tickets[1].number} at the entrance`);
      await expect(page.getByTestId("reissue-check-provider")).toContainText("AllPay");
      await noA11yViolations(page);
      expect(calls.includes(`POST ${postPath}`), "nothing sent before the word").toBe(false);

      // 4. A wrong word changes nothing.
      await page.getByTestId("reissue-go").click();
      await typeWord(page, "REPLACE");
      await expect(page.getByTestId("confirm-word-note")).toContainText("The text is different from the word");
      expect(calls.includes(`POST ${postPath}`), "a wrong word sends nothing").toBe(false);
      expect(inspect(order.order_id).reissues).toEqual([]);

      // 5. The word: the tickets are replaced.
      await typeWord(page, "REISSUE TICKETS");
      await expect(page.getByTestId("reissue-done")).toContainText("We replaced the tickets");
      const pairs = page.getByTestId("reissue-done-pairs");
      for (const tk of order.tickets) await expect(pairs).toContainText(`№${tk.number} →`);
      expect(calls.filter((c) => c === `POST ${postPath}`).length).toBe(1);
      await page.getByTestId("reissue-close").click();

      // The API state: old tickets transferred, two new active ones, the journal pending,
      // and the order keeps its old address until the letter leaves (no worker here).
      const after = inspect(order.order_id);
      expect(after.reissues).toEqual([
        { state: "pending", new_email: RIGHT_EMAIL, reason: "sent_to_wrong_address", note: `Checked the name by phone (${label})`, items: 2 },
      ]);
      const old = new Set(order.tickets.map((t) => t.number));
      expect(after.tickets.filter((t) => old.has(t.number)).map((t) => t.status)).toEqual(["transferred", "transferred"]);
      const fresh = after.tickets.filter((t) => !old.has(t.number));
      expect(fresh.map((t) => t.status)).toEqual(["active", "active"]);
      expect(after.buyer_email).toBe(order.email);

      // The card shows the history; the old tickets are no longer valid.
      const entry = page.getByTestId("reissue-entry");
      await expect(entry).toHaveCount(1);
      await expect(entry).toHaveAttribute("data-state", "pending");
      await expect(page.getByTestId("reissue-entry-tickets")).toContainText(`№${order.tickets[0].number} → №`);
      // O-5: the code used at the door before the replacement is named on the entry.
      await expect(page.getByTestId("reissue-entry-old-before")).toContainText(`The scanner read the first code №${order.tickets[1].number} at the entrance`);

      // A second reissue waits for the first letter: the dialog offers "Send the letter again".
      await page.getByTestId("reissue-open").click();
      await expect(page.getByTestId("reissue-pending")).toHaveAttribute("data-state", "pending");
      await page.getByTestId("reissue-resend-letter").click();
      await expect(page.getByTestId("reissue-resent")).toBeVisible();
      await page.getByTestId("reissue-close").click();
      expect(inspect(order.order_id).reissues).toHaveLength(1);
    });
  }

  test("a lost answer is checked with the same key and replays", async ({ page }) => {
    const order = freshOrder();
    const postUrl = orgUrl(`/orders/${order.order_id}/reissue-tickets`);
    const keys: (string | null)[] = [];
    let first = true;
    await page.route(postUrl, async (route) => {
      const req = route.request();
      if (req.method() !== "POST") return route.fallback();
      keys.push(await req.headerValue("idempotency-key"));
      if (first) {
        first = false;
        await route.fetch(); // the server DID replace the tickets
        return route.fulfill({
          status: 502,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "internal.bad_gateway", message: "bad gateway", request_id: "e2e" } }),
        });
      }
      return route.fallback();
    });
    await openCard(page, "org_admin", order.order_id);
    await walkToCheck(page, RIGHT_EMAIL);
    await page.getByTestId("reissue-go").click();
    await typeWord(page, "REISSUE TICKETS");
    await expect(page.getByTestId("reissue-unknown")).toContainText("The tickets can be replaced");
    await page.getByTestId("reissue-check-again").click();
    await expect(page.getByTestId("reissue-done")).toBeVisible();
    await expect(page.getByTestId("reissue-replayed")).toContainText("We made this replacement before");
    expect(keys).toHaveLength(2);
    expect(keys[0]).toMatch(/^web-/);
    expect(keys[1]).toBe(keys[0]);
    // One operation, two tickets replaced once.
    const after = inspect(order.order_id);
    expect(after.reissues).toHaveLength(1);
    expect(after.reissues[0].items).toBe(2);
    expect(after.tickets.filter((t) => t.status === "active")).toHaveLength(2);
  });

  test("an invitation issued before spec 46 is replaced without the case question (R-5)", async ({ page }) => {
    const inv = seed<{ invitation_id: string; email: string; tickets: { number: number; status: string }[] }>([
      "--reissue-op",
      "invitation",
    ]);
    const oldNumber = inv.tickets[0].number;
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/invitations`);
    const row = page
      .getByTestId("earlier-invitations")
      .getByTestId("invitation-row")
      .filter({ hasText: new RegExp(`Ticket ${oldNumber}(?!\\d)`) });
    await expect(row).toHaveCount(1);
    await row.getByTestId("invitation-reissue").click();
    // No case question: the address comes first, and the texts speak of a guest.
    await expect(page.getByTestId("reissue-address-form")).toBeVisible();
    await expect(page.getByTestId("reissue-case")).toHaveCount(0);
    await page.getByTestId("reissue-email").fill("reissue-guest@example.test");
    await page.getByTestId("reissue-continue").click();
    await page.getByTestId("reissue-reason-sent_to_wrong_address").check();
    await page.getByTestId("reissue-continue").click();
    await expect(page.getByTestId("reissue-check-tickets")).toContainText("Tickets of the invitation to replace: 1");
    await expect(page.getByTestId("reissue-check-to")).toContainText("The guest gets new tickets");
    await expect(page.getByTestId("reissue-check-provider")).toHaveCount(0);
    await noA11yViolations(page);
    await page.getByTestId("reissue-go").click();
    await typeWord(page, "REISSUE TICKETS");
    await expect(page.getByTestId("reissue-done")).toContainText("We replaced the invitation");
    await page.getByTestId("reissue-close").click();
    const after = seed<{ reissues: string[]; tickets: { number: number; status: string }[] }>([
      "--reissue-op",
      "inspect-invitation",
      "--order-id",
      inv.invitation_id,
    ]);
    expect(after.reissues).toEqual(["pending:reissue-guest@example.test"]);
    expect(after.tickets.find((t) => t.number === oldNumber)?.status).toBe("transferred");
    expect(after.tickets.filter((t) => t.status === "active")).toHaveLength(1);
  });

  test("a seller site's order is explained and nothing is sent", async ({ page }) => {
    seed(["--reissue-op", "site"]);
    const calls = trackCalls(page);
    await openCard(page, "org_admin", SITE_ORDER);
    await page.getByTestId("reissue-open").click();
    await expect(page.getByTestId("reissue-site")).toContainText("sold through the site of the seller");
    await expect(page.getByTestId("reissue-from-other")).toHaveCount(0);
    await noA11yViolations(page);
    await page.getByTestId("reissue-close").click();
    expect(calls.some((c) => c.endsWith("/reissue-tickets"))).toBe(false);
    expect(inspect(SITE_ORDER).reissues).toEqual([]);
  });
});
