/**
 * Twin test "page statistics of an event" (task 2-3, HP-2a), the web version of
 * the bot's card line "Page views: N in 30 days", against a REAL arena-api, on
 * the three roles of one organization.
 *
 * The seed (arena-e2e-seed, page_stats_fixture.go) writes counters of the main
 * event for the two days before today: page views 14 (2 of them probable bots),
 * video plays 4, partner clicks 4 (2 probable bots), and the source
 * "instagram / spring" with 8 page views.
 *
 * The truth about each role comes from GET /v1/organizations/{org_id}/me and
 * from the route itself: the block shows when the person holds event.read, and
 * the API refuses the others on its own. The page statistics of another
 * organization's event are a 404 for a key of the wrong organization (covered
 * by the Go integration test TestPageStats_OrganizerReadStaysInsideTheOrganization).
 */
import { expect, test, type APIRequestContext } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${base}/me`, { headers: { Authorization: `Bearer ${person(env, role).access_token}` } });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

const ROLES: { role: Role; label: string }[] = [
  { role: "org_admin", label: "owner" },
  { role: "organizer", label: "manager" },
  { role: "agent", label: "agent" },
];

test.describe("page statistics of an event (live)", () => {
  for (const { role, label } of ROLES) {
    test(`${label} (${role}): the block follows event.read, the API agrees`, async ({ page, request }) => {
      const me = await orgMe(request, role);
      const mayRead = me.permissions.includes("event.read");

      const res = await request.get(`${base}/events/${env.event_id}/page-stats`, {
        headers: { Authorization: `Bearer ${person(env, role).access_token}` },
      });
      expect(res.status(), `GET page-stats as ${role}`).toBe(mayRead ? 200 : 403);
      if (mayRead) {
        const body = (await res.json()) as {
          totals: Record<string, { hits: number; suspect_hits: number }>;
          days: unknown[];
          sources: { utm_source: string; utm_campaign: string; hits: number; suspect_hits: number }[];
        };
        // Seeded numbers: people and probable bots are kept apart by the API.
        expect(body.totals.page_view?.hits).toBeGreaterThanOrEqual(12);
        expect(body.totals.page_view?.suspect_hits).toBeGreaterThanOrEqual(2);
        expect(body.days.length).toBeGreaterThanOrEqual(2);
        expect(body.sources.find((s) => s.utm_source === "instagram" && s.utm_campaign === "spring")?.hits).toBe(8);
      }

      await signInAs(page, person(env, role));
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
      await page.goto(`/o/${env.org_id}/events/${env.event_id}`);
      await expect(page.getByRole("heading", { level: 1 })).toContainText(env.event_name);

      const block = page.getByTestId("page-stats");
      if (!mayRead) {
        await expect(block).toHaveCount(0);
        return;
      }
      await expect(block).toBeVisible();
      await expect(block).toContainText("Page statistics");
      await expect(block).toContainText("not proof for an advertising account");
      await expect(block.getByTestId("stat-views")).toContainText("14");
      await expect(block.getByTestId("stat-views")).toContainText("2 of them look like bots");
      await expect(block.getByTestId("stat-plays")).toContainText("4");
      await expect(block.getByTestId("stat-clicks")).toContainText("4");
      await expect(block.getByTestId("stat-clicks")).toContainText("2 of them look like bots");
      await expect(block.getByTestId("stat-source").first()).toContainText("instagram");
      await expect(block.getByTestId("stat-source").first()).toContainText("spring");
      await expect(block.getByTestId("stat-source").first()).toContainText("8");

      const [download] = await Promise.all([page.waitForEvent("download"), block.getByTestId("csv-page-stats").click()]);
      expect(download.suggestedFilename()).toMatch(/^page-stats_.*\.csv$/i);

      const width = page.viewportSize()?.width ?? 375;
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
      await noA11yViolations(page);
    });
  }
});
