/**
 * Twin test "the team of the organization" (task 4-5), on the three roles of
 * one organization, against a REAL arena-api. It walks the scenario of the
 * bot's Team screens (eventbot/team.go, team_invitations.go, and the e2e
 * bot_e2e_team_invitations_integration_test.go): the owner invites a person
 * through the web or the Telegram channel, sees where each invitation stands,
 * sends a Telegram letter again (too soon is refused with the minutes left),
 * revokes one, and removes a member with the role in the request body. A
 * manager and an agent have no buttons and the API refuses them.
 *
 * The truth about each person comes from GET /v1/organizations/{org_id}/me.
 * Database rows (memberships, invitations, queued letters) are read, and the
 * clock of an invitation moved, through the seed binary's `--team-op`, because
 * this process has no SQL driver. `reset` puts the fixture back before every
 * test. No worker runs in the harness: the letters stay as `worker_jobs` rows
 * (bot.invitation_email, auth.password_reset_email), which is what the test
 * checks - that the right letter was queued, not that it was delivered.
 *
 * The last owner: the server has no "last owner" rule on DELETE .../members,
 * so the web never offers Remove on your own row (the screen is only open to
 * a person who can manage the team, so the organization always keeps the owner
 * who is looking at it). The test checks that the own row has no button.
 */
import { expect, test, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();

const REMOVABLE_EMAIL = "e2e-team-removable@test.arena.local";
const REMOVABLE_USER_ID = "fe0000e5-0000-7000-8000-0000000004a1";
const inviteEmail = (name: string) => `e2e-team-invite-${name}@test.arena.local`;

const here = dirname(fileURLToPath(import.meta.url));
const appDir = resolve(here, "..", "..", "..");
const repoRoot = resolve(appDir, "..", "..");

/** The seed binary the harness built (or CI prebuilt). */
function seedBinary(): string {
  const given = process.env.ARENA_E2E_SEED_BIN;
  if (given !== undefined && given !== "") return given;
  const built = join(appDir, ".live", process.platform === "win32" ? "arena-e2e-seed.exe" : "arena-e2e-seed");
  if (!existsSync(built)) throw new Error(`the seed binary ${built} is missing: run through the live harness`);
  return built;
}

function seed(op: "reset" | "age" | "expire" | "inspect", email?: string): Record<string, unknown> {
  const args = ["--team-op", op];
  if (email !== undefined) args.push("--team-email", email);
  const r = spawnSync(seedBinary(), args, { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed ${args.join(" ")} failed: ${r.stderr}`);
  return JSON.parse(r.stdout.trim()) as Record<string, unknown>;
}

interface Inspect {
  membership_roles: string[];
  preferred_locale: string;
  invitations: number;
  revoked_invitations: number;
  jobs: string[];
}
const rows = (email: string) => seed("inspect", email) as unknown as Inspect;

const orgPath = (rest: string) => `${env.api_base_url}/v1/organizations/${env.org_id}${rest}`;
const bearer = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(orgPath("/me"), { headers: bearer(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

interface TeamMember {
  user_id: string;
  email: string;
  membership_role: string;
  invitation_id?: string;
  invitation_channel?: string;
  invitation_state?: string;
}
async function team(request: APIRequestContext, role: Role): Promise<TeamMember[]> {
  const res = await request.get(orgPath("/bot-team"), { headers: bearer(role) });
  expect(res.status(), `GET bot-team as ${role}`).toBe(200);
  return ((await res.json()) as { members: TeamMember[] }).members;
}

function trackApi(page: Page): string[] {
  const calls: string[] = [];
  page.on("request", (req) => {
    if (req.url().startsWith(env.api_base_url)) calls.push(`${req.method()} ${new URL(req.url()).pathname}`);
  });
  return calls;
}

const row = (page: Page, email: string): Locator =>
  page.locator(`[data-testid="team-member"][data-email="${email}"]`);

async function openTeam(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/team`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
}

type Channel = "web" | "telegram";
async function invite(page: Page, email: string, opts: { role?: "Manager" | "Owner"; channel: Channel; locale?: string }) {
  await page.getByTestId("team-invite-email").fill(email);
  await page.getByTestId("team-invite-role").selectOption({ label: opts.role ?? "Manager" });
  await page.getByTestId("team-invite-channel").selectOption(opts.channel);
  if (opts.locale !== undefined) await page.getByTestId("team-invite-locale").selectOption(opts.locale);
  await page.getByTestId("team-invite-submit").click();
}

test.beforeEach(() => {
  seed("reset");
});

test.describe("the team (live)", () => {
  test("owner sees the team: own row has no Remove, a colleague has, the form is there", async ({ page, request }) => {
    const me = await orgMe(request, "org_admin");
    for (const p of ["membership.read", "membership.grant", "membership.revoke"]) {
      expect(me.permissions.includes(p), `${p} per the server`).toBe(true);
    }
    await openTeam(page, "org_admin");
    await expect(page.getByRole("link", { name: "Team", exact: true })).toBeVisible();
    const own = row(page, person(env, "org_admin").email);
    await expect(own).toBeVisible();
    await expect(own.getByTestId("team-role")).toHaveText("Owner");
    await expect(own).toContainText("(you)");
    await expect(own.getByTestId("team-remove"), "no Remove on your own row").toHaveCount(0);
    const colleague = row(page, REMOVABLE_EMAIL);
    await expect(colleague.getByTestId("team-role")).toHaveText("Manager");
    await expect(colleague.getByTestId("team-remove")).toBeVisible();
    // A member added another way has no invitation: no resend, no revoke.
    await expect(colleague.getByTestId("team-resend")).toHaveCount(0);
    await expect(colleague.getByTestId("team-revoke")).toHaveCount(0);
    await expect(page.getByTestId("team-invite-form")).toBeVisible();
    await noA11yViolations(page);
  });

  test("owner invites for the WEB: the membership exists, the letter is queued in the chosen language, nothing to resend", async ({ page, request }) => {
    const email = inviteEmail("web");
    const calls = trackApi(page);
    await openTeam(page, "org_admin");

    // Text that is not an address sends nothing.
    await invite(page, "not-an-address", { channel: "web" });
    await expect(page.getByTestId("team-invite-email-note")).toBeVisible();
    expect(calls.filter((c) => c.startsWith("POST") && c.endsWith("/bot-invitations")), "no request for a bad address").toEqual([]);

    await invite(page, email, { channel: "web", locale: "es" });
    const notice = page.getByTestId("team-notice");
    await expect(notice).toContainText(`The invitation is sent to ${email}.`);
    await expect(notice).toContainText("link to this event center");
    const added = row(page, email);
    await expect(added.getByTestId("team-role")).toHaveText("Manager");
    await expect(added.getByTestId("team-state")).toHaveText("Waiting");
    await expect(added.getByTestId("team-channel")).toHaveText("Web event center");
    // A web invitation has no letter to send again, and nothing to revoke.
    await expect(added.getByTestId("team-resend")).toHaveCount(0);
    await expect(added.getByTestId("team-revoke")).toHaveCount(0);
    await expect(added.getByTestId("team-remove")).toBeVisible();

    const db = rows(email);
    expect(db.membership_roles).toEqual(["organizer"]);
    expect(db.preferred_locale, "the language of the letter").toBe("es");
    expect(db.jobs, "the setup letter is queued").toEqual(["auth.password_reset_email"]);
    expect(db.invitations).toBe(1);

    // The server agrees: the invitation is a web one and cannot be resent.
    const member = (await team(request, "org_admin")).find((m) => m.email === email);
    expect(member?.invitation_channel).toBe("web");
    expect(member?.invitation_state).toBe("waiting");
    const again = await request.post(orgPath(`/bot-invitations/${member?.invitation_id}/resend`), { headers: bearer("org_admin"), data: {} });
    expect(again.status()).toBe(409);
    expect(((await again.json()) as { error: { code: string } }).error.code).toBe("bot.invitation_channel_unsupported");
    // A channel that does not exist is refused.
    const bad = await request.post(orgPath("/bot-invitations"), {
      headers: bearer("org_admin"),
      data: { email: inviteEmail("badchannel"), role: "manager", channel: "pigeon" },
    });
    expect(bad.status()).toBe(400);
    expect(((await bad.json()) as { error: { code: string } }).error.code).toBe("bot.invalid_channel");
    expect(rows(inviteEmail("badchannel")).membership_roles, "a refused invitation adds nobody").toEqual([]);
    await noA11yViolations(page);
  });

  test("owner invites for TELEGRAM as an owner, resends (too soon, then allowed), revokes", async ({ page }) => {
    const email = inviteEmail("tg");
    await openTeam(page, "org_admin");
    await invite(page, email, { role: "Owner", channel: "telegram" });
    await expect(page.getByTestId("team-notice")).toContainText("Telegram bot");
    const added = row(page, email);
    await expect(added.getByTestId("team-role")).toHaveText("Owner");
    await expect(added.getByTestId("team-state")).toHaveText("Waiting");
    await expect(added.getByTestId("team-channel")).toHaveText("Telegram bot");
    await expect(added.getByTestId("team-resend")).toBeVisible();
    await expect(added.getByTestId("team-revoke")).toBeVisible();
    expect(rows(email).membership_roles).toEqual(["org_admin"]);
    expect(rows(email).jobs).toEqual(["bot.invitation_email"]);

    // Right after the invitation the letter cannot go out again: the screen says how long to wait.
    await added.getByTestId("team-resend").click();
    const notice = page.getByTestId("team-notice");
    await expect(notice).toContainText("The letter went out a moment ago.");
    await expect(notice).toContainText(/again in \d+ min/);
    expect(rows(email).jobs, "a refused resend queues nothing").toEqual(["bot.invitation_email"]);

    // Ten minutes later it goes out, and the next press is too soon again.
    seed("age", email);
    await added.getByTestId("team-resend").click();
    await expect(notice).toContainText(`The letter to ${email} is sent again.`);
    expect(rows(email).jobs).toEqual(["bot.invitation_email", "bot.invitation_email"]);
    await added.getByTestId("team-resend").click();
    await expect(notice).toContainText("The letter went out a moment ago.");
    expect(rows(email).jobs).toHaveLength(2);

    // Revoke: the question first, a refusal changes nothing.
    await added.getByTestId("team-revoke").click();
    const dialog = page.getByTestId("team-revoke-dialog");
    await expect(dialog).toBeVisible();
    await expect(dialog.getByTestId("team-revoke-dialog-subject")).toHaveText(email);
    await noA11yViolations(page);
    await dialog.getByTestId("team-revoke-dialog-cancel").click();
    await expect(dialog).toHaveCount(0);
    expect(rows(email).revoked_invitations).toBe(0);
    await added.getByTestId("team-revoke").click();
    await page.getByTestId("team-revoke-dialog-submit").click();
    await expect(page.getByTestId("team-notice")).toContainText(`The invitation for ${email} is revoked.`);
    await expect(page.getByTestId("team-notice")).toContainText("never joined");
    await expect(row(page, email)).toHaveCount(0);
    const db = rows(email);
    expect(db.revoked_invitations).toBe(1);
    expect(db.membership_roles, "the person never joined, so the membership went too").toEqual([]);
  });

  test("an expired Telegram invitation says so and can be sent again", async ({ page }) => {
    const email = inviteEmail("expired");
    await openTeam(page, "org_admin");
    await invite(page, email, { channel: "telegram" });
    await expect(row(page, email).getByTestId("team-state")).toHaveText("Waiting");
    await expect(row(page, email)).toContainText("The link works until");
    seed("expire", email);
    await page.reload();
    const added = row(page, email);
    await expect(added.getByTestId("team-state")).toHaveText("Invitation expired");
    await expect(added).not.toContainText("The link works until");
    await expect(added.getByTestId("team-resend")).toBeVisible();
    await expect(added.getByTestId("team-revoke")).toBeVisible();
    await added.getByTestId("team-resend").click();
    await expect(page.getByTestId("team-notice")).toContainText(`The letter to ${email} is sent again.`);
    await expect(row(page, email).getByTestId("team-state")).toHaveText("Waiting");
  });

  test("owner removes a member: a question first, and the request names the role in its body", async ({ page, request }) => {
    const bodies: unknown[] = [];
    page.on("request", (req) => {
      if (req.method() === "DELETE" && req.url().includes("/members/")) bodies.push(req.postDataJSON());
    });
    await openTeam(page, "org_admin");
    const colleague = row(page, REMOVABLE_EMAIL);
    await colleague.getByTestId("team-remove").click();
    const dialog = page.getByTestId("team-remove-dialog");
    await expect(dialog).toBeVisible();
    await expect(dialog.getByTestId("team-remove-dialog-subject")).toHaveText(REMOVABLE_EMAIL);
    await expect(dialog).toContainText("cannot open this organization any more");
    // Focus starts on Cancel: Enter must not remove somebody by habit.
    await expect(dialog.getByTestId("team-remove-dialog-cancel")).toBeFocused();
    await noA11yViolations(page);
    await dialog.getByTestId("team-remove-dialog-cancel").click();
    await expect(dialog).toHaveCount(0);
    expect(bodies, "nothing was sent").toEqual([]);
    expect(rows(REMOVABLE_EMAIL).membership_roles).toEqual(["organizer"]);

    await colleague.getByTestId("team-remove").click();
    await page.getByTestId("team-remove-dialog-submit").click();
    await expect(page.getByTestId("team-notice")).toContainText(`${REMOVABLE_EMAIL} is off the team.`);
    await expect(row(page, REMOVABLE_EMAIL)).toHaveCount(0);
    expect(bodies, "the DELETE carries the member's role").toEqual([{ role: "organizer" }]);
    expect(rows(REMOVABLE_EMAIL).membership_roles).toEqual([]);

    // The route needs the role: an empty body is refused with its own code (AGENTS.md).
    const empty = await request.delete(orgPath(`/members/${REMOVABLE_USER_ID}`), { headers: bearer("org_admin") });
    expect(empty.status()).toBe(400);
    expect(((await empty.json()) as { error: { code: string } }).error.code).toBe("membership.empty_body");
    // A second removal says the person is gone.
    const gone = await request.delete(orgPath(`/members/${REMOVABLE_USER_ID}`), { headers: bearer("org_admin"), data: { role: "organizer" } });
    expect(gone.status()).toBe(404);
    expect(((await gone.json()) as { error: { code: string } }).error.code).toBe("membership.not_found");
  });

  test("manager reads the team but has no buttons, and the API refuses the manager", async ({ page, request }) => {
    const me = await orgMe(request, "organizer");
    expect(me.permissions.includes("membership.read"), "membership.read per the server").toBe(true);
    expect(me.permissions.includes("membership.grant"), "membership.grant per the server").toBe(false);
    expect(me.permissions.includes("membership.revoke"), "membership.revoke per the server").toBe(false);

    await openTeam(page, "organizer");
    await expect(row(page, REMOVABLE_EMAIL)).toBeVisible();
    await expect(page.getByTestId("team-invite-form")).toHaveCount(0);
    await expect(page.getByTestId("team-read-only")).toBeVisible();
    for (const id of ["team-remove", "team-resend", "team-revoke"]) await expect(page.getByTestId(id)).toHaveCount(0);

    const inviteRes = await request.post(orgPath("/bot-invitations"), {
      headers: bearer("organizer"),
      data: { email: inviteEmail("by-manager"), role: "manager", channel: "web" },
    });
    expect(inviteRes.status(), "manager POST bot-invitations").toBe(403);
    expect(rows(inviteEmail("by-manager")).membership_roles, "a refused invitation adds nobody").toEqual([]);
    const removeRes = await request.delete(orgPath(`/members/${REMOVABLE_USER_ID}`), { headers: bearer("organizer"), data: { role: "organizer" } });
    expect(removeRes.status(), "manager DELETE member").toBe(403);
    expect(rows(REMOVABLE_EMAIL).membership_roles).toEqual(["organizer"]);
  });

  test("agent has no Team link and no list, and the API refuses the agent", async ({ page, request }) => {
    const me = await orgMe(request, "agent");
    expect(me.role).toBe("agent");
    expect(me.permissions.includes("membership.read"), "membership.read per the server").toBe(false);

    const calls = trackApi(page);
    await signInAs(page, person(env, "agent"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await expect(page.getByRole("link", { name: "Team", exact: true })).toHaveCount(0);
    await page.goto(`/o/${env.org_id}/team`);
    await expect(page.getByTestId("team-no-access")).toBeVisible();
    await expect(page.getByTestId("team-list")).toHaveCount(0);
    await expect(page.getByTestId("team-invite-form")).toHaveCount(0);
    expect(calls.filter((c) => c.endsWith("/bot-team")), "the list is not even asked for").toEqual([]);
    await noA11yViolations(page);

    expect((await request.get(orgPath("/bot-team"), { headers: bearer("agent") })).status(), "agent GET bot-team").toBe(403);
    const inviteRes = await request.post(orgPath("/bot-invitations"), {
      headers: bearer("agent"),
      data: { email: inviteEmail("by-agent"), role: "manager", channel: "telegram" },
    });
    expect(inviteRes.status(), "agent POST bot-invitations").toBe(403);
    const removeRes = await request.delete(orgPath(`/members/${REMOVABLE_USER_ID}`), { headers: bearer("agent"), data: { role: "organizer" } });
    expect(removeRes.status(), "agent DELETE member").toBe(403);
    expect(rows(inviteEmail("by-agent")).membership_roles).toEqual([]);
    expect(rows(REMOVABLE_EMAIL).membership_roles).toEqual(["organizer"]);
  });
});
