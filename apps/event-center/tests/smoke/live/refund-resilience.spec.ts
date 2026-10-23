/**
 * Resilience of the web refund (task 8-1) against a REAL arena-api, the REAL refund engine and the
 * harness's local Stripe stub (never Stripe): what the original twin spec (refund.spec.ts) does not
 * reach. An answer that is lost or a 5xx is UNKNOWN, not a rejection: the retry is the SAME request
 * with the SAME Idempotency-Key and the provider is called once. A double press is one request, an
 * amount is exact in minor units, and the card of another order never inherits a ticked ticket, a
 * reason or a key.
 *
 * Data: apps/backend/cmd/arena-e2e-seed/refund_fixture.go, put back before every test.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();

const ORDER_OWNER = "fe0000e2-0000-7000-8000-000000008111"; // three tickets, the third scanned
const ORDER_MANAGER = "fe0000e2-0000-7000-8000-000000008112"; // one ticket
const TICKET_1 = "fe0000e2-0000-7000-8000-000000008121";
const TICKET_2 = "fe0000e2-0000-7000-8000-000000008122";

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
}

function seed(op: "reset" | "inspect"): Record<string, OrderRows> {
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

const createUrl = (orderId: string) => `**/v1/organizations/${env.org_id}/orders/${orderId}/refunds`;

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

async function confirmPassword(page: Page, role: Role, twice = false) {
  const dialog = page.getByTestId("step-up");
  await expect(dialog).toBeVisible();
  await dialog.getByLabel("Password").fill(person(env, role).password);
  const confirm = dialog.getByRole("button", { name: "Confirm" });
  if (twice) await confirm.dblclick();
  else await confirm.click();
}

const ticketBox = (page: Page, id: string) => page.locator(`[data-testid="refund-ticket"][data-ticket="${id}"]`);

/** Router navigation without a reload: retained component state would show. */
const go = (page: Page, to: string, params: Record<string, string>) =>
  page.evaluate(
    async ([path, p]) => {
      const modulePath = "/src/router.tsx";
      const { router } = await import(modulePath);
      await router.navigate({ to: path, params: p });
    },
    [to, params] as const,
  );

test.beforeEach(async ({ request }) => {
  seed("reset");
  expect((await request.post(`${env.stripe_stub_url}/__reset`)).status()).toBe(200);
});

test.describe("refund: unknown outcomes, double presses and exact amounts (live)", () => {
  for (const reply of ["gateway", "invalid_json", "missing_fields", "wrong_order"] as const) {
  test(`an unverified ${reply} answer is UNKNOWN: same key, one provider call`, async ({ page, request }) => {
    await openCard(page, "org_admin", ORDER_OWNER);
    const sent: { key: string | null; body: string | null }[] = [];
    let first = true;
    await page.route(createUrl(ORDER_OWNER), async (route) => {
      const req = route.request();
      if (req.method() !== "POST") return route.fallback();
      sent.push({ key: await req.headerValue("idempotency-key"), body: req.postData() });
      if (first) {
        first = false;
        const answer = await route.fetch(); // the server DID refund
        return route.fulfill({
          status: reply === "gateway" ? 502 : 200,
          contentType: "application/json",
          body: reply === "gateway" ? JSON.stringify({ error: { code: "internal.bad_gateway", message: "bad gateway", request_id: "e2e" } })
            : reply === "invalid_json" ? "{broken"
            : reply === "missing_fields" ? "{}"
            : JSON.stringify({ ...(await answer.json()), order_id: "00000000-0000-7000-8000-000000000999" }),
        });
      }
      return route.fallback();
    });
    await page.getByTestId("refund-open").click();
    await ticketBox(page, TICKET_1).check();
    await page.getByTestId("refund-reason-buyer").check();
    await page.getByTestId("refund-continue").click();
    await page.getByTestId("refund-go").click();
    await typeWord(page, "REFUND");
    await confirmPassword(page, "org_admin");

    // Not a rejection: the money may have moved, the screen says the answer is missing.
    await expect(page.getByTestId("refund-unknown")).toBeVisible();
    await expect(page.getByTestId("refund-failure")).toHaveCount(0);
    await expect(page.getByTestId("refund-result")).toHaveCount(0);
    expect(rows(ORDER_OWNER)).toMatchObject({ refunds: 1, states: ["succeeded"], amounts: [2500] });

    await page.getByTestId("refund-check").click();
    await expect(page.getByTestId("refund-result")).toContainText("Nothing was refunded twice");
    expect(sent).toHaveLength(2);
    expect(sent[0].key, "a key was sent").toBeTruthy();
    expect(sent[1].key, "the retry carries the SAME key").toBe(sent[0].key);
    expect(sent[1].body, "the retry is the SAME request").toBe(sent[0].body);
    const calls = await stripeCalls(request);
    expect(calls, "the provider was called once").toHaveLength(1);
    expect(calls[0].key.length).toBeGreaterThan(0);
    expect(rows(ORDER_OWNER)).toMatchObject({ refunds: 1, states: ["succeeded"], active_tickets: 2, status: "partially_refunded" });
  });

  }

  test("a double press on the password button is ONE refund request and ONE provider call", async ({ page, request }) => {
    await openCard(page, "org_admin", ORDER_OWNER);
    let posts = 0;
    await page.route(createUrl(ORDER_OWNER), async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      posts += 1;
      await new Promise((r) => setTimeout(r, 800)); // a second click can land while the first runs
      return route.fallback();
    });
    await page.getByTestId("refund-open").click();
    await ticketBox(page, TICKET_2).check();
    await page.getByTestId("refund-reason-event").check();
    await page.getByTestId("refund-continue").click();
    await page.getByTestId("refund-go").click();
    await typeWord(page, "REFUND");
    await confirmPassword(page, "org_admin", true);
    await expect(page.getByTestId("refund-result")).toBeVisible();
    expect(posts, "one refund request").toBe(1);
    expect(await stripeCalls(request)).toHaveLength(1);
    expect(rows(ORDER_OWNER)).toMatchObject({ refunds: 1, amounts: [2500], active_tickets: 2 });
  });

  test("a part of the price is exact in minor units (19,99 is 1999, never 1998 or 2000)", async ({ page, request }) => {
    await openCard(page, "organizer", ORDER_MANAGER);
    await page.getByTestId("refund-open").click();
    await page.getByTestId("refund-ticket").check();
    await page.getByTestId("refund-partial").check();
    await page.getByTestId("refund-part").fill("19,99");
    await page.getByTestId("refund-reason-buyer").check();
    await page.getByTestId("refund-continue").click();
    await expect(page.getByTestId("refund-final-amount")).toContainText(/19[.,]99/);
    await page.getByTestId("refund-go").click();
    await typeWord(page, "REFUND");
    await confirmPassword(page, "organizer");
    await expect(page.getByTestId("refund-result-line")).toHaveAttribute("data-state", "succeeded");
    expect((await stripeCalls(request)).map((c) => c.amount)).toEqual([1999]);
    expect(rows(ORDER_MANAGER)).toMatchObject({ refunds: 1, amounts: [1999] });
  });

  test("the card of another order starts clean: no ticked ticket, reason or key carries over, and no request leaves", async ({ page, request }) => {
    await openCard(page, "org_admin", ORDER_OWNER);
    // Both cards are in the query cache, so leaving shows the other card at once.
    await go(page, "/o/$orgId/orders/$orderId", { orgId: env.org_id, orderId: ORDER_MANAGER });
    await expect(page.getByTestId("refund-open")).toBeVisible();
    await go(page, "/o/$orgId/orders/$orderId", { orgId: env.org_id, orderId: ORDER_OWNER });
    await expect(page.getByTestId("refund-open")).toBeVisible();

    const posts: string[] = [];
    page.on("request", (r) => {
      if (r.method() === "POST" && r.url().includes("/refunds")) posts.push(`${r.method()} ${new URL(r.url()).pathname}`);
    });
    await page.getByTestId("refund-open").click();
    await ticketBox(page, TICKET_1).check();
    await page.getByTestId("refund-reason-buyer").check();
    await page.getByTestId("refund-continue").click();
    await expect(page.getByTestId("refund-final")).toBeVisible();

    await go(page, "/o/$orgId/orders/$orderId", { orgId: env.org_id, orderId: ORDER_MANAGER });
    await expect(page).toHaveURL(new RegExp(`/orders/${ORDER_MANAGER}$`));
    await expect(page.getByTestId("refund-open")).toBeVisible();
    await expect(page.getByTestId("refund-panel")).toHaveCount(0);
    await expect(page.getByTestId("refund-final")).toHaveCount(0);
    await page.getByTestId("refund-open").click();
    // The other order's own ticket is not ticked, and no reason is chosen.
    await expect(page.getByTestId("refund-ticket")).not.toBeChecked();
    await expect(page.getByTestId("refund-reason-buyer")).not.toBeChecked();
    expect(posts).toEqual([]);
    expect(await stripeCalls(request)).toEqual([]);
    expect(rows(ORDER_MANAGER).refunds).toBe(0);
    expect(rows(ORDER_OWNER).refunds).toBe(0);
  });
});
