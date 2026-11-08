/**
 * Twin test "sales state of a session" (migration 0183), on the three roles
 * of one organization, against a REAL arena-api. It walks the bot's scenario
 * (sessions_sales_state.go, `ses:ss:`, `ses:ssgo`): the owner closes the sale
 * as "sold out" with one confirmed press, the manager sees who did it and
 * opens the sale again, the journal names both and the web as the client, and
 * a person who may not change a session gets no button and a 403 from the API.
 *
 * The truth comes from the API itself (GET .../sales-state and its history).
 * The seed binary's `--sales-state-op reset` puts the date back (no state, five
 * free places). The date is the fixture's own ("E2E Sales State Show"), so a
 * "sold out" here never refuses a hold another twin takes.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const EVENT_ID = "fe0000e2-0000-7000-8000-00000000e701";
const SESSION_ID = "fe0000e2-0000-7000-8000-00000000e702";

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
  const r = spawnSync(seedBinary(), ["--sales-state-op", "reset"], { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed --sales-state-op reset failed: ${r.stderr}`);
}

const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const stateUrl = `${base}/sessions/${SESSION_ID}/sales-state`;

interface State {
  manual: string | null;
  effective: string;
  free_places: number;
  changed_by: { name: string; role: string; source: string } | null;
}
interface History {
  items: { old_state: string; new_state: string; changed_by: { role: string; source: string } }[];
}

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${base}/me`, { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

async function readState(request: APIRequestContext): Promise<State> {
  const res = await request.get(stateUrl, { headers: auth("org_admin") });
  expect(res.status()).toBe(200);
  return (await res.json()) as State;
}

async function readHistory(request: APIRequestContext): Promise<History> {
  const res = await request.get(`${stateUrl}/history`, { headers: auth("org_admin") });
  expect(res.status()).toBe(200);
  return (await res.json()) as History;
}

async function openSession(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/events/${EVENT_ID}/sessions/${SESSION_ID}`);
}

test.describe("sales state (live, three roles)", () => {
  test.beforeAll(() => {
    reset();
  });
  test.afterAll(() => {
    reset();
  });

  test("owner: closes the sale as sold out with one confirmed press", async ({ page, request }) => {
    const me = await orgMe(request, "org_admin");
    expect(me.permissions).toContain("session.update");
    expect((await readState(request)).effective).toBe("open");

    await openSession(page, "org_admin");
    const block = page.getByTestId("sales-state");
    await expect(block).toBeVisible();
    await expect(page.getByTestId("sales-state-value")).toContainText("Open");
    await expect(page.getByTestId("sales-state-origin")).toContainText("Set by the platform");
    await expect(page.getByTestId("sales-state-places")).toContainText("Free places: 5");
    await expect(page.getByTestId("sales-state-set-open")).toHaveAttribute("aria-checked", "true");
    await noA11yViolations(page);

    // "Cancel" in the dialog writes nothing.
    await page.getByTestId("sales-state-set-sold_out").click();
    await expect(page.getByTestId("sales-state-confirm")).toBeVisible();
    await page.getByTestId("sales-state-confirm-cancel").click();
    expect((await readState(request)).manual).toBeNull();

    await page.getByTestId("sales-state-set-sold_out").click();
    await expect(page.getByTestId("sales-state-confirm")).toContainText("even if places are left");
    await page.getByTestId("sales-state-confirm-submit").click();
    await expect(page.getByTestId("sales-state-done")).toHaveText("Done. Sales state: Sold out.");
    await expect(page.getByTestId("sales-state-origin")).toContainText("Set by hand");

    const st = await readState(request);
    expect(st.manual).toBe("sold_out");
    expect(st.free_places, "the state never rewrites the places").toBe(5);
    expect(st.changed_by?.source).toBe("event_center_web");
    expect(st.changed_by?.role).toBe("org_admin");
  });

  test("manager: sees who closed it, reads the journal and opens the sale again", async ({ page, request }) => {
    const me = await orgMe(request, "organizer");
    expect(me.permissions).toContain("session.update");

    await openSession(page, "organizer");
    await expect(page.getByTestId("sales-state-value")).toContainText("Sold out");
    await expect(page.getByTestId("sales-state-changed-by")).toContainText("the event center");

    await page.getByTestId("sales-state-history-toggle").click();
    await expect(page.getByTestId("sales-state-history-row")).toHaveCount(1);
    await expect(page.getByTestId("sales-state-history-row").first()).toContainText("Open → Sold out");

    await page.getByTestId("sales-state-set-open").click();
    await page.getByTestId("sales-state-confirm-submit").click();
    await expect(page.getByTestId("sales-state-done")).toHaveText("Done. Sales state: Open.");
    await expect(page.getByTestId("sales-state-history-row")).toHaveCount(2);

    expect((await readState(request)).manual).toBeNull();
    const h = await readHistory(request);
    expect(h.items.map((i) => `${i.old_state}>${i.new_state}`)).toEqual(["sold_out>open", "open>sold_out"]);
    expect(h.items[0].changed_by.role).toBe("organizer");
    expect(h.items.every((i) => i.changed_by.source === "event_center_web")).toBe(true);
  });

  test("agent: no button, and the API refuses the change", async ({ page, request }) => {
    const me = await orgMe(request, "agent");
    expect(me.permissions).not.toContain("session.update");

    const puts: string[] = [];
    page.on("request", (req) => {
      if (req.method() === "PUT" && req.url().startsWith(env.api_base_url)) puts.push(req.url());
    });
    await openSession(page, "agent");
    if (me.permissions.includes("session.read")) {
      await expect(page.getByTestId("sales-state-value")).toContainText("Open");
    }
    await expect(page.locator('[data-testid^="sales-state-set-"][aria-disabled="true"]')).toHaveCount(3);
    expect(puts).toHaveLength(0);

    const res = await request.put(stateUrl, { headers: auth("agent"), data: { state: "sold_out" } });
    expect(res.status()).toBe(403);
    expect((await readState(request)).manual).toBeNull();
  });
});
