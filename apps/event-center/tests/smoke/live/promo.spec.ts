/**
 * Twin test "promo codes" (task 4-3): the web version of the bot's e2e
 * `TestBotE2E_PromoCodes` (httpserver/bot_e2e_promo_integration_test.go),
 * against a REAL arena-api, on owner, manager and agent of one organization.
 *
 * The truth about a role comes from the server (GET .../me and the routes
 * themselves), the truth about the data from the API's own answers. The
 * fixture (arena-e2e-seed + promo_fixture.go) holds: the base event (one EUR
 * date), "E2E Promo Swan Show" (two EUR dates), "E2E Promo Czech Show" (one
 * CZK date) and the code E2EUSED, used twice in two currencies by two named
 * buyers.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs as signInRaw, type Role } from "./liveEnv";

const env = liveEnv();
const SWAN = "E2E Promo Swan Show";
const CZECH = "E2E Promo Czech Show";
const USED = "E2EUSED";
const STAMP = Date.now().toString(36).toUpperCase().slice(-6);
const CLUB = `WEBCLUB${STAMP}`;
const FIXED = `WEBFIVE${STAMP}`;
const MANAGER_CODE = `WEBMGR${STAMP}`;

interface PromoCode {
  id: string;
  code: string;
  discount_type: string;
  discount_value: number;
  currency: string | null;
  applies_to_session_ids: string[];
  status: string;
  max_uses: number | null;
  max_uses_per_customer: number | null;
  valid_until: string | null;
  uses: number;
}

const org = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

async function listCodes(request: APIRequestContext, role: Role = "org_admin"): Promise<PromoCode[]> {
  const res = await request.get(`${org}/promo-codes`, { headers: auth(role) });
  expect(res.status(), `GET promo-codes as ${role}`).toBe(200);
  return ((await res.json()) as { promo_codes: PromoCode[] }).promo_codes;
}

async function codeByName(request: APIRequestContext, name: string, role: Role = "org_admin"): Promise<PromoCode | undefined> {
  return (await listCodes(request, role)).find((c) => c.code === name);
}

async function sessionsOf(request: APIRequestContext, eventName: string): Promise<string[]> {
  const events = await request.get(`${org}/events`, { headers: auth("org_admin") });
  const found = ((await events.json()) as { events: { id: string; name: string }[] }).events.find((e) => e.name === eventName);
  expect(found, `the fixture has the event ${eventName}`).toBeDefined();
  const sessions = await request.get(`${org}/events/${found?.id}/sessions`, { headers: auth("org_admin") });
  return ((await sessions.json()) as { sessions: { id: string }[] }).sessions.map((s) => s.id);
}

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${org}/me`, { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

/** Signs in through the login form and waits for the app: leaving too early cancels the request. */
async function signInAs(page: Page, p: ReturnType<typeof person>) {
  await signInRaw(page, p);
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
}

/** Opens the promo list the way a person does: through the navigation. */
async function openPromo(page: Page) {
  await page.getByTestId("nav-promo").click();
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/promo$`));
}

/** The words of a screen, never an e-mail of a buyer (the report shows names). */
async function noBuyerEmails(page: Page) {
  const body = await page.locator("body").innerText();
  expect(body).not.toContain("@example.test");
}

test.describe.serial("promo codes twin (live)", () => {
  test("the server's truth about the three roles", async ({ request }) => {
    const all = ["promo.read", "promo.create", "promo.update", "promo.delete"];
    for (const role of ["org_admin", "organizer"] as const) {
      const me = await orgMe(request, role);
      for (const p of all) expect(me.permissions, `${role} holds ${p}`).toContain(p);
    }
    const agent = await orgMe(request, "agent");
    for (const p of all) expect(agent.permissions, `agent does not hold ${p}`).not.toContain(p);
  });

  test("owner: the list, the used code says several currencies, the report shows names only", async ({ page, request }) => {
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await openPromo(page);
    const used = page.getByTestId("promo-row").filter({ hasText: USED });
    await expect(used).toContainText("10% off");
    await expect(used).toContainText("Active");
    await expect(used).toContainText("several currencies");
    await noA11yViolations(page);

    await used.getByTestId("promo-code").click();
    await expect(page.getByTestId("promo-card-code")).toHaveText(USED);
    await expect(page.getByTestId("promo-usage")).toContainText("Used: 2");
    await expect(page.getByTestId("promo-usage")).toContainText("several currencies");
    await expect(page.getByTestId("promo-usage-row")).toHaveCount(2);
    await expect(page.getByTestId("promo-usage-table")).toContainText("Alice Promotest");
    await expect(page.getByTestId("promo-usage-table")).toContainText("Boris Promotest");
    await noBuyerEmails(page);

    const used2 = (await codeByName(request, USED)) as PromoCode;
    expect(used2.uses).toBe(2);
    const [download] = await Promise.all([page.waitForEvent("download"), page.getByTestId("promo-csv").click()]);
    expect(download.suggestedFilename()).toMatch(/\.csv$/i);
    await noA11yViolations(page);
  });

  test("owner: a percent club code is created with an empty session scope", async ({ page, request }) => {
    await signInAs(page, person(env, "org_admin"));
    await openPromo(page);
    await page.getByTestId("promo-new").click();
    // Lower case is turned into capitals.
    await page.getByTestId("promo-f-code").fill(CLUB.toLowerCase());
    await page.getByTestId("promo-f-value").fill("15");
    await noA11yViolations(page);
    await page.getByTestId("promo-review").click();
    await expect(page.getByTestId("promo-review")).toContainText(CLUB);
    await page.getByTestId("promo-create-go").click();
    await expect(page.getByTestId("promo-card-code")).toHaveText(CLUB);
    await expect(page.getByTestId("promo-notice")).toContainText("is created");

    const row = (await codeByName(request, CLUB)) as PromoCode;
    expect(row.discount_type).toBe("percent");
    expect(row.discount_value).toBe(15);
    expect(row.currency).toBeNull();
    expect(row.status).toBe("active");
    // A club code: no session list at all, so it covers future sessions too.
    expect(row.applies_to_session_ids).toEqual([]);
  });

  test("owner: a name that exists is refused whatever its case, nothing is written", async ({ page, request }) => {
    const before = (await listCodes(request)).length;
    await signInAs(page, person(env, "org_admin"));
    await page.goto(`/o/${env.org_id}/promo/new`);
    await page.getByTestId("promo-f-code").fill(CLUB.toLowerCase());
    await page.getByTestId("promo-f-value").fill("20");
    await page.getByTestId("promo-review").click();
    await expect(page.getByRole("alert")).toContainText("already have a code with this name");
    expect((await listCodes(request)).length).toBe(before);
    // Spaces are not allowed in a code: buyers type it by hand.
    await page.getByTestId("promo-f-code").fill("two words");
    await page.getByTestId("promo-review").click();
    await expect(page.getByRole("alert")).toContainText("without spaces");
  });

  test("owner: a fixed code names its currency and takes only sessions sold in it", async ({ page, request }) => {
    await signInAs(page, person(env, "org_admin"));
    await page.goto(`/o/${env.org_id}/promo/new`);
    await page.getByTestId("promo-f-code").fill(FIXED);
    await page.getByTestId("promo-f-type-fixed_amount").check();
    await page.getByTestId("promo-f-currency").fill("eur");
    await page.getByTestId("promo-f-value").fill("5,50");
    await expect(page.getByText("We read it as")).toBeVisible();
    await page.getByTestId("promo-f-total").fill("100");
    await page.getByTestId("promo-f-per").fill("2");
    await page.getByTestId("promo-f-scope-pick").check();
    // No pick yet: an empty list would be a club code, so it is refused.
    await page.getByTestId("promo-review").click();
    await expect(page.getByRole("alert")).toContainText("Choose at least one session");

    // A session sold in CZK cannot take a EUR code: its box is off.
    await page.getByTestId("promo-pick-event").selectOption({ label: CZECH });
    await expect(page.getByTestId("promo-pick-session")).toBeDisabled();
    await expect(page.getByTestId("promo-picker")).toContainText("Sold in CZK, while this code is in EUR.");
    await page.getByTestId("promo-pick-event").selectOption({ label: env.event_name });
    await page.getByTestId("promo-pick-session").first().check();
    await expect(page.getByTestId("promo-pick-count")).toContainText("Chosen sessions: 1");

    await page.getByTestId("date-quick-week").click();
    const until = (await page.getByTestId("promo-f-until-value").innerText()).trim().slice(0, 10);
    expect(until).toMatch(/^\d{4}-\d{2}-\d{2}$/);

    await page.getByTestId("promo-review").click();
    await expect(page.getByTestId("promo-review")).toContainText("Works only on sessions sold in EUR");
    await page.getByTestId("promo-create-go").click();
    await expect(page.getByTestId("promo-card-code")).toHaveText(FIXED);
    await expect(page.getByTestId("promo-discount")).toContainText("5.50");

    const row = (await codeByName(request, FIXED)) as PromoCode;
    expect(row.discount_type).toBe("fixed_amount");
    expect(row.discount_value).toBe(550);
    expect(row.currency).toBe("EUR");
    expect(row.applies_to_session_ids).toEqual([env.session_id]);
    expect(row.max_uses).toBe(100);
    expect(row.max_uses_per_customer).toBe(2);
    // The last day ends at 23:59:59 UTC of the chosen date.
    expect(row.valid_until).toBe(`${until}T23:59:59Z`);
  });

  test("owner: pause and activate change the status", async ({ page, request }) => {
    const row = (await codeByName(request, FIXED)) as PromoCode;
    await signInAs(page, person(env, "org_admin"));
    await page.goto(`/o/${env.org_id}/promo/${row.id}`);
    await page.getByTestId("promo-pause").click();
    await expect(page.getByTestId("promo-notice")).toContainText("is paused");
    await expect(page.getByTestId("promo-activate")).toBeVisible();
    expect((await codeByName(request, FIXED))?.status).toBe("paused");
    await page.getByTestId("promo-activate").click();
    await expect(page.getByTestId("promo-notice")).toContainText("is active again");
    expect((await codeByName(request, FIXED))?.status).toBe("active");
  });

  test("owner: the sessions edit merges per event and never empties by accident", async ({ page, request }) => {
    const row = (await codeByName(request, FIXED)) as PromoCode;
    const swan = await sessionsOf(request, SWAN);
    expect(swan).toHaveLength(2);
    await signInAs(page, person(env, "org_admin"));
    await page.goto(`/o/${env.org_id}/promo/${row.id}`);

    // Add the two dates of the other event: the base event's date stays.
    await page.getByTestId("promo-sessions-open").click();
    await page.getByTestId("promo-pick-event").selectOption({ label: SWAN });
    await page.getByTestId("promo-pick-all").click();
    await expect(page.getByTestId("promo-pick-count")).toContainText("Chosen sessions: 3");
    await page.getByTestId("promo-sessions-save").click();
    await expect(page.getByTestId("promo-notice")).toContainText("works on the chosen sessions now");
    expect(new Set((await codeByName(request, FIXED))?.applies_to_session_ids)).toEqual(new Set([env.session_id, ...swan]));

    // Take that event's dates away: the other event's date is kept.
    await page.getByTestId("promo-sessions-open").click();
    await page.getByTestId("promo-pick-event").selectOption({ label: SWAN });
    await page.getByTestId("promo-pick-none").click();
    await expect(page.getByTestId("promo-pick-count")).toContainText("Chosen sessions: 1");
    await page.getByTestId("promo-sessions-save").click();
    await expect(page.getByTestId("promo-sessions")).toContainText("Chosen sessions: 1");
    expect((await codeByName(request, FIXED))?.applies_to_session_ids).toEqual([env.session_id]);

    // Emptying the list is not a save: it would turn the code into a club code.
    await page.getByTestId("promo-sessions-open").click();
    await page.getByTestId("promo-pick-event").selectOption({ label: env.event_name });
    await page.getByTestId("promo-pick-none").click();
    await expect(page.getByTestId("promo-sessions-save")).toBeDisabled();
    expect((await codeByName(request, FIXED))?.applies_to_session_ids).toEqual([env.session_id]);

    // "All sessions" is its own, explicit button: the empty list is stored.
    await page.getByTestId("promo-sessions-all").click();
    await expect(page.getByTestId("promo-notice")).toContainText("works on all sessions now");
    await expect(page.getByTestId("promo-sessions")).toContainText("All sessions");
    expect((await codeByName(request, FIXED))?.applies_to_session_ids).toEqual([]);
  });

  test("owner: delete needs the word, the name stays taken", async ({ page, request }) => {
    const row = (await codeByName(request, CLUB)) as PromoCode;
    await signInAs(page, person(env, "org_admin"));
    await page.goto(`/o/${env.org_id}/promo/${row.id}`);
    await page.getByTestId("promo-delete").click();
    await expect(page.getByTestId("confirm-word")).toContainText("Delete this promo code?");
    // A wrong word changes nothing.
    await page.getByRole("textbox").fill("remove");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("confirm-word")).toContainText("does not match");
    expect(await codeByName(request, CLUB)).toBeDefined();
    // The right word, in any case.
    await page.getByRole("textbox").fill("delete");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/promo`));
    await expect(page.getByTestId("promo-notice")).toContainText(`${CLUB} is deleted`);
    expect(await codeByName(request, CLUB)).toBeUndefined();

    // The server still refuses the name of a deleted code (a soft delete).
    const taken = await request.post(`${org}/promo-codes`, {
      headers: auth("org_admin"),
      data: { code: CLUB, discount_type: "percent", discount_value: 5, applies_to_session_ids: [] },
    });
    expect(taken.status()).toBe(409);
    expect(((await taken.json()) as { error: { code: string } }).error.code).toBe("promo.duplicate");
    // ... and the screen says so in words.
    await page.goto(`/o/${env.org_id}/promo/new`);
    await page.getByTestId("promo-f-code").fill(CLUB);
    await page.getByTestId("promo-f-value").fill("5");
    await page.getByTestId("promo-review").click();
    await page.getByTestId("promo-create-go").click();
    await expect(page.getByTestId("promo-error")).toContainText("A deleted code keeps its name too");
  });

  test("manager: holds all four permissions and does the whole job", async ({ page, request }) => {
    await signInAs(page, person(env, "organizer"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await openPromo(page);
    await expect(page.getByTestId("promo-new")).toBeVisible();
    await page.getByTestId("promo-new").click();
    await page.getByTestId("promo-f-code").fill(MANAGER_CODE);
    await page.getByTestId("promo-f-value").fill("25");
    await page.getByTestId("promo-review").click();
    await page.getByTestId("promo-create-go").click();
    await expect(page.getByTestId("promo-card-code")).toHaveText(MANAGER_CODE);
    await expect(page.getByTestId("promo-pause")).toBeVisible();
    await expect(page.getByTestId("promo-sessions-open")).toBeVisible();
    await expect(page.getByTestId("promo-delete")).toBeVisible();
    await page.getByTestId("promo-pause").click();
    await expect(page.getByTestId("promo-activate")).toBeVisible();
    expect((await codeByName(request, MANAGER_CODE, "organizer"))?.status).toBe("paused");
    await noA11yViolations(page);

    // The manager deletes with the word too.
    await page.getByTestId("promo-delete").click();
    await page.getByRole("textbox").fill("DELETE");
    await page.getByTestId("confirm-word-submit").click();
    await expect(page.getByTestId("promo-notice")).toContainText(`${MANAGER_CODE} is deleted`);
    expect(await codeByName(request, MANAGER_CODE, "organizer")).toBeUndefined();
  });

  test("agent: no entry, a plain explanation, and the server says 403", async ({ page, request }) => {
    const used = (await codeByName(request, USED)) as PromoCode;
    const apiCalls: string[] = [];
    page.on("request", (req) => {
      if (req.url().startsWith(env.api_base_url)) apiCalls.push(`${req.method()} ${new URL(req.url()).pathname}`);
    });
    await signInAs(page, person(env, "agent"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await expect(page.getByTestId("nav-promo")).toHaveCount(0);
    await page.goto(`/o/${env.org_id}/promo`);
    await expect(page.getByTestId("promo-no-access")).toContainText("does not include promo codes");
    await page.goto(`/o/${env.org_id}/promo/new`);
    await expect(page.getByTestId("promo-no-access")).toContainText("does not allow creating promo codes");
    await page.goto(`/o/${env.org_id}/promo/${used.id}`);
    await expect(page.getByTestId("promo-no-access")).toBeVisible();
    await noA11yViolations(page);
    // The screen never asked for a code: the request behind it was never sent.
    expect(apiCalls.filter((c) => c.includes("/promo-code"))).toEqual([]);

    // The server itself refuses every promo route to the agent.
    const headers = auth("agent");
    expect((await request.get(`${org}/promo-codes`, { headers })).status()).toBe(403);
    expect((await request.get(`${org}/promo-codes/${used.id}`, { headers })).status()).toBe(403);
    expect((await request.get(`${org}/promo-code-redemptions`, { headers })).status()).toBe(403);
    expect(
      (await request.post(`${org}/promo-codes`, { headers, data: { code: `AGENT${STAMP}`, discount_type: "percent", discount_value: 5, applies_to_session_ids: [] } })).status(),
    ).toBe(403);
    expect((await request.patch(`${org}/promo-codes/${used.id}`, { headers, data: { status: "paused" } })).status()).toBe(403);
    expect((await request.delete(`${org}/promo-codes/${used.id}`, { headers })).status()).toBe(403);
    // Nothing of that reached the code.
    expect((await codeByName(request, USED))?.status).toBe("active");
  });
});
