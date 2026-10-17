/**
 * Twin test "poster first" (Event Center: poster-first creation with confirmed
 * recognition hints), against a REAL arena-api with a LOCAL media storage on a
 * migrated scratch database.
 *
 * HONEST LABEL OF WHAT IS FAKE: only the vision model. arena-api is built with the
 * test-only tag `e2eposter` (apps/backend/cmd/arena-api/poster_reader_e2e.go), which
 * injects a scripted reader through the existing `Options.PosterReader` seam. The
 * route `POST /v1/organizations/{org}/media/{media}/poster-facts` itself (auth, org
 * access, event.update, media lookup, quota, reading the stored bytes) and the
 * upload, the draft creation and the attach are all real. No Anthropic key, no
 * network call to a model, in this harness.
 *
 * Serial: scenarios share one organization and one scripted reader.
 */
import { expect, test, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";
import { skipPoster } from "./wizardHelpers";
import {
  auth,
  base,
  env,
  events,
  expectStored,
  file,
  makeImage,
  named,
  mediaOf,
  reads,
  scriptFacts,
  scriptFailure,
  scriptNotConfigured,
  sha256,
  trackWrites,
  venueId,
} from "./posterHelpers";

test.describe.configure({ mode: "serial" });
void liveEnv;
const nonce = Date.now().toString(36);

async function openWizard(page: Page, role: Role = "org_admin") {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`), { timeout: 20_000 });
  await page.getByTestId("events-create").click();
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/new`));
  await expect(page.getByTestId("wizard-poster")).toBeVisible();
}

async function statusOk(page: Page) {
  await expect(page.getByTestId("wizard-status")).toContainText("Everything you filled in is correct", { timeout: 25_000 });
}

async function fillManualBasics(page: Page, name: string) {
  await page.getByTestId("wizard-name").fill(name);
  await page.getByTestId("wizard-venue-mode-existing").check();
  await page.getByTestId("wizard-venue-pick").selectOption({ label: "E2E Hall" });
  await page.getByTestId("wizard-currency").fill("EUR");
  const box = page.getByTestId("wizard-date-0-day");
  await box.getByTestId("wizard-date-0-day-toggle").click();
  await box.getByTestId("date-quick-week").click();
  await page.getByTestId("wizard-date-0-time").fill("19:00");
  await page.getByTestId("wizard-cat-0-0-price").fill("18,90");
}

async function chooseImage(page: Page, hue: number, mime: "image/jpeg" | "image/png" = "image/jpeg") {
  const img = await makeImage(page, mime, 600, 900, hue);
  await page.getByTestId("wizard-poster-picker-input").setInputFiles(file(img));
  return img;
}

test.afterAll(async ({ request }) => {
  for (const e of (await events(request)).filter((x) => x.name.startsWith("Poster First ") && x.name.endsWith(nonce) && x.status === "draft")) {
    const del = await request.delete(`${base}/events/${e.id}`, { headers: auth("org_admin") });
    expect(del.status(), `cleanup of ${e.name}`).toBeLessThan(300);
  }
});

test.describe("the poster is the first, conscious decision", () => {
  test("it comes first, says what it is for, offers two explicit ways, and blocks creating until one is chosen", async ({ page }) => {
    await openWizard(page);
    const poster = page.getByTestId("wizard-poster");
    await expect(poster.locator("legend")).toHaveText("Poster");
    await expect(poster).not.toContainText("optional");
    await expect(poster).toContainText("Upload a poster — we will offer to fill in the event details.");
    // First block of the form: above the event name.
    const posterBox = (await poster.boundingBox()) as { y: number };
    const eventBox = (await page.getByTestId("wizard-event").boundingBox()) as { y: number };
    expect(posterBox.y).toBeLessThan(eventBox.y);
    // Two alternatives.
    await expect(page.getByTestId("wizard-poster-picker-choose")).toHaveText("Upload a poster");
    await expect(page.getByTestId("wizard-poster-skip")).toHaveText("Continue without a poster");
    await expect(page.getByTestId("wizard-poster-skipped")).toHaveCount(0);

    // A complete, valid form still cannot be created before the decision.
    await fillManualBasics(page, `Poster First Gate ${nonce}`);
    await statusOk(page);
    await expect(page.getByTestId("wizard-create")).toBeDisabled();
    await expect(page.getByTestId("wizard-create-hint")).toContainText("Upload a poster or choose «Continue without a poster»");
    await noA11yViolations(page);

    // The skip is a keyboard-reachable, deliberate action and shows the reminder.
    await page.getByTestId("wizard-poster-skip").focus();
    await expect(page.getByTestId("wizard-poster-skip")).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(page.getByTestId("wizard-poster-skipped")).toHaveText("No poster added. You can upload it later in the event card.");
    await expect(page.getByTestId("wizard-create")).toBeEnabled();
    await expect(page.getByTestId("wizard-poster-skip")).toHaveCount(0);
  });

  test("skip: reminder, a draft is created without a poster, with no upload and no read; draft off sale", async ({ page, request }) => {
    const name = `Poster First Skip ${nonce}`;
    const readsBefore = reads().length;
    const writes = trackWrites(page);
    await openWizard(page);
    await fillManualBasics(page, name);
    await skipPoster(page);
    await statusOk(page);
    await page.getByTestId("wizard-create").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/[0-9a-f-]{36}$`), { timeout: 30_000 });
    expect(writes).toEqual({ upload: 0, attach: 0, create: 1, read: 0 });
    expect(reads().length).toBe(readsBefore);
    const rows = await named(request, name);
    expect(rows).toHaveLength(1);
    expect(rows[0].status).toBe("draft");
    expect(rows[0].poster_media_id).toBeNull();
    await expect(page.getByTestId("poster-none")).toBeVisible();
  });

  test("the decision survives a reload; an old draft keeps its work and is asked once", async ({ page }) => {
    await openWizard(page);
    await page.getByTestId("wizard-name").fill(`Poster First Reload ${nonce}`);
    await skipPoster(page);
    await page.reload();
    await expect(page.getByTestId("wizard-name")).toHaveValue(`Poster First Reload ${nonce}`);
    await expect(page.getByTestId("wizard-poster-skipped")).toBeVisible();

    // A draft stored by the previous release (version 3, no decision): the typed work stays, the decision is asked.
    const key = `arena.events.wizard.${env.org_id}`;
    await page.evaluate((k) => {
      const d = JSON.parse(localStorage.getItem(k) as string);
      delete d.posterDecision;
      d.version = 3;
      d.event.name = "Typed before the upgrade";
      d.event.description = "Kept description";
      localStorage.setItem(k, JSON.stringify(d));
    }, key);
    await page.reload();
    await expect(page.getByTestId("wizard-name")).toHaveValue("Typed before the upgrade");
    await expect(page.getByTestId("wizard-description")).toHaveValue("Kept description");
    await expect(page.getByTestId("wizard-poster-skip")).toBeVisible();
    await expect(page.getByTestId("wizard-poster-skipped")).toHaveCount(0);
    await expect(page.getByTestId("wizard-create")).toBeDisabled();

    // And a version-2 draft (no poster block at all) too.
    await page.evaluate((k) => {
      const d = JSON.parse(localStorage.getItem(k) as string);
      d.version = 2;
      delete d.poster;
      localStorage.setItem(k, JSON.stringify(d));
    }, key);
    await page.reload();
    await expect(page.getByTestId("wizard-name")).toHaveValue("Typed before the upgrade");
    await expect(page.getByTestId("wizard-poster-skip")).toBeVisible();
    await page.getByTestId("wizard-clear").click();
  });

  test("late upload after a skip: the poster is uploaded, the reminder goes, nothing is read by itself", async ({ page }) => {
    const writes = trackWrites(page);
    const readsBefore = reads().length;
    await openWizard(page);
    await skipPoster(page);
    await chooseImage(page, 40);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("wizard-poster-skipped")).toHaveCount(0);
    await expect(page.getByTestId("wizard-poster")).toHaveAttribute("data-decision", "poster");
    expect(writes.upload).toBe(1);
    expect(writes.read).toBe(0);
    expect(reads().length).toBe(readsBefore);
    await page.getByTestId("wizard-clear").click();
  });
});

test.describe("upload once, review the hints, apply on purpose", () => {
  test("JPEG upload -> poster-facts route -> review -> explicit apply -> manual corrections -> create: one event, the same media attached, after reload", async ({
    page,
    request,
  }) => {
    const finalName = `Poster First Happy ${nonce}`;
    const writes = trackWrites(page);
    const readsBefore = reads().length;
    const hall = await venueId(request);
    await openWizard(page);

    // Existing typed text is not overwritten silently.
    await page.getByTestId("wizard-description").fill("My own description");

    // 1. Upload happens at once; the stored bytes are exactly the chosen ones; nothing is read yet.
    const img = await chooseImage(page, 10);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("wizard-poster-picker-preview")).toBeVisible();
    expect(writes).toMatchObject({ upload: 1, create: 0, attach: 0, read: 0 });
    expect(reads().length).toBe(readsBefore);
    const stored = JSON.parse(await page.evaluate((k) => localStorage.getItem(k) as string, `arena.events.wizard.${env.org_id}`)) as {
      poster: { mediaId: string };
      posterDecision: string;
    };
    const mediaId = stored.poster.mediaId;
    expect(mediaId).toMatch(/^[0-9a-f-]{36}$/);
    expect(stored.posterDecision).toBe("poster");
    await expectStored(request, mediaId, img);
    // The browser kept no file, blob address, link, or hint text.
    const raw = await page.evaluate(() => JSON.stringify({ ...localStorage }));
    for (const bad of ["blob:", "data:image", "Bearer ", "Swan Lake"]) expect(raw).not.toContain(bad);

    // 2. The person is told the picture is sent for recognition BEFORE the paid request.
    const note = page.getByTestId("wizard-poster-read");
    await expect(note).toContainText("sent to an automatic text recognition service");

    // 3. Read it: the scripted model answers (the real route runs).
    scriptFacts({
      name: "Swan Lake",
      date: "2031-03-05",
      time: "19:30",
      venue_name: "E2E Hall",
      city: "Madrid",
      age: "12+",
      description: "Poster description",
      categories: [
        { name: "Stalls", price_minor: 3490, currency: "EUR" },
        { name: "Balcony", price_minor: 2000, currency: "EUR" },
      ],
    });
    await page.getByTestId("wizard-poster-read-button").dblclick();
    const review = page.getByTestId("poster-review");
    await expect(review).toBeVisible({ timeout: 20_000 });
    expect(writes.read).toBe(1); // a double click is ONE paid request
    const mine = reads().slice(readsBefore);
    expect(mine).toHaveLength(1);
    expect(mine[0].sha256).toBe(sha256(img.buffer)); // the model boundary got exactly the uploaded bytes

    // 4. Nothing was applied by the response itself.
    await expect(page.getByTestId("wizard-name")).toHaveValue("");
    await expect(page.getByTestId("wizard-age")).toHaveValue("");
    await expect(page.getByTestId("wizard-date-0-day-value")).toHaveAttribute("data-value", "");
    // The currency field only exists once a venue is chosen: nothing chose one.
    await expect(page.getByTestId("wizard-currency")).toHaveCount(0);
    await expect(page.getByTestId("wizard-venue-pick")).toHaveCount(0);
    await noA11yViolations(page);

    // Typed text is a replacement that is off by default; empty fields are on.
    await expect(page.getByTestId("poster-review-row-description")).not.toBeChecked();
    await expect(page.getByTestId("poster-review-replaces-description")).toContainText("My own description");
    await expect(page.getByTestId("poster-review-row-name")).toBeChecked();
    // A venue is chosen on purpose from the organization's list, never from the name alone; a city is not applied.
    await expect(page.getByTestId("poster-review-venue-none")).toBeChecked();
    // With a candidate, the poster's city is not offered or applied at all (it would only be a hint without one).
    await expect(review).not.toContainText("Choose the country and the city");
    // A price needs an explicit category choice; the currency change is announced.
    await expect(page.getByTestId("poster-review-price-none")).toBeChecked();
    await expect(review).toContainText("Add the others in the tickets section yourself");
    // Model text is plain text: a script in a value would not become markup (checked by the unit tests too).
    await expect(page.getByTestId("poster-review-value-name")).toHaveText("Swan Lake");
    await expect(page.getByTestId("poster-review-value-date")).toHaveText("March 5, 2031");

    await page.getByTestId(`poster-review-venue-${hall}`).check();
    await page.getByTestId("poster-review-price-0").check();
    await expect(review).toContainText("The event currency will be set to EUR.");
    await page.getByTestId("poster-review-apply").click();

    // 5. Only the selected things were filled in.
    await expect(page.getByTestId("wizard-poster-read-done")).toContainText("Filled in from the poster");
    await expect(page.getByTestId("wizard-name")).toHaveValue("Swan Lake");
    await expect(page.getByTestId("wizard-age")).toHaveValue("12+");
    await expect(page.getByTestId("wizard-description")).toHaveValue("My own description");
    await expect(page.getByTestId("wizard-date-0-day-value")).toHaveAttribute("data-value", "2031-03-05");
    await expect(page.getByTestId("wizard-date-0-time")).toHaveValue("19:30");
    await expect(page.getByTestId("wizard-venue-pick")).toContainText("E2E Hall");
    await expect(page.getByTestId("wizard-currency")).toHaveValue("EUR");
    await expect(page.getByTestId("wizard-cat-0-0-name")).toHaveValue("Stalls");
    await expect(page.getByTestId("wizard-cat-0-0-price")).toHaveValue("34.90");
    await expect(page.getByTestId("wizard-tickets-0-mode-single")).toBeChecked();
    expect(reads().length - readsBefore).toBe(1);

    // 6. Manual corrections after the apply.
    await page.getByTestId("wizard-name").fill(finalName);
    await page.getByTestId("wizard-date-0-time").fill("20:00");
    await page.getByTestId("wizard-cat-0-0-price").fill("35,00");
    await statusOk(page);

    // 7. Create: ONE event, the already uploaded media attached, no second upload, no second read.
    await page.getByTestId("wizard-create").dblclick();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/[0-9a-f-]{36}$`), { timeout: 30_000 });
    expect(writes).toEqual({ upload: 1, attach: 1, create: 1, read: 1 });
    const rows = await named(request, finalName);
    expect(rows).toHaveLength(1);
    expect(rows[0].status).toBe("draft");
    expect(rows[0].poster_media_id).toBe(mediaId);
    await expectStored(request, mediaId, img);
    expect((await events(request)).filter((e) => e.name === "Swan Lake")).toHaveLength(0);

    // Date and time are the venue's wall clock (Europe/Madrid), price and currency as corrected, off sale.
    const sessions = await request.get(`${base}/events/${rows[0].id}/sessions`, { headers: auth("org_admin") });
    const list = ((await sessions.json()) as { sessions: { id: string; status: string; start_at: string }[] }).sessions;
    expect(list).toHaveLength(1);
    expect(list[0].status).toBe("draft");
    const wall = new Intl.DateTimeFormat("en-GB", {
      timeZone: "Europe/Madrid",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    }).format(new Date(list[0].start_at));
    expect(wall).toBe("05/03/2031, 20:00");
    const tiers = await request.get(`${base}/events/${rows[0].id}/sessions/${list[0].id}/tiers`, { headers: auth("org_admin") });
    const tierList = ((await tiers.json()) as { tiers: { name: string; price_amount: number; currency: string }[] }).tiers;
    expect(tierList).toHaveLength(1);
    expect(tierList[0]).toMatchObject({ name: "Stalls", price_amount: 3500, currency: "EUR" });
    // Off sale is the DRAFT status of the event and its session (asserted above), as for every wizard creation.

    // After a reload the same poster is still the event's poster.
    await page.reload();
    const shown = page.getByTestId("poster-image");
    await expect(shown).toBeVisible({ timeout: 20_000 });
    await expect(shown).toHaveAttribute("data-media-id", mediaId);
    // The draft is gone and nothing from the review is left in the browser.
    expect(await page.evaluate(() => Object.keys(localStorage).filter((k) => k.includes("wizard")))).toEqual([]);
    expect((await mediaOf(request, mediaId)).checksum_sha256).toBe(sha256(img.buffer));
  });

  test("a venue the organization does not have is only a hint: no venue is created or chosen, the city waits for a country", async ({ page, request }) => {
    const venuesBefore = ((await (await request.get(`${base}/venues`, { headers: auth("org_admin") })).json()) as { venues: unknown[] }).venues.length;
    await openWizard(page);
    await chooseImage(page, 60);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    // The model's words are untrusted text: markup in them must stay plain characters.
    const hostile = 'Palace Nobody Registered <img src=x onerror="window.__xss=1">';
    scriptFacts({ venue_name: hostile, city: "Madrid" });
    await page.getByTestId("wizard-poster-read-button").click();
    const review = page.getByTestId("poster-review");
    await expect(review).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("poster-review-venue")).toHaveCount(0);
    await expect(page.getByTestId("poster-review-value-venueNew")).toHaveText(hostile);
    expect(await page.getByTestId("poster-review").locator("img").count()).toBe(0);
    expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss)).toBeUndefined();
    await expect(review).toContainText("The poster names the city «Madrid». Choose the country and the city in the venue section.");
    await page.getByTestId("poster-review-apply").click();
    // The name went to the NEW venue form only: no existing venue is chosen, and the organization has no new venue.
    await expect(page.getByTestId("wizard-venue-mode-new")).toBeChecked();
    await expect(page.getByTestId("wizard-venue-name")).toHaveValue(hostile);
    expect(((await (await request.get(`${base}/venues`, { headers: auth("org_admin") })).json()) as { venues: unknown[] }).venues.length).toBe(venuesBefore);
    await page.getByTestId("wizard-clear").click();
  });

  test("already typed date, time, venue and currency are never replaced silently; a different currency is not applied", async ({ page, request }) => {
    const readsBefore = reads().length;
    await openWizard(page);
    await fillManualBasics(page, `Poster First Typed ${nonce}`);
    // The typed date (a week ahead), 19:00, E2E Hall, EUR and 18,90 are all in place.
    const typedDay = await page.getByTestId("wizard-date-0-day-value").getAttribute("data-value");
    await chooseImage(page, 80);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    scriptFacts({
      name: "Different Name",
      date: "2031-06-07",
      time: "21:15",
      venue_name: "E2E Hall",
      city: "Madrid",
      age: "16+",
      categories: [{ name: "Floor", price_minor: 5000, currency: "USD" }],
    });
    await page.getByTestId("wizard-poster-read-button").click();
    const review = page.getByTestId("poster-review");
    await expect(review).toBeVisible({ timeout: 20_000 });
    expect(reads().length - readsBefore).toBe(1);

    // Conflicts are named and OFF; empty age is ON.
    for (const key of ["name", "date", "time"]) {
      await expect(page.getByTestId(`poster-review-row-${key}`)).not.toBeChecked();
      await expect(page.getByTestId(`poster-review-replaces-${key}`)).toBeVisible();
    }
    await expect(page.getByTestId("poster-review-row-age")).toBeChecked();
    // The venue that is already chosen needs no candidate; a different currency blocks the price.
    await expect(page.getByTestId("poster-review-venue")).toHaveCount(0);
    await expect(page.getByTestId("poster-review-price")).toHaveCount(0);
    await expect(page.getByTestId("poster-review-notes")).toContainText("The poster prices are in USD, but the event currency is EUR");

    // Apply with the defaults: only the age changes.
    await page.getByTestId("poster-review-apply").click();
    await expect(page.getByTestId("wizard-age")).toHaveValue("16+");
    await expect(page.getByTestId("wizard-name")).toHaveValue(`Poster First Typed ${nonce}`);
    await expect(page.getByTestId("wizard-date-0-day-value")).toHaveAttribute("data-value", typedDay ?? "");
    await expect(page.getByTestId("wizard-date-0-time")).toHaveValue("19:00");
    await expect(page.getByTestId("wizard-currency")).toHaveValue("EUR");
    await expect(page.getByTestId("wizard-cat-0-0-price")).toHaveValue("18,90");

    // Read again (explicit, one more read) and confirm ONE replacement on purpose: only the time changes.
    await page.getByTestId("wizard-poster-read-again").click();
    await expect(page.getByTestId("poster-review")).toBeVisible({ timeout: 20_000 });
    expect(reads().length - readsBefore).toBe(2);
    await page.getByTestId("poster-review-row-time").check();
    await page.getByTestId("poster-review-apply").click();
    await expect(page.getByTestId("wizard-date-0-time")).toHaveValue("21:15");
    await expect(page.getByTestId("wizard-name")).toHaveValue(`Poster First Typed ${nonce}`);
    await expect(page.getByTestId("wizard-date-0-day-value")).toHaveAttribute("data-value", typedDay ?? "");
    await page.getByTestId("wizard-clear").click();
    void request;
  });

  test("text typed AFTER the review appeared is not replaced by an earlier tick", async ({ page }) => {
    await openWizard(page);
    await chooseImage(page, 120);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    scriptFacts({ name: "From the poster" });
    await page.getByTestId("wizard-poster-read-button").click();
    await expect(page.getByTestId("poster-review-row-name")).toBeChecked({ timeout: 20_000 });
    await page.getByTestId("wizard-name").fill("Typed while the review was open");
    // The row turned into a replacement and is off again.
    await expect(page.getByTestId("poster-review-row-name")).not.toBeChecked();
    await expect(page.getByTestId("poster-review-apply")).toBeDisabled();
    await page.getByTestId("poster-review-dismiss").click();
    await expect(page.getByTestId("wizard-name")).toHaveValue("Typed while the review was open");
    await expect(page.getByTestId("wizard-poster-read-done")).toContainText("Nothing was filled in");
    await page.getByTestId("wizard-clear").click();
  });
});

test.describe("a poster that cannot be read never blocks the form and never loses the picture", () => {
  test("a failed read: the picture and the typed text stay, retry is explicit, manual creation works", async ({ page, request }) => {
    const name = `Poster First Fail ${nonce}`;
    const writes = trackWrites(page);
    const readsBefore = reads().length;
    await openWizard(page);
    await page.getByTestId("wizard-description").fill("Typed before the failure");
    const img = await chooseImage(page, 200);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    scriptFailure();
    await page.getByTestId("wizard-poster-read-button").click();
    const failed = page.getByTestId("wizard-poster-read-failed");
    await expect(failed).toBeVisible({ timeout: 20_000 });
    await expect(failed).toHaveAttribute("data-reason", "unavailable");
    await expect(failed).toContainText("could not be read right now");
    expect(writes.read).toBe(1);
    // Nothing retries by itself.
    await page.waitForTimeout(1500);
    expect(writes.read).toBe(1);
    expect(reads().length - readsBefore).toBe(1);
    // The picture and the typed text are intact.
    await expect(page.getByTestId("wizard-poster-picker-preview")).toBeVisible();
    await expect(page.getByTestId("wizard-description")).toHaveValue("Typed before the failure");
    // An explicit "read again" makes exactly one more read (and fails the same way here).
    await page.getByTestId("wizard-poster-read-again").click();
    await expect(failed).toBeVisible();
    await expect.poll(() => reads().length - readsBefore).toBe(2);
    expect(writes.read).toBe(2);
    // Continue by hand and create: the event gets the uploaded poster.
    await page.getByTestId("wizard-poster-read-manual").click();
    await expect(page.getByTestId("wizard-poster-read-done")).toContainText("Nothing was filled in");
    await fillManualBasics(page, name);
    await statusOk(page);
    await page.getByTestId("wizard-create").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/[0-9a-f-]{36}$`), { timeout: 30_000 });
    const rows = await named(request, name);
    expect(rows).toHaveLength(1);
    expect(writes).toMatchObject({ upload: 1, create: 1, attach: 1, read: 2 });
    await expectStored(request, rows[0].poster_media_id as string, img);
  });

  test("the reading service is not set up: said plainly, no retry button, the poster stays", async ({ page }) => {
    await openWizard(page);
    await chooseImage(page, 220);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    scriptNotConfigured();
    await page.getByTestId("wizard-poster-read-button").click();
    const failed = page.getByTestId("wizard-poster-read-failed");
    await expect(failed).toHaveAttribute("data-reason", "unconfigured", { timeout: 20_000 });
    await expect(failed).toContainText("Reading posters is not available now");
    await expect(page.getByTestId("wizard-poster-read-again")).toHaveCount(0);
    await expect(page.getByTestId("wizard-poster-picker-preview")).toBeVisible();
    await page.getByTestId("wizard-poster-read-manual").click();
    await page.getByTestId("wizard-clear").click();
  });

  test("daily limit and missing permission (SIMULATED answers of the route) are said plainly: no retry, picture and form stay", async ({ page }) => {
    // These two answers are produced here, not by the server: the real route's 429/403 are covered by its Go tests
    // and by the foreign-organization/agent checks below; this proves what the SCREEN does with them.
    let answer: { status: number; code: string } = { status: 429, code: "media.poster_reads_exhausted" };
    await page.route(/\/poster-facts$/, (route) =>
      route.fulfill({ status: answer.status, json: { error: { code: answer.code, message: "simulated", request_id: "sim" } } }),
    );
    await openWizard(page);
    await page.getByTestId("wizard-name").fill("Mine while the limit is reached");
    await chooseImage(page, 270);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    await page.getByTestId("wizard-poster-read-button").click();
    const failed = page.getByTestId("wizard-poster-read-failed");
    await expect(failed).toHaveAttribute("data-reason", "quota");
    await expect(failed).toContainText("used today's poster readings");
    await expect(page.getByTestId("wizard-poster-read-again")).toHaveCount(0);
    await expect(page.getByTestId("wizard-poster-picker-preview")).toBeVisible();
    await expect(page.getByTestId("wizard-name")).toHaveValue("Mine while the limit is reached");
    // Continue by hand, then try a reading again from the "done" state: denied.
    await page.getByTestId("wizard-poster-read-manual").click();
    answer = { status: 403, code: "forbidden" };
    await page.getByTestId("wizard-poster-read-again").click();
    await expect(page.getByTestId("wizard-poster-read-failed")).toHaveAttribute("data-reason", "denied");
    await expect(page.getByTestId("wizard-poster-read-again")).toHaveCount(0);
    await expect(page.getByTestId("wizard-create-hint")).not.toContainText("poster");
  });

  test("a poster with nothing usable on it says so and leaves the form alone", async ({ page }) => {
    await openWizard(page);
    await page.getByTestId("wizard-name").fill("Mine");
    await chooseImage(page, 250);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    scriptFacts({});
    await page.getByTestId("wizard-poster-read-button").click();
    await expect(page.getByTestId("wizard-poster-read-nothing")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("wizard-name")).toHaveValue("Mine");
    await page.getByTestId("wizard-clear").click();
  });

  test("an upload that fails is its own failure: the form is kept, retry works, and no read happens", async ({ page }) => {
    const readsBefore = reads().length;
    await openWizard(page);
    await page.getByTestId("wizard-name").fill("Kept after upload failure");
    let failOnce = true;
    await page.route(/\/v1\/media$/, async (route) => {
      if (route.request().method() === "POST" && failOnce) {
        failOnce = false;
        await route.abort("failed");
        return;
      }
      await route.continue();
    });
    await chooseImage(page, 300);
    const failed = page.getByTestId("wizard-poster-upload-failed");
    await expect(failed).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("wizard-poster-picker-preview")).toBeVisible();
    await expect(page.getByTestId("wizard-name")).toHaveValue("Kept after upload failure");
    await expect(page.getByTestId("wizard-poster-read-button")).toHaveCount(0);
    await expect(page.getByTestId("wizard-create")).toBeDisabled();
    await page.getByTestId("wizard-poster-upload-retry").click();
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    expect(reads().length).toBe(readsBefore);
    await page.getByTestId("wizard-clear").click();
  });
});

test.describe("stale answers and a reloaded draft", () => {
  test("a slow read of picture A cannot fill the review after picture B was chosen", async ({ page }) => {
    const readsBefore = reads().length;
    await openWizard(page);
    await chooseImage(page, 20);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    scriptFacts({ name: "Answer for picture A" }, 2500);
    await page.getByTestId("wizard-poster-read-button").click();
    await expect(page.getByTestId("wizard-poster-reading")).toBeVisible();
    // The person chooses another picture while the first is being read.
    await chooseImage(page, 160, "image/png");
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("wizard-poster-read-button")).toBeVisible();
    await page.waitForTimeout(3500);
    await expect(page.getByTestId("poster-review")).toHaveCount(0);
    await expect(page.getByTestId("wizard-name")).toHaveValue("");
    // A request was made for A (it cannot be un-sent), none for B.
    expect(reads().length - readsBefore).toBe(1);
    await page.getByTestId("wizard-clear").click();
  });

  test("removing the poster or skipping while a read is running drops its answer", async ({ page }) => {
    await openWizard(page);
    await chooseImage(page, 30);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    scriptFacts({ name: "Late answer" }, 2000);
    await page.getByTestId("wizard-poster-read-button").click();
    await expect(page.getByTestId("wizard-poster-reading")).toBeVisible();
    await page.getByTestId("wizard-poster-picker-remove").click();
    await expect(page.getByTestId("wizard-poster-skip")).toBeVisible();
    await page.getByTestId("wizard-poster-skip").click();
    await page.waitForTimeout(3000);
    await expect(page.getByTestId("poster-review")).toHaveCount(0);
    await expect(page.getByTestId("wizard-poster-read")).toHaveCount(0);
    await expect(page.getByTestId("wizard-name")).toHaveValue("");
    await page.getByTestId("wizard-clear").click();
  });

  test("after a reload the uploaded picture is kept and is NOT read again by itself", async ({ page, request }) => {
    const readsBefore = reads().length;
    await openWizard(page);
    const img = await chooseImage(page, 330);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    const writes = trackWrites(page);
    await page.reload();
    await expect(page.getByTestId("wizard-poster-uploaded")).toBeVisible();
    // The stored picture is shown again (a signed link used once on the API origin).
    const shown = page.getByTestId("wizard-poster-restored");
    await expect(shown).toBeVisible({ timeout: 20_000 });
    expect(await shown.evaluate((i) => (i as HTMLImageElement).naturalWidth)).toBe(img.width);
    await expect(page.getByTestId("wizard-poster")).toHaveAttribute("data-decision", "poster");
    await expect(page.getByTestId("wizard-poster-read-button")).toBeVisible();
    await page.waitForTimeout(800);
    expect(writes.read).toBe(0);
    expect(writes.upload).toBe(0);
    expect(reads().length).toBe(readsBefore);
    // Creating reuses the stored media: no upload at all.
    const name = `Poster First Reloaded ${nonce}`;
    await fillManualBasics(page, name);
    await statusOk(page);
    await page.getByTestId("wizard-create").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/[0-9a-f-]{36}$`), { timeout: 30_000 });
    expect(writes.upload).toBe(0);
    const rows = await named(request, name);
    expect(rows).toHaveLength(1);
    await expectStored(request, rows[0].poster_media_id as string, img);
    expect(reads().length).toBe(readsBefore);
  });
});

test.describe("the real route refuses what it must", () => {
  const readUrl = (org: string, media: string) => `${env.api_base_url}/v1/organizations/${org}/media/${media}/poster-facts`;

  test("without event.update, for a foreign organization, for foreign or missing media, and without a session: refused before the model", async ({
    page,
    request,
  }) => {
    scriptFacts({ name: "Must never be returned" });
    // A real own poster (uploaded as the owner through the page).
    await openWizard(page);
    await chooseImage(page, 5);
    await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
    const mediaId = (
      JSON.parse(await page.evaluate((k) => localStorage.getItem(k) as string, `arena.events.wizard.${env.org_id}`)) as { poster: { mediaId: string } }
    ).poster.mediaId;
    const before = reads().length;

    // The agent has no event.update: refused.
    const agent = await request.post(readUrl(env.org_id, mediaId), { headers: auth("agent") });
    expect([401, 403]).toContain(agent.status());
    // No session at all.
    expect((await request.post(readUrl(env.org_id, mediaId))).status()).toBe(401);
    // Media of ANOTHER organization read through our org path: a 404, as if it did not exist.
    expect((await request.post(readUrl(env.org_id, env.foreign_media_id), { headers: auth("org_admin") })).status()).toBe(404);
    // A missing media id.
    expect((await request.post(readUrl(env.org_id, "0199aaaa-0000-7000-8000-00000000dead"), { headers: auth("org_admin") })).status()).toBe(404);
    // Our media through a foreign organization's path: no access to that organization.
    expect([403, 404]).toContain((await request.post(readUrl("fe0000e2-0000-7000-8000-0000000000b2", mediaId), { headers: auth("org_admin") })).status());
    // Nothing above reached the model boundary.
    expect(reads().length).toBe(before);

    // The owner and the manager (event.update) are served.
    const owner = await request.post(readUrl(env.org_id, mediaId), { headers: auth("org_admin") });
    expect(owner.status(), await owner.text()).toBe(200);
    expect(((await owner.json()) as { facts: { name: string } }).facts.name).toBe("Must never be returned");
    expect((await request.post(readUrl(env.org_id, mediaId), { headers: auth("organizer") })).status()).toBe(200);
    expect(reads().length - before).toBe(2);
    await page.getByTestId("wizard-clear").click();
  });

  test("the wizard of a person without media.write is not blocked by the poster step", async ({ page }) => {
    const owner = person(env, "org_admin");
    const me = await (await page.request.get(`${base}/me`, { headers: { Authorization: `Bearer ${owner.access_token}` } })).json();
    await page.route(`${base}/me`, async (route) => {
      await route.fulfill({ json: { ...me, permissions: (me.permissions as string[]).filter((p) => p !== "media.write") } });
    });
    await openWizard(page);
    await expect(page.getByTestId("wizard-poster-no-permission")).toBeVisible();
    await expect(page.getByTestId("wizard-poster-skip")).toHaveCount(0);
    await expect(page.getByTestId("wizard-poster-picker-input")).toHaveCount(0);
    await fillManualBasics(page, `Poster First NoWrite ${nonce}`);
    await statusOk(page);
    await expect(page.getByTestId("wizard-create")).toBeEnabled();
    await page.getByTestId("wizard-clear").click();
  });
});

test.describe("an existing event's poster is replaced without reading it", () => {
  test("replacing the poster on the event card makes no read and changes no event data", async ({ page, request }) => {
    const name = `Poster First Replace ${nonce}`;
    const venue = await venueId(request);
    const created = await request.post(`${base}/imports/event-bundle`, {
      headers: auth("org_admin"),
      data: {
        source: "arena",
        externalRef: `web:${env.org_id}:poster-first-${nonce}:1`,
        action: { actionName: name, description: "Original description", age: "6+", promoterId: "" },
        actionEvent: { day: "05.12.2030", time: "19:00", currency: "EUR" },
        venue: { arenaVenueId: venue },
        categoryList: [{ categoryPriceName: "Admission", price: 10, availability: 50, nextCategoryIndex: -1, priceSchedule: [] }],
        publish: false,
      },
    });
    expect(created.status(), await created.text()).toBeLessThan(300);
    const eventId = ((await created.json()) as { event_id: string }).event_id;
    scriptFacts({ name: "Must not be applied to an existing event", date: "2033-01-01" });
    const readsBefore = reads().length;
    const writes = trackWrites(page);
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`), { timeout: 20_000 });
    await page.goto(`/o/${env.org_id}/events/${eventId}`);
    const img = await makeImage(page, "image/jpeg", 500, 700, 99);
    await page.getByTestId("poster-picker-input").setInputFiles(file(img));
    await expect(page.getByTestId("poster-picker-preview")).toBeVisible();
    await page.getByTestId("poster-save").click();
    await expect(page.getByTestId("poster-image")).toBeVisible({ timeout: 30_000 });
    expect(writes).toMatchObject({ upload: 1, attach: 1, read: 0, create: 0 });
    expect(reads().length).toBe(readsBefore);
    const rows = (await events(request)).filter((e) => e.id === eventId);
    expect(rows).toHaveLength(1);
    expect(rows[0].name).toBe(name);
    expect(rows[0].poster_media_id).not.toBeNull();
    await expectStored(request, rows[0].poster_media_id as string, img);
    await request.delete(`${base}/events/${eventId}`, { headers: auth("org_admin") });
  });
});

test.describe("languages and the phone screen", () => {
  for (const [locale, title, hint, skip, reminder] of [
    ["ru-RU", "Афиша", "Загрузите афишу — мы предложим заполнить данные мероприятия", "Продолжить без афиши", "Афиша не добавлена. Её можно загрузить позже в карточке мероприятия."],
    ["es-ES", "Cartel", "Sube un cartel: te propondremos rellenar los datos del evento.", "Continuar sin cartel", "No se ha añadido cartel. Puedes subirlo más tarde en la ficha del evento."],
  ] as const) {
    test.describe(locale, () => {
      test.use({ locale });
      test(`the poster step reads in ${locale} and fits 375px`, async ({ page }) => {
        // The login form's labels are English-only in the helper: sign in in English, then switch the language.
        await page.addInitScript(() => {
          if (localStorage.getItem("arena.events.locale") === null) localStorage.setItem("arena.events.locale", "en");
        });
        await signInAs(page, person(env, "org_admin"));
        await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`), { timeout: 20_000 });
        await page.evaluate((l) => localStorage.setItem("arena.events.locale", l), locale.slice(0, 2));
        await page.goto(`/o/${env.org_id}/events/new`);
        const poster = page.getByTestId("wizard-poster");
        await expect(poster.locator("legend")).toHaveText(title);
        await expect(poster).toContainText(hint);
        await expect(poster).not.toContainText("необязательно");
        await expect(poster).not.toContainText("opcional");
        await expect(page.getByTestId("wizard-poster-skip")).toHaveText(skip);
        await page.getByTestId("wizard-poster-skip").click();
        await expect(page.getByTestId("wizard-poster-skipped")).toHaveText(reminder);
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        expect(overflow).toBeLessThanOrEqual(0);
        await noA11yViolations(page);
        await page.getByTestId("wizard-clear").click();
      });
    });
  }
});
