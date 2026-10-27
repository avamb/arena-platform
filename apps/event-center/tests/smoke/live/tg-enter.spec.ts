/**
 * Twin test "the Telegram mini app entry /tg" (S3b + S3c), against a REAL arena-api. The bot opens its web_app button at
 * /tg?next=<path>; Telegram gives the page signed launch data (Telegram.WebApp.initData). The twin plays Telegram: it signs
 * the launch data with the harness's TELEGRAM_MINIAPP_SECRET_KEY (the key Telegram derives from the bot token) for a
 * Telegram account that arena-e2e-seed linked to a person, puts it into a Telegram stand-in, and opens the page.
 *
 * It proves, through the real API and the real page:
 *  - good launch data signs in and opens the path of `next` (the owner and the manager), and the session works;
 *  - the signed `start_param` of the launch data wins over the `next` of the address;
 *  - a path outside the event center in `next` is dropped, the person lands on the organizations;
 *  - old launch data, a forged signature and a Telegram account nobody linked each say in plain words what to do.
 *
 * The route belongs to PR S3b. Against an API that predates it (404) the whole file is skipped with a reason, so the
 * file can stand in master before and after that PR without a red run.
 */
import { createHmac } from "node:crypto";
import { expect, test, type Page } from "@playwright/test";
import { liveEnv, person, type Role } from "./liveEnv";

const env = liveEnv();
const eventPath = `/o/${env.org_id}/events/${env.event_id}`;

/** Launch data as Telegram builds it: URL-encoded fields plus hash = HMAC(secret key, sorted "name=value" lines). */
function initData(telegramUserId: number, opts: { ageSeconds?: number; startParam?: string; forge?: boolean } = {}): string {
  const fields: Record<string, string> = {
    query_id: "AAH-twin-test",
    user: JSON.stringify({ id: telegramUserId, first_name: "Twin", language_code: "en" }),
    auth_date: String(Math.floor(Date.now() / 1000) - (opts.ageSeconds ?? 5)),
  };
  if (opts.startParam !== undefined) fields.start_param = opts.startParam;
  const check = Object.keys(fields)
    .sort()
    .map((k) => `${k}=${fields[k]}`)
    .join("\n");
  const key = Buffer.from(env.telegram_miniapp_secret_key, "hex");
  let hash = createHmac("sha256", key).update(check).digest("hex");
  if (opts.forge) hash = hash.replace(/^./, hash[0] === "0" ? "1" : "0");
  return new URLSearchParams({ ...fields, hash }).toString();
}

async function telegramStub(page: Page, data: string) {
  await page.addInitScript((d) => {
    const w = window as unknown as Record<string, unknown>;
    w.__tg = { backShown: 0 };
    w.Telegram = {
      WebApp: {
        initData: d,
        platform: "android",
        ready: () => undefined,
        expand: () => undefined,
        close: () => undefined,
        BackButton: {
          show: () => ((w.__tg as { backShown: number }).backShown += 1),
          hide: () => undefined,
          onClick: () => undefined,
          offClick: () => undefined,
        },
      },
    };
  }, data);
}

const base64url = (s: string) => Buffer.from(s, "utf8").toString("base64url");

test.beforeAll(async ({ playwright }) => {
  const api = await playwright.request.newContext();
  const res = await api.post(`${env.api_base_url}/v1/auth/telegram-miniapp`, { data: { init_data: "x" } });
  await api.dispose();
  test.skip(res.status() === 404, "arena-api under test predates the Telegram mini app sign-in (S3b)");
});

test.describe("the mini app entry /tg (live)", () => {
  for (const role of ["org_admin", "organizer"] as Role[]) {
    test(`${role}: good launch data signs in and opens the path of next`, async ({ page }) => {
      const p = person(env, role);
      await telegramStub(page, initData(p.telegram_user_id));
      const exchange = page.waitForResponse((r) => r.url().endsWith("/v1/auth/telegram-miniapp"));
      await page.goto(`/tg?next=${encodeURIComponent(eventPath)}`);
      expect((await exchange).status()).toBe(200);
      await expect(page).toHaveURL(new RegExp(`${eventPath}$`));
      await expect(page.getByRole("heading", { name: env.event_name })).toBeVisible();
    });
  }

  test("the signed start_param wins over the next of the address", async ({ page }) => {
    const p = person(env, "org_admin");
    const signed = `/o/${env.org_id}/events`;
    await telegramStub(page, initData(p.telegram_user_id, { startParam: base64url(signed) }));
    await page.goto(`/tg?next=${encodeURIComponent(eventPath)}`);
    await expect(page).toHaveURL(new RegExp(`${signed}$`));
  });

  test("a path outside the event center in next is dropped, the person lands on the organizations", async ({ page }) => {
    const p = person(env, "org_admin");
    await telegramStub(page, initData(p.telegram_user_id));
    const exchange = page.waitForRequest((r) => r.url().endsWith("/v1/auth/telegram-miniapp"));
    await page.goto(`/tg?next=${encodeURIComponent("https://evil.example/o/x")}`);
    expect((await exchange).postDataJSON()).not.toHaveProperty("next");
    await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
    expect(page.url()).not.toContain("evil.example");
  });

  test("old launch data says the window was opened a while ago", async ({ page }) => {
    const p = person(env, "org_admin");
    await telegramStub(page, initData(p.telegram_user_id, { ageSeconds: 3600 }));
    await page.goto(`/tg?next=${encodeURIComponent(eventPath)}`);
    await expect(page.getByTestId("tg-enter-error")).toContainText("opened a while ago");
    expect(new URL(page.url()).pathname).toBe("/tg");
  });

  test("a forged signature says the sign-in could not be checked, with no technical word", async ({ page }) => {
    const p = person(env, "org_admin");
    await telegramStub(page, initData(p.telegram_user_id, { forge: true }));
    await page.goto(`/tg?next=${encodeURIComponent(eventPath)}`);
    const alert = page.getByTestId("tg-enter-error");
    await expect(alert).toContainText("could not check your Telegram sign-in");
    await expect(alert).not.toContainText(/hash|signature|auth\./i);
  });

  test("a Telegram account nobody linked says to open the bot and accept the invitation", async ({ page }) => {
    await telegramStub(page, initData(9_999_999_001));
    await page.goto(`/tg?next=${encodeURIComponent(eventPath)}`);
    await expect(page.getByTestId("tg-enter-error")).toContainText("not linked to an Arena account");
  });
});
