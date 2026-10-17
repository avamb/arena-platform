/**
 * Twin test "a date's categories: open and close, price, sale dates,
 * quantity" (task 5-4), the web version of the bot's
 * `bot_e2e_categories_integration_test.go`, against a REAL arena-api, on the
 * three roles of one organization (owner = org_admin, manager = organizer,
 * agent).
 *
 * The truth about each role comes from GET /v1/organizations/{org_id}/me; the
 * screen must agree with it, and the API itself must refuse what the screen
 * hides. The scenarios run one after another on one date (the seed's "E2E
 * Categories Show"), so the file is serial. The seed binary's
 * `--categories-op` puts the five categories back before the run and reads
 * the rows (places, window prices) the API does not show.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const EVENT = "fe0000e2-0000-7000-8000-00000000e501";
const SESSION = "fe0000e2-0000-7000-8000-00000000e502";
const FOREIGN_ORG = "fe0000e2-0000-7000-8000-0000000000f1";
// Another organization's date and category (categories_fixture.go).
const FOREIGN_EVENT = "fe0000e2-0000-7000-8000-00000000e522";
const FOREIGN_SESSION = "fe0000e2-0000-7000-8000-00000000e523";
const FOREIGN_TIER = "fe0000e2-0000-7000-8000-00000000e524";

const STANDING = "E2E Cat Standing";
const BALCONY = "E2E Cat Balcony";
const EARLY = "E2E Cat Early";
const LATE = "E2E Cat Late";
const FREE = "E2E Cat Free";

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

interface Inspect {
  price_amount: number;
  is_open: boolean;
  capacity: number | null;
  places: number;
  sold: number;
  sale_window_start: string | null;
  sale_window_end: string | null;
  window_prices: number[] | null;
}

function seed(op: "reset" | "inspect"): Record<string, Inspect> {
  const r = spawnSync(seedBinary(), ["--categories-op", op], { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed --categories-op ${op} failed: ${r.stderr}`);
  return JSON.parse(r.stdout.trim()) as Record<string, Inspect>;
}
const rows = () => seed("inspect");

interface Tier {
  id: string;
  name: string;
  price_amount: number;
  current_price: number | null;
  is_open: boolean;
  capacity: number | null;
  sale_window_start: string | null;
  sale_window_end: string | null;
}

const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const tiersUrl = (org = env.org_id) => `${env.api_base_url}/v1/organizations/${org}/events/${EVENT}/sessions/${SESSION}/tiers`;

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${base}/me`, { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

async function apiTiers(request: APIRequestContext, role: Role = "org_admin"): Promise<Tier[]> {
  const res = await request.get(tiersUrl(), { headers: auth(role) });
  expect(res.status(), `GET tiers as ${role}`).toBe(200);
  return ((await res.json()) as { tiers: Tier[] }).tiers;
}

async function apiTier(request: APIRequestContext, name: string, role: Role = "org_admin"): Promise<Tier> {
  const found = (await apiTiers(request, role)).find((t) => t.name === name);
  expect(found, `category ${name} in the API list`).toBeDefined();
  return found as Tier;
}

/** The wall clock of an instant in the venue's zone (the seed's venue is in Madrid). */
function madrid(instant: string): { day: string; time: string } {
  const p = new Intl.DateTimeFormat("en-GB", {
    timeZone: "Europe/Madrid",
    hourCycle: "h23",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).formatToParts(new Date(instant));
  const g = (type: string) => p.find((x) => x.type === type)?.value ?? "";
  return { day: `${g("year")}-${g("month")}-${g("day")}`, time: `${g("hour")}:${g("minute")}:${g("second")}` };
}

/** "2026-11-25" minus one day, as the card prints it: "24.11.2026". */
function lastDayLabel(endDay: string): string {
  const [y, m, d] = endDay.split("-").map(Number);
  const t = new Date(Date.UTC(y, m - 1, d - 1));
  return `${String(t.getUTCDate()).padStart(2, "0")}.${String(t.getUTCMonth() + 1).padStart(2, "0")}.${t.getUTCFullYear()}`;
}

async function openSession(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}`);
  await expect(page.getByTestId("tiers-table")).toBeVisible();
}

const row = (page: Page, name: string) => page.getByTestId("tier-row").filter({ has: page.locator(`[data-label="Category"]`, { hasText: name }) });

async function openCard(page: Page, name: string) {
  await row(page, name).getByTestId("tier-manage").click();
  await expect(page.getByTestId("category-title")).toHaveText(`Category: ${name}`);
}

test.beforeAll(() => {
  seed("reset");
});

test.describe("categories of a date (live, three roles)", () => {
  test("owner: the list, close and open, price, the price window in force, nothing else changes", async ({ page, request }) => {
    const me = await orgMe(request, "org_admin");
    expect(me.permissions).toContain("tier.read");
    expect(me.permissions).toContain("tier.update");

    const apiCalls: string[] = [];
    page.on("request", (req) => {
      if (req.url().startsWith(env.api_base_url)) apiCalls.push(`${req.method()} ${new URL(req.url()).pathname}`);
    });

    await openSession(page, "org_admin");
    // The list: five categories, the price buyers pay NOW (the window, not the base price).
    await expect(page.getByTestId("tier-row")).toHaveCount(5);
    await expect(row(page, STANDING)).toContainText("25.00");
    await expect(row(page, STANDING)).toContainText("3"); // sold
    await expect(row(page, BALCONY)).toContainText("40.00");

    // Close, then open: one press each, only new sales stop.
    await openCard(page, STANDING);
    await noA11yViolations(page);
    const width = page.viewportSize()?.width ?? 375;
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
    await page.getByTestId("toggle-btn").click();
    await expect(page.getByTestId("category-notice")).toContainText("Closed");
    await expect(page.getByTestId("category-notice")).toContainText("already started stay as they are");
    await expect(page.getByTestId("category-state")).toContainText("Closed");
    expect((await apiTier(request, STANDING)).is_open).toBe(false);
    expect(rows()[STANDING].is_open).toBe(false);
    await page.getByTestId("toggle-btn").click();
    await expect(page.getByTestId("category-notice")).toContainText("Open again");
    expect((await apiTier(request, STANDING)).is_open).toBe(true);

    // Price: a bad shape is stopped on the screen, before any call.
    const before = apiCalls.filter((c) => c.startsWith("PATCH") || c.startsWith("PUT")).length;
    await page.getByTestId("price-change").click();
    await expect(page.getByTestId("price-input")).toHaveValue("25");
    await page.getByTestId("price-input").fill("abc");
    await page.getByTestId("price-check").click();
    await expect(page.getByTestId("price-input-error")).toContainText("That is not a price");
    expect(apiCalls.filter((c) => c.startsWith("PATCH") || c.startsWith("PUT")).length, "no write for a bad price").toBe(before);

    // A good price is confirmed first (old -> new), and nothing is written until the press.
    await page.getByTestId("price-input").fill("27,5");
    await page.getByTestId("price-check").click();
    await expect(page.getByTestId("price-confirm")).toContainText("25.00");
    await expect(page.getByTestId("price-confirm")).toContainText("27.50");
    await expect(page.getByTestId("price-confirm")).toContainText("new purchases only");
    await expect(page.getByTestId("price-confirm-window")).toHaveCount(0);
    expect((await apiTier(request, STANDING)).price_amount, "not saved before the press").toBe(2500);
    await page.getByTestId("price-save").click();
    await expect(page.getByTestId("category-notice")).toContainText("27.50");
    await expect(page.getByTestId("category-price")).toContainText("27.50");
    expect((await apiTier(request, STANDING)).price_amount).toBe(2750);

    // Balcony follows a schedule: the window in force now changes, the base price does not.
    await openCard(page, BALCONY);
    await page.getByTestId("price-change").click();
    await expect(page.getByTestId("price-input")).toHaveValue("40");
    await page.getByTestId("price-input").fill("42");
    await page.getByTestId("price-check").click();
    await expect(page.getByTestId("price-confirm-window")).toContainText("price window in force now");
    await page.getByTestId("price-save").click();
    await expect(page.getByTestId("category-notice")).toContainText("price window in force now was changed");
    const balcony = await apiTier(request, BALCONY);
    expect(balcony.current_price).toBe(4200);
    expect(balcony.price_amount, "the base price stays").toBe(3500);
    expect(rows()[BALCONY].window_prices).toEqual([4200, 5000]);
    await expect(row(page, BALCONY)).toContainText("42.00");
  });

  test("owner: sale dates are the venue's days, the last day counts, a limit can be removed", async ({ page, request }) => {
    await openSession(page, "org_admin");
    await openCard(page, STANDING);
    await expect(page.getByTestId("category-window")).toContainText("no limits");

    // The last day: a week from now. It ends at the venue's midnight AFTER that day.
    await page.getByTestId("window-edit-end").click();
    await expect(page.getByTestId("window-save")).toBeDisabled();
    await page.getByTestId("date-quick-week").click();
    await page.getByTestId("window-save").click();
    await expect(page.getByTestId("category-notice")).toContainText("sale dates are changed");
    const saved = await apiTier(request, STANDING);
    expect(saved.sale_window_end).not.toBeNull();
    const end = madrid(saved.sale_window_end as string);
    expect(end.time, "ends at the venue's midnight").toBe("00:00:00");
    expect(saved.sale_window_start).toBeNull();
    // The card says the LAST day (the day before that midnight), not the midnight.
    await expect(page.getByTestId("window-end-value")).toHaveText(lastDayLabel(end.day));
    await expect(page.getByTestId("category-window")).toContainText("until");
    expect(rows()[STANDING].sale_window_end).not.toBeNull();

    // The first day: today, from the venue's midnight.
    await page.getByTestId("window-edit-start").click();
    await page.getByTestId("date-quick-today").click();
    await page.getByTestId("window-save").click();
    await expect(page.getByTestId("category-notice")).toContainText("sale dates are changed");
    const both = await apiTier(request, STANDING);
    expect(madrid(both.sale_window_start as string).time).toBe("00:00:00");
    expect(new Date(both.sale_window_start as string).getTime()).toBeLessThan(new Date(both.sale_window_end as string).getTime());
    await expect(page.getByTestId("category-window")).toContainText("inclusive");

    // The API refuses a window that ends before it starts, in the API's own words.
    const bad = await request.patch(`${tiersUrl()}/${both.id}`, {
      headers: auth("org_admin"),
      data: { sale_window_end: new Date(new Date(both.sale_window_start as string).getTime() - 86_400_000).toISOString() },
    });
    expect(bad.status()).toBe(400);
    expect(((await bad.json()) as { error: { code: string } }).error.code).toBe("tier.invalid_sale_window");

    // Remove both limits, one press each.
    await page.getByTestId("window-remove-end").click();
    await expect(page.getByTestId("category-notice")).toContainText("limit is removed");
    await page.getByTestId("window-remove-start").click();
    await expect(page.getByTestId("category-window")).toContainText("no limits");
    const cleared = await apiTier(request, STANDING);
    expect(cleared.sale_window_start).toBeNull();
    expect(cleared.sale_window_end).toBeNull();
  });

  test("owner: the quantity cannot go below what is sold, and grows the real places", async ({ page, request }) => {
    const apiCalls: string[] = [];
    page.on("request", (req) => {
      if (req.url().startsWith(env.api_base_url)) apiCalls.push(`${req.method()} ${new URL(req.url()).pathname}`);
    });
    await openSession(page, "org_admin");
    await openCard(page, STANDING);
    await page.getByTestId("qty-change").click();
    await expect(page.getByTestId("qty-input")).toHaveValue("20");
    await expect(page.getByTestId("qty-form")).toContainText("Not less than 3");

    // Not a number: stopped on the screen.
    const before = apiCalls.filter((c) => c.startsWith("PATCH")).length;
    await page.getByTestId("qty-input").fill("abc");
    await page.getByTestId("qty-save").click();
    await expect(page.getByTestId("qty-input-error")).toContainText("whole number of places");
    expect(apiCalls.filter((c) => c.startsWith("PATCH")).length, "no PATCH for a bad number").toBe(before);

    // Below what is sold: the API says no, the screen says the floor.
    await page.getByTestId("qty-input").fill("2");
    await page.getByTestId("qty-save").click();
    await expect(page.getByTestId("category-error")).toContainText("3 places are sold or on hold");
    expect((await apiTier(request, STANDING)).capacity).toBe(20);
    expect(rows()[STANDING].places).toBe(20);

    // More places: the real places are minted.
    await page.getByTestId("qty-input").fill("25");
    await page.getByTestId("qty-save").click();
    await expect(page.getByTestId("category-notice")).toContainText("now has 25 places");
    await expect(page.getByTestId("category-places")).toContainText("of 25");
    expect((await apiTier(request, STANDING)).capacity).toBe(25);
    const stored = rows()[STANDING];
    expect(stored.places).toBe(25);
    expect(stored.sold, "sold places are untouched").toBe(3);
  });

  test("owner: a chain category keeps only its price, a free one has none, another organization is refused", async ({ page, request }) => {
    await openSession(page, "org_admin");

    await openCard(page, EARLY);
    await expect(page.getByTestId("category-note-chain")).toContainText(LATE);
    await expect(page.getByTestId("toggle-btn")).toHaveCount(0);
    await expect(page.getByTestId("qty-change")).toHaveCount(0);
    await expect(page.getByTestId("window-edit-start")).toHaveCount(0);
    await page.getByTestId("price-change").click();
    await page.getByTestId("price-input").fill("12");
    await page.getByTestId("price-check").click();
    await page.getByTestId("price-save").click();
    await expect(page.getByTestId("category-notice")).toContainText("12.00");
    expect((await apiTier(request, EARLY)).price_amount).toBe(1200);

    await openCard(page, LATE);
    await expect(page.getByTestId("category-note-chain")).toBeVisible();
    await expect(page.getByTestId("toggle-btn")).toHaveCount(0);
    await expect(page.getByTestId("price-change")).toBeVisible();

    // A forbidden press is also refused by the API only through the screen's absence of it:
    // the places of the chain were not touched.
    const chain = rows();
    expect(chain[EARLY].places).toBe(5);
    expect(chain[LATE].places).toBe(5);

    await openCard(page, FREE);
    await expect(page.getByTestId("price-change")).toHaveCount(0);
    await expect(page.getByTestId("category-price")).toHaveCount(0);
    await expect(page.getByTestId("category-card")).toContainText("it has no price");
    await expect(page.getByTestId("toggle-btn")).toBeVisible();
    await expect(page.getByTestId("qty-change")).toBeVisible();

    // The same ids under another organization's path change nothing.
    const free = await apiTier(request, FREE);
    const foreign = await request.patch(`${tiersUrl(FOREIGN_ORG)}/${free.id}`, { headers: auth("org_admin"), data: { is_open: false } });
    // A non-member of the path organization stops at the permission gate (PERM-ORG): 403.
    expect(foreign.status(), "a foreign organization's path").toBe(403);
    expect((await apiTier(request, FREE)).is_open).toBe(true);

    // A foreign ROW under the caller's own path is the route's own 404, and nothing changes.
    for (const role of ["org_admin", "organizer"] as const) {
      const row = await request.patch(
        `${base}/events/${FOREIGN_EVENT}/sessions/${FOREIGN_SESSION}/tiers/${FOREIGN_TIER}`,
        { headers: auth(role), data: { is_open: false, capacity: 1 } },
      );
      expect(row.status(), `PATCH another organization's category as ${role}`).toBe(404);
      const sched = await request.get(
        `${base}/events/${FOREIGN_EVENT}/sessions/${FOREIGN_SESSION}/tiers/${FOREIGN_TIER}/price-schedule`,
        { headers: auth(role) },
      );
      expect(sched.status(), `GET another organization's price schedule as ${role}`).toBe(404);
    }
    const untouched = seed("inspect")["E2E Foreign Category"];
    expect(untouched.is_open).toBe(true);
    expect(untouched.capacity).toBeNull();
  });

  test("manager (organizer) can change categories like the owner", async ({ page, request }) => {
    const me = await orgMe(request, "organizer");
    expect(me.permissions).toContain("tier.read");
    expect(me.permissions).toContain("tier.update");

    await openSession(page, "organizer");
    await openCard(page, FREE);
    await page.getByTestId("toggle-btn").click();
    await expect(page.getByTestId("category-notice")).toContainText("Closed");
    expect((await apiTier(request, FREE, "organizer")).is_open).toBe(false);
    await page.getByTestId("toggle-btn").click();
    await expect(page.getByTestId("category-notice")).toContainText("Open again");
    expect((await apiTier(request, FREE, "organizer")).is_open).toBe(true);

    await openCard(page, LATE);
    await page.getByTestId("price-change").click();
    await page.getByTestId("price-input").fill("16");
    await page.getByTestId("price-check").click();
    await page.getByTestId("price-save").click();
    await expect(page.getByTestId("category-notice")).toContainText("16.00");
    expect((await apiTier(request, LATE, "organizer")).price_amount).toBe(1600);
  });

  test("agent: the screen follows what the server allows, and the API refuses the rest", async ({ page, request }) => {
    // The seed's truth: the agent role holds no category rights at all.
    const me = await orgMe(request, "agent");
    expect(me.permissions).not.toContain("tier.update");
    expect(me.permissions).not.toContain("tier.read");
    const free = await apiTier(request, FREE);

    const patch = await request.patch(`${tiersUrl()}/${free.id}`, { headers: auth("agent"), data: { is_open: false } });
    expect(patch.status(), "PATCH as agent").toBe(403);
    const list = await request.get(tiersUrl(), { headers: auth("agent") });
    expect(list.status(), "GET tiers as agent").toBe(403);
    expect((await apiTier(request, FREE)).is_open, "the category was not changed by an agent").toBe(true);

    await signInAs(page, person(env, "agent"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}`);
    await expect(page.getByTestId("tiers-table")).toHaveCount(0);
    await expect(page.getByTestId("tier-manage")).toHaveCount(0);
    await expect(page.getByTestId("category-card")).toHaveCount(0);
  });
});
