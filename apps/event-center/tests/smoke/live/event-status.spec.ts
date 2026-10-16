/**
 * Twin test "the status of an event" (task 5-1), on the three roles of one
 * organization, against a REAL arena-api. It walks the scenario of the bot's
 * status buttons (eventbot/evstatus.go and the e2e
 * bot_e2e_evstatus_integration_test.go): publish, take off sale (one
 * confirmation that says sold tickets stay valid), archive (final), and delete
 * (a dry run first, then the typed word; an event that sold anything is refused
 * with the numbers and the archive is offered instead).
 *
 * The truth about each person comes from GET /v1/organizations/{org_id}/me.
 * The state of the events is read through the API (the events list) and, for
 * the soft delete, through the seed binary's `--event-status-op inspect`,
 * because this process has no SQL driver; `reset` puts the three fixture events
 * back (published, not deleted) before every test, so a retry or a second run
 * starts clean. An archive is final through the API, so only the fixture undoes it.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();

// Names of cmd/arena-e2e-seed/event_status_fixture.go.
const TOGGLE = "E2E Status Toggle";
const DELETE = "E2E Status Delete";
const SOLD = "E2E Status Sold";

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

function seed(op: "reset" | "inspect"): Record<string, unknown> {
  const args = ["--event-status-op", op];
  const r = spawnSync(seedBinary(), args, { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed ${args.join(" ")} failed: ${r.stderr}`);
  return JSON.parse(r.stdout.trim()) as Record<string, unknown>;
}

const rows = () => seed("inspect") as Record<string, { status: string; deleted: boolean }>;

const orgPath = (rest: string) => `${env.api_base_url}/v1/organizations/${env.org_id}${rest}`;
const bearer = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(orgPath("/me"), { headers: bearer(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

interface ListedEvent {
  id: string;
  name: string;
  status: string;
}
async function listed(request: APIRequestContext, role: Role, name: string): Promise<ListedEvent | undefined> {
  const res = await request.get(orgPath("/events"), { headers: bearer(role) });
  expect(res.status(), `GET events as ${role}`).toBe(200);
  const body = (await res.json()) as { events: ListedEvent[] };
  return body.events.find((e) => e.name === name);
}

async function mustList(request: APIRequestContext, role: Role, name: string): Promise<ListedEvent> {
  const found = await listed(request, role, name);
  if (found === undefined) throw new Error(`the fixture event ${name} is not in the list`);
  return found;
}

function trackApi(page: Page): string[] {
  const calls: string[] = [];
  page.on("request", (req) => {
    if (req.url().startsWith(env.api_base_url) && req.method() !== "GET" && req.method() !== "OPTIONS") {
      calls.push(`${req.method()} ${new URL(req.url()).pathname}`);
    }
  });
  return calls;
}

async function openEvent(page: Page, role: Role, id: string) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/events/${id}`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
}

test.beforeEach(() => {
  seed("reset");
});

test.describe("event status (live)", () => {
  for (const role of ["org_admin", "organizer"] as const) {
    test(`${role}: takes the event off sale after one confirmation, then publishes it again`, async ({ page, request }) => {
      const me = await orgMe(request, role);
      expect(me.permissions, "event.publish per the server").toContain("event.publish");
      expect(me.permissions, "event.delete per the server").toContain("event.delete");
      const event = await mustList(request, role, TOGGLE);
      expect(event.status).toBe("published");

      const writes = trackApi(page);
      await openEvent(page, role, event.id);
      const block = page.getByTestId("event-status");
      await expect(block).toBeVisible();
      await expect(page.getByTestId("event-status-current")).toContainText("Published");
      // The buttons follow the lifecycle: a published event cannot be published again.
      await expect(page.getByTestId("event-status-publish")).toHaveCount(0);
      await expect(page.getByTestId("event-status-off")).toBeVisible();
      await expect(page.getByTestId("event-status-archive")).toBeVisible();
      await expect(page.getByTestId("event-status-delete")).toBeVisible();
      await noA11yViolations(page);

      // The question says sold tickets stay valid; Cancel changes nothing.
      await page.getByTestId("event-status-off").click();
      await expect(page.getByTestId("event-off-dialog")).toContainText("Tickets already sold stay valid");
      await page.getByTestId("event-off-dialog-cancel").click();
      expect(writes, "nothing is sent before the confirmation").toEqual([]);
      expect((await mustList(request, role, TOGGLE)).status).toBe("published");

      await page.getByTestId("event-status-off").click();
      await page.getByTestId("event-off-dialog-submit").click();
      await expect(page.getByTestId("event-status-done")).toContainText("off sale");
      await expect(page.getByTestId("event-status-current")).toContainText("Draft");
      expect((await mustList(request, role, TOGGLE)).status, "the server took it off sale").toBe("draft");
      expect(writes).toEqual([`POST /v1/organizations/${env.org_id}/events/${event.id}/status`]);

      // A draft can be published again; it has a date and a priced category.
      await expect(page.getByTestId("event-status-off")).toHaveCount(0);
      await page.getByTestId("event-status-publish").click();
      await expect(page.getByTestId("event-status-done")).toContainText("published");
      await expect(page.getByTestId("event-status-current")).toContainText("Published");
      expect((await mustList(request, role, TOGGLE)).status).toBe("published");
    });
  }

  test("agent: no block, and the server refuses the same calls", async ({ page, request }) => {
    const me = await orgMe(request, "agent");
    expect(me.permissions.includes("event.publish"), "event.publish per the server").toBe(false);
    expect(me.permissions.includes("event.delete"), "event.delete per the server").toBe(false);
    const event = await mustList(request, "org_admin", TOGGLE);

    const writes = trackApi(page);
    await openEvent(page, "agent", event.id);
    await expect(page.getByTestId("event-status")).toHaveCount(0);
    await expect(page.getByTestId("event-status-delete")).toHaveCount(0);
    expect(writes).toEqual([]);

    const status = await request.post(orgPath(`/events/${event.id}/status`), { headers: bearer("agent"), data: { status: "draft" } });
    expect(status.status(), "POST status as agent").toBe(403);
    const impact = await request.get(orgPath(`/events/${event.id}/delete-impact`), { headers: bearer("agent") });
    expect(impact.status(), "GET delete-impact as agent").toBe(403);
    const del = await request.delete(orgPath(`/events/${event.id}`), { headers: bearer("agent") });
    expect(del.status(), "DELETE event as agent").toBe(403);
    expect((await mustList(request, "org_admin", TOGGLE)).status).toBe("published");
  });

  test("owner: delete shows the dry run first and needs the word", async ({ page, request }) => {
    const event = await mustList(request, "org_admin", DELETE);
    const writes = trackApi(page);
    await openEvent(page, "org_admin", event.id);

    await page.getByTestId("event-status-delete").click();
    const panel = page.getByTestId("event-delete-panel");
    await expect(panel).toContainText("Nothing was sold");
    await expect(page.getByTestId("dry-run-result")).toContainText("Paid orders");
    await noA11yViolations(page);
    await page.getByTestId("dry-run-confirm").click();

    // A wrong word and an empty one delete nothing.
    await expect(page.getByTestId("confirm-word")).toContainText("Delete this event?");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("confirm-word")).toContainText("Nothing is typed yet");
    await page.getByRole("textbox").fill("remove");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("confirm-word")).toContainText("does not match");
    expect(writes, "no DELETE before the right word").toEqual([]);
    expect(rows()[DELETE]).toEqual({ status: "published", deleted: false });

    // The word of the event is not the word of an order.
    await page.getByRole("textbox").fill("CANCEL ORDER");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("confirm-word")).toContainText("does not match");
    expect(writes).toEqual([]);

    await page.getByRole("textbox").fill("delete");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events$`));
    expect(writes).toEqual([`DELETE /v1/organizations/${env.org_id}/events/${event.id}`]);
    expect(await listed(request, "org_admin", DELETE), "gone from the list").toBeUndefined();
    expect(rows()[DELETE]).toEqual({ status: "published", deleted: true });
    await expect(page.getByText(DELETE, { exact: true })).toHaveCount(0);
  });

  test("manager: an event that sold tickets cannot be deleted, the archive is offered and final", async ({ page, request }) => {
    const me = await orgMe(request, "organizer");
    expect(me.permissions).toContain("event.delete");
    const event = await mustList(request, "organizer", SOLD);

    // The server's own refusal, with the numbers the screen shows.
    const dry = await request.get(orgPath(`/events/${event.id}/delete-impact`), { headers: bearer("organizer") });
    expect(dry.status()).toBe(200);
    const impact = (await dry.json()) as { paid_orders: number; tickets: number; can_delete: boolean; can_archive: boolean; blocked: string };
    expect(impact).toMatchObject({ paid_orders: 1, tickets: 1, can_delete: false, can_archive: true, blocked: "has_paid_orders" });

    const writes = trackApi(page);
    await openEvent(page, "organizer", event.id);
    await page.getByTestId("event-status-delete").click();
    await expect(page.getByTestId("dry-run-blocked")).toContainText("paid orders: 1, tickets: 1");
    await expect(page.getByTestId("dry-run-blocked")).toContainText("The tickets stay valid");
    // There is no way to the typed word for an event that sold something.
    await expect(page.getByTestId("dry-run-confirm")).toHaveCount(0);
    await expect(page.getByTestId("confirm-word")).toHaveCount(0);
    expect(writes).toEqual([]);
    await noA11yViolations(page);

    // The server refuses the delete too, even when called directly.
    const del = await request.delete(orgPath(`/events/${event.id}`), { headers: bearer("organizer") });
    expect(del.status()).toBe(409);
    expect(((await del.json()) as { error: { code: string } }).error.code).toBe("event.has_paid_orders");

    await page.getByTestId("event-delete-archive-instead").click();
    await expect(page.getByTestId("event-archive-dialog")).toContainText("This is final");
    await expect(page.getByTestId("event-archive-dialog")).toContainText("Tickets already sold stay valid");
    await page.getByTestId("event-archive-dialog-submit").click();
    await expect(page.getByTestId("event-status-done")).toContainText("archived");
    await expect(page.getByTestId("event-status-current")).toContainText("Archived");
    expect((await mustList(request, "organizer", SOLD)).status).toBe("archived");
    expect(writes).toEqual([`POST /v1/organizations/${env.org_id}/events/${event.id}/status`]);

    // Final: no way back to sale, but Delete is still on the card (and still refused).
    await expect(page.getByTestId("event-status-publish")).toHaveCount(0);
    await expect(page.getByTestId("event-status-off")).toHaveCount(0);
    await expect(page.getByTestId("event-status-archive")).toHaveCount(0);
    const back = await request.post(orgPath(`/events/${event.id}/status`), { headers: bearer("organizer"), data: { status: "published" } });
    expect(back.status()).toBe(422);
    expect(((await back.json()) as { error: { code: string } }).error.code).toBe("event.invalid_transition");
    // The sold ticket is untouched: the order is still paid.
    await expect(page.getByTestId("event-status-delete")).toBeVisible();
    await page.getByTestId("event-status-delete").click();
    await expect(page.getByTestId("dry-run-blocked")).toBeVisible();
    await expect(page.getByTestId("event-delete-archive-instead")).toHaveCount(0);
  });
});
