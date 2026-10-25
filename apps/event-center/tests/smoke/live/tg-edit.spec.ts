/**
 * Twin test "the Telegram mini app editor" (spec 44, RT-12b), against a REAL arena-api. It walks the path of the bot's
 * e2e (eventbot/bot_e2e_miniapp_integration_test.go, httpserver/web_link_integration_test.go): the bot asks
 * POST /v1/bot/web-login-codes for a code of purpose "miniapp" in the name of a linked Telegram account, the person
 * opens the link, the page exchanges the code for the short session and opens the editor of the text.
 *
 * The twin plays the bot with the harness's BOT_SERVICE_TOKEN (arena-e2e-seed links each of the three people to a
 * Telegram account). What it proves, always through the API and never through the screen:
 *  - the owner and the manager open the description, edit it and save: ONE PATCH of the event that carries the text
 *    and its format, the stored text is the ART the editor made, the name of the event is untouched, and the Telegram
 *    window is told to close;
 *  - the same for the letter text (the call of the web card);
 *  - a code works once: the second opening says the link does not work and to press the bot button again;
 *  - a person without the right to edit (the agent) and an event of ANOTHER organization get the calm "no access"
 *    line, and no editor, no write.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, person, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const orgPath = (rest: string) => `${env.api_base_url}/v1/organizations/${env.org_id}${rest}`;
const bearer = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
/** An event of another organization (arena-e2e-seed, categories_fixture.go). */
const FOREIGN_EVENT_ID = "fe0000e2-0000-7000-8000-00000000e522";

interface ListedEvent {
  id: string;
  name: string;
  description: string | null;
  description_format: string | null;
  email_note: string | null;
  email_note_format: string | null;
}

async function stored(request: APIRequestContext): Promise<ListedEvent> {
  const res = await request.get(orgPath("/events"), { headers: bearer("org_admin") });
  expect(res.status(), "GET events").toBe(200);
  const event = ((await res.json()) as { events: ListedEvent[] }).events.find((e) => e.id === env.event_id);
  if (event === undefined) throw new Error("the fixture event is not in the list");
  return event;
}

async function patch(request: APIRequestContext, data: Record<string, unknown>) {
  const res = await request.patch(orgPath(`/events/${env.event_id}`), { headers: bearer("org_admin"), data });
  expect(res.status(), "PATCH event").toBe(200);
}

/** The bot's call: a one-time link of the mini app for a linked Telegram account. */
async function miniappLink(request: APIRequestContext, role: Role, kind: "description" | "email_note", eventId = env.event_id): Promise<string> {
  const res = await request.post(`${env.api_base_url}/v1/bot/web-login-codes`, {
    headers: { Authorization: `Bearer ${env.bot_service_token}` },
    data: { telegram_user_id: person(env, role).telegram_user_id, purpose: "miniapp", kind, event_id: eventId },
  });
  expect(res.status(), "POST web-login-codes").toBe(201);
  const { url } = (await res.json()) as { url: string };
  expect(url).toContain("/tg/edit#code=");
  return url;
}

/** A stand-in for the Telegram script: what the page may call, and how often it closed the window. */
async function telegramStub(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as Record<string, unknown>;
    const seen = { closed: 0 };
    w.__tgClosed = seen;
    w.Telegram = {
      WebApp: {
        platform: "android",
        ready: () => undefined,
        expand: () => undefined,
        close: () => seen.closed++,
        BackButton: { show: () => undefined, hide: () => undefined, onClick: () => undefined, offClick: () => undefined },
      },
    };
  });
}

const closedCount = (page: Page) => page.evaluate(() => (window as unknown as { __tgClosed: { closed: number } }).__tgClosed.closed);

/** The writes the page made: every request that is not a read and not the sign-in. */
function trackWrites(page: Page): string[] {
  const calls: string[] = [];
  page.on("request", (req) => {
    const path = new URL(req.url()).pathname;
    if (req.url().startsWith(env.api_base_url) && !["GET", "OPTIONS", "HEAD"].includes(req.method()) && !path.startsWith("/v1/auth/")) {
      calls.push(`${req.method()} ${path}`);
    }
  });
  return calls;
}

const box = (page: Page, testId: string) => page.locator(`[data-testid=${testId}] [role="textbox"][aria-multiline="true"]`);

let original: ListedEvent;

test.beforeAll(async ({ request }) => {
  original = await stored(request);
  await patch(request, { description: "<p>An evening of <strong>jazz</strong>.</p>", description_format: "art" });
  await patch(request, { email_note: "<p>Bring a <strong>warm</strong> jacket.</p>", email_note_format: "art" });
});

test.afterAll(async ({ request }) => {
  // Put the shared event back as the other specs expect it.
  await patch(request, { description: original.description ?? "", description_format: "art" });
  await patch(request, original.email_note === null ? { email_note: null } : { email_note: original.email_note, email_note_format: original.email_note_format ?? "art" });
});

for (const role of ["org_admin", "organizer"] as const) {
  test(`${role}: the description opens from the bot link, one PATCH saves it, the window closes`, async ({ page, request }) => {
    await patch(request, { description: "<p>An evening of <strong>jazz</strong>.</p>", description_format: "art" });
    const before = await stored(request);
    await telegramStub(page);
    const writes = trackWrites(page);
    await page.goto(await miniappLink(request, role, "description"));

    const text = box(page, "tg-description-field");
    await expect(text).toBeVisible({ timeout: 30_000 });
    await expect(text.locator("strong")).toHaveText("jazz");
    await expect(page.getByTestId("tg-edit-event")).toContainText(before.name);
    // The code left the address.
    expect(page.url()).not.toContain("code=");

    await page.locator('button[data-action="bulletList"]').click();
    await page.getByTestId("tg-description-save").click();
    await expect.poll(() => closedCount(page), { timeout: 20_000 }).toBe(1);

    expect(writes).toEqual([`PATCH /v1/organizations/${env.org_id}/events/${env.event_id}`]);
    const after = await stored(request);
    expect(after.description).toBe("<ul><li>An evening of <strong>jazz</strong>.</li></ul>");
    expect(after.description_format).toBe("art");
    // Only the description changed: the name and the letter text are as they were.
    expect(after.name).toBe(before.name);
    expect(after.email_note).toBe(before.email_note);
  });
}

test("the letter text opens, saves with the same call as the web card and closes the window", async ({ page, request }) => {
  await patch(request, { email_note: "<p>Bring a <strong>warm</strong> jacket.</p>", email_note_format: "art" });
  const before = await stored(request);
  await telegramStub(page);
  const writes = trackWrites(page);
  await page.goto(await miniappLink(request, "org_admin", "email_note"));
  const text = box(page, "email-note-field");
  await expect(text).toBeVisible({ timeout: 30_000 });
  await expect(text.locator("strong")).toHaveText("warm");
  await page.locator('button[data-action="bulletList"]').click();
  await page.getByTestId("email-note-save").click();
  await expect.poll(() => closedCount(page), { timeout: 20_000 }).toBe(1);
  expect(writes).toEqual([`PATCH /v1/organizations/${env.org_id}/events/${env.event_id}`]);
  const after = await stored(request);
  expect(after.email_note).toBe("<ul><li>Bring a <strong>warm</strong> jacket.</li></ul>");
  expect(after.email_note_format).toBe("art");
  expect(after.description).toBe(before.description);
});

test("a code works once: the second opening says the link does not work and to press the bot button again", async ({ browser, request }) => {
  const url = await miniappLink(request, "org_admin", "description");
  const first = await browser.newPage();
  await first.goto(url);
  await expect(box(first, "tg-description-field")).toBeVisible({ timeout: 30_000 });
  await first.close();
  const second = await browser.newPage();
  await second.goto(url);
  const error = second.getByTestId("tg-edit-error");
  await expect(error).toContainText("This link does not work any more", { timeout: 20_000 });
  await expect(error).toContainText("Edit in the editor");
  await expect(second.locator(".ProseMirror")).toHaveCount(0);
  await second.close();
});

test("an agent has no right to edit: the same calm line, no editor and no write", async ({ page, request }) => {
  const writes = trackWrites(page);
  await page.goto(await miniappLink(request, "agent", "description"));
  await expect(page.getByTestId("tg-edit-no-access")).toContainText("No access to this event", { timeout: 30_000 });
  await expect(page.locator(".ProseMirror")).toHaveCount(0);
  expect(writes).toEqual([]);
});

test("an event of another organization is 'no access', whatever the link says", async ({ page, request }) => {
  const writes = trackWrites(page);
  await page.goto(await miniappLink(request, "org_admin", "description", FOREIGN_EVENT_ID));
  await expect(page.getByTestId("tg-edit-no-access")).toContainText("No access to this event", { timeout: 30_000 });
  await expect(page.locator(".ProseMirror")).toHaveCount(0);
  expect(writes).toEqual([]);
});
