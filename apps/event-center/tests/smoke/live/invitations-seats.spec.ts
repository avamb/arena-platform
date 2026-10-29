/**
 * Twin test "invitations on the seat map" (spec 46, INV-04) against a REAL
 * arena-api, on the three roles of one organization.
 *
 * The date is the seed's "E2E Hall Show" (seats_fixture.go), the same one the
 * seat manager twin uses: Parter rows 1-6 x 12 seats, row 1 has three sold
 * seats and row 2 two in a hold. The seed binary's `--seats-op` puts the hall
 * back before every test, reads the status of every seat (the API shows it
 * only to the seat manager) and sells one seat behind the organizer's back.
 *
 * What is proven here, through the browser and the real API:
 *   - a seat the organizer HOLDS BACK (closed for sale) is picked on the map
 *     and given away: it becomes sold in the same call, never open on the way;
 *   - a sold seat cannot be picked, and the screen says so;
 *   - a seat taken meanwhile comes back as 409, leaves the pick, is outlined,
 *     and the typed form stays;
 *   - each role sees exactly what the server lets it do.
 *
 * NOT covered, on purpose: the cancel leg (cancel the invitation, the seat is
 * closed again). Tickets are minted by arena-worker (`checkout.issue_tickets`)
 * and this harness runs arena-api only, so a fresh invitation stays "issuing"
 * and the server answers 409 `invitation.tickets_not_issued` to a cancel. The
 * leg is written below and runs when ARENA_E2E_WORKER=1 says the API under test
 * has a worker behind it. The server side of the cancel (the seat goes back to
 * where it came from) is proven by staff_invitations_inv02_integration_test.go.
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

interface Hall {
  seats: Record<string, { tier: string; status: string }>;
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
const invitationsUrl = `${orgBase}/sessions/${SESSION}/invitations`;

async function permissions(request: APIRequestContext, role: Role): Promise<string[]> {
  const res = await request.get(`${orgBase}/me`, { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return ((await res.json()) as { permissions: string[] }).permissions;
}

/** Closes seats for sale the way the seat manager does: one batch. */
async function closeSeats(request: APIRequestContext, keys: string[]) {
  const res = await request.post(`${orgBase}/sessions/${SESSION}/seat-batches`, {
    headers: auth("org_admin"),
    data: { kind: "close", changes: keys.map((seat_key) => ({ seat_key, status: "unavailable" })) },
  });
  expect(res.status(), "close seats").toBeLessThan(300);
}

const page_ = `/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}/invitations`;

async function open(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(page_);
}

const seatNode = (page: Page, key: string) => page.locator(`[data-k="${key}"]`);
const picked = (page: Page) => page.getByTestId("inv-picked-seat");

async function fillGuest(page: Page, name: string) {
  await page.getByTestId("inv-name").fill(name);
  await page.getByTestId("inv-delivery-pdf_only").check();
}

test.beforeAll(() => {
  seed("reset");
});

test.describe("invitations on the seat map (live, three roles)", () => {
  test("each role sees what the server lets it do, and the API refuses what the screen hides", async ({ page, request }) => {
    for (const role of ["org_admin", "organizer", "agent"] as Role[]) {
      const can = new Set(await permissions(request, role));
      const post = await request.post(invitationsUrl, {
        headers: auth(role),
        data: { request_id: "00000000-0000-4000-8000-000000000001", seats: [], ga: [], guest: { name: "x" }, delivery: "pdf_only" },
      });
      // An empty invitation is refused (400) by a role that may issue, and by NOBODY ELSE with anything but 403.
      if (can.has("complimentary.issue")) expect(post.status(), `${role} may issue`).toBe(400);
      else expect(post.status(), `${role} may not issue`).toBe(403);

      await open(page, role);
      if (can.has("complimentary.issue") || can.has("complimentary.read")) {
        await expect(page.getByTestId("inv-no-access")).toHaveCount(0);
        if (can.has("complimentary.issue")) await expect(page.getByTestId("inv-issue")).toBeVisible();
        else await expect(page.getByTestId("inv-issue")).toHaveCount(0);
        if (can.has("complimentary.read")) await expect(page.getByTestId("inv-list")).toBeVisible();
        else await expect(page.getByTestId("inv-list")).toHaveCount(0);
      } else {
        await expect(page.getByTestId("inv-no-access")).toBeVisible();
        await expect(page.getByTestId("inv-issue")).toHaveCount(0);
      }
      await page.context().clearCookies();
      await page.evaluate(() => {
        sessionStorage.clear();
        localStorage.clear();
      });
    }
  });

  test("owner: the date page links to the screen", async ({ page }) => {
    seed("reset");
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}`);
    await page.getByTestId("session-invitations-link").click();
    await expect(page).toHaveURL(new RegExp(`${SESSION}/invitations`));
    await expect(page.getByTestId("inv-issue")).toBeVisible();
  });

  test("owner: a seat held back is given away, a sold one is refused, the seat is sold in the same call", async ({ page, request }) => {
    seed("reset");
    await closeSeats(request, ["P|3|5"]);
    expect(hall().seats["P|3|5"].status).toBe("unavailable");

    await page.setViewportSize({ width: 1280, height: 900 });
    await open(page, "org_admin");
    await expect(page.getByTestId("inv-hall")).toBeVisible();

    // A sold seat: refused, the pick stays empty.
    await seatNode(page, "P|1|1").click();
    await expect(page.getByTestId("inv-note")).toContainText("cannot be given away");
    await expect(picked(page)).toHaveCount(0);

    // The seat held back and a free one.
    await seatNode(page, "P|3|5").click();
    await seatNode(page, "P|3|6").click();
    await expect(picked(page)).toHaveCount(2);
    await expect(page.locator('[data-testid="inv-picked-seat"][data-seat="P|3|5"]')).toHaveAttribute("data-origin", "unavailable");
    await expect(page.getByTestId("inv-from-reserve")).toContainText("1");

    // Without a name nothing is sent, and the form says why.
    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-name-error")).toBeVisible();
    expect(hall().seats["P|3|5"].status).toBe("unavailable");

    await fillGuest(page, "Ana López");
    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-result")).toContainText("2 places for Ana López");
    await expect(page.getByTestId("inv-result")).toContainText("From your reserve: 1");

    const after = hall();
    expect(after.seats["P|3|5"].status).toBe("sold");
    expect(after.seats["P|3|6"].status).toBe("sold");
    // The pick is empty again and the list shows the invitation (tickets are minted by the worker, so it is still issuing).
    await expect(picked(page)).toHaveCount(0);
    await expect(page.getByTestId("inv-row")).toHaveCount(1);
    await expect(page.getByTestId("inv-row")).toContainText("Ana López");
  });

  test("owner: a seat taken meanwhile leaves the pick, is outlined, and the form stays", async ({ page }) => {
    seed("reset");
    await page.setViewportSize({ width: 1280, height: 900 });
    await open(page, "org_admin");
    await expect(page.getByTestId("inv-hall")).toBeVisible();

    await seatNode(page, "P|5|2").click();
    await seatNode(page, "P|5|3").click();
    await fillGuest(page, "Carlos Ruiz");
    await page.getByTestId("inv-email").fill("carlos@example.com");
    await expect(picked(page)).toHaveCount(2);

    // A buyer takes P|5|2 while the organizer is still typing.
    seed("sell", "P|5|2");
    await page.getByTestId("inv-submit").click();

    await expect(page.getByTestId("inv-conflict")).toBeVisible();
    await expect(picked(page)).toHaveCount(1);
    await expect(page.locator('[data-testid="inv-picked-seat"][data-seat="P|5|3"]')).toBeVisible();
    // The form is as it was typed.
    await expect(page.getByTestId("inv-name")).toHaveValue("Carlos Ruiz");
    await expect(page.getByTestId("inv-email")).toHaveValue("carlos@example.com");
    await expect(page.getByTestId("inv-delivery-pdf_only")).toBeChecked();
    // Nothing was written for the seat that is still free.
    expect(hall().seats["P|5|3"].status).toBe("available");

    // Picking again clears the notice, and the remaining seat goes through.
    await seatNode(page, "P|5|4").click();
    await expect(page.getByTestId("inv-conflict")).toHaveCount(0);
    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-result")).toBeVisible();
    expect(hall().seats["P|5|3"].status).toBe("sold");
    expect(hall().seats["P|5|4"].status).toBe("sold");
  });

  test("phone: nothing scrolls sideways and the screen has no accessibility violations", async ({ page }) => {
    seed("reset");
    await page.setViewportSize({ width: 375, height: 812 });
    await open(page, "org_admin");
    await expect(page.getByTestId("inv-hall")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
    await noA11yViolations(page);
  });

  test("owner: cancelling an invitation puts a seat held back into the reserve again (needs a worker)", async ({ page, request }) => {
    test.skip(process.env.ARENA_E2E_WORKER !== "1", "tickets are minted by arena-worker, which this harness does not run");
    seed("reset");
    await closeSeats(request, ["P|4|7"]);
    await page.setViewportSize({ width: 1280, height: 900 });
    await open(page, "org_admin");
    await seatNode(page, "P|4|7").click();
    await fillGuest(page, "Marta Gil");
    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-result")).toBeVisible();
    expect(hall().seats["P|4|7"].status).toBe("sold");

    const row = page.getByTestId("inv-row").first();
    await expect(row).toHaveAttribute("data-state", "active", { timeout: 60_000 });
    await row.getByTestId("inv-cancel").click();
    // The dialog says where the seat goes back to, and the guest is told by default.
    await expect(page.getByTestId("inv-cancel-dialog")).toContainText("go back to your reserve");
    await expect(page.getByTestId("inv-cancel-notify")).toBeChecked();
    await page.getByTestId("inv-cancel-dialog-submit").click();
    await expect(page.getByTestId("inv-cancelled")).toBeVisible();
    await expect(row).toHaveAttribute("data-state", "cancelled");
    expect(hall().seats["P|4|7"].status).toBe("unavailable");
  });
});
