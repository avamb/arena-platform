/**
 * Twin test "smart price" (spec 08_architecture/51_4_2_smart_price_ru.md),
 * the web version of the bot's `bot_e2e_smart_price_integration_test.go`,
 * against a REAL arena-api.
 *
 * One date of its own (the seed's "E2E Smart Price Show", one category of 100
 * places at 20 EUR). `--smart-price-op reset` puts it back before each
 * scenario, `--smart-price-op sell` sells the step on sale and runs the chain
 * sweep the worker would run.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const EVENT = "fe0000e2-0000-7000-8000-00000000e801";
const SESSION = "fe0000e2-0000-7000-8000-00000000e802";
const TIER = "fe0000e2-0000-7000-8000-00000000e811";
const FOREIGN_ORG = "fe0000e2-0000-7000-8000-0000000000f1";
const NAME = "E2E Smart Standing";

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

function seed(op: "reset" | "sell") {
  const r = spawnSync(seedBinary(), ["--smart-price-op", op], { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed --smart-price-op ${op} failed: ${r.stderr}`);
}

interface Rule {
  id: string;
  tier_id: string;
  status: string;
  step_tier_ids: string[];
  now?: { price: number; step: number; steps: number; next_price?: number | null; remaining_at_price?: number | null } | null;
}
interface Tier {
  id: string;
  name: string;
  price_amount: number;
  capacity: number | null;
}

const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const smartUrl = (org = env.org_id) => `${env.api_base_url}/v1/organizations/${org}/sessions/${SESSION}/smart-price`;
const tiersUrl = `${env.api_base_url}/v1/organizations/${env.org_id}/events/${EVENT}/sessions/${SESSION}/tiers`;

async function rules(request: APIRequestContext): Promise<Rule[]> {
  const res = await request.get(smartUrl(), { headers: auth("org_admin") });
  expect(res.status()).toBe(200);
  return ((await res.json()) as { rules: Rule[] }).rules;
}

async function tiers(request: APIRequestContext): Promise<Tier[]> {
  const res = await request.get(tiersUrl, { headers: auth("org_admin") });
  expect(res.status()).toBe(200);
  return ((await res.json()) as { tiers: Tier[] }).tiers;
}

async function openCard(page: Page, role: Role, signIn = true) {
  if (signIn) {
    await signInAs(page, person(env, role));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  }
  await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}`);
  await expect(page.getByTestId("tiers-table")).toBeVisible();
  await page
    .getByTestId("tier-row")
    .filter({ has: page.locator(`[data-label="Category"]`, { hasText: new RegExp(`^${NAME}$`) }) })
    .getByTestId("tier-manage")
    .click();
  await expect(page.getByTestId("category-title")).toHaveText(`Category: ${NAME}`);
  await expect(page.getByTestId("smart-price")).toBeVisible();
}

test.describe("smart price (live)", () => {
  test("owner: steps by tickets sold, the price rises after a sale, stop keeps the price", async ({ page, request }) => {
    seed("reset");
    await openCard(page, "org_admin");
    await page.getByTestId("smart-price-open").click();
    await page.getByTestId("smart-kind-by_sales").check();
    await page.getByTestId("smart-step").fill("10");
    await page.getByTestId("smart-every").fill("30");
    await page.getByTestId("smart-cap").fill("50");
    await page.getByTestId("smart-price-preview").click();
    // 100 places: 30, 30, 30 and the rest of 10, at 20, 22, 24 and 26 EUR.
    await expect(page.getByTestId("smart-price-row")).toHaveCount(4);
    await expect(page.getByTestId("smart-price-table")).toContainText("26.00");
    // The preview writes nothing.
    expect(await rules(request)).toHaveLength(0);
    await noA11yViolations(page);

    await page.getByTestId("smart-price-enable").click();
    await expect(page.getByTestId("smart-price-done")).toBeVisible();
    await expect(page.getByTestId("smart-price-now")).toContainText("1");
    let rs = await rules(request);
    expect(rs).toHaveLength(1);
    expect(rs[0].status).toBe("active");
    expect(rs[0].step_tier_ids).toHaveLength(3);
    expect(rs[0].now).toMatchObject({ price: 2000, step: 1, steps: 4, next_price: 2200, remaining_at_price: 30 });
    // The hall keeps its places: the four steps own the 100 places of the category.
    const ts = await tiers(request);
    expect(ts.reduce((n, t) => n + (t.capacity ?? 0), 0)).toBe(100);

    seed("sell");
    rs = await rules(request);
    expect(rs[0].now).toMatchObject({ price: 2200, step: 2, next_price: 2400 });

    await openCard(page, "org_admin", false);
    await page.getByTestId("smart-price-stop").click();
    await expect(page.getByTestId("smart-price-stop-confirm")).toContainText("22.00");
    await page.getByTestId("smart-price-stop-yes").click();
    await expect(page.getByTestId("smart-price-done")).toBeVisible();
    rs = await rules(request);
    expect(rs[0].status).toBe("stopped");
    expect(Math.max(...(await tiers(request)).map((t) => t.price_amount))).toBe(2200);
  });

  test("manager: a rise by days, then a price set by hand turns it off", async ({ page, request }) => {
    seed("reset");
    await openCard(page, "organizer");
    await page.getByTestId("smart-price-open").click();
    await page.getByTestId("smart-kind-by_days").check();
    await page.getByTestId("smart-step").fill("10");
    await page.getByTestId("smart-every").fill("7");
    await page.getByTestId("smart-cap").fill("30");
    await page.getByTestId("smart-price-preview").click();
    await expect(page.getByTestId("smart-price-row").first()).toBeVisible();
    await page.getByTestId("smart-price-enable").click();
    await expect(page.getByTestId("smart-price-active")).toBeVisible();
    expect((await rules(request))[0].status).toBe("active");

    await page.getByTestId("price-change").click();
    await page.getByTestId("price-input").fill("21");
    await page.getByTestId("price-check").click();
    await expect(page.getByTestId("price-confirm-smart")).toBeVisible();
    await page.getByTestId("price-save").click();
    await expect(page.getByTestId("smart-price-ended")).toBeVisible();
    expect((await rules(request))[0].status).toBe("overridden");
    // The price stops rising: no later price change is left.
    await expect(page.getByTestId("category-card")).not.toContainText("The price changes on");
  });

  test("the agent and another organization change nothing", async ({ request }) => {
    seed("reset");
    const body = { tier_ids: [TIER], kind: "by_days", step_percent: 10, every: 7, cap_percent: 30 };
    const agent = await request.post(smartUrl(), { headers: auth("agent"), data: body });
    expect(agent.status()).toBe(403);
    const foreign = await request.post(smartUrl(FOREIGN_ORG), { headers: auth("org_admin"), data: body });
    expect(foreign.status()).toBeGreaterThanOrEqual(400);
    expect(await rules(request)).toHaveLength(0);
  });
});
