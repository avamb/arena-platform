/**
 * Twin test "the hall of a seated date: paint seats with categories, close and
 * open them, lay the hall out automatically" (tasks S2b and S2c), against a
 * REAL arena-api, on the three roles of one organization.
 *
 * The date is the seed's "E2E Hall Show" (seats_fixture.go): 102 real seats,
 * three sold and two in a hold. The seed binary's `--seats-op` puts the hall
 * back before the run, reads what the API does not show (the tier and status of
 * every seat) and sells one seat behind the organizer's back.
 *
 * The map is touched the way a finger does it: a tap on a seat, a tap on a row
 * button. Nothing here relies on hover.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const EVENT = "fe0000e2-0000-7000-8000-00000000e601";
const SESSION = "fe0000e2-0000-7000-8000-00000000e602";

const PREMIUM = "E2E Hall Premium";
const STANDARD = "E2E Hall Standard";
const ECONOMY = "E2E Hall Economy";

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

interface SeatRow {
  tier: string;
  status: string;
}
interface Hall {
  seats: Record<string, SeatRow>;
  batches: number;
}

function seed(op: "reset" | "inspect" | "sell", key?: string): unknown {
  const args = ["--seats-op", op];
  if (key !== undefined) args.push("--seats-key", key);
  const r = spawnSync(seedBinary(), args, { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed ${args.join(" ")} failed: ${r.stderr}`);
  return JSON.parse(r.stdout.trim());
}
const hall = () => seed("inspect") as Hall;

const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const orgBase = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const batchesUrl = `${orgBase}/sessions/${SESSION}/seat-batches`;

async function can(request: APIRequestContext, role: Role, permission: string): Promise<boolean> {
  const res = await request.get(`${orgBase}/me`, { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return ((await res.json()) as { permissions: string[] }).permissions.includes(permission);
}

const seatsPath = `/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}/seats`;

async function openHall(page: Page, role: Role, search = "") {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(seatsPath + search);
}

const seatNode = (page: Page, key: string) => page.locator(`[data-k="${key}"]`);
const brush = (page: Page, tier: string) => page.locator(`[data-testid="seat-brush"][data-tier-name="${tier}"]`);
const trayCount = (page: Page) => page.getByTestId("seat-tray-count");
/** Row n (1-based) of the Parter in the rows list: the list follows the stored order of the sections, not the map. */
const parterRow = (page: Page, n: number) =>
  page.locator('[data-testid="seat-rows"] section[aria-label="Parter"]').getByTestId("seat-row-apply").nth(n - 1);

async function pickBrush(page: Page, tier: string) {
  await page.getByTestId("seat-tab-brush").click();
  await page.getByTestId("seat-mode-categories").click();
  await brush(page, tier).click();
  await expect(brush(page, tier)).toHaveAttribute("aria-pressed", "true");
}

test.beforeAll(() => {
  seed("reset");
});

test.describe("the hall of a seated date (live, three roles)", () => {
  test("the screen follows the right to change seats, and the API refuses what the screen hides", async ({ page, request }) => {
    const roles: Role[] = ["org_admin", "organizer", "agent"];
    for (const role of roles) {
      const allowed = await can(request, role, "session.seats.manage");
      const res = await request.post(batchesUrl, {
        headers: auth(role),
        data: { kind: "close", changes: [{ seat_key: "B|3|10", status: "unavailable" }] },
      });
      if (allowed) expect(res.status(), `${role} may change seats`).toBeLessThan(300);
      else expect(res.status(), `${role} may not change seats`).toBe(403);
    }
    // The probe above closed one seat for the allowed roles: put the hall back.
    seed("reset");

    for (const role of roles) {
      const allowed = await can(request, role, "session.seats.manage");
      await openHall(page, role);
      if (allowed) {
        await expect(page.getByTestId("seat-manager")).toBeVisible();
        await expect(page.getByTestId("seat-summary")).toContainText("102 seats");
      } else {
        await expect(page.getByTestId("seats-no-access")).toBeVisible();
        await expect(page.getByTestId("seat-manager")).toHaveCount(0);
      }
      await page.context().clearCookies();
      await page.evaluate(() => {
        sessionStorage.clear();
        localStorage.clear();
      });
    }
  });

  test("owner: the date card links to the hall, sold and held seats are shown and counted", async ({ page }) => {
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}`);
    await page.getByTestId("session-seats-link").click();
    await expect(page).toHaveURL(new RegExp(`${SESSION}/seats`));
    await expect(page.getByTestId("seat-manager")).toBeVisible();
    await expect(page.getByTestId("seat-summary")).toContainText("102 seats");
    await expect(page.getByTestId("seat-summary")).toContainText("sold 3");
    await expect(page.getByTestId("seat-summary")).toContainText("in a hold 2");
    // Three categories, each with its price and how many seats are free.
    await expect(page.getByTestId("seat-brush")).toHaveCount(3);
    await expect(brush(page, PREMIUM)).toContainText("50.00");
  });

  test("owner: brush on a seat and a row, sold seats are skipped, ONE atomic batch, history, undo", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await openHall(page, "org_admin");
    await expect(page.getByTestId("seat-map")).toBeVisible();
    const before = hall();
    expect(before.batches).toBe(0);
    expect(before.seats["P|1|1"]).toEqual({ tier: PREMIUM, status: "sold" });

    await pickBrush(page, ECONOMY);
    await page.getByTestId("seat-tool-seat").click();

    // One tap on a free seat of row 6.
    await seatNode(page, "P|6|1").click();
    await expect(trayCount(page)).toContainText("1 seats changed");
    // Nothing reached the server yet.
    expect(hall().seats["P|6|1"].tier).toBe(STANDARD);

    // A tap on a SOLD seat is refused, and the screen says so.
    await seatNode(page, "P|1|1").click();
    await expect(page.getByTestId("seat-note")).toContainText("skipped");
    await expect(trayCount(page)).toContainText("1 seats changed");

    // A whole row from the list: row 3 of the Parter (12 seats, all Standard).
    await page.getByTestId("seat-tab-rows").click();
    await parterRow(page, 3).click();
    await expect(trayCount(page)).toContainText("13 seats changed");

    // The undo takes the last stroke back, the reset takes everything.
    await page.getByTestId("seat-undo").click();
    await expect(trayCount(page)).toContainText("1 seats changed");
    await parterRow(page, 3).click();
    await expect(trayCount(page)).toContainText("13 seats changed");

    // The changes tab reads like the tray.
    await page.getByTestId("seat-tab-changes").click();
    await expect(page.getByTestId("seat-changes")).toContainText("13");

    await page.getByTestId("seat-apply").click();
    await expect(page.getByTestId("seat-note")).toContainText("Applied. 13 seats changed");
    await expect(trayCount(page)).toContainText("No changes yet");

    const after = hall();
    expect(after.batches).toBe(1);
    expect(after.seats["P|3|1"].tier).toBe(ECONOMY);
    expect(after.seats["P|3|12"].tier).toBe(ECONOMY);
    expect(after.seats["P|6|1"].tier).toBe(ECONOMY);
    expect(after.seats["P|6|2"].tier).toBe(STANDARD);
    // The sold seat did not move.
    expect(after.seats["P|1|1"]).toEqual({ tier: PREMIUM, status: "sold" });

    // History: the batch is listed and one press puts every seat back.
    await page.getByTestId("seat-tab-history").click();
    await expect(page.getByTestId("seat-history-item")).toHaveCount(1);
    await expect(page.getByTestId("seat-history-item")).toHaveAttribute("data-kind", "paint");
    await page.getByTestId("seat-revert").click();
    await expect(page.getByTestId("seat-note")).toContainText("Undone");
    const reverted = hall();
    expect(reverted.batches).toBe(2);
    expect(reverted.seats["P|3|1"].tier).toBe(STANDARD);
    expect(reverted.seats["P|6|1"].tier).toBe(STANDARD);
    // The undone batch cannot be undone again.
    await expect(page.locator('[data-testid="seat-history-item"][data-reverted="1"]')).toHaveCount(1);
    await expect(page.getByTestId("seat-revert")).toHaveCount(0);
  });

  test("owner: sale mode closes and opens seats, never a sold one", async ({ page }) => {
    seed("reset");
    await page.setViewportSize({ width: 1280, height: 900 });
    await openHall(page, "org_admin");
    await expect(page.getByTestId("seat-map")).toBeVisible();

    await page.getByTestId("seat-tab-brush").click();
    await page.getByTestId("seat-mode-sale").click();
    await page.getByTestId("seat-brush-close").click();
    // Row 1 has three sold seats: only nine close.
    await page.getByTestId("seat-tab-rows").click();
    await parterRow(page, 1).click();
    await expect(trayCount(page)).toContainText("9 seats changed");
    await page.getByTestId("seat-apply").click();
    await expect(page.getByTestId("seat-note")).toContainText("Applied. 9 seats changed");
    let h = hall();
    expect(h.seats["P|1|4"].status).toBe("unavailable");
    expect(h.seats["P|1|1"].status).toBe("sold");
    await expect(page.getByTestId("seat-summary")).toContainText("closed 9");

    await page.getByTestId("seat-tab-brush").click();
    await page.getByTestId("seat-brush-open").click();
    await page.getByTestId("seat-tab-rows").click();
    await parterRow(page, 1).click();
    await expect(trayCount(page)).toContainText("9 seats changed");
    await page.getByTestId("seat-apply").click();
    await expect(page.getByTestId("seat-note")).toContainText("Applied. 9 seats changed");
    h = hall();
    expect(h.seats["P|1|4"].status).toBe("available");
    expect(h.seats["P|1|1"].status).toBe("sold");
    await expect(page.getByTestId("seat-summary")).toContainText("closed 0");
  });

  test("owner: a seat sold after it went into the tray is skipped, the rest of the batch still goes", async ({ page }) => {
    seed("reset");
    await page.setViewportSize({ width: 1280, height: 900 });
    await openHall(page, "org_admin");
    await expect(page.getByTestId("seat-map")).toBeVisible();
    await pickBrush(page, ECONOMY);
    await page.getByTestId("seat-tool-seat").click();
    await seatNode(page, "P|5|2").click();
    await seatNode(page, "P|5|3").click();
    await expect(trayCount(page)).toContainText("2 seats changed");

    // A buyer takes seat P|5|2 while the organizer is still thinking.
    seed("sell", "P|5|2");
    await page.getByTestId("seat-apply").click();
    await expect(page.getByTestId("seat-note")).toContainText("skipped");
    const h = hall();
    expect(h.seats["P|5|2"]).toEqual({ tier: STANDARD, status: "sold" });
    expect(h.seats["P|5|3"].tier).toBe(ECONOMY);
  });

  test("owner: automatic layout: the server's algorithms, a preview, the revenue of what is sold, a transfer into the tray", async ({ page }) => {
    test.setTimeout(60_000);
    seed("reset");
    await page.setViewportSize({ width: 1280, height: 900 });
    await openHall(page, "org_admin");
    await expect(page.getByTestId("seat-map")).toBeVisible();
    await page.getByTestId("seat-tab-layout").click();
    await expect(page.getByTestId("seat-layout")).toBeVisible();

    // The algorithm that needs the stage asks for it once, and nothing is calculated before.
    await expect(page.getByTestId("layout-stage")).toBeVisible();
    await expect(page.getByTestId("layout-proposal")).toHaveCount(0);
    await page.getByTestId("layout-stage-btn").click();
    await expect(page.getByTestId("seat-stage-banner")).toBeVisible();
    const map = page.getByTestId("seat-map");
    const box = await map.boundingBox();
    if (box === null) throw new Error("the map has no box");
    await map.click({ position: { x: box.width / 2, y: 12 } });
    await expect(page.getByTestId("seat-stage-banner")).toHaveCount(0);

    // The preview arrives by itself after a pause.
    await expect(page.getByTestId("layout-proposal")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("layout-revenue")).toContainText("Sold:");
    await expect(page.getByTestId("layout-revenue")).toContainText("3 seats");
    expect(await page.getByTestId("layout-slot").count()).toBeGreaterThan(0);
    // Nothing is written by a preview.
    expect(hall().batches).toBe(0);

    await page.getByTestId("layout-transfer").click();
    await expect(page.getByTestId("seat-tab-changes")).toHaveAttribute("aria-selected", "true");
    await expect(trayCount(page)).toContainText("seats changed");
    expect(hall().batches).toBe(0);

    await page.getByTestId("seat-apply").click();
    await expect(page.getByTestId("seat-note")).toContainText("Applied");
    const h = hall();
    expect(h.batches).toBe(1);
    // Sold and held seats keep their category, whatever the layout wanted.
    for (const key of ["P|1|1", "P|1|2", "P|1|3"]) expect(h.seats[key]).toEqual({ tier: PREMIUM, status: "sold" });
    for (const key of ["P|2|1", "P|2|2"]) expect(h.seats[key]).toEqual({ tier: PREMIUM, status: "held" });
  });

  test("embedded in Telegram (?embed=1): no page header, no way back of our own", async ({ page }) => {
    seed("reset");
    await openHall(page, "org_admin", "?embed=1");
    await expect(page.getByTestId("seat-manager")).toBeVisible();
    await expect(page.getByTestId("seats-back")).toHaveCount(0);
    await expect(page.getByTestId("seat-summary")).toHaveCount(0);
    await expect(page.locator("header")).toHaveCount(0);
  });

  test("phone: the panel is a sheet, nothing scrolls sideways, zoom works without a hover", async ({ page }) => {
    seed("reset");
    await page.setViewportSize({ width: 375, height: 812 });
    await openHall(page, "org_admin");
    await expect(page.getByTestId("seat-map")).toBeVisible();
    await expect(page.getByTestId("seat-panel")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
    await noA11yViolations(page);

    const before = Number(await page.getByTestId("seat-map").getAttribute("data-scale"));
    await page.getByTestId("seat-zoom-in").click();
    await expect.poll(async () => Number(await page.getByTestId("seat-map").getAttribute("data-scale"))).toBeGreaterThan(before);

    // The sheet folds away, so the whole hall can be touched.
    await page.getByTestId("seat-collapse").click();
    await expect(page.getByTestId("seat-panel")).toHaveClass(/sm-panel-collapsed/);
    await page.getByTestId("seat-collapse").click();
    await expect(page.getByTestId("seat-panel")).not.toHaveClass(/sm-panel-collapsed/);

    // A finger-sized control: every brush is at least 44 px tall.
    const tall = await page.getByTestId("seat-brush").first().boundingBox();
    expect(tall?.height ?? 0).toBeGreaterThanOrEqual(44);
  });
});
