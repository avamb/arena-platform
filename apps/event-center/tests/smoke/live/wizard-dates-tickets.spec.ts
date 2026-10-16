/**
 * Twin test "event wizard: dates and tickets" (task 6-1b), the web version of
 * the bot's date and ticket questions (wizard_dates.go, wizard_sale_times.go,
 * wizard_date_tickets.go), against a REAL arena-api on the three roles of one
 * organization (owner = org_admin, manager = organizer, agent).
 *
 * What it proves:
 *  - two dates with different categories (one category with a sales end and
 *    doors; several at once) pass the platform's own check (`?dry_run=true`),
 *    and the check asks again only about the date that changed;
 *  - a chain of categories one after another (a step with a last day, a step
 *    with a number of tickets, the last step) passes the check;
 *  - price changes by day pass, and a change that does not start after the one
 *    before is refused at once;
 *  - the shape of a price, a sales end and a doors time is refused at once,
 *    in words, under the field; an error the SERVER finds (a day without a
 *    start time) lands under that field through `errors[].field`;
 *  - NOTHING is written: no event, venue or promoter appears;
 *  - the draft with its dates survives a reload;
 *  - the manager has the same screen; the agent has none.
 *
 * The bundle itself is proven against the bot's by the golden files
 * (bundle.golden.test.ts, wizard_golden_test.go).
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";
import { fillEventAndVenue, fillFirstDate, openWizard, pickDay, trackChecks } from "./wizardHelpers";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const nonce = Date.now().toString(36);

async function counts(request: APIRequestContext): Promise<number[]> {
  const get = async (path: string, key: string) => {
    const res = await request.get(`${base}/${path}`, { headers: auth("org_admin") });
    expect(res.status(), `GET ${path}`).toBe(200);
    return ((await res.json()) as Record<string, unknown[]>)[key].length;
  };
  return [await get("events", "events"), await get("venues", "venues"), await get("promoters?include_archived=true", "promoters")];
}

const status = (page: Page) => page.getByTestId("wizard-status");
const correct = (page: Page) => expect(status(page)).toContainText("Everything you filled in is correct", { timeout: 25_000 });

test.describe("event wizard: dates and tickets", () => {
  test("two dates with different categories pass the check, which asks again only about the date that changed", async ({ page, request }) => {
    const before = await counts(request);
    const stamps = trackChecks(page);
    await openWizard(page, "org_admin");
    await fillEventAndVenue(page, `Twin Dates Show ${nonce}`);
    await fillFirstDate(page);
    await page.getByTestId("wizard-date-0-sales-end").fill("19:30");
    await page.getByTestId("wizard-date-0-doors").fill("18:30");
    await correct(page);

    // A second date starts like the first: its time, and "the same tickets as the date before".
    await page.getByTestId("wizard-date-add").click();
    await expect(page.getByTestId("wizard-date-1")).toBeVisible();
    await expect(page.getByTestId("wizard-date-1-time")).toHaveValue("19:00");
    await expect(page.getByTestId("wizard-tickets-1-same")).toBeChecked();
    await expect(page.getByTestId("wizard-tickets-1-same-note")).toBeVisible();
    await pickDay(page, "wizard-date-1-day", "month");

    // ... and then sells its own categories, several at once.
    await page.getByTestId("wizard-tickets-1-own").check();
    await page.getByTestId("wizard-tickets-1-mode-parallel").check();
    await page.getByTestId("wizard-cat-1-0-name").fill("Children");
    await page.getByTestId("wizard-cat-1-0-price").fill("10");
    await page.getByTestId("wizard-cat-1-0-places").fill("40");
    await page.getByTestId("wizard-cat-1-1-name").fill("Adults");
    await page.getByTestId("wizard-cat-1-1-price").fill("25,5");
    await page.getByTestId("wizard-cat-1-1-places").fill("60");
    await correct(page);
    await noA11yViolations(page);

    // One price changes on the second date: one check, not two.
    await page.waitForTimeout(1200);
    const asked = stamps.length;
    await page.getByTestId("wizard-cat-1-1-price").fill("26");
    await expect(status(page)).toContainText("Checking", { timeout: 10_000 }).catch(() => undefined);
    await correct(page);
    expect(stamps.length - asked, "checks sent for one changed date").toBe(1);

    expect(await counts(request), "the check wrote nothing").toEqual(before);
  });

  test("a chain of categories one after another passes: a last day, a number of tickets, the last step", async ({ page }) => {
    await openWizard(page, "org_admin");
    await fillEventAndVenue(page, `Twin Chain Show ${nonce}`);
    await pickDay(page, "wizard-date-0-day", "month");
    await page.getByTestId("wizard-date-0-time").fill("20:00");
    await page.getByTestId("wizard-tickets-0-mode-sequence").check();
    await page.getByTestId("wizard-cat-0-0-name").fill("Early bird");
    await page.getByTestId("wizard-cat-0-0-price").fill("15");
    await pickDay(page, "wizard-cat-0-0-until", "week");
    await page.getByTestId("wizard-cat-0-1-name").fill("Regular");
    await page.getByTestId("wizard-cat-0-1-price").fill("20");
    // The last step has no last day and no number.
    await expect(page.getByTestId("wizard-cat-0-1-until")).toHaveCount(0);
    await correct(page);

    // A third step: the second one now ends by a number of tickets alone.
    await page.getByTestId("wizard-cat-0-add").click();
    await page.getByTestId("wizard-cat-0-2-name").fill("At the door");
    await page.getByTestId("wizard-cat-0-2-price").fill("30");
    await page.getByTestId("wizard-cat-0-1-limit").fill("50");
    await correct(page);

    // A step that ends by neither a day nor a number is refused at once.
    await page.getByTestId("wizard-cat-0-1-limit").fill("");
    await expect(page.getByTestId("wizard-cat-0-1-until-error")).toContainText("Choose the last day of this step, or type a number of tickets.");
    await noA11yViolations(page);
  });

  test("price changes by day pass, and one that does not start after the one before is refused", async ({ page }) => {
    await openWizard(page, "org_admin");
    await fillEventAndVenue(page, `Twin Steps Show ${nonce}`);
    await fillFirstDate(page);
    await page.getByTestId("wizard-step-0-add").click();
    await pickDay(page, "wizard-step-0-0-from", "tomorrow");
    await page.getByTestId("wizard-step-0-0-price").fill("12");
    await correct(page);

    await page.getByTestId("wizard-step-0-add").click();
    await pickDay(page, "wizard-step-0-1-from", "tomorrow");
    await page.getByTestId("wizard-step-0-1-price").fill("14");
    await expect(page.getByTestId("wizard-step-0-1-from-error")).toHaveText("Each price change must start after the one before.");
    await page.getByTestId("wizard-step-0-1-remove").click();
    await correct(page);
  });

  test("the shape of a price, a sales end and a doors time is refused at once, in words", async ({ page }) => {
    await openWizard(page, "org_admin");
    await fillEventAndVenue(page, `Twin Shapes Show ${nonce}`);
    await fillFirstDate(page);
    await page.getByTestId("wizard-cat-0-0-price").fill("ten");
    await expect(page.getByTestId("wizard-cat-0-0-price-error")).toContainText("Type the price as a number");
    await page.getByTestId("wizard-cat-0-0-price").fill("0");
    await expect(page.getByTestId("wizard-cat-0-0-price-error")).toHaveCount(0);
    await page.getByTestId("wizard-date-0-sales-end").fill("soon");
    await expect(page.getByTestId("wizard-date-0-sales-end-error")).toHaveText("Type a time, for example 19:30.");
    await page.getByTestId("wizard-date-0-sales-end").fill("");
    await page.getByTestId("wizard-date-0-doors").fill("19:30");
    await expect(page.getByTestId("wizard-date-0-doors-error")).toContainText("Type a time before the start");
    await page.getByTestId("wizard-date-0-doors").fill("18:30");
    // A free ticket (price 0) is a complete answer.
    await correct(page);
    await noA11yViolations(page);
  });

  test("an error the platform finds lands with its date: a price change on a free ticket", async ({ page }) => {
    await openWizard(page, "org_admin");
    await fillEventAndVenue(page, `Twin Server Error ${nonce}`);
    await fillFirstDate(page);
    await page.getByTestId("wizard-cat-0-0-price").fill("0");
    await page.getByTestId("wizard-step-0-add").click();
    await pickDay(page, "wizard-step-0-0-from", "tomorrow");
    await page.getByTestId("wizard-step-0-0-price").fill("5");
    // The wizard cannot know this rule: the platform's check says it, with the pointer of the field.
    await expect(page.getByTestId("wizard-tickets-0-error")).toContainText("A free ticket cannot have price changes", { timeout: 25_000 });
    await expect(status(page)).toContainText("to fix");
    await page.getByTestId("wizard-cat-0-0-price").fill("8");
    await correct(page);
  });

  test("a start time is asked for: the platform would read none as midnight", async ({ page }) => {
    await openWizard(page, "org_admin");
    await pickDay(page, "wizard-date-0-day", "week");
    await page.getByTestId("wizard-date-0-time").focus();
    await page.getByTestId("wizard-date-0-time").blur();
    await expect(page.getByTestId("wizard-date-0-time-error")).toHaveText("Type the start time, for example 19:00.");
  });

  test("the draft with its dates survives a reload", async ({ page }) => {
    await openWizard(page, "org_admin");
    await fillEventAndVenue(page, `Twin Kept Show ${nonce}`);
    await fillFirstDate(page);
    await page.getByTestId("wizard-date-add").click();
    await page.getByTestId("wizard-tickets-1-own").check();
    await page.getByTestId("wizard-tickets-1-mode-sequence").check();
    await page.getByTestId("wizard-cat-1-0-name").fill("First wave");
    await page.reload();
    await expect(page.getByTestId("wizard-date-1")).toBeVisible();
    await expect(page.getByTestId("wizard-date-0-time")).toHaveValue("19:00");
    await expect(page.getByTestId("wizard-tickets-1-mode-sequence")).toBeChecked();
    await expect(page.getByTestId("wizard-cat-1-0-name")).toHaveValue("First wave");
    await page.getByTestId("wizard-date-1-remove").click();
    await expect(page.getByTestId("wizard-date-1")).toHaveCount(0);
    await page.getByTestId("wizard-clear").click();
    await expect(page.getByTestId("wizard-date-0-time")).toHaveValue("");
  });

  test("the manager has the same screen and a free ticket passes", async ({ page }) => {
    await openWizard(page, "organizer");
    await fillEventAndVenue(page, `Twin Manager Show ${nonce}`);
    await pickDay(page, "wizard-date-0-day", "week");
    await page.getByTestId("wizard-date-0-time").fill("1800");
    await page.getByTestId("wizard-cat-0-0-price").fill("0");
    await correct(page);
  });

  test("the agent has no wizard and so no dates", async ({ page }) => {
    const stamps = trackChecks(page);
    await signInAs(page, person(env, "agent"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/events/new`);
    await expect(page.getByTestId("wizard-no-access")).toBeVisible();
    await expect(page.getByTestId("wizard-date-add")).toHaveCount(0);
    expect(stamps, "no check request for an agent").toEqual([]);
  });
});
