/**
 * Twin test "Mailings and reminders" (spec 51-1), on the three roles of one
 * organization, against a REAL arena-api. The bot has no screen for it yet, so
 * the scenario is the API's own: GET/PUT .../growth-settings and
 * GET .../marketing-subscribers[.csv].
 *
 * The owner switches the mailing tick on with a reward code, the screen offers
 * only a code that may be a reward, the API stores exactly that, and switching
 * the tick off drops the reward. Subscribers are listed by state and the CSV
 * carries them. The manager reads the settings but cannot save (and the API
 * says 403); whoever lacks the permissions sees what the API allows, nothing
 * more.
 *
 * The seed binary's `--growth-op reset` puts the organization back on the
 * defaults (reminder on, tick off), with the codes E2EGROWTH (one use per
 * buyer) and E2EGROWTHMANY (no limit), a confirmed subscriber and a pending one.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const REWARD_CODE_ID = "fe0000e2-0000-7000-8000-00000000e801";
const REWARD = "E2EGROWTH";
const MANY = "E2EGROWTHMANY";
const ANNA = "anna.growth@e2e.test";
const BORIS = "boris.growth@e2e.test";

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

function reset() {
  const r = spawnSync(seedBinary(), ["--growth-op", "reset"], { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed --growth-op reset failed: ${r.stderr}`);
}

const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;

interface Settings {
  cart_reminder_enabled: boolean;
  marketing_optin_enabled: boolean;
  consent_promo_code_id: string | null;
}

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${base}/me`, { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { permissions: string[] };
}

async function readSettings(request: APIRequestContext): Promise<Settings> {
  const res = await request.get(`${base}/growth-settings`, { headers: auth("org_admin") });
  expect(res.status()).toBe(200);
  return (await res.json()) as Settings;
}

async function openGrowth(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.getByTestId("nav-more").click();
  await page.getByTestId("nav-growth").click();
  await expect(page.getByRole("heading", { level: 1, name: "Messages to buyers" })).toBeVisible();
}

test.beforeAll(() => reset());

test("the owner switches the tick on with a reward, and off again", async ({ page, request }) => {
  // Two saves and an axe pass; the first page of a run also waits for vite to compile.
  test.setTimeout(60_000);
  await openGrowth(page, "org_admin");
  const card = page.getByTestId("growth-settings");
  await expect(card.getByTestId("growth-reminder")).toBeChecked();
  await expect(card.getByTestId("growth-optin")).not.toBeChecked();
  await expect(card.getByTestId("growth-save")).toBeDisabled();

  await card.getByTestId("growth-optin").check();
  const reward = card.getByTestId("growth-reward");
  await expect(reward.locator("option", { hasText: REWARD })).toHaveCount(1);
  await expect(reward.locator("option", { hasText: MANY })).toHaveCount(0);
  await reward.selectOption({ label: REWARD });
  await card.getByTestId("growth-save").click();
  await expect(card.getByTestId("growth-saved")).toBeVisible();
  expect(await readSettings(request)).toMatchObject({
    cart_reminder_enabled: true,
    marketing_optin_enabled: true,
    consent_promo_code_id: REWARD_CODE_ID,
  });
  await noA11yViolations(page);

  // Off again: the reminder too, and the reward goes with the tick.
  await card.getByTestId("growth-reminder").uncheck();
  await card.getByTestId("growth-optin").uncheck();
  await expect(card.getByTestId("growth-reward")).toHaveCount(0);
  await card.getByTestId("growth-save").click();
  await expect(card.getByTestId("growth-saved")).toBeVisible();
  expect(await readSettings(request)).toMatchObject({
    cart_reminder_enabled: false,
    marketing_optin_enabled: false,
    consent_promo_code_id: null,
  });
});

test("the API refuses a code that may not be a reward", async ({ request }) => {
  const res = await request.put(`${base}/growth-settings`, {
    headers: auth("org_admin"),
    data: { cart_reminder_enabled: true, marketing_optin_enabled: true, consent_promo_code_id: "fe0000e2-0000-7000-8000-00000000e802" },
  });
  expect(res.status()).toBe(422);
  expect((await readSettings(request)).consent_promo_code_id).toBeNull();
});

test("subscribers are listed by state and the CSV carries them", async ({ page }) => {
  await openGrowth(page, "org_admin");
  const subs = page.getByTestId("growth-subscribers");
  await expect(subs.getByTestId("subscriber-row")).toHaveCount(1);
  await expect(subs.getByTestId("subscriber-row")).toContainText(ANNA);
  await subs.getByTestId("growth-filter").selectOption("pending");
  await expect(subs.getByTestId("subscriber-row")).toHaveCount(1);
  await expect(subs.getByTestId("subscriber-row")).toContainText(BORIS);
  await subs.getByTestId("growth-filter").selectOption("all");
  await expect(subs.getByTestId("subscriber-row")).toHaveCount(2);

  const [download] = await Promise.all([page.waitForEvent("download"), subs.getByTestId("growth-csv").click()]);
  const csv = readFileSync(await download.path(), "utf8");
  expect(csv).toContain(ANNA);
  expect(csv).toContain(BORIS);
});

test("the manager reads the settings and cannot save them", async ({ page, request }) => {
  const me = await orgMe(request, "organizer");
  expect(me.permissions).toContain("promo.read");
  expect(me.permissions).not.toContain("org.update");

  await openGrowth(page, "organizer");
  const card = page.getByTestId("growth-settings");
  await expect(card.getByText("Only the owner can change these settings.")).toBeVisible();
  await expect(card.getByTestId("growth-save")).toHaveCount(0);
  await expect(card.getByTestId("growth-optin")).toBeDisabled();

  const res = await request.put(`${base}/growth-settings`, {
    headers: auth("organizer"),
    data: { cart_reminder_enabled: true, marketing_optin_enabled: false, consent_promo_code_id: null },
  });
  expect(res.status()).toBe(403);
});

test("the agent sees exactly what the API allows", async ({ page, request }) => {
  const me = await orgMe(request, "agent");
  const canSettings = me.permissions.includes("promo.read");
  const canSubscribers = me.permissions.includes("order.read");

  const settings = await request.get(`${base}/growth-settings`, { headers: auth("agent") });
  expect(settings.status()).toBe(canSettings ? 200 : 403);
  const subscribers = await request.get(`${base}/marketing-subscribers`, { headers: auth("agent") });
  expect(subscribers.status()).toBe(canSubscribers ? 200 : 403);

  await signInAs(page, person(env, "agent"));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  if (!canSettings && !canSubscribers) {
    await page.getByTestId("nav-more").click();
    await expect(page.getByTestId("nav-growth")).toHaveCount(0);
    await page.goto(`/o/${env.org_id}/growth`);
    await expect(page.getByText("Only the owner and the managers see this page.")).toBeVisible();
    return;
  }
  await page.getByTestId("nav-more").click();
  await page.getByTestId("nav-growth").click();
  await expect(page.getByTestId("growth-settings")).toHaveCount(canSettings ? 1 : 0);
  await expect(page.getByTestId("growth-subscribers")).toHaveCount(canSubscribers ? 1 : 0);
});
