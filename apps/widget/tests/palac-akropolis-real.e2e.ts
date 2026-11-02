/**
 * Palác Akropolis — Real Backend E2E Acceptance Tests
 *
 * Feature WID-R3: real E2E against the actual Arena backend.
 *
 * Key rule: NO page.route() mocks for /v1/* endpoints.
 * The widget calls the real backend through the proxy server
 * (scripts/serve-demo-real.cjs) running on port 4174.
 *
 * Prerequisites:
 *   1. `npm run build` — widget bundle must exist in dist/v1/
 *   2. A fake payment provider on port 12111:
 *        node scripts/stripe-stub.cjs
 *      Since the hosted-checkout flow landed, a cart above zero is only
 *      confirmed once a provider has created a hosted payment page. Without
 *      the stub every purchase test gets 422 checkout.payment_not_configured
 *      or 503 checkout.payment_start_failed.
 *   3. Arena backend running on port 8080 (or ARENA_API_URL) with:
 *        PUBLIC_TICKETS_BASE_URL=http://localhost:4174
 *        STRIPE_API_BASE_URL=http://localhost:12111/v1
 *      The first is the fallback return URL — these tests send no return_url,
 *      and without it checkout/start answers 400 checkout.invalid_return_url.
 *      The second points the Stripe adapter at the stub and is refused
 *      outright under APP_ENV=production.
 *   4. arena-seed + seed-palac-e2e.sql applied to the DB
 *   5. A FRESH database per run: the seats these tests take are single use,
 *      and the full-purchase test sells J20/J21 for good. That test also
 *      posts a signed Stripe webhook straight to ARENA_API_URL.
 *
 * Run with: npx playwright test --config playwright.config.real.ts
 */

import { createHmac, randomBytes } from 'node:crypto';
import { test, expect, type Browser, type Page, type BrowserContext } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

// ─── Fixture constants ────────────────────────────────────────────────────────

const FEED_TOKEN  = 'palac-akropolis-e2e-token-v1';
const SESSION_ID  = 'fe000007-0000-7000-8000-000000000002';
const DEMO_PAGE   = '/demo/palac-akropolis-real.html';

// Galérie tier ID (from seed-palac-e2e.sql)
const TIER_GALERIE = 'fe000007-0000-7000-8000-000000000006';

// Origin of the fake payment provider (scripts/stripe-stub.cjs). A paid cart
// is only confirmed once a provider has created a hosted page, so the
// acceptance run points arena-api here with STRIPE_API_BASE_URL and every
// redirect_url / payment_url comes back on this origin.
const STRIPE_STUB_ORIGIN =
  process.env['STRIPE_STUB_ORIGIN'] ?? 'http://localhost:12111';

// arena-api itself, for the provider's webhook. The test process plays Stripe
// here, so the event goes straight to the backend, not through the page proxy.
const ARENA_API_URL = (process.env['ARENA_API_URL'] ?? 'http://localhost:8080').replace(/\/+$/, '');

// OrgA's Stripe config and its webhook signing secret. The per-config webhook
// route verifies the signature only against THAT row's secret. Two rows can
// hold the role: seed-palac-e2e.sql section 12 inserts its own, but only when
// arena-seed has not already given OrgA a test-mode Stripe config (the partial
// unique index makes the insert a no-op), and the CI job runs arena-seed
// first. The test tries them in this order; 404 means "no such config".
const STRIPE_WEBHOOK_CONFIGS: ReadonlyArray<{ id: string; secret: string }> = [
  { id: 'fe000007-0000-7000-8000-00000000000c', secret: 'whsec_test_e2e_stub' },          // seed-palac-e2e.sql §12
  { id: 'fe000006-0000-7000-8000-000000000001', secret: 'whsec_TEST_ORGA_DO_NOT_USE' },   // arena-seed PaymentCfgAStripe
];

// Seats only the full-purchase test buys. Every other test of this file and
// of palac-promo-real.e2e.ts stays out of row J, and a seated purchase leaves
// the Galérie count alone, which the promo test compares before and after.
const PURCHASE_SEATS = ['J20', 'J21'] as const;

/**
 * Deliver a signed Stripe `checkout.session.completed` (paid) for `csId` to
 * arena-api's per-config webhook route, the way Stripe would after the buyer
 * paid on the hosted page. The signature is Stripe's scheme:
 * `Stripe-Signature: t=<unix>,v1=<hex HMAC-SHA256 of "<t>.<raw body>">`.
 * Like real Stripe the event names what was charged: arena parks a paid
 * event without an amount, or with another one, for a human.
 */
async function deliverPaidCheckoutWebhook(
  csId: string,
  charged: { total: number; currency: string },
): Promise<{ status: number; body: Record<string, unknown>; configId: string }> {
  const raw = JSON.stringify({
    id: 'evt_test_' + randomBytes(12).toString('hex'),
    object: 'event',
    type: 'checkout.session.completed',
    data: {
      object: {
        id: csId,
        object: 'checkout.session',
        payment_status: 'paid',
        status: 'complete',
        payment_intent: 'pi_test_' + randomBytes(12).toString('hex'),
        amount_total: charged.total,
        currency: charged.currency.toLowerCase(),
      },
    },
  });
  let last: { status: number; body: Record<string, unknown>; configId: string } | null = null;
  for (const cfg of STRIPE_WEBHOOK_CONFIGS) {
    const t = Math.floor(Date.now() / 1000);
    const v1 = createHmac('sha256', cfg.secret).update(`${t}.${raw}`).digest('hex');
    const res = await fetch(`${ARENA_API_URL}/v1/payment-intents/webhook/${cfg.id}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Stripe-Signature': `t=${t},v1=${v1}` },
      body: raw,
    });
    const text = await res.text();
    let body: Record<string, unknown> = {};
    try { body = JSON.parse(text) as Record<string, unknown>; } catch { body = { raw: text }; }
    last = { status: res.status, body, configId: cfg.id };
    if (res.status !== 404) return last;
  }
  return last!;
}

// ─── Helper: wait for the SVG seat map to appear ─────────────────────────────

async function waitForSVG(page: Page): Promise<void> {
  // Use the starts-with CSS attribute selector [attr^=val] because the real
  // backend renders a dynamic label: "Seat map — sections: Parket" (or similar).
  // An exact [aria-label="Seat map"] match would never resolve against the live
  // markup and cause every test that calls waitForSVG to time out.
  await page.waitForFunction(() => {
    const el = document.querySelector('#widget-hybrid-en');
    const shadow = el?.shadowRoot;
    if (!shadow) return false;
    // Accept any SVG whose aria-label starts with "Seat map" (covers both the
    // empty-geometry fallback "Seat map" and the populated "Seat map — sections: …").
    return (
      shadow.querySelector('svg[aria-label^="Seat map"]') !== null ||
      shadow.querySelector('.seat-map-inner svg') !== null
    );
  }, { timeout: 15_000 });
}

// ─── Helper: POST checkout/start via the proxy ────────────────────────────────

async function createCheckout(
  page: Page,
  seats: string[],
  gaQty = 0,
): Promise<{ token: string; status: number }> {
  return page.evaluate(
    async ({
      feedToken,
      sessionId,
      seatsArg,
      gaQtyArg,
      galerieTierId,
    }: {
      feedToken: string;
      sessionId: string;
      seatsArg: string[];
      gaQtyArg: number;
      galerieTierId: string;
    }) => {
      const body: Record<string, unknown> = {
        session_id:   sessionId,
        holder_email: 'buyer@arena-e2e.test',
        buyer:        { email: 'buyer@arena-e2e.test', name: 'E2E Buyer' },
        seats:        seatsArg,
      };
      if (gaQtyArg > 0) {
        body['ga_items'] = [{ tier_id: galerieTierId, quantity: gaQtyArg }];
      }
      const res = await fetch(`/v1/public/feeds/${feedToken}/checkout/start`, {
        method:  'POST',
        headers: { 'Content-Type': 'application/json' },
        body:    JSON.stringify(body),
      });
      const data = (await res.json()) as Record<string, unknown>;
      return {
        token:  (data['checkout_token'] as string) ?? '',
        status: res.status,
      };
    },
    {
      feedToken:     FEED_TOKEN,
      sessionId:     SESSION_ID,
      seatsArg:      seats,
      gaQtyArg:      gaQty,
      galerieTierId: TIER_GALERIE,
    },
  );
}

// ─── 1. Cold load: Moto G / Fast 3G ──────────────────────────────────────────

test.describe('1 — Cold load: Moto G / Fast 3G', () => {
  /**
   * Verifies the widget is interactive within 5 s on a simulated Fast 3G
   * connection against the REAL backend (no mocks).
   */

  test('seat map becomes interactive within 5 s on simulated Fast 3G', async ({ page }) => {
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('Network.enable');
    await cdp.send('Network.emulateNetworkConditions', {
      offline:             false,
      downloadThroughput:  (1.5 * 1024 * 1024) / 8, // 1.5 Mbps → bytes/s
      uploadThroughput:    (750 * 1024) / 8,         // 750 kbps → bytes/s
      latency:             40,
    });

    await page.setViewportSize({ width: 360, height: 640 });

    const t0 = Date.now();
    await page.goto(DEMO_PAGE);

    // Wait until seat map container or SVG is present.
    await page.waitForFunction(() => {
      const el = document.querySelector('#widget-hybrid-en');
      if (!el?.shadowRoot) return false;
      const container = el.shadowRoot.querySelector('.seat-map-container');
      if (!container) return false;
      return (
        container.querySelector('svg') !== null ||
        container.querySelector('.seat-map-state') !== null
      );
    }, { timeout: 5_000 });

    const elapsed = Date.now() - t0;
    expect(
      elapsed,
      `Widget not interactive after ${elapsed} ms (budget: 5000 ms)`,
    ).toBeLessThan(5_000);

    // Wait for full SVG render.
    await waitForSVG(page);

    // Clean up throttle.
    await cdp.send('Network.emulateNetworkConditions', {
      offline:            false,
      downloadThroughput: -1,
      uploadThroughput:   -1,
      latency:            0,
    });
  });

  test('JS bundle size is ≤ 600 KB raw (proxy for gzip gate)', async ({ page }) => {
    let transferBytes = 0;

    // Intercept the bundle request to record its raw size.
    await page.route('**/dist/v1/arena-tickets.js', async (route) => {
      const resp = await route.fetch();
      const body = await resp.body();
      transferBytes = body.length;
      await route.fulfill({ response: resp });
    });

    await page.goto(DEMO_PAGE);
    await page.waitForLoadState('networkidle');

    // Drop the bundle route before the test ends so a late in-flight
    // route.fetch cannot throw "Test ended" into the next test's report.
    await page.unrouteAll({ behavior: 'ignoreErrors' });

    expect(
      transferBytes,
      `Bundle raw size ${transferBytes} bytes exceeds 600 KB proxy limit`,
    ).toBeLessThan(600 * 1024);
  });
});

// ─── 2. Real schema: 260 seats from backend ───────────────────────────────────

test.describe('2 — Real schema: 260 seats from backend', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto(DEMO_PAGE);
  });

  test('renders 260 seat circles from REAL backend data', async ({ page }) => {
    await waitForSVG(page);

    const seatCount = await page.evaluate(() => {
      const el  = document.querySelector('#widget-hybrid-en');
      // Use starts-with selector — the real backend populates the label as
      // "Seat map — sections: Parket" (not the bare "Seat map" fallback).
      const svg = el?.shadowRoot?.querySelector('svg[aria-label^="Seat map"]');
      return svg?.querySelectorAll('[data-seat-key]').length ?? 0;
    });

    expect(seatCount, `Expected 260 seats from real backend, found ${seatCount}`).toBe(260);
  });

  test('GA category "Galérie" served by the real schema (AB-40)', async ({ page }) => {
    // AB-40/AB-51 retired the standing_zones array (and its empty
    // placeholder <g> in the SVG): GA capacity now lives on a geometry
    // category with kind=general_admission. AB-40D wires the widget to
    // render GA areas on the map when the category carries a polygon.
    await waitForSVG(page);

    const gaCategory = await page.evaluate(async () => {
      // Relative fetch — the e2e page origin proxies /v1 to the real
      // backend (same pattern as the seat-status assertions below).
      const sessionID = 'fe000007-0000-7000-8000-000000000002';
      const res = await fetch(`/v1/event-sessions/${sessionID}/schema`);
      if (!res.ok) return null;
      const body = (await res.json()) as {
        geometry?: {
          categories?: readonly {
            index: number;
            name: string;
            kind?: string;
            capacity?: number;
            polygon?: readonly { x: number; y: number }[];
          }[];
        };
      };
      return (
        body.geometry?.categories?.find(
          (c) => c.kind === 'general_admission',
        ) ?? null
      );
    });

    expect(gaCategory, 'GA category missing from real schema').not.toBeNull();
    expect(gaCategory?.name).toBe('Galérie');
    expect(gaCategory?.capacity).toBe(100);

    // AB-40D "one surface, zero toggles": when the real schema carries a
    // polygon (the SVG import path), the widget must render the GA area
    // on the same hall map as the seats — same SVG surface, no toggle.
    // The GA-only hand-entered path has no polygon: we soft-skip the
    // rendering assertion in that case so this test stays useful across
    // both import paths without demanding a specific seeding topology.
    if (gaCategory?.polygon && gaCategory.polygon.length >= 3) {
      const gaAreaPresent = await page.evaluate((idx: number) => {
        const el = document.querySelector('#widget-hybrid-en');
        const svg = el?.shadowRoot?.querySelector('svg[aria-label^="Seat map"]');
        return svg?.querySelector(`[data-ga-category-index="${idx}"]`) !== null;
      }, gaCategory.index);
      expect(
        gaAreaPresent,
        'GA area polygon must render on the same hall map as the seats (AB-40D)',
      ).toBe(true);
    }
  });

  test('no JS console errors on initial load', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (err) => { errors.push(err.message); });
    page.on('console', (msg) => {
      if (msg.type() === 'error') errors.push(msg.text());
    });

    await waitForSVG(page);

    // Filter out known benign Chrome headless noise.
    const realErrors = errors.filter(
      (e) =>
        !e.includes('favicon') &&
        !e.includes('Failed to load resource') &&
        !e.includes('net::ERR'),
    );
    expect(realErrors).toHaveLength(0);
  });

  test('schema is never cached: no-store, no ETag, a repeat request gets the full body', async ({ page }) => {
    // The schema carries tier prices, colours and per-seat categories that
    // change without a new geometry, so the backend serves it
    // `Cache-Control: no-store` with no ETag (hseating/public_schema.go) and
    // the widget fetches it fresh every time.
    let first: { cc: string | undefined; etag: string | undefined } | null = null;
    await page.route(`**/v1/event-sessions/${SESSION_ID}/schema`, async (route) => {
      const resp = await route.fetch();
      if (!first) first = { cc: resp.headers()['cache-control'], etag: resp.headers()['etag'] };
      await route.fulfill({ response: resp });
    });

    await page.goto(DEMO_PAGE);
    await waitForSVG(page);

    // page.request is NOT intercepted by page.route(): it goes through the
    // proxy (serve-demo-real.cjs) to the real backend. A stale validator
    // must not short-circuit to 304.
    const secondResp = await page.request.get(
      `/v1/event-sessions/${SESSION_ID}/schema`,
      { headers: { 'If-None-Match': '"any-old-etag"' } },
    );
    // The widget keeps polling in the background; drop the schema route
    // before the test ends so an in-flight route.fetch cannot throw
    // "Test ended" and poison the next test's report.
    await page.unrouteAll({ behavior: 'ignoreErrors' });

    expect(first, 'the widget fetched the schema').not.toBeNull();
    expect(first!.cc).toBe('no-store');
    expect(first!.etag, 'the schema carries no ETag').toBeUndefined();
    expect(secondResp.status()).toBe(200);
    expect((await secondResp.body()).length).toBeGreaterThan(0);
  });
});

// ─── 3. Concurrent 409 from second browser context (REAL) ────────────────────

test.describe('3 — Concurrent 409 from second browser context (REAL)', () => {
  /**
   * Two browser contexts race to hold B01 and B02.
   * ctx1 succeeds (201); ctx2 gets a real 409 from the backend.
   *
   * Note: After this test the seats B01/B02 remain held until the TTL
   * expires (~15 min). This is acceptable for CI with an ephemeral DB.
   */

  test('ctx2 checkout/start returns 409 after ctx1 holds B01+B02', async ({
    browser,
  }: { browser: Browser }) => {
    const ctx1: BrowserContext = await browser.newContext();
    const ctx2: BrowserContext = await browser.newContext();

    try {
      const page1 = await ctx1.newPage();
      const page2 = await ctx2.newPage();

      // Both pages load the demo page so the widget initialises.
      await Promise.all([
        page1.goto(DEMO_PAGE),
        page2.goto(DEMO_PAGE),
      ]);
      await Promise.all([
        waitForSVG(page1),
        waitForSVG(page2),
      ]);

      // ctx1 holds B01 + B02 first (real POST to backend).
      const ctx1Result = await page1.evaluate(
        async ({ feedToken, sessionId }: { feedToken: string; sessionId: string }) => {
          const res = await fetch(`/v1/public/feeds/${feedToken}/checkout/start`, {
            method:  'POST',
            headers: { 'Content-Type': 'application/json' },
            body:    JSON.stringify({
              session_id:   sessionId,
              holder_email: 'buyer-ctx1@arena-e2e.test',
              buyer:        { email: 'buyer-ctx1@arena-e2e.test', name: 'Test Buyer' },
              seats:        ['B01', 'B02'],
            }),
          });
          const data = (await res.json()) as Record<string, unknown>;
          return { status: res.status, data };
        },
        { feedToken: FEED_TOKEN, sessionId: SESSION_ID },
      );

      expect(ctx1Result.status, 'ctx1 checkout/start should return 201').toBe(201);

      // ctx2 tries the same seats — expects 409 from the REAL backend.
      const ctx2Result = await page2.evaluate(
        async ({ feedToken, sessionId }: { feedToken: string; sessionId: string }) => {
          const res = await fetch(`/v1/public/feeds/${feedToken}/checkout/start`, {
            method:  'POST',
            headers: { 'Content-Type': 'application/json' },
            body:    JSON.stringify({
              session_id:   sessionId,
              holder_email: 'buyer-ctx2@arena-e2e.test',
              buyer:        { email: 'buyer-ctx2@arena-e2e.test', name: 'Test Buyer 2' },
              seats:        ['B01', 'B02'],
            }),
          });
          const data = (await res.json()) as Record<string, unknown>;
          return { status: res.status, data };
        },
        { feedToken: FEED_TOKEN, sessionId: SESSION_ID },
      );

      expect(ctx2Result.status, 'ctx2 checkout/start should return 409 (conflict)').toBe(409);

      // Verify the real seat-status endpoint shows B01/B02 as held.
      const seatStatus = await page1.evaluate(
        async ({ sessionId }: { sessionId: string }) => {
          const res  = await fetch(`/v1/event-sessions/${sessionId}/seat-status`);
          const data = (await res.json()) as { seats: Record<string, string> };
          return { b01: data.seats['B01'], b02: data.seats['B02'] };
        },
        { sessionId: SESSION_ID },
      );

      expect(seatStatus.b01, 'B01 should be held after ctx1 checkout').toBe('held');
      expect(seatStatus.b02, 'B02 should be held after ctx1 checkout').toBe('held');
    } finally {
      await ctx1.close();
      await ctx2.close();
    }
  });

  test('ctx2 409 response carries nested error envelope with conflictKeys (WID-S2)', async ({
    browser,
  }: { browser: Browser }) => {
    /**
     * Verifies that the real backend sends {"error": {"code": "...", "details": {"conflicts": [...]}}}
     * (nested envelope, NOT flat {"error": "...", "code": "...", "details": {...}}).
     * The widget's api.ts must parse the nested envelope to extract seat-level conflict data.
     * This validates the WID-S2 fix: parseConflictsFromApiError now reads from error.details.conflicts.
     *
     * Uses fresh seats E01, E02 to avoid races with the B01/B02 test above.
     */
    const ctx1: BrowserContext = await browser.newContext();
    const ctx2: BrowserContext = await browser.newContext();

    try {
      const page1 = await ctx1.newPage();
      const page2 = await ctx2.newPage();

      await Promise.all([page1.goto(DEMO_PAGE), page2.goto(DEMO_PAGE)]);
      await Promise.all([waitForSVG(page1), waitForSVG(page2)]);

      // ctx1 holds E01+E02.
      const ctx1Status = await page1.evaluate(
        async ({ feedToken, sessionId }: { feedToken: string; sessionId: string }) => {
          const res = await fetch(`/v1/public/feeds/${feedToken}/checkout/start`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              session_id:   sessionId,
              holder_email: 'e2e-ctx1-e@arena-e2e.test',
              buyer: { email: 'e2e-ctx1-e@arena-e2e.test', name: 'WID-S2 Buyer 1' },
              seats: ['E01', 'E02'],
            }),
          });
          return res.status;
        },
        { feedToken: FEED_TOKEN, sessionId: SESSION_ID },
      );
      expect(ctx1Status).toBe(201);

      // ctx2 tries the same seats — expects a 409 with nested error envelope.
      const ctx2Body = await page2.evaluate(
        async ({ feedToken, sessionId }: { feedToken: string; sessionId: string }) => {
          const res = await fetch(`/v1/public/feeds/${feedToken}/checkout/start`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              session_id:   sessionId,
              holder_email: 'e2e-ctx2-e@arena-e2e.test',
              buyer: { email: 'e2e-ctx2-e@arena-e2e.test', name: 'WID-S2 Buyer 2' },
              seats: ['E01', 'E02'],
            }),
          });
          const body = (await res.json()) as Record<string, unknown>;
          return { status: res.status, body };
        },
        { feedToken: FEED_TOKEN, sessionId: SESSION_ID },
      );

      expect(ctx2Body.status).toBe(409);

      // Verify the nested envelope shape: {"error": {"code": "...", "details": {"conflicts": [...]}}}
      const errEnv = ctx2Body.body['error'] as Record<string, unknown> | undefined;
      expect(errEnv, '409 body must have "error" key as object').toBeTruthy();
      expect(typeof errEnv).toBe('object');
      expect(errEnv?.['code']).toBe('reservation.seats_conflict');
      const conflictsArr = (errEnv?.['details'] as Record<string, unknown>)?.['conflicts'];
      expect(Array.isArray(conflictsArr), 'details.conflicts must be an array').toBe(true);
      const conflicts = conflictsArr as Array<Record<string, string>>;
      expect(conflicts.length).toBeGreaterThan(0);
      expect(typeof conflicts[0]?.['seat_key']).toBe('string');
      expect(typeof conflicts[0]?.['status']).toBe('string');

      // Verify seats show up as "held" in the status endpoint (conflict is real).
      const statusSnapshot = await page1.evaluate(
        async ({ sessionId }: { sessionId: string }) => {
          const res  = await fetch(`/v1/event-sessions/${sessionId}/seat-status`);
          const data = (await res.json()) as { seats: Record<string, string> };
          return { e01: data.seats['E01'], e02: data.seats['E02'] };
        },
        { sessionId: SESSION_ID },
      );
      expect(statusSnapshot.e01).toBe('held');
      expect(statusSnapshot.e02).toBe('held');
    } finally {
      await ctx1.close();
      await ctx2.close();
    }
  });
});

// ─── 4. Checkout start contract (real backend) ────────────────────────────────

test.describe('4 — Checkout start contract (real backend)', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto(DEMO_PAGE);
    await waitForSVG(page);
  });

  test('POST checkout/start returns 201 with redirect_url, checkout_token, expires_at', async ({
    page,
  }) => {
    // Use seats C01, C02 + 1 GA unit to avoid conflict with section 3 (B01/B02).
    const result = await createCheckout(page, ['C01', 'C02'], 1);

    expect(result.status, 'checkout/start should return 201').toBe(201);

    // Verify response shape.
    const detail = await page.evaluate(
      async ({ feedToken, sessionId, galerieTierId }: {
        feedToken: string;
        sessionId: string;
        galerieTierId: string;
      }) => {
        const res = await fetch(`/v1/public/feeds/${feedToken}/checkout/start`, {
          method:  'POST',
          headers: { 'Content-Type': 'application/json' },
          body:    JSON.stringify({
            session_id:   sessionId,
            holder_email: 'buyer-c@arena-e2e.test',
            buyer:        { email: 'buyer-c@arena-e2e.test', name: 'E2E Buyer C' },
            seats:        ['C03', 'C04'],
            ga_items:     [{ tier_id: galerieTierId, quantity: 1 }],
          }),
        });
        const data = (await res.json()) as Record<string, unknown>;
        return {
          status:         res.status,
          redirect_url:   data['redirect_url'],
          checkout_token: data['checkout_token'],
          expires_at:     data['expires_at'],
        };
      },
      { feedToken: FEED_TOKEN, sessionId: SESSION_ID, galerieTierId: TIER_GALERIE },
    );

    expect(detail.status).toBe(201);
    expect(typeof detail.redirect_url, 'redirect_url should be a string').toBe('string');
    expect(typeof detail.checkout_token, 'checkout_token should be a string').toBe('string');
    expect(typeof detail.expires_at, 'expires_at should be a string').toBe('string');
    expect(new Date(detail.expires_at as string).getTime()).toBeGreaterThan(Date.now());

    // redirect_url is now the provider's own hosted payment page, not a path
    // on arena. Before the hosted-checkout flow it was a hardcoded, dead
    // "/checkout/<uuid>" that no buyer could ever pay on, and the shape-only
    // assertion above passed all the same — so pin the contract.
    const redirect = detail.redirect_url as string;
    expect(
      /^https?:\/\//.test(redirect),
      `redirect_url must be an absolute provider URL, got: ${redirect}`,
    ).toBe(true);
    expect(
      redirect.startsWith(STRIPE_STUB_ORIGIN),
      `redirect_url must point at the payment provider (${STRIPE_STUB_ORIGIN}), got: ${redirect}`,
    ).toBe(true);
  });

  test('GET checkout/{token} returns status in [pending, created] and a payment_url before payment', async ({
    page,
  }) => {
    // Create a fresh hold.
    const { token, status: startStatus } = await createCheckout(page, ['C05', 'C06'], 0);
    expect(startStatus).toBe(201);
    expect(token.length, 'checkout_token must be non-empty').toBeGreaterThan(0);

    // Poll checkout status — not yet paid.
    const checkoutStatus = await page.evaluate(
      async ({ tok }: { tok: string }) => {
        const res  = await fetch(`/v1/public/checkout/${tok}`);
        const data = (await res.json()) as Record<string, unknown>;
        return {
          httpStatus:  res.status,
          status:      data['status'] as string,
          payment_url: data['payment_url'],
        };
      },
      { tok: token },
    );

    expect(checkoutStatus.httpStatus).toBe(200);
    expect(
      ['pending', 'created'].includes(checkoutStatus.status),
      `Expected status pending or created, got: ${checkoutStatus.status}`,
    ).toBe(true);

    // A buyer who bounced off the payment page — closed the tab, lost the
    // redirect — resumes through this field. An unpaid checkout that cannot
    // hand its payment page back is a sale lost for no reason.
    expect(
      typeof checkoutStatus.payment_url === 'string' && checkoutStatus.payment_url !== '',
      `payment_url must be present while the checkout is unpaid, got: ${String(checkoutStatus.payment_url)}`,
    ).toBe(true);
    expect(
      (checkoutStatus.payment_url as string).startsWith(STRIPE_STUB_ORIGIN),
      `payment_url must point at the payment provider (${STRIPE_STUB_ORIGIN})`,
    ).toBe(true);
  });

  test('full purchase through the widget UI: seats, buyer form, hosted page, signed webhook, paid', async ({
    page,
  }) => {
    /**
     * PAY-01, the browser cycle. Everything a buyer does goes through the
     * widget's own UI; only the payment provider is the stub, and the test
     * process plays Stripe's webhook with a properly signed event.
     *
     * No arena-worker runs in this job, so tickets are not issued and no
     * letter goes out (both covered by Go tests). The seats still become
     * `sold` here: the webhook converts the reservation inline
     * (hcheckout/payment_intents.go), the queued convert job only repeats it.
     */
    test.setTimeout(90_000);
    const widget = page.locator('#widget-hybrid-en');

    // 1. Pick two seats on the map (a real click on the SVG circle).
    const before = await page.request.get(`/v1/event-sessions/${SESSION_ID}/seat-status`);
    const beforeSeats = ((await before.json()) as { seats: Record<string, string> }).seats;
    for (const key of PURCHASE_SEATS) {
      expect(
        beforeSeats[key] ?? 'available',
        `${key} must be free before the purchase (recreate the database between runs)`,
      ).toBe('available');
    }
    for (const key of PURCHASE_SEATS) {
      await widget.locator(`circle[data-seat-key="${key}"]`).click();
    }
    await expect(widget.locator('.mini-cart-btn')).toHaveAttribute('aria-label', /\(2 items\)/);

    // 2. Open the cart and go on to the buyer form.
    await widget.locator('.mini-cart-btn').click();
    const sheet = widget.locator('.sheet');
    await expect(sheet).toBeVisible();
    await sheet.locator('.cta-btn').click();

    // The widget leaves for the payment page the moment checkout/start
    // answers, which drops the response body: read it on the way through. A
    // pass-through, the request still reaches the real backend unchanged.
    let startStatus = 0;
    let startBody: Record<string, unknown> = {};
    let started: {
      checkout_token?: string;
      redirect_url?: string;
      pricing?: { total: number; currency: string };
    } = {};
    await page.route('**/checkout/start', async (route) => {
      const real = await route.fetch();
      startStatus = real.status();
      startBody = route.request().postDataJSON() as Record<string, unknown>;
      started = (await real.json()) as typeof started;
      await route.fulfill({ response: real });
    });

    const form = sheet.locator('form');
    await expect(form).toBeVisible();
    for (const input of await form.locator('input').all()) {
      const type = (await input.getAttribute('type')) ?? 'text';
      if (type === 'email') await input.fill('purchase-e2e@arena-e2e.test');
      else if (type === 'tel') await input.fill('+420777000222');
      else if (type === 'text') await input.fill('Purchase E2E Buyer');
      else if (type === 'checkbox') await input.check();
    }

    // 3. Pay: the browser is sent to the provider's hosted page.
    await sheet.locator('button[type="submit"]').click();
    await page.waitForURL(
      (u) => u.origin === STRIPE_STUB_ORIGIN && u.pathname.startsWith('/pay/cs_'),
      { timeout: 20_000 },
    );
    await page.unrouteAll({ behavior: 'ignoreErrors' });
    expect(startStatus, 'checkout/start through the UI').toBe(201);
    expect(startBody).toMatchObject({ session_id: SESSION_ID });
    expect([...((startBody['seats'] as string[] | undefined) ?? [])].sort()).toEqual([...PURCHASE_SEATS]);
    const token = started.checkout_token ?? '';
    expect(token, 'checkout_token from checkout/start').not.toBe('');

    const csId = new URL(page.url()).pathname.slice('/pay/'.length);
    expect(csId).toMatch(/^cs_[A-Za-z0-9_]+$/);
    expect(started.redirect_url, 'the page the widget went to is the one start answered').toBe(page.url());
    // The stub knows the session arena created, so arena really asked for it.
    await expect(page.getByTestId('session-known')).toHaveText('known');

    // Not paid yet: the checkout is still waiting and the seats are held.
    const pending = await page.request.get(`${ARENA_API_URL}/v1/public/checkout/${token}`);
    expect(pending.status()).toBe(200);
    expect(((await pending.json()) as { status: string }).status).toBe('pending');
    const held = await page.request.get(`${ARENA_API_URL}/v1/event-sessions/${SESSION_ID}/seat-status`);
    const heldSeats = ((await held.json()) as { seats: Record<string, string> }).seats;
    for (const key of PURCHASE_SEATS) {
      expect(heldSeats[key], `${key} before payment`).toBe('held');
    }

    // 4. Stripe tells arena the buyer paid what checkout/start priced.
    const charged = started.pricing;
    expect(charged?.total, 'pricing.total from checkout/start').toBeGreaterThan(0);
    const hook = await deliverPaidCheckoutWebhook(csId, charged!);
    expect(hook.status, `webhook answer ${JSON.stringify(hook.body)} (config ${hook.configId})`).toBe(200);
    expect(hook.body['processed'], `webhook body ${JSON.stringify(hook.body)}`).toBe(true);

    // A second delivery of the same payment changes nothing (terminal intent).
    const replay = await deliverPaidCheckoutWebhook(csId, charged!);
    expect(replay.status).toBe(200);
    expect(replay.body['processed']).toBe(false);

    // 5. The buyer comes back to the page arena gave the provider as the
    //    success URL: the widget's own page with ?checkout_token=….
    await page.goto(`${DEMO_PAGE}?checkout_token=${encodeURIComponent(token)}`);
    const status = widget.locator('.order-status');
    await expect(status).toHaveAttribute('data-status', 'paid', { timeout: 20_000 });
    await expect(status.locator('.status-paid .status-title')).toHaveText('Payment successful!');

    const paid = await page.request.get(`/v1/public/checkout/${token}`);
    expect(paid.status()).toBe(200);
    const paidBody = (await paid.json()) as { status: string; total?: number; currency?: string };
    expect(paidBody.status).toBe('paid');

    // 6. The bought seats are sold, no longer merely held.
    const after = await page.request.get(`/v1/event-sessions/${SESSION_ID}/seat-status`);
    expect(after.status()).toBe(200);
    const afterSeats = ((await after.json()) as { seats: Record<string, string> }).seats;
    for (const key of PURCHASE_SEATS) {
      expect(afterSeats[key], `${key} after payment`).toBe('sold');
    }
  });
});

// ─── 5. Hold expiry recovery (real backend) ───────────────────────────────────

test.describe('5 — Hold expiry recovery (real backend)', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto(DEMO_PAGE);
    await waitForSVG(page);
  });

  /**
   * Uses fresh seats D01, D02 to avoid conflicts with sections 3/4.
   */

  test('POST /v1/public/checkout/{token}/recover returns 200 or 400/404', async ({ page }) => {
    // Create a fresh hold on D01/D02.
    const { token, status: startStatus } = await createCheckout(page, ['D01', 'D02'], 0);
    expect(startStatus).toBe(201);
    expect(token.length).toBeGreaterThan(0);

    // Call recover immediately (hold is still valid — backend may return 200 or
    // handle "not expired yet" with a sensible response).
    const recoverResult = await page.evaluate(
      async ({ tok }: { tok: string }) => {
        const res  = await fetch(`/v1/public/checkout/${tok}/recover`, { method: 'POST' });
        const data = (await res.json()) as Record<string, unknown>;
        return {
          httpStatus:     res.status,
          checkout_token: data['checkout_token'],
          expires_at:     data['expires_at'],
        };
      },
      { tok: token },
    );

    // Accept 200 (recovered), 400 (not expired / already completed), 404
    // (unknown token), or 409 (documented: re-capture finds the seats still
    // held by this very active checkout — reservation.seats_conflict).
    expect(
      [200, 400, 404, 409].includes(recoverResult.httpStatus),
      `recover returned unexpected status ${recoverResult.httpStatus}`,
    ).toBe(true);

    if (recoverResult.httpStatus === 200) {
      expect(typeof recoverResult.checkout_token).toBe('string');
      expect(new Date(recoverResult.expires_at as string).getTime()).toBeGreaterThan(Date.now());
    }
  });

  test('GET checkout/{freshToken}/status shape — status is string, checkout_session_id present', async ({
    page,
  }) => {
    const { token, status: startStatus } = await createCheckout(page, ['D03', 'D04'], 0);
    expect(startStatus).toBe(201);

    const statusResult = await page.evaluate(
      async ({ tok }: { tok: string }) => {
        const res  = await fetch(`/v1/public/checkout/${tok}`);
        const data = (await res.json()) as Record<string, unknown>;
        return {
          httpStatus:          res.status,
          status:              data['status'],
          checkout_session_id: data['checkout_session_id'],
        };
      },
      { tok: token },
    );

    expect(statusResult.httpStatus).toBe(200);
    expect(typeof statusResult.status, 'status field should be a string').toBe('string');
    // checkout_session_id may be a string or uuid.
    expect(
      statusResult.checkout_session_id !== undefined,
      'checkout_session_id should be present',
    ).toBe(true);
  });
});

// ─── 6. A11y: axe WCAG 2.2 AA on POPULATED real map ─────────────────────────

test.describe('6 — A11y: axe WCAG 2.2 AA on POPULATED real map', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto(DEMO_PAGE);
    await waitForSVG(page);
  });

  test('axe scan — no critical/serious WCAG 2.2 AA violations', async ({ page }) => {
    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa'])
      .analyze();

    const critical = results.violations.filter(
      (v) => v.impact === 'critical' || v.impact === 'serious',
    );

    expect(
      critical,
      `WCAG 2.2 AA critical/serious violations:\n${JSON.stringify(critical, null, 2)}`,
    ).toHaveLength(0);
  });

  test('#widget-hybrid-he has dir=rtl in shadow root', async ({ page }) => {
    const dir = await page.evaluate(() => {
      const el = document.querySelector('#widget-hybrid-he');
      if (!el?.shadowRoot) return null;
      const root = el.shadowRoot.querySelector('[dir]');
      return root?.getAttribute('dir') ?? null;
    });

    expect(dir).toBe('rtl');
  });

  test('#widget-hybrid-en has dir=ltr in shadow root', async ({ page }) => {
    const dir = await page.evaluate(() => {
      const el = document.querySelector('#widget-hybrid-en');
      if (!el?.shadowRoot) return null;
      const root = el.shadowRoot.querySelector('[dir]');
      return root?.getAttribute('dir') ?? null;
    });

    expect(dir).toBe('ltr');
  });

  test('seat map container has the WID-T5 composite role and aria-label', async ({ page }) => {
    const attrs = await page.evaluate(() => {
      const el        = document.querySelector('#widget-hybrid-en');
      const container = el?.shadowRoot?.querySelector('.seat-map-container');
      return {
        role:      container?.getAttribute('role'),
        ariaLabel: container?.getAttribute('aria-label'),
        tabindex:  container?.getAttribute('tabindex'),
      };
    });

    // WID-T5 canonical single-stop composite: the container is the sole Tab
    // stop with role="toolbar" (see SeatMapView.svelte and keyboard.e2e.ts).
    expect(attrs.role).toBe('toolbar');
    expect(attrs.tabindex).toBe('0');
    expect(attrs.ariaLabel).toBeTruthy();
  });
});

// ─── 7. API contract shapes (real backend) ────────────────────────────────────

test.describe('7 — API contract shapes (real backend)', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto(DEMO_PAGE);
    await waitForSVG(page);
  });

  test('schema response has session_id, geometry with sections array, category_prices with tier_id and price_amount', async ({
    page,
  }) => {
    const schema = await page.evaluate(
      async ({ sessionId }: { sessionId: string }) => {
        const res = await fetch(`/v1/event-sessions/${sessionId}/schema`);
        return res.json() as Promise<Record<string, unknown>>;
      },
      { sessionId: SESSION_ID },
    );

    expect(typeof schema['session_id']).toBe('string');

    const geo = schema['geometry'] as Record<string, unknown>;
    expect(Array.isArray(geo['sections']), 'geometry.sections should be an array').toBe(true);

    const prices = schema['category_prices'] as Array<Record<string, unknown>>;
    expect(Array.isArray(prices), 'category_prices should be an array').toBe(true);
    expect(prices.length).toBeGreaterThan(0);

    const first = prices[0]!;
    expect(typeof first['tier_id'], 'tier_id should be a string').toBe('string');
    expect(typeof first['price_amount'], 'price_amount should be a number').toBe('number');
  });

  test('seat-status response has session_id, status_version (number), seats (object), delta (boolean)', async ({
    page,
  }) => {
    const statusResp = await page.evaluate(
      async ({ sessionId }: { sessionId: string }) => {
        const res  = await fetch(`/v1/event-sessions/${sessionId}/seat-status`);
        return res.json() as Promise<Record<string, unknown>>;
      },
      { sessionId: SESSION_ID },
    );

    expect(typeof statusResp['session_id']).toBe('string');
    expect(typeof statusResp['status_version']).toBe('number');
    expect(typeof statusResp['seats']).toBe('object');
    expect(typeof statusResp['delta']).toBe('boolean');
  });

  test('all seat statuses from seat-status are valid', async ({ page }) => {
    const statusResp = await page.evaluate(
      async ({ sessionId }: { sessionId: string }) => {
        const res  = await fetch(`/v1/event-sessions/${sessionId}/seat-status`);
        return res.json() as Promise<Record<string, unknown>>;
      },
      { sessionId: SESSION_ID },
    );

    const validStatuses = new Set(['available', 'held', 'sold', 'unavailable']);
    const seats = statusResp['seats'] as Record<string, string>;

    for (const [key, val] of Object.entries(seats)) {
      expect(
        validStatuses.has(val),
        `Seat ${key} has invalid status "${val}"`,
      ).toBe(true);
    }
  });
});
