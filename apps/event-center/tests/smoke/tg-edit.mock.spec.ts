/**
 * The Telegram mini app page /tg/edit (spec 44, RT-12b, RT-13) at the width of a phone webview (360 x 740), against the mocked
 * API. It proves what jsdom cannot: the real editor in a real browser at 360 px has no horizontal scroll and its toolbar is
 * inside the screen, the client theme reaches the page through custom properties only, a save sends the ONE call and closes
 * the window, and the refused cases (a used code, a foreign event) say what to do. CI runs this file twice: on the dev
 * server and, with EVENT_CENTER_BASE_URL, against the real nginx image, where the test "no Content-Security-Policy
 * violation" proves the vendored Telegram script, the editor and a save under `style-src 'self'`. The real API is
 * live/tg-edit.spec.ts.
 */
import { expect, test, type Page } from "@playwright/test";
import { API, CORS, EVENT, ORG_A, mockApi, newAuthState } from "./mockApi";

test.use({ viewport: { width: 360, height: 740 } });

const FOREIGN_EVENT = "0193f01a-0000-7000-8000-0000000000ff";
const DESCRIPTION = "<p>An evening of <strong>jazz</strong>.</p>";
const NOTE = "<p>Bring a <strong>warm</strong> jacket.</p>";

interface Writes {
  patches: Array<Record<string, unknown>>;
}

/** On top of mockApi: the right to edit, an event with texts, and a PATCH that answers 200. */
async function tgMocks(page: Page): Promise<Writes> {
  const writes: Writes = { patches: [] };
  await page.route(`${API}/**`, async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (req.method() === "OPTIONS" && path.startsWith(`/v1/organizations/${ORG_A}/events`)) {
      return route.fulfill({ status: 204, headers: { ...CORS, "Access-Control-Allow-Headers": "*", "Access-Control-Allow-Methods": "GET, POST, PATCH, OPTIONS" } });
    }
    const json = (body: unknown) => route.fulfill({ status: 200, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
    if (req.method() === "GET" && path === `/v1/organizations/${ORG_A}/me`) {
      return json({
        org_id: ORG_A,
        org_name: "Teatr Kolybel",
        org_display_number: 11,
        caller_type: "user",
        role: "organizer",
        membership_roles: ["organizer"],
        roles: ["organizer"],
        permissions: ["event.read", "event.update", "order.read"],
        platform_superadmin: false,
      });
    }
    if (req.method() === "GET" && path === `/v1/organizations/${ORG_A}/events`) {
      return json({
        events: [
          {
            id: EVENT,
            org_id: ORG_A,
            name: "Serebryanoe kopyttse",
            description: DESCRIPTION,
            description_format: "art",
            email_note: NOTE,
            email_note_format: "art",
            status: "published",
            sales_state: "on_sale",
            visibility: "public",
            created_at: "2026-09-01T10:00:00Z",
            updated_at: "2026-09-01T10:00:00Z",
          },
        ],
      });
    }
    if (req.method() === "PATCH" && path === `/v1/organizations/${ORG_A}/events/${EVENT}`) {
      writes.patches.push(req.postDataJSON() as Record<string, unknown>);
      return json({ event: { id: EVENT } });
    }
    return route.fallback();
  });
  return writes;
}

/** A stand-in for the Telegram script, in the page before anything runs. */
async function telegramStub(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as Record<string, unknown>;
    w.__tg = { ready: 0, expand: 0, closed: 0, backShown: 0, backHandler: null };
    const tg = w.__tg as { ready: number; expand: number; closed: number; backShown: number; backHandler: (() => void) | null };
    w.Telegram = {
      WebApp: {
        ready: () => tg.ready++,
        expand: () => tg.expand++,
        close: () => tg.closed++,
        BackButton: {
          show: () => tg.backShown++,
          hide: () => undefined,
          onClick: (h: () => void) => {
            tg.backHandler = h;
          },
          offClick: () => undefined,
        },
      },
    };
  });
}

const tgState = (page: Page) =>
  page.evaluate(() => (window as unknown as { __tg: { ready: number; expand: number; closed: number; backShown: number; backHandler: (() => void) | null } }).__tg);

async function noHorizontalScroll(page: Page) {
  const wide = await page.evaluate(() => ({
    doc: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
    inner: window.innerWidth,
  }));
  expect(wide.doc, "document scrollWidth").toBeLessThanOrEqual(wide.inner);
  expect(wide.body, "body scrollWidth").toBeLessThanOrEqual(wide.inner);
}

/** Records every CSP violation the page reports: by the console and by the event. */
async function watchCsp(page: Page) {
  const violations: string[] = [];
  await page.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (e) => {
      (window as unknown as { __csp: string[] }).__csp ??= [];
      (window as unknown as { __csp: string[] }).__csp.push(`${e.violatedDirective} ${e.blockedURI}`);
    });
  });
  page.on("console", (m) => {
    if (/content security policy|refused to (apply|load|execute)/i.test(m.text())) violations.push(`console: ${m.text().slice(0, 400)}`);
  });
  return {
    all: async () => [...violations, ...(await page.evaluate(() => (window as unknown as { __csp?: string[] }).__csp ?? []))],
  };
}

const bullets = (page: Page) => page.locator('button[data-action="bulletList"]');

test.describe("the mini app editor at 360 x 740", () => {
  test("the description: the editor fits the screen, a save sends ONE PATCH and closes the Telegram window", async ({ page }) => {
    const auth = newAuthState();
    const state = await mockApi(page, auth);
    const writes = await tgMocks(page);
    await telegramStub(page);
    await page.goto(`/tg/edit#code=good-link-code&kind=description&event=${EVENT}`);

    const box = page.locator("[data-testid=tg-description-field] .ProseMirror");
    await expect(box).toBeVisible({ timeout: 20_000 });
    await expect(box.locator("strong")).toHaveText("jazz");
    await expect(page.getByTestId("tg-edit-event")).toContainText("Serebryanoe kopyttse");

    // The code, the kind and the event left the address before the first request.
    expect(page.url()).not.toContain("good-link-code");
    expect(page.url()).not.toContain("kind=");
    expect(state.requests[0]).toBe("POST /v1/auth/web/link");

    // The page told Telegram it is up, asked for the full height and showed the back button.
    const tg = await tgState(page);
    expect(tg.ready).toBeGreaterThan(0);
    expect(tg.expand).toBeGreaterThan(0);
    expect(tg.backShown).toBeGreaterThan(0);

    // No horizontal scroll, and the whole toolbar is inside the screen.
    await noHorizontalScroll(page);
    const toolbar = await page.evaluate(() => {
      const buttons = Array.from(document.querySelectorAll<HTMLElement>(".arena-re__toolbar button"));
      return {
        count: buttons.length,
        outside: buttons.filter((b) => b.getBoundingClientRect().right > window.innerWidth + 0.5 || b.getBoundingClientRect().left < -0.5).length,
        rows: new Set(buttons.map((b) => Math.round(b.getBoundingClientRect().top))).size,
      };
    });
    expect(toolbar.count).toBeGreaterThan(5);
    expect(toolbar.outside, "toolbar buttons outside the screen").toBe(0);
    await expect(bullets(page)).toBeInViewport();

    // Edit and save.
    await bullets(page).click();
    await expect(page.getByTestId("tg-description-unsaved")).toBeVisible();
    await page.getByTestId("tg-description-save").click();
    await expect.poll(async () => (await tgState(page)).closed).toBe(1);
    expect(writes.patches).toEqual([{ description: "<ul><li>An evening of <strong>jazz</strong>.</li></ul>", description_format: "art" }]);
    await noHorizontalScroll(page);
  });

  test("no Content-Security-Policy violation: the vendored Telegram script, the editor and a save", async ({ page }) => {
    await mockApi(page);
    await tgMocks(page);
    const csp = await watchCsp(page);
    await page.goto(`/tg/edit#code=good-link-code&kind=description&event=${EVENT}`);
    await expect(page.locator("[data-testid=tg-description-field] .ProseMirror")).toBeVisible({ timeout: 20_000 });
    // The real script of Telegram loaded (it is a file of this origin) and the page works without a client behind it.
    await expect.poll(() => page.evaluate(() => (window as unknown as { Telegram?: { WebApp?: { platform?: string } } }).Telegram?.WebApp?.platform)).toBe("unknown");
    await bullets(page).click();
    await page.getByTestId("tg-description-save").click();
    await expect(page.getByTestId("tg-edit-saved")).toBeVisible();
    expect(await csp.all()).toEqual([]);
  });

  test("the letter text: a save sends the letter PATCH and closes the window", async ({ page }) => {
    await mockApi(page);
    const writes = await tgMocks(page);
    await telegramStub(page);
    await page.goto(`/tg/edit#code=good-link-code&kind=email_note&event=${EVENT}`);
    await expect(page.locator("[data-testid=email-note-field] .ProseMirror")).toBeVisible({ timeout: 20_000 });
    await noHorizontalScroll(page);
    await bullets(page).click();
    await page.getByTestId("email-note-save").click();
    await expect.poll(async () => (await tgState(page)).closed).toBe(1);
    expect(writes.patches).toEqual([{ email_note: "<ul><li>Bring a <strong>warm</strong> jacket.</li></ul>", email_note_format: "art" }]);
  });

  test("outside Telegram a save says Saved and links to the event", async ({ page }) => {
    await mockApi(page);
    await tgMocks(page);
    await page.goto(`/tg/edit#code=good-link-code&kind=description&event=${EVENT}`);
    await expect(page.locator("[data-testid=tg-description-field] .ProseMirror")).toBeVisible({ timeout: 20_000 });
    await bullets(page).click();
    await page.getByTestId("tg-description-save").click();
    await expect(page.getByTestId("tg-edit-saved")).toContainText("Saved.");
    await expect(page.getByTestId("tg-edit-open-event")).toHaveAttribute("href", `/o/${ORG_A}/events/${EVENT}`);
    await noHorizontalScroll(page);
  });

  test("the back button of Telegram closes the window", async ({ page }) => {
    await mockApi(page);
    await tgMocks(page);
    await telegramStub(page);
    await page.goto(`/tg/edit#code=good-link-code&kind=description&event=${EVENT}`);
    await expect(page.locator("[data-testid=tg-description-field] .ProseMirror")).toBeVisible({ timeout: 20_000 });
    await page.evaluate(() => (window as unknown as { __tg: { backHandler: () => void } }).__tg.backHandler());
    await expect.poll(async () => (await tgState(page)).closed).toBe(1);
  });

  test("the client theme and the safe areas reach the page through custom properties only", async ({ page }) => {
    await mockApi(page);
    await tgMocks(page);
    await page.goto(`/tg/edit#code=good-link-code&kind=description&event=${EVENT}`);
    await expect(page.getByTestId("tg-edit")).toBeVisible({ timeout: 20_000 });
    // What the Telegram script does with the CSSOM, which style-src 'self' allows.
    await page.evaluate(() => {
      const root = document.documentElement.style;
      root.setProperty("--tg-theme-bg-color", "rgb(16, 24, 32)");
      root.setProperty("--tg-theme-text-color", "rgb(240, 240, 240)");
      root.setProperty("--tg-safe-area-inset-left", "20px");
    });
    const look = await page.evaluate(() => {
      const card = getComputedStyle(document.querySelector(".tg-card") as HTMLElement);
      const pageEl = getComputedStyle(document.querySelector(".tg-page") as HTMLElement);
      return { bg: card.backgroundColor, color: card.color, padLeft: pageEl.paddingLeft, padTop: pageEl.paddingTop };
    });
    expect(look.bg).toBe("rgb(16, 24, 32)");
    expect(look.color).toBe("rgb(240, 240, 240)");
    // The inset of Telegram is bigger than the 0.75rem floor, and the floor holds at the top without an inset.
    expect(look.padLeft).toBe("20px");
    expect(look.padTop).toBe("12px");
    // No element of the page carries a style attribute of its own.
    expect(await page.locator("main [style]").count()).toBe(0);
  });

  test("a used code says the link does not work and to press the bot button again", async ({ page }) => {
    const auth = newAuthState();
    auth.linkUsed = true;
    await mockApi(page, auth);
    await tgMocks(page);
    await page.goto(`/tg/edit#code=good-link-code&kind=description&event=${EVENT}`);
    const err = page.getByTestId("tg-edit-error");
    await expect(err).toContainText("This link does not work any more");
    await expect(err).toContainText("Edit in the editor");
    await expect(page.locator(".ProseMirror")).toHaveCount(0);
    await noHorizontalScroll(page);
  });

  test("an event that is not in the person's organizations says there is no access, and shows no editor", async ({ page }) => {
    await mockApi(page);
    await tgMocks(page);
    await page.goto(`/tg/edit#code=good-link-code&kind=description&event=${FOREIGN_EVENT}`);
    await expect(page.getByTestId("tg-edit-no-access")).toContainText("No access to this event", { timeout: 20_000 });
    await expect(page.locator(".ProseMirror")).toHaveCount(0);
    await expect(page.getByTestId("tg-edit-close")).toBeVisible();
  });
});
