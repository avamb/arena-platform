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
  /** The harness's Stripe stub (task 8-1): GET /__calls, POST /__reset. */
  stripe_stub_url: string;
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
