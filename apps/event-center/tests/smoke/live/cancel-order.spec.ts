/**
 * Twin test "cancel an unpaid order with the typed word" (task 4-2), on the
 * three roles of one organization, against a REAL arena-api. It walks the
 * scenario of the bot's Orders e2e (bot_e2e_orders_integration_test.go,
 * `or:c:`): a wrong word cancels nothing, the order's own word cancels, a paid
 * order has no button, a 409 ("the status changed meanwhile") is shown
 * honestly.
 *
 * What the web adds on top of the bot: the word of an ORDER is not the word
 * of a SESSION, and the web asks for the password again before it cancels
 * (spec 37 4.6, owner decision 2026-10-14).
 *
 * The truth about each person comes from GET /v1/organizations/{org_id}/me.
 * The database rows (orders.status, order_events) are read through the seed
 * binary's `--cancel-order-op inspect`, because this process has no SQL
 * driver; `reset` puts the fixture back before every test, so a retry or a
 * second run starts clean.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();

// Fixed ids of cmd/arena-e2e-seed/cancel_order_fixture.go.
const ORDER_OWNER = "fe0000e2-0000-7000-8000-0000000000c1";
const ORDER_MANAGER = "fe0000e2-0000-7000-8000-0000000000c2";
const ORDER_AGENT = "fe0000e2-0000-7000-8000-0000000000c3";
const ORDER_PAID = "fe0000e2-0000-7000-8000-0000000000c4";
const ORDER_RACE = "fe0000e2-0000-7000-8000-0000000000c5";

const here = dirname(fileURLToPath(import.meta.url));
const appDir = resolve(here, "..", "..", "..");
const repoRoot = resolve(appDir, "..", "..");

/** The seed binary the harness built (or CI prebuilt). */
function seedBinary(): string {
  const given = process.env.ARENA_E2E_SEED_BIN;
  if (given !== undefined && given !== "") return given;
  const built = join(appDir, ".live", process.platform === "win32" ? "arena-e2e-seed.exe" : "arena-e2e-seed");
  if (!existsSync(built)) throw new Error(`the seed binary ${built} is missing: run through the live harness`);
  return built;
}

function seed(op: "reset" | "inspect" | "pay", orderId?: string): Record<string, unknown> {
  const args = ["--cancel-order-op", op];
  if (orderId !== undefined) args.push("--order-id", orderId);
  const r = spawnSync(seedBinary(), args, { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed ${args.join(" ")} failed: ${r.stderr}`);
  return JSON.parse(r.stdout.trim()) as Record<string, unknown>;
}

function rows(orderId: string): { status: string; cancelled_events: number } {
  return seed("inspect", orderId) as { status: string; cancelled_events: number };
}

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/me`, {
    headers: { Authorization: `Bearer ${person(env, role).access_token}` },
  });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

/** The cancel route as the web calls it, optionally with a step-up token. */
async function postCancel(request: APIRequestContext, role: Role, orderId: string, headers: Record<string, string> = {}) {
  return request.post(`${env.api_base_url}/v1/organizations/${env.org_id}/orders/${orderId}/cancel`, {
    headers: { Authorization: `Bearer ${person(env, role).access_token}`, ...headers },
    data: {},
  });
}

function trackApi(page: Page): string[] {
  const calls: string[] = [];
  page.on("request", (req) => {
    if (req.url().startsWith(env.api_base_url)) calls.push(`${req.method()} ${new URL(req.url()).pathname}`);
  });
  return calls;
}

const cancelPath = (orderId: string) => `POST /v1/organizations/${env.org_id}/orders/${orderId}/cancel`;

async function openCard(page: Page, role: Role, orderId: string) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/orders/${orderId}`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
}

/** Types into the word dialog and presses its button. */
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

test.beforeEach(() => {
  seed("reset");
});

test.describe("cancel an unpaid order (live)", () => {
  test("owner: a wrong word and the SESSION word cancel nothing, the ORDER word cancels after the password", async ({ page, request }) => {
    const me = await orgMe(request, "org_admin");
    expect(me.permissions.includes("order.write"), "order.write per the server").toBe(true);
    expect(rows(ORDER_OWNER)).toEqual({ status: "pending_payment", cancelled_events: 0 });

    const calls = trackApi(page);
    await openCard(page, "org_admin", ORDER_OWNER);
    await expect(page.getByTestId("cancel-order")).toBeVisible();
    await noA11yViolations(page);
    await page.getByTestId("cancel-order").click();
    const dialog = page.getByTestId("confirm-word");
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText("CANCEL ORDER");
    await expect(dialog).not.toContainText("CANCEL SESSION");
    await noA11yViolations(page);

    // Nothing typed: nothing sent.
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("confirm-word-note")).toBeVisible();
    // A wrong word.
    await typeWord(page, "yes please");
    await expect(page.getByTestId("confirm-word-note")).toContainText("nothing was changed");
    // The words of the other actions do not cancel an order, in any language.
    for (const other of ["CANCEL SESSION", "ОТМЕНИТЬ СЕАНС", "CANCELAR SESIÓN", "DELETE", "REFUND"]) {
      await typeWord(page, other);
      await expect(page.getByTestId("confirm-word-note")).toBeVisible();
      await expect(dialog, `the dialog stays after "${other}"`).toBeVisible();
    }
    expect(rows(ORDER_OWNER)).toEqual({ status: "pending_payment", cancelled_events: 0 });
    expect(calls.filter((c) => c === cancelPath(ORDER_OWNER)), "no cancel request was sent").toEqual([]);
    expect(calls.some((c) => c === "POST /v1/auth/step-up"), "no password was asked for a wrong word").toBe(false);

    // The order's own word, in another case and with extra spaces: the password question comes next.
    await typeWord(page, "  cancel   order. ");
    await confirmPassword(page, "org_admin");

    await expect(page.getByTestId("cancel-order-done")).toBeVisible();
    await expect(page.getByTestId("confirm-word")).toHaveCount(0);
    await expect(page.getByTestId("cancel-order")).toHaveCount(0);
    await expect(page.locator(".badge, [class*='badge']").first()).toContainText("Canceled");
    expect(rows(ORDER_OWNER)).toEqual({ status: "cancelled", cancelled_events: 1 });
    expect(calls.filter((c) => c === cancelPath(ORDER_OWNER))).toHaveLength(1);
    await noA11yViolations(page);
  });

  test("owner: the ORDER word in Russian cancels too", async ({ page }) => {
    await openCard(page, "org_admin", ORDER_OWNER);
    // The language chooser is on every screen of the org shell.
    await page.getByLabel("Language").selectOption("ru");
    await page.getByTestId("cancel-order").click();
    await expect(page.getByTestId("confirm-word")).toContainText("ОТМЕНИТЬ ЗАКАЗ");
    await page.getByTestId("confirm-word").getByRole("textbox").fill("отменить заказ");
    await page.getByTestId("confirm-word-submit").click();
    const dialog = page.getByTestId("step-up");
    await expect(dialog).toBeVisible();
    await dialog.getByRole("textbox").fill(person(env, "org_admin").password);
    await dialog.getByRole("button").first().click();
    await expect(page.getByTestId("cancel-order-done")).toBeVisible();
    expect(rows(ORDER_OWNER)).toEqual({ status: "cancelled", cancelled_events: 1 });
  });

  test("owner: closing the password question sends nothing and keeps the dialog", async ({ page }) => {
    const calls = trackApi(page);
    await openCard(page, "org_admin", ORDER_OWNER);
    await page.getByTestId("cancel-order").click();
    await typeWord(page, "CANCEL ORDER");
    await expect(page.getByTestId("step-up")).toBeVisible();
    await page.getByTestId("step-up").getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByTestId("step-up")).toHaveCount(0);
    await expect(page.getByTestId("confirm-word")).toBeVisible();
    expect(calls.filter((c) => c === cancelPath(ORDER_OWNER))).toEqual([]);
    expect(rows(ORDER_OWNER)).toEqual({ status: "pending_payment", cancelled_events: 0 });
  });

  test("manager can cancel with the order word", async ({ page, request }) => {
    const me = await orgMe(request, "organizer");
    expect(me.permissions.includes("order.write"), "order.write per the server").toBe(true);
    await openCard(page, "organizer", ORDER_MANAGER);
    await page.getByTestId("cancel-order").click();
    await typeWord(page, "CANCEL SESSION");
    await expect(page.getByTestId("confirm-word-note")).toBeVisible();
    expect(rows(ORDER_MANAGER).status).toBe("pending_payment");
    await typeWord(page, "CANCEL ORDER");
    await confirmPassword(page, "organizer");
    await expect(page.getByTestId("cancel-order-done")).toBeVisible();
    expect(rows(ORDER_MANAGER)).toEqual({ status: "cancelled", cancelled_events: 1 });
  });

  test("agent sees the order but no button, and the API refuses the agent", async ({ page, request }) => {
    const me = await orgMe(request, "agent");
    expect(me.role).toBe("agent");
    expect(me.permissions.includes("order.write"), "order.write per the server").toBe(false);
    // The agent holds no order.read either: the card itself is refused, so
    // there is no screen to put a button on. The server says no to the cancel.
    const res = await postCancel(request, "agent", ORDER_AGENT);
    expect(res.status(), "agent POST cancel").toBe(403);
    expect(rows(ORDER_AGENT)).toEqual({ status: "pending_payment", cancelled_events: 0 });

    const calls = trackApi(page);
    await signInAs(page, person(env, "agent"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/orders/${ORDER_AGENT}`);
    await expect(page.getByTestId("cancel-order")).toHaveCount(0);
    await expect(page.getByTestId("confirm-word")).toHaveCount(0);
    expect(calls.filter((c) => c === cancelPath(ORDER_AGENT))).toEqual([]);
    expect(rows(ORDER_AGENT)).toEqual({ status: "pending_payment", cancelled_events: 0 });
  });

  test("a paid order has no button, and the API answers 409", async ({ page, request }) => {
    await openCard(page, "org_admin", ORDER_PAID);
    await expect(page.getByTestId("ticket-list")).toBeVisible();
    await expect(page.getByTestId("cancel-order")).toHaveCount(0);
    // A stale client that presses anyway meets the server's own rule.
    const ask = await request.post(`${env.api_base_url}/v1/auth/step-up`, {
      headers: { Authorization: `Bearer ${person(env, "org_admin").access_token}`, "X-Client-Channel": "event_center_web" },
      data: { password: person(env, "org_admin").password, org_id: env.org_id },
    });
    expect(ask.status(), "step-up").toBe(200);
    const token = ((await ask.json()) as { step_up_token: string }).step_up_token;
    const res = await postCancel(request, "org_admin", ORDER_PAID, { "X-Step-Up-Token": token });
    expect(res.status(), "cancel of a paid order").toBe(409);
    expect(rows(ORDER_PAID)).toEqual({ status: "paid", cancelled_events: 0 });
  });

  test("the web channel needs the password: no step-up token, no cancellation", async ({ request }) => {
    const res = await postCancel(request, "org_admin", ORDER_OWNER, { "X-Client-Channel": "event_center_web" });
    expect(res.status()).toBe(403);
    const body = (await res.json()) as { error: { code: string } };
    expect(body.error.code).toBe("auth.step_up_required");
    expect(rows(ORDER_OWNER)).toEqual({ status: "pending_payment", cancelled_events: 0 });
  });

  test("a 409 while the card is open is shown honestly and the card reloads", async ({ page }) => {
    await openCard(page, "org_admin", ORDER_RACE);
    await page.getByTestId("cancel-order").click();
    // The payment lands while the dialog is open.
    seed("pay", ORDER_RACE);
    await typeWord(page, "CANCEL ORDER");
    await confirmPassword(page, "org_admin");

    const changed = page.getByTestId("cancel-order-changed");
    await expect(changed).toBeVisible();
    await expect(changed).toContainText("changed while you were working");
    await expect(changed).toContainText("nothing was canceled");
    await expect(page.getByTestId("confirm-word")).toHaveCount(0);
    // The card was reloaded: it shows the order as paid and no button.
    await expect(page.getByTestId("cancel-order")).toHaveCount(0);
    await expect(page.getByText("Paid", { exact: true }).first()).toBeVisible();
    expect(rows(ORDER_RACE)).toEqual({ status: "paid", cancelled_events: 0 });
  });
});
