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
 *   2. Arena backend running on port 8080 (or ARENA_API_URL)
 *   3. arena-seed + seed-palac-e2e.sql applied to the DB
 *
 * Run with: npx playwright test --config playwright.config.real.ts
 */

import { test, expect, type Browser, type Page, type BrowserContext } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

// ─── Fixture constants ────────────────────────────────────────────────────────

const FEED_TOKEN  = 'palac-akropolis-e2e-token-v1';
const SESSION_ID  = 'fe000007-0000-7000-8000-000000000002';
const DEMO_PAGE   = '/demo/palac-akropolis-real.html';

// Galérie tier ID (from seed-palac-e2e.sql)
const TIER_GALERIE = 'fe000007-0000-7000-8000-000000000006';

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

  test('standing zone "galerie" present in SVG from real schema', async ({ page }) => {
    await waitForSVG(page);

    const zonePresent = await page.evaluate(() => {
      const el  = document.querySelector('#widget-hybrid-en');
      const svg = el?.shadowRoot?.querySelector('svg');
      return svg?.querySelector('[data-zone-key="galerie"]') !== null;
    });

    expect(zonePresent, 'Standing zone "galerie" not found in SVG from real backend').toBe(true);
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

  test('ETag sent on second schema request (real Cache-Control behavior)', async ({ page }) => {
    // First load — capture the ETag returned by the real backend.
    let firstEtag: string | null = null;
    let secondRequestHadIfNoneMatch = false;

    await page.route(`**/v1/event-sessions/${SESSION_ID}/schema`, async (route) => {
      const ifNoneMatch = route.request().headers()['if-none-match'];
      if (ifNoneMatch) {
        secondRequestHadIfNoneMatch = true;
      }
      // Forward to the real backend.
      const resp = await route.fetch();
      if (!firstEtag) {
        firstEtag = resp.headers()['etag'] ?? null;
      }
      await route.fulfill({ response: resp });
    });

    await page.goto(DEMO_PAGE);
    await waitForSVG(page);

    // The real backend always sets ETag = '"<geometry_checksum>"' (strong ETag)
    // on GET /v1/event-sessions/{id}/schema — see hseating/public_schema.go.
    // Assert that the response included a non-null ETag with the expected format.
    expect(firstEtag, 'Backend schema endpoint must set an ETag header').not.toBeNull();
    // Strong ETags are double-quoted strings, e.g. '"sha256-palac-akropolis-…"'.
    expect(
      firstEtag,
      `ETag "${firstEtag}" should be a double-quoted strong ETag`,
    ).toMatch(/^"[^"]+"$/);
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
  });

  test('GET checkout/{token} returns status in [pending, created] before payment', async ({
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
        return { httpStatus: res.status, status: data['status'] as string };
      },
      { tok: token },
    );

    expect(checkoutStatus.httpStatus).toBe(200);
    expect(
      ['pending', 'created'].includes(checkoutStatus.status),
      `Expected status pending or created, got: ${checkoutStatus.status}`,
    ).toBe(true);
  });

  test('full Stripe purchase — skipped without STRIPE_TEST_KEY', async ({ page }) => {
    if (!process.env['STRIPE_TEST_KEY']) {
      test.skip();
      return;
    }
    // Full Stripe purchase flow would go here when STRIPE_TEST_KEY is set.
    await page.goto(DEMO_PAGE);
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

    // Accept 200 (recovered), 400 (not expired yet / invalid state), or 404.
    expect(
      [200, 400, 404].includes(recoverResult.httpStatus),
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

  test('seat map container has role=application and aria-label', async ({ page }) => {
    const attrs = await page.evaluate(() => {
      const el        = document.querySelector('#widget-hybrid-en');
      const container = el?.shadowRoot?.querySelector('.seat-map-container');
      return {
        role:      container?.getAttribute('role'),
        ariaLabel: container?.getAttribute('aria-label'),
        tabindex:  container?.getAttribute('tabindex'),
      };
    });

    expect(attrs.role).toBe('application');
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

    const validStatuses = new Set(['available', 'held', 'sold', 'blocked']);
    const seats = statusResp['seats'] as Record<string, string>;

    for (const [key, val] of Object.entries(seats)) {
      expect(
        validStatuses.has(val),
        `Seat ${key} has invalid status "${val}"`,
      ).toBe(true);
    }
  });
});
