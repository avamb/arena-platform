/**
 * Twin test "fill in from your event page" (spec 59), against a REAL arena-api.
 *
 * The organizer's page is served by a small HTTP server this spec starts on
 * 127.0.0.1. arena-api reads it through the egress guard, which the live harness
 * opens for local addresses with EGRESS_ALLOW_PRIVATE_TARGETS (a local stand
 * setting, refused in production). Nothing else is fake: the route, its access
 * check, the page extraction and the stored picture are the real ones. No text
 * model is configured in this harness, so the model is never called.
 *
 * The bot's twin of this flow is TestBotE2E_PageLinkHintsComeFromTheAPI.
 */
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { expect, test, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs } from "./liveEnv";
import { auth, makeImage, type Img } from "./posterHelpers";

test.describe.configure({ mode: "serial" });
const env = liveEnv();
const nonce = Date.now().toString(36);
const name = `Page Link ${nonce}`;

let site: Server;
let siteBase = "";
let picture: Img | null = null;

function jsonLdPage(): string {
  const ld = {
    "@context": "https://schema.org",
    "@type": "TheaterEvent",
    name,
    description: "A comedy in two acts.",
    image: "/poster.png",
    location: { "@type": "Place", name: "Teatro Kolybel", address: { addressLocality: "Madrid", addressCountry: "ES" } },
    subEvent: [
      { "@type": "TheaterEvent", startDate: "2099-11-20T19:00:00+01:00" },
      { "@type": "TheaterEvent", startDate: "2099-11-21T17:00:00+01:00" },
    ],
    offers: [{ "@type": "Offer", name: "Stalls", price: "25", priceCurrency: "EUR" }],
  };
  return `<!doctype html><html lang="en"><head><title>${name}</title>
<script type="application/ld+json">${JSON.stringify(ld)}</script></head><body><h1>${name}</h1></body></html>`;
}

test.beforeAll(async () => {
  site = createServer((req, res) => {
    if (req.url === "/show") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      res.end(jsonLdPage());
    } else if (req.url === "/no-dates") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      res.end(`<!doctype html><html><head><meta property="og:title" content="${name} without dates">
<meta property="og:description" content="Dates follow soon."></head><body></body></html>`);
    } else if (req.url === "/poster.png" && picture !== null) {
      res.writeHead(200, { "Content-Type": picture.mime });
      res.end(picture.buffer);
    } else {
      res.writeHead(404);
      res.end();
    }
  });
  await new Promise<void>((ok) => site.listen(0, "127.0.0.1", ok));
  siteBase = `http://127.0.0.1:${(site.address() as AddressInfo).port}`;
});

test.afterAll(async () => {
  await new Promise<void>((ok) => site.close(() => ok()));
});

async function openWizard(page: Page) {
  await signInAs(page, person(env, "org_admin"));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`), { timeout: 20_000 });
  await page.getByTestId("events-create").click();
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/new`));
  await expect(page.getByTestId("wizard-page")).toBeVisible();
}

async function readPage(page: Page, url: string) {
  await page.getByTestId("wizard-page-url").fill(url);
  await page.getByTestId("wizard-page-read").click();
  await expect(page.getByTestId("wizard-page")).not.toHaveAttribute("data-state", "reading", { timeout: 70_000 });
}

test("a ticketing platform's page is refused, the organizer's page gives suggestions and its picture", async ({ page, request }) => {
  await openWizard(page);
  await noA11yViolations(page);
  picture = await makeImage(page, "image/png", 1000, 1250, 210);

  await readPage(page, "https://www.ticketmaster.es/event/123");
  await expect(page.getByTestId("wizard-page-failed")).toHaveAttribute("data-reason", "ticketing");

  const answered = page.waitForResponse((r) => r.url().endsWith("/event-page-facts") && r.request().method() === "POST" && r.status() === 200);
  await readPage(page, `${siteBase}/show`);
  const mediaId: string = (await (await answered).json()).facts.image.media_id;
  await expect(page.getByTestId("wizard-page")).toHaveAttribute("data-state", "ready");
  await expect(page.getByTestId("poster-review-value-name")).toHaveText(name);
  await expect(page.getByTestId("poster-review-value-date")).toHaveText("November 20, 2099");
  await expect(page.getByTestId("wizard-page-other-dates")).toContainText("November 21, 2099, 17:00");
  // Nothing is filled in before the press.
  await expect(page.getByTestId("wizard-name")).toHaveValue("");

  await expect(page.getByTestId("wizard-page-image")).toContainText("1000×1250");
  await expect(page.getByTestId("wizard-page-image")).toHaveAttribute("data-fits", "true");
  await noA11yViolations(page);
  await page.getByTestId("wizard-page-image-use").click();
  await expect(page.getByTestId("wizard-page-image-used")).toBeVisible();
  await expect(page.getByTestId("wizard-poster")).toHaveAttribute("data-decision", "poster");

  await page.getByTestId("poster-review-apply").click();
  await expect(page.getByTestId("wizard-page-done")).toBeVisible();
  await expect(page.getByTestId("wizard-name")).toHaveValue(name);

  // The picture is stored in THIS organization and attached to nothing yet.
  const media = await request.get(`${env.api_base_url}/v1/media/${mediaId}`, { headers: auth("org_admin") });
  expect(media.status()).toBe(200);
  const obj = (await media.json()).media_object;
  expect(obj.org_id).toBe(env.org_id);
  expect(obj.owner_type).toBe("event_poster");
});

test("a page without dates fills what it has and asks for nothing more", async ({ page }) => {
  await openWizard(page);
  await readPage(page, `${siteBase}/no-dates`);
  await expect(page.getByTestId("wizard-page")).toHaveAttribute("data-state", "ready");
  await expect(page.getByTestId("poster-review-value-name")).toHaveText(`${name} without dates`);
  await expect(page.getByTestId("poster-review-row-date")).toHaveCount(0);
  await expect(page.getByTestId("wizard-page-other-dates")).toHaveCount(0);
  await expect(page.getByTestId("wizard-page-image")).toHaveCount(0);
});
