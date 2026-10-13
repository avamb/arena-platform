/**
 * Promo code in the cart sheet — REAL backend acceptance.
 *
 * No page.route() mocks: the widget talks to the actual arena-api through the
 * proxy (scripts/serve-demo-real.cjs). The Stripe stub only stands in for the
 * payment provider.
 *
 * What it proves against the live code and database:
 *  - Apply prices the cart on the platform and the sheet shows those numbers;
 *  - the quote holds nothing (the category's free places do not move);
 *  - the total the sheet showed is the total checkout/start stored;
 *  - a code that does not fit is refused with the one neutral answer.
 *
 * Prerequisites: those of palac-akropolis-real.e2e.ts, plus the promo codes of
 * seed-palac-e2e.sql (section 13): E2E10 (10 %), E2EOLD (expired), E2EBIG
 * (needs a 10 000.00 order).
 *
 * Run with: npx playwright test --config playwright.config.real.ts
 */

import { test, expect, type Page, type Response } from '@playwright/test';

const FEED_TOKEN = 'palac-akropolis-e2e-token-v1';
const EVENT_ID = 'fe000007-0000-7000-8000-000000000001';
const SESSION_ID = 'fe000007-0000-7000-8000-000000000002';
const TIER_GALERIE = 'fe000007-0000-7000-8000-000000000006';
const DEMO_PAGE = '/demo/palac-akropolis-real.html';
const WIDGET = '#widget-hybrid-en';

/** Free places of the Galérie category as the public feed reports them. */
async function galerieAvailable(page: Page): Promise<number | null> {
  const res = await page.request.get(`/v1/public/feeds/${FEED_TOKEN}/events/${EVENT_ID}`);
  expect(res.status()).toBe(200);
  const body = (await res.json()) as {
    event: { sessions: Array<{ id: string; tiers: Array<{ id: string; available?: number | null }> }> };
  };
  const tier = body.event.sessions.find((s) => s.id === SESSION_ID)?.tiers.find((t) => t.id === TIER_GALERIE);
  return tier?.available ?? null;
}

/** Pick `qty` Galérie places and open the cart sheet. */
async function openCart(page: Page, qty: number): Promise<void> {
  await page.goto(DEMO_PAGE);
  const widget = page.locator(WIDGET);
  const plus = widget.getByRole('button', { name: /Increase quantity for Gal/ });
  await plus.waitFor({ timeout: 20_000 });
  for (let i = 0; i < qty; i++) await plus.click();
  await widget.locator('.mini-cart-btn').click();
  await expect(widget.locator('.sheet')).toBeVisible();
  // The organization has promo codes, so the field sits behind a link.
  await widget.locator('.sheet').getByRole('button', { name: 'Have a promo code?' }).click();
}

/** "€21.64" -> 2164 */
function minor(text: string): number {
  const digits = text.replace(/[^\d.,]/g, '').replace(',', '.');
  return Math.round(parseFloat(digits) * 100);
}

function isQuote(r: Response): boolean {
  return r.url().endsWith('/checkout/quote') && r.request().method() === 'POST';
}

test.describe('promo code — real backend', () => {
  test('the feed tells the widget the organization has a usable code', async ({ page }) => {
    const res = await page.request.get(`/v1/public/feeds/${FEED_TOKEN}/events/${EVENT_ID}`);
    expect(res.status()).toBe(200);
    const body = (await res.json()) as { event: { sessions: Array<{ id: string; promo_codes_available?: boolean }> } };
    expect(body.event.sessions.find((x) => x.id === SESSION_ID)?.promo_codes_available).toBe(true);
    // Only the existence is public: no code text anywhere in the answer.
    expect(JSON.stringify(body)).not.toContain('E2E10');
  });

  test('the quote holds nothing and the sheet total is the stored checkout total', async ({ page }) => {
    const before = await galerieAvailable(page);
    await openCart(page, 2);
    const sheet = page.locator(`${WIDGET} .sheet`);

    const listTotal = minor((await sheet.locator('.total-value').innerText()) ?? '');

    const quoteResponse = page.waitForResponse(isQuote);
    await sheet.getByLabel('Promo code').fill('e2e10');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    const quote = await quoteResponse;
    expect(quote.status()).toBe(200);
    const q = (await quote.json()) as {
      pricing: { subtotal: number; discount: number; platform_fee: number; total: number; currency: string };
      promo_applied: boolean;
    };
    expect(q.promo_applied).toBe(true);
    expect(q.pricing.subtotal).toBe(2400);
    expect(q.pricing.discount).toBe(240);
    expect(q.pricing.total).toBe(q.pricing.subtotal - q.pricing.discount + q.pricing.platform_fee);

    // The sheet shows the platform's numbers, and the discount made it cheaper.
    await expect(sheet.locator('.promo-applied-text')).toHaveText('Code E2E10 applied.');
    await expect(sheet.locator('.discount-row')).toBeVisible();
    const sheetTotal = minor(await sheet.locator('.total-value').innerText());
    expect(sheetTotal).toBe(q.pricing.total);
    expect(sheetTotal).toBeLessThan(listTotal);

    // A quote holds nothing: the category still has every place it had.
    expect(await galerieAvailable(page)).toBe(before);

    // Pay: what checkout/start stored is what the sheet promised.
    await sheet.locator('.cta-btn').click();
    // The widget leaves for the payment page the moment start answers, which
    // drops the response body: read it on the way through (a pass-through, the
    // request still reaches the real backend).
    let started: { checkout_token: string } | null = null;
    let startStatus = 0;
    let startBody: Record<string, unknown> = {};
    await page.route('**/checkout/start', async (route) => {
      const real = await route.fetch();
      startStatus = real.status();
      startBody = route.request().postDataJSON() as Record<string, unknown>;
      started = (await real.json()) as { checkout_token: string };
      await route.fulfill({ response: real });
    });
    const form = sheet.locator('form');
    for (const input of await form.locator('input').all()) {
      const type = (await input.getAttribute('type')) ?? 'text';
      if (type === 'email') await input.fill('promo-e2e@arena-e2e.test');
      else if (type === 'tel') await input.fill('+420777000111');
      else if (type === 'text') await input.fill('Promo E2E Buyer');
    }
    await sheet.locator('button[type="submit"]').click();
    await expect.poll(() => startStatus).toBe(201);
    expect(startBody).toMatchObject({ session_id: SESSION_ID, promo_code: 'e2e10' });
    const token = (started as { checkout_token: string } | null)?.checkout_token ?? '';
    expect(token).not.toBe('');

    const status = await page.request.get(`/v1/public/checkout/${token}`);
    expect(status.status()).toBe(200);
    const stored = (await status.json()) as { total: number; discount: number; subtotal: number; currency: string };
    expect(stored.subtotal).toBe(q.pricing.subtotal);
    expect(stored.discount).toBe(q.pricing.discount);
    expect(stored.total).toBe(q.pricing.total);
    expect(stored.currency).toBe(q.pricing.currency);
  });

  test('a code that does not fit gets the one neutral answer and changes nothing', async ({ page }) => {
    await openCart(page, 1);
    const sheet = page.locator(`${WIDGET} .sheet`);
    const listTotal = await sheet.locator('.total-value').innerText();

    for (const code of ['E2EOLD', 'NO-SUCH-CODE']) {
      const quoteResponse = page.waitForResponse(isQuote);
      await sheet.getByLabel('Promo code').fill(code);
      await sheet.getByRole('button', { name: 'Apply' }).click();
      const quote = await quoteResponse;
      expect(quote.status(), code).toBe(422);
      const body = (await quote.json()) as { error: { code: string; message: string } };
      expect(body.error.code, code).toBe('promo.not_applicable');
      await expect(sheet.getByRole('alert')).toHaveText('This code does not work for this order.');
      await expect(sheet.locator('.promo-applied')).toHaveCount(0);
      await expect(sheet.locator('.total-value')).toHaveText(listTotal);
    }
  });

  test('an order below the code minimum says so', async ({ page }) => {
    await openCart(page, 1);
    const sheet = page.locator(`${WIDGET} .sheet`);

    const quoteResponse = page.waitForResponse(isQuote);
    await sheet.getByLabel('Promo code').fill('E2EBIG');
    await sheet.getByRole('button', { name: 'Apply' }).click();
    const quote = await quoteResponse;
    expect(quote.status()).toBe(422);
    expect(((await quote.json()) as { error: { code: string } }).error.code).toBe('promo.minimum_not_reached');
    await expect(sheet.getByRole('alert')).toHaveText('The order total is below the minimum for this code. Add tickets.');
  });
});
