/**
 * Twin test "event posters" (task 7-1), against a REAL arena-api with a LOCAL
 * media storage on a migrated scratch database. No mocked API: the only
 * interception is where a test must SIMULATE a failure (a refused upload, a lost
 * attach answer) and every other request still reaches the real server.
 *
 * Covered: choose + preview + create a GA draft + poster bytes persisted +
 * reload; bad files and the size limit; replacing the poster of an existing
 * event (the old media is kept, a session override is kept, nothing about sales
 * changes); a failed replacement keeps the old poster; the three roles; a media
 * object of another organization; an attach failure AFTER a real creation and
 * its safe retry (no second event, no second upload), also after a reload; a
 * lost attach answer.
 *
 * Serial: scenarios share one organization. Every event has a unique name.
 */
import { createHash } from "node:crypto";
import { expect, test, type APIRequestContext, type Page, type Route } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";
import { fillEventAndVenue, fillFirstDate } from "./wizardHelpers";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const nonce = Date.now().toString(36);
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const sha256 = (b: Buffer) => createHash("sha256").update(b).digest("hex");

interface EventRow {
  id: string;
  name: string;
  status: string;
  poster_media_id: string | null;
}

interface Img {
  buffer: Buffer;
  width: number;
  height: number;
  mime: "image/jpeg" | "image/png";
}

/** A REAL picture, encoded by the browser itself (so the browser can also decode it). */
async function makeImage(page: Page, mime: Img["mime"], width: number, height: number, hue: number): Promise<Img> {
  const b64 = await page.evaluate(
    async ({ mime, width, height, hue }) => {
      const canvas = document.createElement("canvas");
      canvas.width = width;
      canvas.height = height;
      const ctx = canvas.getContext("2d") as CanvasRenderingContext2D;
      const g = ctx.createLinearGradient(0, 0, width, height);
      g.addColorStop(0, `hsl(${hue} 80% 50%)`);
      g.addColorStop(1, `hsl(${(hue + 120) % 360} 80% 30%)`);
      ctx.fillStyle = g;
      ctx.fillRect(0, 0, width, height);
      ctx.fillStyle = "#fff";
      ctx.font = `${Math.round(height / 8)}px sans-serif`;
      ctx.fillText(`poster ${hue}`, width / 10, height / 2);
      const blob = await new Promise<Blob | null>((res) => canvas.toBlob(res, mime, 0.92));
      if (blob === null) throw new Error("the browser could not encode the picture");
      const bytes = new Uint8Array(await blob.arrayBuffer());
      let s = "";
      for (const b of bytes) s += String.fromCharCode(b);
      return btoa(s);
    },
    { mime, width, height, hue },
  );
  return { buffer: Buffer.from(b64, "base64"), width, height, mime };
}

const file = (img: Img, name?: string) => ({ name: name ?? (img.mime === "image/png" ? "poster.png" : "poster.jpg"), mimeType: img.mime, buffer: img.buffer });

async function events(request: APIRequestContext): Promise<EventRow[]> {
  const res = await request.get(`${base}/events`, { headers: auth("org_admin") });
  expect(res.status()).toBe(200);
  return ((await res.json()) as { events: EventRow[] }).events;
}
const named = async (request: APIRequestContext, name: string) => (await events(request)).filter((e) => e.name === name);

async function uploadViaApi(request: APIRequestContext, role: Role, img: Img) {
  return request.post(`${env.api_base_url}/v1/media`, {
    headers: auth(role),
    multipart: { owner_type: "event_poster", org_id: env.org_id, content_type: img.mime, file: file(img) },
  });
}

async function mediaOf(request: APIRequestContext, id: string) {
  const res = await request.get(`${env.api_base_url}/v1/media/${id}`, { headers: auth("org_admin") });
  expect(res.status()).toBe(200);
  return ((await res.json()) as { media_object: { content_type: string; byte_size: number; checksum_sha256: string; signed_url: string } }).media_object;
}

/** The stored bytes, through the signed address, equal what was uploaded. */
async function expectStored(request: APIRequestContext, mediaId: string, img: Img) {
  const m = await mediaOf(request, mediaId);
  expect(m.content_type).toBe(img.mime);
  expect(m.byte_size).toBe(img.buffer.length);
  expect(m.checksum_sha256).toBe(sha256(img.buffer));
  const bytes = await request.get(m.signed_url);
  expect(bytes.status()).toBe(200);
  expect(sha256(Buffer.from(await bytes.body()))).toBe(sha256(img.buffer));
}

async function venueId(request: APIRequestContext): Promise<string> {
  const res = await request.get(`${base}/venues`, { headers: auth("org_admin") });
  const v = ((await res.json()) as { venues: { id: string; name: string }[] }).venues.find((x) => x.name === "E2E Hall");
  if (v === undefined) throw new Error("the fixture venue E2E Hall is missing");
  return v.id;
}

/** An event made through the same API the wizard uses: a draft, no poster. */
async function createEventViaApi(request: APIRequestContext, name: string): Promise<{ id: string; sessionId: string }> {
  const res = await request.post(`${base}/imports/event-bundle`, {
    headers: auth("org_admin"),
    data: {
      source: "arena",
      externalRef: `web:${env.org_id}:poster-${name}:1`,
      action: { actionName: name, description: "", age: "0+", promoterId: "" },
      actionEvent: { day: "05.12.2030", time: "19:00", currency: "EUR" },
      venue: { arenaVenueId: await venueId(request) },
      categoryList: [{ categoryPriceName: "Admission", price: 10, availability: 50, nextCategoryIndex: -1, priceSchedule: [] }],
      publish: false,
    },
  });
  expect(res.status(), await res.text()).toBeLessThan(300);
  const body = (await res.json()) as { event_id: string; session_id: string };
  return { id: body.event_id, sessionId: body.session_id };
}

async function sessionRow(request: APIRequestContext, eventId: string, sessionId: string) {
  const res = await request.get(`${base}/events/${eventId}/sessions`, { headers: auth("org_admin") });
  const rows = ((await res.json()) as { sessions: { id: string; status: string; poster_media_id: string | null; sales_end_at?: string | null }[] }).sessions;
  const row = rows.find((s) => s.id === sessionId);
  if (row === undefined) throw new Error("session missing");
  return row;
}

async function tiersOf(request: APIRequestContext, eventId: string, sessionId: string) {
  const res = await request.get(`${base}/events/${eventId}/sessions/${sessionId}/tiers`, { headers: auth("org_admin") });
  return ((await res.json()) as { tiers: { name: string; price_amount: number; is_open: boolean; capacity: number | null }[] }).tiers.map((t) => ({
    name: t.name,
    price: t.price_amount,
    open: t.is_open,
    capacity: t.capacity,
  }));
}

function trackWrites(page: Page) {
  const seen = { upload: 0, attach: 0, create: 0 };
  page.on("request", (r) => {
    const url = r.url();
    if (r.method() === "POST" && /\/v1\/media$/.test(url)) seen.upload += 1;
    if (r.method() === "PATCH" && /\/events\/[0-9a-f-]{36}$/.test(url)) seen.attach += 1;
    if (r.method() === "POST" && url.includes("/imports/event-bundle") && !url.includes("dry_run=true")) seen.create += 1;
  });
  return seen;
}

/** Signs in through the form and waits until the organization list is open (a goto before that would cancel the sign-in). */
async function signIn(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`), { timeout: 20_000 });
}

async function openWizardAs(page: Page, role: Role = "org_admin") {
  await signIn(page, role);
  await page.getByTestId("events-create").click();
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/new`));
}

async function fillWizard(page: Page, name: string) {
  await fillEventAndVenue(page, name);
  await fillFirstDate(page);
  await expect(page.getByTestId("wizard-status")).toContainText("Everything you filled in is correct", { timeout: 25_000 });
}

async function expectPosterShown(page: Page, mediaId: string, img?: Img) {
  const shown = page.getByTestId("poster-image");
  await expect(shown).toBeVisible({ timeout: 20_000 });
  await expect(shown).toHaveAttribute("data-media-id", mediaId);
  await expect.poll(() => shown.evaluate((i) => (i as HTMLImageElement).complete && (i as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
  if (img !== undefined) {
    expect(await shown.evaluate((i) => (i as HTMLImageElement).naturalWidth)).toBe(img.width);
    const box = await shown.boundingBox();
    expect(box).not.toBeNull();
    // Never stretched: the rendered proportions are the picture own.
    expect(Math.abs((box as { width: number }).width / (box as { height: number }).height - img.width / img.height)).toBeLessThan(0.02);
    expect((box as { width: number }).width).toBeLessThanOrEqual(375);
  }
}

test.afterAll(async ({ request }) => {
  for (const e of (await events(request)).filter((x) => x.name.startsWith("Poster ") && x.name.endsWith(nonce) && x.status === "draft")) {
    const del = await request.delete(`${base}/events/${e.id}`, { headers: auth("org_admin") });
    expect(del.status(), `cleanup of ${e.name}`).toBeLessThan(300);
  }
});

test.describe("create a draft GA event with a poster", () => {
  test("choose, preview, create: the poster bytes are stored and shown again after a reload", async ({ page, request }) => {
    const name = `Poster Create ${nonce}`;
    const writes = trackWrites(page);
    await openWizardAs(page);
    const img = await makeImage(page, "image/jpeg", 600, 900, 10);
    await fillWizard(page, name);

    await page.getByTestId("wizard-poster-picker-input").setInputFiles(file(img, "my private holiday name.jpg"));
    const preview = page.getByTestId("wizard-poster-picker-preview");
    await expect(preview).toBeVisible();
    expect(await preview.evaluate((i) => (i as HTMLImageElement).naturalWidth)).toBe(600);
    const box = (await preview.boundingBox()) as { width: number; height: number };
    expect(Math.abs(box.width / box.height - 600 / 900)).toBeLessThan(0.02);
    expect(box.width).toBeLessThanOrEqual(375);
    await noA11yViolations(page);
    // Choosing sends NOTHING: the picture travels when the event is created.
    expect(writes.upload).toBe(0);
    // Nothing of the file is stored in the browser.
    const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }));
    for (const bad of ["blob:", "data:image", "my private holiday", "Bearer "]) expect(stored).not.toContain(bad);

    // The controls work from the keyboard.
    await page.getByTestId("wizard-poster-picker-choose").focus();
    await expect(page.getByTestId("wizard-poster-picker-choose")).toBeFocused();

    await page.getByTestId("wizard-create").dblclick();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/[0-9a-f-]{36}$`), { timeout: 30_000 });
    expect(writes).toEqual({ upload: 1, attach: 1, create: 1 });

    const rows = await named(request, name);
    expect(rows).toHaveLength(1);
    expect(rows[0].status).toBe("draft");
    const mediaId = rows[0].poster_media_id as string;
    expect(mediaId).toMatch(/^[0-9a-f-]{36}$/);
    await expectStored(request, mediaId, img);
    const m = await mediaOf(request, mediaId);
    expect(m.content_type).toBe("image/jpeg");
    await expectPosterShown(page, mediaId, img);

    // After a reload the poster is read from the server again.
    await page.reload();
    await expectPosterShown(page, mediaId, img);

    // Creation stayed a draft, off sale.
    const sessions = await request.get(`${base}/events/${rows[0].id}/sessions`, { headers: auth("org_admin") });
    const list = ((await sessions.json()) as { sessions: { status: string }[] }).sessions;
    expect(list.map((s) => s.status)).toEqual(["draft"]);
  });

  test("a PNG of another proportion is shown undistorted too", async ({ page, request }) => {
    const name = `Poster Wide ${nonce}`;
    await openWizardAs(page);
    const img = await makeImage(page, "image/png", 900, 500, 200);
    await fillWizard(page, name);
    await page.getByTestId("wizard-poster-picker-input").setInputFiles(file(img));
    await expect(page.getByTestId("wizard-poster-picker-preview")).toBeVisible();
    // Replace the choice, then remove it: the create button is not blocked by a removed choice.
    const other = await makeImage(page, "image/png", 300, 300, 90);
    await page.getByTestId("wizard-poster-picker-input").setInputFiles(file(other));
    await expect.poll(() => page.getByTestId("wizard-poster-picker-preview").evaluate((i) => (i as HTMLImageElement).naturalWidth)).toBe(300);
    await page.getByTestId("wizard-poster-picker-remove").click();
    await expect(page.getByTestId("wizard-poster-picker-preview")).toHaveCount(0);
    await page.getByTestId("wizard-poster-picker-input").setInputFiles(file(img));
    await expect.poll(() => page.getByTestId("wizard-poster-picker-preview").evaluate((i) => (i as HTMLImageElement).naturalWidth)).toBe(900);
    await page.getByTestId("wizard-create").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/[0-9a-f-]{36}$`), { timeout: 30_000 });
    const rows = await named(request, name);
    expect(rows).toHaveLength(1);
    await expectStored(request, rows[0].poster_media_id as string, img);
    await expectPosterShown(page, rows[0].poster_media_id as string, img);
  });

  test("bad files and the size limit are refused before anything is sent", async ({ page }) => {
    const writes = trackWrites(page);
    await openWizardAs(page);
    const good = await makeImage(page, "image/png", 120, 80, 40);
    await fillWizard(page, `Poster Bad ${nonce}`);
    const input = page.getByTestId("wizard-poster-picker-input");
    const rejected = page.getByTestId("wizard-poster-picker-rejected");

    const svg = Buffer.from('<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><script>alert(1)</script></svg>');
    const pdf = Buffer.from("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n");
    // A real PNG with one damaged data byte: complete structure, the browser cannot decode it.
    const damaged = Buffer.from(good.buffer);
    damaged[Math.floor(damaged.length / 2)] ^= 0xff;
    const cases: Array<{ label: string; f: { name: string; mimeType: string; buffer: Buffer }; reason: string }> = [
      { label: "SVG", f: { name: "poster.svg", mimeType: "image/svg+xml", buffer: svg }, reason: "notImage" },
      { label: "SVG named png", f: { name: "poster.png", mimeType: "image/png", buffer: svg }, reason: "notImage" },
      { label: "PDF", f: { name: "poster.pdf", mimeType: "application/pdf", buffer: pdf }, reason: "notImage" },
      { label: "text named jpg", f: { name: "poster.jpg", mimeType: "image/jpeg", buffer: Buffer.from("not a picture at all") }, reason: "notImage" },
      { label: "empty", f: { name: "poster.png", mimeType: "image/png", buffer: Buffer.alloc(0) }, reason: "empty" },
      { label: "truncated PNG", f: { name: "poster.png", mimeType: "image/png", buffer: good.buffer.subarray(0, good.buffer.length - 30) }, reason: "corrupt" },
      { label: "damaged PNG", f: { name: "poster.png", mimeType: "image/png", buffer: damaged }, reason: "corrupt" },
      { label: "oversize", f: { name: "poster.jpg", mimeType: "image/jpeg", buffer: Buffer.alloc(5 * 1024 * 1024 + 1, 0xff) }, reason: "tooLarge" },
    ];
    for (const c of cases) {
      await input.setInputFiles(c.f);
      await expect(rejected, c.label).toBeVisible();
      await expect(rejected, c.label).toHaveAttribute("data-reason", c.reason);
      await expect(page.getByTestId("wizard-poster-picker-preview"), c.label).toHaveCount(0);
      // A refused file blocks Create: it would be silently left out otherwise.
      await expect(page.getByTestId("wizard-create"), c.label).toBeDisabled();
    }
    await noA11yViolations(page);
    // A good file after a refusal works again, and the refusal is gone.
    await input.setInputFiles(file(good));
    await expect(page.getByTestId("wizard-poster-picker-preview")).toBeVisible();
    await expect(rejected).toHaveCount(0);
    await expect(page.getByTestId("wizard-create")).toBeEnabled();
    expect(writes).toEqual({ upload: 0, attach: 0, create: 0 });
  });

  test("the server itself takes the media of this organization only: foreign media is refused as a poster", async ({ request }) => {
    const name = `Poster Foreign ${nonce}`;
    const ev = await createEventViaApi(request, name);
    const patch = await request.patch(`${base}/events/${ev.id}`, { headers: auth("org_admin"), data: { poster_media_id: env.foreign_media_id } });
    expect(patch.status()).toBe(422);
    expect(((await patch.json()) as { error: { code: string } }).error.code).toBe("event.invalid_poster_media_id");
    const read = await request.get(`${env.api_base_url}/v1/media/${env.foreign_media_id}`, { headers: auth("org_admin") });
    expect([403, 404]).toContain(read.status());
    expect((await named(request, name))[0].poster_media_id).toBeNull();
  });
});

test.describe("the poster of an existing event", () => {
  test("upload, replace, and what stays: the old media, the session override, the sale settings", async ({ page, request }) => {
    const name = `Poster Card ${nonce}`;
    const ev = await createEventViaApi(request, name);
    const writes = trackWrites(page);
    await signIn(page, "org_admin");
    await page.goto(`/o/${env.org_id}/events/${ev.id}`);
    await expect(page.getByTestId("poster-none")).toBeVisible();
    await expect(page.getByTestId("poster-picker-choose")).toHaveText("Upload a poster");

    // A session of its own poster: it must survive the event poster change.
    const override = await makeImage(page, "image/png", 200, 200, 300);
    const up = await uploadViaApi(request, "org_admin", override);
    expect(up.status()).toBe(201);
    const overrideId = ((await up.json()) as { media_object: { id: string } }).media_object.id;
    const patchSession = await request.patch(`${base}/events/${ev.id}/sessions/${ev.sessionId}`, { headers: auth("org_admin"), data: { poster_media_id: overrideId } });
    expect(patchSession.status(), await patchSession.text()).toBeLessThan(300);
    const salesBefore = { event: (await named(request, name))[0].status, session: await sessionRow(request, ev.id, ev.sessionId), tiers: await tiersOf(request, ev.id, ev.sessionId) };
    expect(salesBefore.session.poster_media_id).toBe(overrideId);

    // First upload.
    const first = await makeImage(page, "image/jpeg", 500, 700, 20);
    await page.getByTestId("poster-picker-input").setInputFiles(file(first));
    await expect(page.getByTestId("poster-picker-preview")).toBeVisible();
    await noA11yViolations(page);
    await page.getByTestId("poster-save").dblclick();
    await expect(page.getByTestId("poster-saved")).toBeVisible({ timeout: 30_000 });
    expect(writes.upload).toBe(1);
    expect(writes.attach).toBe(1);
    const afterFirst = (await named(request, name))[0].poster_media_id as string;
    await expectStored(request, afterFirst, first);
    await expectPosterShown(page, afterFirst, first);
    await expect(page.getByTestId("poster-picker-choose")).toHaveText("Replace the poster");

    // Replacement.
    const second = await makeImage(page, "image/png", 700, 400, 160);
    await page.getByTestId("poster-picker-input").setInputFiles(file(second));
    await expect(page.getByTestId("poster-picker-preview")).toBeVisible();
    // The current poster is still the OLD one until the new one is saved.
    await expect(page.getByTestId("poster-image")).toHaveAttribute("data-media-id", afterFirst);
    await page.getByTestId("poster-save").click();
    await expect(page.getByTestId("poster-saved")).toBeVisible({ timeout: 30_000 });
    const afterSecond = (await named(request, name))[0].poster_media_id as string;
    expect(afterSecond).not.toBe(afterFirst);
    await expectStored(request, afterSecond, second);
    // The replaced media is NOT deleted (it may be shared).
    await expectStored(request, afterFirst, first);
    await page.reload();
    await expectPosterShown(page, afterSecond, second);

    // Nothing else moved: the session keeps its own poster, status, categories and prices.
    const salesAfter = { event: (await named(request, name))[0].status, session: await sessionRow(request, ev.id, ev.sessionId), tiers: await tiersOf(request, ev.id, ev.sessionId) };
    expect(salesAfter.session.poster_media_id).toBe(overrideId);
    expect(salesAfter).toEqual(salesBefore);
  });

  test("a failed upload keeps the old poster, and a retry then succeeds", async ({ page, request }) => {
    const name = `Poster Keep ${nonce}`;
    const ev = await createEventViaApi(request, name);
    const old = await makeImage(page, "image/jpeg", 400, 400, 60);
    const up = await uploadViaApi(request, "org_admin", old);
    const oldId = ((await up.json()) as { media_object: { id: string } }).media_object.id;
    expect((await request.patch(`${base}/events/${ev.id}`, { headers: auth("org_admin"), data: { poster_media_id: oldId } })).status()).toBe(200);

    let refuse = true;
    await page.route(/\/v1\/media$/, async (route: Route) => {
      if (refuse && route.request().method() === "POST") await route.abort("failed");
      else await route.continue();
    });
    const writes = trackWrites(page);
    await signIn(page, "org_admin");
    await page.goto(`/o/${env.org_id}/events/${ev.id}`);
    await expectPosterShown(page, oldId, old);
    const next = await makeImage(page, "image/png", 400, 400, 250);
    await page.getByTestId("poster-picker-input").setInputFiles(file(next));
    await page.getByTestId("poster-save").click();
    await expect(page.getByTestId("poster-upload-failed")).toBeVisible();
    await expect(page.getByTestId("poster-saved")).toHaveCount(0);
    // The old poster is still the poster, on screen and on the server.
    await expect(page.getByTestId("poster-image")).toHaveAttribute("data-media-id", oldId);
    expect((await named(request, name))[0].poster_media_id).toBe(oldId);
    expect(writes.attach).toBe(0);

    refuse = false;
    await page.getByTestId("poster-save").click();
    await expect(page.getByTestId("poster-saved")).toBeVisible({ timeout: 30_000 });
    const now = (await named(request, name))[0].poster_media_id as string;
    expect(now).not.toBe(oldId);
    await expectStored(request, now, next);
  });

  test("a refused attachment (foreign media) keeps the old poster and offers no retry of the same picture", async ({ page, request }) => {
    const name = `Poster Refused ${nonce}`;
    const ev = await createEventViaApi(request, name);
    const old = await makeImage(page, "image/jpeg", 300, 450, 75);
    const oldId = ((await (await uploadViaApi(request, "org_admin", old)).json()) as { media_object: { id: string } }).media_object.id;
    await request.patch(`${base}/events/${ev.id}`, { headers: auth("org_admin"), data: { poster_media_id: oldId } });

    // The browser really sends the PATCH; only the media id in it is swapped for a media of ANOTHER organization.
    await page.route(new RegExp(`/events/${ev.id}$`), async (route) => {
      if (route.request().method() === "PATCH") await route.continue({ postData: JSON.stringify({ poster_media_id: env.foreign_media_id }) });
      else await route.continue();
    });
    await signIn(page, "org_admin");
    await page.goto(`/o/${env.org_id}/events/${ev.id}`);
    await expectPosterShown(page, oldId);
    await page.getByTestId("poster-picker-input").setInputFiles(file(await makeImage(page, "image/png", 300, 300, 5)));
    await page.getByTestId("poster-save").click();
    const failed = page.getByTestId("poster-attach-failed");
    await expect(failed).toBeVisible({ timeout: 30_000 });
    await expect(failed).toHaveAttribute("data-kind", "rejected");
    await expect(page.getByTestId("poster-retry-attach")).toHaveCount(0);
    await expect(page.getByTestId("poster-saved")).toHaveCount(0);
    await expect(page.getByTestId("poster-image")).toHaveAttribute("data-media-id", oldId);
    expect((await named(request, name))[0].poster_media_id).toBe(oldId);
  });
});

test.describe("who may change a poster (three roles)", () => {
  for (const role of ["org_admin", "organizer", "agent"] as const) {
    test(`${role}: the screen shows what the server allows`, async ({ page, request }) => {
      const me = (await (await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/me`, { headers: auth(role) })).json()) as { permissions: string[] };
      const canWrite = me.permissions.includes("media.write") && me.permissions.includes("event.update");
      const canRead = me.permissions.includes("media.read");
      const name = `Poster Role ${role} ${nonce}`;
      const ev = await createEventViaApi(request, name);
      const img = await makeImage(page, "image/jpeg", 300, 400, 100);
      const mediaId = ((await (await uploadViaApi(request, "org_admin", img)).json()) as { media_object: { id: string } }).media_object.id;
      await request.patch(`${base}/events/${ev.id}`, { headers: auth("org_admin"), data: { poster_media_id: mediaId } });

      const writes = trackWrites(page);
      await signIn(page, role);
      await page.goto(`/o/${env.org_id}/events/${ev.id}`);
      await expect(page.getByTestId("event-poster")).toBeVisible();
      if (canRead) await expectPosterShown(page, mediaId, img);
      else await expect(page.getByTestId("poster-no-view")).toBeVisible();
      if (canWrite) {
        await expect(page.getByTestId("poster-picker-choose")).toBeVisible();
        await expect(page.getByTestId("poster-read-only")).toHaveCount(0);
      } else {
        await expect(page.getByTestId("poster-picker-choose")).toHaveCount(0);
        await expect(page.getByTestId("poster-picker-input")).toHaveCount(0);
        await expect(page.getByTestId("poster-read-only")).toBeVisible();
      }
      await noA11yViolations(page);
      expect(writes).toEqual({ upload: 0, attach: 0, create: 0 });

      // The API refuses the same writes for a person without the permission.
      if (!canWrite) {
        const denied = await uploadViaApi(request, role, img);
        expect(denied.status()).toBe(403);
        const patch = await request.patch(`${base}/events/${ev.id}`, { headers: auth(role), data: { poster_media_id: mediaId } });
        expect(patch.status()).toBe(403);
      }
    });
  }

  test("the agent cannot create an event with a poster: the wizard is closed to them", async ({ page }) => {
    await signIn(page, "agent");
    await page.goto(`/o/${env.org_id}/events/new`);
    await expect(page.getByTestId("wizard-no-access")).toBeVisible();
    await expect(page.getByTestId("wizard-poster")).toHaveCount(0);
  });
});

test.describe("an attachment that fails after a REAL creation", () => {
  const attachUrl = /\/v1\/organizations\/[0-9a-f-]{36}\/events\/[0-9a-f-]{36}$/;

  test("the event exists, nothing claims the poster, and the retry attaches only (also after a reload)", async ({ page, request }) => {
    const name = `Poster Partial ${nonce}`;
    const writes = trackWrites(page);
    let failures = 1;
    await page.route(attachUrl, async (route) => {
      if (route.request().method() === "PATCH" && failures > 0) {
        failures -= 1;
        await route.abort("failed"); // never reaches the server
      } else await route.continue();
    });
    await openWizardAs(page);
    const img = await makeImage(page, "image/jpeg", 450, 600, 330);
    await fillWizard(page, name);
    await page.getByTestId("wizard-poster-picker-input").setInputFiles(file(img));
    await expect(page.getByTestId("wizard-poster-picker-preview")).toBeVisible();
    await page.getByTestId("wizard-create").click();

    const panel = page.getByTestId("wizard-attach-failed");
    await expect(panel).toBeVisible({ timeout: 30_000 });
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/new`));
    await noA11yViolations(page);
    // The truth on the server: ONE event, a draft, the media uploaded, NO poster attached.
    let rows = await named(request, name);
    expect(rows).toHaveLength(1);
    expect(rows[0].status).toBe("draft");
    expect(rows[0].poster_media_id).toBeNull();
    expect(writes).toEqual({ upload: 1, attach: 1, create: 1 });
    // The form is closed for a second creation.
    await expect(page.getByTestId("wizard-create")).toBeDisabled();

    // A reload keeps the explanation (the ids are in the draft, the file is not).
    await page.reload();
    await expect(page.getByTestId("wizard-attach-failed")).toBeVisible({ timeout: 20_000 });
    const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }));
    for (const bad of ["blob:", "data:image", "Bearer ", "sig="]) expect(stored).not.toContain(bad);

    await page.getByTestId("wizard-attach-retry").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/${rows[0].id}$`), { timeout: 30_000 });
    rows = await named(request, name);
    expect(rows).toHaveLength(1);
    const mediaId = rows[0].poster_media_id as string;
    expect(mediaId).toMatch(/^[0-9a-f-]{36}$/);
    await expectStored(request, mediaId, img);
    await expectPosterShown(page, mediaId, img);
    // No second creation, no second upload: the retry was one more PATCH only (the reload reset this page counter).
    expect(writes.create).toBe(1);
    expect(writes.upload).toBe(1);
    expect(writes.attach).toBeGreaterThanOrEqual(2);
    // The saved draft is gone from the browser.
    expect(await page.evaluate(() => Object.keys(localStorage).filter((k) => k.includes("wizard")))).toEqual([]);
  });

  test("open the card instead of retrying: the event is the same one, the poster can be added there", async ({ page, request }) => {
    const name = `Poster Open ${nonce}`;
    await page.route(attachUrl, async (route) => {
      if (route.request().method() === "PATCH") await route.abort("failed");
      else await route.continue();
    });
    await openWizardAs(page);
    await fillWizard(page, name);
    await page.getByTestId("wizard-poster-picker-input").setInputFiles(file(await makeImage(page, "image/png", 300, 200, 15)));
    await page.getByTestId("wizard-create").click();
    await expect(page.getByTestId("wizard-attach-failed")).toBeVisible({ timeout: 30_000 });
    await page.getByTestId("wizard-attach-open").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/[0-9a-f-]{36}$`));
    await expect(page.getByRole("heading", { level: 1, name })).toBeVisible();
    await expect(page.getByTestId("poster-none")).toBeVisible();
    expect(await named(request, name)).toHaveLength(1);
    await page.unroute(attachUrl);
    const img = await makeImage(page, "image/png", 300, 200, 15);
    await page.getByTestId("poster-picker-input").setInputFiles(file(img));
    await page.getByTestId("poster-save").click();
    await expect(page.getByTestId("poster-saved")).toBeVisible({ timeout: 30_000 });
    expect(await named(request, name)).toHaveLength(1);
    expect((await named(request, name))[0].poster_media_id).not.toBeNull();
  });

  test("a LOST answer after a real attach is verified by reading the event: no false failure", async ({ page, request }) => {
    const name = `Poster Lost ${nonce}`;
    await page.route(attachUrl, async (route) => {
      if (route.request().method() === "PATCH") {
        await route.fetch(); // the server really applies it...
        await route.abort("failed"); // ...but the answer never arrives
      } else await route.continue();
    });
    await openWizardAs(page);
    const img = await makeImage(page, "image/jpeg", 360, 480, 270);
    await fillWizard(page, name);
    await page.getByTestId("wizard-poster-picker-input").setInputFiles(file(img));
    await page.getByTestId("wizard-create").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/[0-9a-f-]{36}$`), { timeout: 30_000 });
    const rows = await named(request, name);
    expect(rows).toHaveLength(1);
    await expectStored(request, rows[0].poster_media_id as string, img);
    await page.unroute(attachUrl);
    await expectPosterShown(page, rows[0].poster_media_id as string, img);
  });
});
