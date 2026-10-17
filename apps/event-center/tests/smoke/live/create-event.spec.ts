/**
 * Twin test "create a draft GA event" (task 6-1c), against a REAL arena-api on
 * a migrated scratch database: the owner fills the wizard in a browser, presses
 * "Create event" and the platform stores a DRAFT event with one draft session
 * and exact categories; the list and the card show it. The API itself refuses
 * the agent, a foreign organization, a foreign venue and a request without a
 * token. Nothing here sells, publishes or sends anything.
 *
 * Serial: the scenarios share one organization. Every created event has a
 * unique name (`nonce`), and the checks count events BY NAME.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";
import { skipPoster, pickDay } from "./wizardHelpers";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const FOREIGN_ORG = "fe0000e2-0000-7000-8000-0000000000f1";
const FOREIGN_VENUE = "fe0000e2-0000-7000-8000-00000000e521";
const nonce = Date.now().toString(36);
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const CREATE_URL = /\/imports\/event-bundle(\?(?!dry_run=true).*)?$/;

interface EventRow {
  id: string;
  name: string;
  status: string;
}

async function eventsNamed(request: APIRequestContext, name: string): Promise<EventRow[]> {
  const res = await request.get(`${base}/events`, { headers: auth("org_admin") });
  expect(res.status()).toBe(200);
  const body = (await res.json()) as { events: EventRow[] };
  return body.events.filter((e) => e.name === name);
}

async function venueId(request: APIRequestContext): Promise<string> {
  const res = await request.get(`${base}/venues`, { headers: auth("org_admin") });
  const body = (await res.json()) as { venues: { id: string; name: string }[] };
  const v = body.venues.find((x) => x.name === "E2E Hall");
  if (v === undefined) throw new Error("the fixture venue E2E Hall is missing");
  return v.id;
}

/** A complete one-date bundle, the shape lib/wizard/bundle.ts sends. */
function bundle(name: string, venue: string, ref: string) {
  return {
    source: "arena",
    externalRef: ref,
    action: { actionName: name, description: "", age: "0+", promoterId: "" },
    actionEvent: { day: "05.12.2030", time: "19:00", currency: "EUR" },
    venue: { arenaVenueId: venue },
    categoryList: [{ categoryPriceName: "Admission", price: 10, availability: 50, nextCategoryIndex: -1, priceSchedule: [] }],
    publish: false,
  };
}

async function openFromList(page: Page) {
  await signInAs(page, person(env, "org_admin"));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.getByTestId("events-create").click();
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/new`));
}

async function fillForm(page: Page, name: string) {
  // Poster first: this spec is about creation, so it makes the conscious choice to go without a poster.
  await skipPoster(page);
  await page.getByTestId("wizard-name").fill(name);
  await page.getByTestId("wizard-description").fill("A twin-test show");
  await page.getByTestId("wizard-venue-mode-existing").check();
  await page.getByTestId("wizard-venue-pick").selectOption({ label: "E2E Hall" });
  await page.getByTestId("wizard-currency").fill("EUR");
  await pickDay(page, "wizard-date-0-day", "week");
  await page.getByTestId("wizard-date-0-time").fill("19:00");
  await page.getByTestId("wizard-tickets-0-mode-parallel").check();
  await page.getByTestId("wizard-cat-0-0-name").fill("Standard");
  await page.getByTestId("wizard-cat-0-0-price").fill("25,50");
  await page.getByTestId("wizard-cat-0-0-places").fill("120");
  await page.getByTestId("wizard-cat-0-1-name").fill("VIP");
  await page.getByTestId("wizard-cat-0-1-price").fill("60");
  await page.getByTestId("wizard-cat-0-1-places").fill("30");
  await expect(page.getByTestId("wizard-status")).toContainText("Everything you filled in is correct", { timeout: 25_000 });
}

function countCreates(page: Page): string[] {
  const seen: string[] = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().includes("/imports/event-bundle") && !r.url().includes("dry_run=true")) seen.push(r.url());
  });
  return seen;
}

// The base is shared with the other specs (the events list test expects the
// fixture event first): put it back by deleting what this file created. Only
// drafts of this run (the nonce is in the name) are touched.
test.afterAll(async ({ request }) => {
  const res = await request.get(`${base}/events`, { headers: auth("org_admin") });
  const all = ((await res.json()) as { events: EventRow[] }).events;
  for (const e of all.filter((x) => x.name.startsWith("Twin ") && x.name.endsWith(nonce) && x.status === "draft")) {
    const del = await request.delete(`${base}/events/${e.id}`, { headers: auth("org_admin") });
    expect(del.status(), `cleanup of ${e.name}`).toBeLessThan(300);
  }
  const left = ((await (await request.get(`${base}/events`, { headers: auth("org_admin") })).json()) as { events: EventRow[] }).events;
  expect(left.filter((x) => x.name.endsWith(nonce))).toEqual([]);
});

test.describe("create a draft GA event", () => {
  test("the owner creates it: one POST for a double click, a draft off sale, exact categories, the venue time zone", async ({ page, request }) => {
    const name = `Twin Create Show ${nonce}`;
    const writes = countCreates(page);
    await openFromList(page);
    await expect(page.getByTestId("wizard-create")).toBeDisabled();
    await fillForm(page, name);
    await noA11yViolations(page);
    await expect(page.getByTestId("wizard-create")).toBeEnabled();
    await expect(page.getByTestId("wizard-create-note")).toContainText("draft");

    await page.getByTestId("wizard-create").dblclick();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/[0-9a-f-]{36}$`), { timeout: 30_000 });
    expect(writes).toHaveLength(1);
    await expect(page.getByRole("heading", { level: 1, name })).toBeVisible();

    // The saved draft is gone from the browser.
    const stored = await page.evaluate(() => Object.keys(localStorage).filter((k) => k.includes("wizard")));
    expect(stored).toEqual([]);

    // The server is the truth: a draft, off sale, with exactly what was typed.
    const rows = await eventsNamed(request, name);
    expect(rows).toHaveLength(1);
    expect(rows[0].status).toBe("draft");
    const eventId = rows[0].id;
    const sessions = await request.get(`${base}/events/${eventId}/sessions`, { headers: auth("org_admin") });
    const sessionList = ((await sessions.json()) as { sessions: { id: string; status: string; start_at: string }[] }).sessions;
    expect(sessionList).toHaveLength(1);
    expect(sessionList[0].status).toBe("draft");
    // 19:00 on the wall clock of the venue (Europe/Madrid), whatever the offset of that day is.
    const wall = new Intl.DateTimeFormat("en-GB", { timeZone: "Europe/Madrid", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(sessionList[0].start_at));
    expect(wall).toBe("19:00");
    const tiers = await request.get(`${base}/events/${eventId}/sessions/${sessionList[0].id}/tiers`, { headers: auth("org_admin") });
    const tierList = ((await tiers.json()) as { tiers: { name: string; price_amount: number; currency: string; capacity: number | null; pricing_mode: string }[] }).tiers;
    const byName = Object.fromEntries(tierList.map((t) => [t.name, t]));
    expect(Object.keys(byName).sort()).toEqual(["Standard", "VIP"]);
    expect(byName.Standard).toMatchObject({ price_amount: 2550, currency: "EUR", capacity: 120, pricing_mode: "fixed" });
    expect(byName.VIP).toMatchObject({ price_amount: 6000, currency: "EUR", capacity: 30, pricing_mode: "fixed" });

    // The card shows the categories; the organization list shows the event.
    await expect(page.getByText("Standard").first()).toBeVisible();
    await page.goto(`/o/${env.org_id}/events`);
    await expect(page.getByText(name).first()).toBeVisible();
  });

  test("a refusal and a lost answer keep the draft; the retry cannot make a second event", async ({ page, request }) => {
    const name = `Twin Retry Show ${nonce}`;
    await openFromList(page);
    await fillForm(page, name);

    // 1) the platform refuses: the form stays, the words are shown.
    await page.route(CREATE_URL, (route) =>
      route.fulfill({
        status: 422,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "import.invalid_category", message: "bad category", request_id: "req-1" } }),
      }),
    );
    await page.getByTestId("wizard-create").click();
    await expect(page.getByTestId("wizard-save-rejected")).toBeVisible();
    await expect(page.getByTestId("wizard-name")).toHaveValue(name);
    expect(await eventsNamed(request, name)).toHaveLength(0);
    await page.unroute(CREATE_URL);

    // 2) the answer is lost AFTER the server stored the event: the request goes out for
    //    real, then the page sees a network failure.
    await page.route(CREATE_URL, async (route) => {
      await route.fetch();
      await route.abort("connectionreset");
    });
    await page.getByTestId("wizard-create").click();
    await expect(page.getByTestId("wizard-save-unknown")).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId("wizard-name")).toHaveValue(name);
    await expect(page.getByTestId("wizard-create")).toContainText("Check and send again");
    expect(await eventsNamed(request, name)).toHaveLength(1); // the lost answer did create it
    await page.unroute(CREATE_URL);
    await page.reload();
    await expect(page.getByTestId("wizard-name")).toHaveValue(name); // the draft survived a reload

    // 3) the retry carries the same key: the platform edits the event it made.
    await expect(page.getByTestId("wizard-status")).toContainText("Everything you filled in is correct", { timeout: 25_000 });
    await page.getByTestId("wizard-create").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/[0-9a-f-]{36}$`), { timeout: 30_000 });
    const rows = await eventsNamed(request, name);
    expect(rows).toHaveLength(1);
    expect(rows[0].status).toBe("draft");
  });

  test("the manager may create, the agent has no screen and no write, a foreign organization and a foreign venue are refused", async ({ page, request }) => {
    const venue = await venueId(request);
    const name = `Twin Manager Show ${nonce}`;
    const ok = await request.post(`${base}/imports/event-bundle`, { headers: auth("organizer"), data: bundle(name, venue, `web:test:${nonce}:manager`) });
    expect(ok.status(), await ok.text()).toBe(200);
    expect((await eventsNamed(request, name))[0].status).toBe("draft");

    // The agent: refused by the API, and the screen says "no access" (no button, no menu entry).
    const agentName = `Twin Agent Show ${nonce}`;
    const denied = await request.post(`${base}/imports/event-bundle`, { headers: auth("agent"), data: bundle(agentName, venue, `web:test:${nonce}:agent`) });
    expect([401, 403]).toContain(denied.status());
    expect(await eventsNamed(request, agentName)).toHaveLength(0);
    await signInAs(page, person(env, "agent"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await expect(page.getByTestId("events-create")).toHaveCount(0);
    await expect(page.getByTestId("nav-new-event")).toHaveCount(0);
    await page.goto(`/o/${env.org_id}/events/new`);
    await expect(page.getByTestId("wizard-no-access")).toBeVisible();
    await expect(page.getByTestId("wizard-create")).toHaveCount(0);

    // The owner of org A cannot write into org B, nor use org B's venue in org A.
    const foreign = await request.post(`${env.api_base_url}/v1/organizations/${FOREIGN_ORG}/imports/event-bundle`, {
      headers: auth("org_admin"),
      data: bundle(`Twin Foreign Show ${nonce}`, FOREIGN_VENUE, `web:test:${nonce}:foreign`),
    });
    expect([403, 404]).toContain(foreign.status());
    const wrongName = `Twin Wrong Venue ${nonce}`;
    const wrongVenue = await request.post(`${base}/imports/event-bundle`, { headers: auth("org_admin"), data: bundle(wrongName, FOREIGN_VENUE, `web:test:${nonce}:wrongvenue`) });
    expect(wrongVenue.status()).toBe(422);
    expect(await eventsNamed(request, wrongName)).toHaveLength(0);
    const anonymous = await request.post(`${base}/imports/event-bundle`, { data: bundle("Anonymous", venue, `web:test:${nonce}:anon`) });
    expect(anonymous.status()).toBe(401);
  });
});
