/**
 * Resilience of the web move/cancel of a session (task 5-2) against a REAL arena-api: what the
 * original twin spec (session-change.spec.ts) does not reach. Failed or lost writes keep what the person
 * typed and never write twice, a double press is ONE write, a contact that fails to save can be
 * retried, a time is read in the VENUE's zone (the browser here is in another one), an hour that
 * happens twice is read as its first occurrence, and a client-side move to another organization drops
 * every open panel and every answer still in flight.
 *
 * Writes that are made to fail are answered by the test, NOT forwarded: the server never sees them.
 * A "lost answer" is different: the request IS forwarded (the server writes), and only the answer is
 * dropped, so the retry meets the state the first write left.
 */
import { expect, test, type APIRequestContext, type Page, type Route } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();

// Fixed ids of cmd/arena-e2e-seed/session_change_fixture.go.
const EVENT = "fe0000e2-0000-7000-8000-000000000501";
const S_MOVE = "fe0000e2-0000-7000-8000-000000000511";
const S_CANCEL = "fe0000e2-0000-7000-8000-000000000512";
const S_EMPTY = "fe0000e2-0000-7000-8000-000000000515";
const START = "2090-06-15T17:00:00Z"; // 19:00 in Madrid
const SECOND_ORG = "fe0000e2-0000-7000-8000-0000000000b2";
const SECOND_SESSION = "fe0000e2-0000-7000-8000-0000000000b3";

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
  status: string;
  venue_id: string;
  start_at: string;
  changes: number;
  notices: number;
  queued_letters: number;
  org_contact_email: string;
  last_message: string;
  paid_orders: number;
}

function seed(op: "reset" | "inspect", sessionId?: string): Record<string, unknown> {
  const args = ["--session-change-op", op];
  if (sessionId !== undefined) args.push("--session-id", sessionId);
  const r = spawnSync(seedBinary(), args, { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed ${args.join(" ")} failed: ${r.stderr}`);
  return JSON.parse(r.stdout.trim()) as Record<string, unknown>;
}
const rows = (sessionId: string) => seed("inspect", sessionId) as unknown as Rows;

/** The contact set through the API (as the owner would), so a test can start past `contact_missing`. */
async function setContact(request: APIRequestContext, role: Role, email: string) {
  const res = await request.put(`${env.api_base_url}/v1/organizations/${env.org_id}/events/${EVENT}/contact`, {
    headers: { Authorization: `Bearer ${person(env, role).access_token}` },
    data: { email, phone: null, phone_hidden: true },
  });
  expect(res.status(), "PUT contact").toBeLessThan(300);
}

function trackApi(page: Page): string[] {
  const calls: string[] = [];
  page.on("request", (req) => {
    if (req.url().startsWith(env.api_base_url)) calls.push(`${req.method()} ${new URL(req.url()).pathname}`);
  });
  return calls;
}

const patchPath = (sessionId: string) => `PATCH /v1/organizations/${env.org_id}/events/${EVENT}/sessions/${sessionId}`;
const impactPath = (sessionId: string) => `GET /v1/organizations/${env.org_id}/sessions/${sessionId}/change-impact`;
const contactPath = `PUT /v1/organizations/${env.org_id}/events/${EVENT}/contact`;
const patchUrl = (sessionId: string) => `${env.api_base_url}/v1/organizations/${env.org_id}/events/${EVENT}/sessions/${sessionId}`;

async function openSession(page: Page, role: Role, sessionId: string) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${sessionId}`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
}

async function pickDay(page: Page, typed: string, iso: string) {
  await page.getByTestId("date-field-input").fill(typed);
  await page.getByTestId("date-field-read").click();
  await page.getByTestId(`date-confirm-${iso}`).click();
  await expect(page.getByTestId("date-field-selected")).toContainText("2090");
}

const unavailable = (route: Route) =>
  route.fulfill({
    status: 503,
    contentType: "application/json",
    body: JSON.stringify({ error: { code: "internal.unavailable", message: "try again", request_id: "e2e" } }),
  });

test.beforeEach(() => {
  seed("reset");
});

test.describe("session change: failed and repeated writes (live)", () => {
  test("a failed write keeps the typed message and the chosen time, and a double press then writes ONCE", async ({ page, request }) => {
    await setContact(request, "org_admin", "resilience-contact@example.test");
    const calls = trackApi(page);
    await openSession(page, "org_admin", S_MOVE);
    await page.getByTestId("session-move-open").click();
    await pickDay(page, "21.06.2090", "2090-06-21");
    await page.getByTestId("session-move-time").fill("21:15");
    await expect(page.getByTestId("dry-run-confirm")).toBeVisible();
    const message = page.getByTestId("session-change-message");
    await message.fill("Typed by a person: we moved to Sunday.");

    // The first write is refused by a stand-in server (the real one never sees it).
    let failNext = true;
    let forwarded = 0;
    await page.route(patchUrl(S_MOVE), async (route) => {
      if (route.request().method() !== "PATCH") return route.continue();
      if (failNext) {
        failNext = false;
        return unavailable(route);
      }
      forwarded += 1;
      await new Promise((r) => setTimeout(r, 700)); // slow enough for a second click to land while it runs
      return route.continue();
    });
    await page.getByTestId("dry-run-confirm").click();
    await expect(page.getByTestId("dry-run-action-error")).toBeVisible();
    expect(rows(S_MOVE), "nothing was written").toMatchObject({ start_at: START, changes: 0, queued_letters: 0 });
    // Everything the person typed is still there.
    await expect(message).toHaveValue("Typed by a person: we moved to Sunday.");
    await expect(page.getByTestId("session-move-time")).toHaveValue("21:15");
    await expect(page.getByTestId("date-field-selected")).toContainText("2090");
    await expect(page.getByTestId("session-contact-form")).toHaveCount(0);

    // Two fast presses on the retry button are ONE request.
    await page.getByTestId("dry-run-confirm").dblclick();
    await expect(page.getByTestId("session-change-done")).toBeVisible();
    expect(calls.filter((c) => c === impactPath(S_MOVE)).length, "the dry run came before the write").toBeGreaterThan(0);
    expect(forwarded, "one write reached the server").toBe(1);
    expect(calls.filter((c) => c === patchPath(S_MOVE)), "the failed attempt plus ONE retry").toHaveLength(2);
    expect(rows(S_MOVE)).toMatchObject({
      start_at: "2090-06-21T19:15:00Z",
      changes: 1,
      notices: 1,
      queued_letters: 1,
      paid_orders: 1,
      last_message: "Typed by a person: we moved to Sunday.",
    });
  });

  test("a lost answer after the server wrote: the retry does not duplicate the session, the change or the letter", async ({ page, request }) => {
    await setContact(request, "org_admin", "lost-contact@example.test");
    await openSession(page, "org_admin", S_MOVE);
    await page.getByTestId("session-move-open").click();
    await pickDay(page, "22.06.2090", "2090-06-22");
    await expect(page.getByTestId("dry-run-confirm")).toBeVisible();

    let lose = true;
    const writes: number[] = [];
    await page.route(patchUrl(S_MOVE), async (route) => {
      if (route.request().method() !== "PATCH") return route.continue();
      writes.push(1);
      if (lose) {
        lose = false;
        await route.fetch(); // the server DOES write
        return route.abort("connectionreset"); // ... and the answer never arrives
      }
      return route.continue();
    });
    await page.getByTestId("dry-run-confirm").click();
    await expect(page.getByTestId("dry-run-action-error")).toBeVisible();
    // The server wrote once, whatever the screen knows.
    expect(rows(S_MOVE)).toMatchObject({ start_at: "2090-06-22T17:00:00Z", changes: 1, queued_letters: 1 });

    // The person tries again with the same request.
    await page.getByTestId("dry-run-confirm").click();
    // The server answers the same PATCH as "nothing to change", so the screen says the session is moved to
    // the new time. KNOWN LIMITATION: it then also says "Letters to buyers queued: 0", although the first
    // (lost) write queued one; the letter itself is not lost or doubled (asserted below).
    await expect(page.getByTestId("session-change-done")).toContainText("22 Jun 2090");
    expect(writes).toHaveLength(2);
    // Whatever the second answer was, nothing was doubled: one session, one journal row, one letter, one paid order.
    expect(rows(S_MOVE)).toMatchObject({ start_at: "2090-06-22T17:00:00Z", changes: 1, notices: 1, queued_letters: 1, paid_orders: 1 });
    const sessions = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/events/${EVENT}/sessions`, {
      headers: { Authorization: `Bearer ${person(env, "org_admin").access_token}` },
    });
    const ids = ((await sessions.json()) as { sessions: { id: string }[] }).sessions.map((s) => s.id);
    expect(ids.filter((id) => id === S_MOVE)).toHaveLength(1);
    expect(new Set(ids).size, "no session was duplicated").toBe(ids.length);
  });

  test("a cancellation whose write fails keeps the typed word's dialog, and one press then cancels once", async ({ page, request }) => {
    await setContact(request, "org_admin", "cancel-retry@example.test");
    const calls = trackApi(page);
    await openSession(page, "org_admin", S_CANCEL);
    await page.getByTestId("session-cancel-open").click();
    await page.getByTestId("session-change-message").fill("Canceled by the organizer, text typed by hand.");
    await page.getByTestId("session-cancel-continue").click();
    let failNext = true;
    await page.route(patchUrl(S_CANCEL), (route) => {
      if (route.request().method() === "PATCH" && failNext) {
        failNext = false;
        return unavailable(route);
      }
      return route.continue();
    });
    const dialog = page.getByTestId("confirm-word");
    await dialog.getByLabel("Type the word").fill("CANCEL SESSION");
    await page.getByTestId("confirm-word-submit").click();
    await expect(dialog).toBeVisible(); // the dialog stays, with the failure
    expect(rows(S_CANCEL)).toMatchObject({ status: "scheduled", changes: 0, queued_letters: 0, paid_orders: 1 });
    await expect(dialog.getByLabel("Type the word")).toHaveValue("CANCEL SESSION");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("session-change-done")).toBeVisible();
    expect(calls.filter((c) => c === patchPath(S_CANCEL))).toHaveLength(2);
    expect(rows(S_CANCEL)).toMatchObject({
      status: "cancelled",
      changes: 1,
      queued_letters: 1,
      paid_orders: 1,
      last_message: "Canceled by the organizer, text typed by hand.",
    });
  });
});

test.describe("session change: the contact form (live)", () => {
  test("a contact that fails to save keeps the typed address, and the retry saves it once", async ({ page }) => {
    const calls = trackApi(page);
    await openSession(page, "org_admin", S_MOVE);
    await page.getByTestId("session-move-open").click();
    await pickDay(page, "20.06.2090", "2090-06-20");
    await expect(page.getByTestId("dry-run-blocked")).toContainText("contact e-mail");
    let failNext = true;
    await page.route(`${env.api_base_url}/v1/organizations/${env.org_id}/events/${EVENT}/contact`, (route) => {
      if (route.request().method() === "PUT" && failNext) {
        failNext = false;
        return unavailable(route);
      }
      return route.continue();
    });
    await page.getByTestId("session-contact-email").fill("retry-contact@example.test");
    await page.getByTestId("session-contact-save").click();
    await expect(page.getByTestId("session-contact-error")).toBeVisible();
    await expect(page.getByTestId("session-contact-email")).toHaveValue("retry-contact@example.test");
    expect(rows(S_MOVE).org_contact_email, "the failed save wrote nothing").not.toBe("retry-contact@example.test");
    await page.getByTestId("session-contact-save").click();
    await expect(page.getByTestId("session-contact-form")).toHaveCount(0);
    await expect(page.getByTestId("dry-run-confirm")).toBeVisible();
    expect(calls.filter((c) => c === contactPath)).toHaveLength(2);
    expect(rows(S_MOVE)).toMatchObject({ org_contact_email: "retry-contact@example.test", changes: 0, queued_letters: 0, start_at: START });
  });
});

test.describe("session change: the venue's zone, not the browser's (live)", () => {
  // The browser is in New York; the venue is in Madrid. 20:30 on 20 June is 20:30 MADRID = 18:30 UTC.
  test.use({ timezoneId: "America/New_York" });

  test("a time is read in the venue zone and an hour that happens twice is read as its first occurrence", async ({ page }) => {
    await openSession(page, "org_admin", S_EMPTY);
    await page.getByTestId("session-move-open").click();
    await expect(page.locator("#session-move-zone")).toContainText("Europe/Madrid");
    await pickDay(page, "20.06.2090", "2090-06-20");
    await page.getByTestId("session-move-time").fill("20:30");
    await expect(page.getByTestId("dry-run-summary")).toBeVisible();
    await page.getByTestId("dry-run-confirm").click();
    await expect(page.getByTestId("session-change-done")).toBeVisible();
    expect(rows(S_EMPTY).start_at, "20:30 in Madrid, not in New York").toBe("2090-06-20T18:30:00Z");

    // 2090-10-29 02:30 happens twice in Madrid (summer time first, UTC+2): the first one is 00:30 UTC.
    await page.reload();
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await page.getByTestId("session-move-open").click();
    await pickDay(page, "29.10.2090", "2090-10-29");
    await page.getByTestId("session-move-time").fill("02:30");
    await expect(page.getByTestId("dry-run-summary")).toBeVisible();
    await page.getByTestId("dry-run-confirm").click();
    await expect(page.getByTestId("session-change-done")).toBeVisible();
    expect(rows(S_EMPTY).start_at).toBe("2090-10-29T00:30:00Z");
  });
});

test.describe("session change: another organization (live)", () => {
  test("a client-side move to another organization drops the open panel and a dry run still in flight", async ({ page, request }) => {
    const owner = person(env, "org_admin");
    const me = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/me`, { headers: { Authorization: `Bearer ${owner.access_token}` } });
    expect(me.status()).toBe(200);
    const allowed = (await me.json()) as { permissions: string[] };
    // The second organization is SIMULATED (read answers only; a write to it is a test failure).
    const secondRequests: string[] = [];
    await page.route(`${env.api_base_url}/v1/organizations/${SECOND_ORG}/**`, async (route) => {
      const url = new URL(route.request().url());
      secondRequests.push(`${route.request().method()} ${url.pathname}`);
      if (url.pathname.endsWith("/me")) {
        return route.fulfill({
          json: { ...allowed, org_id: SECOND_ORG, organization_id: SECOND_ORG, permissions: ["session.read", "session.update", "event.read", "venue.read"] },
        });
      }
      if (url.pathname.endsWith(`/events/${EVENT}/sessions`)) {
        return route.fulfill({
          json: {
            sessions: [
              {
                id: SECOND_SESSION,
                event_id: EVENT,
                start_at: "2091-01-10T10:00:00Z",
                sales_end_at: "2091-01-10T09:00:00Z",
                doors_open_at: null,
                status: "scheduled",
                venue_id: null,
              },
            ],
          },
        });
      }
      if (url.pathname.endsWith("/venues")) return route.fulfill({ json: { venues: [] } });
      return route.fulfill({ status: 404, json: { error: { code: "not_found", message: "simulated" } } });
    });

    // The first organization's dry run is held until the person has left.
    let release: () => void = () => undefined;
    const gate = new Promise<void>((r) => (release = r));
    let held = 0;
    await page.route(`${env.api_base_url}/v1/organizations/${env.org_id}/sessions/${S_MOVE}/change-impact**`, async (route) => {
      held += 1;
      await gate;
      await route.continue();
    });
    const writes: string[] = [];
    page.on("request", (r) => {
      if (["POST", "PATCH", "PUT", "DELETE"].includes(r.method()) && !r.url().includes("/auth/")) writes.push(`${r.method()} ${new URL(r.url()).pathname}`);
    });

    await openSession(page, "org_admin", S_MOVE);
    // Visit the other organization's session once and come back, so that BOTH pages are in the
    // query cache: leaving then shows the cached page at once (no loading state that would unmount
    // the card by itself), exactly the case in which retained component state would show.
    const go = (org: string, session: string) =>
      page.evaluate(async ([o, sid]) => {
        const modulePath = "/src/router.tsx";
        const { router } = await import(modulePath);
        await router.navigate({ to: "/o/$orgId/events/$eventId/sessions/$sessionId", params: { orgId: o, eventId: "fe0000e2-0000-7000-8000-000000000501", sessionId: sid } });
      }, [org, session]);
    await go(SECOND_ORG, SECOND_SESSION);
    await expect(page.getByTestId("session-change")).toBeVisible();
    await go(env.org_id, S_MOVE);
    await expect(page.getByTestId("session-change")).toBeVisible();
    await page.getByTestId("session-move-open").click();
    await pickDay(page, "20.06.2090", "2090-06-20");
    await expect.poll(() => held).toBeGreaterThanOrEqual(1); // dev StrictMode may ask twice

    // Client-side navigation (no reload) to the other organization's session.
    await go(SECOND_ORG, SECOND_SESSION);
    await expect(page).toHaveURL(new RegExp(`/o/${SECOND_ORG}/events/.*/sessions/${SECOND_SESSION}$`));
    await expect(page.getByTestId("session-change")).toBeVisible();

    // The first organization's answer arrives NOW: it must change nothing here.
    release();
    await page.waitForTimeout(1200);
    await expect(page.getByTestId("session-move")).toHaveCount(0);
    await expect(page.getByTestId("dry-run")).toHaveCount(0);
    await expect(page.getByTestId("dry-run-blocked")).toHaveCount(0);
    await expect(page.getByTestId("session-contact-form")).toHaveCount(0);
    await expect(page.getByTestId("session-move-open")).toBeVisible();
    // The second organization's panel starts clean: no typed day, nothing asked.
    await page.getByTestId("session-move-open").click();
    await expect(page.getByTestId("session-move-unchanged")).toBeVisible();
    expect(secondRequests.filter((c) => c.includes("change-impact")), "nothing was asked of the other organization yet").toEqual([]);
    expect(writes, "no write left the browser").toEqual([]);
    expect(rows(S_MOVE)).toMatchObject({ start_at: START, changes: 0, queued_letters: 0 });
  });
});
