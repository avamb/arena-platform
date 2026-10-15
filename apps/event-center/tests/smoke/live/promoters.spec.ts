/**
 * Twin test "promoters: list, card, page address, archive" (task 4-6), the
 * web version of the bot's `bot_e2e_promoters_integration_test.go`, against a
 * REAL arena-api, on the three roles of one organization (owner = org_admin,
 * manager = organizer, agent).
 *
 * The truth about each role comes from GET /v1/organizations/{org_id}/me; the
 * screen must agree with it, and the API itself must refuse what the screen
 * hides. The scenarios run one after another on one organization (one promoter
 * is walked through every step), so the file is serial.
 *
 * The seed (arena-e2e-seed) adds ANOTHER organization with one promoter,
 * "E2E Foreign Promoter": it must never be listed and never be reachable.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const FOREIGN_PROMOTER_ID = "fe0000e2-0000-7000-8000-0000000000f2";
const FOREIGN_PROMOTER_NAME = "E2E Foreign Promoter";
const FOREIGN_SLUG = "e2e-foreign-promoter";

interface PromoterRow {
  id: string;
  name: string;
  slug: string | null;
  website: string | null;
  archived: boolean;
  archived_at: string | null;
  public_url: string;
}

const nonce = Date.now().toString(36);
const NAME = `E2E Promoter ${nonce}`;
const RENAMED = `E2E Renamed ${nonce}`;
const NEW_SLUG = `e2e-page-${nonce}`;
const MANAGER_NAME = `E2E Manager ${nonce}`;

const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${base}/me`, { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

async function apiList(request: APIRequestContext, role: Role): Promise<PromoterRow[]> {
  const res = await request.get(`${base}/promoters?include_archived=true`, { headers: auth(role) });
  expect(res.status(), `GET promoters as ${role}`).toBe(200);
  return ((await res.json()) as { promoters: PromoterRow[] }).promoters;
}

async function apiFind(request: APIRequestContext, role: Role, name: string): Promise<PromoterRow> {
  const found = (await apiList(request, role)).find((p) => p.name === name);
  expect(found, `promoter ${name} in the API list`).toBeDefined();
  return found as PromoterRow;
}

async function openPromoters(page: Page) {
  await page.getByRole("link", { name: "Promoters" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/promoters`));
}

async function createViaUi(page: Page, name: string) {
  await page.getByTestId("promoter-new").click();
  await page.getByTestId("create-name").fill(name);
  await page.getByTestId("create-submit").click();
}

/** Opens a row of the list by name and waits for the card. */
async function openCard(page: Page, name: string) {
  await page.getByTestId("promoter-row").filter({ hasText: name }).getByRole("link").click();
  await expect(page.getByTestId("promoter-name")).toHaveText(name);
}

async function editField(page: Page, field: string, value: string) {
  await page.getByTestId(`edit-${field}`).click();
  await page.getByTestId(`input-${field}`).fill(value);
  await page.getByTestId("field-save").click();
}

test.describe("promoters (live, three roles)", () => {
  test("owner: create, see the auto slug, rename, website, page address, duplicates", async ({ page, request }) => {
    const me = await orgMe(request, "org_admin");
    expect(me.permissions).toContain("promoter.read");
    expect(me.permissions).toContain("promoter.manage");

    const apiCalls: string[] = [];
    page.on("request", (req) => {
      if (req.url().startsWith(env.api_base_url)) apiCalls.push(`${req.method()} ${new URL(req.url()).pathname}`);
    });

    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await openPromoters(page);

    // The foreign organization's promoter is not here, in no tab.
    await expect(page.getByTestId("promoter-list")).not.toContainText(FOREIGN_PROMOTER_NAME);
    await page.getByTestId("promoters-tab-archived").click();
    await expect(page.getByTestId("promoter-list")).not.toContainText(FOREIGN_PROMOTER_NAME);
    await page.getByTestId("promoters-tab-active").click();

    // Create: only the name is typed, the page address comes from the name.
    await createViaUi(page, NAME);
    await expect(page.getByTestId("promoter-added")).toContainText(NAME);
    const row = page.getByTestId("promoter-row").filter({ hasText: NAME });
    await expect(row).toBeVisible();
    const created = await apiFind(request, "org_admin", NAME);
    expect(created.slug, "an auto slug").toMatch(/^e2e-promoter-/);
    await expect(row.getByTestId("promoter-slug")).toContainText(created.slug as string);
    expect(created.public_url).toBe(`https://tickets.e2e.test/${created.slug}`);

    // A second promoter with the same name is refused in words.
    await createViaUi(page, NAME.toUpperCase());
    await expect(page.getByTestId("create-error")).toContainText("already has this name");
    await page.getByTestId("create-cancel").click();

    // The card: the public link is shown and has a copy button.
    await openCard(page, NAME);
    await expect(page.getByTestId("page-url")).toHaveText(created.public_url);
    await expect(page.getByTestId("page-copy")).toBeVisible();

    // Rename.
    await editField(page, "name", RENAMED);
    await expect(page.getByTestId("promoter-name")).toHaveText(RENAMED);
    expect((await apiFind(request, "org_admin", RENAMED)).id).toBe(created.id);

    // A bad shape is stopped on the screen, before any call.
    await page.getByTestId("edit-email").click();
    await page.getByTestId("input-email").fill("not an address");
    const before = apiCalls.filter((c) => c.startsWith("PATCH")).length;
    await page.getByTestId("field-save").click();
    await expect(page.getByTestId("field-error")).toContainText("does not look like an e-mail");
    expect(apiCalls.filter((c) => c.startsWith("PATCH")).length, "no PATCH for a bad shape").toBe(before);
    await page.getByTestId("field-cancel").click();

    // Website without a scheme is stored with https://.
    await editField(page, "website", "partner.example");
    await expect(page.getByTestId("field-value-website")).toHaveText("https://partner.example");
    expect((await apiFind(request, "org_admin", RENAMED)).website).toBe("https://partner.example");

    // Clearing an optional field sends null.
    await page.getByTestId("edit-website").click();
    await page.getByTestId("field-clear").click();
    await expect(page.getByTestId("field-value-website")).toHaveText("Not set");
    expect((await apiFind(request, "org_admin", RENAMED)).website).toBeNull();

    // The page address: old -> new is shown first. "Keep" saves nothing.
    const oldSlug = created.slug as string;
    await page.getByTestId("edit-slug").click();
    await page.getByTestId("input-slug").fill(`https://tickets.e2e.test/${NEW_SLUG}`.toUpperCase());
    await page.getByTestId("field-save").click();
    await expect(page.getByTestId("slug-confirm-old")).toHaveText(oldSlug);
    await expect(page.getByTestId("slug-confirm-new")).toHaveText(NEW_SLUG);
    await page.getByTestId("slug-confirm-back").click();
    expect((await apiFind(request, "org_admin", RENAMED)).slug).toBe(oldSlug);

    // A taken address (another organization's promoter) is refused in words.
    await page.getByTestId("edit-slug").click();
    await page.getByTestId("input-slug").fill(FOREIGN_SLUG);
    await page.getByTestId("field-save").click();
    await page.getByTestId("slug-confirm-save").click();
    await expect(page.getByTestId("field-error")).toContainText("already taken");
    expect((await apiFind(request, "org_admin", RENAMED)).slug).toBe(oldSlug);
    await page.getByTestId("slug-confirm-back").click();

    // Confirm the new address: the row and the public link follow.
    await page.getByTestId("edit-slug").click();
    await page.getByTestId("input-slug").fill(NEW_SLUG);
    await page.getByTestId("field-save").click();
    await page.getByTestId("slug-confirm-save").click();
    await expect(page.getByTestId("field-value-slug")).toHaveText(NEW_SLUG);
    await expect(page.getByTestId("page-url")).toHaveText(`https://tickets.e2e.test/${NEW_SLUG}`);
    const moved = await apiFind(request, "org_admin", RENAMED);
    expect(moved.slug).toBe(NEW_SLUG);

    const width = page.viewportSize()?.width ?? 375;
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
    await noA11yViolations(page);
  });

  test("owner: archive says what stops opening, hides the row, and restore brings it back", async ({ page, request }) => {
    await signInAs(page, person(env, "org_admin"));
    await openPromoters(page);
    await openCard(page, RENAMED);

    await page.getByTestId("archive-open").click();
    const ask = page.getByTestId("archive-ask");
    await expect(ask).toContainText("public page");
    await expect(ask).toContainText("stop opening");
    await expect(ask).toContainText("tickets and letters still show it as the organizer");
    // "No" archives nothing.
    await page.getByTestId("archive-cancel").click();
    expect((await apiFind(request, "org_admin", RENAMED)).archived).toBe(false);

    await page.getByTestId("archive-open").click();
    await page.getByTestId("archive-confirm").click();
    await expect(page.getByTestId("promoter-status")).toContainText("is archived");
    const archived = await apiFind(request, "org_admin", RENAMED);
    expect(archived.archived).toBe(true);
    expect(archived.archived_at).not.toBeNull();
    await expect(page.getByTestId("page-archived")).toBeVisible();
    await expect(page.getByTestId("edit-name")).toHaveCount(0);

    // The active list hides it, the archived tab shows it.
    await page.getByTestId("back-to-promoters").click();
    await expect(page.getByTestId("promoter-list")).not.toContainText(RENAMED);
    await page.getByTestId("promoters-tab-archived").click();
    await expect(page).toHaveURL(/f=archived/);
    await expect(page.getByTestId("promoter-row").filter({ hasText: RENAMED })).toBeVisible();

    // Restore.
    await openCard(page, RENAMED);
    await page.getByTestId("restore").click();
    await expect(page.getByTestId("promoter-status")).toContainText("is active again");
    const restored = await apiFind(request, "org_admin", RENAMED);
    expect(restored.archived).toBe(false);
    expect(restored.archived_at).toBeNull();
    await page.getByTestId("back-to-promoters").click();
    await expect(page.getByTestId("promoter-row").filter({ hasText: RENAMED })).toBeVisible();
  });

  test("manager (organizer) can do everything the owner can here", async ({ page, request }) => {
    const me = await orgMe(request, "organizer");
    expect(me.permissions).toContain("promoter.read");
    expect(me.permissions).toContain("promoter.manage");

    await signInAs(page, person(env, "organizer"));
    await openPromoters(page);
    // The promoter the owner made is the same list.
    await expect(page.getByTestId("promoter-row").filter({ hasText: RENAMED })).toBeVisible();
    await expect(page.getByTestId("promoter-list")).not.toContainText(FOREIGN_PROMOTER_NAME);

    await createViaUi(page, MANAGER_NAME);
    await expect(page.getByTestId("promoter-added")).toContainText(MANAGER_NAME);
    await openCard(page, MANAGER_NAME);
    await editField(page, "phone", "+420 123 456 789");
    await expect(page.getByTestId("field-value-phone")).toHaveText("+420 123 456 789");
    await page.getByTestId("archive-open").click();
    await page.getByTestId("archive-confirm").click();
    await expect(page.getByTestId("promoter-status")).toContainText("is archived");
    expect((await apiFind(request, "organizer", MANAGER_NAME)).archived).toBe(true);
    await page.getByTestId("restore").click();
    await expect(page.getByTestId("promoter-status")).toContainText("is active again");
    expect((await apiFind(request, "organizer", MANAGER_NAME)).archived).toBe(false);
  });

  test("agent: the screen follows what the server allows, and the API refuses the rest", async ({ page, request }) => {
    const me = await orgMe(request, "agent");
    const canRead = me.permissions.includes("promoter.read");
    const canManage = me.permissions.includes("promoter.manage");
    const target = await apiFind(request, "org_admin", RENAMED);

    // The API itself: a write by someone without promoter.manage is refused,
    // and a promoter of another organization is not reachable by its id.
    const patch = await request.patch(`${base}/promoters/${target.id}`, {
      headers: auth("agent"),
      data: { name: `${RENAMED} by an agent` },
    });
    expect(patch.status(), "PATCH as agent").toBe(canManage ? 200 : 403);
    if (canManage) {
      // Put it back, the other tests are over but the data stays honest.
      await request.patch(`${base}/promoters/${target.id}`, { headers: auth("org_admin"), data: { name: RENAMED } });
    }
    for (const role of ["org_admin", "organizer", "agent"] as const) {
      const foreign = await request.patch(`${base}/promoters/${FOREIGN_PROMOTER_ID}`, {
        headers: auth(role),
        data: { name: "Hijacked" },
      });
      expect(foreign.status(), `PATCH foreign promoter as ${role}`).toBe(role === "agent" && !canManage ? 403 : 404);
    }

    await signInAs(page, person(env, "agent"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    const link = page.getByRole("link", { name: "Promoters" });
    if (!canRead) {
      await expect(link).toHaveCount(0);
      await page.goto(`/o/${env.org_id}/promoters`);
      await expect(page.getByTestId("promoters-no-access")).toBeVisible();
      return;
    }
    await openPromoters(page);
    await expect(page.getByTestId("promoter-list")).not.toContainText(FOREIGN_PROMOTER_NAME);
    await expect(page.getByTestId("promoter-row").filter({ hasText: RENAMED })).toBeVisible();
    if (canManage) {
      await expect(page.getByTestId("promoter-new")).toBeVisible();
    } else {
      // Read-only: no way to add, change or archive.
      await expect(page.getByTestId("promoter-new")).toHaveCount(0);
      await expect(page.getByTestId("promoters-read-only")).toBeVisible();
      await openCard(page, RENAMED);
      await expect(page.getByTestId("edit-name")).toHaveCount(0);
      await expect(page.getByTestId("archive-open")).toHaveCount(0);
      await expect(page.getByTestId("restore")).toHaveCount(0);
      await expect(page.getByTestId("page-url")).toBeVisible();
    }
    await noA11yViolations(page);
  });

  test("a promoter of another organization is not listed for anyone", async ({ request }) => {
    for (const role of ["org_admin", "organizer", "agent"] as const) {
      const me = await orgMe(request, role);
      if (!me.permissions.includes("promoter.read")) continue;
      const list = await apiList(request, role);
      expect(list.some((p) => p.id === FOREIGN_PROMOTER_ID || p.name === FOREIGN_PROMOTER_NAME), `foreign promoter in ${role}'s list`).toBe(false);
    }
  });
});
