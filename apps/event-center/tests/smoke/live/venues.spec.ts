/**
 * Twin test "Venues" on the three roles of one organization, against a REAL
 * arena-api (spec 64 section 2.1.1, the stepwise venue chooser).
 *
 * The routes GET /v1/geo/world-countries and the venue directory are not in
 * the API yet, so the chooser answers from its fallback (the existing venue,
 * country and city reads): the quick row lists every reference country, there
 * are no regions, and "has a seating plan" is not known. The cases that need
 * the new routes are `fixme` below.
 *
 * The seed has no venue with a country and a city, so the spec makes one with
 * the owner's token (idempotently, by name) before it looks at the screen.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";
import { openWizard, skipPoster } from "./wizardHelpers";

const env = liveEnv();

const HALL = "Venues Screen Hall";
const CITY = "Madrid";
const WAITS = "waits for GET /v1/geo/world-countries and venue-directory (branch plan-screens-bot)";

const ROLES: { role: Role; label: string }[] = [
  { role: "org_admin", label: "owner" },
  { role: "organizer", label: "manager" },
  { role: "agent", label: "agent" },
];

function headers(role: Role) {
  return { Authorization: `Bearer ${person(env, role).access_token}` };
}

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/me`, { headers: headers(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

/** A venue in Spain with a city, made once with the owner's token. */
let ensured: Promise<string> | null = null;
function ensureHall(request: APIRequestContext): Promise<string> {
  ensured ??= (async () => {
    const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
    const owner = headers("org_admin");
    const list = await request.get(`${base}/venues`, { headers: owner });
    expect(list.status(), "GET venues").toBe(200);
    const have = ((await list.json()) as { venues: { id: string; name: string }[] }).venues.find((v) => v.name === HALL);
    if (have !== undefined) return have.id;
    const countries = await request.get(`${env.api_base_url}/v1/geo/countries?lang=en`);
    expect(countries.status(), "GET countries").toBe(200);
    const spain = ((await countries.json()) as { countries: { id: string; iso2: string }[] }).countries.find((c) => c.iso2 === "ES");
    expect(spain, "the reference has Spain").toBeDefined();
    const city = await request.post(`${base}/cities`, { headers: owner, data: { country_id: spain?.id, name: CITY, locale: "en" } });
    expect(city.ok(), `POST cities: ${await city.text()}`).toBe(true);
    const cityId = ((await city.json()) as { city: { id: string } }).city.id;
    const made = await request.post(`${base}/venues`, {
      headers: owner,
      data: { name: HALL, city_id: cityId, country: "ES", timezone: "Europe/Madrid", address_line1: "Calle Mayor 1", capacity_default: 120, status: "active" },
    });
    expect(made.ok(), `POST venues: ${await made.text()}`).toBe(true);
    return ((await made.json()) as { venue: { id: string } }).venue.id;
  })();
  return ensured;
}

async function openMore(page: Page) {
  await page.getByTestId("nav-more").click();
  await expect(page.getByTestId("more-sheet")).toBeVisible();
}

/** The chooser opens on the organization's country when it has venues, else on the quick row: get to the quick row. */
async function toQuickRow(page: Page) {
  const quick = page.getByTestId("venue-chooser-quick-ES");
  const back = page.getByTestId("venue-chooser-back");
  await expect(quick.or(back)).toBeVisible();
  if (await back.isVisible()) await back.click();
  await expect(quick).toBeVisible();
}

test.describe("venues on the three roles (live)", () => {
  for (const { role, label } of ROLES) {
    test(`${label} (${role}) ${role === "agent" ? "is told by the server what it may see" : "picks country, city, venue and sees the card"}`, async ({ page, request }) => {
      const hallId = await ensureHall(request);
      const me = await orgMe(request, role);
      const canRead = me.permissions.includes("venue.read");

      await signInAs(page, person(env, role));
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
      await openMore(page);

      if (!canRead) {
        await expect(page.getByTestId("nav-venues")).toHaveCount(0);
        return;
      }
      await page.getByTestId("nav-venues").click();
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/venues$`));
      await expect(page.getByRole("heading", { level: 1, name: "Venues" })).toBeVisible();
      await expect(page.getByTestId("venue-chooser")).toBeVisible();

      // Country -> city -> venue.
      await toQuickRow(page);
      await page.getByTestId("venue-chooser-quick-ES").click();
      const city = page.locator('[data-testid^="venue-chooser-city-"]', { hasText: CITY });
      await expect(city).toBeVisible();
      await expect(city).toContainText("1");
      await city.click();
      const row = page.getByTestId(`venue-chooser-venue-${hallId}`);
      await expect(row).toContainText(HALL);
      await expect(page.getByTestId("venue-chooser-not-listed")).toHaveCount(0);

      // The card.
      await row.click();
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/venues/${hallId}$`));
      const card = page.getByTestId("venue-card");
      await expect(card.getByTestId("venue-card-name")).toHaveText(HALL);
      await expect(card.getByTestId("venue-card-address")).toContainText("Calle Mayor 1");
      await expect(card.getByTestId("venue-card-zone")).toContainText("Europe/Madrid");

      const width = page.viewportSize()?.width ?? 375;
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
      await noA11yViolations(page);

      // A deep link opens the same card.
      await page.goto(`/o/${env.org_id}/venues/${hallId}`);
      await expect(page.getByTestId("venue-card-name")).toHaveText(HALL);
    });
  }

  test("the search finds a venue by its name from any step", async ({ page, request }) => {
    const hallId = await ensureHall(request);
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/venues`);
    await expect(page.getByTestId("venue-chooser")).toBeVisible();
    await page.getByTestId("venue-chooser-search").fill("Screen Hall");
    await expect(page.getByTestId(`venue-chooser-venue-${hallId}`)).toBeVisible();
    await page.getByTestId("venue-chooser-search").fill("zzzzzz nothing");
    await expect(page.getByTestId(`venue-chooser-venue-${hallId}`)).toHaveCount(0);
  });

  test("the wizard picks the venue through the chooser and shows its facts", async ({ page, request }) => {
    const hallId = await ensureHall(request);
    await openWizard(page, "org_admin");
    await skipPoster(page);
    await page.getByTestId("wizard-venue-mode-existing").check();
    await page.getByTestId("venue-chooser-search").fill("Screen Hall");
    await page.getByTestId(`venue-chooser-venue-${hallId}`).click();
    await expect(page.getByTestId("wizard-venue-chosen")).toContainText(HALL);
    await expect(page.getByTestId("wizard-venue-facts")).toContainText("Europe/Madrid");
    await page.getByTestId("wizard-venue-change").click();
    await expect(page.getByTestId("venue-chooser")).toBeVisible();
  });

  test("the regions list the seven regions of the world", async () => {
    test.fixme(true, WAITS);
  });

  test("a country outside the reference is chosen through its region and given to a new venue", async () => {
    test.fixme(true, WAITS);
  });
});
