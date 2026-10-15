/**
 * Checkout consent (P2) — mock e2e.
 *
 * When the feed session says `consent.required`, the buyer form shows the
 * "I accept the sale terms (seller: …) and the privacy policy" checkbox with
 * two links opening in a new tab, the pay button waits for the tick, and
 * checkout/start carries `consent.accepted`. A 422 checkout.consent_required
 * shows the buyer text, and when the box was not on screen (the feed said
 * "not required") the widget shows it instead of a dead end. Without the
 * requirement nothing changes: no box, no `consent` in the body.
 *
 * Prerequisite: `npm run build` must be run before `npm run test:e2e`.
 */

import { test, expect, type Page } from '@playwright/test';

const FEED_TOKEN = 'consent-feed-token';
const EVENT_ID = 'event-consent-001';
const SESSION_ID = 'consent-session-001';
const TIER_ID = 'tier-consent-standard';
const TERMS = 'https://site.example/legal/terms';
const PRIVACY = 'https://site.example/legal/privacy';
const REFUSAL = 'Please accept the sale terms and the privacy policy, then try again.';

function eventSummary(): Record<string, unknown> {
  const start = new Date(Date.now() + 7 * 24 * 3600 * 1000).toISOString();
  const end = new Date(Date.now() + 7 * 24 * 3600 * 1000 + 2 * 3600 * 1000).toISOString();
  return {
    id: EVENT_ID,
    org_id: 'org-consent',
    venue_id: 'venue-consent',
    name: 'Consent Night',
    description: null,
    status: 'published',
    start_at: start,
    end_at: end,
    visibility: 'public',
    image_url: null,
    created_at: start,
    updated_at: start,
  };
}

function eventDetail(required: boolean): object {
  const summary = eventSummary();
  return {
    event: {
      ...summary,
      sessions: [
        {
          id: SESSION_ID,
          start_at: summary['start_at'],
          end_at: summary['end_at'],
          capacity_total: 50,
          status: 'published',
          admission_mode: 'general_admission',
          buyer_fields: [],
          service_fee_percent: '0.00',
          promo_codes_available: false,
          consent: required
            ? { required: true, seller_legal_name: 'Teatro Demo S.L.', terms_url: TERMS, privacy_url: PRIVACY }
            : { required: false, terms_url: TERMS, privacy_url: PRIVACY },
          tiers: [
            {
              id: TIER_ID,
              name: 'Standard',
              pricing_mode: 'fixed',
              price_amount: 2500,
              currency: 'EUR',
              capacity: 50,
              available: 50,
              is_open: true,
              sort_order: 0,
            },
          ],
        },
      ],
    },
  };
}

/**
 * Mocks the platform. `refuse(n)` decides whether the n-th checkout/start
 * (1-based) is refused with 422 checkout.consent_required.
 */
async function mockPlatform(
  page: Page,
  opts: { required: boolean; refuse?: (n: number, body: Record<string, unknown>) => boolean },
): Promise<Array<Record<string, unknown>>> {
  const starts: Array<Record<string, unknown>> = [];
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/events`, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ events: [eventSummary()] }) }),
  );
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/events/${EVENT_ID}`, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(eventDetail(opts.required)) }),
  );
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/checkout/start`, (route) => {
    const body = route.request().postDataJSON() as Record<string, unknown>;
    starts.push(body);
    if (opts.refuse?.(starts.length, body)) {
      return route.fulfill({
        status: 422,
        contentType: 'application/json',
        body: JSON.stringify({
          error: { code: 'checkout.consent_required', message: 'Please accept the sale terms of the organizer and the platform policy, then try again.' },
        }),
      });
    }
    return route.fulfill({
      status: 201,
      contentType: 'application/json',
      body: JSON.stringify({
        checkout_session: {},
        checkout_token: 'ckout-consent-001',
        redirect_url: '',
        expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
      }),
    });
  });
  await page.route('**/v1/public/checkout/ckout-consent-001', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        status: 'pending',
        checkout_token: 'ckout-consent-001',
        checkout_session_id: 'cs-consent-001',
        items: [],
        tickets: [],
      }),
    }),
  );
  return starts;
}

/** Pick one ticket, open the cart sheet and go on to the buyer form. */
async function openBuyerForm(page: Page): Promise<void> {
  await page.goto('/demo/consent.html');
  const widget = page.locator('#widget-consent');
  const plus = widget.getByRole('button', { name: 'Increase quantity for Standard' });
  await plus.waitFor({ timeout: 15_000 });
  await plus.click();
  await widget.locator('.mini-cart-btn').click();
  const sheet = widget.locator('.sheet');
  await expect(sheet).toBeVisible();
  await sheet.locator('.cta-btn').click();
  await sheet.locator('input[type="email"]').fill('buyer@example.com');
}

test.describe('checkout consent in the buyer form', () => {
  test('a required consent holds the pay button and travels with the order', async ({ page }) => {
    const starts = await mockPlatform(page, { required: true });
    await openBuyerForm(page);
    const sheet = page.locator('#widget-consent .sheet');

    const consent = sheet.getByTestId('checkout-consent');
    await expect(consent).toBeVisible();
    await expect(consent).toContainText('I accept the sale terms (seller: Teatro Demo S.L.) and the privacy policy.');
    const terms = consent.getByRole('link', { name: 'sale terms' });
    const privacy = consent.getByRole('link', { name: 'privacy policy' });
    await expect(terms).toHaveAttribute('href', TERMS);
    await expect(terms).toHaveAttribute('target', '_blank');
    await expect(privacy).toHaveAttribute('href', PRIVACY);
    await expect(privacy).toHaveAttribute('target', '_blank');

    const pay = sheet.locator('button[type="submit"]');
    await expect(pay).toBeDisabled();
    await expect(sheet.getByText('Tick the box to continue to payment.')).toBeVisible();

    await sheet.getByTestId('checkout-consent-box').check();
    await expect(pay).toBeEnabled();
    await pay.click();

    await expect.poll(() => starts.length).toBe(1);
    expect(starts[0]).toMatchObject({ session_id: SESSION_ID, consent: { accepted: true, locale: 'en' } });
  });

  test('a refusal for a missing consent shows the buyer text', async ({ page }) => {
    await mockPlatform(page, { required: true, refuse: () => true });
    await openBuyerForm(page);
    const sheet = page.locator('#widget-consent .sheet');

    await sheet.getByTestId('checkout-consent-box').check();
    await sheet.locator('button[type="submit"]').click();
    await expect(sheet.locator('.submit-error')).toHaveText(REFUSAL);
  });

  test('without the requirement there is no box and no consent in the order', async ({ page }) => {
    const starts = await mockPlatform(page, { required: false });
    await openBuyerForm(page);
    const sheet = page.locator('#widget-consent .sheet');

    await expect(sheet.getByTestId('checkout-consent')).toHaveCount(0);
    const pay = sheet.locator('button[type="submit"]');
    await expect(pay).toBeEnabled();
    await pay.click();

    await expect.poll(() => starts.length).toBe(1);
    expect(starts[0]).not.toHaveProperty('consent');
  });

  test('a refusal the feed did not announce brings the box up instead of a dead end', async ({ page }) => {
    // The switch was turned on after the page loaded: the first order is
    // refused, the second one carries the tick.
    const starts = await mockPlatform(page, { required: false, refuse: (n) => n === 1 });
    await openBuyerForm(page);
    const sheet = page.locator('#widget-consent .sheet');

    await expect(sheet.getByTestId('checkout-consent')).toHaveCount(0);
    await sheet.locator('button[type="submit"]').click();
    await expect(sheet.locator('.submit-error')).toHaveText(REFUSAL);

    const box = sheet.getByTestId('checkout-consent-box');
    await expect(box).toBeVisible();
    const pay = sheet.locator('button[type="submit"]');
    await expect(pay).toBeDisabled();
    await box.check();
    await expect(pay).toBeEnabled();
    await pay.click();

    await expect.poll(() => starts.length).toBe(2);
    expect(starts[1]).toMatchObject({ consent: { accepted: true } });
  });
});
