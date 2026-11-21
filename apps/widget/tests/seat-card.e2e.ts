/**
 * Seat card E2E (MOCK suite: every /v1/* call is answered by page.route()).
 *
 * One card tells the buyer about a seat: sector, row, seat, category name,
 * price and, where it matters, the state. A mouse hover and keyboard focus
 * show it; a tap selects the seat at once AND shows it; a tap on a seat that
 * cannot be chosen shows its state and selects nothing.
 */

import { test, expect, type Page } from '@playwright/test';

const SESSION = 'kbd-session-001';

function schema(): object {
  const rows = ['A', 'B'];
  return {
    session_id: SESSION,
    event_id: 'kbd-event-001',
    admission_mode: 'assigned_seats',
    seating_plan_version_id: 'spv-card-001',
    seat_status_version: 1,
    geometry_checksum: 'card-checksum',
    capacity_seated: 8,
    capacity_standing: 0,
    geometry: {
      schema_version: 1,
      canvas: { width: 600, height: 300 },
      categories: [
        { index: 0, name: 'Gold', color: '#D97706', price_hint: '30.00', currency_hint: 'EUR' },
        { index: 1, name: 'Silver', color: '#4F46E5', price_hint: '22.00', currency_hint: 'EUR' },
      ],
      sections: [
        {
          key: 'parter',
          name: 'Parter',
          rows: rows.map((name, r) => ({
            key: `parter-row-${name}`,
            name,
            seats: [1, 2, 3, 4].map((n) => ({
              key: `${name}${n}`,
              number: String(n),
              x: 120 + n * 80,
              y: 90 + r * 80,
              radius: 14,
              category_index: r,
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
      { index: 0, name: 'Gold', color: '#D97706', price_hint: '30.00', currency_hint: 'EUR', pricing_mode: 'fixed', price_amount: 3000, currency: 'EUR' },
      { index: 1, name: 'Silver', color: '#4F46E5', price_hint: '22.00', currency_hint: 'EUR', pricing_mode: 'fixed', price_amount: 2200, currency: 'EUR' },
    ],
  };
}

function statuses(): object {
  return {
    session_id: SESSION,
    status_version: 1,
    delta: false,
    seats: {
      A1: 'available', A2: 'sold', A3: 'unavailable', A4: 'held',
      B1: 'available', B2: 'available', B3: 'available', B4: 'available',
    },
  };
}

async function open(page: Page): Promise<void> {
  await page.route(`**/v1/event-sessions/${SESSION}/schema`, (route) => {
    void route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(schema()) });
  });
  await page.route(`**/v1/event-sessions/${SESSION}/seat-status**`, (route) => {
    void route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(statuses()) });
  });
  await page.goto('/demo/populated-map.html');
  await page.waitForLoadState('networkidle');
  // Wait for the first status poll: A2 turns sold.
  await expect(page.locator('circle[data-seat-key="A2"][data-status="sold"]')).toHaveCount(1, { timeout: 10_000 });
}

const card = (page: Page) => page.locator('.arena-seat-card');
const selected = (page: Page) => page.locator('circle[data-selected="true"]');

async function shot(page: Page, name: string): Promise<void> {
  const dir = process.env['SEAT_CARD_SHOT_DIR'];
  if (dir) await page.locator('.seat-map-container').screenshot({ path: `${dir}/${name}` });
}

test.describe('Seat card (mouse)', () => {
  test('hover shows sector, row, seat, category and price, and moving away hides it', async ({ page }) => {
    await open(page);
    await page.locator('circle[data-seat-key="B2"]').hover();
    await expect(card(page)).toBeVisible();
    await expect(card(page)).toHaveAttribute('role', 'tooltip');
    await expect(card(page)).toContainText('Sector Parter · Row B · Seat 2');
    await expect(card(page)).toContainText('Silver');
    await expect(card(page)).toContainText('22.00 EUR');
    await shot(page, 'card-widget.png');
    // Hover only informs.
    await expect(selected(page)).toHaveCount(0);
    await page.mouse.move(2, 2);
    await expect(card(page)).toBeHidden();
  });

  test('a click selects the seat and the card stays with the seat marked selected', async ({ page }) => {
    await open(page);
    await page.locator('circle[data-seat-key="B1"]').click();
    await expect(selected(page)).toHaveCount(1);
    await expect(card(page)).toBeVisible();
    await expect(card(page)).toContainText('Row B · Seat 1');
    await expect(card(page)).toContainText('Selected');
  });

  test('a seat that is sold shows its state and is not selected', async ({ page }) => {
    await open(page);
    await page.locator('circle[data-seat-key="A2"]').click();
    await expect(card(page)).toContainText('Sold');
    await expect(card(page)).toContainText('Gold');
    await expect(selected(page)).toHaveCount(0);
  });

  test('Escape hides the card', async ({ page }) => {
    await open(page);
    await page.locator('circle[data-seat-key="B3"]').hover();
    await expect(card(page)).toBeVisible();
    await page.locator('.seat-map-container').focus();
    await page.keyboard.press('Escape');
    await expect(card(page)).toBeHidden();
  });
});

test.describe('Seat card (keyboard)', () => {
  test('keyboard focus on a seat shows the card', async ({ page }) => {
    await open(page);
    await page.locator('circle[data-seat-key="B2"]').focus();
    await expect(card(page)).toBeVisible();
    await expect(card(page)).toContainText('Row B · Seat 2');
    await page.keyboard.press('ArrowRight');
    await expect(card(page)).toContainText('Seat 3');
    await page.keyboard.press('Escape');
    await expect(card(page)).toBeHidden();
  });
});

test.describe('Seat card (touch)', () => {
  test.use({ hasTouch: true });

  test('a tap selects the seat at once and shows the card; a second seat needs one more tap only', async ({ page }) => {
    await open(page);
    await page.locator('circle[data-seat-key="B1"]').tap();
    await expect(selected(page)).toHaveCount(1);
    await expect(card(page)).toContainText('Row B · Seat 1');
    await page.locator('circle[data-seat-key="B2"]').tap();
    await expect(selected(page)).toHaveCount(2);
    await expect(card(page)).toContainText('Row B · Seat 2');
    await expect(card(page)).toContainText('Selected');
  });

  test('a tap on a sold seat shows Sold and selects nothing; a tap on empty space hides the card', async ({ page }) => {
    await open(page);
    await page.locator('circle[data-seat-key="A2"]').tap();
    await expect(card(page)).toContainText('Sold');
    await expect(selected(page)).toHaveCount(0);
    await page.locator('circle[data-seat-key="A3"]').tap();
    await expect(card(page)).toContainText('Not on sale');
    await expect(selected(page)).toHaveCount(0);
    const box = await page.locator('.seat-map-container').boundingBox();
    // A corner of the map holds no seat.
    await page.touchscreen.tap(box!.x + 4, box!.y + box!.height - 4);
    await expect(card(page)).toBeHidden();
  });
});
