/**
 * Twin test "Connect Telegram to the organization" (finding F-007 of the acceptance run 2026-11-09), against a REAL
 * arena-api. It walks the path of the bot's e2e (httpserver/bot_e2e_telegram_connect_integration_test.go): the web page
 * makes a one-time t.me link with its QR code, the bot (played here with the harness's BOT_SERVICE_TOKEN) reads it
 * with preview, the person presses "Connect" and the bot calls accept.
 *
 * What it proves:
 *  - the owner, the manager and the agent all see "Connect Telegram" on "Organization data" (it is their own Telegram,
 *    not an owner's setting);
 *  - the page shows the link and a QR picture, and the link carries a one-time code (start=con_...);
 *  - nothing is connected by the preview; the accept links the account and the page says "Done" on its own;
 *  - a used link no longer works (404), and a new link replaces the old one.
 */
import { randomInt } from "node:crypto";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, person, signInAs, type Role } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const svc = { Authorization: `Bearer ${env.bot_service_token}` };

/** A Telegram account nobody linked yet: one per run, so the scratch base can be reused. */
function freshTelegramId(): number {
  return 9_200_000_000 + randomInt(99_999_999);
}

async function openOrgData(page: Page, role: Role) {
  await signInAs(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
  await page.goto(`/o/${env.org_id}/org-data`);
}

/** Presses "Connect (another) Telegram" and answers the one-time code the link carries. */
async function makeLink(page: Page): Promise<string> {
  const block = page.getByTestId("connect-telegram");
  await expect(block).toBeVisible();
  await block.getByTestId("connect-telegram-start").click();
  await expect(block.getByTestId("connect-telegram-qr")).toBeVisible();
  const href = await block.getByTestId("connect-telegram-open").getAttribute("href");
  expect(href).toMatch(/^https:\/\/t\.me\/ArenaE2EEventsBot\?start=con_[A-Za-z0-9_-]+$/);
  return (href as string).split("start=con_")[1];
}

async function botCall(request: APIRequestContext, route: "preview" | "accept", code: string, telegramUserId: number) {
  return request.post(`${env.api_base_url}/v1/bot/telegram-connect/${route}`, {
    headers: svc,
    data: { code, telegram_user_id: telegramUserId, telegram_username: `e2e_${telegramUserId}`, locale: "en" },
  });
}

for (const role of ["org_admin", "organizer", "agent"] as const) {
  test(`${role}: the link connects a new Telegram after the press, and works once`, async ({ page, request }) => {
    await openOrgData(page, role);
    const code = await makeLink(page);
    const tg = freshTelegramId();

    // The bot opens the link: a question, nothing connected yet.
    const preview = await botCall(request, "preview", code, tg);
    expect(preview.status(), "preview").toBe(200);
    const p = (await preview.json()) as { org_id: string; org_name: string; email_masked: string; state: string };
    expect(p.org_id).toBe(env.org_id);
    expect(p.state).toBe("new");
    expect(p.email_masked).toContain("•");
    await expect(page.getByTestId("connect-telegram-done")).toHaveCount(0);

    // The press of "Connect": the account is linked and the page says so by itself.
    const accept = await botCall(request, "accept", code, tg);
    expect(accept.status(), "accept").toBe(200);
    const a = (await accept.json()) as { org_id: string; already_linked: boolean };
    expect(a.org_id).toBe(env.org_id);
    expect(a.already_linked).toBe(false);
    await expect(page.getByTestId("connect-telegram-done")).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId("connect-telegram-linked")).toContainText(`@e2e_${tg}`);

    // The same link a second time: it no longer works.
    const again = await botCall(request, "accept", code, freshTelegramId());
    expect(again.status(), "second accept").toBe(404);
  });
}

test("a new link replaces the one made before", async ({ page, request }) => {
  await openOrgData(page, "org_admin");
  const first = await makeLink(page);
  await page.getByTestId("connect-telegram-renew").click();
  await expect
    .poll(async () => (await page.getByTestId("connect-telegram-open").getAttribute("href"))?.split("start=con_")[1])
    .not.toBe(first);
  const old = await botCall(request, "preview", first, freshTelegramId());
  expect(old.status(), "the replaced link").toBe(404);
});
