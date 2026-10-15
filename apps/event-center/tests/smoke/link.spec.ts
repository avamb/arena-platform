/**
 * The bot's "Open in browser" link (spec 37 section 4.3): the code leaves
 * the address before any request, travels in the request body only, works
 * once, and a refused link says what to do.
 */
import { expect, test } from "@playwright/test";
import { mockApi, newAuthState } from "./mockApi";

test.describe("sign-in with a link from the bot", () => {
  test("a good link signs in, and the code never stays in the address", async ({ page }) => {
    const auth = newAuthState();
    const state = await mockApi(page, auth);
    const exchange = page.waitForRequest((r) => r.url().endsWith("/v1/auth/web/link"));
    await page.goto("/login/link#code=good-link-code");
    const req = await exchange;
    expect(req.postDataJSON()).toEqual({ code: "good-link-code" });
    expect(req.url()).not.toContain("good-link-code");
    await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
    expect(page.url()).not.toContain("good-link-code");
    // The code left the address before the first request of the page.
    expect(state.requests[0]).toBe("POST /v1/auth/web/link");
  });

  test("a used or wrong link explains what to do", async ({ page }) => {
    const auth = newAuthState();
    auth.linkUsed = true;
    await mockApi(page, auth);
    await page.goto("/login/link#code=good-link-code");
    await expect(page.getByTestId("link-login-error")).toContainText("Open in browser");
    await expect(page.getByRole("link", { name: "Sign in with e-mail and password" })).toBeVisible();
    expect(page.url()).not.toContain("code");
  });

  test("a link without a code says it is not complete", async ({ page }) => {
    const state = await mockApi(page);
    await page.goto("/login/link");
    await expect(page.getByTestId("link-login-error")).toContainText("not complete");
    expect(state.requests.filter((r) => r.includes("/v1/auth/web/link"))).toEqual([]);
  });
});
