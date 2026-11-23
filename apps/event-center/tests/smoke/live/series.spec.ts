/**
 * Twin test "tours: list, card, events of a tour, link an event" (spec 63, L-2), the web version of the bot's
 * tour step and button (`eventbot/series.go`), against a REAL arena-api, on two roles of one organization (owner =
 * org_admin, manager = organizer). The truth about each tour comes from the API; the screen must agree with it.
 *
 * The scenarios run one after another on one organization (one tour is walked through every step), so the file is
 * serial. The event is the fixture event of the harness (env.event_id): the test links it and unlinks it again.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const nonce = Date.now().toString(36);
const NAME = `E2E Tour ${nonce}`;
const RENAMED = `E2E Tour Renamed ${nonce}`;
const MANAGER_NAME = `E2E Manager Tour ${nonce}`;
const VIDEO = "https://www.youtube.com/watch?v=dQw4w9WgXcQ";

const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;

interface SeriesRow {
  id: string;
  slug: string;
  name: string;
  hero_video_url: string | null;
  hide_children_in_lists: boolean;
  event_count: number;
  archived_at: string | null;
}

async function apiList(request: APIRequestContext, role: Role): Promise<SeriesRow[]> {
  const res = await request.get(`${base}/series?include_archived=true`, { headers: auth(role) });
  expect(res.status(), `GET series as ${role}`).toBe(200);
  return ((await res.json()) as { items: SeriesRow[] }).items;
}

async function apiFind(request: APIRequestContext, role: Role, name: string): Promise<SeriesRow> {
  const found = (await apiList(request, role)).find((s) => s.name === name);
  expect(found, `tour ${name} in the API list`).toBeDefined();
  return found as SeriesRow;
}

async function eventSeriesId(request: APIRequestContext): Promise<string | null> {
  const res = await request.get(`${base}/events`, { headers: auth("org_admin") });
  expect(res.status(), "GET events").toBe(200);
  const body = (await res.json()) as { events: { id: string; series_id?: string | null }[] };
  const event = body.events.find((e) => e.id === env.event_id);
  expect(event, "the fixture event").toBeDefined();
  return event?.series_id ?? null;
}

async function openSeries(page: Page) {
  await page.getByTestId("nav-more").click();
  await page.getByTestId("nav-series").click();
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/series`));
}

async function createViaUi(page: Page, name: string) {
  await page.getByTestId("series-new").click();
  await page.getByTestId("series-create-name").fill(name);
  await page.getByTestId("series-create-submit").click();
}

test.describe("tours (live, two roles)", () => {
  test("owner: create, edit, video link, flag, events of the tour", async ({ page, request }) => {
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await openSeries(page);

    // Create: only the name is typed, the page address comes from the name, the card opens.
    await createViaUi(page, NAME);
    await expect(page.getByTestId("series-heading")).toHaveText(NAME);
    const created = await apiFind(request, "org_admin", NAME);
    expect(created.slug, "an auto slug").toMatch(/^e2e-tour-/);
    await expect(page.getByTestId("series-slug-input")).toHaveValue(created.slug);

    // A bad video link is stopped on the screen, before any call.
    await page.getByTestId("series-video").fill("http://example.com/video");
    await expect(page.getByTestId("series-video-error")).toBeVisible();
    await expect(page.getByTestId("series-save")).toBeDisabled();

    // Rename, video, flag: ONE save.
    await page.getByTestId("series-name").fill(RENAMED);
    await page.getByTestId("series-video").fill(VIDEO);
    await page.getByTestId("series-hide-children").check();
    await page.getByTestId("series-save").click();
    await expect(page.getByTestId("series-save-status")).toBeVisible();
    const saved = await apiFind(request, "org_admin", RENAMED);
    expect(saved.id).toBe(created.id);
    expect(saved.hero_video_url).toBe(VIDEO);
    expect(saved.hide_children_in_lists).toBe(true);

    // A taken address (the slug of an event or another tour) is refused in words.
    const other = await request.post(`${base}/series`, { headers: auth("org_admin"), data: { name: `E2E Other ${nonce}` } });
    expect(other.status()).toBe(201);
    const otherSlug = ((await other.json()) as SeriesRow).slug;
    await page.getByTestId("series-slug-input").fill(otherSlug);
    await page.getByTestId("series-save").click();
    await expect(page.getByTestId("series-error")).toContainText("another tour or an event");
    expect((await apiFind(request, "org_admin", RENAMED)).slug).toBe(created.slug);

    // Link the fixture event through the API: the card lists it, with a link to its screen.
    const link = await request.patch(`${base}/events/${env.event_id}`, { headers: auth("org_admin"), data: { series_id: created.id } });
    expect(link.status()).toBe(200);
    await page.reload();
    await expect(page.getByTestId("series-event-row")).toHaveCount(1);
    await page.getByTestId("series-event-row").getByRole("link").click();
    await expect(page).toHaveURL(new RegExp(`/events/${env.event_id}`));
    await expect(page.getByTestId("event-series-link")).toBeVisible();
    await page.goto(`/o/${env.org_id}/events/${env.event_id}/edit`);
    await page.getByTestId("event-series-select").selectOption("");
    await page.getByTestId("event-series-save").click();
    await expect(page.getByTestId("event-series-saved")).toBeVisible();
    expect(await eventSeriesId(request)).toBeNull();

    const width = page.viewportSize()?.width ?? 375;
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
    await noA11yViolations(page);
  });

  test("owner: link an event from its edit screen, the badge in the list, archive", async ({ page, request }) => {
    const tour = await apiFind(request, "org_admin", RENAMED);
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/events/${env.event_id}/edit`);
    await page.getByTestId("event-series-select").selectOption(tour.id);
    await page.getByTestId("event-series-save").click();
    await expect(page.getByTestId("event-series-saved")).toBeVisible();
    expect(await eventSeriesId(request)).toBe(tour.id);

    // The events list shows the tour on the row of the event.
    await page.goto(`/o/${env.org_id}/events`);
    await expect(page.getByTestId("event-series-badge").first()).toContainText(RENAMED);

    // Archive the tour: it leaves the active list, shows in the archived tab, and the event keeps the link.
    await openSeries(page);
    await page.getByTestId("series-row").filter({ hasText: RENAMED }).getByRole("link").click();
    await page.getByTestId("series-archive-open").click();
    await expect(page.getByTestId("series-archive-ask")).toContainText("its page stops opening");
    await page.getByTestId("series-archive-cancel").click();
    expect((await apiFind(request, "org_admin", RENAMED)).archived_at).toBeNull();
    await page.getByTestId("series-archive-open").click();
    await page.getByTestId("series-archive-confirm").click();
    await expect(page.getByTestId("series-status")).toContainText("is in the archive");
    expect((await apiFind(request, "org_admin", RENAMED)).archived_at).not.toBeNull();
    expect(await eventSeriesId(request)).toBe(tour.id);

    await page.getByTestId("back-to-series").click();
    await expect(page.getByTestId("series-list")).not.toContainText(RENAMED);
    await page.getByTestId("series-tab-archived").click();
    await expect(page).toHaveURL(/f=archived/);
    await expect(page.getByTestId("series-row").filter({ hasText: RENAMED })).toBeVisible();

    // An archived tour cannot be linked: the API answers 422 and the words say so.
    const refused = await request.patch(`${base}/events/${env.event_id}`, { headers: auth("org_admin"), data: { series_id: null } });
    expect(refused.status()).toBe(200);
    const archivedLink = await request.patch(`${base}/events/${env.event_id}`, { headers: auth("org_admin"), data: { series_id: tour.id } });
    expect(archivedLink.status(), "linking an archived tour").toBe(422);
    expect(((await archivedLink.json()) as { error: { code: string } }).error.code).toBe("event.invalid_series");
  });

  test("manager (organizer) can create and edit a tour", async ({ page, request }) => {
    await signInAs(page, person(env, "organizer"));
    await openSeries(page);
    await expect(page.getByTestId("series-list")).not.toContainText(`E2E Foreign`);
    await createViaUi(page, MANAGER_NAME);
    await expect(page.getByTestId("series-heading")).toHaveText(MANAGER_NAME);
    const mine = await apiFind(request, "organizer", MANAGER_NAME);
    await page.getByTestId("series-position").fill("7");
    await page.getByTestId("series-save").click();
    await expect(page.getByTestId("series-save-status")).toBeVisible();
    const res = await request.get(`${base}/series/${mine.id}`, { headers: auth("organizer") });
    expect(((await res.json()) as { position: number }).position).toBe(7);
  });
});
