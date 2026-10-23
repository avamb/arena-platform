/**
 * Posters under the REAL nginx Content-Security-Policy (task 7-1). The page is
 * served by the production image and talks to a real arena-api with a local
 * media storage. It proves what a dev server cannot: the preview (a blob
 * address) and the persisted poster (bytes fetched from the API origin into a
 * blob) are drawn with `img-src 'self' data: blob:`, no policy violation is
 * reported, and the policy still refuses an image from any other address.
 */
import { expect, test, type Page } from "@playwright/test";
import { liveEnv, person, signInAs } from "../live/liveEnv";
import { scriptFacts } from "../live/posterHelpers";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const nonce = Date.now().toString(36);
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const auth = { Authorization: `Bearer ${person(env, "org_admin").access_token}` };

async function makePng(page: Page, width: number, height: number, hue: number) {
  const b64 = await page.evaluate(
    async ({ width, height, hue }) => {
      const c = document.createElement("canvas");
      c.width = width;
      c.height = height;
      const ctx = c.getContext("2d") as CanvasRenderingContext2D;
      ctx.fillStyle = `hsl(${hue} 70% 45%)`;
      ctx.fillRect(0, 0, width, height);
      const blob = await new Promise<Blob | null>((res) => c.toBlob(res, "image/png"));
      const bytes = new Uint8Array(await (blob as Blob).arrayBuffer());
      let s = "";
      for (const b of bytes) s += String.fromCharCode(b);
      return btoa(s);
    },
    { width, height, hue },
  );
  return { name: "poster.png", mimeType: "image/png", buffer: Buffer.from(b64, "base64") };
}

/** Records every CSP violation the page reports. */
async function watchCsp(page: Page) {
  const violations: string[] = [];
  await page.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (e) => {
      (window as unknown as { __csp: string[] }).__csp ??= [];
      (window as unknown as { __csp: string[] }).__csp.push(`${e.violatedDirective} ${e.blockedURI}`);
    });
  });
  page.on("console", (m) => {
    if (/content security policy/i.test(m.text())) violations.push(`console: ${m.text().slice(0, 400)}`);
  });
  return {
    all: async () => [...violations, ...(await page.evaluate(() => (window as unknown as { __csp?: string[] }).__csp ?? []))],
  };
}

test("the container sends the policy with blob: in img-src only", async ({ request }) => {
  for (const path of ["/", "/o/any/events/new", "/index.html"]) {
    const res = await request.get(path);
    const csp = res.headers()["content-security-policy"] ?? "";
    expect(csp, path).toContain("img-src 'self' data: blob:;");
    expect(csp, path).toContain("script-src 'self';");
    expect(csp, path).toContain("object-src 'none'");
    expect(csp, path).toContain("frame-ancestors 'none'");
    expect(csp, path).toContain(`connect-src 'self' ${new URL(env.api_base_url).origin};`);
    expect(csp, path).not.toMatch(/\*|unsafe-|https?:\/\/(?!127\.0\.0\.1)/);
    // The one change of this release: blob: is NOT in any other directive.
    expect(csp.replace("img-src 'self' data: blob:;", "")).not.toContain("blob:");
  }
});

test("a chosen file is previewed and a created event shows its poster, with no policy violation", async ({ page, request }) => {
  const csp = await watchCsp(page);
  const name = `Poster Container ${nonce}`;
  await signInAs(page, person(env, "org_admin"));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`), { timeout: 20_000 });
  await page.getByTestId("events-create").click();
  await page.getByTestId("wizard-name").fill(name);
  await page.getByTestId("wizard-venue-mode-existing").check();
  await page.getByTestId("wizard-venue-pick").selectOption({ label: "E2E Hall" });
  await page.getByTestId("wizard-currency").fill("EUR");
  const day = page.getByTestId("wizard-date-0-day");
  await day.getByTestId("wizard-date-0-day-toggle").click();
  await day.getByTestId("date-quick-week").click();
  await page.getByTestId("wizard-date-0-time").fill("19:00");
  await page.getByTestId("wizard-cat-0-0-price").fill("18,90");
  await expect(page.getByTestId("wizard-status")).toContainText("Everything you filled in is correct", { timeout: 25_000 });

  const png = await makePng(page, 480, 640, 200);
  await page.getByTestId("wizard-poster-picker-input").setInputFiles(png);
  const preview = page.getByTestId("wizard-poster-picker-preview");
  await expect(preview).toBeVisible();
  // The preview is a blob address and it is really drawn under the real policy.
  expect(await preview.getAttribute("src")).toMatch(/^blob:/);
  await expect.poll(() => preview.evaluate((i) => (i as HTMLImageElement).complete && (i as HTMLImageElement).naturalWidth)).toBe(480);

  // Poster first: the picture is uploaded at once. The review of the hints (scripted model, real route)
  // is drawn under the same policy, and filling it in is explicit.
  await expect(page.getByTestId("wizard-poster-ok")).toBeVisible({ timeout: 20_000 });
  scriptFacts({ name: "Hint under the real policy", age: "12+" });
  await page.getByTestId("wizard-poster-read-button").click();
  await expect(page.getByTestId("poster-review")).toBeVisible({ timeout: 20_000 });
  await expect(page.getByTestId("wizard-age")).toHaveValue("");
  await page.getByTestId("poster-review-row-name").uncheck();
  await page.getByTestId("poster-review-apply").click();
  await expect(page.getByTestId("wizard-age")).toHaveValue("12+");
  await expect(page.getByTestId("wizard-name")).toHaveValue(name);

  await page.getByTestId("wizard-create").click();
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/[0-9a-f-]{36}$`), { timeout: 30_000 });
  const poster = page.getByTestId("poster-image");
  await expect(poster).toBeVisible({ timeout: 20_000 });
  expect(await poster.getAttribute("src")).toMatch(/^blob:/);
  await expect.poll(() => poster.evaluate((i) => (i as HTMLImageElement).complete && (i as HTMLImageElement).naturalWidth)).toBe(480);

  // The server really holds that event with that poster.
  const rows = ((await (await request.get(`${base}/events`, { headers: auth })).json()) as { events: { id: string; name: string; poster_media_id: string | null }[] }).events.filter((e) => e.name === name);
  expect(rows).toHaveLength(1);
  expect(rows[0].poster_media_id).not.toBeNull();

  await page.reload();
  await expect(page.getByTestId("poster-image")).toBeVisible({ timeout: 20_000 });
  await expect.poll(() => page.getByTestId("poster-image").evaluate((i) => (i as HTMLImageElement).complete && (i as HTMLImageElement).naturalWidth)).toBe(480);

  // Replace it from the card: the new preview and the new poster are drawn too.
  const second = await makePng(page, 640, 360, 20);
  await page.getByTestId("poster-picker-input").setInputFiles(second);
  await expect(page.getByTestId("poster-picker-preview")).toBeVisible();
  await expect.poll(() => page.getByTestId("poster-picker-preview").evaluate((i) => (i as HTMLImageElement).naturalWidth)).toBe(640);
  await page.getByTestId("poster-save").click();
  await expect(page.getByTestId("poster-saved")).toBeVisible({ timeout: 30_000 });
  await expect.poll(() => page.getByTestId("poster-image").evaluate((i) => (i as HTMLImageElement).complete && (i as HTMLImageElement).naturalWidth), { timeout: 20_000 }).toBe(640);

  expect(await csp.all()).toEqual([]);

  // Cleanup: the draft of this run.
  const del = await request.delete(`${base}/events/${rows[0].id}`, { headers: auth });
  expect(del.status()).toBeLessThan(300);
});

test("the policy still refuses an image from the API origin or any other address", async ({ page }) => {
  const csp = await watchCsp(page);
  await page.goto("/login");
  await page.evaluate((apiOrigin) => {
    for (const src of [`${apiOrigin}/v1/media-files/00000000-0000-0000-0000-000000000000`, "http://example.invalid/x.png"]) {
      const img = document.createElement("img");
      img.src = src;
      document.body.appendChild(img);
    }
  }, new URL(env.api_base_url).origin);
  await expect.poll(async () => (await csp.all()).length, { timeout: 10_000 }).toBeGreaterThanOrEqual(2);
  for (const v of await csp.all()) expect(v).toContain("img-src");
});
