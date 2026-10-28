#!/usr/bin/env node
/**
 * Visual comparison of the storefront against the live Shopify site.
 *
 *   LOCAL=http://localhost:5180/einatwinery node scripts/compare-live.mjs [name ...]
 *
 * For every page and viewport it saves, under docs/screenshots/:
 *   <name>-<viewport>-live.png    the live site
 *   <name>-<viewport>-local.png   this app
 *   <name>-<viewport>-side.png    the two next to each other plus a difference
 *                                 heat map, with the share of differing pixels
 * Pages marked `local` have no counterpart on the live site (our own checkout,
 * order status) and only get <name>-<viewport>-local.png.
 *
 * Needs playwright-core (not a dependency of this app: point PLAYWRIGHT_CORE at
 * an installation, for example apps/widget/node_modules/playwright-core) and a
 * Chrome (CHROME_PATH, else the playwright "chrome" channel). The mock API
 * (npm run dev:mock) must be running for the local flows.
 */
import { createRequire } from 'node:module';
import { mkdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const out = join(here, '..', 'docs', 'screenshots');
mkdirSync(out, { recursive: true });

const require = createRequire(import.meta.url);
let pw;
try {
  pw = require('playwright-core');
} catch {
  pw = require(process.env.PLAYWRIGHT_CORE || 'playwright-core');
}

const LIVE = process.env.LIVE || 'https://www.einatwinery.com';
const LOCAL = process.env.LOCAL || 'http://localhost:5180/einatwinery';
const MOCK_API = process.env.MOCK_API || 'http://localhost:8787';

/** Puts one bottle in the basket the way a buyer does, then lands on `to`. */
const addThenGo = (to) => async (page, base) => {
  await page.locator('.product-form__submit').first().click();
  await page.waitForTimeout(1200);
  await page.goto(base + to, { waitUntil: 'networkidle' }).catch(() => {});
};

/** A fresh order on the mock API, then its status page. */
const orderStatus = (action) => async (page, base) => {
  const token = await page.evaluate(async ([api]) => {
    const r = await fetch(api + '/v1/public/shop/einatwinery/checkout', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        items: [{ variant_id: '50647889379613', qty: 1 }],
        buyer: { name: 'Test', email: 't@example.com', phone: '0501234567' },
        ship: { address1: 'x 1', city: 'y', country: 'IL' },
        locale: 'ru',
        return_url: location.origin + '/einatwinery/checkout/result',
      }),
    });
    return (await r.json()).order_token;
  }, [MOCK_API]);
  if (action) await page.evaluate(async ([api, t, a]) => { await fetch(api + '/mock-pay/' + t + '/' + a, { redirect: 'manual' }); }, [MOCK_API, token, action]);
  await page.goto(base + '/checkout/' + token, { waitUntil: 'networkidle' }).catch(() => {});
};

const PAGES = [
  { name: 'home', path: '/' },
  { name: 'collection', path: '/collections/all' },
  { name: 'product-single', path: '/products/lonely-cat-cabfr' },
  { name: 'product-variants', path: '/products/wineandglassesset' },
  { name: 'cart-empty', path: '/cart' },
  { name: 'cart-filled', path: '/products/lonelycat', after: addThenGo('/cart') },
  { name: 'notification', path: '/products/lonelycat', afterShot: 'viewport', after: async (page) => { await page.locator('.product-form__submit').first().click(); await page.waitForTimeout(1200); } },
  { name: 'page-about', path: '/pages/%D0%BE-%D0%BD%D0%B0%D1%81' },
  { name: 'page-events', path: '/pages/%D0%BD%D0%B0%D1%88%D0%B8-%D0%BC%D0%B5%D1%80%D0%BE%D0%BF%D1%80%D0%B8%D1%8F%D1%82%D0%B8%D1%8F' },
  { name: 'page-private', path: '/pages/%D1%87%D0%B0%D1%81%D1%82%D0%BD%D1%8B%D0%B5-%D0%BC%D0%B5%D1%80%D0%BE%D0%BF%D1%80%D0%B8%D1%8F%D1%82%D0%B8%D1%8F' },
  { name: 'page-partners', path: '/pages/%D0%BD%D0%B0%D1%88%D0%B8-%D0%BF%D0%B0%D1%80%D1%82%D0%BD%D0%B5%D1%80%D1%8B' },
  { name: 'page-contact', path: '/pages/contact' },
  { name: 'policy-refund', path: '/policies/refund-policy' },
  { name: 'policy-terms', path: '/policies/terms-of-service' },
  { name: 'not-found', path: '/pages/does-not-exist' },
  { name: 'age-gate', path: '/', ageGate: true, viewportOnly: true },
  { name: 'menu-open', path: '/', mobileOnly: true, viewportOnly: true, after: async (page) => { await page.locator('header-drawer summary').click(); await page.waitForTimeout(800); } },
  { name: 'search-open', path: '/', viewportOnly: true, after: async (page) => { await page.locator('details-modal summary').first().click(); await page.waitForTimeout(800); } },
  // our own pages, no live original
  { name: 'checkout', path: '/products/lonelycat', local: true, after: async (page, base) => { await page.locator('.product-form__submit').first().click(); await page.waitForTimeout(800); await page.goto(base + '/checkout', { waitUntil: 'networkidle' }); } },
  { name: 'status-pending', path: '/', local: true, after: orderStatus('') },
  { name: 'status-paid', path: '/', local: true, after: orderStatus('pay') },
  { name: 'status-expired', path: '/', local: true, after: orderStatus('expire') },
];

const VIEWPORTS = {
  desktop: { width: 1440, height: 900, isMobile: false, hasTouch: false, deviceScaleFactor: 1 },
  mobile: { width: 390, height: 844, isMobile: true, hasTouch: true, deviceScaleFactor: 1 },
};

const only = process.argv.slice(2);

const browser = await pw.chromium.launch({
  headless: true,
  ...(process.env.CHROME_PATH ? { executablePath: process.env.CHROME_PATH } : { channel: 'chrome' }),
});

async function shoot(vp, base, entry, file) {
  // A fresh context per shot: the live basket is a cookie, ours is localStorage, and neither may leak between pages.
  const context = await newContext(vp);
  const page = await context.newPage();
  if (!entry.ageGate) {
    await page.addInitScript(() => {
      try {
        sessionStorage.setItem('allow', 'true');
        localStorage.setItem('arena_shop_age_ok:einatwinery', '1');
      } catch {}
    });
  }
  await page.goto(base + entry.path, { waitUntil: 'networkidle', timeout: 60000 }).catch(() => {});
  if (!entry.viewportOnly) {
    // Trigger lazy images, then return to the top.
    await page.evaluate(async () => {
      for (let y = 0; y < document.body.scrollHeight; y += 600) {
        window.scrollTo(0, y);
        await new Promise((r) => setTimeout(r, 80));
      }
      window.scrollTo(0, 0);
    });
  }
  await page.waitForTimeout(800);
  if (entry.after) {
    await entry.after(page, base);
    await page.waitForTimeout(600);
    if (!entry.viewportOnly && entry.afterShot !== 'viewport') {
      await page.evaluate(async () => {
        for (let y = 0; y < document.body.scrollHeight; y += 600) {
          window.scrollTo(0, y);
          await new Promise((r) => setTimeout(r, 80));
        }
        window.scrollTo(0, 0);
      });
      await page.waitForTimeout(500);
    }
  }
  await page.screenshot({ path: file, fullPage: !(entry.viewportOnly || entry.afterShot === 'viewport') });
  await context.close();
}

const newContext = (vp) =>
  browser.newContext({
    viewport: { width: vp.width, height: vp.height },
    isMobile: vp.isMobile,
    hasTouch: vp.hasTouch,
    deviceScaleFactor: vp.deviceScaleFactor,
    locale: 'ru-RU',
    userAgent: vp.isMobile
      ? 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1'
      : undefined,
  });

const diffPage = readFileSync(join(here, 'diff.html'), 'utf8');

for (const [vpName, vp] of Object.entries(VIEWPORTS)) {
  for (const p of PAGES) {
    if (only.length && !only.includes(p.name)) continue;
    if (p.mobileOnly && vpName !== 'mobile') continue;
    const base = join(out, `${p.name}-${vpName}`);
    if (p.local) {
      await shoot(vp, LOCAL, p, base + '-local.png');
      console.log(`${p.name.padEnd(18)} ${vpName.padEnd(8)} local only`);
      continue;
    }
    await shoot(vp, LIVE, p, base + '-live.png');
    await shoot(vp, LOCAL, p, base + '-local.png');
    const context = await newContext(vp);
    const page = await context.newPage();
    const live = readFileSync(base + '-live.png').toString('base64');
    const local = readFileSync(base + '-local.png').toString('base64');
    await page.setViewportSize({ width: 1200, height: 800 });
    await page.setContent(diffPage);
    const stats = await page.evaluate(
      async ([a, b]) => window.renderDiff('data:image/png;base64,' + a, 'data:image/png;base64,' + b),
      [live, local],
    );
    await page.locator('#out').screenshot({ path: base + '-side.png' });
    await context.close();
    console.log(`${p.name.padEnd(18)} ${vpName.padEnd(8)} live ${stats.lw}x${stats.lh}  local ${stats.rw}x${stats.rh}  differing pixels ${stats.pct.toFixed(1)}%`);
  }
}
await browser.close();
