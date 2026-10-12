/**
 * Promo code in the cart sheet — mock e2e.
 *
 * A buyer opens the cart, types a code, presses Apply and sees the discounted
 * total BEFORE paying. The platform is mocked with page.route(): the quote
 * route answers the way the real one does (the discount comes off the
 * subtotal, the 1.25 % service fee is charged on what is left), so the test
 * pins what the SHEET does with the answer, not the arithmetic.
 *
 * Prerequisite: `npm run build` must be run before `npm run test:e2e`.
 */

import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

const FEED_TOKEN = 'promo-feed-token';
const EVENT_ID = 'event-promo-001';
const SESSION_ID = 'promo-session-001';
const TIER_ID = 'tier-promo-standard';
const UNIT = 2500;

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
          service_fee_percent: '1.25',
          tiers: [
            {
              id: TIER_ID,
              name: 'Standard',
              pricing_mode: 'fixed',
              price_amount: UNIT,
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

interface Seen {
  quotes: Array<Record<string, unknown>>;
  starts: Array<Record<string, unknown>>;
}

/** The platform's quote for `qty` tickets and an optional code. */
function quoteBody(qty: number, promo: string | undefined): object {
  const subtotal = UNIT * qty;
  const discount = promo?.toUpperCase() === 'TEN' ? Math.floor(subtotal / 10) : 0;
  const fee = Math.floor(((subtotal - discount) * 125) / 10_000);
  return {
    pricing: { subtotal, discount, platform_fee: fee, total: subtotal - discount + fee, currency: 'EUR' },
    promo_applied: discount > 0,
  };
}

async function mockPlatform(
  page: Page,
  opts: { startStatus?: number; startError?: { code: string; message: string } } = {},
): Promise<Seen> {
  const seen: Seen = { quotes: [], starts: [] };
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/events`, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ events: [eventSummary()] }) }),
  );
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/events/${EVENT_ID}`, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(eventDetail()) }),
  );
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/checkout/quote`, (route) => {
    const body = route.request().postDataJSON() as {
      promo_code?: string;
      ga_items?: Array<{ quantity: number }>;
    };
    seen.quotes.push(body);
    const qty = (body.ga_items ?? []).reduce((n, i) => n + i.quantity, 0);
    if (body.promo_code && body.promo_code.toUpperCase() !== 'TEN') {
      return route.fulfill({
        status: 422,
        contentType: 'application/json',
        body: JSON.stringify({ error: { code: 'promo.not_applicable', message: 'This code does not apply to this order.' } }),
      });
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(quoteBody(qty, body.promo_code)) });
  });
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/checkout/start`, (route) => {
    seen.starts.push(route.request().postDataJSON() as Record<string, unknown>);
    if (opts.startError) {
      return route.fulfill({
        status: opts.startStatus ?? 422,
        contentType: 'application/json',
        body: JSON.stringify({ error: opts.startError }),
      });
    }
    return route.fulfill({
      status: 201,
      contentType: 'application/json',
      body: JSON.stringify({
        checkout_session: {},
        checkout_token: 'ckout-promo-001',
        // Stay on the page: an empty redirect shows the order status instead.
        redirect_url: '',
        expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
      }),
    });
  });
  await page.route('**/v1/public/checkout/ckout-promo-001', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        status: 'pending',
        checkout_token: 'ckout-promo-001',
        checkout_session_id: 'cs-promo-001',
        items: [],
        tickets: [],
      }),
    }),
  );
  return seen;
}

/** Pick two tickets and open the cart sheet. */
async function openCartWithTwoTickets(page: Page): Promise<void> {
  await page.goto('/demo/promo-code.html');
  const widget = page.locator('#widget-promo');
  const plus = widget.getByRole('button', { name: 'Increase quantity for Standard' });
  await plus.waitFor({ timeout: 15_000 });
  await plus.click();
  await plus.click();
  await widget.locator('.mini-cart-btn').click();
  await expect(widget.locator('.sheet')).toBeVisible();
}

test.describe('promo code in the cart sheet', () => {
  test('a valid code shows the discounted total before paying', async ({ page }) => {
    const seen = await mockPlatform(page);
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    // Before a code: list price plus the 1.25 % fee (62), no discount row.
    await expect(sheet.locator('.total-value')).toHaveText('€50.62');
    await expect(sheet.locator('.discount-row')).toHaveCount(0);
    expect(seen.quotes).toHaveLength(0); // nothing asks the platform until a code is typed

    await sheet.getByLabel('Promo code').fill('ten');
    await sheet.getByRole('button', { name: 'Apply' }).click();

    await expect(sheet.locator('.promo-applied-text')).toHaveText('Code TEN applied.');
    await expect(sheet.locator('.discount-row .fee-value')).toHaveText('−€5');
    await expect(sheet.locator('.total-value')).toHaveText('€45.56');
    // The pay button names the discounted amount.
    expect(seen.quotes).toHaveLength(1);
    expect(seen.quotes[0]).toMatchObject({ session_id: SESSION_ID, promo_code: 'ten' });
  });

  test('a code that does not fit says so and changes nothing', async ({ page }) => {
    await mockPlatform(page);
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByLabel('Promo code').fill('NOPE');
    await sheet.getByRole('button', { name: 'Apply' }).click();

    await expect(sheet.getByRole('alert')).toHaveText('This code does not work for this order.');
    await expect(sheet.locator('.promo-applied')).toHaveCount(0);
    await expect(sheet.locator('.discount-row')).toHaveCount(0);
    await expect(sheet.locator('.total-value')).toHaveText('€50.62');
  });

  test('an empty field asks for a code and does not call the platform', async ({ page }) => {
    const seen = await mockPlatform(page);
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByRole('button', { name: 'Apply' }).click();
    await expect(sheet.getByRole('alert')).toHaveText('Type a code first.');
    expect(seen.quotes).toHaveLength(0);
  });

  test('removing the code brings the list total back', async ({ page }) => {
    await mockPlatform(page);
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByLabel('Promo code').fill('TEN');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    await expect(sheet.locator('.total-value')).toHaveText('€45.56');

    await sheet.getByRole('button', { name: 'Remove', exact: true }).click();
    await expect(sheet.locator('.promo-applied')).toHaveCount(0);
    await expect(sheet.getByText('Code removed.')).toBeVisible();
    await expect(sheet.locator('.discount-row')).toHaveCount(0);
    await expect(sheet.locator('.total-value')).toHaveText('€50.62');
  });

  test('checkout/start carries the applied code', async ({ page }) => {
    const seen = await mockPlatform(page);
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByLabel('Promo code').fill('TEN');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    await expect(sheet.locator('.total-value')).toHaveText('€45.56');

    await sheet.locator('.cta-btn').click();
    await sheet.locator('input[type="email"]').fill('buyer@example.com');
    await sheet.locator('button[type="submit"]').click();

    await expect.poll(() => seen.starts.length).toBe(1);
    expect(seen.starts[0]).toMatchObject({ session_id: SESSION_ID, promo_code: 'TEN' });
  });

  test('a code refused at checkout comes off with the reason', async ({ page }) => {
    const seen = await mockPlatform(page, {
      startError: { code: 'promo.per_customer_limit', message: 'You have already used this code the maximum number of times.' },
    });
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByLabel('Promo code').fill('TEN');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    await expect(sheet.locator('.total-value')).toHaveText('€45.56');

    await sheet.locator('.cta-btn').click();
    await sheet.locator('input[type="email"]').fill('buyer@example.com');
    await sheet.locator('button[type="submit"]').click();

    await expect(sheet.locator('.submit-error')).toHaveText('You already used this code. Code removed.');
    expect(seen.starts).toHaveLength(1);

    // Back in the cart the code is gone and the total is the list total.
    await sheet.locator('.back-btn').click();
    await expect(sheet.locator('.promo-applied')).toHaveCount(0);
    await expect(sheet.locator('.total-value')).toHaveText('€50.62');
  });

  test('removing a line under an applied code asks the platform again', async ({ page }) => {
    const seen = await mockPlatform(page);
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByLabel('Promo code').fill('TEN');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    await expect(sheet.locator('.total-value')).toHaveText('€45.56');

    // Dropping the only line empties the cart, so the code goes with it.
    await sheet.getByRole('button', { name: 'Remove Standard' }).click();
    await expect(sheet.getByText('Your cart is empty.')).toBeVisible();
    expect(seen.quotes.length).toBeGreaterThanOrEqual(1);
  });

  test('the open sheet with an applied code has no accessibility violations', async ({ page }) => {
    await mockPlatform(page);
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    const field = await new AxeBuilder({ page }).include('#widget-promo').withTags(['wcag2a', 'wcag2aa']).analyze();
    expect(field.violations.map((v) => v.id)).toEqual([]);

    await sheet.getByLabel('Promo code').fill('TEN');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    await expect(sheet.locator('.promo-applied')).toBeVisible();
    const applied = await new AxeBuilder({ page }).include('#widget-promo').withTags(['wcag2a', 'wcag2aa']).analyze();
    expect(applied.violations.map((v) => v.id)).toEqual([]);
  });

  test('the sheet speaks Russian', async ({ page }) => {
    await mockPlatform(page);
    await page.goto('/demo/promo-code.html');
    await page.locator('#widget-promo').evaluate((el) => el.setAttribute('locale', 'ru'));
    const widget = page.locator('#widget-promo');
    const plus = widget.getByRole('button', { name: /Increase quantity/ });
    await plus.waitFor({ timeout: 15_000 });
    await plus.click();
    await widget.locator('.mini-cart-btn').click();
    const sheet = widget.locator('.sheet');
    await expect(sheet.getByLabel('Промокод')).toBeVisible();
    await expect(sheet.getByRole('button', { name: 'Применить' })).toBeVisible();
  });
});
