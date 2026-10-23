import { expect, test, type Page } from "@playwright/test";
import { liveEnv, person } from "../live/liveEnv";

const env = liveEnv();
const origin = `http://127.0.0.1:${process.env.ARENA_LIVE_PUBLIC_UI_PORT ?? 18095}`;
const ev = `${env.api_base_url}/v1/organizations/${env.org_id}/events/${env.event_id}`;
const headers = { Authorization: `Bearer ${person(env, "org_admin").access_token}` };
test.describe.configure({ mode: "serial" });

test.beforeAll(async ({ request, browser }) => {
  for (const list of ["media-items", "videos", "partner-links"]) {
    const res = await request.get(`${ev}/${list}`, { headers });
    expect(res.ok()).toBeTruthy();
    for (const item of (await res.json()).items) {
      expect((await request.delete(`${ev}/${list}/${item.id}`, { headers })).ok()).toBeTruthy();
    }
  }
  // Real media storage and real public responses: no intercepted API calls.
  const encoder = await browser.newPage();
  const png = Buffer.from(await encoder.evaluate(() => {
    const canvas = document.createElement("canvas");
    canvas.width = 640; canvas.height = 400;
    const ctx = canvas.getContext("2d")!;
    ctx.fillStyle = "#4f46e5"; ctx.fillRect(0, 0, 640, 400);
    ctx.fillStyle = "white"; ctx.font = "40px sans-serif";
    ctx.fillText("Local gallery fixture", 45, 205);
    return canvas.toDataURL("image/png").split(",")[1]!;
  }), "base64");
  await encoder.close();
  for (const caption of ["Gallery first", "Gallery second"]) {
    const up = await request.post(`${env.api_base_url}/v1/media`, { headers,
      multipart: { org_id: env.org_id, owner_type: "event_gallery", content_type: "image/png",
        file: { name: "fixture.png", mimeType: "image/png", buffer: png } } });
    expect(up.ok(), await up.text()).toBeTruthy();
    const added = await request.post(`${ev}/media-items`, { headers, data: { media_id: (await up.json()).media_object.id, caption } });
    expect(added.ok()).toBeTruthy();
  }
  expect((await request.post(`${ev}/videos`, { headers, data: { url: "https://www.youtube.com/watch?v=aqz-KE-bpKQ" } })).ok()).toBeTruthy();
  expect((await request.post(`${ev}/partner-links`, { headers, data: { url: "https://example.org/tickets", seller_name: "Independent Seller <b>text</b>" } })).ok()).toBeTruthy();
  expect((await request.put(`${ev}/map-point`, { headers, data: { latitude: 50.0755, longitude: 14.4378, label: "Main entrance" } })).ok()).toBeTruthy();
  const venues = await (await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/venues`, { headers })).json();
  const venue = venues.venues.find((v: { name: string }) => v.name === "E2E Hall");
  expect((await request.patch(`${env.api_base_url}/v1/organizations/${env.org_id}/venues/${venue.id}`, { headers,
    data: { address: "Verified street 12", address_line1: "Verified street 12", country: "CZ", postal_code: "11000" } })).ok()).toBeTruthy();
});

async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBeTruthy();
}

for (const width of [375, 1280]) {
  test(`production page ${width}: real materials, keyboard, no duplicates or premature video traffic`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const externalVideo: string[] = [];
    page.on("request", r => { if (/youtube|vimeo|rutube/.test(r.url())) externalVideo.push(r.url()); });
    const res = await page.goto(`${origin}/e2e-event-center-org/e2e-materials-event?lang=en`);
    expect(res?.status()).toBe(200);
    const csp = res?.headers()["content-security-policy"] ?? "";
    expect(csp).toContain("frame-src");
    for (const host of ["www.youtube-nocookie.com", "player.vimeo.com", "rutube.ru"]) expect(csp).toContain(host);
    await expect(page.locator(".asa-photos__btn")).toHaveCount(2);
    await expect.poll(() => page.locator(".asa-photos__img").first().evaluate((img: HTMLImageElement) => img.naturalWidth)).toBeGreaterThan(0);
    await expect(page.locator(".asa-venue__address")).toContainText("Verified street 12");
    await expect(page.locator(".asa-partner__seller")).toHaveText("Seller: Independent Seller <b>text</b>");
    await expect(page.locator(".asa-materials__report")).toHaveCount(0);
    await expect(page.locator("arena-tickets").first()).toHaveAttribute("materials", "hidden");
    await expect(page.locator("arena-tickets [data-materials-section]")).toHaveCount(0);
    expect(externalVideo).toEqual([]);
    const opener = page.locator(".asa-photos__btn").first();
    await opener.click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await page.keyboard.press("ArrowRight");
    await expect(page.locator(".asa-lightbox__caption")).toHaveText("Gallery second");
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(opener).toBeFocused();
    await noOverflow(page);
    await page.screenshot({ path: `../../docs/tasks/sales_page_materials_screens/public-${width}.png`, fullPage: true });
  });
}

test("standalone widget renders the same real materials and restores photo focus", async ({ page }) => {
  await page.goto(`${origin}/e2e-event-center-org/e2e-materials-event?lang=en`);
  await page.waitForFunction(() => customElements.get("arena-tickets") !== undefined);
  await page.evaluate(({ api, id }) => {
    const widget = document.createElement("arena-tickets");
    widget.setAttribute("api-base", api);
    widget.setAttribute("feed-token", "e2e-materials-feed-token");
    widget.setAttribute("session-id", id);
    widget.setAttribute("locale", "en");
    document.querySelector("main")!.replaceChildren(widget);
  }, { api: env.api_base_url, id: env.session_id });
  const widget = page.locator("arena-tickets");
  await expect(widget.locator('[data-materials-section="gallery"]')).toHaveCount(1);
  await expect.poll(() => widget.locator(".mat-photo-img").first().evaluate((img: HTMLImageElement) => img.naturalWidth)).toBeGreaterThan(0);
  await expect(widget.locator(".mat-venue")).toContainText("Verified street 12");
  await expect(widget.locator('[data-materials-section="partners"]')).toContainText("Independent Seller <b>text</b>");
  await expect(widget.locator('[data-materials-section="report"]')).toHaveCount(0);
  const opener = widget.locator(".mat-photo-btn").first();
  await opener.click();
  await expect(widget.getByRole("dialog")).toBeVisible();
  await page.keyboard.press("ArrowRight");
  await expect(widget.locator(".mat-lightbox-caption")).toHaveText("Gallery second");
  await page.keyboard.press("Escape");
  await expect(widget.getByRole("dialog")).toHaveCount(0);
  await expect(opener).toBeFocused();
  await noOverflow(page);
  await page.screenshot({ path: "../../docs/tasks/sales_page_materials_screens/widget-public.png", fullPage: true });
});
