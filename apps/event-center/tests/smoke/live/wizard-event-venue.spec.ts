/**
 * Twin test "event wizard: sections Event and Venue" (task 6-1a), the web
 * version of the first steps of the bot's wizard, against a REAL arena-api on
 * the three roles of one organization (owner = org_admin, manager =
 * organizer, agent).
 *
 * What it proves:
 *  - owner and manager see "+ Event" and fill both sections; the platform's
 *    own check (`?dry_run=true`) answers "correct" for a good draft;
 *  - an empty name is reported by the check, under the NAME field, with the
 *    server's code mapped to words (errors[].field -> the form field);
 *  - a venue of the organization, and a NEW venue (country, city with
 *    suggestions, time zone, seats) both pass; the currency follows the
 *    country;
 *  - the check is rate limited: typing twenty letters sends few requests and
 *    no two closer than a second;
 *  - NOTHING is written: the organization has no new event, no new venue and
 *    no new promoter afterwards (the check reads only);
 *  - the draft survives a reload;
 *  - the agent has no menu entry, and the address shows "no access" without a
 *    single check request; the API itself answers 403 to the agent.
 *
 * The harness builds the app with VITE_FEATURE_WIZARD=1 (playwright.live.config.ts).
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const nonce = Date.now().toString(36);
const EVENT_NAME = `Twin Wizard Show ${nonce}`;
const NEW_HALL = `Twin New Hall ${nonce}`;

interface Counts {
  events: number;
  venues: number;
  promoters: number;
}

async function counts(request: APIRequestContext): Promise<Counts> {
  const get = async (path: string, key: string) => {
    const res = await request.get(`${base}/${path}`, { headers: auth("org_admin") });
    expect(res.status(), `GET ${path}`).toBe(200);
    return ((await res.json()) as Record<string, unknown[]>)[key].length;
  };
  return {
    events: await get("events", "events"),
    venues: await get("venues", "venues"),
    promoters: await get("promoters?include_archived=true", "promoters"),
  };
}

/** Records the start time of every check request of the page. */
function trackChecks(page: Page): number[] {
  const stamps: number[] = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().includes("/imports/event-bundle") && r.url().includes("dry_run=true")) stamps.push(Date.now());
  });
  return stamps;
}

async function openWizard(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.getByTestId("nav-new-event").click();
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/new`));
  await expect(page.getByRole("heading", { level: 1, name: "New event" })).toBeVisible();
}

const status = (page: Page) => page.getByTestId("wizard-status");

test.describe("event wizard: Event and Venue", () => {
  test("the owner fills an existing venue and the check says everything is correct; nothing is written", async ({ page, request }) => {
    const before = await counts(request);
    await openWizard(page, "org_admin");
    await expect(status(page)).toContainText("Fill in the sections");

    await page.getByTestId("wizard-name").fill(EVENT_NAME);
    await page.getByTestId("wizard-description").fill("A twin test event.");
    await page.getByTestId("wizard-age").selectOption("6+");
    await page.getByTestId("wizard-promoter").selectOption({ label: "E2E Wizard Promoter" });
    await page.getByTestId("wizard-venue-mode-existing").check();
    await page.getByTestId("wizard-venue-pick").selectOption({ label: "E2E Hall" });
    await expect(page.getByTestId("wizard-venue-facts")).toContainText("Europe/Madrid");
    await expect(page.getByTestId("wizard-venue-facts")).toContainText("300");
    // The hall has no country, so no currency comes from it: type one.
    await page.getByTestId("wizard-currency").fill("EUR");

    await expect(status(page)).toContainText("Everything you filled in is correct", { timeout: 20_000 });
    await noA11yViolations(page);

    expect(await counts(request), "the check wrote nothing").toEqual(before);
  });

  test("an empty name is reported by the check under the name field", async ({ page }) => {
    await openWizard(page, "org_admin");
    await page.getByTestId("wizard-venue-mode-existing").check();
    await page.getByTestId("wizard-venue-pick").selectOption({ label: "E2E Hall" });
    await page.getByTestId("wizard-currency").fill("EUR");
    await page.getByTestId("wizard-name").fill("A name");
    await expect(status(page)).toContainText("Everything you filled in is correct", { timeout: 20_000 });

    await page.getByTestId("wizard-name").fill("");
    // The code import.action_name_required, in words, under the name.
    await expect(page.getByTestId("wizard-name-error")).toHaveText("Type the name of the event.", { timeout: 20_000 });
    await expect(status(page)).toContainText("1 to fix");
    // The stub date and category are not the person's errors.
    await expect(page.getByTestId("wizard-general")).toHaveCount(0);
    await noA11yViolations(page);

    // A bad currency is the server's to judge too: EURO is refused by the wizard at once,
    // a three-letter code the server does not know is not (no list of codes is kept here).
    await page.getByTestId("wizard-name").fill("A name again");
    await page.getByTestId("wizard-currency").fill("EURO");
    await expect(page.getByTestId("wizard-currency-error")).toContainText("three letters");
  });

  test("a new venue: country, city suggestions, time zone, seats; the currency follows the country", async ({ page }) => {
    await openWizard(page, "org_admin");
    await page.getByTestId("wizard-name").fill(`${EVENT_NAME} new venue`);
    await page.getByTestId("wizard-venue-mode-new").check();
    await page.getByTestId("wizard-venue-name").fill(NEW_HALL);
    await page.getByTestId("wizard-venue-country").selectOption("ES");
    // Spain has several zones: the person chooses; the currency came from the country.
    await expect(page.getByTestId("wizard-currency")).toHaveValue("EUR", { timeout: 15_000 });
    await page.getByTestId("wizard-venue-city").fill("barc");
    const offers = page.getByTestId("wizard-city-offers");
    await expect(offers).toBeVisible({ timeout: 15_000 });
    await offers.getByRole("button", { name: "Barcelona" }).first().click();
    await expect(page.getByTestId("wizard-venue-city")).toHaveValue("Barcelona");
    await expect(page.getByTestId("wizard-venue-timezone")).toHaveValue("Europe/Madrid");
    await page.getByTestId("wizard-venue-capacity").fill("1 200");

    await expect(status(page)).toContainText("Everything you filled in is correct", { timeout: 20_000 });

    // A country with ONE zone fills the zone in without a question.
    await page.getByTestId("wizard-venue-country").selectOption("CZ");
    await expect(page.getByTestId("wizard-tz-single")).toContainText("Europe/Prague", { timeout: 15_000 });
    await expect(page.getByTestId("wizard-currency")).toHaveValue("CZK", { timeout: 15_000 });
    await expect(status(page)).toContainText("Everything you filled in is correct", { timeout: 20_000 });

    // Seats are a whole number.
    await page.getByTestId("wizard-venue-capacity").fill("lots");
    await expect(page.getByTestId("wizard-venue-capacity-error")).toContainText("whole number");
    await noA11yViolations(page);
  });

  test("a new venue without a time zone is reported by the check under the zone", async ({ page }) => {
    await openWizard(page, "org_admin");
    await page.getByTestId("wizard-name").fill("Zone test");
    await page.getByTestId("wizard-venue-mode-new").check();
    await page.getByTestId("wizard-venue-name").fill(`Zoneless ${nonce}`);
    await page.getByTestId("wizard-venue-country").selectOption("ES");
    await page.getByTestId("wizard-venue-capacity").fill("50");
    await page.getByTestId("wizard-venue-timezone").focus();
    await page.getByTestId("wizard-venue-timezone").blur();
    await expect(page.getByTestId("wizard-venue-timezone-error")).toHaveText("Choose the time zone of the venue.", { timeout: 20_000 });
  });

  test("the check is rate limited while a person types", async ({ page }) => {
    await openWizard(page, "org_admin");
    await page.getByTestId("wizard-venue-mode-existing").check();
    await page.getByTestId("wizard-venue-pick").selectOption({ label: "E2E Hall" });
    await page.getByTestId("wizard-currency").fill("EUR");
    await page.waitForTimeout(1500);

    const stamps = trackChecks(page);
    await page.getByTestId("wizard-name").pressSequentially("Typing at a steady pace now", { delay: 80 });
    await expect(status(page)).toContainText("Everything you filled in is correct", { timeout: 20_000 });
    // 27 keystrokes in about two seconds: one check after the pause, not 27.
    expect(stamps.length, `checks sent: ${stamps.length}`).toBeLessThanOrEqual(2);
    expect(stamps.length).toBeGreaterThanOrEqual(1);
    for (let i = 1; i < stamps.length; i += 1) expect(stamps[i] - stamps[i - 1], "gap between two checks").toBeGreaterThanOrEqual(900);
  });

  test("the draft survives a reload and can be cleared", async ({ page }) => {
    await openWizard(page, "org_admin");
    await page.getByTestId("wizard-name").fill(`Kept ${nonce}`);
    await page.getByTestId("wizard-age").selectOption("12+");
    await page.reload();
    await expect(page.getByTestId("wizard-name")).toHaveValue(`Kept ${nonce}`);
    await expect(page.getByTestId("wizard-age")).toHaveValue("12+");
    await page.getByTestId("wizard-clear").click();
    await expect(page.getByTestId("wizard-name")).toHaveValue("");
    await page.reload();
    await expect(page.getByTestId("wizard-name")).toHaveValue("");
  });

  test("the manager has the wizard too", async ({ page }) => {
    const stamps = trackChecks(page);
    await openWizard(page, "organizer");
    await page.getByTestId("wizard-name").fill(`Manager ${nonce}`);
    await page.getByTestId("wizard-venue-mode-existing").check();
    await page.getByTestId("wizard-venue-pick").selectOption({ label: "E2E Hall" });
    await page.getByTestId("wizard-currency").fill("EUR");
    await expect(status(page)).toContainText("Everything you filled in is correct", { timeout: 20_000 });
    expect(stamps.length).toBeGreaterThanOrEqual(1);
  });

  test("the agent has no menu entry, the address says no access, and the API refuses the check", async ({ page, request }) => {
    const stamps = trackChecks(page);
    await signInAs(page, person(env, "agent"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await expect(page.getByTestId("nav-new-event")).toHaveCount(0);
    await page.goto(`/o/${env.org_id}/events/new`);
    await expect(page.getByTestId("wizard-no-access")).toBeVisible();
    await expect(page.getByTestId("wizard-name")).toHaveCount(0);
    expect(stamps, "no check request for an agent").toEqual([]);
    await noA11yViolations(page);

    const res = await request.post(`${base}/imports/event-bundle?dry_run=true`, { headers: auth("agent"), data: { source: "arena" } });
    expect(res.status(), "the API refuses the agent").toBe(403);
  });
});
