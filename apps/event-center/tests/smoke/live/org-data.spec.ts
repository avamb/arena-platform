/**
 * Twin test "Organization data" (task 4-7), on the three roles of one
 * organization, against a REAL arena-api. It walks the scenario of the bot's
 * e2e (bot_e2e_org_data_integration_test.go, `og:`): the owner fills the
 * legal data one field at a time, a malformed tax number is refused in plain
 * words and nothing is stored, the page's public e-mail (with the personal
 * mailbox hint) makes the profile complete and the seller block shown,
 * clearing a required field makes it incomplete again, the Telegram switch
 * flips, and the operator's "hidden" state is shown but never set from here.
 * The manager and the agent are told it is the owner's, and the API agrees.
 *
 * The truth about each person comes from GET /v1/organizations/{org_id}/me.
 * The database rows are read through the seed binary's `--org-data-op
 * inspect` (this process has no SQL driver); `reset` puts the fixture back
 * before every test.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();
const CHANNEL = "E2E Org Data Page";

const here = dirname(fileURLToPath(import.meta.url));
const appDir = resolve(here, "..", "..", "..");
const repoRoot = resolve(appDir, "..", "..");

function seedBinary(): string {
  const given = process.env.ARENA_E2E_SEED_BIN;
  if (given !== undefined && given !== "") return given;
  const built = join(appDir, ".live", process.platform === "win32" ? "arena-e2e-seed.exe" : "arena-e2e-seed");
  if (!existsSync(built)) throw new Error(`the seed binary ${built} is missing: run through the live harness`);
  return built;
}

interface Rows {
  legal_name: string | null;
  legal_form: string | null;
  tax_id: string | null;
  tax_id_scheme: string | null;
  legal_address_city: string | null;
  contact_email: string | null;
  contact_form_to_telegram: boolean;
  public_email: string | null;
  public_phone: string | null;
}

function seed(op: "reset" | "inspect" | "hide"): Record<string, unknown> {
  const args = ["--org-data-op", op];
  const r = spawnSync(seedBinary(), args, { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed ${args.join(" ")} failed: ${r.stderr}`);
  return JSON.parse(r.stdout.trim()) as Record<string, unknown>;
}

const rows = (): Rows => seed("inspect") as unknown as Rows;

const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/me`, { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

interface ProfileChannel {
  name: string;
  complete: boolean;
  seller_shown: boolean;
  seller_hidden: boolean;
  missing: string[];
}
interface Profile {
  complete: boolean;
  seller_shown: boolean;
  missing: string[];
  contact_form_to_telegram?: boolean;
  channels: ProfileChannel[];
}

async function profile(request: APIRequestContext, role: Role): Promise<Profile> {
  const res = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/seller-profile`, { headers: auth(role) });
  expect(res.status(), `GET seller-profile as ${role}`).toBe(200);
  return ((await res.json()) as { seller_profile: Profile }).seller_profile;
}

function trackApi(page: Page): string[] {
  const calls: string[] = [];
  page.on("request", (req) => {
    if (req.url().startsWith(env.api_base_url)) calls.push(`${req.method()} ${new URL(req.url()).pathname}`);
  });
  return calls;
}

const orgPatch = `PATCH /v1/organizations/${env.org_id}`;

async function openScreen(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
}

const card = (page: Page) => page.locator(`[data-testid="seller-channel"][data-channel-name="${CHANNEL}"]`);

async function fill(page: Page, testId: string, value: string) {
  await page.getByTestId(testId).fill(value);
}

test.beforeEach(() => {
  seed("reset");
});

test.describe("organization data (live)", () => {
  test("owner: fills the legal data, a bad tax number stores nothing, the page contact completes the profile", async ({ page, request }) => {
    const me = await orgMe(request, "org_admin");
    expect(me.permissions.includes("org.update"), "org.update per the server").toBe(true);
    expect(rows().legal_name).toBeNull();

    const calls = trackApi(page);
    await openScreen(page, "org_admin");
    await page.getByRole("link", { name: "Organization data" }).click();
    await expect(page.getByTestId("org-data")).toBeVisible();
    await expect(page.getByTestId("org-data-form")).toBeVisible();
    await expect(card(page)).toBeVisible();

    // Nothing is filled in: the screen says exactly what is missing.
    const status = page.getByTestId("org-data-status");
    await expect(status).toHaveAttribute("data-state", "incomplete");
    const missing = page.getByTestId("org-data-missing");
    for (const word of ["legal name", "tax number", "street and number", "city", "postal code"]) {
      await expect(missing).toContainText(word);
    }
    // The owner holds payment_config.read and the fixture has no payment config.
    if (me.permissions.includes("payment_config.read")) await expect(page.getByTestId("org-data-payments")).toBeVisible();
    await noA11yViolations(page);

    // Legal name and form.
    await fill(page, "field-legal-name", "Teatro Bot S.L.");
    await fill(page, "field-legal-form", "S.L.");

    // A bare NIF under "EU VAT" is refused in words and nothing is sent.
    await page.getByTestId("field-tax-scheme").selectOption("eu_vat");
    await fill(page, "field-tax-id", "B12345678");
    await page.getByTestId("org-data-save").click();
    await expect(page.getByTestId("org-data-problems")).toContainText("EU VAT number");
    await expect(page.getByTestId("field-tax-id-error")).toBeVisible();
    expect(calls.filter((c) => c === orgPatch), "no PATCH for a refused number").toEqual([]);
    expect(rows().tax_id).toBeNull();
    expect(rows().legal_name).toBeNull();

    // The API says the same when a stale client sends it anyway.
    const raw = await request.patch(`${env.api_base_url}/v1/organizations/${env.org_id}`, {
      headers: auth("org_admin"),
      data: { tax_id: "B12345678", tax_id_scheme: "eu_vat" },
    });
    expect(raw.status()).toBe(400);
    expect(((await raw.json()) as { error: { code: string } }).error.code).toBe("org.invalid_tax_id");
    expect(rows().tax_id).toBeNull();

    // A number typed the way people type it is folded and saved.
    await fill(page, "field-tax-id", "es b-1234 5678");
    await fill(page, "field-line1", "Calle Mayor 1");
    await fill(page, "field-city", "Madrid");
    await fill(page, "field-postal-code", "28013");
    await fill(page, "field-country", "es");
    await fill(page, "field-contact-email", "owner-private@example.test");
    await page.getByTestId("org-data-save").click();
    await expect(page.getByTestId("org-data-saved")).toBeVisible();
    expect(calls.filter((c) => c === orgPatch)).toHaveLength(1);
    const saved = rows();
    expect(saved).toMatchObject({
      legal_name: "Teatro Bot S.L.",
      legal_form: "S.L.",
      tax_id: "ESB12345678",
      tax_id_scheme: "eu_vat",
      legal_address_city: "Madrid",
      contact_email: "owner-private@example.test",
    });

    // The organization's data is complete, the page still lacks its public e-mail.
    await expect(status).toHaveAttribute("data-state", "legal");
    await expect(card(page)).toHaveAttribute("data-seller-state", "incomplete");
    await expect(page.getByTestId("seller-channel-missing").first()).toContainText("public e-mail");
    let p = await profile(request, "org_admin");
    expect(p.missing).toEqual([]);
    expect(p.seller_shown).toBe(false);

    // The page's public e-mail: a malformed one is refused before any call.
    const channelCard = card(page);
    await channelCard.getByTestId("seller-email").fill("not-an-email");
    await channelCard.getByTestId("seller-save").click();
    await expect(channelCard.getByTestId("seller-email-error")).toBeVisible();
    expect(calls.filter((c) => c.startsWith("PATCH") && c.endsWith("/seller-display"))).toEqual([]);
    expect(rows().public_email).toBeNull();

    // A personal mailbox is saved lower-cased, with a hint and no refusal.
    await channelCard.getByTestId("seller-email").fill("Teatro.Tickets@Gmail.com");
    await channelCard.getByTestId("seller-phone").fill("+34 600 111 222");
    await channelCard.getByTestId("seller-save").click();
    await expect(channelCard.getByTestId("seller-saved")).toBeVisible();
    await expect(channelCard.getByTestId("seller-personal-hint")).toBeVisible();
    expect(rows()).toMatchObject({ public_email: "teatro.tickets@gmail.com", public_phone: "+34 600 111 222" });
    const displayCalls = calls.filter((c) => c.startsWith("PATCH") && c.endsWith("/seller-display"));
    expect(displayCalls).toHaveLength(1);

    // The server agrees: complete and shown, with no "show" button pressed.
    await expect(channelCard).toHaveAttribute("data-seller-state", "shown");
    p = await profile(request, "org_admin");
    expect(p.complete).toBe(true);
    expect(p.seller_shown).toBe(true);
    const mine = p.channels.find((c) => c.name === CHANNEL);
    expect(mine).toMatchObject({ complete: true, seller_shown: true, seller_hidden: false, missing: [] });
    await expect(status).toHaveAttribute("data-seller-shown", "true");
    await noA11yViolations(page);

    // Clearing a required field takes the block off the page again.
    await fill(page, "field-tax-id", "");
    await page.getByTestId("field-tax-scheme").selectOption("");
    await page.getByTestId("org-data-save").click();
    await expect(page.getByTestId("org-data-saved")).toBeVisible();
    expect(rows().tax_id).toBeNull();
    await expect(status).toHaveAttribute("data-state", "incomplete");
    await expect(page.getByTestId("org-data-missing")).toContainText("tax number");
    await expect(channelCard).toHaveAttribute("data-seller-state", "incomplete");
    p = await profile(request, "org_admin");
    expect(p.complete).toBe(false);
    expect(p.seller_shown).toBe(false);
    expect(p.missing).toContain("tax_id");
  });

  test("owner: the Telegram switch flips and is stored", async ({ page }) => {
    expect(rows().contact_form_to_telegram).toBe(true);
    await openScreen(page, "org_admin");
    await page.goto(`/o/${env.org_id}/org-data`);
    const box = page.getByTestId("org-data-tg");
    await expect(box).toHaveAttribute("data-on", "true");
    await page.getByTestId("org-data-tg-toggle").click();
    await expect(box).toHaveAttribute("data-on", "false");
    await expect(page.getByTestId("org-data-tg-state")).toContainText("only to the e-mail");
    expect(rows().contact_form_to_telegram).toBe(false);
    await page.getByTestId("org-data-tg-toggle").click();
    await expect(box).toHaveAttribute("data-on", "true");
    expect(rows().contact_form_to_telegram).toBe(true);
  });

  test("owner: the operator's hidden state is shown, and the owner cannot change it", async ({ page, request }) => {
    // The page is complete first, then the operator hides it.
    const res = await request.patch(`${env.api_base_url}/v1/organizations/${env.org_id}`, {
      headers: auth("org_admin"),
      data: {
        legal_name: "Teatro Bot S.L.",
        tax_id: "B12345678",
        tax_id_scheme: "other",
        legal_address_line1: "Calle Mayor 1",
        legal_address_city: "Madrid",
        legal_address_postal_code: "28013",
      },
    });
    expect(res.status()).toBe(200);
    await openScreen(page, "org_admin");
    await page.goto(`/o/${env.org_id}/org-data`);
    await card(page).getByTestId("seller-email").fill("tickets@teatro.example");
    await card(page).getByTestId("seller-save").click();
    await expect(card(page)).toHaveAttribute("data-seller-state", "shown");

    seed("hide");
    await page.reload();
    await expect(card(page)).toHaveAttribute("data-seller-state", "operator_hidden");
    await expect(card(page).getByTestId("seller-state")).toContainText("platform operator hid");
    // No control of the screen sends seller_hidden; the API refuses the owner too.
    const calls = trackApi(page);
    await card(page).getByTestId("seller-phone").fill("+34 600 111 222");
    await card(page).getByTestId("seller-save").click();
    await expect(card(page).getByTestId("seller-saved")).toBeVisible();
    await expect(card(page)).toHaveAttribute("data-seller-state", "operator_hidden");
    expect(calls.filter((c) => c.endsWith("/seller-display"))).toHaveLength(1);
    const channel = (await profile(request, "org_admin")).channels.find((c) => c.name === CHANNEL);
    expect(channel?.seller_hidden).toBe(true);
  });

  for (const role of ["organizer", "agent"] as const) {
    test(`${role === "organizer" ? "manager" : "agent"} (${role}): the screen explains it is the owner's and the API refuses`, async ({ page, request }) => {
      const me = await orgMe(request, role);
      expect(me.permissions.includes("org.update"), "org.update per the server").toBe(false);
      const get = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/seller-profile`, { headers: auth(role) });
      expect(get.status(), "GET seller-profile").toBe(403);
      const patch = await request.patch(`${env.api_base_url}/v1/organizations/${env.org_id}`, {
        headers: auth(role),
        data: { legal_name: "Hijacked S.L." },
      });
      expect(patch.status(), "PATCH organization").toBe(403);
      expect(rows().legal_name).toBeNull();

      const calls = trackApi(page);
      await openScreen(page, role);
      await expect(page.getByRole("link", { name: "Organization data" })).toHaveCount(0);
      await page.goto(`/o/${env.org_id}/org-data`);
      await expect(page.getByTestId("org-data-owner-only")).toContainText("Only the owner");
      await expect(page.getByTestId("org-data-form")).toHaveCount(0);
      await expect(page.getByTestId("seller-channel")).toHaveCount(0);
      expect(calls.filter((c) => c.includes("/seller-profile") || c === orgPatch || c.endsWith("/payment-configs")), "no data call").toEqual([]);
      await noA11yViolations(page);
    });
  }
});
