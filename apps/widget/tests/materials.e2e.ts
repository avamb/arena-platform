/**
 * Event materials (task 2-2) — mock e2e.
 *
 * The widget shows the event's gallery, videos, map point, "where else to
 * buy" links and photo report under the ticket picker, section by section,
 * only for data that exists. Nothing third-party loads before a click: the
 * video is a button until pressed and only then an iframe to the embed host
 * appears. Outbound links open in a new tab with the right rel. The block
 * mirrors for Hebrew and goes away with materials="hidden".
 *
 * Prerequisite: `npm run build` must be run before `npm run test:e2e`.
 */

import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

const FEED_TOKEN = 'materials-feed-token';
const EVENT_ID = 'event-materials-001';
const SESSION_ID = 'materials-session-001';

// A 1x1 transparent PNG, served for every picture address.
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==',
  'base64',
);

function eventSummary(): Record<string, unknown> {
  const start = new Date(Date.now() + 7 * 24 * 3600 * 1000).toISOString();
  const end = new Date(Date.now() + 7 * 24 * 3600 * 1000 + 2 * 3600 * 1000).toISOString();
  return {
    id: EVENT_ID,
    org_id: 'org-materials',
    venue_id: 'venue-materials',
    name: 'Materials Night',
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

const photo = (n: number, caption: string | null) => ({
  media_id: `m${n}`,
  url: `https://media.example/photo-${n}.png`,
  caption,
  width: 800,
  height: 600,
});

const FULL_MATERIALS = {
  gallery: [photo(1, 'The hall'), photo(2, 'Backstage'), photo(3, null)],
  videos: [
    {
      provider: 'youtube',
      video_id: 'abc123',
      embed_url: 'https://www.youtube-nocookie.com/embed/abc123',
      url: 'https://www.youtube.com/watch?v=abc123',
    },
  ],
  partner_links: [{ url: 'https://partner.example/tickets/42', seller_name: 'Partner Tickets Ltd' }],
  map_point: { latitude: 50.0755, longitude: 14.4378, label: 'Main entrance' },
  report_photos: [photo(7, 'Last year')],
};

function eventDetail(materials: unknown): object {
  const summary = eventSummary();
  const event: Record<string, unknown> = {
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
        service_fee_percent: '0.00',
        promo_codes_available: false,
        tiers: [
          {
            id: 'tier-materials-standard',
            name: 'Standard',
            pricing_mode: 'fixed',
            price_amount: 2500,
            currency: 'EUR',
            capacity: 50,
            available: 50,
            is_open: true,
            sort_order: 0,
          },
        ],
        media_gallery: [],
      },
    ],
  };
  if (materials !== undefined) event['materials'] = materials;
  return { event };
}

/** Mocks the platform and records which third-party hosts were contacted. */
async function mockPlatform(page: Page, materials: unknown): Promise<{ embedRequests: string[] }> {
  const embedRequests: string[] = [];
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/events`, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ events: [eventSummary()] }) }),
  );
  await page.route(`**/v1/public/feeds/${FEED_TOKEN}/events/${EVENT_ID}`, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(eventDetail(materials)) }),
  );
  await page.route('https://media.example/**', (route) =>
    route.fulfill({ status: 200, contentType: 'image/png', body: PNG }),
  );
  await page.route('https://www.youtube-nocookie.com/**', (route) => {
    embedRequests.push(route.request().url());
    return route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><title>video</title><p>video</p>' });
  });
  return { embedRequests };
}

async function openWidget(page: Page): Promise<void> {
  await page.goto('/demo/materials.html');
  await page.locator('#widget-materials').getByRole('button', { name: 'Increase quantity for Standard' }).waitFor({ timeout: 15_000 });
}

test.describe('event materials in the widget', () => {
  test('every section shows, and nothing third-party loads before a click', async ({ page }) => {
    const { embedRequests } = await mockPlatform(page, FULL_MATERIALS);
    await openWidget(page);
    const block = page.locator('#widget-materials [data-arena-materials]');
    await expect(block).toBeVisible();
    for (const section of ['gallery', 'videos', 'map', 'partners', 'report']) {
      await expect(block.locator(`[data-materials-section="${section}"]`)).toBeVisible();
    }
    await expect(block.getByRole('heading', { name: 'Photos' })).toBeVisible();
    await expect(block.getByRole('heading', { name: 'How to get there' })).toBeVisible();
    await expect(block.getByRole('heading', { name: 'Where else to buy' })).toBeVisible();
    await expect(block.getByRole('heading', { name: 'Photo report' })).toBeVisible();

    // The video is a button: no iframe and no request to the embed host yet.
    await expect(block.locator('iframe')).toHaveCount(0);
    await expect(block.getByRole('button', { name: /Play video/ })).toBeVisible();
    expect(embedRequests).toEqual([]);

    // The map: coordinates as text and two outbound links, no iframe or script.
    await expect(block.locator('[data-materials-section="map"]')).toContainText('50.07550, 14.43780');
    await expect(block.locator('[data-materials-section="map"]')).toContainText('Main entrance');
    const osm = block.getByRole('link', { name: /OpenStreetMap/ });
    await expect(osm).toHaveAttribute('href', /^https:\/\/www\.openstreetmap\.org\//);
    await expect(osm).toHaveAttribute('target', '_blank');
    await expect(osm).toHaveAttribute('rel', 'noopener noreferrer');
    await expect(block.getByRole('link', { name: /Google Maps/ })).toHaveAttribute('href', /^https:\/\/www\.google\.com\/maps\//);

    // Partner link: seller shown, rel keeps the buyer's page safe.
    await expect(block.locator('[data-materials-section="partners"]')).toContainText('Seller: Partner Tickets Ltd');
    const partner = block.locator('[data-materials-section="partners"] a');
    await expect(partner).toHaveAttribute('href', 'https://partner.example/tickets/42');
    await expect(partner).toHaveAttribute('target', '_blank');
    await expect(partner).toHaveAttribute('rel', 'noopener nofollow');
  });

  test('the video iframe appears only after the press', async ({ page }) => {
    const { embedRequests } = await mockPlatform(page, FULL_MATERIALS);
    await openWidget(page);
    const block = page.locator('#widget-materials [data-arena-materials]');
    await block.getByRole('button', { name: /Play video/ }).click();
    const frame = block.locator('iframe');
    await expect(frame).toHaveCount(1);
    await expect(frame).toHaveAttribute('src', /^https:\/\/www\.youtube-nocookie\.com\/embed\/abc123/);
    await expect(frame).toHaveAttribute('src', /autoplay=1/);
    await expect.poll(() => embedRequests.length).toBeGreaterThan(0);
    // The plain link stays beside the player.
    await expect(block.getByRole('link', { name: /Open on YouTube/ })).toHaveAttribute('rel', 'noopener noreferrer');
  });

  test('the lightbox opens, steps with the arrow keys and closes with Escape', async ({ page }) => {
    await mockPlatform(page, FULL_MATERIALS);
    await openWidget(page);
    const widget = page.locator('#widget-materials');
    const first = widget.locator('[data-materials-section="gallery"] button').first();
    await first.click();
    const dialog = widget.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('img')).toHaveAttribute('src', 'https://media.example/photo-1.png');
    await expect(dialog).toContainText('The hall');
    await page.keyboard.press('ArrowRight');
    await expect(dialog.locator('img')).toHaveAttribute('src', 'https://media.example/photo-2.png');
    await page.keyboard.press('ArrowLeft');
    await page.keyboard.press('ArrowLeft');
    await expect(dialog.locator('img')).toHaveAttribute('src', 'https://media.example/photo-3.png');
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);
    await expect(first).toBeFocused();
  });

  test('an event without materials, or on an older backend, shows no block', async ({ page }) => {
    await mockPlatform(page, { gallery: [], videos: [], partner_links: [], map_point: null, report_photos: [] });
    await openWidget(page);
    await expect(page.locator('#widget-materials [data-arena-materials]')).toHaveCount(0);

    const older = await page.context().newPage();
    await mockPlatform(older, undefined);
    await openWidget(older);
    await expect(older.locator('#widget-materials [data-arena-materials]')).toHaveCount(0);
  });

  test('only the sections with data appear', async ({ page }) => {
    await mockPlatform(page, {
      gallery: [],
      videos: [],
      partner_links: [{ url: 'https://partner.example/x', seller_name: '' }],
      map_point: null,
      report_photos: [],
    });
    await openWidget(page);
    const block = page.locator('#widget-materials [data-arena-materials]');
    await expect(block.locator('[data-materials-section]')).toHaveCount(1);
    await expect(block.locator('[data-materials-section="partners"]')).toContainText('Seller: partner.example');
  });

  test('materials="hidden" removes the block', async ({ page }) => {
    await mockPlatform(page, FULL_MATERIALS);
    await openWidget(page);
    const widget = page.locator('#widget-materials');
    await expect(widget.locator('[data-arena-materials]')).toBeVisible();
    await widget.evaluate((el) => el.setAttribute('materials', 'hidden'));
    await expect(widget.locator('[data-arena-materials]')).toHaveCount(0);
  });

  test('Hebrew mirrors the block and speaks Hebrew', async ({ page }) => {
    await mockPlatform(page, FULL_MATERIALS);
    await openWidget(page);
    const widget = page.locator('#widget-materials');
    await widget.evaluate((el) => el.setAttribute('locale', 'he'));
    const block = widget.locator('[data-arena-materials]');
    await expect(block.getByRole('heading', { name: 'תמונות', exact: true })).toBeVisible();
    await expect(widget.locator('.arena-tickets-root')).toHaveAttribute('dir', 'rtl');
    // In a right-to-left lightbox "next" is the left arrow.
    await block.locator('[data-materials-section="gallery"] button').first().click();
    const dialog = widget.getByRole('dialog');
    await expect(dialog.locator('img')).toHaveAttribute('src', 'https://media.example/photo-1.png');
    await page.keyboard.press('ArrowLeft');
    await expect(dialog.locator('img')).toHaveAttribute('src', 'https://media.example/photo-2.png');
  });

  test('the block has no WCAG 2.2 AA violations, closed and with the lightbox open', async ({ page }) => {
    await mockPlatform(page, FULL_MATERIALS);
    await openWidget(page);
    const scan = () =>
      new AxeBuilder({ page })
        .include('#widget-materials')
        .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa'])
        .analyze();
    expect((await scan()).violations.filter((v) => /materials|mat-/.test(JSON.stringify(v.nodes)))).toEqual([]);
    await page.locator('#widget-materials [data-materials-section="gallery"] button').first().click();
    await expect(page.locator('#widget-materials').getByRole('dialog')).toBeVisible();
    expect((await scan()).violations.filter((v) => /materials|mat-/.test(JSON.stringify(v.nodes)))).toEqual([]);
  });
});
