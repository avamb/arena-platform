/**
 * Twin test "Text in the letter" (spec 44, RT-11), on the roles of one organization, against a REAL
 * arena-api. It walks the scenario of the bot's screen (eventbot/textfmt_note.go and its e2e
 * bot_e2e_textfmt_note_integration_test.go): write the text with the editor, save it (ONE PATCH of the
 * event with `email_note` and `email_note_format: "art"`), try it out with the test letter to oneself
 * (a draft goes out, an empty one gets its own line), delete it after a question.
 *
 * The truth about the stored text comes from the API (the events list is the only route that carries
 * `email_note`), never from the screen. The test letter is a queued job here (the harness runs no worker),
 * so the screen's answer is what is proven: the 202 and its address. A 429 is a stand-in answer: the real
 * counter is five letters an hour per organization, in the memory of one process.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, richText, signInAs, type Role } from "./liveEnv";

const env = liveEnv();
const orgPath = (rest: string) => `${env.api_base_url}/v1/organizations/${env.org_id}${rest}`;
const bearer = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

interface ListedEvent {
  id: string;
  email_note: string | null;
  email_note_format: string | null;
}

async function storedNote(request: APIRequestContext): Promise<ListedEvent> {
  const res = await request.get(orgPath("/events"), { headers: bearer("org_admin") });
  expect(res.status(), "GET events").toBe(200);
  const body = (await res.json()) as { events: ListedEvent[] };
  const event = body.events.find((e) => e.id === env.event_id);
  if (event === undefined) throw new Error("the fixture event is not in the list");
  return event;
}

async function setNote(request: APIRequestContext, note: string | null) {
  const res = await request.patch(orgPath(`/events/${env.event_id}`), {
    headers: bearer("org_admin"),
    data: note === null ? { email_note: null } : { email_note: note, email_note_format: "art" },
  });
  expect(res.status(), "PATCH email_note").toBe(200);
}

function trackApi(page: Page): string[] {
  const calls: string[] = [];
  page.on("request", (req) => {
    const path = new URL(req.url()).pathname;
    if (req.url().startsWith(env.api_base_url) && req.method() !== "GET" && req.method() !== "OPTIONS" && !path.startsWith("/v1/auth/")) {
      calls.push(`${req.method()} ${path}`);
    }
  });
  return calls;
}

async function openEvent(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/events/${env.event_id}`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
}

const editor = (page: Page) => richText(page, "email-note-field");

test.beforeEach(async ({ request }) => {
  await setNote(request, null);
});

test.describe("text in the letter (live)", () => {
  for (const role of ["org_admin", "organizer"] as const) {
    test(`${role}: writes, saves, tries out and deletes the text`, async ({ page, request }) => {
      await openEvent(page, role);
      const section = page.getByTestId("email-note");
      await expect(section).toBeVisible();
      // The screen says plainly what the letter adds by itself, and offers no substitution.
      await expect(section).toContainText("adds the greeting, the date and the venue");
      await expect(section).not.toContainText("{name}");
      await expect(page.getByTestId("email-note-none")).toBeVisible();
      await expect(page.getByTestId("email-note-save")).toBeDisabled();
      await noA11yViolations(page);

      // Write a text, make the first line bold with the toolbar, and save.
      const writes = trackApi(page);
      // Bold with the keyboard shortcut for the first line (a selection made by the test and a click on the
      // toolbar race the editor's own selection tracking; the toolbar is covered by the library's tests).
      await editor(page).click();
      await page.keyboard.press("Control+B");
      await page.keyboard.type("Bring a warm jacket.");
      await page.keyboard.press("Control+B");
      await page.keyboard.press("Shift+Enter");
      await page.keyboard.type("Cloakroom is free.");
      await expect(page.getByTestId("email-note-unsaved")).toBeVisible();
      // Save only when the bold is really in the editor: the text on the server is what the person saw.
      await expect(editor(page).locator("strong")).toHaveText("Bring a warm jacket.");
      await page.getByTestId("email-note-save").click();
      await expect(page.getByTestId("email-note-status")).toContainText("Saved");
      expect(writes, "exactly one write").toEqual([`PATCH /v1/organizations/${env.org_id}/events/${env.event_id}`]);
      const stored = await storedNote(request);
      expect(stored.email_note_format).toBe("art");
      expect(stored.email_note).toBe("<p><strong>Bring a warm jacket.</strong><br>Cloakroom is free.</p>");
      await expect(page.getByTestId("email-note-none")).toHaveCount(0);

      // The test letter carries the draft and goes to the caller's own address.
      const sent = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith(`/events/${env.event_id}/email-note/test`));
      await page.getByTestId("email-note-test").click();
      const body = (await (await sent).postDataJSON()) as { note?: string; note_format?: string };
      expect(body.note_format).toBe("art");
      expect(body.note).toContain("Bring a warm jacket.");
      await expect(page.getByTestId("email-note-status")).toContainText(person(env, role).email);

      // Delete asks first; "Cancel" changes nothing, the confirmation removes the text.
      await page.getByTestId("email-note-delete").click();
      await expect(page.getByTestId("email-note-delete-dialog")).toContainText("without this text");
      await page.getByTestId("email-note-delete-dialog-cancel").click();
      expect((await storedNote(request)).email_note).not.toBeNull();
      await page.getByTestId("email-note-delete").click();
      await page.getByTestId("email-note-delete-dialog-submit").click();
      await expect(page.getByTestId("email-note-status")).toContainText("deleted");
      expect((await storedNote(request)).email_note).toBeNull();
      await expect(editor(page)).toHaveText("");
    });
  }

  test("a text saved by an older client (plain text) is shown as formatted text and saved back as ART", async ({ page, request }) => {
    const res = await request.patch(orgPath(`/events/${env.event_id}`), {
      headers: bearer("org_admin"),
      data: { email_note: "First line\n\n- one\n- two" },
    });
    expect(res.status()).toBe(200);
    await openEvent(page, "org_admin");
    // The list says what the format is; the editor shows paragraphs and a list, not the marks.
    await expect(editor(page).locator("li")).toHaveCount(2);
    await expect(editor(page)).not.toContainText("- one");
  });

  test("an empty draft with nothing saved gets its own line, not a generic failure", async ({ page }) => {
    await openEvent(page, "org_admin");
    await page.getByTestId("email-note-test").click();
    await expect(page.getByTestId("email-note-error")).toContainText("There is no text to send");
  });

  test("the limit of five test letters an hour has its own line", async ({ page }) => {
    await openEvent(page, "org_admin");
    await page.route("**/email-note/test", (route) =>
      route.fulfill({
        status: 429,
        json: { error: { code: "event.email_note_test_rate_limited", message: "too many", request_id: "e2e" } },
      }),
    );
    await editor(page).fill("A text");
    await page.getByTestId("email-note-test").click();
    await expect(page.getByTestId("email-note-error")).toContainText("Too many test letters");
  });

  test("agent: has no right to edit the event and does not see the screen", async ({ page }) => {
    await openEvent(page, "agent");
    await expect(page.getByTestId("email-note")).toHaveCount(0);
  });
});
