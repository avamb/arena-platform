/**
 * Capture of the film apply-on-website: a new organizer applies on the website and signs in to the web event center.
 *
 *   npx tsx scripts/capture-apply-on-website.ts <ru|en|es> <form|approve|login|signin>
 *
 * form     the application form of the website (a LOCAL build of the site that calls the staging API),
 *          the confirmation link, all steps, the three consents, "application sent"
 * approve  the test operator of the stand approves THIS application (found by its e-mail)
 * login    the "choose a password" page (the link works once)
 * signin   the first sign-in to the web event center and what the new organizer sees
 *
 * The steps change data on the stand (an application, then an organization and an account), thus this
 * is a script of its own and not `capture` steps of the scenario. Everything that identifies the stand
 * or a real mailbox is replaced in the page before each screenshot (see `mask`).
 *
 * Environment (ops/video/.env, never committed):
 *   VIDEO_SITE_URL            local build of the website, for example http://127.0.0.1:4391
 *   VIDEO_API_URL             API of the stand
 *   VIDEO_BASE_URL            web event center of the stand
 *   VIDEO_ONB_EMAIL_<LANG>    real address of the test applicant (unique for each language)
 *   VIDEO_ONB_ORG_<LANG>      organization name on the stand (unique for each language)
 *   VIDEO_ONB_PASSWORD        password that the test applicant chooses
 *   VIDEO_OPERATOR_EMAIL, VIDEO_OPERATOR_PASSWORD   test operator of the stand
 *   VIDEO_STAND_PSQL          command that reads SQL from stdin and prints rows (psql -At on the stand)
 */
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { chromium, type BrowserContext, type Page } from "playwright";
import { assertHostAllowed } from "../src/capture.ts";
import { loadEnv, ROOT } from "../src/env.ts";

type Lang = "ru" | "en" | "es";

const SHOWN = { email: "carmen@example.com", org: "Aurora Events", site: "arenasoldout.com", app: "app.arenasoldout.com" };
const PERSON = { first: "Carmen", last: "Ortega", phone: "+34 600 555 010" };
const BROWSER_LOCALE: Record<Lang, string> = { ru: "ru-RU", en: "en-GB", es: "es-ES" };
/** The block of the site page that holds the form (heading, progress, fields, buttons). */
const SECTION = "section:has(#onb), main:has(#onb)";
const SITE_PREFIX: Record<Lang, string> = { ru: "/ru", en: "", es: "/es" };

function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`capture-apply-on-website: environment variable ${name} is not set`);
  return v;
}

function sql(query: string): string {
  const [cmd, ...args] = env("VIDEO_STAND_PSQL").split(/\s+/);
  return execFileSync(cmd, args, { input: query, encoding: "utf8" }).trim();
}

const lit = (s: string) => `'${s.replace(/'/g, "''")}'`;

async function poll(what: string, read: () => string, tries = 30): Promise<string> {
  for (let i = 0; i < tries; i += 1) {
    const v = read();
    if (v) return v;
    await new Promise((r) => setTimeout(r, 2000));
  }
  throw new Error(`capture-apply-on-website: ${what} did not appear on the stand`);
}

/** ONB_VIEW=desktop: a browser window of 1280 x 720 for the 16:9 film; default is the phone size. */
const DESKTOP = process.env.ONB_VIEW === "desktop";

class Shots {
  constructor(private readonly dir: string, private readonly replace: [string, string][]) {
    fs.mkdirSync(dir, { recursive: true });
  }

  /** Replaces stand data in texts and fields, hides the captcha box, then restores the fields. */
  async take(page: Page, name: string, opts: { full?: boolean; clip?: string; at?: string } = {}): Promise<void> {
    await page.evaluate((pairs) => {
      const swap = (s: string) => pairs.reduce((acc, [from, to]) => acc.split(from).join(to), s);
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        const t = n.nodeValue ?? "";
        const r = swap(t);
        if (r !== t) n.nodeValue = r;
      }
      document.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("input,textarea").forEach((i) => {
        if (i.type === "password" || i.type === "checkbox" || i.type === "hidden") return;
        const r = swap(i.value);
        if (r !== i.value) { i.dataset.videoReal = i.value; i.value = r; }
      });
      document.querySelectorAll<HTMLElement>(".cf-turnstile-box, astro-dev-toolbar").forEach((b) => { b.style.display = "none"; });
      (document.activeElement as HTMLElement | null)?.blur?.();
    }, this.replace);
    if (opts.full) {
      // a bar fixed to the screen would be drawn in the middle of a whole-page picture
      await page.evaluate(() => {
        document.querySelectorAll<HTMLElement>("body *").forEach((e) => {
          if (getComputedStyle(e).position === "fixed") { e.style.visibility = "hidden"; e.dataset.videoHidden = "1"; }
        });
      });
    }
    await page.waitForTimeout(250);
    if (DESKTOP) {
      // Desktop pictures (file names d_*) are what the browser window shows: no cut, no whole page.
      // A form block is moved under the fixed header of the site first; `at` moves another element there.
      const anchor = opts.at ?? (opts.clip ? ((await page.locator(".onb-crumb").count()) ? ".onb-crumb" : "main h1") : undefined);
      if (anchor) await page.locator(anchor).first().evaluate((e) => window.scrollTo(0, e.getBoundingClientRect().top + window.scrollY - 230));
      else await page.evaluate(() => window.scrollTo(0, 0));
      await page.waitForTimeout(250);
      await page.screenshot({ path: path.join(this.dir, `d_${name}.png`) });
      await this.restore(page);
      console.log(`  shot d_${name}`);
      return;
    }
    // clip: the block of the page that holds this element, whole, without the fixed header and the footer
    const clip = opts.clip
      ? await page.locator(opts.clip).last().evaluate((e) => {
          // the fixed header of the site would be drawn in the middle of a whole-page picture
          document.querySelectorAll<HTMLElement>("body > header, header.header").forEach((h) => { h.style.visibility = "hidden"; h.dataset.videoHidden = "1"; });
          const r = e.getBoundingClientRect();
          return { x: 0, y: Math.max(0, r.top + window.scrollY), width: document.documentElement.clientWidth, height: r.height };
        })
      : undefined;
    await page.screenshot({ path: path.join(this.dir, `${name}.png`), fullPage: !!clip || (opts.full ?? false), clip });
    await this.restore(page);
    console.log(`  shot ${name}`);
  }

  private async restore(page: Page): Promise<void> {
    await page.evaluate(() => {
      document.querySelectorAll<HTMLElement>("[data-video-hidden]").forEach((h) => { h.style.visibility = ""; delete h.dataset.videoHidden; });
      document.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("[data-video-real]").forEach((i) => {
        i.value = i.dataset.videoReal ?? i.value;
        delete i.dataset.videoReal;
      });
    });
  }
}

async function newContext(lang: Lang): Promise<{ close: () => Promise<void>; ctx: BrowserContext }> {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({
    viewport: DESKTOP ? { width: 1280, height: 720 } : { width: 390, height: 844 },
    deviceScaleFactor: 2,
    isMobile: !DESKTOP,
    hasTouch: !DESKTOP,
    locale: BROWSER_LOCALE[lang],
    timezoneId: "Europe/Madrid",
  });
  // tsx wraps named functions in __name(); the page does not have it.
  await ctx.addInitScript("window.__name = (f) => f");
  return { ctx, close: () => browser.close() };
}

/** The local site is not in the CORS list of the stand: the calls are done by Playwright and answered with open CORS headers. */
async function routeApi(ctx: BrowserContext, api: string): Promise<void> {
  await ctx.route(`${api}/**`, async (route) => {
    const req = route.request();
    const cors = { "access-control-allow-origin": "*", "access-control-allow-headers": "*", "access-control-allow-methods": "GET,POST,PUT,OPTIONS" };
    if (req.method() === "OPTIONS") return route.fulfill({ status: 204, headers: cors });
    const res = await route.fetch();
    return route.fulfill({ response: res, headers: { ...res.headers(), ...cors } });
  });
  await ctx.route(/pr-top\.com|googletagmanager|google-analytics/, (route) => route.abort());
}

async function form(lang: Lang, shots: Shots): Promise<void> {
  const site = env("VIDEO_SITE_URL").replace(/\/$/, "");
  const api = env("VIDEO_API_URL").replace(/\/$/, "");
  assertHostAllowed(site);
  assertHostAllowed(api);
  const email = env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`);
  const org = env(`VIDEO_ONB_ORG_${lang.toUpperCase()}`);
  const start = `${site}${SITE_PREFIX[lang]}/start`;

  const { ctx, close } = await newContext(lang);
  try {
    await routeApi(ctx, api);
    const page = await ctx.newPage();
    // ONB_RESUME=1: the draft of this address exists already, go on from the confirmation link
    if (process.env.ONB_RESUME !== "1") {
      await page.goto(start, { waitUntil: "networkidle" });
      await page.waitForSelector("#onb-first_name");
      await shots.take(page, "f_01_start");
      await page.fill("#onb-first_name", PERSON.first);
      await page.fill("#onb-last_name", PERSON.last);
      await page.fill("#onb-email", email);
      await page.fill("#onb-phone", PERSON.phone);
      await shots.take(page, "f_02_contact", { clip: SECTION });
      if (process.env.ONB_STOP === "contact") return;

      // The test key of the captcha provider answers by itself; wait until its token is in the page.
      await page.waitForFunction(() => !!document.querySelector<HTMLInputElement>("[name=cf-turnstile-response]")?.value, undefined, { timeout: 30000 });
      await page.click("form button[type=submit]");
      await page.waitForSelector("#onb-org_name", { timeout: 30000 });
      await shots.take(page, "f_03_email_sent", { clip: SECTION });

    }

    // The e-mail with the confirmation link: its one-time token is read from the mail job of the stand.
    const token = await poll("the confirmation link", () =>
      sql(`select payload->>'token' from worker_jobs where job_type='onboarding.email' and payload->>'kind'='confirm' and payload->>'email'=${lit(email)} order by created_at desc limit 1`));
    await page.goto(`${start}/confirm?token=${encodeURIComponent(token)}`, { waitUntil: "networkidle" });
    await page.waitForSelector(".onb-ok");
    await page.evaluate(() => window.scrollTo(0, 0));
    await shots.take(page, "f_04_confirmed");
    await page.click(".onb-body a.btn-primary");

    // Organization
    await page.waitForSelector("#onb-org_name");
    await page.fill("#onb-org_name", org);
    await page.fill("#onb-legal_name", `${org} S.L.`);
    await page.selectOption("#onb-country", "ES");
    await page.fill("#onb-tax_id", "ESB12345674");
    await page.selectOption("#onb-tax_id_scheme", "vat");
    await page.fill("#onb-address_line1", "Calle de la Luna 12");
    await page.fill("#onb-address_postal_code", "28004");
    await page.fill("#onb-address_city", "Madrid");
    await shots.take(page, "f_05_org", { clip: SECTION });
    if (DESKTOP) await shots.take(page, "f_05_org_b", { at: "#onb-tax_id" });
    await page.click("form button[type=submit]");

    // Events
    await page.waitForSelector("#onb-seating");
    await page.check("input[type=checkbox][value=concert]");
    await page.check("input[type=checkbox][value=theatre]");
    await page.selectOption("#onb-seating", "ga");
    await page.selectOption("#onb-events_per_year", "6-20");
    await page.selectOption("#onb-tickets_per_year", "500-5k");
    await shots.take(page, "f_06_events", { clip: SECTION });
    await page.click("form button[type=submit]");

    // Tickets and payment
    await page.waitForSelector("#onb-payment_provider");
    await page.selectOption("#onb-payment_provider", "stripe");
    await shots.take(page, "f_07_payment", { clip: SECTION });
    await page.click("form button[type=submit]");

    // Consents and sending
    await page.waitForSelector("#onb-accept_terms");
    await page.check("#onb-accept_terms");
    await page.check("#onb-accept_privacy");
    await page.check("#onb-confirm_authority");
    await shots.take(page, "f_08_consents", { clip: SECTION });
    await page.click("form button[type=submit]");
    await page.waitForURL(/\/start\/thanks/, { timeout: 30000 });
    await page.waitForLoadState("networkidle");
    await shots.take(page, "f_09_thanks", { clip: "main" });
  } finally {
    await close();
  }
}

async function approve(lang: Lang): Promise<void> {
  const api = env("VIDEO_API_URL").replace(/\/$/, "");
  assertHostAllowed(api);
  const email = env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`);
  const id = sql(`select id from onboarding_applications where applicant_email=${lit(email)} and status='pending_approval' order by created_at desc limit 1`);
  if (!id) throw new Error("capture-apply-on-website: no application of this address waits for approval");
  const login = await fetch(`${api}/v1/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email: env("VIDEO_OPERATOR_EMAIL"), password: env("VIDEO_OPERATOR_PASSWORD") }),
  });
  if (!login.ok) throw new Error(`capture-apply-on-website: operator sign-in answered ${login.status}`);
  const access = ((await login.json()) as { access_token?: string }).access_token;
  const res = await fetch(`${api}/v1/admin/onboarding/applications/${id}/approve`, {
    method: "POST",
    headers: { Authorization: `Bearer ${access}`, "Content-Type": "application/json", "X-Admin-Reason": "video apply-on-website test application" },
    body: "{}",
  });
  console.log(`  approve ${id}: ${res.status}`);
  if (!res.ok) throw new Error(await res.text());
}

async function login(lang: Lang, shots: Shots): Promise<void> {
  const app = env("VIDEO_BASE_URL").replace(/\/$/, "");
  assertHostAllowed(app);
  const email = env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`);
  const token = await poll("the password letter", () =>
    sql(`select payload->>'token' from worker_jobs where job_type='auth.password_reset_email' and payload->>'email'=${lit(email)} order by created_at desc limit 1`));
  const { ctx, close } = await newContext(lang);
  try {
    const page = await ctx.newPage();
    await page.goto(`${app}/accept-invite?token=${encodeURIComponent(token)}`, { waitUntil: "networkidle" });
    await shots.take(page, "p_01_password_empty");
    const fields = page.locator("input[type=password]");
    const n = await fields.count();
    for (let i = 0; i < n; i += 1) await fields.nth(i).fill(env("VIDEO_ONB_PASSWORD"));
    await shots.take(page, "p_02_password");
    await page.click("button[type=submit]");
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(1500);
    await shots.take(page, "p_03_after_password");
    await shots.take(page, "p_03_after_password_full", { full: true });
    console.log(`  now at ${new URL(page.url()).pathname}`);
  } finally {
    await close();
  }
}

/** The first sign-in: the sign-in page, then every screen the new organizer sees first. */
async function signin(lang: Lang, shots: Shots): Promise<void> {
  const app = env("VIDEO_BASE_URL").replace(/\/$/, "");
  assertHostAllowed(app);
  const email = env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`);
  const { ctx, close } = await newContext(lang);
  try {
    const page = await ctx.newPage();
    await page.goto(`${app}/login`, { waitUntil: "networkidle" });
    await page.fill("input[type=email]", email);
    await page.fill("input[type=password]", env("VIDEO_ONB_PASSWORD"));
    await shots.take(page, "e_01_signin");
    await page.click("button[type=submit]");
    await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 30000 });
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(1500);
    console.log(`  now at ${new URL(page.url()).pathname.replace(/[0-9a-f-]{36}/g, "<id>")}`);
    await shots.take(page, "e_02_first");
    await shots.take(page, "e_02_first_full", { full: true });
    const extra = (process.env.ONB_PAGES ?? "").split(",").filter(Boolean);
    const orgPath = /\/o\/[0-9a-f-]{36}/.exec(new URL(page.url()).pathname)?.[0] ?? "";
    for (const [i, sub] of extra.entries()) {
      await page.goto(`${app}${orgPath}${sub}`, { waitUntil: "networkidle" });
      await page.waitForTimeout(1200);
      await shots.take(page, `e_1${i}_${sub.replace(/[^a-z]+/g, "_").replace(/^_|_$/g, "") || "home"}_full`, { full: true });
    }
  } finally {
    await close();
  }
}

async function main(): Promise<void> {
  loadEnv();
  const lang = process.argv[2] as Lang;
  const stage = process.argv[3];
  if (!["ru", "en", "es"].includes(lang) || !["form", "approve", "login", "signin"].includes(stage)) {
    throw new Error("usage: tsx scripts/capture-apply-on-website.ts <ru|en|es> <form|approve|login>");
  }
  const dir = path.join(ROOT, "scenarios", "apply-on-website", "captures", lang === "ru" ? "" : lang);
  fs.mkdirSync(path.join(ROOT, ".cache"), { recursive: true });
  const shots = new Shots(dir, [
    [env(`VIDEO_ONB_EMAIL_${lang.toUpperCase()}`), SHOWN.email],
    [env(`VIDEO_ONB_ORG_${lang.toUpperCase()}`), SHOWN.org],
    [new URL(env("VIDEO_SITE_URL")).host, SHOWN.site],
    [new URL(env("VIDEO_BASE_URL")).host, SHOWN.app],
  ]);
  console.log(`apply-on-website ${lang} ${stage}`);
  if (stage === "form") await form(lang, shots);
  else if (stage === "approve") await approve(lang);
  else if (stage === "login") await login(lang, shots);
  else await signin(lang, shots);
}

main().catch((e) => {
  console.error(e instanceof Error ? e.message : e);
  process.exit(1);
});
