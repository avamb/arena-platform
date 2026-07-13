/**
 * Custom Events E2E tests — WID-S5
 *
 * Verifies that <arena-tickets> dispatches typed CustomEvents that host pages
 * can listen to outside the Shadow DOM.  All five events are covered:
 *
 *  arena:seat_selected   — seat tapped to add to selection
 *  arena:seat_released   — seat tapped again to deselect
 *  arena:payment_started — checkout/start succeeded (tested via full UI flow)
 *  arena:order_paid      — GET checkout returns status:"paid"
 *  arena:order_failed    — GET checkout returns status:"failed" or "expired"
 *
 * All backend calls are intercepted via Playwright page.route() so the suite
 * runs fully offline against the built widget bundle.
 *
 * Prerequisite: `npm run build` must be run before `npm run test:e2e`.
 */

import { test, expect } from '@playwright/test';

// ─── Session / fixture constants ──────────────────────────────────────────────

const SESSION_ID = 'evt-session-001';
const FEED_TOKEN = 'evt-feed-token';
const CHECKOUT_TOKEN = 'ckout-evt-test-001';

// ─── Small 3×4 seat map fixture ───────────────────────────────────────────────

/** 3 rows (A-C) × 4 seats = 12 seats. Kept small for fast test runs. */
function buildSchema(): object {
  const rowNames = ['A', 'B', 'C'];
  return {
    session_id: SESSION_ID,
    event_id: 'evt-event-001',
    admission_mode: 'assigned_seats',
    seating_plan_version_id: 'spv-evt-001',
    seat_status_version: 1,
    geometry_checksum: 'evt-checksum',
    capacity_seated: 12,
    capacity_standing: 0,
    geometry: {
      schema_version: 1,
      canvas: { width: 500, height: 300 },
      categories: [
        {
          index: 0,
          name: 'Parter',
          color: '#4F46E5',
          price_hint: '22.00',
          currency_hint: 'EUR',
        },
      ],
      sections: [
        {
          key: 'parter',
          name: 'Parter',
          rows: rowNames.map((rowName, rowIdx) => ({
            key: `parter-row-${rowName}`,
            name: rowName,
            seats: Array.from({ length: 4 }, (_, seatIdx) => ({
              key: `${rowName}${seatIdx + 1}`,
              number: String(seatIdx + 1),
              x: 80 + seatIdx * 80,
              y: 80 + rowIdx * 60,
              radius: 12,
              category_index: 0,
              barcode_hint: null,
            })),
          })),
        },
      ],
      standing_zones: [],
      tables: [],
      decor_svg: '',
    },
    category_prices: [
      {
        index: 0,
        name: 'Parter',
        color: '#4F46E5',
        tier_id: 'tier-parter',
        tier_name: 'Parter',
        pricing_mode: 'fixed',
        price_amount: 2200,
        currency: 'EUR',
      },
    ],
  };
}

function buildStatusResponse(): object {
  return {
    session_id: SESSION_ID,
    status_version: 1,
    delta: false,
    seats: {
      A1: 'available', A2: 'available', A3: 'available', A4: 'available',
      B1: 'available', B2: 'available', B3: 'available', B4: 'available',
      C1: 'available', C2: 'available', C3: 'available', C4: 'available',
    },
  };
}

function buildPaidStatus(): object {
  return {
    status: 'paid',
    checkout_token: CHECKOUT_TOKEN,
    checkout_session_id: 'csid-evt-001',
    expires_at: null,
    subtotal: 2200,
    discount: 0,
    platform_fee: 0,
    provider_fee: 0,
    tax: 0,
    total: 2200,
    currency: 'EUR',
    items: [
      { type: 'seat', seat_key: 'A1', sector: 'Parter', row: 'A', number: '1', unit_price: 2200, quantity: 1 },
    ],
    tickets: [
      { ticket_id: 'tkt-evt-001', sector: 'Parter', row: 'A', number: '1', human_code: 'EVT-A1-001', pdf_url: '/tickets/tkt-evt-001.pdf' },
    ],
  };
}

function buildFailedStatus(reason: 'failed' | 'expired' = 'failed'): object {
  return {
    status: reason,
    checkout_token: CHECKOUT_TOKEN,
    checkout_session_id: 'csid-evt-001',
    expires_at: null,
    subtotal: 2200,
    discount: 0,
    platform_fee: 0,
    provider_fee: 0,
    tax: 0,
    total: 2200,
    currency: 'EUR',
    items: [],
    tickets: [],
  };
}

function buildCheckoutStartResponse(): object {
  return {
    checkout_session: {},
    redirect_url: `https://checkout.stripe.com/test/${CHECKOUT_TOKEN}`,
    checkout_token: CHECKOUT_TOKEN,
    expires_at: new Date(Date.now() + 15 * 60 * 1000).toISOString(),
  };
}

// ─── Shared route setup ───────────────────────────────────────────────────────

async function setupBaseRoutes(
  page: import('@playwright/test').Page,
  opts: { checkoutStatus?: object } = {},
): Promise<void> {
  await page.route(`**/v1/event-sessions/${SESSION_ID}/schema`, (route) => {
    void route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(buildSchema()),
    });
  });

  await page.route(`**/v1/event-sessions/${SESSION_ID}/seat-status**`, (route) => {
    void route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(buildStatusResponse()),
    });
  });

  // Checkout start.
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/checkout/start`, (route) => {
    void route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(buildCheckoutStartResponse()),
    });
  });

  // Checkout status (GET by token).
  if (opts.checkoutStatus) {
    const statusBody = JSON.stringify(opts.checkoutStatus);
    await page.route(`**/v1/public/checkout/${CHECKOUT_TOKEN}`, (route) => {
      void route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: statusBody,
      });
    });
  }
}

/** Wait until the widget's shadow root contains an SVG with seat elements. */
async function waitForSeatMap(page: import('@playwright/test').Page): Promise<void> {
  await page.waitForFunction(
    () => {
      const host = document.getElementById('test-widget');
      return (host?.shadowRoot?.querySelectorAll('[data-seat-key]').length ?? 0) > 0;
    },
    { timeout: 15_000 },
  );
}

/**
 * Attach host-page event listeners and store captured events in
 * `window.__arenaEvents` for later assertion.  Must be called BEFORE the
 * action that triggers the events.
 */
async function captureEvents(page: import('@playwright/test').Page): Promise<void> {
  await page.evaluate(() => {
    (window as Record<string, unknown>)['__arenaEvents'] = [] as Array<{ name: string; detail: unknown }>;
    const EVENTS = [
      'arena:seat_selected',
      'arena:seat_released',
      'arena:payment_started',
      'arena:order_paid',
      'arena:order_failed',
    ];
    const widget = document.getElementById('test-widget');
    if (!widget) return;
    for (const name of EVENTS) {
      widget.addEventListener(name, (e) => {
        (window as Record<string, unknown[]>)['__arenaEvents'].push({
          name: (e as CustomEvent).type,
          detail: (e as CustomEvent).detail,
        });
      });
    }
  });
}

/** Read the captured events array from window.__arenaEvents. */
async function getCapturedEvents(
  page: import('@playwright/test').Page,
): Promise<Array<{ name: string; detail: Record<string, unknown> }>> {
  return page.evaluate(
    () => (window as Record<string, unknown>)['__arenaEvents'] as Array<{ name: string; detail: Record<string, unknown> }>,
  );
}

// ─── Test suite ───────────────────────────────────────────────────────────────

test.describe('WID-S5 — CustomEvents from <arena-tickets>', () => {
  // ── Seat selection events ──────────────────────────────────────────────────

  test.describe('arena:seat_selected', () => {
    test('fires with seatKey and sessionId when a seat is clicked', async ({ page }) => {
      await setupBaseRoutes(page);
      await page.goto('/demo/custom-events.html');
      await waitForSeatMap(page);
      await captureEvents(page);

      // Click the first available seat via shadowRoot.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        const seat = host?.shadowRoot?.querySelector<HTMLElement>('[data-seat-key="A1"]');
        seat?.click();
      });

      // Allow a tick for the reactive update + event dispatch.
      await page.waitForTimeout(100);

      const events = await getCapturedEvents(page);
      const selected = events.filter((e) => e.name === 'arena:seat_selected');

      expect(selected).toHaveLength(1);
      expect(selected[0]?.detail['seatKey']).toBe('A1');
      expect(selected[0]?.detail['sessionId']).toBe(SESSION_ID);
    });

    test('fires once per seat when multiple seats are clicked', async ({ page }) => {
      await setupBaseRoutes(page);
      await page.goto('/demo/custom-events.html');
      await waitForSeatMap(page);
      await captureEvents(page);

      // Click three distinct seats.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        const sr = host?.shadowRoot;
        sr?.querySelector<HTMLElement>('[data-seat-key="A1"]')?.click();
        sr?.querySelector<HTMLElement>('[data-seat-key="A2"]')?.click();
        sr?.querySelector<HTMLElement>('[data-seat-key="B1"]')?.click();
      });

      await page.waitForTimeout(100);

      const events = await getCapturedEvents(page);
      const selected = events.filter((e) => e.name === 'arena:seat_selected');

      expect(selected).toHaveLength(3);
      expect(selected.map((e) => e.detail['seatKey'])).toEqual(
        expect.arrayContaining(['A1', 'A2', 'B1']),
      );
    });

    test('does NOT fire for a held seat', async ({ page }) => {
      // Override seat status with A1 held.
      await page.route(`**/v1/event-sessions/${SESSION_ID}/seat-status**`, (route) => {
        void route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            ...buildStatusResponse(),
            seats: { ...buildStatusResponse().seats as Record<string, string>, A1: 'held' },
          }),
        });
      });
      await setupBaseRoutes(page);
      await page.goto('/demo/custom-events.html');
      await waitForSeatMap(page);
      await captureEvents(page);

      // Wait for seat-status to update A1 to held.
      await page.waitForTimeout(500);

      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        host?.shadowRoot?.querySelector<HTMLElement>('[data-seat-key="A1"]')?.click();
      });

      await page.waitForTimeout(100);

      const events = await getCapturedEvents(page);
      const selected = events.filter((e) => e.name === 'arena:seat_selected');

      // Held seats cannot be selected — no event should fire.
      expect(selected).toHaveLength(0);
    });
  });

  // ── Seat deselection events ────────────────────────────────────────────────

  test.describe('arena:seat_released', () => {
    test('fires when a selected seat is clicked again to deselect', async ({ page }) => {
      await setupBaseRoutes(page);
      await page.goto('/demo/custom-events.html');
      await waitForSeatMap(page);
      await captureEvents(page);

      // Select A1, then deselect it.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        const seat = host?.shadowRoot?.querySelector<HTMLElement>('[data-seat-key="A1"]');
        seat?.click(); // select
        seat?.click(); // deselect
      });

      await page.waitForTimeout(100);

      const events = await getCapturedEvents(page);
      const selected = events.filter((e) => e.name === 'arena:seat_selected');
      const released = events.filter((e) => e.name === 'arena:seat_released');

      expect(selected).toHaveLength(1);
      expect(selected[0]?.detail['seatKey']).toBe('A1');

      expect(released).toHaveLength(1);
      expect(released[0]?.detail['seatKey']).toBe('A1');
      expect(released[0]?.detail['sessionId']).toBe(SESSION_ID);
    });

    test('selecting and deselecting produces balanced selected/released pairs', async ({ page }) => {
      await setupBaseRoutes(page);
      await page.goto('/demo/custom-events.html');
      await waitForSeatMap(page);
      await captureEvents(page);

      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        const sr = host?.shadowRoot;
        // Select A1, A2, A3 — then release A2.
        sr?.querySelector<HTMLElement>('[data-seat-key="A1"]')?.click();
        sr?.querySelector<HTMLElement>('[data-seat-key="A2"]')?.click();
        sr?.querySelector<HTMLElement>('[data-seat-key="A3"]')?.click();
        sr?.querySelector<HTMLElement>('[data-seat-key="A2"]')?.click(); // deselect A2
      });

      await page.waitForTimeout(100);

      const events = await getCapturedEvents(page);
      const selected = events.filter((e) => e.name === 'arena:seat_selected');
      const released = events.filter((e) => e.name === 'arena:seat_released');

      expect(selected).toHaveLength(3);
      expect(released).toHaveLength(1);
      expect(released[0]?.detail['seatKey']).toBe('A2');
    });
  });

  // ── Order outcome events ───────────────────────────────────────────────────

  test.describe('arena:order_paid', () => {
    test('fires with token, total, and currency when order status is paid', async ({ page }) => {
      await setupBaseRoutes(page, { checkoutStatus: buildPaidStatus() });

      // Load with checkout_token in the URL so the widget enters order-status stage.
      await captureEvents(page);
      await page.goto(
        `/demo/custom-events.html?checkout_token=${CHECKOUT_TOKEN}`,
      );

      // Wait for event to fire (order status loads on mount).
      await page.waitForFunction(
        () => {
          const events = (window as Record<string, unknown>)['__arenaEvents'] as Array<{ name: string }> | undefined;
          return events?.some((e) => e.name === 'arena:order_paid');
        },
        { timeout: 10_000 },
      );

      const events = await getCapturedEvents(page);
      const paid = events.filter((e) => e.name === 'arena:order_paid');

      expect(paid).toHaveLength(1);
      expect(paid[0]?.detail['checkoutToken']).toBe(CHECKOUT_TOKEN);
      expect(paid[0]?.detail['totalMinorUnits']).toBe(2200);
      expect(paid[0]?.detail['currency']).toBe('EUR');
      // orderRef is null until the backend surfaces it.
      expect(paid[0]?.detail['orderRef']).toBeNull();
    });

    test('does NOT emit order_failed when status is paid', async ({ page }) => {
      await setupBaseRoutes(page, { checkoutStatus: buildPaidStatus() });
      await captureEvents(page);
      await page.goto(`/demo/custom-events.html?checkout_token=${CHECKOUT_TOKEN}`);

      await page.waitForFunction(
        () => {
          const events = (window as Record<string, unknown>)['__arenaEvents'] as Array<{ name: string }> | undefined;
          return events?.some((e) => e.name === 'arena:order_paid');
        },
        { timeout: 10_000 },
      );

      const events = await getCapturedEvents(page);
      expect(events.filter((e) => e.name === 'arena:order_failed')).toHaveLength(0);
    });
  });

  test.describe('arena:order_failed', () => {
    test('fires with checkoutToken and reason "failed" when order fails', async ({ page }) => {
      await setupBaseRoutes(page, { checkoutStatus: buildFailedStatus('failed') });
      await captureEvents(page);
      await page.goto(`/demo/custom-events.html?checkout_token=${CHECKOUT_TOKEN}`);

      await page.waitForFunction(
        () => {
          const events = (window as Record<string, unknown>)['__arenaEvents'] as Array<{ name: string }> | undefined;
          return events?.some((e) => e.name === 'arena:order_failed');
        },
        { timeout: 10_000 },
      );

      const events = await getCapturedEvents(page);
      const failed = events.filter((e) => e.name === 'arena:order_failed');

      expect(failed).toHaveLength(1);
      expect(failed[0]?.detail['checkoutToken']).toBe(CHECKOUT_TOKEN);
      expect(failed[0]?.detail['reason']).toBe('failed');
    });

    test('fires with reason "expired" when order expires', async ({ page }) => {
      await setupBaseRoutes(page, { checkoutStatus: buildFailedStatus('expired') });
      await captureEvents(page);
      await page.goto(`/demo/custom-events.html?checkout_token=${CHECKOUT_TOKEN}`);

      await page.waitForFunction(
        () => {
          const events = (window as Record<string, unknown>)['__arenaEvents'] as Array<{ name: string }> | undefined;
          return events?.some((e) => e.name === 'arena:order_failed');
        },
        { timeout: 10_000 },
      );

      const events = await getCapturedEvents(page);
      const failed = events.filter((e) => e.name === 'arena:order_failed');

      expect(failed).toHaveLength(1);
      expect(failed[0]?.detail['reason']).toBe('expired');
    });

    test('does NOT emit order_paid when status is failed', async ({ page }) => {
      await setupBaseRoutes(page, { checkoutStatus: buildFailedStatus('failed') });
      await captureEvents(page);
      await page.goto(`/demo/custom-events.html?checkout_token=${CHECKOUT_TOKEN}`);

      await page.waitForFunction(
        () => {
          const events = (window as Record<string, unknown>)['__arenaEvents'] as Array<{ name: string }> | undefined;
          return events?.some((e) => e.name === 'arena:order_failed');
        },
        { timeout: 10_000 },
      );

      const events = await getCapturedEvents(page);
      expect(events.filter((e) => e.name === 'arena:order_paid')).toHaveLength(0);
    });
  });

  // ── Cross-shadow-DOM boundary ─────────────────────────────────────────────

  test.describe('composed events cross Shadow DOM boundary', () => {
    test('seat_selected listener on host element receives event fired inside shadow DOM', async ({ page }) => {
      await setupBaseRoutes(page);
      await page.goto('/demo/custom-events.html');
      await waitForSeatMap(page);

      // Attach listener directly on the document (above the host element).
      await page.evaluate(() => {
        (window as Record<string, unknown>)['__docEvents'] = [] as Array<{ name: string; detail: unknown }>;
        document.addEventListener('arena:seat_selected', (e) => {
          (window as Record<string, unknown[]>)['__docEvents'].push({
            name: (e as CustomEvent).type,
            detail: (e as CustomEvent).detail,
          });
        });
      });

      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        host?.shadowRoot?.querySelector<HTMLElement>('[data-seat-key="B1"]')?.click();
      });

      await page.waitForTimeout(100);

      const docEvents = await page.evaluate(
        () => (window as Record<string, unknown>)['__docEvents'] as Array<{ name: string; detail: Record<string, unknown> }>,
      );

      // The composed event must bubble all the way to document.
      expect(docEvents).toHaveLength(1);
      expect(docEvents[0]?.detail['seatKey']).toBe('B1');
    });
  });

  // ── No spurious events ────────────────────────────────────────────────────

  test.describe('no spurious events', () => {
    test('loading the widget without interaction emits no seat events', async ({ page }) => {
      await setupBaseRoutes(page);
      await captureEvents(page);
      await page.goto('/demo/custom-events.html');
      await waitForSeatMap(page);

      // Wait a beat for any spurious events.
      await page.waitForTimeout(200);

      const events = await getCapturedEvents(page);
      const seatEvents = events.filter(
        (e) => e.name === 'arena:seat_selected' || e.name === 'arena:seat_released',
      );

      expect(seatEvents).toHaveLength(0);
    });
  });
});
