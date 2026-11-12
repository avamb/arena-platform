/**
 * Twin test "the sectioned menu and the first steps" on the three roles of one
 * organization, against a REAL arena-api (owner decision 2026-11-12: the web
 * menu has the same sections as the Telegram bot's menu).
 *
 * What it proves, on the phone viewport of the live config (375 px):
 *  - the bar has five items (Events, Orders, Promotion, Entrance, Menu), and an
 *    item the role cannot use is not there;
 *  - the Menu sheet shows headed groups in the order of the bot's menu, with
 *    exactly the items the server's permissions allow, and EVERY item opens its
 *    screen (nothing the old menu reached was lost);
 *  - the "First steps" card shows what GET .../getting-started says: only the
 *    steps this person may do, a done step as plain text, an open one as a link
 *    that opens the right screen; no card when everything is done or nothing is
 *    listed;
 *  - the Entrance and Help screens, the brand note of the promoters screen, the
 *    payments section of Organization data;
 *  - no horizontal overflow of the page at 375 px, and the "add to home screen"
 *    note shows on a phone and not on a wide screen.
 *
 * The truth about each person comes from GET /v1/organizations/{org_id}/me and
 * GET /v1/organizations/{org_id}/getting-started with the person's own token.
 * The table of expected items below is written out here on purpose: it does not
 * import the app's own menu model.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();

const ROLES: { role: Role; label: string }[] = [
  { role: "org_admin", label: "owner" },
  { role: "organizer", label: "manager" },
  { role: "agent", label: "agent" },
];

const orgUrl = (path: string) => `${env.api_base_url}/v1/organizations/${env.org_id}${path}`;
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

async function permissionsOf(request: APIRequestContext, role: Role): Promise<string[]> {
  const res = await request.get(orgUrl("/me"), { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return ((await res.json()) as { permissions: string[] }).permissions;
}

interface Step {
  key: "org_data" | "payments" | "event" | "sales_link";
  done: boolean;
}

async function stepsOf(request: APIRequestContext, role: Role): Promise<{ complete: boolean; steps: Step[] }> {
  const res = await request.get(orgUrl("/getting-started"), { headers: auth(role) });
  expect(res.status(), `GET getting-started as ${role}`).toBe(200);
  return ((await res.json()) as { getting_started: { complete: boolean; steps: Step[] } }).getting_started;
}

/** Item ids of one group, from the permissions alone. */
function expectedGroups(perms: string[]): [string, string[]][] {
  const can = (p: string) => perms.includes(p);
  const groups: [string, string[]][] = [
    ["new", can("import.bil24_session") ? ["nav-new-event"] : []],
    ["events", ["menu-events", "nav-summary"]],
    ["orders", [...(can("order.read") ? ["menu-orders"] : []), ...(can("refund.read") ? ["menu-refunds"] : [])]],
    [
      "promotion",
      [
        ...(can("promo.read") ? ["nav-promo"] : []),
        ...(can("complimentary.read") ? ["nav-invitations"] : []),
        ...(can("sales_link.read") ? ["nav-links"] : []),
        ...(can("promo.read") || can("order.read") ? ["nav-growth"] : []),
      ],
    ],
    ["entrance", ["nav-scanner", ...(can("order.read") ? ["nav-guest-list"] : [])]],
    [
      "settings",
      [
        ...(can("org.update") ? ["nav-org-data"] : []),
        ...(can("org.update") && can("payment_config.read") ? ["nav-payments"] : []),
        ...(can("membership.read") ? ["nav-team"] : []),
        ...(can("promoter.read") ? ["nav-promoters"] : []),
        ...(can("venue.read") ? ["nav-venues"] : []),
        "nav-notifications",
        ...(can("billing.read") ? ["nav-billing"] : []),
      ],
    ],
    ["help", ["nav-help"]],
  ];
  return groups.filter(([, items]) => items.length > 0);
}

/** Where each item leads (the path after /o/<org>, and the hash). */
const TARGET: Record<string, RegExp> = {
  "nav-new-event": /\/events\/new$/,
  "menu-events": /\/events$/,
  "nav-summary": /\/summary$/,
  "menu-orders": /\/orders$/,
  "menu-refunds": /\/refunds$/,
  "nav-promo": /\/promo$/,
  "nav-invitations": /\/invitations$/,
  "nav-links": /\/links$/,
  "nav-growth": /\/growth$/,
  "nav-scanner": /\/entrance$/,
  "nav-guest-list": /\/entrance#guest-list$/,
  "nav-org-data": /\/org-data$/,
  "nav-payments": /\/org-data#payments$/,
  "nav-team": /\/team$/,
  "nav-promoters": /\/promoters$/,
  "nav-venues": /\/venues$/,
  "nav-notifications": /\/notifications$/,
  "nav-billing": /\/billing$/,
  "nav-help": /\/help$/,
};

async function openMenu(page: Page) {
  await page.getByTestId("nav-more").click();
  await expect(page.getByTestId("more-sheet")).toBeVisible();
}

async function noOverflow(page: Page, what: string) {
  const [scroll, client] = await page.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
  expect(scroll, `${what}: the page is ${scroll}px wide in a ${client}px window`).toBeLessThanOrEqual(client);
}

test.describe("the sectioned menu on the three roles (live)", () => {
  for (const { role, label } of ROLES) {
    test(`${label} (${role}): the bar and the Menu sheet show what the server allows`, async ({ page, request }) => {
      const perms = await permissionsOf(request, role);
      const groups = expectedGroups(perms);
      const can = (p: string) => perms.includes(p);

      await signInAs(page, person(env, role));
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));

      // The bar: five items at most, an unusable one is absent.
      const hasPromotion = groups.some(([id]) => id === "promotion");
      await expect(page.getByTestId("nav-events")).toBeVisible();
      await expect(page.getByTestId("nav-orders")).toHaveCount(can("order.read") ? 1 : 0);
      await expect(page.getByTestId("nav-promotion")).toHaveCount(hasPromotion ? 1 : 0);
      await expect(page.getByTestId("nav-entrance")).toBeVisible();
      await expect(page.getByTestId("nav-more")).toBeVisible();
      // Without order.read the second item is Refunds, when the role holds refund.read.
      const refundsOnBar = !can("order.read") && can("refund.read");
      await expect(page.getByTestId("nav-refunds")).toHaveCount(refundsOnBar ? 1 : 0);
      await expect(page.getByTestId("tabbar").locator(".tab-item")).toHaveCount(
        3 + (can("order.read") || refundsOnBar ? 1 : 0) + (hasPromotion ? 1 : 0),
      );
      // The summary moved into the Menu sheet.
      await expect(page.getByTestId("nav-summary")).toHaveCount(0);
      await noOverflow(page, "the events list with the bar");

      // The Menu sheet: headed groups, in order, with the allowed items.
      await openMenu(page);
      const sheet = page.getByTestId("more-sheet");
      const seen = await sheet.locator("section.more-group").evaluateAll((sections) =>
        sections.map((s) => [
          (s.getAttribute("data-testid") ?? "").replace("more-group-", ""),
          Array.from(s.querySelectorAll("a[data-testid]")).map((a) => a.getAttribute("data-testid") ?? ""),
        ]),
      );
      expect(seen, `the Menu sheet of ${role}`).toEqual(groups);
      for (const h of await sheet.locator("h3.more-group-title").all()) await expect(h).toBeVisible();
      await expect(page.getByTestId("theme-switch")).toBeVisible();
      await expect(page.getByTestId("logout")).toBeVisible();
      await noA11yViolations(page);
    });

    test(`${label} (${role}): every item of the Menu opens its screen`, async ({ page, request }) => {
      const perms = await permissionsOf(request, role);
      const ids = expectedGroups(perms).flatMap(([, items]) => items);
      await signInAs(page, person(env, role));
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));

      for (const id of ids) {
        await openMenu(page);
        await page.getByTestId("more-sheet").getByTestId(id).click();
        await expect(page.getByTestId("more-sheet"), `${id} closes the sheet`).toHaveCount(0);
        await expect(page, id).toHaveURL(TARGET[id]);
        await expect(page.locator("main h1").first(), `${id}: the screen has a heading`).toBeVisible();
        if (id === "nav-payments" && perms.includes("org.update")) {
          // The payments section of Organization data: its own heading, connected or not.
          await expect(page.locator("#payments")).toBeVisible();
          await expect(page.locator("#payments h2")).toHaveText("Payments from buyers");
          await expect(page.getByTestId("org-data-payments").or(page.getByTestId("org-data-payments-connected"))).toHaveCount(1);
        }
        await noOverflow(page, id);
      }
    });

    test(`${label} (${role}): Promotion, Entrance and Help`, async ({ page, request }) => {
      const perms = await permissionsOf(request, role);
      const groups = expectedGroups(perms);
      const promo = groups.find(([id]) => id === "promotion")?.[1] ?? [];
      await signInAs(page, person(env, role));
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));

      if (promo.length > 0) {
        await page.getByTestId("nav-promotion").click();
        const sheet = page.getByTestId("promotion-sheet");
        await expect(sheet).toBeVisible();
        expect(await sheet.locator("a[data-testid]").evaluateAll((as) => as.map((a) => a.getAttribute("data-testid")))).toEqual(promo);
        await expect(page.getByTestId("more-sheet")).toHaveCount(0);
        await sheet.getByTestId(promo[0]).click();
        await expect(page).toHaveURL(TARGET[promo[0]]);
        // On a promotion screen the bar's Promotion item is the active one.
        await expect(page.getByTestId("nav-promotion")).toHaveClass(/active/);
      }

      await page.getByTestId("nav-entrance").click();
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/entrance$`));
      await expect(page.getByRole("heading", { level: 1 })).toHaveText("Entrance to the event");
      await expect(page.getByTestId("nav-entrance")).toHaveClass(/active/);
      await expect(page.getByTestId("entrance-android")).toHaveAttribute("href", "https://play.google.com/store/apps/details?id=com.arenasoldout.macs");
      await expect(page.getByTestId("entrance-ios")).toHaveAttribute("href", "https://apps.apple.com/app/arenasoldout/id6770095694");
      if (perms.includes("order.read")) {
        await expect(page.getByTestId("guest-list-subscription")).toBeVisible();
        await page.getByTestId("entrance-events").click();
        await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events$`));
      } else {
        await expect(page.getByTestId("entrance-guest-no-access")).toBeVisible();
        await expect(page.getByTestId("guest-list-subscription")).toHaveCount(0);
      }

      await page.goto(`/o/${env.org_id}/help`);
      await expect(page.getByRole("heading", { level: 1 })).toHaveText("Help");
      const helped = await page.getByTestId("help-sections").locator("[data-testid^='help-section-']").evaluateAll((els) =>
        els.map((e) => (e.getAttribute("data-testid") ?? "").replace("help-section-", "")),
      );
      expect(helped, "Help describes the sections this role has").toEqual(groups.map(([id]) => id).filter((id) => id !== "help"));
      await noA11yViolations(page);
      await noOverflow(page, "help");

      if (perms.includes("promoter.read")) {
        await page.goto(`/o/${env.org_id}/promoters`);
        await expect(page.getByTestId("promoters-brand-note")).toContainText("A brand is the name under which the event is sold");
      }
    });

    test(`${label} (${role}): the First steps card follows the server`, async ({ page, request }) => {
      const perms = await permissionsOf(request, role);
      const gs = await stepsOf(request, role);
      // The server lists only the steps this person may do.
      const keys = gs.steps.map((s) => s.key);
      expect(keys.includes("org_data"), "org_data needs org.update").toBe(perms.includes("org.update"));
      expect(keys.includes("payments"), "payments needs payment_config.read").toBe(perms.includes("payment_config.read"));
      expect(keys.includes("event"), "event needs import.bil24_session").toBe(perms.includes("import.bil24_session"));
      // The seed has one event.
      if (keys.includes("event")) expect(gs.steps.find((s) => s.key === "event")?.done).toBe(true);

      await signInAs(page, person(env, role));
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
      const card = page.getByTestId("first-steps");
      if (gs.steps.length === 0 || gs.complete) {
        await expect(page.getByTestId("event-list")).toBeVisible();
        await expect(card).toHaveCount(0);
        return;
      }
      await expect(card).toBeVisible();
      const done = gs.steps.filter((s) => s.done).length;
      await expect(page.getByTestId("first-steps-progress")).toHaveText(`${done} of ${gs.steps.length} done`);
      await expect(card.locator("li")).toHaveCount(gs.steps.length);
      for (const s of gs.steps) {
        const row = page.getByTestId(`first-step-row-${s.key}`);
        await expect(row).toHaveAttribute("data-done", s.done ? "true" : "false");
        await expect(row.locator("a")).toHaveCount(s.done ? 0 : 1);
      }
      await noOverflow(page, "the first-steps card");

      // An open step is a link that opens its screen.
      const open = gs.steps.find((s) => !s.done) as Step;
      const target: Record<Step["key"], RegExp> = {
        org_data: /\/org-data$/,
        payments: /\/org-data#payments$/,
        event: /\/events\/new$/,
        sales_link: /\/links\/new$/,
      };
      await page.getByTestId(`first-step-${open.key}`).click();
      await expect(page, `the ${open.key} step`).toHaveURL(target[open.key]);
      await expect(page.locator("main h1").first()).toBeVisible();
    });
  }

  test("the add-to-home-screen note shows on a phone and not on a wide screen", async ({ page }) => {
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await expect(page.getByTestId("a2hs")).toBeVisible();
    await page.setViewportSize({ width: 1280, height: 800 });
    await expect(page.getByTestId("a2hs")).toHaveCount(0);
    // The bar is still five items under the header.
    await expect(page.getByTestId("tabbar").locator(".tab-item")).toHaveCount(5);
    await page.setViewportSize({ width: 375, height: 812 });
    await expect(page.getByTestId("a2hs")).toBeVisible();
  });
});
