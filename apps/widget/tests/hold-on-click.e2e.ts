/**
 * Seat hold at the click (spec 67) — MOCK suite: every /v1/* call is answered
 * by page.route().
 *
 * The hold code is a lazy chunk (src/lib/holdRuntime.ts, loaded by
 * ArenaTickets.svelte with import()). These tests prove it works in a real
 * browser: the chunk is requested for a seated session, a click on a free seat
 * posts a hold, a second click takes it off, the order starts with the hold's
 * key, a platform without the feature falls back to the plain selection, and a
 * general-admission page never asks for the chunk.
 *
 * Prerequisite: `npm run build` (dist/v1/arena-tickets-holdRuntime-*.js).
 */

import { test, expect, type Page, type Request } from '@playwright/test';

const SESSION = 'kbd-session-001';
const FEED = 'kbd-feed-token';
const HOLD_TOKEN = 'hold-tok-001';
const CHUNK = /arena-tickets-holdRuntime-.*\.js/;

function schema(): object {
  return {
    session_id: SESSION,
    event_id: 'kbd-event-001',
    admission_mode: 'assigned_seats',
    seating_plan_version_id: 'spv-hold-001',
    seat_status_version: 1,
    geometry_checksum: 'hold-checksum',
    capacity_seated: 4,
    capacity_standing: 0,
    geometry: {
      schema_version: 1,
      canvas: { width: 400, height: 200 },
      categories: [{ index: 0, name: 'Gold', color: '#D97706', price_hint: '30.00', currency_hint: 'EUR' }],
      sections: [
        {
          key: 'parter',
          name: 'Parter',
          rows: [
            {
              key: 'parter-row-A',
              name: 'A',
              seats: [1, 2, 3, 4].map((n) => ({
                key: `A${n}`,
                number: String(n),
                x: 60 + n * 70,
                y: 100,
                radius: 14,
                category_index: 0,
                barcode_hint: null,
              })),
            },
          ],
        },
      ],
      standing_zones: [],
      tables: [],
      decor_svg: '',
    },
    category_prices: [
      { index: 0, name: 'Gold', color: '#D97706', price_hint: '30.00', currency_hint: 'EUR', pricing_mode: 'fixed', price_amount: 3000, currency: 'EUR' },
    ],
  };
}

function statuses(): object {
  return {
    session_id: SESSION,
    status_version: 1,
    delta: false,
    seats: { A1: 'available', A2: 'available', A3: 'available', A4: 'available' },
  };
}

function holdAnswer(seatKeys: string[]): object {
  return {
    hold_token: HOLD_TOKEN,
    session_id: SESSION,
    seat_keys: seatKeys,
    step: 'select',
    expires_at: new Date(Date.now() + 10 * 60 * 1000).toISOString(),
    extend_available: false,
  };
}

function json(body: unknown, status = 200): { status: number; contentType: string; body: string } {
  return { status, contentType: 'application/json', body: JSON.stringify(body) };
}

interface Seen {
  posts: Record<string, unknown>[];
  deletes: string[];
  starts: Record<string, unknown>[];
  chunks: string[];
}

/** Routes for the seated page. `postHold` answers POST .../holds. */
async function mock(
  page: Page,
  postHold: (body: Record<string, unknown>) => { status: number; contentType: string; body: string },
): Promise<Seen> {
  const seen: Seen = { posts: [], deletes: [], starts: [], chunks: [] };
  page.on('request', (req: Request) => {
    if (CHUNK.test(req.url())) seen.chunks.push(req.url());
  });
  await page.route(`**/v1/event-sessions/${SESSION}/schema`, (route) => route.fulfill(json(schema())));
  await page.route(`**/v1/event-sessions/${SESSION}/seat-status**`, (route) => route.fulfill(json(statuses())));
  await page.route(`**/v1/public/feeds/${FEED}/holds`, (route) => {
    const body = route.request().postDataJSON() as Record<string, unknown>;
    seen.posts.push(body);
    return route.fulfill(postHold(body));
  });
  await page.route('**/v1/public/holds/*/seats/*', (route) => {
    seen.deletes.push(`${route.request().method()} ${new URL(route.request().url()).pathname}`);
    // The only seat went: an empty hold.
    return route.fulfill(json({ ...(holdAnswer([]) as object), seat_keys: [] }));
  });
  await page.route('**/v1/public/holds/*', (route) => {
    if (route.request().method() === 'GET') return route.fulfill(json({ error: { code: 'hold.expired', message: 'gone' } }, 404));
    return route.fulfill({ status: 204 });
  });
  await page.route(`**/v1/public/feeds/${FEED}/checkout/start`, (route) => {
    seen.starts.push(route.request().postDataJSON() as Record<string, unknown>);
    return route.fulfill(
      json(
        {
          checkout_session: {},
          checkout_token: 'ckout-hold-001',
          redirect_url: '',
          expires_at: new Date(Date.now() + 15 * 60 * 1000).toISOString(),
        },
        201,
      ),
    );
  });
  await page.route('**/v1/public/checkout/ckout-hold-001**', (route) =>
    route.fulfill(
      json({ status: 'pending', checkout_token: 'ckout-hold-001', checkout_session_id: 'cs-hold-001', items: [], tickets: [] }),
    ),
  );
  return seen;
}

async function open(page: Page): Promise<void> {
  await page.goto('/demo/populated-map.html');
  await expect(page.locator('circle[data-seat-key="A1"][data-status="available"]')).toHaveCount(1, { timeout: 15_000 });
}

const seat = (page: Page, key: string) => page.locator(`circle[data-seat-key="${key}"]`);
const selected = (page: Page) => page.locator('circle[data-selected="true"]');
const clock = (page: Page) => page.getByTestId('hold-countdown');

test.describe('Seat hold at the click (spec 67)', () => {
  test('a click on a free seat posts a hold, marks the seat as own and shows the clock', async ({ page }) => {
    const seen = await mock(page, (b) => json(holdAnswer(b['seat_keys'] as string[]), 201));
    await open(page);
    await seat(page, 'A2').click();

    await expect.poll(() => seen.posts.length).toBe(1);
    expect(seen.posts[0]).toMatchObject({ session_id: SESSION, seat_keys: ['A2'] });
    await expect(seat(page, 'A2')).toHaveAttribute('data-hold', 'own');
    await expect(selected(page)).toHaveCount(1);
    await expect(clock(page)).toBeVisible();
    expect(seen.chunks.length).toBeGreaterThan(0);
  });

  test('a click on the held seat takes it off with a DELETE and the seat is no longer selected', async ({ page }) => {
    const seen = await mock(page, (b) => json(holdAnswer(b['seat_keys'] as string[]), 201));
    await open(page);
    await seat(page, 'A2').click();
    await expect(seat(page, 'A2')).toHaveAttribute('data-hold', 'own');

    await seat(page, 'A2').click();
    await expect.poll(() => seen.deletes.length).toBe(1);
    expect(seen.deletes[0]).toBe(`DELETE /v1/public/holds/${HOLD_TOKEN}/seats/A2`);
    await expect(selected(page)).toHaveCount(0);
    await expect(seat(page, 'A2')).not.toHaveAttribute('data-hold', /.+/);
    await expect(clock(page)).toHaveCount(0);
  });

  test('starting the checkout sends the hold_token instead of the seat list', async ({ page }) => {
    const seen = await mock(page, (b) => json(holdAnswer(b['seat_keys'] as string[]), 201));
    await open(page);
    await seat(page, 'A3').click();
    await expect(clock(page)).toBeVisible();

    const widget = page.locator('#test-widget');
    await widget.locator('.mini-cart-btn').click();
    const sheet = widget.locator('.sheet');
    await expect(sheet).toBeVisible();
    await sheet.locator('.cta-btn').click();
    await sheet.locator('input[type="email"]').fill('buyer@example.com');
    await sheet.locator('button[type="submit"]').click();

    await expect.poll(() => seen.starts.length).toBeGreaterThan(0);
    expect(seen.starts[0]).toMatchObject({ session_id: SESSION, hold_token: HOLD_TOKEN });
    expect(seen.starts[0]!['seats']).toBeUndefined();
  });

  test('a platform that answers hold.disabled falls back to the plain selection with no clock', async ({ page }) => {
    const seen = await mock(page, () => json({ error: { code: 'hold.disabled', message: 'Holds are off.' } }, 404));
    await open(page);
    await seat(page, 'A2').click();

    await expect.poll(() => seen.posts.length).toBe(1);
    await expect(selected(page)).toHaveCount(1);
    await expect(seat(page, 'A2')).toHaveAttribute('data-selected', 'true');
    await expect(clock(page)).toHaveCount(0);
    await expect(seat(page, 'A2')).not.toHaveAttribute('data-hold', 'own');
  });

  test('a general-admission page never requests the hold chunk', async ({ page }) => {
    const chunks: string[] = [];
    page.on('request', (req) => {
      if (CHUNK.test(req.url())) chunks.push(req.url());
    });
    const start = new Date(Date.now() + 7 * 24 * 3600 * 1000).toISOString();
    const summary = {
      id: 'event-consent-001',
      org_id: 'org-consent',
      venue_id: 'venue-consent',
      name: 'GA Night',
      description: null,
      status: 'published',
      start_at: start,
      end_at: start,
      visibility: 'public',
      image_url: null,
      created_at: start,
      updated_at: start,
    };
    await page.route('**/v1/public/feeds/consent-feed-token/events', (route) => route.fulfill(json({ events: [summary] })));
    await page.route('**/v1/public/feeds/consent-feed-token/events/event-consent-001', (route) =>
      route.fulfill(
        json({
          event: {
            ...summary,
            sessions: [
              {
                id: 'consent-session-001',
                start_at: start,
                end_at: start,
                capacity_total: 50,
                status: 'published',
                admission_mode: 'general_admission',
                buyer_fields: [],
                service_fee_percent: '0.00',
                promo_codes_available: false,
                tiers: [
                  { id: 'tier-ga', name: 'Standard', pricing_mode: 'fixed', price_amount: 2500, currency: 'EUR', capacity: 50, available: 50, is_open: true, sort_order: 0 },
                ],
              },
            ],
          },
        }),
      ),
    );
    await page.goto('/demo/consent.html');
    const widget = page.locator('#widget-consent');
    const plus = widget.getByRole('button', { name: 'Increase quantity for Standard' });
    await plus.waitFor({ timeout: 15_000 });
    await plus.click();
    await expect(widget.locator('.mini-cart-btn')).toBeVisible();
    await page.waitForLoadState('networkidle');
    expect(chunks).toEqual([]);
  });
});
