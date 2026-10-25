/**
 * Twin test "move or cancel a session" (task 5-2), on the three roles of one
 * organization, against a REAL arena-api. It walks the scenario of the bot's
 * Sessions e2e (bot_e2e_sessions_integration_test.go): the dry run first,
 * a contact e-mail asked INSIDE the change when there is none
 * (`contact_missing`), one press for a move, the typed SESSION word for a
 * cancellation, and the honest block for orders sold through a partner site
 * (`session.change_site_unsupported`).
 *
 * The truth about each person comes from GET /v1/organizations/{org_id}/me.
 * The database rows (sessions, the journal, the queued letters) are read
 * through the seed binary's `--session-change-op inspect`, because this process
 * has no SQL driver; `reset` rebuilds the five sessions of the fixture before
 * every test, so a retry or a second run starts clean. The harness runs no
 * worker: a letter is checked as a queued job, not as a delivered mail.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, richText, signInAs, type Role } from "./liveEnv";

const env = liveEnv();

// Fixed ids of cmd/arena-e2e-seed/session_change_fixture.go.
const EVENT = "fe0000e2-0000-7000-8000-000000000501";
const ANNEX_VENUE = "fe0000e2-0000-7000-8000-000000000502";
const S_MOVE = "fe0000e2-0000-7000-8000-000000000511";
const S_CANCEL = "fe0000e2-0000-7000-8000-000000000512";
const S_MANAGER = "fe0000e2-0000-7000-8000-000000000513";
const S_SITE = "fe0000e2-0000-7000-8000-000000000514";
const S_EMPTY = "fe0000e2-0000-7000-8000-000000000515";
const START = "2090-06-15T17:00:00Z"; // 19:00 in Madrid

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

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/me`, {
    headers: { Authorization: `Bearer ${person(env, role).access_token}` },
  });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

/** The write as a stale client would send it, without the screen. */
async function patchSession(request: APIRequestContext, role: Role, sessionId: string, data: Record<string, unknown>) {
  return request.patch(`${env.api_base_url}/v1/organizations/${env.org_id}/events/${EVENT}/sessions/${sessionId}`, {
    headers: { Authorization: `Bearer ${person(env, role).access_token}` },
    data,
  });
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

async function openSession(page: Page, role: Role, sessionId: string) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${sessionId}`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
}

/** Types a day into the date field and confirms the way it was read. */
async function pickDay(page: Page, typed: string, iso: string) {
  await page.getByTestId("date-field-input").fill(typed);
  await page.getByTestId("date-field-read").click();
  await page.getByTestId(`date-confirm-${iso}`).click();
  await expect(page.getByTestId("date-field-selected")).toContainText("2090");
}

async function saveContact(page: Page, email: string) {
  const form = page.getByTestId("session-contact-form");
  await expect(form).toBeVisible();
  await page.getByTestId("session-contact-email").fill(email);
  await page.getByTestId("session-contact-save").click();
  await expect(form).toHaveCount(0);
}

test.beforeEach(() => {
  seed("reset");
});

test.describe("move or cancel a session (live)", () => {
  test("owner: a move is checked first, asks for the contact, then moves the EXISTING session with one press", async ({ page, request }) => {
    const me = await orgMe(request, "org_admin");
    expect(me.permissions.includes("session.update"), "session.update per the server").toBe(true);
    expect(rows(S_MOVE)).toMatchObject({ status: "scheduled", start_at: START, changes: 0, queued_letters: 0, paid_orders: 1 });

    const calls = trackApi(page);
    await openSession(page, "org_admin", S_MOVE);
    await expect(page.getByTestId("session-change")).toBeVisible();
    await page.getByTestId("session-move-open").click();
    await expect(page.getByTestId("session-move")).toBeVisible();
    // Nothing is asked of the server until something is chosen.
    await expect(page.getByTestId("session-move-unchanged")).toBeVisible();
    await expect(page.getByTestId("dry-run")).toHaveCount(0);
    expect(calls.filter((c) => c === impactPath(S_MOVE))).toEqual([]);
    await noA11yViolations(page);

    // A new day and a new time (in the zone of the venue): 20 June, 20:30 Madrid = 18:30 UTC.
    await pickDay(page, "20.06.2090", "2090-06-20");
    await page.getByTestId("session-move-time").fill("20:30");

    // The dry run says the change needs a contact e-mail, so there is no confirm button yet.
    await expect(page.getByTestId("dry-run-blocked")).toBeVisible();
    await expect(page.getByTestId("dry-run-blocked")).toContainText("contact e-mail");
    await expect(page.getByTestId("dry-run-confirm")).toHaveCount(0);
    expect(calls.filter((c) => c === patchPath(S_MOVE)), "nothing is written by a dry run").toEqual([]);
    expect(rows(S_MOVE)).toMatchObject({ status: "scheduled", start_at: START, changes: 0, queued_letters: 0 });
    await noA11yViolations(page);

    // A bad address is refused on the screen, before any request.
    await page.getByTestId("session-contact-email").fill("not-an-address");
    await page.getByTestId("session-contact-save").click();
    await expect(page.getByTestId("session-contact-email-error")).toBeVisible();
    expect(calls.filter((c) => c === contactPath)).toEqual([]);

    await saveContact(page, "organizer-contact@example.test");
    expect(calls.filter((c) => c === contactPath)).toHaveLength(1);

    // The dry run runs again: now it says how many buyers get a letter, and the message is suggested.
    await expect(page.getByTestId("dry-run-blocked")).toHaveCount(0);
    await expect(page.getByTestId("dry-run-summary")).toBeVisible();
    await expect(page.getByTestId("dry-run-result")).toContainText("organizer-contact@example.test");
    const message = richText(page, "session-change-message");
    await expect(message).not.toHaveText("");
    await message.fill("We moved the show to Saturday. Sorry for the change.");
    expect(rows(S_MOVE)).toMatchObject({ start_at: START, changes: 0, queued_letters: 0, org_contact_email: "organizer-contact@example.test" });

    await page.getByTestId("dry-run-confirm").click();
    await expect(page.getByTestId("session-change-done")).toBeVisible();
    await expect(page.getByTestId("session-change-done")).toContainText("Letters to buyers queued: 1");
    expect(calls.filter((c) => c === patchPath(S_MOVE)), "exactly one write").toHaveLength(1);
    expect(rows(S_MOVE)).toMatchObject({
      status: "scheduled",
      start_at: "2090-06-20T18:30:00Z",
      changes: 1,
      notices: 1,
      queued_letters: 1,
      paid_orders: 1,
      last_message: "<p>We moved the show to Saturday. Sorry for the change.</p>",
    });
    await noA11yViolations(page);
  });

  test("owner: a cancellation needs the SESSION word, then letters are queued and the order stays paid", async ({ page, request }) => {
    const me = await orgMe(request, "org_admin");
    expect(me.permissions.includes("session.update"), "session.update per the server").toBe(true);
    const calls = trackApi(page);
    await openSession(page, "org_admin", S_CANCEL);
    await page.getByTestId("session-cancel-open").click();
    await expect(page.getByTestId("session-cancel")).toBeVisible();
    // Before anything: the platform does NOT refund, said plainly.
    await expect(page.getByTestId("session-cancel-refund-note")).toContainText("does NOT refund");
    await expect(page.getByTestId("dry-run-blocked")).toContainText("contact e-mail");
    await expect(page.getByTestId("session-cancel-continue")).toHaveCount(0);
    await noA11yViolations(page);

    await saveContact(page, "cancel-contact@example.test");
    await expect(page.getByTestId("session-cancel-continue")).toBeVisible();
    await richText(page, "session-change-message").fill("The show is canceled. We will write to you about the money.");
    await page.getByTestId("session-cancel-continue").click();

    const dialog = page.getByTestId("confirm-word");
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText("CANCEL SESSION");
    await expect(dialog).not.toContainText("CANCEL ORDER");
    await noA11yViolations(page);
    // Nothing typed, a wrong word and the word of an ORDER cancel nothing.
    const typeWord = async (text: string) => {
      await dialog.getByLabel("Type the word").fill(text);
      await page.getByTestId("confirm-word-submit").click();
    };
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("confirm-word-note")).toBeVisible();
    for (const wrong of ["yes please", "CANCEL ORDER", "ОТМЕНИТЬ ЗАКАЗ", "DELETE"]) {
      await typeWord(wrong);
      await expect(page.getByTestId("confirm-word-note")).toBeVisible();
      await expect(dialog, `the dialog stays after "${wrong}"`).toBeVisible();
    }
    expect(calls.filter((c) => c === patchPath(S_CANCEL)), "no write before the right word").toEqual([]);
    expect(rows(S_CANCEL)).toMatchObject({ status: "scheduled", changes: 0, queued_letters: 0 });

    // The right word, in another case and with extra spaces and a full stop.
    await typeWord("  cancel   session. ");
    await expect(page.getByTestId("session-change-done")).toBeVisible();
    await expect(page.getByTestId("session-change-done")).toContainText("Letters to buyers queued: 1");
    await expect(page.getByTestId("confirm-word")).toHaveCount(0);
    expect(calls.filter((c) => c === patchPath(S_CANCEL))).toHaveLength(1);
    // The session is CANCELED (a status, never a delete); the order is still paid.
    expect(rows(S_CANCEL)).toMatchObject({
      status: "cancelled",
      changes: 1,
      notices: 1,
      queued_letters: 1,
      paid_orders: 1,
      last_message: "<p>The show is canceled. We will write to you about the money.</p>",
    });
  });

  test("owner: the SESSION word in Russian cancels too", async ({ page }) => {
    await openSession(page, "org_admin", S_EMPTY);
    await page.getByLabel("Language").selectOption("ru");
    await page.getByTestId("session-cancel-open").click();
    // Nobody bought: no contact needed, nothing blocked.
    await page.getByTestId("session-cancel-continue").click();
    await expect(page.getByTestId("confirm-word")).toContainText("ОТМЕНИТЬ СЕАНС");
    await page.getByTestId("confirm-word").getByRole("textbox").fill("отменить сеанс");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("session-change-done")).toBeVisible();
    expect(rows(S_EMPTY)).toMatchObject({ status: "cancelled", queued_letters: 0 });
  });

  test("owner: a session nobody bought moves with no contact and no message box", async ({ page }) => {
    await openSession(page, "org_admin", S_EMPTY);
    await page.getByTestId("session-move-open").click();
    await pickDay(page, "17.06.2090", "2090-06-17");
    await expect(page.getByTestId("dry-run-summary")).toContainText("Nobody has bought");
    await expect(page.getByTestId("session-contact-form")).toHaveCount(0);
    await expect(page.getByTestId("session-change-message-box")).toHaveCount(0);
    await page.getByTestId("dry-run-confirm").click();
    await expect(page.getByTestId("session-change-done")).toBeVisible();
    expect(rows(S_EMPTY)).toMatchObject({ status: "scheduled", start_at: "2090-06-17T17:00:00Z", queued_letters: 0 });
  });

  test("owner: a time that does not exist when the clocks go forward is refused on the screen", async ({ page }) => {
    const calls = trackApi(page);
    await openSession(page, "org_admin", S_EMPTY);
    await page.getByTestId("session-move-open").click();
    // 2090-03-26: the last Sunday of March in Madrid, 02:00 jumps to 03:00.
    await pickDay(page, "26.03.2090", "2090-03-26");
    await expect(page.getByTestId("dry-run-summary")).toBeVisible(); // 19:00 exists: the dry run ran
    const before = calls.filter((c) => c === impactPath(S_EMPTY)).length;
    await page.getByTestId("session-move-time").fill("02:30");
    await expect(page.getByTestId("session-move-time-error")).toBeVisible();
    await expect(page.getByTestId("dry-run")).toHaveCount(0);
    expect(calls.filter((c) => c === impactPath(S_EMPTY)).length, "no new dry run for a time that does not exist").toBe(before);
    expect(calls.filter((c) => c === patchPath(S_EMPTY))).toEqual([]);
    expect(rows(S_EMPTY)).toMatchObject({ start_at: START });
  });

  test("owner: orders sold through a partner site block the change honestly, and the API agrees", async ({ page, request }) => {
    const calls = trackApi(page);
    await openSession(page, "org_admin", S_SITE);
    await page.getByTestId("session-move-open").click();
    await pickDay(page, "20.06.2090", "2090-06-20");
    const blocked = page.getByTestId("dry-run-blocked");
    await expect(blocked).toBeVisible();
    await expect(blocked).toContainText("partner site");
    await expect(blocked).toContainText("Nothing was changed");
    await expect(page.getByTestId("dry-run-confirm")).toHaveCount(0);
    await expect(page.getByTestId("session-contact-form")).toHaveCount(0);
    await page.getByTestId("session-change-close").click();

    await page.getByTestId("session-cancel-open").click();
    await expect(page.getByTestId("dry-run-blocked")).toContainText("partner site");
    await expect(page.getByTestId("session-cancel-continue")).toHaveCount(0);
    expect(calls.filter((c) => c === patchPath(S_SITE))).toEqual([]);

    // A stale client that writes anyway meets the server's own rule.
    const move = await patchSession(request, "org_admin", S_SITE, { start_at: "2090-06-20T18:30:00Z" });
    expect(move.status(), "move of a site session").toBe(422);
    expect(((await move.json()) as { error: { code: string } }).error.code).toBe("session.change_site_unsupported");
    const cancel = await patchSession(request, "org_admin", S_SITE, { status: "cancelled" });
    expect(cancel.status(), "cancel of a site session").toBe(422);
    expect(rows(S_SITE)).toMatchObject({ status: "scheduled", start_at: START, changes: 0, queued_letters: 0 });
  });

  test("a message longer than the API allows is refused on the screen and nothing is sent", async ({ page }) => {
    const calls = trackApi(page);
    await openSession(page, "org_admin", S_MOVE);
    await page.getByTestId("session-move-open").click();
    await pickDay(page, "20.06.2090", "2090-06-20");
    await saveContact(page, "long-contact@example.test");
    await expect(page.getByTestId("dry-run-confirm")).toBeVisible();
    await richText(page, "session-change-message").fill("x".repeat(1001));
    await expect(page.getByTestId("session-change-message-error")).toBeVisible();
    await page.getByTestId("dry-run-confirm").click();
    await expect(page.getByTestId("dry-run-action-error")).toBeVisible();
    expect(calls.filter((c) => c === patchPath(S_MOVE))).toEqual([]);
    expect(rows(S_MOVE)).toMatchObject({ start_at: START, changes: 0, queued_letters: 0 });
  });

  test("manager: moves a session to another venue; the time stays", async ({ page, request }) => {
    const me = await orgMe(request, "organizer");
    expect(me.permissions.includes("session.update"), "session.update per the server").toBe(true);
    expect(me.permissions.includes("event.update"), "event.update per the server").toBe(true);
    expect(me.permissions.includes("venue.read"), "venue.read per the server").toBe(true);
    await openSession(page, "organizer", S_MANAGER);
    await page.getByTestId("session-move-open").click();
    await page.getByTestId("session-move-venue").selectOption(ANNEX_VENUE);
    await saveContact(page, "manager-contact@example.test");
    await expect(page.getByTestId("dry-run-summary")).toBeVisible();
    await expect(page.getByTestId("dry-run-result")).toContainText("venue");
    await page.getByTestId("dry-run-confirm").click();
    await expect(page.getByTestId("session-change-done")).toBeVisible();
    expect(rows(S_MANAGER)).toMatchObject({ venue_id: ANNEX_VENUE, start_at: START, changes: 1, queued_letters: 1 });
  });

  test("agent sees no block, and the API refuses the agent", async ({ page, request }) => {
    const me = await orgMe(request, "agent");
    expect(me.role).toBe("agent");
    expect(me.permissions.includes("session.update"), "session.update per the server").toBe(false);
    const move = await patchSession(request, "agent", S_MOVE, { start_at: "2090-06-20T18:30:00Z" });
    expect(move.status(), "agent PATCH").toBe(403);
    const cancel = await patchSession(request, "agent", S_MOVE, { status: "cancelled" });
    expect(cancel.status(), "agent cancel").toBe(403);

    const calls = trackApi(page);
    await signInAs(page, person(env, "agent"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${S_MOVE}`);
    // Whatever the agent may read of the card, there is no move or cancel on it.
    await expect(page.getByTestId("session-move-open")).toHaveCount(0);
    await expect(page.getByTestId("session-cancel-open")).toHaveCount(0);
    await expect(page.getByTestId("session-change")).toHaveCount(0);
    expect(calls.filter((c) => c.startsWith("PATCH ") || c === impactPath(S_MOVE))).toEqual([]);
    expect(rows(S_MOVE)).toMatchObject({ status: "scheduled", start_at: START, changes: 0, queued_letters: 0 });
  });

  test("a cancelled session offers neither a move nor a cancellation again", async ({ page }) => {
    await openSession(page, "org_admin", S_EMPTY);
    await page.getByTestId("session-cancel-open").click();
    await page.getByTestId("session-cancel-continue").click();
    await page.getByTestId("confirm-word").getByLabel("Type the word").fill("CANCEL SESSION");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("session-change-done")).toBeVisible();
    await page.reload();
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByTestId("session-move-open")).toHaveCount(0);
    await expect(page.getByTestId("session-cancel-open")).toHaveCount(0);
  });
});
