/**
 * A letter's link opened in a browser already signed in to ANOTHER account
 * (2026-11-07: the owner invited a new user to Vino&Co, opened the letter in
 * their own browser and landed in their own organization). The password page
 * warns and signs the open session out after saving; the sign-in page with
 * the letter's e-mail never enters the open session silently.
 */
import { expect, test, type Page } from "@playwright/test";
import { mockApi } from "./mockApi";

async function signInAsManager(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByTestId("org-list")).toBeVisible();
}

test("the invitation link signs out the open session after the password is saved", async ({ page }) => {
  const state = await mockApi(page);
  await signInAsManager(page);

  await page.goto("/accept-invite?token=tok-1&email=anna%40example.test");
  await expect(page.getByTestId("password-other-session")).toContainText("sign in as anna@example.test");
  await page.getByLabel("New password").fill("a long phrase");
  await page.getByLabel("Repeat the password").fill("a long phrase");
  await page.getByTestId("password-submit").click();
  await expect(page.getByTestId("password-done")).toBeVisible();
  expect(state.requests).toContain("POST /v1/auth/logout");

  await page.getByRole("link", { name: "Go to sign in" }).click();
  await expect(page.getByLabel("E-mail")).toHaveValue("anna@example.test");
  await expect(page.getByTestId("org-list")).toHaveCount(0);
});

test("the sign-in link of a letter asks before entering the open session", async ({ page }) => {
  const state = await mockApi(page);
  await signInAsManager(page);

  await page.goto("/login?email=anna%40example.test");
  await expect(page.getByTestId("login-other-session")).toContainText("To sign in as anna@example.test, sign out first.");
  await expect(page.getByTestId("org-list")).toHaveCount(0);

  await page.getByTestId("login-other-signout").click();
  await expect(page.getByLabel("E-mail")).toHaveValue("anna@example.test");
  expect(state.requests).toContain("POST /v1/auth/logout");
});

test("the plain sign-in page still opens the open session", async ({ page }) => {
  await mockApi(page);
  await signInAsManager(page);
  await page.goto("/login");
  await expect(page.getByTestId("org-list")).toBeVisible();
});
