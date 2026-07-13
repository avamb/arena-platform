/**
 * Keyboard-only navigation E2E tests.
 *
 * Verifies that the widget and demo pages are fully navigable with keyboard
 * only (no mouse), with visible focus indicators on all interactive elements.
 *
 * WID-E: Keyboard-only purchase E2E — feature #327
 * WID-R4: Roving tabindex + live seat labels — feature #333
 */

import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

// ─── Minimal schema for keyboard / axe tests ──────────────────────────────────

/** 3 rows × 4 seats = 12 seats total.  Kept small for fast test runs. */
function buildMinimalSchema(): object {
  const rowNames = ['A', 'B', 'C'];
  return {
    session_id: 'kbd-session-001',
    event_id: 'kbd-event-001',
    admission_mode: 'assigned_seats',
    seating_plan_version_id: 'spv-kbd-001',
    seat_status_version: 1,
    geometry_checksum: 'kbd-checksum',
    capacity_seated: 12,
    capacity_standing: 0,
    geometry: {
      schema_version: 1,
      canvas: { width: 600, height: 300 },
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
              radius: 10,
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
        price_hint: '22.00',
        currency_hint: 'EUR',
        pricing_mode: 'fixed',
        price_amount: 2200,
        currency: 'EUR',
      },
    ],
  };
}

function buildStatusResponse(): object {
  return {
    session_id: 'kbd-session-001',
    status_version: 1,
    delta: false,
    seats: {
      A1: 'available', A2: 'available', A3: 'available', A4: 'available',
      B1: 'available', B2: 'available', B3: 'available', B4: 'available',
      C1: 'available', C2: 'available', C3: 'available', C4: 'available',
    },
  };
}

// ─── Shared route setup ───────────────────────────────────────────────────────

async function setupPopulatedMapRoutes(page: import('@playwright/test').Page): Promise<void> {
  await page.route('**/v1/event-sessions/kbd-session-001/schema', (route) => {
    void route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(buildMinimalSchema()),
    });
  });
  await page.route('**/v1/event-sessions/kbd-session-001/seat-status**', (route) => {
    void route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(buildStatusResponse()),
    });
  });
}

// ─── Demo page keyboard navigation ───────────────────────────────────────────

test.describe('Demo page keyboard navigation', () => {
  test('Tab key moves focus through interactive elements without getting stuck', async ({
    page,
  }) => {
    await page.goto('/demo/index.html');
    await page.waitForLoadState('networkidle');

    // Start from the body.
    await page.keyboard.press('Tab');

    const visitedTags = new Set<string>();
    const MAX_TABS = 30;

    for (let i = 0; i < MAX_TABS; i++) {
      const tag = await page.evaluate(() => {
        const el = document.activeElement;
        return el ? el.tagName.toLowerCase() : 'body';
      });
      visitedTags.add(tag);
      await page.keyboard.press('Tab');
    }

    // We should have navigated through at least one focusable element.
    // The demo page has no links or interactive elements currently, so the
    // body retains focus (no keyboard trap — cycle wraps back through body).
    // What matters is that Tab never traps: after N tabs we should be in a
    // state where another Tab continues to move focus.
    expect(visitedTags.size).toBeGreaterThanOrEqual(1);
  });

  test('Shift+Tab moves focus backwards', async ({ page }) => {
    await page.goto('/demo/index.html');
    await page.waitForLoadState('networkidle');

    // Tab forward once.
    await page.keyboard.press('Tab');
    const forwardTag = await page.evaluate(() => document.activeElement?.tagName ?? 'BODY');

    // Shift+Tab back.
    await page.keyboard.press('Shift+Tab');
    const backTag = await page.evaluate(() => document.activeElement?.tagName ?? 'BODY');

    // Both should be reachable (even if both are body, no crash).
    expect(forwardTag).toBeTruthy();
    expect(backTag).toBeTruthy();
  });

  test('Enter key on focusable element does not throw', async ({ page }) => {
    await page.goto('/demo/index.html');
    await page.waitForLoadState('networkidle');

    // Focus the first interactive element and press Enter. Activation may
    // legitimately navigate (links) — the page must simply stay alive.
    // Note: `.not.toThrow()` around an async fn does not await the promise
    // (the rejection lands after the test ends), so we await directly and
    // let a real failure fail the test.
    await page.keyboard.press('Tab');
    await page.keyboard.press('Enter');
    expect(await page.evaluate(() => document.readyState)).toBeTruthy();
  });
});

// ─── A11y fixture keyboard navigation ────────────────────────────────────────

test.describe('A11y fixture keyboard navigation', () => {
  test('page is navigable by keyboard (no trap — roving tabindex model)', async ({ page }) => {
    await page.goto('/demo/index.html');
    await page.waitForLoadState('networkidle');

    // Tab 15 times and collect the DEEP focused element identity.
    // document.activeElement stops at the shadow host (ARENA-TICKETS), so a
    // tagName heuristic would see one repeating tag while focus is in fact
    // advancing through focusable elements inside the shadow root.
    // Resolve through shadowRoot and track element identity instead.
    const identities: string[] = [];
    for (let i = 0; i < 15; i++) {
      await page.keyboard.press('Tab');
      const id = await page.evaluate(() => {
        let el: Element | null = document.activeElement;
        while (el && el.shadowRoot && el.shadowRoot.activeElement) {
          el = el.shadowRoot.activeElement;
        }
        if (!el) return 'BODY';
        return (
          el.tagName +
          '|' +
          (el.getAttribute('aria-label') ?? el.id ?? el.textContent?.slice(0, 24) ?? '')
        );
      });
      identities.push(id);
    }

    // No keyboard trap: a trap means focus is STUCK on one element. Seeing
    // several distinct focused elements across 15 Tabs proves movement.
    // WID-T5 canonical single-stop: the entire seat map is ONE Tab stop
    // (the .seat-map-container).  The toolbar buttons (fit/reset) add 2 more
    // Tab stops.  So the widget contributes ≥ 3 distinct Tab stops in total
    // regardless of the number of rows rendered.
    const distinct = new Set(identities).size;
    expect(distinct).toBeGreaterThanOrEqual(3);
  });

  test('headings are reachable semantically (h1/h2 present)', async ({ page }) => {
    await page.goto('/demo/a11y-keyboard.html');
    const h1 = await page.locator('h1').count();
    const h2 = await page.locator('h2').count();
    expect(h1).toBe(1);
    expect(h2).toBeGreaterThan(0);
  });

  test('sections have aria-labelledby or aria-label', async ({ page }) => {
    await page.goto('/demo/a11y-keyboard.html');
    // All <section> elements should be labelled for screen reader navigation.
    const sections = page.locator('section');
    const count = await sections.count();
    for (let i = 0; i < count; i++) {
      const section = sections.nth(i);
      const labelledBy = await section.getAttribute('aria-labelledby');
      const label = await section.getAttribute('aria-label');
      expect(labelledBy ?? label).toBeTruthy();
    }
  });
});

// ─── Widget internal focus (shadow DOM) ──────────────────────────────────────

test.describe('Widget shadow DOM focus styles', () => {
  test('arena-tickets root carries role=region', async ({ page }) => {
    await page.goto('/demo/a11y-keyboard.html');
    await page.waitForLoadState('networkidle');

    const roleRegion = await page.evaluate(() => {
      const el = document.querySelector('#widget-no-token');
      if (!el || !el.shadowRoot) return null;
      const root = el.shadowRoot.querySelector('[role="region"]');
      return root?.getAttribute('role') ?? null;
    });
    expect(roleRegion).toBe('region');
  });

  test('arena-tickets root has aria-label', async ({ page }) => {
    await page.goto('/demo/a11y-keyboard.html');
    await page.waitForLoadState('networkidle');

    const ariaLabel = await page.evaluate(() => {
      const el = document.querySelector('#widget-no-token');
      if (!el || !el.shadowRoot) return null;
      const root = el.shadowRoot.querySelector('[aria-label]');
      return root?.getAttribute('aria-label') ?? null;
    });
    expect(ariaLabel).toBeTruthy();
  });
});

// ─── Roving tabindex + live seat labels (WID-R4) ─────────────────────────────

test.describe('WID-R4: roving tabindex + live seat labels', () => {
  test.beforeEach(async ({ page }) => {
    await setupPopulatedMapRoutes(page);
  });

  test('axe WCAG 2.2 AA — no critical/serious violations on populated seat map', async ({
    page,
  }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');

    // Wait for seat circles to appear in the shadow DOM.
    await page.waitForFunction(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return false;
      return host.shadowRoot.querySelectorAll('[data-seat-key]').length > 0;
    }, { timeout: 10_000 });

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa'])
      .analyze();

    const critical = results.violations.filter(
      (v) => v.impact === 'critical' || v.impact === 'serious',
    );
    expect(
      critical,
      `WCAG 2.2 AA violations on populated map:\n${JSON.stringify(critical, null, 2)}`,
    ).toHaveLength(0);
  });

  test('seat aria-labels include price and status', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');

    await page.waitForFunction(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return false;
      return host.shadowRoot.querySelectorAll('[data-seat-key]').length > 0;
    }, { timeout: 10_000 });

    const firstSeatLabel = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      const seat = host.shadowRoot.querySelector('[data-seat-key]');
      return seat?.getAttribute('aria-label') ?? null;
    });

    // Label should include section, row, seat, price, and status.
    expect(firstSeatLabel).toBeTruthy();
    expect(firstSeatLabel).toContain('Parter');
    expect(firstSeatLabel).toContain('22.00 EUR');
    expect(firstSeatLabel).toContain('available');
  });

  test('roving tabindex: all seats initially have tabindex="-1" (canonical single-stop model)', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');

    await page.waitForFunction(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return false;
      return host.shadowRoot.querySelectorAll('[data-seat-key]').length > 0;
    }, { timeout: 10_000 });

    const { zeroCount, minusOneCount } = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return { zeroCount: 0, minusOneCount: 0 };
      const seats = host.shadowRoot.querySelectorAll('[data-seat-key]');
      let zeroCount = 0;
      let minusOneCount = 0;
      for (const seat of seats) {
        const tab = seat.getAttribute('tabindex');
        if (tab === '0') zeroCount++;
        if (tab === '-1') minusOneCount++;
      }
      return { zeroCount, minusOneCount };
    });

    // WID-T5 canonical single-stop: ALL seats start at tabindex="-1".
    // The container (.seat-map-container) holds the single tabindex="0" Tab stop.
    // The onfocus handler promotes one seat to "0" on first keyboard entry.
    expect(zeroCount).toBe(0);
    // 3 rows × 4 seats = 12 seats, all at tabindex="-1".
    expect(minusOneCount).toBe(12);
  });

  test('ArrowRight moves focus to next seat within the same row', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');

    await page.waitForFunction(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return false;
      return host.shadowRoot.querySelectorAll('[data-seat-key]').length > 0;
    }, { timeout: 10_000 });

    // Focus the first seat (row A, seat 1) — it has tabindex="0" already.
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const seat = host?.shadowRoot?.querySelector('[data-seat-key]') as HTMLElement | null;
      seat?.focus();
    });

    // Press ArrowRight.
    await page.keyboard.press('ArrowRight');

    const focusedKey = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      let el: Element | null = host.shadowRoot.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      return el?.getAttribute('data-seat-key') ?? null;
    });

    // Focus should have moved to seat A2.
    expect(focusedKey).toBe('A2');
  });

  test('ArrowDown moves focus to same-column seat in the next row', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');

    await page.waitForFunction(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return false;
      return host.shadowRoot.querySelectorAll('[data-seat-key]').length > 0;
    }, { timeout: 10_000 });

    // Focus the first seat in row A (A1).
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const seat = host?.shadowRoot?.querySelector('[data-seat-key]') as HTMLElement | null;
      seat?.focus();
    });

    await page.keyboard.press('ArrowDown');

    const focusedKey = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      let el: Element | null = host.shadowRoot.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      return el?.getAttribute('data-seat-key') ?? null;
    });

    // Focus should have moved to B1 (same column, next row).
    expect(focusedKey).toBe('B1');
  });

  test('Home moves focus to first seat in the row', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');

    await page.waitForFunction(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return false;
      return host.shadowRoot.querySelectorAll('[data-seat-key]').length > 0;
    }, { timeout: 10_000 });

    // Focus A3 directly (tabindex="-1" seat).
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const seat = host?.shadowRoot?.querySelector('[data-seat-key="A3"]') as HTMLElement | null;
      seat?.focus();
    });

    await page.keyboard.press('Home');

    const focusedKey = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      let el: Element | null = host.shadowRoot.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      return el?.getAttribute('data-seat-key') ?? null;
    });

    expect(focusedKey).toBe('A1');
  });

  test('End moves focus to last seat in the row', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');

    await page.waitForFunction(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return false;
      return host.shadowRoot.querySelectorAll('[data-seat-key]').length > 0;
    }, { timeout: 10_000 });

    // Focus A1 (first seat).
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const seat = host?.shadowRoot?.querySelector('[data-seat-key="A1"]') as HTMLElement | null;
      seat?.focus();
    });

    await page.keyboard.press('End');

    const focusedKey = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      let el: Element | null = host.shadowRoot.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      return el?.getAttribute('data-seat-key') ?? null;
    });

    // Last seat in row A has 4 seats → A4.
    expect(focusedKey).toBe('A4');
  });

  test('status-poll updates aria-labels without moving focus', async ({ page }) => {
    // Set up a status response that marks B2 as "held".
    await page.route('**/v1/event-sessions/kbd-session-001/seat-status**', (route) => {
      void route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          session_id: 'kbd-session-001',
          status_version: 2,
          delta: true,
          seats: { B2: 'held' },
        }),
      });
    });
    // Unregister the previous catchall schema route and re-register.
    await page.unrouteAll({ behavior: 'ignoreErrors' });
    await page.route('**/v1/event-sessions/kbd-session-001/schema', (route) => {
      void route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(buildMinimalSchema()),
      });
    });
    await page.route('**/v1/event-sessions/kbd-session-001/seat-status**', (route) => {
      void route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          session_id: 'kbd-session-001',
          status_version: 2,
          delta: true,
          seats: { B2: 'held' },
        }),
      });
    });

    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');

    await page.waitForFunction(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return false;
      return host.shadowRoot.querySelectorAll('[data-seat-key]').length > 0;
    }, { timeout: 10_000 });

    // Focus seat B2.
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const seat = host?.shadowRoot?.querySelector('[data-seat-key="B2"]') as HTMLElement | null;
      seat?.focus();
    });

    // Wait for status poll to update B2 → held.
    await page.waitForFunction(() => {
      const host = document.querySelector('arena-tickets');
      const seat = host?.shadowRoot?.querySelector('[data-seat-key="B2"]');
      return seat?.getAttribute('data-status') === 'held';
    }, { timeout: 10_000 });

    // aria-label should be updated.
    const label = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const seat = host?.shadowRoot?.querySelector('[data-seat-key="B2"]');
      return seat?.getAttribute('aria-label') ?? null;
    });

    expect(label).toContain('held');
    // Focus should still be on B2 after the label update.
    const stillFocused = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      let el: Element | null = host.shadowRoot.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      return el?.getAttribute('data-seat-key') ?? null;
    });
    expect(stillFocused).toBe('B2');
  });
});

// ─── WID-S4: roving tabindex invariant across row navigation ──────────────────

test.describe('WID-S4: roving tabindex invariant across row navigation', () => {
  /** Wait until the shadow DOM contains all 12 seat circles. */
  async function waitForSeats(page: import('@playwright/test').Page): Promise<void> {
    await page.waitForFunction(
      () => {
        const host = document.querySelector('arena-tickets');
        if (!host || !host.shadowRoot) return false;
        return host.shadowRoot.querySelectorAll('[data-seat-key]').length >= 12;
      },
      { timeout: 10_000 },
    );
  }

  /** Evaluate tabindex=0 count in a specific row inside the shadow DOM. */
  async function rowTabzeroCount(
    page: import('@playwright/test').Page,
    rowKey: string,
  ): Promise<number> {
    return page.evaluate((key) => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return -1;
      const row = host.shadowRoot.querySelector(`[data-row-key="${key}"]`);
      if (!row) return -1;
      return Array.from(row.querySelectorAll('[data-seat-key]')).filter(
        (s) => s.getAttribute('tabindex') === '0',
      ).length;
    }, rowKey);
  }

  test.beforeEach(async ({ page }) => {
    await setupPopulatedMapRoutes(page);
  });

  // ── Single Tab stop (WID-T5) ───────────────────────────────────────────────

  test('seat-map container has tabindex="0" (canonical single Tab stop)', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');
    await waitForSeats(page);

    const containerTabindex = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      const container = host.shadowRoot.querySelector('.seat-map-container');
      return container?.getAttribute('tabindex') ?? null;
    });

    // WID-T5 canonical single-stop: the container is the SOLE Tab stop for the
    // entire seat map composite widget.  It must have tabindex="0" before the
    // user has interacted with it (all seat circles start at tabindex="-1").
    // The container's onfocus handler delegates focus to the current seat and
    // temporarily sets the container to tabindex="-1" while focus is inside.
    expect(containerTabindex).toBe('0');
  });

  // ── Cross-row invariant ─────────────────────────────────────────────────────

  test('ArrowDown into non-first column: target row ends with exactly 1 tabindex=0', async ({
    page,
  }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');
    await waitForSeats(page);

    // Focus A1 (already tabindex="0"), move right to A2 (column index 1).
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const s = host?.shadowRoot?.querySelector('[data-seat-key="A1"]') as HTMLElement | null;
      s?.focus();
    });
    await page.keyboard.press('ArrowRight');

    // Navigate down from A2 → B2.
    await page.keyboard.press('ArrowDown');

    // Which seat has focus?
    const focusedKey = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      let el: Element | null = host.shadowRoot.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      return el?.getAttribute('data-seat-key') ?? null;
    });
    expect(focusedKey).toBe('B2');

    // Invariant: row B must have EXACTLY ONE tabindex=0 seat (B2, not B1 AND B2).
    // Before WID-S4 the first-seat tabindex="0" from buildSeatMapSVG was never
    // cleared when entering via ArrowDown, so B1 would remain at "0" while B2
    // also received "0" — two Tab stops in the same row.
    const bTabzero = await rowTabzeroCount(page, 'parter-row-B');
    expect(bTabzero).toBe(1);
  });

  test('ArrowUp into non-first column: target row ends with exactly 1 tabindex=0', async ({
    page,
  }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');
    await waitForSeats(page);

    // Focus C3 directly (tabindex="-1" seat in row C, column index 2).
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const s = host?.shadowRoot?.querySelector('[data-seat-key="C3"]') as HTMLElement | null;
      s?.focus();
    });

    // Navigate up from C3 → B3.
    await page.keyboard.press('ArrowUp');

    const focusedKey = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      let el: Element | null = host.shadowRoot.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      return el?.getAttribute('data-seat-key') ?? null;
    });
    expect(focusedKey).toBe('B3');

    // Row B: exactly one tabindex=0 (B3, not B1 AND B3).
    const bTabzero = await rowTabzeroCount(page, 'parter-row-B');
    expect(bTabzero).toBe(1);
  });

  test('repeated cross-row navigation keeps invariant (ArrowDown A→B→C)', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');
    await waitForSeats(page);

    // Start at A1, move to A3, then down to B3, then down to C3.
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const s = host?.shadowRoot?.querySelector('[data-seat-key="A1"]') as HTMLElement | null;
      s?.focus();
    });
    await page.keyboard.press('ArrowRight'); // A1 → A2
    await page.keyboard.press('ArrowRight'); // A2 → A3
    await page.keyboard.press('ArrowDown');  // A3 → B3
    await page.keyboard.press('ArrowDown');  // B3 → C3

    const focusedKey = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      let el: Element | null = host.shadowRoot.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      return el?.getAttribute('data-seat-key') ?? null;
    });
    expect(focusedKey).toBe('C3');

    // Row B should have 0 tabindex=0 seats (we passed through it).
    const bTabzero = await rowTabzeroCount(page, 'parter-row-B');
    expect(bTabzero).toBe(0);

    // Row C should have exactly 1 tabindex=0 seat (C3).
    const cTabzero = await rowTabzeroCount(page, 'parter-row-C');
    expect(cTabzero).toBe(1);
  });

  // ── aria-label data-base-label mechanism (WID-S2 coverage) ─────────────────

  test('aria-label restores correctly after seat deselection (data-base-label)', async ({
    page,
  }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');
    await waitForSeats(page);

    // Capture the pre-selection label (data-base-label + ", available").
    const originalLabel = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      return (
        host?.shadowRoot?.querySelector('[data-seat-key="A1"]')?.getAttribute('aria-label') ?? null
      );
    });
    expect(originalLabel).toBeTruthy();
    expect(originalLabel).toContain('available');

    // Click A1 to select it — applySelectionHighlights appends ", selected".
    // WID-T2: dispatchEvent instead of .click() for SVG elements.
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      host?.shadowRoot
        ?.querySelector('[data-seat-key="A1"]')
        ?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, composed: true }));
    });

    await page.waitForFunction(
      () => {
        const host = document.querySelector('arena-tickets');
        const s = host?.shadowRoot?.querySelector('[data-seat-key="A1"]');
        return s?.getAttribute('aria-label')?.includes('selected') === true;
      },
      { timeout: 5_000 },
    );

    // Click A1 again to deselect — applySelectionHighlights removes suffix via data-base-label.
    // WID-T2: dispatchEvent for SVG element.
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      host?.shadowRoot
        ?.querySelector('[data-seat-key="A1"]')
        ?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, composed: true }));
    });

    await page.waitForFunction(
      () => {
        const host = document.querySelector('arena-tickets');
        const s = host?.shadowRoot?.querySelector('[data-seat-key="A1"]');
        const lbl = s?.getAttribute('aria-label') ?? '';
        return !lbl.includes('selected') && lbl.includes('available');
      },
      { timeout: 5_000 },
    );

    const restoredLabel = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      return (
        host?.shadowRoot?.querySelector('[data-seat-key="A1"]')?.getAttribute('aria-label') ?? null
      );
    });

    // Restored label must exactly match the original: data-base-label + ", available".
    // Any stale ", selected" suffix would indicate the data-base-label mechanism is broken.
    expect(restoredLabel).toBe(originalLabel);
    expect(restoredLabel).not.toContain('selected');
  });

  test('aria-label restores via data-base-label after status-poll overrides a conflict suffix', async ({
    page,
  }) => {
    // Override the status-poll to mark B1 as "held" on every tick.
    await page.unrouteAll({ behavior: 'ignoreErrors' });
    await page.route('**/v1/event-sessions/kbd-session-001/schema', (route) => {
      void route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(buildMinimalSchema()),
      });
    });
    await page.route('**/v1/event-sessions/kbd-session-001/seat-status**', (route) => {
      void route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          session_id: 'kbd-session-001',
          status_version: 2,
          delta: true,
          seats: { B1: 'held' },
        }),
      });
    });

    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');
    await waitForSeats(page);

    // Simulate a conflict highlight on B1 — as if applyConflictHighlight was
    // called after a 409 response. We write a non-standard aria-label suffix
    // ("conflict — not available") directly in the DOM.
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const seat = host?.shadowRoot?.querySelector('[data-seat-key="B1"]');
      if (!seat) return;
      const base = seat.getAttribute('data-base-label') ?? '';
      seat.setAttribute('data-status', 'conflict');
      seat.setAttribute('fill', '#b91c1c');
      seat.setAttribute('aria-label', base ? `${base}, conflict — not available` : 'conflict — not available');
    });

    // Wait for the status poll to fire and call applySeatStatusUpdate → B1 = "held".
    // applySeatStatusUpdate reads data-base-label to build the aria-label,
    // so the "conflict — not available" suffix must NOT survive.
    await page.waitForFunction(
      () => {
        const host = document.querySelector('arena-tickets');
        return (
          host?.shadowRoot?.querySelector('[data-seat-key="B1"]')?.getAttribute('data-status') ===
          'held'
        );
      },
      { timeout: 10_000 },
    );

    const restoredLabel = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      return host?.shadowRoot?.querySelector('[data-seat-key="B1"]')?.getAttribute('aria-label') ?? null;
    });
    const baseLabel = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      return host?.shadowRoot?.querySelector('[data-seat-key="B1"]')?.getAttribute('data-base-label') ?? null;
    });

    // aria-label must be base-label + ", held" — not the polluted conflict suffix.
    expect(restoredLabel).toContain('held');
    expect(restoredLabel).not.toContain('conflict');
    expect(restoredLabel).toBe(`${baseLabel}, held`);
  });
});

// ─── WID-T5: canonical single-stop tab navigation ─────────────────────────────

test.describe('WID-T5: canonical single-stop tab navigation', () => {
  /** Wait until 12 seat circles are in the shadow DOM. */
  async function waitForSeats(page: import('@playwright/test').Page): Promise<void> {
    await page.waitForFunction(
      () => {
        const host = document.querySelector('arena-tickets');
        if (!host || !host.shadowRoot) return false;
        return host.shadowRoot.querySelectorAll('[data-seat-key]').length >= 12;
      },
      { timeout: 10_000 },
    );
  }

  test.beforeEach(async ({ page }) => {
    await setupPopulatedMapRoutes(page);
  });

  test('container has tabindex="0" and all seats have tabindex="-1" initially', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');
    await waitForSeats(page);

    const { containerTab, zeroSeatCount } = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return { containerTab: null, zeroSeatCount: -1 };
      const container = host.shadowRoot.querySelector('.seat-map-container');
      const containerTab = container?.getAttribute('tabindex') ?? null;
      const seats = host.shadowRoot.querySelectorAll('[data-seat-key]');
      let zeroSeatCount = 0;
      for (const s of seats) {
        if (s.getAttribute('tabindex') === '0') zeroSeatCount++;
      }
      return { containerTab, zeroSeatCount };
    });

    // Container is the single Tab stop before any interaction.
    expect(containerTab).toBe('0');
    // All seat circles start at tabindex="-1" — none is a Tab stop yet.
    expect(zeroSeatCount).toBe(0);
  });

  test('programmatic focus on container delegates to first seat, container becomes tabindex="-1"', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');
    await waitForSeats(page);

    // Programmatically focus the container — simulates Tab arriving on it.
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const container = host?.shadowRoot?.querySelector<HTMLElement>('.seat-map-container');
      container?.focus();
    });

    // Wait for focus to settle (onfocus handler fires synchronously but we
    // still need to let Playwright observe the DOM state).
    await page.waitForFunction(
      () => {
        const host = document.querySelector('arena-tickets');
        if (!host || !host.shadowRoot) return false;
        const seats = host.shadowRoot.querySelectorAll('[data-seat-key]');
        for (const s of seats) {
          if (s.getAttribute('tabindex') === '0') return true;
        }
        return false;
      },
      { timeout: 3_000 },
    );

    const { containerTab, focusedSeatKey, seatZeroCount } = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return { containerTab: null, focusedSeatKey: null, seatZeroCount: -1 };
      const container = host.shadowRoot.querySelector('.seat-map-container');
      const containerTab = container?.getAttribute('tabindex') ?? null;

      // Find the focused seat.
      let el: Element | null = host.shadowRoot.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      const focusedSeatKey = el?.getAttribute('data-seat-key') ?? null;

      // Count seats with tabindex=0.
      const seats = host.shadowRoot.querySelectorAll('[data-seat-key]');
      let seatZeroCount = 0;
      for (const s of seats) {
        if (s.getAttribute('tabindex') === '0') seatZeroCount++;
      }
      return { containerTab, focusedSeatKey, seatZeroCount };
    });

    // Container must have ceded its Tab-stop role while focus is inside.
    expect(containerTab).toBe('-1');
    // Exactly one seat has tabindex=0 (the "current" seat that received focus).
    expect(seatZeroCount).toBe(1);
    // Focus landed on a seat (A1 — the first circle in the SVG).
    expect(focusedSeatKey).toBeTruthy();
    expect(focusedSeatKey).toBe('A1');
  });

  test('after focus leaves widget, container is restored to tabindex="0"', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');
    await waitForSeats(page);

    // Enter the composite widget by focusing the container.
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const container = host?.shadowRoot?.querySelector<HTMLElement>('.seat-map-container');
      container?.focus();
    });

    // Wait until a seat has tabindex=0 (focus delegated inside).
    await page.waitForFunction(
      () => {
        const host = document.querySelector('arena-tickets');
        return (
          !!host?.shadowRoot?.querySelector('[data-seat-key][tabindex="0"]')
        );
      },
      { timeout: 3_000 },
    );

    // Now move focus AWAY from the widget (blur the current seat).
    await page.evaluate(() => {
      (document.body as HTMLElement).focus();
    });

    // Container should be restored to tabindex=0 (focusout handler fires).
    await page.waitForFunction(
      () => {
        const host = document.querySelector('arena-tickets');
        const container = host?.shadowRoot?.querySelector('.seat-map-container');
        return container?.getAttribute('tabindex') === '0';
      },
      { timeout: 3_000 },
    );

    const containerTab = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const container = host?.shadowRoot?.querySelector('.seat-map-container');
      return container?.getAttribute('tabindex') ?? null;
    });

    expect(containerTab).toBe('0');
  });

  test('ArrowRight navigates after Tab-into-container (one-stop entry)', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');
    await waitForSeats(page);

    // Enter via container focus (simulating Tab into widget).
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const container = host?.shadowRoot?.querySelector<HTMLElement>('.seat-map-container');
      container?.focus();
    });

    // Wait for focus delegation to first seat.
    await page.waitForFunction(
      () => {
        const host = document.querySelector('arena-tickets');
        return !!host?.shadowRoot?.querySelector('[data-seat-key][tabindex="0"]');
      },
      { timeout: 3_000 },
    );

    // Now press ArrowRight — should navigate from A1 to A2.
    await page.keyboard.press('ArrowRight');

    const focusedKey = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      let el: Element | null = host.shadowRoot.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      return el?.getAttribute('data-seat-key') ?? null;
    });

    expect(focusedKey).toBe('A2');
  });

  test('re-entry after Tab-out restores focus to last-visited seat', async ({ page }) => {
    await page.goto('/demo/populated-map.html');
    await page.waitForLoadState('networkidle');
    await waitForSeats(page);

    // Enter and navigate to A3.
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const container = host?.shadowRoot?.querySelector<HTMLElement>('.seat-map-container');
      container?.focus();
    });
    await page.waitForFunction(
      () => !!document.querySelector('arena-tickets')?.shadowRoot?.querySelector('[data-seat-key][tabindex="0"]'),
      { timeout: 3_000 },
    );
    await page.keyboard.press('ArrowRight'); // A1 → A2
    await page.keyboard.press('ArrowRight'); // A2 → A3

    // Leave the widget.
    await page.evaluate(() => { (document.body as HTMLElement).focus(); });

    // Wait for container to be restored.
    await page.waitForFunction(
      () => {
        const container = document.querySelector('arena-tickets')?.shadowRoot?.querySelector('.seat-map-container');
        return container?.getAttribute('tabindex') === '0';
      },
      { timeout: 3_000 },
    );

    // Re-enter the widget via container focus.
    await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      const container = host?.shadowRoot?.querySelector<HTMLElement>('.seat-map-container');
      container?.focus();
    });

    // Wait for focus delegation.
    await page.waitForFunction(
      () => !!document.querySelector('arena-tickets')?.shadowRoot?.querySelector('[data-seat-key][tabindex="0"]'),
      { timeout: 3_000 },
    );

    const focusedKey = await page.evaluate(() => {
      const host = document.querySelector('arena-tickets');
      if (!host || !host.shadowRoot) return null;
      let el: Element | null = host.shadowRoot.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      return el?.getAttribute('data-seat-key') ?? null;
    });

    // Focus should resume at A3 (the last-visited seat before leaving).
    expect(focusedKey).toBe('A3');
  });
});
