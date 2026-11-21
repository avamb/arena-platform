/**
 * What scripts/live-harness.mjs wrote: the live API, the fixture and the
 * three people of the organization (one per membership role).
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { openAllEventSections } from "../eventSections";

export type Role = "org_admin" | "organizer" | "agent";

export interface Person {
  role: Role;
  email: string;
  password: string;
  user_id: string;
  /** The Telegram account the person is linked to (arena-e2e-seed, tgedit_fixture.go). */
  telegram_user_id: number;
  /** An access token of the harness's own sign-in (about 15 minutes); `person()` renews it. */
  access_token: string;
  /** When `access_token` stops working (RFC 3339). */
  expires_at?: string;
}

export interface LiveEnv {
  api_base_url: string;
  org_id: string;
  org_name: string;
  event_id: string;
  event_name: string;
  session_id: string;
  /** A poster (media object) of ANOTHER organization, seeded by poster_fixture.go. */
  foreign_media_id: string;
  /** Test-only: the file that scripts what the scripted poster reader answers (see live-harness.mjs). */
  poster_script: string;
  /** Test-only: one JSON line per read the scripted reader served. */
  poster_calls: string;
  /** The harness's Stripe stub (task 8-1): GET /__calls, POST /__reset. */
  stripe_stub_url: string;
  /** The BOT_SERVICE_TOKEN of the harness API: the twin of the Telegram mini app plays the bot with it. */
  bot_service_token: string;
  /** TELEGRAM_MINIAPP_SECRET_KEY of the harness API (hex): the twin of /tg signs launch data with it. */
  telegram_miniapp_secret_key: string;
  people: Person[];
}

const envFile = join(dirname(fileURLToPath(import.meta.url)), "..", ".live-env.json");

export function liveEnv(): LiveEnv {
  return JSON.parse(readFileSync(envFile, "utf8")) as LiveEnv;
}

/** A token with less than this left is renewed before it is handed out. */
const RENEW_MARGIN_MS = 3 * 60_000;

/**
 * The person of a role, with an access token that still works. The harness
 * signs everybody in once, and a token lives about 15 minutes, while a full
 * run takes longer: a spec late in the run got 401 from the API. A token
 * close to its end is renewed with the same sign-in the harness makes, and
 * written back to `.live-env.json` so the next spec file reads the new one.
 */
export function person(env: LiveEnv, role: Role): Person {
  const p = env.people.find((x) => x.role === role);
  if (p === undefined) throw new Error(`the live fixture has no ${role}`);
  renewToken(env, p);
  return p;
}

function renewToken(env: LiveEnv, p: Person): void {
  if (p.expires_at === undefined) return;
  const left = Date.parse(p.expires_at) - Date.now();
  if (Number.isFinite(left) && left > RENEW_MARGIN_MS) return;
  const script =
    'fetch(process.env.LOGIN_URL,{method:"POST",headers:{"Content-Type":"application/json","X-Client-Channel":"event_center_web"},' +
    "body:JSON.stringify({email:process.env.LOGIN_EMAIL,password:process.env.LOGIN_PASSWORD})})" +
    ".then(async(r)=>{if(!r.ok){console.error(r.status,await r.text());process.exit(1)}" +
    "const b=await r.json();process.stdout.write(JSON.stringify({access_token:b.access_token,expires_at:b.expires_at}))})" +
    ".catch((e)=>{console.error(String(e));process.exit(1)})";
  const r = spawnSync(process.execPath, ["-e", script], {
    env: { ...process.env, LOGIN_URL: `${env.api_base_url}/v1/auth/login`, LOGIN_EMAIL: p.email, LOGIN_PASSWORD: p.password },
    encoding: "utf8",
    timeout: 30_000,
  });
  if (r.status !== 0) throw new Error(`renewing the token of ${p.role} failed: ${r.stderr}`);
  Object.assign(p, JSON.parse(r.stdout) as { access_token: string; expires_at: string });
  writeFileSync(envFile, JSON.stringify(env, null, 2));
}

/**
 * The text area of a RichTextField (the editor of spec 44, RT-11). The field's own test id holds the whole
 * editor (toolbar, text, counter); the text itself is the one multi-line textbox in it.
 */
export function richText(page: Page, testId: string) {
  return page.getByTestId(testId).locator('[role="textbox"][aria-multiline="true"]');
}

/** Signs in through the real login form, exactly as the organizer does. */
export async function signInAs(page: Page, p: Person, opts: { sections?: "open" | "default" } = {}) {
  // The event card keeps its five sections closed but one. Specs of the blocks inside them find all open;
  // a spec of the sections themselves passes `sections: "default"`.
  if (opts.sections !== "default") await openAllEventSections(page);
  await page.goto("/login");
  await page.getByLabel("E-mail").fill(p.email);
  await page.getByLabel("Password").fill(p.password);
  await page.getByTestId("login-submit").click();
}

/** WCAG 2.1 A and AA rules of axe on the screen as it is now. */
export async function noA11yViolations(page: Page) {
  const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  const found = result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`);
  expect(found, "axe accessibility violations").toEqual([]);
}
