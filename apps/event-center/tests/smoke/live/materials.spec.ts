/**
 * Twin test "sales page materials", against a REAL arena-api on a migrated
 * scratch database with LOCAL media storage. No mocked API: the screen saves,
 * and the PUBLIC answers (the widget feed and the hosted page) are read back
 * from the same server, so "editor to API to public page" is proven end to end.
 *
 * Covered: every editor saves and survives a reload, the public answers carry
 * the same data (the venue address from the venue, the point 0,0, an XSS text
 * kept as text), edit / reorder / delete show up after a reload, the 6 / 4 / 3
 * limits (screen and server), invalid links and coordinates, a media object of
 * another organization, another organization's event, and the three roles by
 * what the server says they may do.
 *
 * Serial: the scenarios share one fixture event. Constants of the public side
 * mirror apps/backend/cmd/arena-e2e-seed/materials_fixture.go.
 */
import { randomUUID } from "node:crypto";
import { mkdirSync } from "node:fs";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const orgBase = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const ev = `${orgBase}/events/${env.event_id}`;
const FEED_TOKEN = "e2e-materials-feed-token";
const ORG_SLUG = "e2e-event-center-org";
const EVENT_SLUG = "e2e-materials-event";
const SHOTS = process.env.MATERIALS_SHOTS_DIR;

interface Img {
  buffer: Buffer;
  mime: "image/png";
}

/** A REAL png encoded by the browser itself. */
async function makeImage(page: Page, hue: number): Promise<Img> {
  const b64 = await page.evaluate(async (h) => {
    const canvas = document.createElement("canvas");
    canvas.width = 640;
    canvas.height = 400;
    const ctx = canvas.getContext("2d") as CanvasRenderingContext2D;
    ctx.fillStyle = `hsl(${h} 70% 45%)`;
    ctx.fillRect(0, 0, 640, 400);
    ctx.fillStyle = "#fff";
    ctx.font = "48px sans-serif";
    ctx.fillText(`photo ${h}`, 40, 210);
    const blob = await new Promise<Blob | null>((res) => canvas.toBlob(res, "image/png"));
    if (blob === null) throw new Error("the browser could not encode the picture");
    const bytes = new Uint8Array(await blob.arrayBuffer());
    let s = "";
    for (const b of bytes) s += String.fromCharCode(b);
    return btoa(s);
  }, hue);
  return { buffer: Buffer.from(b64, "base64"), mime: "image/png" };
}

const file = (img: Img) => ({ name: "photo.png", mimeType: img.mime, buffer: img.buffer });

/** A repeated run starts clean: every material of the fixture event is removed through the API. */
async function resetMaterials(request: APIRequestContext) {
  const h = auth("org_admin");
  for (const [list, key] of [
    ["media-items", "id"],
    ["videos", "id"],
    ["partner-links", "id"],
    ["photo-report", "id"],
  ] as const) {
    const res = await request.get(`${ev}/${list}`, { headers: h });
    expect(res.status()).toBe(200);
    for (const item of ((await res.json()) as { items: Record<string, string>[] }).items) {
      expect((await request.delete(`${ev}/${list}/${item[key]}`, { headers: h })).status()).toBeLessThan(300);
    }
  }
  await request.delete(`${ev}/map-point`, { headers: h });
}

test.beforeAll(async ({ request }) => {
  await resetMaterials(request);
});

async function openCard(page: Page, role: Role = "org_admin") {
  await signInAs(page, person(env, role));
  await page.waitForURL((u) => !u.pathname.startsWith("/login"));
  await page.goto(`/o/${env.org_id}/events/${env.event_id}`);
  await expect(page.getByTestId("materials-section")).toBeVisible();
}

async function publicEvent(request: APIRequestContext) {
  const res = await request.get(`${env.api_base_url}/v1/public/feeds/${FEED_TOKEN}/events/${env.event_id}`);
  expect(res.status(), await res.text()).toBe(200);
  return ((await res.json()) as { event: PublicEvent }).event;
}

interface PublicEvent {
  materials: {
    gallery: { caption: string | null; url: string }[];
    videos: { provider: string; embed_url: string | null; url: string }[];
    partner_links: { seller_name: string; url: string }[];
    map_point: { latitude: number; longitude: number; label: string | null } | null;
    report_photos: unknown[];
  };
  venues: { name: string; address: string | null; postal_code: string | null; country: string | null; session_starts: string[] }[];
}

async function venueId(request: APIRequestContext): Promise<string> {
  const res = await request.get(`${orgBase}/venues`, { headers: auth("org_admin") });
  const v = ((await res.json()) as { venues: { id: string; name: string }[] }).venues.find((x) => x.name === "E2E Hall");
  if (v === undefined) throw new Error("the fixture venue E2E Hall is missing");
  return v.id;
}

async function shot(page: Page, name: string) {
  if (SHOTS === undefined) return;
  mkdirSync(SHOTS, { recursive: true });
  await page.getByTestId("materials-section").screenshot({ path: `${SHOTS}/${name}.png` });
}

test("the owner saves every material through the screen and the public answers show it", async ({ page, request }) => {
  await openCard(page);
  const img = await makeImage(page, 200);
  const section = page.getByTestId("materials-section");

  // Gallery: upload, caption.
  await section.getByTestId("mat-gallery-file").setInputFiles(file(img));
  await expect(page.getByTestId("mat-gallery-item")).toHaveCount(1);
  await page.getByTestId("mat-gallery-0-caption").fill("Main stage <script>alert(1)</script>");
  await page.getByTestId("mat-gallery-0-caption-save").click();
  await expect(page.getByTestId("mat-gallery-ok")).toBeVisible();

  // Video.
  await page.getByTestId("mat-video-url").fill("https://www.youtube.com/watch?v=dQw4w9WgXcQ");
  await page.getByTestId("mat-video-add").click();
  await expect(page.getByTestId("mat-video-item")).toHaveCount(1);

  // Another seller; the name is text, never markup.
  await page.getByTestId("mat-partner-new").click();
  await page.getByTestId("mat-partner-seller").fill("Other Tickets <b>Ltd</b>");
  await page.getByTestId("mat-partner-url").fill("https://tickets.example.org/e/1");
  await page.getByTestId("mat-partner-save").click();
  await expect(page.getByTestId("mat-partner-item")).toHaveCount(1);

  // The venue's own address, and the map point 0,0 (a valid point).
  const vid = await venueId(request);
  await page.getByTestId(`mat-venue-${vid}-line1`).fill("Calle Mayor 1");
  await page.getByTestId(`mat-venue-${vid}-postal`).fill("28013");
  await page.getByTestId(`mat-venue-${vid}-country`).fill("es");
  await page.getByTestId(`mat-venue-${vid}-save`).click();
  await expect(page.getByTestId(`mat-venue-${vid}-ok`)).toBeVisible();
  await page.getByTestId("mat-map-lat").fill("0");
  await page.getByTestId("mat-map-lng").fill("0");
  await page.getByTestId("mat-map-label").fill("Gate A");
  await page.getByTestId("mat-map-save").click();
  await expect(page.getByTestId("mat-map-ok")).toBeVisible();

  // Photo report: uploaded now, public only after the last date ends.
  await page.getByTestId("mat-report-file").setInputFiles(file(await makeImage(page, 30)));
  await expect(page.getByTestId("mat-report-item")).toHaveCount(1);
  await expect(page.getByTestId("mat-report-note")).toContainText("last date ends");
  await shot(page, "event-center-mobile-materials");

  // After a reload everything is still there.
  await page.reload();
  await expect(page.getByTestId("mat-gallery-0-caption")).toHaveValue("Main stage <script>alert(1)</script>");
  await expect(page.getByTestId("mat-video-item")).toContainText("youtube.com");
  await expect(page.getByTestId("mat-partner-item")).toContainText("Other Tickets <b>Ltd</b>");
  await expect(page.getByTestId(`mat-venue-${vid}-line1`)).toHaveValue("Calle Mayor 1");
  await expect(page.getByTestId("mat-map-lat")).toHaveValue("0");
  await expect(page.getByTestId("mat-map-lng")).toHaveValue("0");
  await expect(page.getByTestId("mat-report-item")).toHaveCount(1);
  await noA11yViolations(page);

  // The public widget feed carries the same data.
  const pub = await publicEvent(request);
  expect(pub.materials.gallery).toHaveLength(1);
  expect(pub.materials.gallery[0].caption).toBe("Main stage <script>alert(1)</script>");
  expect(pub.materials.videos[0].provider).toBe("youtube");
  expect(pub.materials.videos[0].embed_url).toBe("https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ");
  expect(pub.materials.partner_links).toEqual([{ seller_name: "Other Tickets <b>Ltd</b>", url: "https://tickets.example.org/e/1" }]);
  expect(pub.materials.map_point).toMatchObject({ latitude: 0, longitude: 0, label: "Gate A" });
  expect(pub.materials.report_photos, "the report stays hidden until the last date ends").toEqual([]);
  expect(pub.venues).toHaveLength(1);
  expect(pub.venues[0]).toMatchObject({ name: "E2E Hall", address: "Calle Mayor 1", postal_code: "28013", country: "ES" });
  expect(pub.venues[0].session_starts).toHaveLength(1);

  // And so does the hosted page.
  const pageRes = await request.get(`${env.api_base_url}/v1/public/pages/${ORG_SLUG}/${EVENT_SLUG}`);
  expect(pageRes.status(), await pageRes.text()).toBe(200);
  const hosted = (await pageRes.json()) as { materials: PublicEvent["materials"]; event: { venues: PublicEvent["venues"] } };
  expect(hosted.materials.gallery).toHaveLength(1);
  expect(hosted.materials.partner_links).toHaveLength(1);
  expect(hosted.event.venues[0].address).toBe("Calle Mayor 1");
});

test("edit, reorder and delete are visible after a reload", async ({ page, request }) => {
  await openCard(page);
  // A second video and a second seller.
  await page.getByTestId("mat-video-url").fill("https://vimeo.com/76979871");
  await page.getByTestId("mat-video-add").click();
  await expect(page.getByTestId("mat-video-item")).toHaveCount(2);
  await page.getByTestId("mat-partner-new").click();
  await page.getByTestId("mat-partner-seller").fill("Second Seller");
  await page.getByTestId("mat-partner-url").fill("https://second.example.org/t");
  await page.getByTestId("mat-partner-save").click();
  await expect(page.getByTestId("mat-partner-item")).toHaveCount(2);

  // Move the second video up, edit the first seller's link, change the point.
  await page.getByTestId("mat-video-1-up").click();
  await expect(page.getByTestId("mat-video-item").first()).toContainText("vimeo.com");
  await page.getByTestId("mat-partner-0-edit").click();
  await page.getByTestId("mat-partner-url").fill("https://tickets.example.org/e/2");
  await page.getByTestId("mat-partner-save").click();
  await expect(page.getByTestId("mat-partner-item").first()).toContainText("/e/2");
  await page.getByTestId("mat-map-lat").fill("40.4168");
  await page.getByTestId("mat-map-lng").fill("-3.7038");
  await page.getByTestId("mat-map-save").click();
  await expect(page.getByTestId("mat-map-ok")).toBeVisible();

  // Replace the youtube link (no PATCH exists: add, remove, restore the place).
  await page.getByTestId("mat-video-1-replace").click();
  await page.getByTestId("mat-video-replace-url").fill("https://www.youtube.com/watch?v=aqz-KE-bpKQ");
  await page.getByTestId("mat-video-replace-save").click();
  await expect(page.getByTestId("mat-video-item")).toHaveCount(2);
  await expect(page.getByTestId("mat-video-item").nth(1)).toContainText("aqz-KE-bpKQ");

  // Delete the second seller after the confirmation.
  await page.getByTestId("mat-partner-1-remove").click();
  await page.getByTestId("mat-partner-confirm").getByRole("button", { name: "Remove" }).click();
  await expect(page.getByTestId("mat-partner-item")).toHaveCount(1);

  await page.reload();
  await expect(page.getByTestId("mat-video-item").first()).toContainText("vimeo.com");
  await expect(page.getByTestId("mat-video-item").nth(1)).toContainText("aqz-KE-bpKQ");
  await expect(page.getByTestId("mat-partner-item")).toHaveCount(1);
  await expect(page.getByTestId("mat-map-lat")).toHaveValue("40.4168");

  const pub = await publicEvent(request);
  expect(pub.materials.videos.map((v) => v.provider)).toEqual(["vimeo", "youtube"]);
  expect(pub.materials.partner_links).toEqual([{ seller_name: "Other Tickets <b>Ltd</b>", url: "https://tickets.example.org/e/2" }]);
  expect(pub.materials.map_point).toMatchObject({ latitude: 40.4168, longitude: -3.7038 });

  // Removing the point removes it publicly too.
  await page.getByTestId("mat-map-remove").click();
  await page.getByTestId("mat-map-confirm").getByRole("button", { name: "Remove" }).click();
  await expect(page.getByTestId("mat-map-lat")).toHaveValue("");
  expect((await publicEvent(request)).materials.map_point).toBeNull();
});

test("clearing a legacy venue address clears it publicly and survives reload", async ({ page, request }) => {
  const id = await venueId(request);
  const h = auth("org_admin");
  expect((await request.patch(`${orgBase}/venues/${id}`, {
    headers: h, data: { address: "Old legacy street", address_line1: null, address_line2: null },
  })).ok()).toBeTruthy();
  await openCard(page);
  await expect(page.getByTestId(`mat-venue-${id}-line1`)).toHaveValue("Old legacy street");
  await page.getByTestId(`mat-venue-${id}-line1`).fill("");
  await page.getByTestId(`mat-venue-${id}-line2`).fill("");
  await page.getByTestId(`mat-venue-${id}-save`).click();
  await expect(page.getByTestId(`mat-venue-${id}-ok`)).toBeVisible();
  expect((await publicEvent(request)).venues[0]?.address).toBeNull();
  await page.reload();
  await expect(page.getByTestId(`mat-venue-${id}-line1`)).toHaveValue("");
});

test("bad links and coordinates are refused on the screen and by the server, never shown as saved", async ({ page, request }) => {
  await openCard(page);
  await page.getByTestId("mat-video-url").fill("http://insecure.example.org/v");
  await page.getByTestId("mat-video-add").click();
  await expect(page.getByTestId("mat-video-problem")).toContainText("https://");
  await page.getByTestId("mat-video-url").fill("javascript:alert(1)");
  await page.getByTestId("mat-video-add").click();
  await expect(page.getByTestId("mat-video-problem")).toBeVisible();
  await expect(page.getByTestId("mat-videos-ok")).toHaveCount(0);

  await page.getByTestId("mat-map-lat").fill("12");
  await page.getByTestId("mat-map-lng").fill("");
  await page.getByTestId("mat-map-save").click();
  await expect(page.getByTestId("mat-map-problem")).toContainText("not zero");
  await page.getByTestId("mat-map-lat").fill("91");
  await page.getByTestId("mat-map-lng").fill("10");
  await page.getByTestId("mat-map-save").click();
  await expect(page.getByTestId("mat-map-problem")).toContainText("-90 and 90");

  // The server says the same without the screen.
  const h = auth("org_admin");
  const bad = [
    request.post(`${ev}/videos`, { headers: h, data: { url: "javascript:alert(1)" } }),
    request.post(`${ev}/videos`, { headers: h, data: { url: "data:text/html,<b>x</b>" } }),
    request.post(`${ev}/partner-links`, { headers: h, data: { url: "http://plain.example.org", seller_name: "X" } }),
    request.put(`${ev}/map-point`, { headers: h, data: { latitude: 12 } }),
    request.put(`${ev}/map-point`, { headers: h, data: { latitude: 0, longitude: 181 } }),
  ];
  for (const res of await Promise.all(bad)) expect(res.status()).toBe(400);
  // A media object of another organization is not a photo of this one.
  const foreign = await request.post(`${ev}/media-items`, { headers: h, data: { media_id: env.foreign_media_id } });
  expect(foreign.status()).toBeGreaterThanOrEqual(400);
  expect(foreign.status()).toBeLessThan(500);
  // Another organization's event is neither readable nor writable.
  const other = `${env.api_base_url}/v1/organizations/${randomUUID()}/events/${env.event_id}`;
  expect((await request.get(`${other}/videos`, { headers: h })).status()).toBeGreaterThanOrEqual(403);
  expect((await request.post(`${other}/videos`, { headers: h, data: { url: "https://vimeo.com/1" } })).status()).toBeGreaterThanOrEqual(403);
});

test("the limits 4 videos, 3 sellers and 6 gallery photos hold on the screen and on the server", async ({ page, request }) => {
  await openCard(page);
  const h = auth("org_admin");
  // Fill the video list to 4 through the API, then see the screen stop offering the form.
  for (const n of [3, 4]) {
    const res = await request.post(`${ev}/videos`, { headers: h, data: { url: `https://vimeo.com/${100 + n}` } });
    if (res.status() !== 409) expect(res.status(), await res.text()).toBe(201);
  }
  const videos = (await (await request.get(`${ev}/videos`, { headers: h })).json()) as { items: unknown[] };
  expect(videos.items.length).toBe(4);
  expect((await request.post(`${ev}/videos`, { headers: h, data: { url: "https://vimeo.com/999" } })).status()).toBe(409);
  await page.reload();
  await expect(page.getByTestId("mat-video-url")).toHaveCount(0);
  await expect(page.getByTestId("mat-videos")).toContainText("4 of 4");

  for (const n of [2, 3]) {
    const res = await request.post(`${ev}/partner-links`, { headers: h, data: { url: `https://seller${n}.example.org/t`, seller_name: `Seller ${n}` } });
    if (res.status() !== 409) expect(res.status(), await res.text()).toBe(201);
  }
  const partners = (await (await request.get(`${ev}/partner-links`, { headers: h })).json()) as { items: unknown[] };
  expect(partners.items.length).toBe(3);
  expect((await request.post(`${ev}/partner-links`, { headers: h, data: { url: "https://x.example.org/t", seller_name: "Over" } })).status()).toBe(409);

  // Gallery: fill to 6 with real uploads, the 7th is refused by the server.
  const uploaded: string[] = [];
  for (let i = 0; i < 6; i += 1) {
    const img = await makeImage(page, 10 + i * 40);
    const up = await request.post(`${env.api_base_url}/v1/media`, {
      headers: h,
      multipart: { owner_type: "event_gallery", org_id: env.org_id, content_type: "image/png", file: file(img) },
    });
    expect(up.status(), await up.text()).toBeLessThan(300);
    uploaded.push(((await up.json()) as { media_object: { id: string } }).media_object.id);
  }
  let added = 0;
  let refused = 0;
  for (const id of uploaded) {
    const res = await request.post(`${ev}/media-items`, { headers: h, data: { media_id: id } });
    if (res.status() === 201) added += 1;
    else if (res.status() === 409) refused += 1;
  }
  expect(added + refused).toBe(6);
  const gallery = (await (await request.get(`${ev}/media-items`, { headers: h })).json()) as { items: unknown[] };
  expect(gallery.items.length).toBe(6);
  await page.reload();
  await expect(page.getByTestId("mat-gallery")).toContainText("6 of 6");
  await expect(page.getByTestId("mat-gallery-file")).toHaveCount(0);
  const extra = await request.post(`${env.api_base_url}/v1/media`, {
    headers: h,
    multipart: { owner_type: "event_gallery", org_id: env.org_id, content_type: "image/png", file: file(await makeImage(page, 300)) },
  });
  const extraId = ((await extra.json()) as { media_object: { id: string } }).media_object.id;
  expect((await request.post(`${ev}/media-items`, { headers: h, data: { media_id: extraId } })).status()).toBe(409);
  await shot(page, "event-center-mobile-limits");
});

test("a rejected map save keeps the entered coordinates and never reports success", async ({ page }) => {
  await openCard(page);
  await page.getByTestId("mat-map-lat").fill("12.34");
  await page.getByTestId("mat-map-lng").fill("56.78");
  await page.route(`**/events/${env.event_id}/map-point`, async route => {
    if (route.request().method() === "PUT") {
      await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: { code: "service.unavailable", message: "Test-only refusal" } }) });
    } else await route.continue();
  });
  await page.getByTestId("mat-map-save").click();
  await expect(page.getByTestId("mat-map-error")).toBeVisible();
  await expect(page.getByTestId("mat-map-ok")).toHaveCount(0);
  await expect(page.getByTestId("mat-map-lat")).toHaveValue("12.34");
  await expect(page.getByTestId("mat-map-lng")).toHaveValue("56.78");
});

test("a wrong file is refused before upload", async ({ page }) => {
  await openCard(page);
  // Free a place first: the file input is hidden at 6 of 6.
  const first = page.getByTestId("mat-report-file");
  await first.setInputFiles({ name: "notes.txt", mimeType: "text/plain", buffer: Buffer.from("not a picture") });
  await expect(page.getByTestId("mat-report-reject")).toBeVisible();
});

for (const role of ["org_admin", "organizer", "agent"] as const) {
  test(`the ${role} sees and can do exactly what the server allows`, async ({ page, request }) => {
    const me = await request.get(`${orgBase}/me`, { headers: auth(role) });
    expect(me.status()).toBe(200);
    const perms = new Set(((await me.json()) as { permissions: string[] }).permissions);
    const canRead = perms.has("event.read");
    const canWrite = perms.has("event.update");
    expect(canRead, "fixture role grants must not silently remove coverage").toBe(true);
    expect(canWrite, "owner and manager may edit; agent must remain read-only").toBe(role !== "agent");

    const writes: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/events/") && ["POST", "PUT", "PATCH", "DELETE"].includes(r.method())) writes.push(`${r.method()} ${r.url()}`);
    });
    await signInAs(page, person(env, role));
    await page.waitForURL((u) => !u.pathname.startsWith("/login"));
    await page.goto(`/o/${env.org_id}/events/${env.event_id}`);
    if (!canRead) {
      await expect(page.getByTestId("materials-no-read").or(page.getByTestId("materials-section"))).toBeVisible();
      return;
    }
    await expect(page.getByTestId("materials-section")).toBeVisible();
    await expect(page.getByTestId("mat-videos")).toBeVisible();
    if (canWrite) {
      await expect(page.getByTestId("materials-read-only")).toHaveCount(0);
    } else {
      await expect(page.getByTestId("materials-read-only")).toBeVisible();
      await expect(page.getByTestId("mat-video-url")).toHaveCount(0);
      await expect(page.getByTestId("mat-partner-new")).toHaveCount(0);
      await expect(page.getByTestId("mat-map-save")).toHaveCount(0);
      // The direct call is refused too.
      const direct = await request.put(`${ev}/map-point`, { headers: auth(role), data: { latitude: 1, longitude: 1 } });
      expect(direct.status()).toBe(403);
      expect(writes).toEqual([]);
    }
  });
}

test("desktop layout has no horizontal overflow", async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await context.newPage();
  await openCard(page);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(0);
  await shot(page, "event-center-desktop-materials");
  await context.close();
});

test("on a phone the materials do not overflow either", async ({ page }) => {
  await openCard(page);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(0);
});
