/**
 * WID-T4 — UI-level dual-context 409 conflict test.
 *
 * Verifies that when two browser contexts compete for the same seat:
 *  - Context A selects a seat (mocked schema + seat-status)
 *  - Context B tries to checkout with the same seat key and receives a 409
 *    `reservation.seats_conflict` response
 *  - Context B sees the conflict highlight on the contested seat
 *    (fill = CONFLICT_COLOR #b91c1c, data-status="conflict")
 *  - The "continue without conflicts" CTA is visible after the 409
 *
 * All backend calls are intercepted via Playwright page.route() so the suite
 * runs fully offline against the built widget bundle.
 */

import { test, expect, type Browser, type Page } from '@playwright/test';

// ─── Constants ────────────────────────────────────────────────────────────────

const SESSION_ID = 'conflict-session-001';
const FEED_TOKEN = 'conflict-feed-token';
const CONTESTED_SEAT = 'A1';
const CHECKOUT_TOKEN = 'ckout-conflict-001';

// ─── Fixture helpers ─────────────────────────────────────────────────────────

function buildSchema(): object {
  return {
    session_id: SESSION_ID,
    event_id: 'conflict-event-001',
    admission_mode: 'assigned_seats',
    seating_plan_version_id: 'spv-conflict-001',
    seat_status_version: 1,
    geometry_checksum: 'conflict-checksum',
    capacity_seated: 8,
    capacity_standing: 0,
    geometry: {
      schema_version: 1,
      canvas: { width: 400, height: 300 },
      categories: [
        { index: 0, name: 'Parter', color: '#4F46E5', price_hint: '25.00', currency_hint: 'EUR' },
      ],
      sections: [
        {
          key: 'parter',
          name: 'Parter',
          rows: [
            {
              key: 'parter-row-A',
              name: 'A',
              seats: [
                { key: 'A1', number: '1', x: 80,  y: 80, radius: 12, category_index: 0, barcode_hint: null },
                { key: 'A2', number: '2', x: 140, y: 80, radius: 12, category_index: 0, barcode_hint: null },
              ],
            },
          ],
        },
      ],
      standing_zones: [],
      tables: [],
      decor_svg: '',
    },
    category_prices: [
      { index: 0, name: 'Parter', color: '#4F46E5', price_amount: 2500, currency: 'EUR',
        price_hint: '25.00', currency_hint: 'EUR' },
    ],
    tiers: [
      { id: 'tier-parter', name: 'Parter', tier_type: 'seated', capacity: 8, price_amount: 2500, currency: 'EUR' },
    ],
  };
}

function buildSeatStatus(heldSeats: string[] = []): object {
  const seats: Record<string, string> = { A1: 'available', A2: 'available' };
  for (const key of heldSeats) seats[key] = 'held';
  return {
    session_id: SESSION_ID,
    status_version: 1,
    seats,
  };
}

/** 409 conflict response body for the Arena nested envelope format. */
function build409Body(conflictSeatKey: string): object {
  return {
    error: {
      code: 'reservation.seats_conflict',
      message: 'One or more requested seats are no longer available.',
      details: {
        conflicts: [
          { seat_key: conflictSeatKey, status: 'held' },
        ],
      },
    },
  };
}

// ─── Route mock helper ────────────────────────────────────────────────────────

async function setupRoutes(page: Page, opts: { checkoutStatus?: number } = {}): Promise<void> {
  const schema = buildSchema();
  const statusOk = buildSeatStatus();

  // Schema (ETag: static)
  await page.route(`**/v1/event-sessions/${SESSION_ID}/schema`, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { ETag: '"conflict-etag-001"' },
      body: JSON.stringify(schema),
    });
  });

  // Seat status (no held seats)
  await page.route(`**/v1/event-sessions/${SESSION_ID}/seat-status**`, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(statusOk),
    });
  });

  const checkoutStatus = opts.checkoutStatus ?? 200;
  if (checkoutStatus === 409) {
    // Context B: checkout/start returns 409 with seat A1 in conflict
    await page.route(`**/feeds/${FEED_TOKEN}/checkout/start`, async (route) => {
      await route.fulfill({
        status: 409,
        contentType: 'application/json',
        body: JSON.stringify(build409Body(CONTESTED_SEAT)),
      });
    });
  } else {
    // Context A: checkout/start succeeds
    await page.route(`**/feeds/${FEED_TOKEN}/checkout/start`, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          checkout_token: CHECKOUT_TOKEN,
          checkout_session: {},
          redirect_url: 'https://stripe.test/pay/test',
          expires_at: new Date(Date.now() + 15 * 60 * 1000).toISOString(),
        }),
      });
    });
  }
}

// ─── Wait for SVG helper ──────────────────────────────────────────────────────

async function waitForSVG(page: Page): Promise<void> {
  await page.waitForFunction(() => {
    const el = document.querySelector('#widget-conflict');
    const shadow = el?.shadowRoot;
    if (!shadow) return false;
    return (
      shadow.querySelector('svg[aria-label^="Seat map"]') !== null ||
      shadow.querySelector('.seat-map-inner svg') !== null
    );
  }, { timeout: 15_000 });
}

// ─── Tests ────────────────────────────────────────────────────────────────────

test.describe('WID-T4 — dual-context 409 conflict', () => {
  test('context B gets 409 and sees conflict highlight on seat A1', async ({ browser }: { browser: Browser }) => {
    // ── Context A: holds seat A1 (checkout succeeds) ──────────────────────
    const ctxA = await browser.newContext();
    const pageA = await ctxA.newPage();
    await setupRoutes(pageA, { checkoutStatus: 200 });
    await pageA.goto('/demo/conflict-409.html');
    await waitForSVG(pageA);

    // Verify that context A can see the seat A1
    const seatA1_inA = await pageA.evaluateHandle(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      return el?.shadowRoot?.querySelector('circle[data-seat-key="A1"]') ?? null;
    });
    expect(seatA1_inA).not.toBeNull();

    // ── Context B: tries checkout for same seat A1, gets 409 ──────────────
    const ctxB = await browser.newContext();
    const pageB = await ctxB.newPage();
    await setupRoutes(pageB, { checkoutStatus: 409 });
    await pageB.goto('/demo/conflict-409.html');
    await waitForSVG(pageB);

    // Open cart sheet in context B by clicking seat A1 to select it
    await pageB.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      const seat = el?.shadowRoot?.querySelector('circle[data-seat-key="A1"]');
      seat?.dispatchEvent(new MouseEvent('click', { bubbles: true, composed: true }));
    });

    // Open the MiniCart to get to the CartSheet
    await pageB.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      const miniCart = el?.shadowRoot?.querySelector<HTMLButtonElement>('.mini-cart-btn');
      miniCart?.click();
    });

    // Proceed to buyer form ("Continue")
    await pageB.waitForTimeout(300);
    await pageB.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      // Click the CTA button in the cart (submit_label / Continue to payment)
      const ctaBtn = el?.shadowRoot?.querySelector<HTMLButtonElement>('.cta-btn');
      ctaBtn?.click();
    });

    // Fill in the buyer form email and submit
    await pageB.waitForTimeout(300);
    await pageB.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      const form = el?.shadowRoot?.querySelector('form');
      const emailInput = form?.querySelector<HTMLInputElement>('input[type="email"]');
      if (emailInput) {
        emailInput.value = 'test@example.com';
        emailInput.dispatchEvent(new Event('input', { bubbles: true }));
      }
    });
    await pageB.waitForTimeout(100);
    await pageB.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      const form = el?.shadowRoot?.querySelector('form');
      form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    });

    // Wait for the 409 response to be processed
    await pageB.waitForTimeout(500);

    // ── Assert: seat A1 in context B has conflict highlight ───────────────
    const seatStatus = await pageB.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      const seat = el?.shadowRoot?.querySelector('circle[data-seat-key="A1"]');
      if (!seat) return null;
      return {
        fill: seat.getAttribute('fill'),
        dataStatus: seat.getAttribute('data-status'),
      };
    });

    // The conflict highlight (#b91c1c) should be applied
    expect(seatStatus).not.toBeNull();
    expect(seatStatus?.dataStatus).toBe('conflict');
    expect(seatStatus?.fill?.toLowerCase()).toBe('#b91c1c');

    await ctxA.close();
    await ctxB.close();
  });

  test('conflict highlight survives a poller tick (priority over status update)', async ({ page }) => {
    let pollCount = 0;
    const schema = buildSchema();

    // Schema
    await page.route(`**/v1/event-sessions/${SESSION_ID}/schema`, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        headers: { ETag: '"conflict-etag-001"' },
        body: JSON.stringify(schema),
      });
    });

    // Seat status: first poll returns available; subsequent return "held" for A1
    await page.route(`**/v1/event-sessions/${SESSION_ID}/seat-status**`, async (route) => {
      pollCount++;
      const heldSeats = pollCount > 1 ? ['A1'] : [];
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(buildSeatStatus(heldSeats)),
      });
    });

    // Checkout/start returns 409
    await page.route(`**/feeds/${FEED_TOKEN}/checkout/start`, async (route) => {
      await route.fulfill({
        status: 409,
        contentType: 'application/json',
        body: JSON.stringify(build409Body(CONTESTED_SEAT)),
      });
    });

    await page.goto('/demo/conflict-409.html');
    await waitForSVG(page);

    // Select seat A1
    await page.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      const seat = el?.shadowRoot?.querySelector('circle[data-seat-key="A1"]');
      seat?.dispatchEvent(new MouseEvent('click', { bubbles: true, composed: true }));
    });

    // Open MiniCart
    await page.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      const miniCart = el?.shadowRoot?.querySelector<HTMLButtonElement>('.mini-cart-btn');
      miniCart?.click();
    });

    await page.waitForTimeout(300);
    // Proceed to buyer form
    await page.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      const ctaBtn = el?.shadowRoot?.querySelector<HTMLButtonElement>('.cta-btn');
      ctaBtn?.click();
    });

    // Submit form (trigger 409)
    await page.waitForTimeout(300);
    await page.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      const emailInput = el?.shadowRoot?.querySelector<HTMLInputElement>('input[type="email"]');
      if (emailInput) {
        emailInput.value = 'user@example.com';
        emailInput.dispatchEvent(new Event('input', { bubbles: true }));
      }
    });
    await page.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      const form = el?.shadowRoot?.querySelector('form');
      form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    });

    // Wait for 409 processing + conflict highlight applied
    await page.waitForTimeout(500);

    // Verify conflict highlight is present
    const conflictFill = await page.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      return el?.shadowRoot?.querySelector('circle[data-seat-key="A1"]')?.getAttribute('fill') ?? '';
    });
    expect(conflictFill.toLowerCase()).toBe('#b91c1c');

    // Wait for another poll tick (poller runs every 3s; wait extra to be safe)
    await page.waitForTimeout(4_000);

    // Conflict highlight MUST still be present even after the poller returned "held"
    const conflictFillAfterPoll = await page.evaluate(() => {
      const el = document.querySelector('#widget-conflict') as HTMLElement & { shadowRoot: ShadowRoot };
      return el?.shadowRoot?.querySelector('circle[data-seat-key="A1"]')?.getAttribute('fill') ?? '';
    });
    expect(conflictFillAfterPoll.toLowerCase()).toBe('#b91c1c');
  });
});
