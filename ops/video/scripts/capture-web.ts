/**
 * Pictures for the how-to films of the web event center (web-*), desktop window 1280 x 720.
 *
 *   npx tsx scripts/capture-web.ts <ru|en|es> <stage>
 *
 * Stages (each signs in as the test organizer of that language); the full list and its order are in the README,
 * section "How-to films of the web event center". The first ones:
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
/**
 * WEB_ACCOUNT=es: the pictures of language <lang> are taken in the organization of ANOTHER test account (the
 * only one with paid orders). The browser language decides the language of the screens; the data of the
 * organization (the name of the event, the buyers) stay as they are.
 */
const account = (lang: Lang): Lang => (process.env.WEB_ACCOUNT as Lang | undefined) ?? lang;

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
      // a half-hidden address (a***@a***.com) keeps the first letters of the real mailbox
      const swap = (s: string) => pairs.reduce((acc, [from, to]) => acc.split(from).join(to), s).replace(/([a-z]+)-staging\./g, "$1.").replace(/\w\*+@\w\*+\.[a-z]+/g, "c*****@e******.com")
        // any other real mailbox (a buyer of a paid test order): an invented address by the name in it
        .replace(/[\w.+-]+@(?!example\.)[\w-]+(\.[\w-]+)+/g, (m) => `${/lukas|weber/i.test(m) ? "lukas" : /carmen|ortega/i.test(m) ? "carmen" : "buyer"}@example.com`);
      const kept: [Node, string][] = [];
      (window as unknown as { videoKept: [Node, string][] }).videoKept = kept;
      // the ticket widget of the sales page lives in a shadow tree: its texts and fields are replaced too
      const roots: (Document | ShadowRoot)[] = [document];
      for (let i = 0; i < roots.length; i += 1) roots[i].querySelectorAll("*").forEach((e) => { if (e.shadowRoot) roots.push(e.shadowRoot); });
      const fields: HTMLInputElement[] = [];
      (window as unknown as { videoFields: HTMLInputElement[] }).videoFields = fields;
      for (const root of roots) {
        const walker = document.createTreeWalker(root === document ? document.body : root, NodeFilter.SHOW_TEXT);
        for (let n = walker.nextNode(); n; n = walker.nextNode()) {
          const t = n.nodeValue ?? "";
          const r = swap(t);
          if (r !== t) { kept.push([n, t]); n.nodeValue = r; }
        }
        root.querySelectorAll<HTMLInputElement>("input,textarea").forEach((i) => {
          if (["password", "checkbox", "radio", "hidden", "file"].includes(i.type)) return;
          const r = swap(i.value);
          if (r !== i.value) { i.dataset.videoReal = i.value; i.value = r; fields.push(i); }
        });
      }
      (document.activeElement as HTMLElement | null)?.blur?.();
    }, this.replace);
    await page.waitForTimeout(300);
    await page.screenshot({ path: path.join(this.dir(), `${name}.png`) });
    await page.evaluate(() => {
      // the page must keep its real texts: a later step reads an address from it
      for (const [n, t] of (window as unknown as { videoKept?: [Node, string][] }).videoKept ?? []) n.nodeValue = t;
      for (const i of (window as unknown as { videoFields?: HTMLInputElement[] }).videoFields ?? []) {
        i.value = i.dataset.videoReal ?? i.value;
        delete i.dataset.videoReal;
      }
    });
    console.log(`  shot ${this.film} ${name}`);
    if (DEBUG) console.log((await page.evaluate(() => document.body.innerText)).replace(/[\w.+-]+@(?!example\.)[\w.-]+/g, "<mail>"));
  }
}

function statePath(lang: Lang): string {
  return path.join(ROOT, ".cache", `web-${account(lang)}${process.env.WEB_STATE ? `-${process.env.WEB_STATE}` : ""}.json`);
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
  await page.fill("input[type=email]", env(`VIDEO_ONB_EMAIL_${account(lang).toUpperCase()}`));
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

/** WEB_DEBUG=1: prints the test ids of the page, to write the next step of a stage. */
async function ids(page: Page, label: string): Promise<void> {
  if (!DEBUG) return;
  console.log(`TESTIDS ${label}:`, await page.evaluate(() => Array.from(document.querySelectorAll("[data-testid]")).map((e) => e.getAttribute("data-testid")).join(" ")));
}

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
    // WEB_BRAND=1: the event is sold under the brand of stage `brand`, so the public page of the brand shows it.
    // Use it with WEB_STATE=brand, to keep the event of the other films in its own state file.
    if (process.env.WEB_BRAND === "1") {
      await tid(page, "wizard-promoter").selectOption({ label: BRAND.name });
      await settle(page, 500);
      await shots.take(page, "n_07_wizard_brand", tid(page, "wizard-promoter"), 300);
    }

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
    // WEB_DOORS=19:30: the line "Doors open" of the date, for the films about the ticket of a buyer
    if (process.env.WEB_DOORS) await tid(page, "wizard-date-0-doors").fill(process.env.WEB_DOORS);
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
    // The note is the real one of the organization: "not connected" in a test organization without a provider,
    // "connected" in the one with a test payment config (WEB_ACCOUNT=es). The screen shows no key and no account.
    const connected = (await tid(page, "org-data-payments-connected").count()) > 0;
    await shots.take(page, connected ? "p_05_connected" : "p_03_not_connected", tid(page, "org-data-payments-section"), 200);
    await shots.take(page, "p_04_org_data");
    await shots.take(page, "p_06_receipt", tid(page, "org-data-receipt"), 260);
  } finally {
    await close();
  }
}

const BUYER_UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36";

/** A window of a buyer: no sign-in, an ordinary browser name. */
async function buyerPage(ctx: BrowserContext, lang: Lang): Promise<{ page: Page; close: () => Promise<void> }> {
  const buyer = readState(lang).buyer;
  if (!buyer) throw new Error("capture-web: no sales page in the state file, run stage event first");
  assertHostAllowed(buyer);
  const bctx = await ctx.browser()!.newContext({ viewport: { width: 1280, height: 720 }, deviceScaleFactor: 2, locale: BROWSER_LOCALE[lang], timezoneId: "Europe/Madrid", userAgent: BUYER_UA });
  await bctx.addInitScript("window.__name = (f) => f");
  const page = await bctx.newPage();
  await page.goto(buyer, { waitUntil: "networkidle" });
  await page.waitForTimeout(3000);
  return { page, close: () => bctx.close() };
}

/** The text of the ticket widget (it lives in a shadow tree, the page text does not have it). */
async function widgetText(page: Page): Promise<string> {
  return page.evaluate(() => {
    const sr = document.querySelector("arena-tickets")?.shadowRoot;
    if (!sr) return "";
    return Array.from(sr.children).filter((c) => c.tagName !== "STYLE").map((c) => (c as HTMLElement).innerText || "").join(" ").split(/\s+/).join(" ");
  });
}

/** Opens the promo code of this name, or creates it through the form. With `shots` the form is filmed. */
async function promoCode(page: Page, app: string, org: string, code: string, percent: string, shots?: Shots): Promise<void> {
  await page.goto(`${app}${org}/promo`, { waitUntil: "networkidle" });
  await settle(page, 1200);
  const row = tid(page, "promo-row").filter({ hasText: code });
  if (await row.count()) {
    await row.first().locator("a").first().click();
    await page.waitForURL(/\/promo\/[0-9a-f-]{36}/);
    await settle(page, 1200);
    return;
  }
  if (shots) await shots.take(page, "r_01_list");
  await tid(page, "promo-new").click();
  await page.waitForURL(/\/promo\/new/);
  await settle(page, 1000);
  await tid(page, "promo-f-code").fill(code);
  await tid(page, "promo-f-type-percent").check();
  await tid(page, "promo-f-value").fill(percent);
  await tid(page, "promo-f-scope-all").check();
  if (shots) await shots.take(page, "r_02_form", tid(page, "promo-f-code"), 220);
  if (shots) await shots.take(page, "r_03_limits", tid(page, "promo-f-total"), 260);
  await tid(page, "promo-review").last().click();
  await tid(page, "promo-create-go").waitFor({ timeout: 20000 });
  await settle(page, 800);
  if (shots) await shots.take(page, "r_04_review", tid(page, "promo-create-go"), 420);
  await tid(page, "promo-create-go").click();
  await page.waitForURL(/\/promo\/[0-9a-f-]{36}/, { timeout: 30000 });
  await settle(page, 1500);
}

/** web-promo-codes: a new promo code, its card, then the cart of a buyer with the code applied. Nothing is bought. */
async function promo(lang: Lang, shots: Shots): Promise<void> {
  const { ctx, page, app, org, close } = await signIn(lang);
  try {
    await promoCode(page, app, org, "JAZZ20", "20", shots);
    await shots.take(page, "r_05_card");
    await page.goto(`${app}${org}/promo`, { waitUntil: "networkidle" });
    await settle(page, 1200);
    await shots.take(page, "r_06_list");
    const b = await buyerPage(ctx, lang);
    const widget = b.page.locator("arena-tickets");
    await b.page.locator(".mini-cart-cta").click();
    await b.page.waitForTimeout(1200);
    await shots.take(b.page, "b_01_cart", widget, 120);
    await b.page.locator(".promo-link").click();
    await b.page.locator("#arena-promo-input").fill("JAZZ20");
    await shots.take(b.page, "b_02_code", null);
    await b.page.locator(".promo-apply").click();
    await b.page.waitForTimeout(3000);
    await shots.take(b.page, "b_03_applied", null);
    if (DEBUG) console.log("WIDGET", await widgetText(b.page));
    await b.close();
  } finally {
    await close();
  }
}

/** web-orders-and-refund: a free order of an invented buyer (a 100 % promo code, no payment), the list, the card. */
async function order(lang: Lang, shots: Shots): Promise<void> {
  const { ctx, page, app, org, close } = await signIn(lang);
  try {
    if (!readState(lang).order) {
      await promoCode(page, app, org, "GUEST100", "100");
      const b = await buyerPage(ctx, lang);
      await b.page.locator(".mini-cart-cta").click();
      await b.page.locator(".promo-link").click();
      await b.page.locator("#arena-promo-input").fill("GUEST100");
      await b.page.locator(".promo-apply").click();
      await b.page.waitForTimeout(3000);
      await b.page.locator(".cta-btn").click();
      await b.page.locator("#bf-email").fill(env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`));
      await b.page.locator("#bf-name").fill("Carmen Ortega");
      if (DEBUG) console.log("FORM", await widgetText(b.page));
      await b.page.locator("arena-tickets form button[type=submit]").click();
      await b.page.waitForTimeout(8000);
      console.log(`  after the order: ${new URL(b.page.url()).host}${new URL(b.page.url()).pathname.replace(/[0-9a-f-]{36}/g, "<id>")}`);
      if (DEBUG) console.log("DONE", (await widgetText(b.page)) || (await b.page.evaluate(() => document.body.innerText)));
      if (DEBUG) await b.page.screenshot({ path: path.join(ROOT, ".cache", `order-${lang}.png`) });
      await b.close();
      writeState(lang, { order: "made" });
    }
    await page.goto(`${app}${org}/orders`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await shots.take(page, "d_01_orders");
    await tid(page, "order-card-open").first().click();
    await page.waitForURL(/\/orders\/[0-9a-f-]{36}/);
    await settle(page, 1800);
    writeState(lang, { orderPath: new URL(page.url()).pathname });
    await shots.take(page, "d_02_card");
    await shots.take(page, "d_03_tickets", tid(page, "ticket-list"), 200);
    await shots.take(page, "d_04_letters", tid(page, "order-letters"), 160);
    await tid(page, "resend-open").click();
    await settle(page, 1200);
    await ids(page, "resend");
    await shots.take(page, "d_05_resend", tid(page, "order-letters"), 160);
    await tid(page, "resend-same").click();
    await settle(page, 1500);
    await ids(page, "resend-same");
    await shots.take(page, "d_06_resend_same", tid(page, "resend-dialog"), 200);
    const send = tid(page, "resend-send");
    if (await send.count()) {
      await send.click();
      await settle(page, 3500);
      await ids(page, "resent");
      await shots.take(page, "d_07_resent", tid(page, "order-letters"), 160);
    }
  } finally {
    await close();
  }
}

/** web-orders-and-refund, the refund part: run it last, the ticket of the order stops to be valid. */
async function refund(lang: Lang, shots: Shots): Promise<void> {
  const { page, app, close } = await signIn(lang);
  try {
    const st = readState(lang);
    if (!st.orderPath) throw new Error("capture-web: no order in the state file, run stage order first");
    await page.goto(`${app}${st.orderPath}`, { waitUntil: "networkidle" });
    await settle(page, 1800);
    await tid(page, "refund-open").click();
    await settle(page, 2000);
    await ids(page, "refund");
    if (DEBUG) console.log("REFUND", await tid(page, "refund-panel").innerText());
    await shots.take(page, "f_01_refund", tid(page, "ticket-list"), 200);
    await tid(page, "refund-close").click();
    await tid(page, "extref-open").click();
    await settle(page, 1500);
    await ids(page, "extref");
    if (DEBUG) console.log("EXTREF", await page.evaluate(() => document.querySelector("main")?.innerText.slice(0, 2500)));
    await shots.take(page, "f_02_outside", tid(page, "ticket-list"), 200);
  } finally {
    await close();
  }
}

/** Opens the card of the order with this number from the list "Orders". */
async function openOrder(page: Page, app: string, org: string, num: string): Promise<void> {
  await page.goto(`${app}${org}/orders`, { waitUntil: "networkidle" });
  await settle(page, 1500);
  await tid(page, "order-row").filter({ hasText: num }).getByTestId("order-card-open").click();
  await page.waitForURL(/\/orders\/[0-9a-f-]{36}/);
  await settle(page, 1800);
}

/**
 * web-refund-order, before the refund: the PAID order WEB_ORDER (a test card of the stand), the refund panel,
 * the last check and the password question. Without WEB_REFUND_GO=1 nothing is sent: the stage stops at the
 * password question, so it can be run in each language. With WEB_REFUND_GO=1 it sends the refund (the money of
 * the test payment goes back, this cannot be undone) and takes the answer; run it ONCE, in one language.
 */
async function paidRefund(lang: Lang, shots: Shots): Promise<void> {
  const num = env("WEB_ORDER");
  const { page, app, org, close } = await signIn(lang);
  try {
    await page.goto(`${app}${org}/orders`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await shots.take(page, "g_01_orders");
    await openOrder(page, app, org, num);
    if (!(await tid(page, "refund-open").count())) throw new Error("capture-web: the order has no button Refund (refunded already?), use stage refunded");
    await shots.take(page, "g_02_card");
    await shots.take(page, "g_03_button", tid(page, "ticket-list"), 200);
    await tid(page, "refund-open").click();
    await tid(page, "refund-pick").waitFor({ timeout: 30000 });
    await settle(page, 800);
    if (DEBUG) console.log("REFUND", await tid(page, "refund-panel").innerText());
    await shots.take(page, "g_04_pick", tid(page, "refund-panel"), 120);
    await tid(page, "refund-ticket").first().check();
    await tid(page, "refund-reason-buyer").check();
    await page.waitForTimeout(400);
    await shots.take(page, "g_05_reason", tid(page, "refund-ticket").first(), 200);
    await tid(page, "refund-continue").click();
    await tid(page, "refund-final").waitFor({ timeout: 20000 });
    await settle(page, 600);
    if (DEBUG) console.log("FINAL", await tid(page, "refund-panel").innerText());
    await shots.take(page, "g_06_final", tid(page, "refund-panel"), 120);
    await tid(page, "refund-go").click();
    const password = page.locator("#step-up-password");
    await password.waitFor({ timeout: 20000 });
    await page.waitForTimeout(600);
    await shots.take(page, "g_07_password", null);
    if (process.env.WEB_REFUND_GO !== "1") return;
    await password.fill(env("VIDEO_ONB_PASSWORD"));
    await page.locator("[aria-labelledby=step-up-title] button[type=submit]").click();
    await tid(page, "refund-result").waitFor({ timeout: 150000 });
    await settle(page, 1500);
    console.log("RESULT", await tid(page, "refund-result").innerText());
    await shots.take(page, "g_08_result", tid(page, "refund-result"), 200);
  } finally {
    await close();
  }
}

/** web-refund-order, after the refund: the card of the order WEB_ORDER, its tickets, the tab "Refunds", the screen "Refunds". */
async function refunded(lang: Lang, shots: Shots): Promise<void> {
  const num = env("WEB_ORDER");
  const { page, app, org, close } = await signIn(lang);
  try {
    await openOrder(page, app, org, num);
    await ids(page, "refunded card");
    if (DEBUG) console.log("CARD", await page.evaluate(() => document.querySelector("main")?.innerText ?? ""));
    await shots.take(page, "h_01_card");
    await shots.take(page, "h_02_tickets", tid(page, "ticket-list"), 200);
    await page.goto(`${app}${org}/orders?tab=refunds`, { waitUntil: "networkidle" });
    await settle(page, 1800);
    await shots.take(page, "h_03_tab");
    await page.goto(`${app}${org}/refunds`, { waitUntil: "networkidle" });
    await settle(page, 1800);
    if (DEBUG) console.log("QUEUE", await page.evaluate(() => document.querySelector("main")?.innerText ?? ""));
    await shots.take(page, "h_04_queue");
  } finally {
    await close();
  }
}

/** The screen of the only date of the event of stage `event`. */
async function openSession(page: Page, app: string, lang: Lang): Promise<void> {
  const st = readState(lang);
  if (!st.event) throw new Error("capture-web: no event in the state file, run stage event first");
  if (!st.session) {
    await page.goto(`${app}${st.event}`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await openSection(page, "edit");
    const href = await page.locator('a[href*="/sessions/"]').first().getAttribute("href");
    if (!href) throw new Error("capture-web: the event card has no link to its date");
    writeState(lang, { session: href });
  }
  await page.goto(`${app}${readState(lang).session}`, { waitUntil: "networkidle" });
  await settle(page, 1800);
}

/** web-invitations: a free ticket for a named guest, sent to the e-mail of the guest. */
async function invite(lang: Lang, shots: Shots): Promise<void> {
  const { page, app, org, close } = await signIn(lang);
  try {
    await page.goto(`${app}${org}/invitations`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await shots.take(page, "i_01_invitations");
    await openSession(page, app, lang);
    await shots.take(page, "i_02_session", tid(page, "session-invitations-link"), 300);
    await tid(page, "session-invitations-link").click();
    await page.waitForURL(/\/invitations$/);
    await settle(page, 1800);
    await tid(page, "inv-ga-more").first().click();
    await tid(page, "inv-name").fill("Carmen Ortega");
    await tid(page, "inv-email").fill(env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`));
    await tid(page, "inv-locale").selectOption({ label: lang.toUpperCase() }).catch(() => undefined);
    await tid(page, "inv-delivery-guest_email").check();
    await shots.take(page, "i_03_places", tid(page, "inv-ga"), 220);
    await shots.take(page, "i_04_guest", tid(page, "inv-fields"), 160);
    await shots.take(page, "i_05_delivery", tid(page, "inv-delivery"), 260);
    await tid(page, "inv-submit").click();
    await settle(page, 4000);
    await ids(page, "invited");
    await shots.take(page, "i_06_list", tid(page, "inv-list"), 220);
    await page.goto(`${app}${org}/invitations`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await shots.take(page, "i_07_all", tid(page, "org-inv-list"), 220);
  } finally {
    await close();
  }
}

/** web-invite-manager: an invitation of a manager to the team. The address is on example.com. */
async function team(lang: Lang, shots: Shots): Promise<void> {
  const { page, app, org, close } = await signIn(lang);
  try {
    await tid(page, "nav-more").click();
    await settle(page, 700);
    await tid(page, "nav-payments").scrollIntoViewIfNeeded();
    await page.waitForTimeout(400);
    await shots.take(page, "m_01_menu", null);
    await page.goto(`${app}${org}/team`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await shots.take(page, "m_02_team");
    if (!(await tid(page, "team-member").filter({ hasText: "marco@example.com" }).count())) {
      await tid(page, "team-invite-email").fill("marco@example.com");
      await tid(page, "team-invite-locale").selectOption({ index: ["en", "ru", "es"].indexOf(lang) });
      await shots.take(page, "m_03_form", tid(page, "team-invite-form"), 160);
      await tid(page, "team-invite-submit").click();
      await settle(page, 3500);
      await ids(page, "team invited");
    }
    await shots.take(page, "m_04_invited");
  } finally {
    await close();
  }
}

/** web-change-price: a new price of a category of one date, then the buyer page. */
async function price(lang: Lang, shots: Shots): Promise<void> {
  const { ctx, page, app, close } = await signIn(lang);
  try {
    await openSession(page, app, lang);
    await shots.take(page, "c_01_session", tid(page, "tiers-table"), 260);
    await tid(page, "tier-manage").first().click();
    await settle(page, 1200);
    await shots.take(page, "c_02_category", tid(page, "category-card"), 160);
    await tid(page, "price-change").click();
    await tid(page, "price-input").fill("30");
    await shots.take(page, "c_03_new_price", tid(page, "category-price-block"), 300);
    await tid(page, "price-check").click();
    await settle(page, 2500);
    await ids(page, "price check");
    await shots.take(page, "c_04_confirm", tid(page, "category-price-block"), 300);
    await tid(page, "price-save").click();
    await settle(page, 3000);
    await shots.take(page, "c_05_saved", tid(page, "category-card"), 160);
    const b = await buyerPage(ctx, lang);
    await shots.take(b.page, "c_06_buyer", b.page.locator("arena-tickets"), 260);
    await b.close();
  } finally {
    await close();
  }
}

const MOVE_DAY = "2027-02-20";
const CANCEL_WORD: Record<Lang, string> = { ru: "ОТМЕНИТЬ СЕАНС", en: "CANCEL SESSION", es: "CANCELAR SESIÓN" };

/** web-move-or-cancel-session, the move: a new day of the date. The holders of tickets (test addresses) get a letter. */
async function move(lang: Lang, shots: Shots): Promise<void> {
  const { page, app, close } = await signIn(lang);
  try {
    await openSession(page, app, lang);
    await shots.take(page, "v_01_session", tid(page, "session-change"), 260);
    await tid(page, "session-move-open").click();
    await settle(page, 1200);
    const panel = tid(page, "session-move");
    for (let i = 0; i < 12 && !(await panel.getByTestId(`date-day-${MOVE_DAY}`).count()); i += 1) await panel.getByTestId("date-field-next").click();
    await panel.getByTestId(`date-day-${MOVE_DAY}`).click();
    const confirm = panel.getByTestId(`date-confirm-${MOVE_DAY}`);
    if (await confirm.count()) await confirm.click();
    await settle(page, 2500);
    await ids(page, "move day");
    await shots.take(page, "v_02_move", panel, 160);
    if (DEBUG) console.log("MOVE", await tid(page, "session-change").innerText());
    await shots.take(page, "v_03_check", tid(page, "session-change-message-box"), 160);
    if (process.env.WEB_STOP === "move") return;
    await tid(page, "dry-run-confirm").click();
    await settle(page, 4000);
    await ids(page, "moved");
    if (DEBUG) console.log("MOVED", await tid(page, "session-change").innerText());
    await shots.take(page, "v_04_moved");
  } finally {
    await close();
  }
}

/** web-move-or-cancel-session, the cancellation. Run it last: the date stops to exist for buyers. */
async function cancel(lang: Lang, shots: Shots): Promise<void> {
  const { page, app, close } = await signIn(lang);
  try {
    await openSession(page, app, lang);
    await tid(page, "session-cancel-open").click();
    await settle(page, 2500);
    await ids(page, "cancel");
    if (DEBUG) console.log("CANCEL", await tid(page, "session-change").innerText());
    await shots.take(page, "x_01_cancel", tid(page, "session-cancel"), 160);
    if (process.env.WEB_STOP === "cancel") return;
    await tid(page, "session-cancel-continue").click();
    const dialog = tid(page, "confirm-word");
    await dialog.waitFor();
    await dialog.locator("input").fill(CANCEL_WORD[lang]);
    await shots.take(page, "x_02_word", null);
    await tid(page, "confirm-word-submit").click();
    await settle(page, 5000);
    await ids(page, "cancelled");
    await shots.take(page, "x_03_cancelled");
  } finally {
    await close();
  }
}

const BRAND = { name: "Aurora Jazz Club", address: "Calle Ejemplo 12, Madrid", phone: "+34 600 555 010", site: "aurora-jazz.example" };

/** web-brands: a brand (the name under which tickets are sold), its card and its public page. */
async function brand(lang: Lang, shots: Shots): Promise<void> {
  const { ctx, page, app, org, close } = await signIn(lang);
  try {
    await page.goto(`${app}${org}/promoters`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    if (!(await tid(page, "promoter-list").getByText(BRAND.name).count())) {
      await shots.take(page, "n_01_brands");
      await tid(page, "promoter-new").click();
      await settle(page, 800);
      await tid(page, "create-name").fill(BRAND.name);
      await tid(page, "create-address").fill(BRAND.address);
      await tid(page, "create-phone").fill(BRAND.phone);
      await tid(page, "create-email").fill(env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`));
      await tid(page, "create-website").fill(BRAND.site);
      await shots.take(page, "n_02_form", tid(page, "promoter-create"), 160);
      await shots.take(page, "n_03_form_end", tid(page, "create-phone"), 260);
      await tid(page, "create-submit").click();
      await settle(page, 3000);
    }
    await ids(page, "brand made");
    await shots.take(page, "n_04_list");
    await tid(page, "promoter-list").locator("a").first().click();
    await page.waitForURL(/\/promoters\/[0-9a-f-]{36}/);
    await settle(page, 2000);
    await ids(page, "brand card");
    if (DEBUG) console.log("CARD", await page.evaluate(() => document.querySelector("main")?.innerText ?? ""), "\nLINKS", await page.evaluate(() => Array.from(document.querySelectorAll("main a[href]")).map((a) => a.getAttribute("href")).join(" ")));
    await shots.take(page, "n_05_card");
    const pub = await page.evaluate(() => Array.from(document.querySelectorAll<HTMLAnchorElement>("main a[href^='http']")).map((a) => a.href).find((h) => /tickets/.test(new URL(h).host)) ?? "");
    if (pub) {
      assertHostAllowed(pub);
      const p2 = await ctx.newPage();
      await p2.goto(pub, { waitUntil: "networkidle" });
      await p2.waitForTimeout(3000);
      await shots.take(p2, "n_06_public");
      await p2.close();
    } else console.log("  the brand card has no link to a public page");
  } finally {
    await close();
  }
}

/** web-sales-statistics: the totals and the page statistics on the event card, the sales funnel, the summary. */
async function stats(lang: Lang, shots: Shots): Promise<void> {
  const { page, app, org, close } = await signIn(lang);
  try {
    const st = readState(lang);
    await page.goto(`${app}${st.event}`, { waitUntil: "networkidle" });
    await settle(page, 1800);
    await openSection(page, "sales");
    await shots.take(page, "t_01_totals", tid(page, "sales-figures"), 260);
    await shots.take(page, "t_02_page_stats", tid(page, "page-stats"), 160);
    await tid(page, "event-funnel-link").click();
    await page.waitForURL(/\/funnel/);
    await settle(page, 2500);
    await shots.take(page, "t_03_funnel");
    await shots.take(page, "t_04_steps", tid(page, "funnel-main"), 160);
    await shots.take(page, "t_05_sources", tid(page, "funnel-source").first(), 330);
    await page.goto(`${app}${org}/summary`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await tid(page, "summary-preset-all").click();
    await settle(page, 2500);
    await ids(page, "summary");
    await shots.take(page, "t_06_summary");
    await page.evaluate(() => window.scrollTo(0, 420));
    await shots.take(page, "t_07_summary_more", null);
  } finally {
    await close();
  }
}

/** web-buyer-messages-and-notifications: "Messages to buyers" (reminder, subscription) and "Sales notifications". */
async function messages(lang: Lang, shots: Shots): Promise<void> {
  const { page, app, org, close } = await signIn(lang);
  try {
    await page.goto(`${app}${org}/growth`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await shots.take(page, "g_01_messages");
    for (const id of ["growth-reminder", "growth-optin"]) {
      const box = tid(page, id);
      if (!(await box.isChecked())) await box.check();
    }
    await shots.take(page, "g_02_on", tid(page, "growth-settings"), 160);
    if (DEBUG) console.log("GROWTH", await tid(page, "growth-settings").innerText());
    if (await tid(page, "growth-save").isEnabled()) await tid(page, "growth-save").click();
    await settle(page, 2500);
    await ids(page, "growth saved");
    await shots.take(page, "g_03_saved", tid(page, "growth-settings"), 160);
    await shots.take(page, "g_04_subscribers", tid(page, "growth-subscribers"), 160);
    await page.goto(`${app}${org}/notifications`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await shots.take(page, "g_05_notifications");
    await shots.take(page, "g_06_guest_list", tid(page, "guest-list-subscription"), 260);
  } finally {
    await close();
  }
}

/** A phone window of a buyer (390 x 844), for the vertical films. */
async function buyerPhone(ctx: BrowserContext, lang: Lang): Promise<{ page: Page; close: () => Promise<void> }> {
  const buyer = readState(lang).buyer;
  if (!buyer) throw new Error("capture-web: no sales page in the state file, run stage event first");
  assertHostAllowed(buyer);
  const bctx = await ctx.browser()!.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, locale: BROWSER_LOCALE[lang], timezoneId: "Europe/Madrid", acceptDownloads: true });
  await bctx.addInitScript("window.__name = (f) => f");
  const page = await bctx.newPage();
  await page.goto(buyer, { waitUntil: "networkidle" });
  await page.waitForTimeout(3000);
  return { page, close: () => bctx.close() };
}

/**
 * buyer-where-is-my-ticket and buyer-how-to-read-the-date: a buyer on a phone gets a free ticket (promo code of
 * 100 %, no payment), the page after the order, the PDF ticket (saved to .cache), the form "Write to the organizer".
 */
async function ticket(lang: Lang, shots: Shots): Promise<void> {
  const { ctx, page, app, org, close } = await signIn(lang);
  try {
    await promoCode(page, app, org, "GUEST100", "100");
    const b = await buyerPhone(ctx, lang);
    const p = b.page;
    await shots.take(p, "y_01_page");
    await shots.take(p, "y_02_date", p.locator("h1").first(), 150);
    await shots.take(p, "y_03_tickets", p.locator("arena-tickets"), 110);
    await p.locator(".mini-cart-cta").click();
    await p.locator(".promo-link").click();
    await p.locator("#arena-promo-input").fill("GUEST100");
    await p.locator(".promo-apply").click();
    await p.waitForTimeout(3000);
    await p.locator(".cta-btn").click();
    await p.locator("#bf-email").fill(env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`));
    await p.locator("#bf-name").fill("Carmen Ortega");
    await shots.take(p, "y_04_form", null);
    await p.locator("arena-tickets form button[type=submit]").click();
    for (let i = 0; i < 20 && !/PDF/.test(await widgetText(p)); i += 1) await p.waitForTimeout(2000);
    const done = await widgetText(p);
    const number = /\b1\d{9}\b/.exec(done)?.[0] ?? "";
    console.log(`  order ${number || "not found"}`);
    writeState(lang, { ticketOrder: number });
    await shots.take(p, "y_05_done", p.locator("arena-tickets"), 110);
    const link = p.locator("arena-tickets a, arena-tickets button").filter({ hasText: /PDF/ }).first();
    if (await link.count()) {
      const [dl] = await Promise.all([p.waitForEvent("download", { timeout: 20000 }).catch(() => null), link.click()]);
      if (dl) {
        await dl.saveAs(path.join(ROOT, ".cache", `ticket-${lang}.pdf`));
        console.log("  PDF ticket saved");
      } else console.log("  the PDF button did not start a download");
    }
    await p.goto(readState(lang).buyer, { waitUntil: "networkidle" });
    await p.waitForTimeout(2500);
    const write = p.getByRole("button", { name: /организатору|organizer|organizador/i }).first();
    if (await write.count()) {
      await write.scrollIntoViewIfNeeded();
      await shots.take(p, "y_06_write", null);
      await write.click();
      await p.waitForTimeout(1500);
      if (DEBUG) console.log("FORM", await p.evaluate(() => document.body.innerText.slice(-900)));
      await shots.take(p, "y_07_form", null);
    }
    await b.close();
  } finally {
    await close();
  }
}

/** web-entrance-and-guest-list: the screen "Entrance to the event", the tickets of a date, the guest list files. */
async function entrance(lang: Lang, shots: Shots): Promise<void> {
  const { page, app, org, close } = await signIn(lang);
  try {
    await page.goto(`${app}${org}/entrance`, { waitUntil: "networkidle" });
    await settle(page, 1500);
    await shots.take(page, "e_01_entrance");
    await shots.take(page, "e_02_guest_list", tid(page, "entrance-guest-list"), 200);
    await openSession(page, app, lang);
    await shots.take(page, "e_03_session");
    await shots.take(page, "e_04_tickets", tid(page, "session-tickets"), 200);
    if (DEBUG) console.log("TICKETS", await tid(page, "session-tickets").innerText());
    await tid(page, "session-tickets-filter-notEntered").click();
    await settle(page, 1200);
    await shots.take(page, "e_05_not_entered", tid(page, "session-tickets"), 200);
    await shots.take(page, "e_06_auto", tid(page, "guest-list-subscription"), 300);
  } finally {
    await close();
  }
}

/** look: prints the text and the test ids of screens, to plan a film. Paths after the stage name; "E..." is a path under the event, "#id" clicks a test id. */
async function look(lang: Lang): Promise<void> {
  const { page, app, org, close } = await signIn(lang);
  try {
    for (const sub of process.argv.slice(4)) {
      if (sub.startsWith("#")) {
        await tid(page, sub.slice(1)).first().click();
        await settle(page, 1800);
      } else {
        await page.goto(sub.startsWith("E") ? `${app}${readState(lang).event}${sub.slice(1)}` : `${app}${org}${sub}`, { waitUntil: "networkidle" });
        await settle(page, 1800);
      }
      console.log(`\n===== ${sub} =====`);
      console.log("TESTIDS", await page.evaluate(() => Array.from(document.querySelectorAll("main [data-testid]")).map((e) => e.getAttribute("data-testid")).filter((x) => !/^date-day/.test(x || "")).join(" ")));
      console.log("LINKS", (await page.evaluate(() => Array.from(document.querySelectorAll("main a[href]")).map((a) => a.getAttribute("href")).join(" "))).replace(/[0-9a-f]{8}-[0-9a-f-]{27}/g, "<id>"));
      console.log((await page.evaluate(() => document.querySelector("main")?.innerText ?? "")).replace(/[\w.+-]+@(?!example\.)[\w.-]+/g, "<mail>"));
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

const FILMS: Record<string, string> = {
  event: "web-create-event", card: "web-create-event", reset: "web-create-event",
  org: "web-organization-details", links: "web-sales-link-and-qr", pay: "web-payments-status",
  promo: "web-promo-codes", order: "web-orders-and-refund", refund: "web-orders-and-refund",
  invite: "web-invitations", team: "web-invite-manager", price: "web-change-price",
  move: "web-move-or-cancel-session", cancel: "web-move-or-cancel-session",
  ticket: "buyer-where-is-my-ticket", entrance: "web-entrance-and-guest-list",
  paidrefund: "web-refund-order", refunded: "web-refund-order",
  brand: "web-brands", stats: "web-sales-statistics", messages: "web-buyer-messages-and-notifications",
};
const STAGES = [...Object.keys(FILMS), "look"];

async function main(): Promise<void> {
  loadEnv();
  const lang = process.argv[2] as Lang;
  const stage = process.argv[3];
  if (!["ru", "en", "es"].includes(lang) || !STAGES.includes(stage)) {
    throw new Error(`usage: tsx scripts/capture-web.ts <ru|en|es> <${STAGES.join("|")}>`);
  }
  const film = FILMS[stage] ?? "look";
  const acc = account(lang).toUpperCase();
  const shots = new Shots(film, lang, [
    [env(`VIDEO_ONB_EMAIL_${acc}`), SHOWN.email],
    [env(`VIDEO_ONB_ORG_${acc}`), SHOWN.org],
    [env(`VIDEO_ONB_ORG_${acc}`).toLowerCase().replace(/[^a-z0-9]+/g, "-"), "aurora-events"],
  ]);
  console.log(`${film} ${lang} ${stage}`);
  if (stage === "look") return look(lang);
  if (stage === "event") await event(lang, shots);
  else if (stage === "card") await resumeCard(lang, shots);
  else if (stage === "reset") await reset(lang);
  else if (stage === "org") await orgData(lang, shots);
  else if (stage === "links") await links(lang, shots);
  else if (stage === "promo") await promo(lang, shots);
  else if (stage === "order") await order(lang, shots);
  else if (stage === "refund") await refund(lang, shots);
  else if (stage === "invite") await invite(lang, shots);
  else if (stage === "team") await team(lang, shots);
  else if (stage === "price") await price(lang, shots);
  else if (stage === "move") await move(lang, shots);
  else if (stage === "cancel") await cancel(lang, shots);
  else if (stage === "paidrefund") await paidRefund(lang, shots);
  else if (stage === "refunded") await refunded(lang, shots);
  else if (stage === "brand") await brand(lang, shots);
  else if (stage === "ticket") await ticket(lang, shots);
  else if (stage === "entrance") await entrance(lang, shots);
  else if (stage === "stats") await stats(lang, shots);
  else if (stage === "messages") await messages(lang, shots);
  else await pay(lang, shots);
}

main().catch((e) => {
  console.error(e instanceof Error ? e.message : e);
  process.exit(1);
});
