/**
 * The Telegram mini app entry /tg (the bot's web_app button opens /tg?next=<path>) at the width of a phone webview,
 * against the mocked API: the launch data is exchanged once, the page opens the path the server answers, a path outside
 * the event center is never opened, every refusal of the server says in plain words what to do, and the Telegram back
 * button walks the history of the app. CI runs this file twice: on the dev server and, with EVENT_CENTER_BASE_URL,
 * against the real nginx image, where the test "no Content-Security-Policy violation" proves the page under the real
 * policy. The real API is live/tg-enter.spec.ts.
 */
import { expect, test, type Page } from "@playwright/test";
import { API, CORS, EVENT, ORG_A, mockApi, newAuthState } from "./mockApi";

test.use({ viewport: { width: 360, height: 740 } });

const INIT_DATA = "query_id=AAH123&user=%7B%22id%22%3A42%7D&auth_date=1790000000&hash=abc";
const EVENTS_PATH = `/o/${ORG_A}/events`;

interface Exchange {
  bodies: Array<Record<string, unknown>>;
}

/** On top of mockApi: POST /v1/auth/telegram-miniapp answers `reply` (a session, or an error). */
async function miniappMock(page: Page, reply: { status: number; body: Record<string, unknown> }): Promise<Exchange> {
  const seen: Exchange = { bodies: [] };
  await page.route(`${API}/v1/auth/telegram-miniapp`, async (route) => {
    const req = route.request();
    if (req.method() === "OPTIONS") {
      return route.fulfill({ status: 204, headers: { ...CORS, "Access-Control-Allow-Headers": "*", "Access-Control-Allow-Methods": "POST, OPTIONS" } });
    }
    seen.bodies.push(req.postDataJSON() as Record<string, unknown>);
    return route.fulfill({ status: reply.status, contentType: "application/json", headers: CORS, body: JSON.stringify(reply.body) });
  });
  return seen;
}

const session = (next: string) => ({
  status: 200,
  body: {
    access_token: "at-tg",
    refresh_token: "rt-tg",
    token_type: "Bearer",
    expires_at: new Date(Date.now() + 1_800_000).toISOString(),
    user_id: "u1",
    next,
  },
});

const refusal = (status: number, code: string) => ({ status, body: { error: { code, message: "refused" } } });

/** A stand-in for the Telegram script, with launch data (or without), before anything runs. */
async function telegramStub(page: Page, initData: string) {
  await page.addInitScript((data) => {
    const w = window as unknown as Record<string, unknown>;
    const tg = { ready: 0, expand: 0, backShown: 0, backHidden: 0, backHandler: null as (() => void) | null };
    w.__tg = tg;
    w.Telegram = {
      WebApp: {
        initData: data,
        platform: "android",
        ready: () => tg.ready++,
        expand: () => tg.expand++,
        close: () => undefined,
        BackButton: {
          show: () => tg.backShown++,
          hide: () => tg.backHidden++,
          onClick: (h: () => void) => {
            tg.backHandler = h;
          },
          offClick: () => {
            tg.backHandler = null;
          },
        },
      },
    };
  }, initData);
}

interface TgState {
  ready: number;
  expand: number;
  backShown: number;
  backHidden: number;
  backHandler: (() => void) | null;
}
const tgState = (page: Page) => page.evaluate(() => (window as unknown as { __tg: TgState }).__tg);

/** Every CSP violation the page reports (the container run enforces the real nginx policy). */
async function watchCsp(page: Page) {
  await page.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (e) => {
      const w = window as unknown as { __csp?: string[] };
      (w.__csp ??= []).push(e.violatedDirective + " " + e.blockedURI);
    });
  });
  return () => page.evaluate(() => (window as unknown as { __csp?: string[] }).__csp ?? []);
}

test.describe("the mini app entry /tg", () => {
  test("signs in with the launch data, tells Telegram it is ready and opens the path the server answers", async ({ page }) => {
    await mockApi(page, newAuthState());
    const seen = await miniappMock(page, session(EVENTS_PATH));
    await telegramStub(page, INIT_DATA);
    const violations = await watchCsp(page);
    await page.goto(`/tg?next=${encodeURIComponent(EVENTS_PATH)}`);

    await expect(page).toHaveURL(new RegExp(EVENTS_PATH + "$"));
    expect(seen.bodies).toEqual([{ init_data: INIT_DATA, next: EVENTS_PATH }]);
    const tg = await tgState(page);
    expect(tg.ready).toBeGreaterThan(0);
    expect(tg.expand).toBeGreaterThan(0);
    expect(await violations(), "no Content-Security-Policy violation").toEqual([]);
  });

  test("opens the organizations when the server answers no next, and sends none for an unsafe one", async ({ page }) => {
    await mockApi(page, newAuthState());
    const seen = await miniappMock(page, session(""));
    await telegramStub(page, INIT_DATA);
    await page.goto(`/tg?next=${encodeURIComponent("https://evil.example/o/x")}`);

    await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
    expect(seen.bodies).toEqual([{ init_data: INIT_DATA }]);
    expect(page.url()).not.toContain("evil.example");
  });

  test("never opens a path outside the event center, even if the server answered one", async ({ page }) => {
    await mockApi(page, newAuthState());
    await miniappMock(page, session("//evil.example/o/x"));
    await telegramStub(page, INIT_DATA);
    await page.goto("/tg");

    await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
    expect(page.url()).not.toContain("evil.example");
  });

  test("the Telegram back button appears after a step inside the app and walks back", async ({ page }) => {
    await mockApi(page, newAuthState());
    await miniappMock(page, session(EVENTS_PATH));
    await telegramStub(page, INIT_DATA);
    await page.goto(`/tg?next=${encodeURIComponent(EVENTS_PATH)}`);
    await expect(page).toHaveURL(new RegExp(EVENTS_PATH + "$"));
    // The first page of the app: nothing to go back to, so no button (Telegram's cross closes the window).
    expect((await tgState(page)).backShown).toBe(0);

    await page.getByRole("link", { name: /Serebryanoe kopyttse/ }).first().click();
    await expect(page).toHaveURL(new RegExp(EVENTS_PATH + "/" + EVENT));
    await expect.poll(async () => (await tgState(page)).backShown).toBeGreaterThan(0);

    await page.evaluate(() => (window as unknown as { __tg: { backHandler: () => void } }).__tg.backHandler());
    await expect(page).toHaveURL(new RegExp(EVENTS_PATH + "$"));
    await expect.poll(async () => (await tgState(page)).backHidden).toBeGreaterThan(0);
  });

  const REFUSALS: Array<[number, string, RegExp]> = [
    [401, "auth.telegram_init_data_invalid", /could not check your Telegram sign-in/],
    [401, "auth.telegram_init_data_expired", /opened a while ago/],
    [403, "auth.telegram_not_linked", /not linked to an Arena account/],
    [403, "auth.user_deactivated", /account is switched off/],
    [403, "auth.web_link_superadmin", /platform superadmin/],
    [429, "auth.rate_limited", /too many attempts/],
    [503, "auth.telegram_miniapp_not_configured", /not available right now/],
  ];
  for (const [status, code, text] of REFUSALS) {
    test(`says what to do after ${status} ${code}, without technical words`, async ({ page }) => {
      await mockApi(page, newAuthState());
      await miniappMock(page, refusal(status, code));
      await telegramStub(page, INIT_DATA);
      await page.goto(`/tg?next=${encodeURIComponent(EVENTS_PATH)}`);
      const alert = page.getByTestId("tg-enter-error");
      await expect(alert).toContainText(text);
      await expect(alert).not.toContainText(/auth\.|init_?data|\b40[13]\b|\b50\d\b/i);
      await expect(page.getByTestId("tg-enter-close")).toBeVisible();
      // Nothing signed in: the page stays where it is.
      expect(new URL(page.url()).pathname).toBe("/tg");
    });
  }

  test("outside Telegram (no launch data) says the page opens from the bot and calls nothing", async ({ page }) => {
    await mockApi(page, newAuthState());
    const seen = await miniappMock(page, session(EVENTS_PATH));
    await telegramStub(page, "");
    await page.goto(`/tg?next=${encodeURIComponent(EVENTS_PATH)}`);
    await expect(page.getByTestId("tg-enter-error")).toContainText("opens from the Telegram bot");
    expect(seen.bodies).toEqual([]);
  });
});
