/**
 * Sales-link attribution — mock e2e (spec 43 section 5.1).
 *
 * A buyer who arrives through a poster QR lands on a page whose address
 * carries `ref` and `utm_*`. The widget reads them, keeps them for 7 days per
 * feed token and hands them to checkout/start. The platform is mocked with
 * page.route(); the test pins what the WIDGET sends.
 *
 * Reuses the promo-code demo page (feed token `promo-feed-token`).
 * Prerequisite: `npm run build` must be run before `npm run test:e2e`.
 */

import { test, expect, type Page } from '@playwright/test';

const FEED_TOKEN = 'promo-feed-token';
const EVENT_ID = 'event-promo-001';
const SESSION_ID = 'promo-session-001';
const TIER_ID = 'tier-promo-standard';
const STORAGE_KEY = `arena_attr:${FEED_TOKEN}`;

function eventSummary(): object {
  const start = new Date(Date.now() + 7 * 24 * 3600 * 1000).toISOString();
  const end = new Date(Date.now() + 7 * 24 * 3600 * 1000 + 2 * 3600 * 1000).toISOString();
  return {
    id: EVENT_ID,
    org_id: 'org-promo',
    venue_id: 'venue-promo',
    name: 'Promo Night',
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

function eventDetail(): object {
  const summary = eventSummary() as Record<string, unknown>;
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
          service_fee_percent: '0',
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

async function mockPlatform(page: Page): Promise<Array<Record<string, unknown>>> {
  const starts: Array<Record<string, unknown>> = [];
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/events`, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ events: [eventSummary()] }) }),
  );
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/events/${EVENT_ID}`, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(eventDetail()) }),
  );
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/checkout/start`, (route) => {
    starts.push(route.request().postDataJSON() as Record<string, unknown>);
    return route.fulfill({
      status: 201,
      contentType: 'application/json',
      body: JSON.stringify({
        checkout_session: {},
        checkout_token: 'ckout-attr-001',
        redirect_url: '',
        expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
      }),
    });
  });
  await page.route('**/v1/public/checkout/ckout-attr-001', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        status: 'pending',
        checkout_token: 'ckout-attr-001',
        checkout_session_id: 'cs-attr-001',
        items: [],
        tickets: [],
      }),
    }),
  );
  return starts;
}

/** Open the page at `query`, pick two tickets and press through to the pay call. */
async function buyTwoTickets(page: Page, query: string): Promise<void> {
  await page.goto(`/demo/promo-code.html${query}`);
  const widget = page.locator('#widget-promo');
  const plus = widget.getByRole('button', { name: 'Increase quantity for Standard' });
  await plus.waitFor({ timeout: 15_000 });
  await plus.click();
  await plus.click();
  await widget.locator('.mini-cart-btn').click();
  const sheet = widget.locator('.sheet');
  await expect(sheet).toBeVisible();
  await sheet.locator('.cta-btn').click();
  await sheet.locator('input[type="email"]').fill('buyer@example.com');
  await sheet.locator('button[type="submit"]').click();
}

test.describe('sales-link attribution', () => {
  test('an address with ref and utm labels reaches checkout/start', async ({ page }) => {
    const starts = await mockPlatform(page);
    await buyTwoTickets(page, '?ref=x7k2qpm&utm_source=qr&utm_medium=poster&utm_campaign=autumn');

    await expect.poll(() => starts.length).toBe(1);
    const attribution = starts[0]!['attribution'] as Record<string, string>;
    expect(attribution['ref']).toBe('x7k2qpm');
    expect(attribution['utm_source']).toBe('qr');
    expect(attribution['utm_medium']).toBe('poster');
    expect(attribution['utm_campaign']).toBe('autumn');
    expect(Date.parse(attribution['landed_at']!)).toBeGreaterThan(Date.now() - 60_000);
    expect(attribution).not.toHaveProperty('expires_at');
    expect(attribution).not.toHaveProperty('utm_term');

    // The touch is kept for 7 days under the feed token; the address is untouched.
    const stored = await page.evaluate((k) => localStorage.getItem(k), STORAGE_KEY);
    expect(JSON.parse(stored!)).toMatchObject({ ref: 'x7k2qpm', utm_source: 'qr' });
    expect(new URL(page.url()).search).toBe('?ref=x7k2qpm&utm_source=qr&utm_medium=poster&utm_campaign=autumn');
  });

  test('a later visit without labels still carries the stored touch', async ({ page }) => {
    const starts = await mockPlatform(page);
    await page.goto('/demo/promo-code.html?ref=x7k2qpm&utm_source=qr');
    await expect(page.locator('#widget-promo').getByRole('button', { name: 'Increase quantity for Standard' })).toBeVisible({ timeout: 15_000 });

    await buyTwoTickets(page, '');
    await expect.poll(() => starts.length).toBe(1);
    expect(starts[0]!['attribution']).toMatchObject({ ref: 'x7k2qpm', utm_source: 'qr' });
  });

  test('a new source keeps the old touch as first', async ({ page }) => {
    const starts = await mockPlatform(page);
    await page.goto('/demo/promo-code.html?ref=aaaaaaa&utm_source=qr');
    await expect(page.locator('#widget-promo').getByRole('button', { name: 'Increase quantity for Standard' })).toBeVisible({ timeout: 15_000 });

    await buyTwoTickets(page, '?utm_source=facebook&utm_campaign=autumn');
    await expect.poll(() => starts.length).toBe(1);
    expect(starts[0]!['attribution']).toMatchObject({
      utm_source: 'facebook',
      utm_campaign: 'autumn',
      first: { ref: 'aaaaaaa', utm_source: 'qr' },
    });
  });

  test('an address with no labels sends no attribution', async ({ page }) => {
    const starts = await mockPlatform(page);
    await buyTwoTickets(page, '');
    await expect.poll(() => starts.length).toBe(1);
    expect(starts[0]).not.toHaveProperty('attribution');
  });
});
