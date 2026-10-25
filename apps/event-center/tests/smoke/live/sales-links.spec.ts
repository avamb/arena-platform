/**
 * Twin test "links and QR" (spec 43 section 5.3, task QL-12): the web version
 * of the bot's sales-link e2e (`bot_e2e_sales_links_*_integration_test.go`),
 * against a REAL arena-api, on owner, manager and agent of one organization.
 *
 * The scenario of the spec: create a link → GET of the redirect leads to the
 * event page → the scan is in the statistics. Then the card's retarget, edit,
 * archive and restore, a manager's link for a date from the session card, and
 * the agent, who holds neither permission.
 *
 * The redirect is asked on the API host (`/go/{code}`), not on the short host:
 * the harness has no go.arenasoldout.com. The scan is sent with a browser's
 * User-Agent, because the API counts a client that names Playwright as a bot.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs as signInRaw, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const org = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const ORG_SLUG = "e2e-event-center-org";
const BROWSER_UA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1";
const STAMP = Date.now().toString(36);
const LABEL = `E2E poster ${STAMP}`;
const RENAMED = `E2E poster renamed ${STAMP}`;
const MANAGER_LABEL = `E2E flyer ${STAMP}`;

interface SalesLink {
  id: string;
  code: string;
  short_url: string;
  label: string;
  placement_kind: string;
  utm: Record<string, string>;
  status: string;
  scan_count: number;
  orders_count: number;
  target: { kind: string; event_id: string | null; session_id: string | null; promoter_id: string | null; url: string | null };
  destination_url: string;
  destination_degraded: boolean;
  destination_reason: string;
}

const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${org}/me`, { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

async function findLink(request: APIRequestContext, label: string, role: Role = "org_admin"): Promise<SalesLink> {
  const res = await request.get(`${org}/sales-links?status=all&limit=100`, { headers: auth(role) });
  expect(res.status(), `GET sales-links as ${role}`).toBe(200);
  const found = ((await res.json()) as { items: SalesLink[] }).items.find((l) => l.label === label);
  expect(found, `the link "${label}" in the API list`).toBeDefined();
  return found as SalesLink;
}

async function getLink(request: APIRequestContext, id: string): Promise<SalesLink> {
  const res = await request.get(`${org}/sales-links/${id}`, { headers: auth("org_admin") });
  expect(res.status()).toBe(200);
  return (await res.json()) as SalesLink;
}

async function eventSlug(request: APIRequestContext): Promise<string> {
  const res = await request.get(`${org}/events`, { headers: auth("org_admin") });
  const ev = ((await res.json()) as { events: { id: string; slug: string | null }[] }).events.find((e) => e.id === env.event_id);
  expect(ev?.slug, "the fixture event has a page address (materials fixture)").toBeTruthy();
  return ev?.slug ?? "";
}

/** A buyer's scan: GET of the code with a browser's User-Agent, the redirect not followed. */
async function scan(request: APIRequestContext, code: string) {
  const res = await request.get(`${env.api_base_url}/go/${code}`, { maxRedirects: 0, headers: { "User-Agent": BROWSER_UA } });
  expect(res.status(), "GET /go/{code}").toBe(302);
  return res.headers().location ?? "";
}

/** Signs in through the login form and waits for the app: leaving too early cancels the request. */
async function signInAs(page: Page, role: Role) {
  await signInRaw(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
}

test.describe("links and QR twin (live)", () => {
  test("the server's truth about the three roles", async ({ request }) => {
    for (const role of ["org_admin", "organizer"] as const) {
      const me = await orgMe(request, role);
      expect(me.permissions, `${role} holds sales_link.read`).toContain("sales_link.read");
      expect(me.permissions, `${role} holds sales_link.manage`).toContain("sales_link.manage");
    }
    const agent = await orgMe(request, "agent");
    expect(agent.permissions).not.toContain("sales_link.read");
    expect(agent.permissions).not.toContain("sales_link.manage");
  });

  test("owner: a link from the event card, the redirect leads to the event page, the scan is counted", async ({ page, request }) => {
    const slug = await eventSlug(request);
    await signInAs(page, "org_admin");
    await expect(page.getByTestId("nav-links")).toBeVisible();

    await page.goto(`/o/${env.org_id}/events/${env.event_id}`);
    await page.getByTestId("event-qr-link").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/links/new\\?event=${env.event_id}`));
    await expect(page.getByTestId("link-kind-event")).toBeChecked();
    await expect(page.getByTestId("link-target-text")).toContainText(env.event_name);
    // An empty label is refused on the screen, nothing is sent.
    await page.getByTestId("link-create-go").click();
    await expect(page.getByTestId("link-label-error")).toBeVisible();
    await page.getByTestId("link-f-label").fill(LABEL);
    await page.getByTestId("link-create-go").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/links/[0-9a-f-]{36}`));
    await expect(page.getByTestId("link-notice")).toContainText("The link is created");
    await expect(page.getByTestId("link-title")).toHaveText(LABEL);
    await expect(page.getByTestId("link-placement")).toHaveText("Poster");
    await expect(page.getByTestId("link-short-url")).toHaveText(/\/[a-z0-9]{7}$/);
    await expect(page.getByTestId("link-copy")).toBeVisible();
    await expect(page.getByTestId("link-where")).toContainText(env.event_name);
    await expect(page.getByTestId("link-dest")).toContainText(`/${ORG_SLUG}/${slug}`);
    await expect(page.getByTestId("link-why")).toHaveCount(0);
    await expect(page.getByTestId("link-qr-img")).toBeVisible();
    await expect(page.getByTestId("link-stats-scans")).toContainText("Scans: 0");

    const link = await findLink(request, LABEL);
    expect(link.target.kind).toBe("event");
    expect(link.target.event_id).toBe(env.event_id);
    expect(link.destination_degraded).toBe(false);
    expect(link.destination_url.endsWith(`/${ORG_SLUG}/${slug}`)).toBe(true);
    expect(link.utm.utm_source).toBe("qr");
    expect(link.utm.utm_content).toBe(link.code);

    // The buyer scans the code: the redirect leads to the event page with the utm labels.
    const location = await scan(request, link.code);
    expect(location.startsWith(link.destination_url)).toBe(true);
    expect(location).toContain("utm_source=qr");
    expect(location).toContain(`utm_content=${link.code}`);

    // The scan is counted after the answer went out: wait for it.
    await expect
      .poll(async () => (await getLink(request, link.id)).scan_count, { timeout: 15_000, message: "the scan reaches scan_count" })
      .toBeGreaterThanOrEqual(1);
    const statsRes = await request.get(`${org}/sales-links/${link.id}/stats`, { headers: auth("org_admin") });
    expect(statsRes.status()).toBe(200);
    const stats = (await statsRes.json()) as { scans_total: number; by_day: { day: string; hits: number }[]; orders: { count: number } };
    expect(stats.scans_total).toBeGreaterThanOrEqual(1);
    expect(stats.by_day.reduce((n, d) => n + d.hits, 0)).toBeGreaterThanOrEqual(1);

    await page.reload();
    await expect(page.getByTestId("link-scans")).toHaveText("1");
    await expect(page.getByTestId("link-stats-scans")).toContainText("Scans: 1");
    await expect(page.getByTestId("link-stats-days")).toContainText("1");
    await expect(page.getByTestId("link-stats-orders")).toContainText("Orders: 0");

    const [png] = await Promise.all([page.waitForEvent("download"), page.getByTestId("link-qr-png").click()]);
    expect(png.suggestedFilename()).toMatch(/\.png$/i);
    const [svg] = await Promise.all([page.waitForEvent("download"), page.getByTestId("link-qr-svg").click()]);
    expect(svg.suggestedFilename()).toMatch(/\.svg$/i);

    const width = page.viewportSize()?.width ?? 375;
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
    await noA11yViolations(page);
  });

  test("owner: the list, retarget, edit, archive and restore", async ({ page, request }) => {
    const link = await findLink(request, LABEL);
    await signInAs(page, "org_admin");
    await page.getByTestId("nav-links").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/links$`));
    const row = page.getByTestId("link-row").filter({ hasText: LABEL });
    await expect(row).toContainText("Scans: 1");
    await expect(row).toContainText(`Event: ${env.event_name}`);
    await noA11yViolations(page);
    await row.getByTestId("link-label").click();
    await expect(page.getByTestId("link-title")).toHaveText(LABEL);

    // The same target again changes nothing and sends nothing.
    const patches: string[] = [];
    page.on("request", (r) => {
      if (r.method() === "PATCH" && r.url().includes("/sales-links/")) patches.push(r.url());
    });
    await page.getByTestId("link-retarget-open").click();
    await expect(page.getByTestId("link-kind-event")).toBeChecked();
    await page.getByTestId("link-retarget-go").click();
    await expect(page.getByTestId("link-retarget-same")).toBeVisible();
    expect(patches).toEqual([]);

    // The page with all events: a new address.
    await page.getByTestId("link-kind-org_page").check();
    await page.getByTestId("link-retarget-go").click();
    await expect(page.getByTestId("link-notice")).toContainText("now leads to a new page");
    await expect(page.getByTestId("link-dest")).toContainText(`/${ORG_SLUG}`);
    await expect(page.getByTestId("link-where")).toContainText("Page with all events");
    let saved = await getLink(request, link.id);
    expect(saved.target.kind).toBe("org_page");
    expect(saved.destination_url.endsWith(`/${ORG_SLUG}`)).toBe(true);
    expect(patches).toHaveLength(1);

    // Label, placement and utm: the whole utm set travels, so utm_source survives a change of utm_campaign.
    await page.getByTestId("link-edit-open").click();
    await page.getByTestId("link-edit-label").fill(RENAMED);
    await page.getByTestId("link-edit-pl-flyer").check();
    await page.getByTestId("link-edit-utm_campaign").fill("e2e-print");
    await page.getByTestId("link-edit-save").click();
    await expect(page.getByTestId("link-notice")).toContainText("saved");
    await expect(page.getByTestId("link-title")).toHaveText(RENAMED);
    await expect(page.getByTestId("link-placement")).toHaveText("Flyer");
    saved = await getLink(request, link.id);
    expect(saved.label).toBe(RENAMED);
    expect(saved.placement_kind).toBe("flyer");
    expect(saved.utm.utm_campaign).toBe("e2e-print");
    expect(saved.utm.utm_source).toBe("qr");

    // The archive: a question first, then the code leads to the page with all events.
    await page.getByTestId("link-archive").click();
    await expect(page.getByTestId("link-archive-confirm")).toBeVisible();
    await page.getByTestId("link-archive-confirm-cancel").click();
    expect((await getLink(request, link.id)).status).toBe("active");
    await page.getByTestId("link-archive").click();
    await page.getByTestId("link-archive-confirm-submit").click();
    await expect(page.getByTestId("link-notice")).toContainText("in the archive");
    await expect(page.getByTestId("link-why")).toContainText("in the archive");
    await expect(page.getByTestId("link-retarget-open")).toHaveCount(0);
    await expect(page.getByTestId("link-restore")).toBeVisible();
    saved = await getLink(request, link.id);
    expect(saved.status).toBe("archived");
    expect(saved.destination_reason).toBe("archived");
    const location = await scan(request, saved.code);
    expect(location.startsWith(saved.destination_url)).toBe(true);

    await page.goto(`/o/${env.org_id}/links`);
    await expect(page.getByTestId("link-row").filter({ hasText: RENAMED })).toHaveCount(0);
    await page.getByTestId("links-filter-archived").click();
    await expect(page.getByTestId("link-row").filter({ hasText: RENAMED })).toHaveCount(1);
    await page.getByTestId("link-row").filter({ hasText: RENAMED }).getByTestId("link-label").click();
    await page.getByTestId("link-restore").click();
    await expect(page.getByTestId("link-notice")).toContainText("works again");
    expect((await getLink(request, link.id)).status).toBe("active");
    await noA11yViolations(page);
  });

  test("manager: a link for a date from the session card, and the list of one event", async ({ page, request }) => {
    await signInAs(page, "organizer");
    await expect(page.getByTestId("nav-links")).toBeVisible();
    await page.goto(`/o/${env.org_id}/events/${env.event_id}/sessions/${env.session_id}`);
    await page.getByTestId("session-qr-link").click();
    await expect(page).toHaveURL(/\/links\/new\?/);
    await expect(page.getByTestId("link-kind-session")).toBeChecked();
    await expect(page.getByTestId("link-target-session")).toHaveValue(env.session_id);
    await expect(page.getByTestId("link-target-text")).toContainText(`Date of ${env.event_name}`);
    await page.getByTestId("link-pl-flyer").check();
    await page.getByTestId("link-f-label").fill(MANAGER_LABEL);
    await page.getByTestId("link-create-go").click();
    await expect(page.getByTestId("link-title")).toHaveText(MANAGER_LABEL);
    await expect(page.getByTestId("link-where")).toContainText(`Date of ${env.event_name}`);
    await expect(page.getByTestId("link-why")).toHaveCount(0);

    const link = await findLink(request, MANAGER_LABEL, "organizer");
    expect(link.target.kind).toBe("session");
    expect(link.target.session_id).toBe(env.session_id);
    expect(link.target.event_id).toBe(env.event_id);
    expect(link.placement_kind).toBe("flyer");

    await page.getByTestId("link-event-list").click();
    await expect(page).toHaveURL(new RegExp(`/links\\?event=${env.event_id}`));
    await expect(page.getByTestId("links-event-filter")).toHaveValue(env.event_id);
    await expect(page.getByTestId("link-row").filter({ hasText: MANAGER_LABEL })).toHaveCount(1);
    await noA11yViolations(page);
  });

  test("agent: no menu entry, the screen explains, the API refuses", async ({ page, request }) => {
    const list = await request.get(`${org}/sales-links`, { headers: auth("agent") });
    expect(list.status()).toBe(403);
    const create = await request.post(`${org}/sales-links`, {
      headers: auth("agent"),
      data: { target: { kind: "org_page" }, label: "agent try" },
    });
    expect(create.status()).toBe(403);

    await signInAs(page, "agent");
    await expect(page.getByTestId("nav-links")).toHaveCount(0);
    const calls: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/sales-links")) calls.push(r.url());
    });
    await page.goto(`/o/${env.org_id}/links`);
    await expect(page.getByTestId("links-no-access")).toBeVisible();
    await page.goto(`/o/${env.org_id}/links/new`);
    await expect(page.getByTestId("links-no-access")).toBeVisible();
    expect(calls).toEqual([]);
    await noA11yViolations(page);
  });
});
