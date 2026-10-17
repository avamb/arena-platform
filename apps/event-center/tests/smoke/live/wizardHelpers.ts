/**
 * Helpers shared by the live twin tests of the event wizard (tasks 6-1a and
 * 6-1b): opening the wizard, tracking the check requests, picking a day from
 * the calendar of a date, and filling one complete date.
 */
import { expect, type Page } from "@playwright/test";
import { liveEnv, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();

/** Records the start time of every check request (`?dry_run=true`) of the page. */
export function trackChecks(page: Page): number[] {
  const stamps: number[] = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().includes("/imports/event-bundle") && r.url().includes("dry_run=true")) stamps.push(Date.now());
  });
  return stamps;
}

export async function openWizard(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.getByTestId("nav-new-event").click();
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/new`));
  await expect(page.getByRole("heading", { level: 1, name: "New event" })).toBeVisible();
}

/**
 * Picks a day of a day field (`wizard-date-0-day`, `wizard-cat-0-0-until` ...)
 * with one of the calendar's quick buttons: the calendar opens, the button is
 * pressed and the calendar closes by itself.
 */
export async function pickDay(page: Page, testId: string, quick: "today" | "tomorrow" | "week" | "month") {
  const box = page.getByTestId(testId);
  await box.getByTestId(`${testId}-toggle`).click();
  await box.getByTestId(`date-quick-${quick}`).click();
  await expect(box.getByTestId(`${testId}-value`)).not.toHaveAttribute("data-value", "");
}

/**
 * The first step of the wizard is the poster: a draft cannot be created before the person
 * uploads one or CONSCIOUSLY continues without. Specs that are not about posters make that choice
 * here, through the real button, and see the reminder (it is not skipped by a test-only flag).
 */
export async function skipPoster(page: Page) {
  await page.getByTestId("wizard-poster-skip").click();
  await expect(page.getByTestId("wizard-poster-skipped")).toContainText("No poster added");
}

/** Event name, the venue "E2E Hall" and EUR: what every complete draft needs besides dates. */
export async function fillEventAndVenue(page: Page, name: string, opts: { poster?: "skip" | "leave" } = {}) {
  if ((opts.poster ?? "skip") === "skip" && (await page.getByTestId("wizard-poster-skip").count()) > 0) await skipPoster(page);
  await page.getByTestId("wizard-name").fill(name);
  await page.getByTestId("wizard-venue-mode-existing").check();
  await page.getByTestId("wizard-venue-pick").selectOption({ label: "E2E Hall" });
  // The hall has no country, so no currency comes from it: type one.
  await page.getByTestId("wizard-currency").fill("EUR");
}

/** The first date, complete: a day a week ahead, 19:00, one category "Admission" at 18,90. */
export async function fillFirstDate(page: Page) {
  await pickDay(page, "wizard-date-0-day", "week");
  await page.getByTestId("wizard-date-0-time").fill("19:00");
  await page.getByTestId("wizard-cat-0-0-price").fill("18,90");
}
