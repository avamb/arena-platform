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
 * Install event listeners via page.addInitScript() so they are registered
 * BEFORE the page navigates.  This is required for events fired during onMount
 * (e.g. arena:order_paid / arena:order_failed) which can fire before any
 * post-navigation page.evaluate() call can run.
 *
 * Must be called BEFORE page.goto().  Uses window.addEventListener so the
 * listeners catch bubbled composed events from inside the Shadow DOM even
 * though the custom element is not yet registered at script install time.
 */
async function preCaptureEvents(page: import('@playwright/test').Page): Promise<void> {
  await page.addInitScript(() => {
    (window as Record<string, unknown>)['__arenaEvents'] = [] as Array<{ name: string; detail: unknown }>;
    const EVENTS = [
      'arena:seat_selected',
      'arena:seat_released',
      'arena:payment_started',
      'arena:order_paid',
      'arena:order_failed',
      'arena:cart_opened',
      'arena:recovery',
    ];
    for (const name of EVENTS) {
      window.addEventListener(name, (e) => {
        (window as Record<string, unknown[]>)['__arenaEvents'].push({
          name: (e as CustomEvent).type,
          detail: (e as CustomEvent).detail,
        });
      });
    }
  });
}

/**
 * Attach host-page event listeners and store captured events in
 * `window.__arenaEvents` for later assertion.  Must be called BEFORE the
 * action that triggers the events.
 */
async function captureEvents(page: import('@playwright/test').Page): Promise<void> {
  await page.evaluate(() => {
    (window as Record<string, unknown>)['__arenaEvents'] = [] as Array<{ name: string; detail: unknown }>;
    // WID-T2: added cart_opened and recovery to the captured event list.
    const EVENTS = [
      'arena:seat_selected',
      'arena:seat_released',
      'arena:payment_started',
      'arena:order_paid',
      'arena:order_failed',
      'arena:cart_opened',
      'arena:recovery',
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
      // WID-T2: use dispatchEvent instead of .click() so the MouseEvent bubbles
      // correctly through the SVG shadow-root tree to the container's onclick handler.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        const seat = host?.shadowRoot?.querySelector<Element>('[data-seat-key="A1"]');
        seat?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, composed: true }));
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
      // WID-T2: dispatchEvent instead of .click() for SVG elements.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        const sr = host?.shadowRoot;
        const click = (key: string) =>
          sr?.querySelector(`[data-seat-key="${key}"]`)?.dispatchEvent(
            new MouseEvent('click', { bubbles: true, cancelable: true, composed: true }),
          );
        click('A1');
        click('A2');
        click('B1');
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
      // setupBaseRoutes registers the default seat-status route first.
      // The A1-held override is registered AFTER so Playwright's LIFO route
      // matching gives it higher priority (last registered wins).
      await setupBaseRoutes(page);
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
      await page.goto('/demo/custom-events.html');
      await waitForSeatMap(page);
      await captureEvents(page);

      // Wait for seat-status to update A1 to held.
      await page.waitForTimeout(500);

      // WID-T2: dispatchEvent for SVG element click.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        host?.shadowRoot
          ?.querySelector('[data-seat-key="A1"]')
          ?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, composed: true }));
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
      // WID-T2: dispatchEvent for SVG elements.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        const seat = host?.shadowRoot?.querySelector('[data-seat-key="A1"]');
        const evt = () => new MouseEvent('click', { bubbles: true, cancelable: true, composed: true });
        seat?.dispatchEvent(evt()); // select
        seat?.dispatchEvent(evt()); // deselect
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

      // WID-T2: dispatchEvent for SVG elements.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        const sr = host?.shadowRoot;
        const click = (key: string) =>
          sr?.querySelector(`[data-seat-key="${key}"]`)?.dispatchEvent(
            new MouseEvent('click', { bubbles: true, cancelable: true, composed: true }),
          );
        // Select A1, A2, A3 — then release A2.
        click('A1');
        click('A2');
        click('A3');
        click('A2'); // deselect A2
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

      // WID-T2: arena:order_paid fires from onMount → loadOrderStatus() which
      // resolves almost immediately from the mock. captureEvents() called after
      // page.goto() is too late because the event fires during page load.
      // preCaptureEvents() installs window-level listeners via addInitScript()
      // so they are registered before any script executes on the page.
      await preCaptureEvents(page);
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
      // WID-T2: pre-capture so onMount events are not missed.
      await preCaptureEvents(page);
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
      // WID-T2: pre-capture so onMount events are not missed.
      await preCaptureEvents(page);
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
      // WID-T2: pre-capture so onMount events are not missed.
      await preCaptureEvents(page);
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
      // WID-T2: pre-capture so onMount events are not missed.
      await preCaptureEvents(page);
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

      // WID-T2: dispatchEvent for SVG element click.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        host?.shadowRoot
          ?.querySelector('[data-seat-key="B1"]')
          ?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, composed: true }));
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
      // WID-T2: navigate before captureEvents (window replacement fix).
      await page.goto('/demo/custom-events.html');
      await captureEvents(page);
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

// ─── WID-T2: Real Playwright coordinate click via hit-testing ─────────────────
//
// These tests use page.mouse.click(x, y) — real OS-level pointer events —
// rather than synthetic seat?.click() or dispatchEvent() in page.evaluate().
// The full event chain (pointerdown → setPointerCapture → pointerup → click)
// fires, which is the only reliable way to validate that the widget's
// setPointerCapture call in onPointerDown does NOT break subsequent tap
// recognition in onContainerClick.
//
// Rule (from WID-T2 spec): if this test fails, FIX THE WIDGET CODE — do NOT
// delete or skip this test.

test.describe('WID-T2 — Real coordinate click via Playwright hit-testing', () => {
  const RECOVER_TOKEN = 'ckout-recover-t2-001';

  async function buildRecoverSchema(): Promise<object> {
    const rowNames = ['A', 'B'];
    return {
      session_id: SESSION_ID,
      event_id: 'evt-event-t2',
      admission_mode: 'assigned_seats',
      seating_plan_version_id: 'spv-t2-001',
      seat_status_version: 1,
      geometry_checksum: 't2-checksum',
      capacity_seated: 8,
      capacity_standing: 0,
      geometry: {
        schema_version: 1,
        canvas: { width: 500, height: 300 },
        categories: [
          { index: 0, name: 'Parter', color: '#4F46E5', price_hint: '22.00', currency_hint: 'EUR' },
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

  test('real mouse click at seat SVG coordinates triggers arena:seat_selected', async ({ page }) => {
    // Set up routes for the seat map.
    await page.route(`**/v1/event-sessions/${SESSION_ID}/schema`, async (route) => {
      void route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(await buildRecoverSchema()),
      });
    });
    await page.route(`**/v1/event-sessions/${SESSION_ID}/seat-status**`, (route) => {
      void route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(buildStatusResponse()),
      });
    });

    await page.goto('/demo/custom-events.html');
    await waitForSeatMap(page);
    await captureEvents(page);

    // Get the center of seat A1 in page (viewport) coordinates.
    // SVGElement.getBoundingClientRect() returns the post-transform visual box.
    const seatCenter = await page.evaluate(() => {
      const host = document.getElementById('test-widget');
      const seat = host?.shadowRoot?.querySelector('[data-seat-key="A1"]');
      if (!seat) return null;
      const rect = seat.getBoundingClientRect();
      if (rect.width === 0 && rect.height === 0) return null;
      return {
        x: rect.left + rect.width / 2,
        y: rect.top + rect.height / 2,
        width: rect.width,
        height: rect.height,
      };
    });

    // Ensure the seat is visible and within the viewport before clicking.
    expect(
      seatCenter,
      'Seat A1 must have a non-zero bounding rect (visible on screen)',
    ).not.toBeNull();

    if (!seatCenter) throw new Error('Seat A1 not visible');

    // Perform a REAL Playwright mouse click:
    // 1. page.mouse.click sends: mousemove → pointerdown → mousedown →
    //    [setPointerCapture captured here in onPointerDown] → pointerup →
    //    mouseup → click.
    // 2. The click event fires at the SVG circle and bubbles to the
    //    seat-map-container's onContainerClick.
    // 3. pointerDownX/Y are set from the pointerdown position, so
    //    |dx| = |dy| = 0 — below the DRAG_THRESHOLD of 8 px.
    await page.mouse.click(seatCenter.x, seatCenter.y);

    // Allow a tick for the Svelte reactive update + dispatchWidgetEvent.
    await page.waitForTimeout(150);

    const events = await getCapturedEvents(page);
    const selected = events.filter((e) => e.name === 'arena:seat_selected');

    expect(
      selected,
      'Real coordinate click must fire arena:seat_selected — if this fails, fix ' +
      'setPointerCapture handling in SeatMapView.svelte, NOT this test.',
    ).toHaveLength(1);
    expect(selected[0]?.detail['seatKey']).toBe('A1');
    expect(selected[0]?.detail['sessionId']).toBe(SESSION_ID);
  });
});

// ─── WID-T2: cart_opened and recovery events ─────────────────────────────────

test.describe('WID-T2 — arena:cart_opened and arena:recovery events', () => {
  const RECOVER_TOKEN = 'ckout-t2-recover-001';

  // Build a minimal expired-hold status for recovery tests.
  function buildExpiredStatus(): object {
    return {
      status: 'expired',
      checkout_token: RECOVER_TOKEN,
      checkout_session_id: 'csid-t2-recover',
      expires_at: new Date(Date.now() - 5_000).toISOString(),
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
      tickets: [],
    };
  }

  // ── arena:cart_opened ─────────────────────────────────────────────────────

  test.describe('arena:cart_opened', () => {
    test('fires with sessionId and itemCount when MiniCart is clicked', async ({ page }) => {
      await setupBaseRoutes(page);
      await page.goto('/demo/custom-events.html');
      await waitForSeatMap(page);
      await captureEvents(page);

      // Select seat A1 to make the MiniCart appear (visible when cartCount > 0).
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        host?.shadowRoot
          ?.querySelector('[data-seat-key="A1"]')
          ?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, composed: true }));
      });

      // Wait for MiniCart to appear (renders when lines.length > 0).
      await page.waitForFunction(
        () => {
          const host = document.getElementById('test-widget');
          return host?.shadowRoot?.querySelector('.mini-cart-btn') !== null;
        },
        { timeout: 5_000 },
      );

      // Click the MiniCart button — triggers openCartSheet() → dispatches cart_opened.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        const btn = host?.shadowRoot?.querySelector<HTMLElement>('.mini-cart-btn');
        btn?.click();
      });

      await page.waitForTimeout(100);

      const events = await getCapturedEvents(page);
      const cartEvents = events.filter((e) => e.name === 'arena:cart_opened');

      expect(cartEvents, 'Clicking MiniCart must fire arena:cart_opened').toHaveLength(1);
      expect(cartEvents[0]?.detail['sessionId']).toBe(SESSION_ID);
      // 1 seat selected → itemCount = 1.
      expect(cartEvents[0]?.detail['itemCount']).toBe(1);
    });

    test('itemCount reflects multiple selected seats', async ({ page }) => {
      await setupBaseRoutes(page);
      await page.goto('/demo/custom-events.html');
      await waitForSeatMap(page);
      await captureEvents(page);

      // Select 2 seats.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        const sr = host?.shadowRoot;
        const click = (key: string) =>
          sr?.querySelector(`[data-seat-key="${key}"]`)?.dispatchEvent(
            new MouseEvent('click', { bubbles: true, cancelable: true, composed: true }),
          );
        click('A1');
        click('A2');
      });

      // Wait for MiniCart.
      await page.waitForFunction(
        () => {
          const host = document.getElementById('test-widget');
          return host?.shadowRoot?.querySelector('.mini-cart-btn') !== null;
        },
        { timeout: 5_000 },
      );

      // Open cart.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        host?.shadowRoot?.querySelector<HTMLElement>('.mini-cart-btn')?.click();
      });

      await page.waitForTimeout(100);

      const events = await getCapturedEvents(page);
      const cartEvents = events.filter((e) => e.name === 'arena:cart_opened');

      expect(cartEvents).toHaveLength(1);
      expect(cartEvents[0]?.detail['itemCount']).toBe(2);
    });
  });

  // ── arena:recovery ────────────────────────────────────────────────────────

  test.describe('arena:recovery', () => {
    test('does NOT fire on a plain checkout-token resume', async ({ page }) => {
      await page.route(`**/v1/public/checkout/${RECOVER_TOKEN}`, (route) => {
        void route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(buildExpiredStatus()),
        });
      });
      await preCaptureEvents(page);

      await page.goto(`/demo/custom-events.html?checkout_token=${RECOVER_TOKEN}`);
      await page.waitForFunction(
        () => document.getElementById('test-widget')?.shadowRoot?.querySelector('[data-status="expired"]') !== null,
        { timeout: 10_000 },
      );

      const events = await getCapturedEvents(page);
      expect(events.filter((e) => e.name === 'arena:recovery')).toHaveLength(0);
    });

    test('fires after successful silent recovery from a 401 status response', async ({ page }) => {
      const refreshedToken = `${RECOVER_TOKEN}-refreshed`;
      const newExpiry = new Date(Date.now() + 15 * 60 * 1000).toISOString();
      await page.route(`**/v1/public/checkout/${RECOVER_TOKEN}`, (route) => {
        void route.fulfill({
          status: 401,
          contentType: 'application/json',
          body: JSON.stringify({ error: { code: 'checkout.expired', message: 'Token expired' } }),
        });
      });
      await page.route(`**/v1/public/checkout/${RECOVER_TOKEN}/recover`, (route) => {
        void route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            checkout_session: {},
            checkout_token: refreshedToken,
            expires_at: newExpiry,
          }),
        });
      });
      await page.route(`**/v1/public/checkout/${refreshedToken}`, (route) => {
        void route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ ...buildExpiredStatus(), checkout_token: refreshedToken }),
        });
      });
      await preCaptureEvents(page);

      await page.goto(`/demo/custom-events.html?checkout_token=${RECOVER_TOKEN}`);
      await page.waitForFunction(() => {
        const events = (window as Record<string, unknown>)['__arenaEvents'] as Array<{ name: string }> | undefined;
        return events?.some((e) => e.name === 'arena:recovery');
      }, { timeout: 10_000 });

      const recoveries = (await getCapturedEvents(page)).filter((e) => e.name === 'arena:recovery');
      expect(recoveries).toHaveLength(1);
      expect(recoveries[0]?.detail['checkoutToken']).toBe(refreshedToken);
      expect(recoveries[0]?.detail['expiresAt']).toBe(newExpiry);
    });

    test('fires with checkoutToken and expiresAt when recover call succeeds', async ({ page }) => {
      const newExpiry = new Date(Date.now() + 15 * 60 * 1000).toISOString();

      // Route: GET checkout status → expired.
      await page.route(`**/v1/public/checkout/${RECOVER_TOKEN}`, (route) => {
        void route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(buildExpiredStatus()),
        });
      });

      // Route: POST checkout recover → success.
      await page.route(`**/v1/public/checkout/${RECOVER_TOKEN}/recover`, (route) => {
        void route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            checkout_session: {},
            checkout_token: RECOVER_TOKEN,
            expires_at: newExpiry,
          }),
        });
      });

      // Navigate to order-status deep-link (checkout_token in URL triggers onMount).
      await page.goto(`/demo/custom-events.html?checkout_token=${RECOVER_TOKEN}`);

      // Wait for the expired status screen to appear before attaching listeners.
      await page.waitForFunction(
        () => {
          const host = document.getElementById('test-widget');
          return host?.shadowRoot?.querySelector('[data-status="expired"]') !== null;
        },
        { timeout: 10_000 },
      );

      // Attach listeners after the expired screen renders; this test exercises
      // the explicit reclaim action rather than the mount/resume path.
      await captureEvents(page);

      // Click the "Reclaim seats" button (action-btn primary inside status-expired).
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        const btn = host?.shadowRoot?.querySelector<HTMLElement>('.status-expired .action-btn.primary');
        btn?.click();
      });

      // Wait for recovery event.
      await page.waitForFunction(
        () => {
          const events = (window as Record<string, unknown>)['__arenaEvents'] as Array<{ name: string }> | undefined;
          return events?.some((e) => e.name === 'arena:recovery');
        },
        { timeout: 10_000 },
      );

      const events = await getCapturedEvents(page);
      const recoveries = events.filter((e) => e.name === 'arena:recovery');

      expect(recoveries, 'Successful recover call must fire arena:recovery').toHaveLength(1);
      expect(recoveries[0]?.detail['checkoutToken']).toBe(RECOVER_TOKEN);
      // expiresAt must be the fresh value from the recover response (non-empty).
      expect(recoveries[0]?.detail['expiresAt']).toBe(newExpiry);
    });

    test('does NOT fire arena:recovery when recover returns 409', async ({ page }) => {
      // Route: GET checkout status → expired.
      await page.route(`**/v1/public/checkout/${RECOVER_TOKEN}`, (route) => {
        void route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(buildExpiredStatus()),
        });
      });

      // Route: POST checkout recover → 409 conflict.
      await page.route(`**/v1/public/checkout/${RECOVER_TOKEN}/recover`, (route) => {
        void route.fulfill({
          status: 409,
          contentType: 'application/json',
          body: JSON.stringify({
            error: { code: 'seat_conflict', message: 'Seats are no longer available', details: null },
          }),
        });
      });

      await page.goto(`/demo/custom-events.html?checkout_token=${RECOVER_TOKEN}`);

      // Wait for expired screen.
      await page.waitForFunction(
        () => {
          const host = document.getElementById('test-widget');
          return host?.shadowRoot?.querySelector('[data-status="expired"]') !== null;
        },
        { timeout: 10_000 },
      );

      await captureEvents(page);

      // Click recover button.
      await page.evaluate(() => {
        const host = document.getElementById('test-widget');
        host?.shadowRoot?.querySelector<HTMLElement>('.status-expired .action-btn.primary')?.click();
      });

      // Wait briefly for any async operations.
      await page.waitForTimeout(500);

      const events = await getCapturedEvents(page);
      const recoveries = events.filter((e) => e.name === 'arena:recovery');

      // On 409 the recovery fails — no recovery event should be dispatched.
      expect(recoveries, 'arena:recovery must NOT fire when recover call returns 409').toHaveLength(0);
    });
  });
});
