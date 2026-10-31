/**
 * Twin test "event wizard: hall plan" (S3a), the web version of the bot's
 * seated date (wizard_seated.go; the bot's own proof is
 * TestBotE2E_WizardSeatedDate), against a REAL arena-api.
 *
 * The venue is the seed's "E2E Hall" with its plan "E2E Hall Plan"
 * (seats_fixture.go: Premium, Standard, Economy, 102 seats). What it proves:
 *  - the venue's plan is offered next to "without a plan", which stays the
 *    default (every other wizard twin sells this hall without seats);
 *  - a chosen plan asks the price of each of its categories and hides the
 *    date's places and ticket editor;
 *  - an extra price category (VIP, a colour) is added over the plan's;
 *  - "Create event" makes a DRAFT event whose session is bound to the plan,
 *    with the plan's categories priced and VIP without places, and opens the
 *    seat map of that session, which shows VIP to paint seats into (the map
 *    is the organizer's, read although the event is not published).
 */
import { expect, test, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, type Role } from "./liveEnv";
import { fillEventAndVenue, openWizard, pickDay } from "./wizardHelpers";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const PLAN_VERSION = "fe0000e2-0000-7000-8000-00000000e604";
const nonce = Date.now().toString(36);

const status = (page: Page) => page.getByTestId("wizard-status");
const correct = (page: Page) => expect(status(page)).toContainText("Everything you filled in is correct", { timeout: 25_000 });

test.describe("event wizard: hall plan (live)", () => {
  test("a draft event sold by the venue's plan, with an extra price category, opens its seat map", async ({ page, request }) => {
    const name = `Twin Seated Show ${nonce}`;
    await openWizard(page, "org_admin");
    await fillEventAndVenue(page, name);

    const section = page.getByTestId("wizard-seating");
    await expect(section).toBeVisible();
    await expect(page.getByTestId("wizard-seating-plan-none")).toBeChecked();
    const plan = page.getByTestId(`wizard-seating-plan-${PLAN_VERSION}`);
    await expect(plan).toBeVisible();
    await plan.check();

    await page.getByTestId("wizard-seating-cat-1-price").fill("50");
    await page.getByTestId("wizard-seating-cat-2-price").fill("30");
    await page.getByTestId("wizard-seating-cat-3-price").fill("15");
    await page.getByTestId("wizard-seating-extra-add").click();
    await page.getByTestId("wizard-seating-extra-0-name").fill("VIP");
    await page.getByTestId("wizard-seating-extra-0-price").fill("99");
    await page.getByTestId("wizard-seating-extra-0-color-dc2626").check();

    await pickDay(page, "wizard-date-0-day", "month");
    await page.getByTestId("wizard-date-0-time").fill("19:00");
    await expect(page.getByTestId("wizard-date-0-seated")).toContainText("E2E Hall Plan");
    await expect(page.getByTestId("wizard-date-0-capacity")).toHaveCount(0);
    await correct(page);
    await noA11yViolations(page);

    await page.getByTestId("wizard-create").click();
    await page.waitForURL(/\/events\/[^/]+\/sessions\/[^/]+\/seats/, { timeout: 30_000 });
    await expect(page.locator('[data-tier-name="VIP"]')).toBeVisible({ timeout: 20_000 });

    // The platform's own view of what was made.
    const m = /\/events\/([^/]+)\/sessions\/([^/]+)\/seats/.exec(page.url());
    expect(m).not.toBeNull();
    const [, eventId, sessionId] = m as RegExpExecArray;
    const ses = await request.get(`${base}/events/${eventId}/sessions`, { headers: auth("org_admin") });
    expect(ses.status()).toBe(200);
    const session = ((await ses.json()) as { sessions: { id: string; seating_plan_version_id?: string | null }[] }).sessions.find((s) => s.id === sessionId);
    expect(session?.seating_plan_version_id).toBe(PLAN_VERSION);
    const tiers = await request.get(`${base}/events/${eventId}/sessions/${sessionId}/tiers`, { headers: auth("org_admin") });
    expect(tiers.status()).toBe(200);
    const list = ((await tiers.json()) as { tiers: { name: string; price_amount: number; seat_count?: number; color?: string | null }[] }).tiers;
    const vip = list.find((t) => t.name === "VIP");
    expect(vip?.price_amount).toBe(9900);
    expect(vip?.seat_count ?? 0).toBe(0);
    expect(vip?.color).toBe("#dc2626");
    expect(list.map((t) => t.price_amount).sort((a, b) => a - b)).toEqual([1500, 3000, 5000, 9900]);
  });
});
