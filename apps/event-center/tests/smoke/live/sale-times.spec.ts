/**
 * Twin test "sales end and doors-open time of a session" (task 5-3), on the
 * three roles of one organization, against a REAL arena-api. It walks the
 * bot's scenario (sessions_sale_times.go, `ses:se`, `ses:dr`): the sales end
 * moves with one press, the doors time is a clock time on the venue's clock,
 * doors after the start are refused, the doors time can be removed, and a
 * person who may not change a session sees no editor.
 *
 * The truth about each person comes from GET /v1/organizations/{org_id}/me.
 * The database row is read through the seed binary's `--sale-times-op
 * inspect` (this process has no SQL driver); `reset` puts the date back.
 * The date is the fixture's own ("E2E Sale Times Show"), so the other twins
 * never see it change.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const EVENT_ID = "fe0000e2-0000-7000-8000-00000000e301";
const SESSION_ID = "fe0000e2-0000-7000-8000-00000000e302";
const ZONE = "Europe/Madrid";

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

interface Rows {
  start_at: string;
  end_at: string;
  sales_end_at: string;
  doors_open_at: string | null;
}

function seed(op: "reset" | "inspect"): Record<string, unknown> {
  const args = ["--sale-times-op", op];
  const r = spawnSync(seedBinary(), args, { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed ${args.join(" ")} failed: ${r.stderr}`);
  return JSON.parse(r.stdout.trim()) as Record<string, unknown>;
}

const rows = (): Rows => seed("inspect") as unknown as Rows;

/** The instant `minutes` after (negative: before) another, as RFC 3339 with whole seconds. */
const shift = (iso: string, minutes: number) =>
  new Date(new Date(iso).getTime() + minutes * 60_000).toISOString().replace(/\.\d{3}Z$/, "Z");

/** "HH:MM" on the venue's wall clock (the fixture venue is in Madrid). */
function wallClock(iso: string): string {
  return new Intl.DateTimeFormat("en-GB", { timeZone: ZONE, hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).format(new Date(iso));
}

const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const sessionUrl = `${base}/events/${EVENT_ID}/sessions/${SESSION_ID}`;

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${base}/me`, { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

async function openSession(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/events/${EVENT_ID}/sessions/${SESSION_ID}`);
}

test.describe("sales end and doors (live, three roles)", () => {
  test.beforeAll(() => {
    seed("reset");
  });

  test("owner: sales end one hour before the start, doors by clock time, doors after the start refused", async ({ page, request }) => {
    const me = await orgMe(request, "org_admin");
    expect(me.permissions).toContain("session.update");
    const start = rows().start_at;

    const patches: string[] = [];
    page.on("request", (req) => {
      if (req.method() === "PATCH" && req.url().startsWith(env.api_base_url)) patches.push(new URL(req.url()).pathname);
    });

    await openSession(page, "org_admin");
    await expect(page.getByTestId("sale-times")).toBeVisible();
    await expect(page.getByTestId("sales-end-value")).toContainText("the start of the session");
    await expect(page.getByTestId("doors-value")).toHaveText("Not set");
    await noA11yViolations(page);

    // Sales end earlier than the start: the preset "1 hour before".
    await page.getByTestId("sales-end-change").click();
    await page.getByTestId("sales-end-preset-before1h").click();
    await expect(page.getByTestId("sales-end-time")).toHaveValue(wallClock(shift(start, -60)));
    await page.getByTestId("sales-end-save").click();
    await expect(page.getByTestId("sales-end-done")).toHaveText("Saved.");
    expect(rows().sales_end_at).toBe(shift(start, -60));
    expect(rows().doors_open_at, "doors untouched by a sales end save").toBeNull();
    await expect(page.getByTestId("sales-end-value")).not.toContainText("the start of the session");

    // Doors: a typed clock time on the venue's clock, 90 minutes before the start.
    await page.getByTestId("doors-change").click();
    await page.getByTestId("doors-time").fill(wallClock(shift(start, -90)));
    await page.getByTestId("doors-save").click();
    await expect(page.getByTestId("doors-done")).toHaveText("Saved.");
    expect(rows().doors_open_at).toBe(shift(start, -90));
    expect(rows().sales_end_at, "sales end untouched by a doors save").toBe(shift(start, -60));

    // Doors after the start: told in words, no call is sent, the row keeps its value.
    const before = patches.length;
    await page.getByTestId("doors-change").click();
    await page.getByTestId("doors-time").fill(wallClock(shift(start, 60)));
    await page.getByTestId("doors-save").click();
    await expect(page.getByTestId("doors-problem")).toContainText("Doors cannot open after the session starts");
    expect(patches.length).toBe(before);
    expect(rows().doors_open_at).toBe(shift(start, -90));
    await page.getByTestId("doors-cancel").click();

    // The server says the same when the screen is bypassed.
    const res = await request.patch(sessionUrl, { headers: auth("org_admin"), data: { doors_open_at: shift(start, 60) } });
    expect(res.status()).toBe(400);
    expect(((await res.json()) as { error: { code: string } }).error.code).toBe("session.doors_after_start");
  });

  test("manager: sees the owner's times and removes the doors time", async ({ page, request }) => {
    const me = await orgMe(request, "organizer");
    expect(me.permissions).toContain("session.update");
    const start = rows().start_at;

    await openSession(page, "organizer");
    await expect(page.getByTestId("sale-times")).toBeVisible();
    await expect(page.getByTestId("doors-value")).toContainText(wallClock(shift(start, -90)));
    await page.getByTestId("doors-remove").click();
    await expect(page.getByTestId("doors-value")).toHaveText("Not set");
    expect(rows().doors_open_at).toBeNull();
    expect(rows().sales_end_at).toBe(shift(start, -60));

    // The sales end is put back on the start with the "At the start" preset.
    await page.getByTestId("sales-end-change").click();
    await page.getByTestId("sales-end-preset-atStart").click();
    await page.getByTestId("sales-end-save").click();
    await expect(page.getByTestId("sales-end-done")).toHaveText("Saved.");
    expect(rows().sales_end_at).toBe(start);
  });

  test("agent: no editor, and the API refuses the change", async ({ page, request }) => {
    const me = await orgMe(request, "agent");
    expect(me.permissions).not.toContain("session.update");
    const start = rows().start_at;

    const patches: string[] = [];
    page.on("request", (req) => {
      if (req.method() === "PATCH" && req.url().startsWith(env.api_base_url)) patches.push(req.url());
    });
    await openSession(page, "agent");
    await expect(page.getByTestId("sale-times")).toHaveCount(0);
    expect(patches).toHaveLength(0);

    const res = await request.patch(sessionUrl, { headers: auth("agent"), data: { sales_end_at: shift(start, -60) } });
    expect(res.status()).toBe(403);
    expect(rows().sales_end_at).toBe(start);
  });
});
