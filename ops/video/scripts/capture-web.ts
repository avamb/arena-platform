/**
 * Pictures for the how-to films of the web event center (web-*), desktop window 1280 x 720.
 *
 *   npx tsx scripts/capture-web.ts <ru|en|es> <event|org|links|pay|card|reset>
 *
 * Stages (run in this order, each signs in as the test organizer of that language):
 *   event   web-create-event: the wizard "New event" filled by hand, the event card, the poster, publication, the buyers' page
 *   org     web-organization-details: "Organization data" (legal data, contact e-mail, public e-mail of the sales page), the seller block
 *   links   web-sales-link-and-qr: "Links and QR", a new link to the event of stage `event`, its card with the QR code
 *   pay     web-payments-status: the section "Payments from buyers" of "Organization data"
 *   card    web-create-event again: only the pictures of the event card of the event that stage `event` created
 *   reset   deletes the event that stage `event` created (a trial run)
 *
 * Environment (ops/video/.env, not committed):
 *   VIDEO_BASE_URL            web event center of the stand
 *   VIDEO_ONB_EMAIL_<LANG>    address of the test organizer (the accounts of film apply-on-website)
 *   VIDEO_ONB_ORG_<LANG>      organization name on the stand
 *   VIDEO_ONB_PASSWORD        password of these accounts
 *
 * Before a picture is taken the stand data is replaced in the page: the real mailbox with carmen@example.com,
 * the organization name with "Aurora Events", stand hosts with the public ones. Pictures go to
 * scenarios/<film>/captures/ (Russian) and captures/<language>/; they are not committed.
 * WEB_DEBUG=1 prints the text of the page at each picture. WEB_ONLY=<regexp> takes only the pictures with such names.
 * WEB_STOP=ready stops the stage `event` before the event is created.
 */
import fs from "node:fs";
import path from "node:path";
import QRCode from "qrcode";
import { chromium, type BrowserContext, type Locator, type Page } from "playwright";
import { assertHostAllowed } from "../src/capture.ts";
import { loadEnv, ROOT } from "../src/env.ts";

type Lang = "ru" | "en" | "es";

const SHOWN = { email: "carmen@example.com", org: "Aurora Events" };
const BROWSER_LOCALE: Record<Lang, string> = { ru: "ru-RU", en: "en-GB", es: "es-ES" };
const DEBUG = process.env.WEB_DEBUG === "1";

/** The invented event of the films: a jazz evening in Berlin, 25 EUR. */
const EVENT: Record<Lang, { name: string; text: string; category: string; poster: string }> = {
  ru: { name: "Вечер джаза", text: "Живой джаз и хорошая компания.", category: "Входной билет", poster: "scenarios/web-create-event/assets/poster_4x5.png" },
  en: { name: "Jazz Night", text: "Live jazz and good company.", category: "General admission", poster: "scenarios/web-create-event/assets/en/poster_4x5.png" },
  es: { name: "Noche de jazz", text: "Jazz en directo y buena compañía.", category: "Entrada general", poster: "scenarios/web-create-event/assets/es/poster_4x5.png" },
};
/** The day on the poster (scripts/make-web-posters.ts). */
const EVENT_DAY = "2027-02-13";
const VENUE = { name: "Aurora Club", country: "DE", city: "Berlin", timezone: "Europe/Berlin", address: "Musterstraße 12", capacity: "120" };
const ORG_DATA = {
  legalName: "Aurora Events S.L.",
  legalForm: "S.L.",
  registration: "B12345678",
  taxId: "ESB12345678",
  line1: "Calle Ejemplo 12",
  city: "Madrid",
  postal: "28001",
  country: "ES",
  phone: "+34 600 555 010",
};

function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`capture-web: environment variable ${name} is not set`);
  return v;
}

class Shots {
  constructor(private readonly film: string, private readonly lang: Lang, private readonly replace: [string, string][]) {}

  private dir(): string {
    const d = path.join(ROOT, "scenarios", this.film, "captures", this.lang === "ru" ? "" : this.lang);
    fs.mkdirSync(d, { recursive: true });
    return d;
  }

  /** Puts `at` near the top of the window (or the page top), replaces stand data, takes the window picture, restores the fields. */
  async take(page: Page, name: string, at?: Locator | null, offset = 110): Promise<void> {
    if (process.env.WEB_ONLY && !new RegExp(process.env.WEB_ONLY).test(name)) return;
    if (at) await at.first().evaluate((e, off) => window.scrollTo(0, e.getBoundingClientRect().top + window.scrollY - off), offset);
    else if (at === undefined) await page.evaluate(() => window.scrollTo(0, 0));
    await page.evaluate((pairs) => {
      const swap = (s: string) => pairs.reduce((acc, [from, to]) => acc.split(from).join(to), s).replace(/([a-z]+)-staging\./g, "$1.");
      const kept: [Node, string][] = [];
      (window as unknown as { videoKept: [Node, string][] }).videoKept = kept;
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        const t = n.nodeValue ?? "";
        const r = swap(t);
        if (r !== t) { kept.push([n, t]); n.nodeValue = r; }
      }
      document.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("input,textarea").forEach((i) => {
        if (["password", "checkbox", "radio", "hidden", "file"].includes(i.type)) return;
        const r = swap(i.value);
        if (r !== i.value) { i.dataset.videoReal = i.value; i.value = r; }
      });
      (document.activeElement as HTMLElement | null)?.blur?.();
    }, this.replace);
    await page.waitForTimeout(300);
    await page.screenshot({ path: path.join(this.dir(), `${name}.png`) });
    await page.evaluate(() => {
      // the page must keep its real texts: a later step reads an address from it
      for (const [n, t] of (window as unknown as { videoKept?: [Node, string][] }).videoKept ?? []) n.nodeValue = t;
      document.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("[data-video-real]").forEach((i) => {
        i.value = i.dataset.videoReal ?? i.value;
        delete i.dataset.videoReal;
      });
    });
    console.log(`  shot ${this.film} ${name}`);
    if (DEBUG) console.log((await page.evaluate(() => document.body.innerText)).replace(/[\w.+-]+@(?!example\.)[\w.-]+/g, "<mail>"));
  }
}

function statePath(lang: Lang): string {
  return path.join(ROOT, ".cache", `web-${lang}.json`);
}
function readState(lang: Lang): Record<string, string> {
  return fs.existsSync(statePath(lang)) ? (JSON.parse(fs.readFileSync(statePath(lang), "utf8")) as Record<string, string>) : {};
}
function writeState(lang: Lang, patch: Record<string, string>): void {
  fs.mkdirSync(path.dirname(statePath(lang)), { recursive: true });
  fs.writeFileSync(statePath(lang), JSON.stringify({ ...readState(lang), ...patch }, null, 2));
}

async function signIn(lang: Lang): Promise<{ ctx: BrowserContext; page: Page; app: string; org: string; close: () => Promise<void> }> {
  const app = env("VIDEO_BASE_URL").replace(/\/$/, "");
  assertHostAllowed(app);
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 720 }, deviceScaleFactor: 2, locale: BROWSER_LOCALE[lang], timezoneId: "Europe/Madrid" });
  // tsx wraps named functions in __name(); the page does not have it.
  await ctx.addInitScript("window.__name = (f) => f");
  const page = await ctx.newPage();
  await page.goto(`${app}/login`, { waitUntil: "networkidle" });
  await page.fill("input[type=email]", env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`));
  await page.fill("input[type=password]", env("VIDEO_ONB_PASSWORD"));
  await page.click("button[type=submit]");
  await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 30000 });
  await page.waitForLoadState("networkidle");
  await page.waitForTimeout(1200);
  const org = /\/o\/[0-9a-f-]{36}/.exec(new URL(page.url()).pathname)?.[0];
  if (!org) throw new Error("capture-web: the organization is not in the address after sign-in");
  return { ctx, page, app, org, close: () => browser.close() };
}

const tid = (page: Page, id: string) => page.getByTestId(id);

async function settle(page: Page, ms = 900): Promise<void> {
  await page.waitForLoadState("networkidle").catch(() => undefined);
  await page.waitForTimeout(ms);
}

/** web-create-event: the wizard filled by hand, the card, the poster, the publication, the buyers' page. */
async function event(lang: Lang, shots: Shots): Promise<void> {
  const { ctx, page, app, org, close } = await signIn(lang);
  const ev = EVENT[lang];
  try {
    await shots.take(page, "w_01_events");
    await tid(page, "events-create").click();
    await page.waitForURL(/\/events\/new/);
    await settle(page);
    await shots.take(page, "w_02_new");
    await tid(page, "wizard-poster-skip").click();
    await tid(page, "wizard-name").fill(ev.name);
    await tid(page, "wizard-description").locator('[role="textbox"][aria-multiline="true"]').fill(ev.text);
    await shots.take(page, "w_03_name", tid(page, "wizard-name"), 200);

    await tid(page, "wizard-venue-mode-new").check();
    await tid(page, "wizard-venue-name").fill(VENUE.name);
    const change = tid(page, "wizard-venue-country-change");
    if (await change.count()) await change.click();
    const chooser = tid(page, "wizard-venue-country-chooser");
    await chooser.getByTestId("venue-chooser-search").waitFor();
    await page.waitForTimeout(1500);
    const quick = chooser.getByTestId(`venue-chooser-quick-${VENUE.country}`);
    if (await quick.count()) await quick.click();
    else {
      await chooser.getByTestId("venue-chooser-search").fill("Germany");
      await page.waitForTimeout(1200);
      await chooser.getByTestId(`venue-chooser-country-${VENUE.country}`).click();
    }
    await tid(page, "wizard-venue-country").waitFor();
    await tid(page, "wizard-venue-city").fill(VENUE.city);
    await page.waitForTimeout(1500);
    if (await tid(page, "wizard-venue-timezone").count()) await tid(page, "wizard-venue-timezone").selectOption(VENUE.timezone);
    await tid(page, "wizard-venue-address").fill(VENUE.address);
    await tid(page, "wizard-venue-capacity").fill(VENUE.capacity);
    await settle(page);
    await shots.take(page, "w_04_venue", tid(page, "wizard-venue-mode-new"), 200);

    const day = tid(page, "wizard-date-0-day");
    await day.getByTestId("wizard-date-0-day-toggle").click();
    await page.waitForTimeout(500);
    await shots.take(page, "w_05_calendar", day, 200);
    for (let i = 0; i < 12 && !(await day.getByTestId(`date-day-${EVENT_DAY}`).count()); i += 1) await day.getByTestId("date-field-next").click();
    await day.getByTestId(`date-day-${EVENT_DAY}`).click();
    const confirm = day.getByTestId(`date-confirm-${EVENT_DAY}`);
    if (await confirm.count()) await confirm.click();
    await tid(page, "wizard-date-0-time").fill("20:00");
    const places = tid(page, "wizard-date-0-capacity");
    if (await places.count()) await places.fill(VENUE.capacity);
    await settle(page, 500);
    await shots.take(page, "w_06_date", day, 200);

    await tid(page, "wizard-cat-0-0-name").fill(ev.category);
    await tid(page, "wizard-cat-0-0-price").fill("25");
    await settle(page, 2500);
    await shots.take(page, "w_07_tickets", tid(page, "wizard-cat-0-0-name"), 320);
    await tid(page, "wizard-create").waitFor();
    for (let i = 0; i < 20 && (await tid(page, "wizard-create").isDisabled()); i += 1) await page.waitForTimeout(1500);
    // "Good to know: a venue with this name exists" comes only on a repeated run of this script; a new organizer does not see it.
    await tid(page, "wizard-warnings").evaluateAll((els) => els.forEach((e) => { (e as HTMLElement).style.display = "none"; }));
    await shots.take(page, "w_08_ready", tid(page, "wizard-status"), 230);
    if (await tid(page, "wizard-create").isDisabled()) throw new Error("capture-web: the wizard does not allow to create the event (see w_08_ready)");
    if (process.env.WEB_STOP === "ready") return;

    await tid(page, "wizard-create").click();
    await page.waitForURL(/\/events\/[0-9a-f-]{36}$/, { timeout: 40000 });
    await settle(page, 1500);
    writeState(lang, { event: new URL(page.url()).pathname });
    console.log(`  event at ${new URL(page.url()).pathname.replace(/[0-9a-f-]{36}/g, "<id>")}`);
    await card(lang, shots, page, app, ctx);
  } finally {
    await close();
  }
  void org;
}

/** The event card: poster, publication, buyers' page. Also run alone by the stage `card` for an event that exists. */
async function card(lang: Lang, shots: Shots, page: Page, _app: string, ctx: BrowserContext): Promise<void> {
  const ev = EVENT[lang];
  // An event that is on sale already (a repeated run) goes back to draft first: the film shows the publication.
  await openSection(page, "more");
  if (await tid(page, "event-status-off").count()) {
    await tid(page, "event-status-off").click();
    await tid(page, "event-off-dialog").locator("button.btn-primary, button.btn-danger").first().click();
    await settle(page, 2500);
    await page.reload({ waitUntil: "networkidle" });
    await settle(page, 1200);
  }
  await shots.take(page, "c_01_card");
  await openSection(page, "edit");
  const input = tid(page, "poster-picker-input");
  if (await tid(page, "poster-none").count()) {
    await shots.take(page, "c_02_poster_none", tid(page, "event-poster"), 220);
    await input.setInputFiles(path.join(ROOT, ev.poster));
    await settle(page, 2500);
    await shots.take(page, "c_03_poster_chosen", tid(page, "event-poster"), 220);
    await tid(page, "poster-save").click();
    await tid(page, "poster-saved").waitFor({ timeout: 40000 });
    await settle(page, 1500);
  }
  await shots.take(page, "c_04_poster_saved", tid(page, "event-poster"), 220);
  await openSection(page, "more");
  const publish = tid(page, "event-status-publish");
  await shots.take(page, "c_05_status", tid(page, "event-status"), 260);
  await publish.click();
  await tid(page, "event-status-done").waitFor({ timeout: 30000 });
  await settle(page, 1200);
  await shots.take(page, "c_06_published", tid(page, "event-status"), 260);
  await openSection(page, "edit");
  const buyer = await tid(page, "materials-public-link").getAttribute("href");
  await shots.take(page, "c_07_open_page", tid(page, "materials-public-link"), 300);
  if (!buyer) throw new Error("capture-web: the card has no link to the sales page");
  writeState(lang, { buyer });
  const p2 = await ctx.newPage();
  assertHostAllowed(buyer);
  await p2.goto(buyer, { waitUntil: "networkidle" });
  await p2.waitForTimeout(3000);
  await shots.take(p2, "b_01_page");
  if (DEBUG) await p2.screenshot({ path: path.join(ROOT, ".cache", `buyer-${lang}.png`), fullPage: true });
  for (const [i, y] of [500, 1000, 1500].entries()) {
    await p2.evaluate((top) => window.scrollTo(0, top), y);
    await shots.take(p2, `b_0${i + 2}_page`, null);
  }
  await p2.close();
}

/** Opens one of the five sections of the event card (more, link, sales, edit, promo). */
async function openSection(page: Page, id: string): Promise<void> {
  const toggle = tid(page, `event-section-toggle-${id}`);
  if ((await toggle.count()) && (await toggle.getAttribute("aria-expanded")) !== "true") {
    await toggle.click();
    await settle(page, 1000);
  }
}

async function resumeCard(lang: Lang, shots: Shots): Promise<void> {
  const { ctx, page, app, close } = await signIn(lang);
  try {
    const st = readState(lang);
    if (!st.event) throw new Error("capture-web: no event in the state file, run stage event first");
    await page.goto(`${app}${st.event}`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await card(lang, shots, page, app, ctx);
  } finally {
    await close();
  }
}

/** web-organization-details: legal data, the contact e-mail, the public e-mail of the sales page, then the seller block of the buyers' page. */
async function orgData(lang: Lang, shots: Shots): Promise<void> {
  const { ctx, page, app, org, close } = await signIn(lang);
  const email = env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`);
  try {
    await tid(page, "nav-more").click();
    await settle(page, 700);
    await tid(page, "nav-payments").scrollIntoViewIfNeeded();
    await page.waitForTimeout(400);
    await shots.take(page, "o_01_menu", null);
    await page.goto(`${app}${org}/org-data`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await shots.take(page, "o_02_top");
    await tid(page, "field-legal-name").fill(ORG_DATA.legalName);
    await tid(page, "field-legal-form").fill(ORG_DATA.legalForm);
    await tid(page, "field-registration").fill(ORG_DATA.registration);
    await tid(page, "field-tax-scheme").selectOption({ index: 1 });
    await tid(page, "field-tax-id").fill(ORG_DATA.taxId);
    await tid(page, "field-line1").fill(ORG_DATA.line1);
    await tid(page, "field-city").fill(ORG_DATA.city);
    await tid(page, "field-postal-code").fill(ORG_DATA.postal);
    await tid(page, "field-country").fill(ORG_DATA.country);
    await tid(page, "field-contact-email").fill(email);
    await tid(page, "field-contact-phone").fill(ORG_DATA.phone);
    await shots.take(page, "o_03_legal", tid(page, "field-name"), 190);
    await shots.take(page, "o_04_address", tid(page, "field-tax-id"), 190);
    await shots.take(page, "o_05_contact", tid(page, "field-contact-email"), 330);
    await tid(page, "org-data-save").click();
    // a repeated run changes nothing, and then the screen does not say "Saved"
    await tid(page, "org-data-saved").waitFor({ timeout: 12000 }).catch(() => undefined);
    await settle(page, 1000);
    await shots.take(page, "o_06_saved", tid(page, "field-contact-email"), 330);
    const card = tid(page, "seller-channel").first();
    await card.getByTestId("seller-email").fill(email);
    await card.getByTestId("seller-phone").fill(ORG_DATA.phone);
    await shots.take(page, "o_07_page_contact", card, 150);
    await card.getByTestId("seller-save").click();
    await card.getByTestId("seller-saved").waitFor({ timeout: 12000 }).catch(() => undefined);
    await settle(page, 1500);
    await shots.take(page, "o_08_page_saved", card, 150);
    await page.reload({ waitUntil: "networkidle" });
    await settle(page, 1500);
    await shots.take(page, "o_09_status", tid(page, "org-data-payments-section"), 150);
    const buyer = readState(lang).buyer;
    if (!buyer) throw new Error("capture-web: no sales page in the state file, run stage event first");
    const p2 = await ctx.newPage();
    assertHostAllowed(buyer);
    await p2.goto(buyer, { waitUntil: "networkidle" });
    await p2.waitForTimeout(3000);
    if (DEBUG) await p2.screenshot({ path: path.join(ROOT, ".cache", `buyer-org-${lang}.png`), fullPage: true });
    const seller = p2.getByText(ORG_DATA.legalName).first();
    if (await seller.count()) await shots.take(p2, "s_01_seller", seller, 330);
    else console.log("  the sales page does not show the seller block");
    await p2.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
    await shots.take(p2, "s_02_page_end", null);
    await p2.close();
  } finally {
    await close();
  }
}

const LINK_LABEL: Record<Lang, string> = { ru: "Афиша у входа", en: "Poster at the entrance", es: "Cartel en la entrada" };

/** web-sales-link-and-qr: the link section of the event card, a new tracked link, its QR code, the buyers' page behind it, the count. */
async function links(lang: Lang, shots: Shots): Promise<void> {
  const { ctx, page, app, org, close } = await signIn(lang);
  try {
    const st = readState(lang);
    if (!st.event) throw new Error("capture-web: no event in the state file, run stage event first");
    await page.goto(`${app}${st.event}`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await openSection(page, "link");
    await shots.take(page, "k_01_card");
    await tid(page, "event-qr-link").click();
    await page.waitForURL(/\/links\/new/);
    await settle(page, 1500);
    await shots.take(page, "k_02_new");
    await page.locator('[data-testid^="link-pl-"]').first().check();
    await tid(page, "link-f-label").fill(LINK_LABEL[lang]);
    await shots.take(page, "k_03_form", page.locator('[data-testid^="link-pl-"]').first(), 250);
    if (process.env.WEB_LINK_EXISTS === "1") {
      // a repeated run: the link of the first run is opened, a second one is not created
      await page.goto(`${app}${org}/links`, { waitUntil: "networkidle" });
      await settle(page, 1200);
      await tid(page, "link-row").first().locator("a").first().click();
    } else {
      await tid(page, "link-create-go").click();
    }
    await page.waitForURL(/\/links\/[0-9a-f-]{36}(\?|$)/, { timeout: 30000 });
    await settle(page, 2500);
    writeState(lang, { link: new URL(page.url()).pathname });
    const short = (await tid(page, "link-short-url").innerText()).trim();
    assertHostAllowed(short);
    // The QR code of the stand would lead a viewer of the film to the stand. The picture shows a code of the same
    // address on the public host, the one written under the code in the frame.
    const publicQr = await QRCode.toDataURL(short.replace(/-staging\./, "."), { margin: 2, width: 440 });
    const showPublicQr = () => tid(page, "link-qr-img").evaluate((img, src) => { (img as HTMLImageElement).src = src; }, publicQr);
    await showPublicQr();
    await shots.take(page, "k_04_link");
    await shots.take(page, "k_05_qr", tid(page, "link-qr"), 70);
    await shots.take(page, "k_06_address", tid(page, "link-short-url"), 260);
    if (!process.env.WEB_ONLY || new RegExp(process.env.WEB_ONLY).test("k_07_buyer")) {
      // A buyer opens the short address. An ordinary browser name: the stand does not count a headless browser as a scan.
      const buyerCtx = await ctx.browser()!.newContext({
        viewport: { width: 1280, height: 720 },
        deviceScaleFactor: 2,
        locale: BROWSER_LOCALE[lang],
        timezoneId: "Europe/Madrid",
        userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
      });
      await buyerCtx.addInitScript("window.__name = (f) => f");
      const p2 = await buyerCtx.newPage();
      await p2.goto(short, { waitUntil: "networkidle" });
      await p2.waitForTimeout(3000);
      assertHostAllowed(p2.url());
      console.log(`  the short link leads to ${new URL(p2.url()).host}`);
      await shots.take(p2, "k_07_buyer");
      await buyerCtx.close();
      for (let i = 0; i < 6; i += 1) {
        await page.reload({ waitUntil: "networkidle" });
        await settle(page, 2500);
        if (!/0/.test(await tid(page, "link-scans").innerText())) break;
        await page.waitForTimeout(5000);
      }
    }
    console.log(`  scans: ${(await tid(page, "link-scans").innerText()).replace(/\s+/g, " ")}`);
    await showPublicQr();
    await shots.take(page, "k_08_counts", tid(page, "link-short-url"), 200);
    await shots.take(page, "k_09_stats", tid(page, "link-stats"), 200);
    await page.goto(`${app}${org}/links`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await shots.take(page, "k_10_list");
  } finally {
    await close();
  }
}

/** web-payments-status: where the section "Payments from buyers" is and what it says. */
async function pay(lang: Lang, shots: Shots): Promise<void> {
  const { ctx, page, app, org, close } = await signIn(lang);
  try {
    await shots.take(page, "p_01_events");
    await tid(page, "nav-more").click();
    await settle(page, 700);
    await tid(page, "nav-payments").scrollIntoViewIfNeeded();
    await page.waitForTimeout(400);
    await shots.take(page, "p_02_menu", null);
    await tid(page, "nav-payments").click();
    await settle(page, 1500);
    await shots.take(page, "p_03_not_connected", tid(page, "org-data-payments-section"), 200);
    await shots.take(page, "p_04_org_data");
    await shots.take(page, "p_06_receipt", tid(page, "org-data-receipt"), 260);
    if (process.env.WEB_PAY_CONNECTED === "1") {
      // The test organizations have no payment provider and no stand account with one is open to this script.
      // The answer of the stand to "payment configs" gets one accepted config, so the screen draws its own "connected" note.
      await ctx.route(/\/v1\/organizations\/[0-9a-f-]+\/payment-configs/, async (route) => {
        if (route.request().method() !== "GET") return route.continue();
        const res = await route.fetch();
        const item = { id: "00000000-0000-7000-8000-000000000001", provider: "stripe", mode: "live", status: "active", is_active: true, verification_status: "ok" };
        return route.fulfill({ response: res, json: { payment_configs: [item] } });
      });
      await page.goto(`${app}${org}/org-data`, { waitUntil: "networkidle" });
      await settle(page, 1500);
      await shots.take(page, "p_05_connected", tid(page, "org-data-payments-section"), 200);
    }
  } finally {
    await close();
  }
}

/** Deletes the event of the state file (a trial run): off sale, "Delete", the typed word. Only the test organizer's own event. */
async function reset(lang: Lang): Promise<void> {
  const { page, app, close } = await signIn(lang);
  try {
    const st = readState(lang);
    if (!st.event) return;
    await page.goto(`${app}${st.event}`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await openSection(page, "more");
    if (await tid(page, "event-status-off").count()) {
      await tid(page, "event-status-off").click();
      await tid(page, "event-off-dialog").locator("button.btn-primary").first().click();
      await settle(page, 2500);
      await page.reload({ waitUntil: "networkidle" });
      await settle(page, 1200);
      await openSection(page, "more");
    }
    await tid(page, "event-status-delete").click();
    await tid(page, "dry-run-confirm").click();
    const dialog = tid(page, "confirm-word");
    await dialog.waitFor();
    const word = { ru: "УДАЛИТЬ", en: "DELETE", es: "ELIMINAR" }[lang];
    await dialog.locator("input").fill(word);
    await tid(page, "confirm-word-submit").click();
    await page.waitForURL(/\/events$/, { timeout: 30000 });
    fs.rmSync(statePath(lang));
    console.log("  event deleted");
  } finally {
    await close();
  }
}

async function main(): Promise<void> {
  loadEnv();
  const lang = process.argv[2] as Lang;
  const stage = process.argv[3];
  if (!["ru", "en", "es"].includes(lang) || !["event", "card", "reset", "org", "links", "pay"].includes(stage)) {
    throw new Error("usage: tsx scripts/capture-web.ts <ru|en|es> <event|card|org|links|pay>");
  }
  const film = { event: "web-create-event", card: "web-create-event", reset: "web-create-event", org: "web-organization-details", links: "web-sales-link-and-qr", pay: "web-payments-status" }[stage]!;
  const shots = new Shots(film, lang, [
    [env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`), SHOWN.email],
    [env(`VIDEO_ONB_ORG_${lang.toUpperCase()}`), SHOWN.org],
    [env(`VIDEO_ONB_ORG_${lang.toUpperCase()}`).toLowerCase().replace(/[^a-z0-9]+/g, "-"), "aurora-events"],
  ]);
  console.log(`${film} ${lang} ${stage}`);
  if (stage === "event") await event(lang, shots);
  else if (stage === "card") await resumeCard(lang, shots);
  else if (stage === "reset") await reset(lang);
  else if (stage === "org") await orgData(lang, shots);
  else if (stage === "links") await links(lang, shots);
  else await pay(lang, shots);
}

main().catch((e) => {
  console.error(e instanceof Error ? e.message : e);
  process.exit(1);
});
