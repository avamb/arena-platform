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
  await page.getByTestId("events-create").click();
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
  await pickWizardVenue(page, "E2E Hall");
  // The hall has no country, so no currency comes from it: type one.
  await page.getByTestId("wizard-currency").fill("EUR");
}

/** The first date, complete: a day a week ahead, 19:00, one category "Admission" at 18,90. */
export async function fillFirstDate(page: Page) {
  await pickDay(page, "wizard-date-0-day", "week");
  await page.getByTestId("wizard-date-0-time").fill("19:00");
  await page.getByTestId("wizard-cat-0-0-price").fill("18,90");
}

/** The chooser may open on the organization's country: go back to its first step, so a search covers every venue. */
async function toChooserStart(page: Page) {
  const back = page.getByTestId("venue-chooser-back");
  await expect(page.getByTestId("venue-chooser-search")).toBeVisible();
  while (await back.isVisible()) await back.click();
}

/** The rows of the venue chooser whose name contains `name`, found with the search field. */
export async function searchVenueRows(page: Page, name: string) {
  await toChooserStart(page);
  await page.getByTestId("venue-chooser-search").fill(name);
  return page.locator('[data-testid^="venue-chooser-venue-"]', { hasText: name });
}

/** Chooses an existing venue in the wizard through the chooser: mode "existing", search by name, click the row. */
export async function pickWizardVenue(page: Page, name: string) {
  await page.getByTestId("wizard-venue-mode-existing").check();
  const rows = await searchVenueRows(page, name);
  await rows.first().click();
  await expect(page.getByTestId("wizard-venue-chosen")).toContainText(name);
}

/**
 * Chooses the country of a NEW venue through the quick row. A country the quick row does not list
 * (a source with regions lists only countries with venues) is found with the search field.
 */
export async function pickWizardCountry(page: Page, iso2: string) {
  const change = page.getByTestId("wizard-venue-country-change");
  if ((await change.count()) > 0) await change.click();
  const chooser = page.getByTestId("wizard-venue-country-chooser");
  const quick = chooser.getByTestId(`venue-chooser-quick-${iso2}`);
  await expect(chooser.getByTestId("venue-chooser-search")).toBeVisible();
  await expect(quick.or(chooser.getByTestId("venue-chooser-regions")).or(chooser.getByTestId("venue-chooser-quick")).first()).toBeVisible();
  if ((await quick.count()) > 0) {
    await quick.click();
  } else {
    await chooser.getByTestId("venue-chooser-search").fill(iso2);
    await chooser.getByTestId(`venue-chooser-country-${iso2}`).click();
  }
  await expect(page.getByTestId("wizard-venue-country")).toBeVisible();
}

/** Takes the country of a new venue back: the chooser opens again. */
export async function clearWizardCountry(page: Page) {
  await page.getByTestId("wizard-venue-country-change").click();
  await expect(page.getByTestId("wizard-venue-country-chooser")).toBeVisible();
}
