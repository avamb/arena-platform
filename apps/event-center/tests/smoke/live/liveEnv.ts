/**
 * What scripts/live-harness.mjs wrote: the live API, the fixture and the
 * three people of the organization (one per membership role).
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export type Role = "org_admin" | "organizer" | "agent";

export interface Person {
  role: Role;
  email: string;
  password: string;
  user_id: string;
  /** The Telegram account the person is linked to (arena-e2e-seed, tgedit_fixture.go). */
  telegram_user_id: number;
  /** An access token of the harness's own sign-in (about 15 minutes). */
  access_token: string;
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

export function liveEnv(): LiveEnv {
  const file = join(dirname(fileURLToPath(import.meta.url)), "..", ".live-env.json");
  return JSON.parse(readFileSync(file, "utf8")) as LiveEnv;
}

export function person(env: LiveEnv, role: Role): Person {
  const p = env.people.find((x) => x.role === role);
  if (p === undefined) throw new Error(`the live fixture has no ${role}`);
  return p;
}

/**
 * The text area of a RichTextField (the editor of spec 44, RT-11). The field's own test id holds the whole
 * editor (toolbar, text, counter); the text itself is the one multi-line textbox in it.
 */
export function richText(page: Page, testId: string) {
  return page.getByTestId(testId).locator('[role="textbox"][aria-multiline="true"]');
}

/** Signs in through the real login form, exactly as the organizer does. */
export async function signInAs(page: Page, p: Person) {
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
