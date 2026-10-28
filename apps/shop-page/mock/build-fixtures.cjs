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
fs.mkdirSync(path.join(outDir, 'pages'), { recursive: true });

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

const src = JSON.parse(fs.readFileSync(path.join(exportDir, 'public', 'products.json'), 'utf8')).products;
const byHandle = new Map(src.map((p) => [p.handle, p]));

const minor = (price) => Math.round(parseFloat(price) * 100);

const products = ORDER.map((handle) => {
  const p = byHandle.get(handle);
  if (!p) throw new Error('product missing in export: ' + handle);
  const multi = p.variants.length > 1;
  return {
    id: String(p.id),
    slug: p.handle,
    title: p.title,
    vendor: p.vendor || 'Einat Winery',
    body_html: p.body_html || '',
    option_name: multi ? p.options[0].name : null,
    images: p.images.map((i) => ({
      url: i.src,
      alt: i.alt || '',
      width: i.width,
      height: i.height,
    })),
    variants: p.variants.map((v) => ({
      id: String(v.id),
      title: v.title,
      price_minor: minor(v.price),
      compare_at_minor: v.compare_at_price ? minor(v.compare_at_price) : null,
      available: !!v.available,
    })),
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

for (const p of pages) {
  fs.writeFileSync(
    path.join(outDir, 'pages', encodeURIComponent(p.slug) + '.json'),
    JSON.stringify({ slug: p.slug, title: p.title, kind: p.kind, body_html: p.body_html }, null, 1),
  );
}

// ---- shop ------------------------------------------------------------------

const enc = (s) => s.split('/').map((x) => (x === '' ? x : encodeURIComponent(x))).join('/');
const shop = {
  settings: {
    currency: 'ILS',
    shipping_fee_minor: 5000,
    shipping_label: 'Доставка по Израилю',
    age_gate_min_age: 18,
    announcement_text: 'Special discount 15% on gift sets',
    contact_email: 'sales@einatwinery.com',
    shop_name: 'Einat Winery',
  },
  products,
  pages: pages.filter((p) => p.kind === 'pages').map((p) => ({ slug: p.slug, title: p.title })),
  menus: {
    main: [
      { title: 'Магазин', url: '/collections/all' },
      { title: 'Публичные мероприятия', url: enc('/pages/наши-мероприятия') },
      { title: 'Частные мероприятия', url: enc('/pages/частные-мероприятия') },
      { title: 'Наши партнеры', url: enc('/pages/наши-партнеры') },
      { title: 'О нас', url: enc('/pages/о-нас') },
      { title: 'Свяжитесь с нами', url: '/pages/contact' },
    ],
    footer_rules: [
      { title: 'Privacy Policy', url: '/policies/privacy-policy' },
      { title: 'Refund Policy', url: '/policies/refund-policy' },
      { title: 'Terms of Service', url: '/policies/terms-of-service' },
      { title: 'Contact Information', url: '/policies/contact-information' },
    ],
    footer_info: [],
  },
};
fs.writeFileSync(path.join(outDir, 'shop.json'), JSON.stringify(shop, null, 1));
console.log('products', products.length, 'pages', pages.length);
