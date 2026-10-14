/**
 * Owner decisions 2026-10-14 (spec 37 section 2, rows 7 and 13): the web
 * session survives a new tab and a browser restart (72 hours without
 * activity, enforced by arena-api), and the event center installs as an app
 * whose service worker never caches an API answer.
 */
import { expect, test } from "@playwright/test";
import { ORG_A, mockApi, newAuthState } from "./mockApi";

const container = !!process.env.EVENT_CENTER_BASE_URL;

test.describe("web session and installable app", () => {
  test("a new tab of the same browser is already signed in", async ({ page, context }) => {
    const auth = newAuthState(20_000);
    await mockApi(page, auth);
    await page.goto("/login");
    await page.getByLabel("E-mail").fill("manager@example.test");
    await page.getByLabel("Password").fill("right-password");
    await page.getByTestId("login-submit").click();
    await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();

    // A tab opened by hand starts with an empty sessionStorage.
    const tab = await context.newPage();
    await mockApi(tab, auth);
    await tab.goto(`/o/${ORG_A}/events`);
    await expect(tab).not.toHaveURL(/\/login/);
    await expect(tab.getByTestId("event-row").first()).toBeVisible();
    expect(auth.compromised, "an old refresh token was sent again").toBe(false);

    // Sign-out in one tab ends the session of the browser.
    await page.getByRole("button", { name: "Sign out" }).click();
    await expect(page).toHaveURL(/\/login/);
    const left = await page.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith("arena")));
    expect(left.filter((k) => /session|token/i.test(k))).toEqual([]);
    await tab.close();
  });

  test("the page names the manifest and the manifest loads", async ({ page, request }) => {
    await page.goto("/login");
    const href = await page.locator('link[rel="manifest"]').getAttribute("href");
    expect(href).toBe("/manifest.webmanifest");
    const res = await request.get(href ?? "");
    expect(res.ok()).toBe(true);
    const manifest = await res.json();
    expect(manifest.display).toBe("standalone");
    for (const icon of manifest.icons as { src: string }[]) {
      expect((await request.get(icon.src)).ok(), icon.src).toBe(true);
    }
  });

  test("the service worker caches the app shell and no API answer", async ({ page }) => {
    test.skip(!container, "the service worker is registered by the production build only");
    const auth = newAuthState(20_000);
    await mockApi(page, auth);
    await page.goto("/login");
    await page.evaluate(async () => {
      await navigator.serviceWorker.ready;
    });
    await page.getByLabel("E-mail").fill("manager@example.test");
    await page.getByLabel("Password").fill("right-password");
    await page.getByTestId("login-submit").click();
    await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
    await page.goto(`/o/${ORG_A}/events`);
    await expect(page.getByTestId("event-row").first()).toBeVisible();

    const cached = await page.evaluate(async () => {
      const urls: string[] = [];
      for (const name of await caches.keys()) {
        const cache = await caches.open(name);
        for (const req of await cache.keys()) urls.push(req.url);
      }
      return urls;
    });
    expect(cached.length).toBeGreaterThan(0);

    // The worker is registered with the build id, and its only cache is named
    // after that build: a deploy gets a new worker and the old cache goes.
    const scriptURL = await page.evaluate(async () => (await navigator.serviceWorker.ready).active?.scriptURL ?? "");
    const build = new URL(scriptURL).searchParams.get("v");
    expect(build, scriptURL).toBeTruthy();
    const names = await page.evaluate(() => caches.keys());
    expect(names).toEqual([`arena-ec-shell-${build}`]);
    for (const url of cached) {
      const u = new URL(url);
      expect(u.origin, url).toBe(new URL(page.url()).origin);
      expect(u.pathname === "/index.html" || u.pathname.startsWith("/assets/") || u.pathname.startsWith("/icons/"), url).toBe(true);
    }
  });
});
