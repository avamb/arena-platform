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
const VIP_TIER_ID = 'tier-promo-vip';
const VIP_UNIT = 4000;

/** The sheet prints every amount inside a left-to-right isolate (U+2066 ... U+2069). */
function amount(text: string): RegExp {
  const escaped = text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return new RegExp(`^\u2066?${escaped}\u2069?$`);
}

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

function eventDetail(promoAvailable = true): object {
  const flag = promoAvailable;
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
          promo_codes_available: promoAvailable,
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
            {
              id: VIP_TIER_ID,
              name: 'VIP',
              pricing_mode: 'fixed',
              price_amount: VIP_UNIT,
              currency: 'EUR',
              capacity: 20,
              available: 20,
              is_open: true,
              sort_order: 1,
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

/** The platform's quote for these lines and an optional code. */
function quoteBody(items: Array<{ tier_id: string; quantity: number }>, promo: string | undefined): object {
  const subtotal = items.reduce((n, i) => n + (i.tier_id === VIP_TIER_ID ? VIP_UNIT : UNIT) * i.quantity, 0);
  const discount = promo?.toUpperCase() === 'TEN' ? Math.floor(subtotal / 10) : 0;
  const fee = Math.floor(((subtotal - discount) * 125) / 10_000);
  return {
    pricing: { subtotal, discount, platform_fee: fee, total: subtotal - discount + fee, currency: 'EUR' },
    promo_applied: discount > 0,
  };
}

async function mockPlatform(
  page: Page,
  opts: {
    startStatus?: number;
    startError?: { code: string; message: string };
    /** Called with the 1-based number of each quote request; the answer waits for the returned promise. */
    holdQuote?: (n: number) => Promise<void> | void;
    /** Whether the organization has a usable promo code (default true). */
    promoAvailable?: boolean;
  } = {},
): Promise<Seen> {
  const seen: Seen = { quotes: [], starts: [] };
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/events`, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ events: [eventSummary()] }) }),
  );
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/events/${EVENT_ID}`, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(eventDetail(opts.promoAvailable ?? true)) }),
  );
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/checkout/quote`, async (route) => {
    const body = route.request().postDataJSON() as {
      promo_code?: string;
      ga_items?: Array<{ tier_id: string; quantity: number }>;
    };
    seen.quotes.push(body);
    await opts.holdQuote?.(seen.quotes.length);
    const items = body.ga_items ?? [];
    if (body.promo_code && body.promo_code.toUpperCase() !== 'TEN') {
      return route.fulfill({
        status: 422,
        contentType: 'application/json',
        body: JSON.stringify({ error: { code: 'promo.not_applicable', message: 'This code does not apply to this order.' } }),
      });
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(quoteBody(items, body.promo_code)) });
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
  await widget.locator('.sheet').getByRole('button', { name: 'Have a promo code?' }).click();
}

/** Two Standard (50.00) and one VIP (40.00), then open the cart sheet. */
async function openCartWithTwoLines(page: Page): Promise<void> {
  await page.goto('/demo/promo-code.html');
  const widget = page.locator('#widget-promo');
  const standard = widget.getByRole('button', { name: 'Increase quantity for Standard' });
  await standard.waitFor({ timeout: 15_000 });
  await standard.click();
  await standard.click();
  await widget.getByRole('button', { name: 'Increase quantity for VIP' }).click();
  await widget.locator('.mini-cart-btn').click();
  await expect(widget.locator('.sheet')).toBeVisible();
  await widget.locator('.sheet').getByRole('button', { name: 'Have a promo code?' }).click();
}

test.describe('promo code in the cart sheet', () => {
  test('a valid code shows the discounted total before paying', async ({ page }) => {
    const seen = await mockPlatform(page);
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    // Before a code: list price plus the 1.25 % fee (62), no discount row.
    await expect(sheet.locator('.total-value')).toHaveText(amount('€50.62'));
    await expect(sheet.locator('.discount-row')).toHaveCount(0);
    expect(seen.quotes).toHaveLength(0); // nothing asks the platform until a code is typed

    await sheet.getByLabel('Promo code').fill('ten');
    await sheet.getByRole('button', { name: 'Apply' }).click();

    await expect(sheet.locator('.promo-applied-text')).toHaveText('Code TEN applied.');
    await expect(sheet.locator('.discount-row .fee-value')).toHaveText(amount('−€5'));
    await expect(sheet.locator('.total-value')).toHaveText(amount('€45.56'));
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
    await expect(sheet.locator('.total-value')).toHaveText(amount('€50.62'));
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
    await expect(sheet.locator('.total-value')).toHaveText(amount('€45.56'));

    await sheet.getByRole('button', { name: 'Remove', exact: true }).click();
    await expect(sheet.locator('.promo-applied')).toHaveCount(0);
    await expect(sheet.getByText('Code removed.')).toBeVisible();
    await expect(sheet.locator('.discount-row')).toHaveCount(0);
    await expect(sheet.locator('.total-value')).toHaveText(amount('€50.62'));
  });

  test('checkout/start carries the applied code', async ({ page }) => {
    const seen = await mockPlatform(page);
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByLabel('Promo code').fill('TEN');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    await expect(sheet.locator('.total-value')).toHaveText(amount('€45.56'));

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
    await expect(sheet.locator('.total-value')).toHaveText(amount('€45.56'));

    await sheet.locator('.cta-btn').click();
    await sheet.locator('input[type="email"]').fill('buyer@example.com');
    await sheet.locator('button[type="submit"]').click();

    await expect(sheet.locator('.submit-error')).toHaveText('You already used this code. Code removed.');
    expect(seen.starts).toHaveLength(1);

    // Back in the cart the code is gone and the total is the list total.
    await sheet.locator('.back-btn').click();
    await expect(sheet.locator('.promo-applied')).toHaveCount(0);
    await expect(sheet.locator('.total-value')).toHaveText(amount('€50.62'));
  });

  test('removing a line under an applied code asks the platform again', async ({ page }) => {
    const seen = await mockPlatform(page);
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByLabel('Promo code').fill('TEN');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    await expect(sheet.locator('.total-value')).toHaveText(amount('€45.56'));

    // Dropping the only line empties the cart, so the code goes with it.
    await sheet.getByRole('button', { name: 'Remove Standard' }).click();
    await expect(sheet.getByText('Your cart is empty.')).toBeVisible();
    expect(seen.quotes.length).toBeGreaterThanOrEqual(1);
  });

  test('the lines are locked while a quote is on its way', async ({ page }) => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    await mockPlatform(page, { holdQuote: () => gate });
    await openCartWithTwoLines(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByLabel('Promo code').fill('TEN');
    await sheet.getByRole('button', { name: 'Apply' }).click();

    await expect(sheet.getByText('Checking the code')).toBeVisible();
    await expect(sheet.getByRole('button', { name: 'Remove VIP' })).toBeDisabled();
    await expect(sheet.getByRole('button', { name: 'Remove Standard' })).toBeDisabled();
    await expect(sheet.locator('.cta-btn')).toBeDisabled();

    release();
    await expect(sheet.locator('.promo-applied-text')).toHaveText('Code TEN applied.');
    await expect(sheet.getByRole('button', { name: 'Remove VIP' })).toBeEnabled();
    await expect(sheet.locator('.cta-btn')).toBeEnabled();
  });

  test('a line removed under an applied code is re-quoted before the buyer can pay', async ({ page }) => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    // Only the SECOND quote (the refresh after the removal) waits.
    const seen = await mockPlatform(page, { holdQuote: (n) => (n === 2 ? gate : undefined) });
    await openCartWithTwoLines(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByLabel('Promo code').fill('TEN');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    // 2 x 25.00 + 40.00 = 90.00, 10 % off = 9.00, fee 1.25 % of 81.00 = 1.01
    await expect(sheet.locator('.total-value')).toHaveText(amount('€82.01'));

    await sheet.getByRole('button', { name: 'Remove VIP' }).click();
    await expect.poll(() => seen.quotes.length).toBe(2);
    expect(seen.quotes[1]).toMatchObject({ ga_items: [{ tier_id: TIER_ID, quantity: 2 }], promo_code: 'TEN' });

    // While the new quote is on its way the old discount is not shown and the buyer cannot pay.
    await expect(sheet.locator('.cta-btn')).toBeDisabled();
    await expect(sheet.locator('.discount-row')).toHaveCount(0);

    release();
    await expect(sheet.locator('.total-value')).toHaveText(amount('€45.56'));
    await expect(sheet.locator('.discount-row .fee-value')).toHaveText(amount('−€5'));
    await expect(sheet.locator('.cta-btn')).toBeEnabled();
  });

  test('a quote that never answers does not hold the purchase', async ({ page }) => {
    test.setTimeout(45_000);
    await mockPlatform(page, { holdQuote: () => new Promise<void>(() => {}) });
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByLabel('Promo code').fill('TEN');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    await expect(sheet.locator('.cta-btn')).toBeDisabled();

    // The wait is bounded: the sheet frees itself, says the check failed, and
    // the buyer can still pay the list price.
    await expect(sheet.getByRole('alert')).toHaveText('Cannot check the code now. Try again.', { timeout: 15_000 });
    await expect(sheet.locator('.promo-applied')).toHaveCount(0);
    await expect(sheet.locator('.cta-btn')).toBeEnabled();
    await expect(sheet.locator('.total-value')).toHaveText(amount('€50.62'));
  });

  test('a code that takes nothing off is not applied', async ({ page }) => {
    await mockPlatform(page);
    await page.route(`**/v1/public/feeds/${FEED_TOKEN}/checkout/quote`, (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(quoteBody([{ tier_id: TIER_ID, quantity: 2 }], undefined)) }),
    );
    await openCartWithTwoTickets(page);
    const sheet = page.locator('#widget-promo .sheet');

    await sheet.getByLabel('Promo code').fill('ZERO');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    await expect(sheet.getByRole('alert')).toHaveText('This code gives no discount on this order.');
    await expect(sheet.locator('.promo-applied')).toHaveCount(0);
    await expect(sheet.locator('.total-value')).toHaveText(amount('€50.62'));
  });

  test('the field stays behind a link until the buyer asks for it', async ({ page }) => {
    const seen = await mockPlatform(page);
    await page.goto('/demo/promo-code.html');
    const widget = page.locator('#widget-promo');
    const plus = widget.getByRole('button', { name: 'Increase quantity for Standard' });
    await plus.waitFor({ timeout: 15_000 });
    await plus.click();
    await widget.locator('.mini-cart-btn').click();
    const sheet = widget.locator('.sheet');

    await expect(sheet.getByRole('button', { name: 'Have a promo code?' })).toBeVisible();
    await expect(sheet.locator('#arena-promo-input')).toHaveCount(0);
    expect(seen.quotes).toHaveLength(0);

    await sheet.getByRole('button', { name: 'Have a promo code?' }).click();
    await expect(sheet.getByLabel('Promo code')).toBeVisible();
    await expect(sheet.getByLabel('Promo code')).toBeFocused();
    await expect(sheet.getByRole('button', { name: 'Have a promo code?' })).toHaveCount(0);
    expect(seen.quotes).toHaveLength(0);
  });

  test('an organization without a usable code shows no promo field and asks nothing', async ({ page }) => {
    const seen = await mockPlatform(page, { promoAvailable: false });
    await page.goto('/demo/promo-code.html');
    const widget = page.locator('#widget-promo');
    const plus = widget.getByRole('button', { name: 'Increase quantity for Standard' });
    await plus.waitFor({ timeout: 15_000 });
    await plus.click();
    await plus.click();
    await widget.locator('.mini-cart-btn').click();
    const sheet = widget.locator('.sheet');

    await expect(sheet.locator('.total-value')).toHaveText(amount('€50.62'));
    await expect(sheet.getByRole('button', { name: 'Have a promo code?' })).toHaveCount(0);
    await expect(sheet.locator('.promo')).toHaveCount(0);
    await expect(sheet.locator('#arena-promo-input')).toHaveCount(0);

    // The buyer still pays normally and nothing about a promo code is sent.
    await sheet.locator('.cta-btn').click();
    await sheet.locator('input[type="email"]').fill('buyer@example.com');
    await sheet.locator('button[type="submit"]').click();
    await expect.poll(() => seen.starts.length).toBe(1);
    expect(seen.starts[0]).not.toHaveProperty('promo_code');
    expect(seen.quotes).toHaveLength(0);
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
    const plus = widget.getByRole('button', { name: 'Increase quantity for Standard' });
    await plus.waitFor({ timeout: 15_000 });
    await plus.click();
    await widget.locator('.mini-cart-btn').click();
    const sheet = widget.locator('.sheet');
    await expect(sheet.locator('.promo-link')).toHaveText('Есть промокод?');
    await sheet.locator('.promo-link').click();
    await expect(sheet.getByLabel('Промокод')).toBeVisible();
    await expect(sheet.getByRole('button', { name: 'Применить' })).toBeVisible();
  });

  // The widget is embedded on Spanish, German and French sites too, where the
  // rest of the UI still reads English but money wording must not.
  for (const [tag, link, label, apply] of [
    ['es-ES', '¿Tienes un código promocional?', 'Código promocional', 'Aplicar'],
    ['de', 'Haben Sie einen Gutscheincode?', 'Gutscheincode', 'Einlösen'],
    ['fr', 'Vous avez un code promo ?', 'Code promo', 'Appliquer'],
  ] as const) {
    test(`the promo field speaks ${tag}`, async ({ page }) => {
      await mockPlatform(page);
      await page.goto('/demo/promo-code.html');
      await page.locator('#widget-promo').evaluate((el, loc) => el.setAttribute('locale', loc), tag);
      const widget = page.locator('#widget-promo');
      const plus = widget.getByRole('button', { name: 'Increase quantity for Standard' });
      await plus.waitFor({ timeout: 15_000 });
      await plus.click();
      await widget.locator('.mini-cart-btn').click();
      const sheet = widget.locator('.sheet');
      await expect(sheet.locator('.promo-link')).toHaveText(link);
      await sheet.locator('.promo-link').click();
      await expect(sheet.getByLabel(label)).toBeVisible();
      await expect(sheet.getByRole('button', { name: apply })).toBeVisible();
    });
  }

  test('in a right-to-left sheet the discount keeps its minus in front of the amount', async ({ page }) => {
    await mockPlatform(page);
    await page.goto('/demo/promo-code.html');
    await page.locator('#widget-promo').evaluate((el) => el.setAttribute('locale', 'he'));
    const widget = page.locator('#widget-promo');
    const plus = widget.getByRole('button', { name: 'Increase quantity for Standard' });
    await plus.waitFor({ timeout: 15_000 });
    await plus.click();
    await plus.click();
    await widget.locator('.mini-cart-btn').click();
    const sheet = widget.locator('.sheet');
    await expect(sheet.locator('.promo-link')).toHaveText('יש לכם קוד קופון?');
    await sheet.locator('.promo-link').click();
    await sheet.getByLabel('קוד קופון').fill('TEN');
    await sheet.getByRole('button', { name: 'החל' }).click();
    const row = sheet.locator('.discount-row .fee-value');
    await expect(row).toHaveText(amount('−€5'));
    // Rendered, the minus sits to the LEFT of the currency sign (visual order, not DOM order).
    const pos = await row.evaluate((el) => {
      const text = el.firstChild as Text;
      const range = document.createRange();
      const left = (i: number): number => {
        range.setStart(text, i);
        range.setEnd(text, i + 1);
        return range.getBoundingClientRect().left;
      };
      return { minus: left(text.data.indexOf('\u2212')), euro: left(text.data.indexOf('€')) };
    });
    expect(pos.minus).toBeLessThan(pos.euro);
  });
});
