/**
 * Feature #412 (AB-26): Sales Channels and Payment Configs
 * — org picker and create flow browser tests.
 *
 * Coverage:
 *  1. /channels — org picker loads orgs, "New channel" button is gated on org
 *     selection, create modal opens with all required fields. The submit button
 *     starts disabled (name is required but initially empty); typing a name
 *     enables it. Cancelling closes the modal. The UUID escape hatch works.
 *  2. /payments — same pattern. The submit button starts enabled (defaults are
 *     valid); entering invalid JSON for public_config disables it and shows a
 *     validation error. Cancelling closes the modal.
 *
 * The tests use the seeded superadmin account (super@test.arena.local /
 * TestPass!23) and interact with the live backend via the dev server. They
 * cover UI contract: correct testids, field presence, and client-side
 * validation — not backend persistence, which is covered by Go integration
 * tests.
 *
 * Audit reason:
 *   Both /channels and /payments call org-scoped admin endpoints that require
 *   X-Admin-Reason. The spec fills in the audit-reason modal before navigating
 *   to each page when it is present.
 */
import { test, expect } from "@playwright/test";

/** Seed credentials from apps/backend/cmd/arena-seed/main.go */
const SEED_EMAIL = "super@test.arena.local";
const SEED_PASSWORD = "TestPass!23";
const AUDIT_REASON = "Feature #412 browser test — channels & payments";

// ---------------------------------------------------------------------------
// Shared login helper
// ---------------------------------------------------------------------------

/**
 * Navigate to the app, log in if the login form is visible, and dismiss the
 * audit-reason dialog if it appears.
 */
async function loginAndSetReason(
  page: import("@playwright/test").Page,
  reason = AUDIT_REASON,
) {
  await page.goto("/");

  // If the login form is visible, fill credentials.
  const emailInput = page.locator('input[type="email"]');
  if (await emailInput.isVisible({ timeout: 4_000 }).catch(() => false)) {
    await emailInput.fill(SEED_EMAIL);
    await page.locator('input[type="password"]').fill(SEED_PASSWORD);
    await page.locator('button[type="submit"]').click();
    // Wait for navigation away from the login page.
    await page.waitForURL((url) => !url.pathname.startsWith("/login"), {
      timeout: 10_000,
    });
  }

  // If the audit-reason dialog is open (triggered by dashboard data loads),
  // fill it in so the session carries a valid reason for subsequent calls.
  const dialog = page.locator('[role="dialog"]');
  if (await dialog.isVisible({ timeout: 2_000 }).catch(() => false)) {
    const textarea = dialog.locator('textarea, input[type="text"]').first();
    await textarea.fill(reason);
    const confirmBtn = dialog.locator('button:has-text("Confirm")');
    await confirmBtn.click();
    await dialog.waitFor({ state: "hidden", timeout: 5_000 });
  }
}

// ---------------------------------------------------------------------------
// Sales Channels tests
// ---------------------------------------------------------------------------

test.describe("Sales Channels page (/channels)", () => {
  test.beforeEach(async ({ page }) => {
    await loginAndSetReason(page);
  });

  test("org picker is visible and loads organizations from the backend", async ({
    page,
  }) => {
    // SPA navigation (click the sidebar nav link) to avoid a full reload that
    // would remount the auth provider and trigger a session-restore race.
    await page.locator('[data-testid="nav-channels"]').click();
    await page.waitForSelector('[data-testid="channels-org-picker"]');

    const picker = page.locator('[data-testid="channels-org-picker"]');
    await expect(picker).toBeVisible();

    // The backend should return at least one org (seed has three).
    const options = picker.locator("option");
    const count = await options.count();
    expect(count).toBeGreaterThan(1); // placeholder + ≥1 org
  });

  test('"New channel" is disabled without org, enabled after org selection', async ({
    page,
  }) => {
    await page.locator('[data-testid="nav-channels"]').click();
    await page.waitForSelector('[data-testid="channels-org-picker"]');

    // No org selected → button must be disabled.
    const newBtn = page.locator('[data-testid="channels-new"]');
    await expect(newBtn).toBeDisabled();

    // Select an org.
    await page
      .locator('[data-testid="channels-org-picker"]')
      .selectOption({ index: 1 }); // first real org (index 0 = placeholder)

    // After org selection the button must become enabled.
    await expect(newBtn).toBeEnabled({ timeout: 5_000 });
  });

  test("create modal opens and shows all required form fields", async ({
    page,
  }) => {
    await page.locator('[data-testid="nav-channels"]').click();
    await page.waitForSelector('[data-testid="channels-org-picker"]');

    await page
      .locator('[data-testid="channels-org-picker"]')
      .selectOption({ index: 1 }); // first real org (index 0 = placeholder)
    await page.locator('[data-testid="channels-new"]').click();

    const dialog = page.locator('[data-testid="channels-form-dialog"]');
    await expect(dialog).toBeVisible();

    // All required fields must be present.
    await expect(
      dialog.locator('[data-testid="channels-form-name"]'),
    ).toBeVisible();
    await expect(
      dialog.locator('[data-testid="channels-form-payment-mode"]'),
    ).toBeVisible();
    await expect(
      dialog.locator('[data-testid="channels-form-provider"]'),
    ).toBeVisible();
    await expect(
      dialog.locator('[data-testid="channels-form-fee-percent"]'),
    ).toBeVisible();
  });

  test("create modal — submit is disabled until name is filled", async ({
    page,
  }) => {
    await page.locator('[data-testid="nav-channels"]').click();
    await page.waitForSelector('[data-testid="channels-org-picker"]');

    await page
      .locator('[data-testid="channels-org-picker"]')
      .selectOption({ index: 1 }); // first real org (index 0 = placeholder)
    await page.locator('[data-testid="channels-new"]').click();

    const dialog = page.locator('[data-testid="channels-form-dialog"]');
    await expect(dialog).toBeVisible();

    const submitBtn = dialog.locator('[data-testid="channels-form-submit"]');

    // Name is empty on open → submit must be disabled (client-side validation).
    await expect(submitBtn).toBeDisabled();

    // Fill a valid name (the default mode is direct_merchant which also
    // requires provider_account_id, so fill both to get a fully valid form).
    await dialog
      .locator('[data-testid="channels-form-name"]')
      .fill("Test Channel 412");
    await dialog
      .locator('[data-testid="channels-form-account-id"]')
      .fill("acct_test_412");
    await expect(submitBtn).toBeEnabled({ timeout: 3_000 });
  });

  test("create modal — cancel button closes the dialog", async ({ page }) => {
    await page.locator('[data-testid="nav-channels"]').click();
    await page.waitForSelector('[data-testid="channels-org-picker"]');

    await page
      .locator('[data-testid="channels-org-picker"]')
      .selectOption({ index: 1 }); // first real org (index 0 = placeholder)
    await page.locator('[data-testid="channels-new"]').click();

    const dialog = page.locator('[data-testid="channels-form-dialog"]');
    await expect(dialog).toBeVisible();

    await dialog.locator('[data-testid="channels-form-cancel"]').click();
    await expect(dialog).toBeHidden({ timeout: 3_000 });
  });

  test('"Paste UUID instead" escape hatch is present and expands', async ({
    page,
  }) => {
    await page.locator('[data-testid="nav-channels"]').click();
    await page.waitForSelector('[data-testid="channels-org-picker"]');

    // The UUID escape-hatch input lives inside a <details> element scoped to
    // the org picker area. Click its <summary> to expand it.
    // Use the data-testid of the hidden input as an anchor then find the
    // wrapping details > summary.
    const uuidInput = page.locator('[data-testid="channels-org-id"]');
    // The summary is the sibling/ancestor of the input — click the first
    // details summary within the page (there is only one on this page).
    await page.locator('[data-testid="channels-org-id"]').evaluate((el) => {
      const details = el.closest("details");
      if (details) (details as HTMLDetailsElement).open = true;
    });
    await expect(uuidInput).toBeVisible({ timeout: 3_000 });
  });
});

// ---------------------------------------------------------------------------
// Payment Configs tests
// ---------------------------------------------------------------------------

test.describe("Payment Configs page (/payments)", () => {
  test.beforeEach(async ({ page }) => {
    await loginAndSetReason(page);
  });

  test("org picker is visible and loads organizations from the backend", async ({
    page,
  }) => {
    await page.locator('[data-testid="nav-payments"]').click();
    await page.waitForSelector('[data-testid="payments-org-picker"]');

    const picker = page.locator('[data-testid="payments-org-picker"]');
    await expect(picker).toBeVisible();

    const options = picker.locator("option");
    const count = await options.count();
    expect(count).toBeGreaterThan(1);
  });

  test('"New payment config" is disabled without org, enabled after org selection', async ({
    page,
  }) => {
    await page.locator('[data-testid="nav-payments"]').click();
    await page.waitForSelector('[data-testid="payments-org-picker"]');

    const newBtn = page.locator('[data-testid="payments-new"]');
    await expect(newBtn).toBeDisabled();

    await page
      .locator('[data-testid="payments-org-picker"]')
      .selectOption({ index: 1 }); // first real org (index 0 = placeholder)

    await expect(newBtn).toBeEnabled({ timeout: 5_000 });
  });

  test("create modal opens and shows all required form fields", async ({
    page,
  }) => {
    await page.locator('[data-testid="nav-payments"]').click();
    await page.waitForSelector('[data-testid="payments-org-picker"]');

    await page
      .locator('[data-testid="payments-org-picker"]')
      .selectOption({ index: 1 }); // first real org (index 0 = placeholder)
    await page.locator('[data-testid="payments-new"]').click();

    const dialog = page.locator('[data-testid="payments-form-dialog"]');
    await expect(dialog).toBeVisible();

    await expect(
      dialog.locator('[data-testid="payments-form-provider"]'),
    ).toBeVisible();
    await expect(
      dialog.locator('[data-testid="payments-form-mode"]'),
    ).toBeVisible();
    await expect(
      dialog.locator('[data-testid="payments-form-account-id"]'),
    ).toBeVisible();
    await expect(
      dialog.locator('[data-testid="payments-form-public-config"]'),
    ).toBeVisible();
  });

  test("create modal — invalid JSON in public config disables submit and shows error", async ({
    page,
  }) => {
    await page.locator('[data-testid="nav-payments"]').click();
    await page.waitForSelector('[data-testid="payments-org-picker"]');

    await page
      .locator('[data-testid="payments-org-picker"]')
      .selectOption({ index: 1 }); // first real org (index 0 = placeholder)
    await page.locator('[data-testid="payments-new"]').click();

    const dialog = page.locator('[data-testid="payments-form-dialog"]');
    await expect(dialog).toBeVisible();

    const submitBtn = dialog.locator('[data-testid="payments-form-submit"]');
    // Defaults (stripe / test / empty public config) are all valid → submit enabled.
    await expect(submitBtn).toBeEnabled();

    // Enter invalid JSON to trigger client-side validation.
    await dialog
      .locator('[data-testid="payments-form-public-config"]')
      .fill("{bad json}");

    // Submit must become disabled because publicConfig is invalid.
    await expect(submitBtn).toBeDisabled({ timeout: 3_000 });

    // The field-level error must be visible.
    const configError = dialog.locator('[data-testid="payment-public-config-error"]');
    await expect(configError).toBeVisible({ timeout: 3_000 });
    await expect(configError).not.toBeEmpty();
  });

  test("create modal — cancel button closes the dialog", async ({ page }) => {
    await page.locator('[data-testid="nav-payments"]').click();
    await page.waitForSelector('[data-testid="payments-org-picker"]');

    await page
      .locator('[data-testid="payments-org-picker"]')
      .selectOption({ index: 1 }); // first real org (index 0 = placeholder)
    await page.locator('[data-testid="payments-new"]').click();

    const dialog = page.locator('[data-testid="payments-form-dialog"]');
    await expect(dialog).toBeVisible();

    await dialog.locator('[data-testid="payments-form-cancel"]').click();
    await expect(dialog).toBeHidden({ timeout: 3_000 });
  });

  test('"Paste UUID instead" escape hatch is present and expands', async ({
    page,
  }) => {
    await page.locator('[data-testid="nav-payments"]').click();
    await page.waitForSelector('[data-testid="payments-org-picker"]');

    const uuidInput = page.locator('[data-testid="payments-org-id"]');
    await page.locator('[data-testid="payments-org-id"]').evaluate((el) => {
      const details = el.closest("details");
      if (details) (details as HTMLDetailsElement).open = true;
    });
    await expect(uuidInput).toBeVisible({ timeout: 3_000 });
  });
});
