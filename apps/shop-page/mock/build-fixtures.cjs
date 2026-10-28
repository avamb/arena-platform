#!/usr/bin/env node
'use strict';
/**
 * Builds mock/fixtures/* from the offline export of the live Einat Winery
 * Shopify storefront. The output is committed, so this only needs to run when
 * the export changes:
 *
 *   node mock/build-fixtures.cjs C:/Projects/arena_new/.local/einatwinery_export
 *
 * What it reads: public/products.json (the 7 active products - public data)
 * and theme_live/pages/*.html (rendered pages and policies). Nothing here
 * contains buyer data or secrets.
 */
const fs = require('fs');
const path = require('path');

const exportDir = process.argv[2] || process.env.EXPORT_DIR;
if (!exportDir) {
  console.error('usage: node mock/build-fixtures.cjs <einatwinery_export dir>');
  process.exit(1);
}
const outDir = path.join(__dirname, 'fixtures');
fs.mkdirSync(outDir, { recursive: true });

// The order the live storefront shows the products in (home and /collections/all).
const ORDER = [
  'wineandglassesset',
  'lonelycat',
  'красное-сухое-4-кота',
  'набор-красных-вин-собери-кота',
  '3cats-2018',
  'two-cats',
  'lonely-cat-cabfr',
];

// What the live shop's "You may also like" showed for each product on 2026-10-28
// (Shopify's own recommendation engine; observed, not derivable from the export).
const RELATED = {
  wineandglassesset: ['two-cats', 'красное-сухое-4-кота', 'набор-красных-вин-собери-кота', '3cats-2018'],
  lonelycat: ['красное-сухое-4-кота', 'two-cats', 'набор-красных-вин-собери-кота', 'lonely-cat-cabfr'],
  'красное-сухое-4-кота': ['two-cats', 'lonelycat', 'набор-красных-вин-собери-кота', '3cats-2018'],
  'набор-красных-вин-собери-кота': ['two-cats', '3cats-2018', 'красное-сухое-4-кота', 'lonelycat'],
  '3cats-2018': ['набор-красных-вин-собери-кота', 'красное-сухое-4-кота', 'two-cats', 'lonely-cat-cabfr'],
  'two-cats': ['красное-сухое-4-кота', 'lonely-cat-cabfr', 'набор-красных-вин-собери-кота', 'lonelycat'],
  'lonely-cat-cabfr': ['two-cats', 'lonelycat', 'набор-красных-вин-собери-кота', 'красное-сухое-4-кота'],
};

const src = JSON.parse(fs.readFileSync(path.join(exportDir, 'public', 'products.json'), 'utf8')).products;
const byHandle = new Map(src.map((p) => [p.handle, p]));

const minor = (price) => Math.round(parseFloat(price) * 100);

// The shape of GET /v1/public/shop/{org} products (hshop/views.go publicProductJSON).
// option_name, related_slugs and images[].width/height are storefront extensions the
// real API may not send; the storefront falls back without them.
const products = ORDER.map((handle) => {
  const p = byHandle.get(handle);
  if (!p) throw new Error('product missing in export: ' + handle);
  const multi = p.variants.length > 1;
  const variants = p.variants.map((v) => ({
    id: String(v.id),
    title: v.title,
    sku: null,
    price_minor: minor(v.price),
    compare_at_minor: v.compare_at_price ? minor(v.compare_at_price) : null,
    available: null,
    in_stock: !!v.available,
    option1: multi ? v.title : null,
    option2: null,
    option3: null,
  }));
  return {
    slug: p.handle,
    title: p.title,
    body_html: p.body_html || '',
    product_type: p.product_type || '',
    vendor: p.vendor || 'Einat Winery',
    price_min_minor: Math.min(...variants.map((v) => v.price_minor)),
    images: p.images.map((i, n) => ({
      media_id: String(i.id),
      url: i.src,
      position: n,
      alt: i.alt || '',
      width: i.width,
      height: i.height,
    })),
    variants,
    option_name: multi ? p.options[0].name : null,
    related_slugs: RELATED[handle] || [],
  };
});

// ---- pages -----------------------------------------------------------------

const pagesDir = path.join(exportDir, 'theme_live', 'pages');
const decode = (s) => decodeURIComponent(s);

function mainOf(html) {
  html = html.replace(/<script[\s\S]*?<\/script>/g, '');
  const m = html.match(/<main[^>]*>([\s\S]*?)<\/main>/);
  if (!m) throw new Error('no <main>');
  return m[1]
    .replace(/<link [^>]*>/g, '')
    .replace(/<noscript>[\s\S]*?<\/noscript>/g, '')
    .replace(/(src|srcset)="\/\//g, '$1="https://')
    .replace(/, \/\/www\.einatwinery\.com/g, ', https://www.einatwinery.com')
    .replace(/\n\s*\n+/g, '\n')
    .trim();
}

function titleOf(html) {
  const m = html.match(/<title>\s*([\s\S]*?)\s*&ndash;/);
  return m ? m[1].trim() : '';
}

const pages = [];
for (const file of fs.readdirSync(pagesDir)) {
  const m = file.match(/^(pages|policies)__(.+)\.html$/);
  if (!m) continue;
  const kind = m[1];
  const slug = decode(m[2]);
  const html = fs.readFileSync(path.join(pagesDir, file), 'utf8');
  let title = titleOf(html);
  let body = mainOf(html);
  if (kind === 'policies') {
    const t = body.match(/<h1>([\s\S]*?)<\/h1>/);
    if (t) title = t[1].trim();
    const b = body.match(/<div class="shopify-policy__body">\s*<div class="rte">([\s\S]*?)<\/div>\s*<\/div>\s*<\/div>\s*$/);
    if (!b) throw new Error('policy body not found: ' + file);
    body = b[1].trim();
  }
  pages.push({ slug, title, kind, body_html: body });
}
// Page titles that Shopify keeps in the menu, not in <title> of an empty section
const titleFix = {
  'наши-мероприятия': 'Публичные мероприятия',
  'наши-партнеры': 'Наши партнеры',
  'о-нас': 'О нас',
  'частные-мероприятия': 'Частные мероприятия',
  contact: 'Свяжитесь с нами',
};
for (const p of pages) if (titleFix[p.slug]) p.title = titleFix[p.slug];

// GET /v1/public/shop/pages/{org}: every page with its text, in menu order.
const PAGE_ORDER = [
  'наши-мероприятия', 'частные-мероприятия', 'наши-партнеры', 'о-нас', 'contact',
  'privacy-policy', 'refund-policy', 'terms-of-service', 'contact-information',
];
pages.sort((a, b) => PAGE_ORDER.indexOf(a.slug) - PAGE_ORDER.indexOf(b.slug));
const wirePages = pages.map((p, n) => ({
  slug: p.slug,
  title: p.title,
  body_html: p.body_html,
  position: n,
  updated_at: '2026-10-28T00:00:00Z',
}));
fs.rmSync(path.join(outDir, 'pages'), { recursive: true, force: true });
fs.writeFileSync(path.join(outDir, 'pages.json'), JSON.stringify({ pages: wirePages }, null, 1));

// ---- shop ------------------------------------------------------------------

// GET /v1/public/shop/{org}: the API has no menus, the storefront builds them from the page list.
const shop = {
  org: { slug: 'einatwinery', name: 'Einat Winery', shop_domain: 'einatwinery.com' },
  settings: {
    currency: 'ILS',
    shipping_fee_minor: 5000,
    shipping_label: 'Доставка по Израилю',
    country_iso2: 'IL',
    age_gate_min_age: 18,
    announcement_text: 'Special discount 15% on gift sets',
    contact_email: 'sales@einatwinery.com',
  },
  products,
  pages: wirePages.map((p) => ({ slug: p.slug, title: p.title })),
};
fs.writeFileSync(path.join(outDir, 'shop.json'), JSON.stringify(shop, null, 1));
console.log('products', products.length, 'pages', pages.length);
