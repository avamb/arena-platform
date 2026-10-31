/**
 * The Telegram mini app page /tg/invitations (spec 46, 4.2, INV-05m) at the width of a phone webview (360 x 740), against
 * the mocked API. It proves what jsdom cannot: the real hall and form in a real browser at 360 px have no horizontal
 * scroll and no axe findings, an invitation is issued from the reserve in ONE request, a seat taken meanwhile is
 * outlined and the form stays, the PDF goes through the share sheet of the window or, without one, a plain download with
 * a hint, there is no CSV upload but a link to the web screen, and the refused cases say what to do. The real API twin
 * is live/invitations-seats.spec.ts (the screen is the same parts as the web one).
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { API, CORS, EVENT, ORG_A, SESSION, mockApi, newAuthState } from "./mockApi";
import { mockInvitationsSeats } from "./invitationsSeatsMock";

test.use({ viewport: { width: 360, height: 740 } });

const FOREIGN_EVENT = "0193f01a-0000-7000-8000-0000000000ff";
const FOREIGN_SESSION = "0193f01a-0000-7000-8000-0000000000fe";
const LINK = `/tg/invitations?session=${SESSION}&event=${EVENT}#code=good-link-code`;
const seat = (page: Page, key: string) => page.locator(`[data-k="${key}"]`);

interface TgStub {
  ready: number;
  expand: number;
  closed: number;
  backShown: number;
  backHandler: (() => void) | null;
  downloadFile: number;
  openLink: string[];
  shared: Array<{ name: string; type: string }>;
}

/** A stand-in for the Telegram script before anything runs. `share` makes the window able to share a file. */
async function telegramStub(page: Page, opts: { share: boolean } = { share: true }) {
  await page.addInitScript((o) => {
    const w = window as unknown as Record<string, unknown>;
    const tg: TgStubShape = { ready: 0, expand: 0, closed: 0, backShown: 0, backHandler: null, downloadFile: 0, openLink: [], shared: [] };
    interface TgStubShape {
      ready: number;
      expand: number;
      closed: number;
      backShown: number;
      backHandler: (() => void) | null;
      downloadFile: number;
      openLink: string[];
      shared: Array<{ name: string; type: string }>;
    }
    w.__tg = tg;
    w.Telegram = {
      WebApp: {
        initData: "",
        platform: "android",
        ready: () => tg.ready++,
        expand: () => tg.expand++,
        close: () => tg.closed++,
        openLink: (url: string) => tg.openLink.push(url),
        downloadFile: () => tg.downloadFile++,
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
    if (o.share) {
      const nav = navigator as unknown as Record<string, unknown>;
      nav.canShare = (d: { files?: File[] }) => (d.files?.length ?? 0) > 0;
      nav.share = async (d: { files: File[] }) => {
        tg.shared.push({ name: d.files[0].name, type: d.files[0].type });
      };
    }
  }, opts);
}

const tgState = (page: Page) => page.evaluate(() => (window as unknown as { __tg: TgStub }).__tg);

/** The PDF route of INV-03 is not in the shared invitations mock (it answers 404): a real small PDF here. */
async function pdfRoute(page: Page) {
  await page.route(`${API}/v1/organizations/${ORG_A}/invitations/*/tickets.pdf`, async (route) => {
    if (route.request().method() === "OPTIONS") return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: "application/pdf",
      headers: { ...CORS, "Access-Control-Expose-Headers": "Content-Disposition", "Content-Disposition": 'attachment; filename="invitation-1000000500.pdf"' },
      body: Buffer.from("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF"),
    });
  });
}

async function start(page: Page, opts: { share: boolean } = { share: true }) {
  await mockApi(page, newAuthState());
  const state = await mockInvitationsSeats(page);
  await pdfRoute(page);
  await telegramStub(page, opts);
  return state;
}

async function noHorizontalScroll(page: Page) {
  const wide = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, body: document.body.scrollWidth, inner: window.innerWidth }));
  expect(wide.doc, "document scrollWidth").toBeLessThanOrEqual(wide.inner);
  expect(wide.body, "body scrollWidth").toBeLessThanOrEqual(wide.inner);
}

/** Issues one invitation for the seat held back by the organizer and waits until its PDF button is usable. */
async function issueFromReserve(page: Page) {
  await seat(page, "A|2|3").click();
  await page.getByTestId("inv-name").fill("Ana López");
  await page.getByTestId("inv-delivery-pdf_only").check();
  await page.getByTestId("inv-submit").click();
  const result = page.getByTestId("inv-result");
  await expect(result).toContainText("1 places for Ana López");
  await expect(result.getByTestId("inv-pdf")).toBeEnabled({ timeout: 15_000 });
  return result;
}

test.describe("the invitations mini app at 360 x 740", () => {
  test.describe.configure({ timeout: 60_000 });

  test("signs in with the code, issues a seat from the reserve in ONE request, fits the screen", async ({ page }) => {
    const api = await mockApi(page, newAuthState());
    const state = await mockInvitationsSeats(page);
    await pdfRoute(page);
    await telegramStub(page);
    await page.goto(LINK);

    await expect(page.getByTestId("inv-hall")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("tg-inv-event")).toContainText("Serebryanoe kopyttse");

    // The code, the session and the event left the address before the first request.
    expect(page.url()).not.toContain("good-link-code");
    expect(page.url()).not.toContain("session=");
    expect(api.requests[0]).toBe("POST /v1/auth/web/link");

    // The page told Telegram it is up, asked for the full height and showed the back button.
    const tg = await tgState(page);
    expect(tg.ready).toBeGreaterThan(0);
    expect(tg.expand).toBeGreaterThan(0);
    await expect.poll(async () => (await tgState(page)).backShown).toBeGreaterThan(0);
    await noHorizontalScroll(page);

    await seat(page, "A|2|3").click();
    await expect(page.getByTestId("inv-picked-seat")).toHaveCount(1);
    await expect(page.getByTestId("inv-from-reserve")).toContainText("1");
    await page.getByTestId("inv-name").fill("Ana López");
    await page.getByTestId("inv-delivery-pdf_only").check();
    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-result")).toContainText("1 places for Ana López");

    expect(state.issued).toHaveLength(1);
    const body = state.issued[0] as { seats: unknown; guest: Record<string, unknown>; delivery: string };
    expect(body.seats).toEqual([{ seat_key: "A|2|3" }]);
    expect(body.guest).toMatchObject({ name: "Ana López" });
    expect(body.delivery).toBe("pdf_only");
    // No password question: issuing is not a money route.
    await expect(page.getByTestId("step-up-dialog")).toHaveCount(0);
    await expect(page.getByTestId("inv-row").first()).toContainText("Ana López");
    await noHorizontalScroll(page);
  });

  test("a seat taken meanwhile is outlined, the form stays as typed, the next send goes through", async ({ page }) => {
    const state = await start(page);
    await page.goto(LINK);
    await expect(page.getByTestId("inv-hall")).toBeVisible({ timeout: 20_000 });
    await seat(page, "A|2|2").click();
    await seat(page, "A|2|4").click();
    await page.getByTestId("inv-name").fill("Carlos Ruiz");
    await page.getByTestId("inv-delivery-pdf_only").check();

    state.takeOnNextIssue = "A|2|2";
    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-conflict")).toContainText("Parter, row 2, seat 2");
    await expect(page.getByTestId("inv-picked-seat")).toHaveCount(1);
    await expect(page.getByTestId("inv-name")).toHaveValue("Carlos Ruiz");
    await expect(seat(page, "A|2|2")).toHaveAttribute("stroke", "#dc2626");
    await noHorizontalScroll(page);

    await page.getByTestId("inv-submit").click();
    await expect(page.getByTestId("inv-result")).toBeVisible();
    expect(state.issued).toHaveLength(2);
  });

  test("the PDF goes through the share sheet of the window, and downloadFile is not used", async ({ page }) => {
    await start(page);
    await page.goto(LINK);
    await expect(page.getByTestId("inv-hall")).toBeVisible({ timeout: 20_000 });
    const result = await issueFromReserve(page);
    await result.getByTestId("inv-pdf").click();
    await expect.poll(async () => (await tgState(page)).shared).toEqual([{ name: "invitation-1000000500.pdf", type: "application/pdf" }]);
    // The Telegram client fetches a downloadFile URL itself and cannot send our header: never called.
    expect((await tgState(page)).downloadFile).toBe(0);
    await expect(result.getByTestId("inv-action-say")).not.toContainText("did not open");
  });

  test("without a share sheet the PDF is a plain download and the page says where the sure way is", async ({ page }) => {
    await start(page, { share: false });
    await page.goto(LINK);
    await expect(page.getByTestId("inv-hall")).toBeVisible({ timeout: 20_000 });
    const result = await issueFromReserve(page);
    const download = page.waitForEvent("download");
    await result.getByTestId("inv-pdf").click();
    expect((await download).suggestedFilename()).toBe("invitation-1000000500.pdf");
    await expect(result.getByTestId("inv-action-say")).toContainText("If the PDF did not open");
    expect((await tgState(page)).downloadFile).toBe(0);
  });

  test("there is no CSV upload, only a link that opens the web screen of the same session in the browser", async ({ page }) => {
    await start(page);
    await page.goto(LINK);
    await expect(page.getByTestId("inv-hall")).toBeVisible({ timeout: 20_000 });
    await expect(page.locator('input[type="file"]')).toHaveCount(0);
    const link = page.getByTestId("tg-inv-open-browser");
    const web = `/o/${ORG_A}/events/${EVENT}/sessions/${SESSION}/invitations`;
    await expect(link).toHaveAttribute("href", new RegExp(`${web}$`));
    await link.click();
    // Inside Telegram the phone's own browser opens it and the window stays where it is.
    await expect.poll(async () => (await tgState(page)).openLink).toHaveLength(1);
    expect((await tgState(page)).openLink[0]).toMatch(new RegExp(`${web}$`));
    expect(new URL(page.url()).pathname).toBe("/tg/invitations");
  });

  test("the back button of Telegram closes the window", async ({ page }) => {
    await start(page);
    await page.goto(LINK);
    await expect(page.getByTestId("inv-hall")).toBeVisible({ timeout: 20_000 });
    await expect.poll(async () => (await tgState(page)).backHandler !== null).toBe(true);
    await page.evaluate(() => (window as unknown as { __tg: { backHandler: () => void } }).__tg.backHandler());
    await expect.poll(async () => (await tgState(page)).closed).toBe(1);
  });

  test("nothing scrolls sideways with the list open, and axe finds nothing", async ({ page }) => {
    const state = await start(page);
    state.issuingReads = 0;
    await page.goto(LINK);
    await expect(page.getByTestId("inv-hall")).toBeVisible({ timeout: 20_000 });
    await issueFromReserve(page);
    await expect(page.getByTestId("inv-row").first()).toHaveAttribute("data-state", "active", { timeout: 15_000 });
    await noHorizontalScroll(page);
    const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
  });

  test("the client theme reaches the page through custom properties only", async ({ page }) => {
    await start(page);
    await page.goto(LINK);
    await expect(page.getByTestId("tg-inv")).toBeVisible({ timeout: 20_000 });
    await page.evaluate(() => {
      const root = document.documentElement.style;
      root.setProperty("--tg-theme-bg-color", "rgb(16, 24, 32)");
      root.setProperty("--tg-theme-text-color", "rgb(240, 240, 240)");
    });
    const look = await page.evaluate(() => {
      const card = getComputedStyle(document.querySelector(".tg-card") as HTMLElement);
      return { bg: card.backgroundColor, color: card.color };
    });
    expect(look).toEqual({ bg: "rgb(16, 24, 32)", color: "rgb(240, 240, 240)" });
    expect(await page.locator("main [style]").count()).toBe(0);
  });

  test("a used code says the link does not work and to press the bot button again", async ({ page }) => {
    const auth = newAuthState();
    auth.linkUsed = true;
    await mockApi(page, auth);
    await mockInvitationsSeats(page);
    await page.goto(LINK);
    const err = page.getByTestId("tg-inv-error");
    await expect(err).toContainText("This link does not work any more", { timeout: 20_000 });
    await expect(err).toContainText("invitations button");
    await expect(page.getByTestId("inv-hall")).toHaveCount(0);
    await noHorizontalScroll(page);
  });

  test("an event or a session the person does not have says there is no access, and shows no form", async ({ page }) => {
    await mockApi(page, newAuthState());
    await mockInvitationsSeats(page);
    await page.goto(`/tg/invitations?session=${SESSION}&event=${FOREIGN_EVENT}#code=good-link-code`);
    await expect(page.getByTestId("tg-inv-no-access")).toContainText("No access to this date", { timeout: 20_000 });
    await expect(page.getByTestId("inv-form")).toHaveCount(0);
    await expect(page.getByTestId("tg-inv-close")).toBeVisible();
  });

  test("a real event with a session that is not its own says there is no access", async ({ page }) => {
    await mockApi(page, newAuthState());
    await mockInvitationsSeats(page);
    await page.goto(`/tg/invitations?session=${FOREIGN_SESSION}&event=${EVENT}#code=good-link-code`);
    await expect(page.getByTestId("tg-inv-no-access")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("inv-form")).toHaveCount(0);
  });

  test("a link without a session says it is not complete", async ({ page }) => {
    await mockApi(page, newAuthState());
    await mockInvitationsSeats(page);
    await page.goto(`/tg/invitations?event=${EVENT}#code=good-link-code`);
    await expect(page.getByTestId("tg-inv-error")).toContainText("The link is not complete", { timeout: 20_000 });
  });
});
