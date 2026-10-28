/**
 * api.ts - client of the public shop API (backend lane feat/shop-core,
 * apps/backend/internal/platform/httpserver/hshop/public.go):
 *
 *   GET  /v1/public/shop/by-host?host=                  custom domain -> {org_slug}
 *   GET  /v1/public/shop/{org_slug}                     {org, settings, products, pages}
 *   GET  /v1/public/shop/{org_slug}/products/{slug}     {currency, product}
 *   GET  /v1/public/shop/pages/{org_slug}               {pages: [{slug, title, body_html, ...}]}
 *   POST /v1/public/shop/{org_slug}/checkout            201 {order_token, number, status, payment_url, ...}
 *                                                        409 shop.out_of_stock
 *   GET  /v1/public/shop/checkout/{order_token}         order status
 *
 * The wire shapes are private to this file. The views work on the NORMALISED
 * model below (ShopData, ShopProduct, ...), so a change of the contract stays
 * here. Money is in MINOR units everywhere.
 *
 * Picture URLs in the answers are signed and short-lived while the answer may
 * be cached for 30 seconds: they are used for the current render only and are
 * never written to localStorage or any other store (the basket keeps only
 * variant ids and quantities).
 */

export const API_BASE: string = (import.meta.env.VITE_API_BASE_URL ?? '').replace(/\/+$/, '');

// ---- normalised model (what the views use) -------------------------------------

export interface ShopSettings {
  currency: string;
  shipping_fee_minor: number;
  shipping_label?: string;
  /** null or absent: no age gate and no 18+ consent. */
  age_gate_min_age?: number | null;
  announcement_text?: string;
  contact_email?: string | null;
  /** Two-letter country of the shop, the delivery country of every order. */
  country_iso2?: string | null;
  /** The organization's display name (header logo alt, footer copyright). */
  shop_name?: string;
}

export interface ShopImage {
  url: string;
  alt?: string;
  /** Optional extension: natural pixel size, used for the card's aspect ratio before the image loads. */
  width?: number;
  height?: number;
}

export interface ShopVariant {
  id: string;
  title: string;
  price_minor: number;
  compare_at_minor?: number | null;
  /** True while the variant can be sold (wire: in_stock). */
  available: boolean;
  /** Units still for sale, null = unlimited (wire: available). */
  max?: number | null;
}

export interface ShopProduct {
  /** The API has no product id: the slug stands in. */
  id: string;
  slug: string;
  title: string;
  body_html: string;
  images: ShopImage[];
  variants: ShopVariant[];
  /** Printed above the title ("Einat Winery"). */
  vendor?: string;
  /** Optional extension: name of the variant option, for the picker's legend. */
  option_name?: string | null;
  /** Optional extension: slugs shown under "You may also like". Absent = the next products of the catalog. */
  related_slugs?: string[];
}

export interface MenuItem {
  title: string;
  url: string;
}

export interface ShopData {
  settings: ShopSettings;
  products: ShopProduct[];
  /** Static pages WITHOUT the policies. */
  pages: { slug: string; title: string }[];
  /** Derived here: the API has no menus (see buildMenus). */
  menus: { main: MenuItem[]; footer_rules: MenuItem[]; footer_info: MenuItem[] };
}

export interface ShopPage {
  title: string;
  body_html: string;
  /** 'policy' bodies are wrapped in the policy layout, 'pages' in the page layout. */
  kind?: string;
  slug?: string;
}

export interface CheckoutRequest {
  items: { variant_id: string; qty: number }[];
  buyer: { name: string; email: string; phone: string };
  ship: { address1: string; address2?: string; city: string; postal?: string; country: string };
  delivery_note?: string;
  locale: string;
  return_url: string;
  /** Required by the API whenever the shop has an age gate. */
  age_confirmed: boolean;
}

export interface CheckoutCreated {
  order_token: string;
  /** Empty for a free order (already paid). */
  payment_url: string;
  total_minor: number;
  currency: string;
  number?: number;
  status?: OrderStatusName;
}

export type OrderStatusName = 'pending' | 'paid' | 'expired' | 'cancelled';

export interface OrderLine {
  product_title: string;
  variant_title: string;
  qty: number;
  unit_price_minor: number;
  total_minor: number;
}

export interface OrderStatus {
  status: OrderStatusName;
  /** Only while the order is pending; empty or absent otherwise. */
  payment_url?: string | null;
  number?: string | number | null;
  total_minor?: number | null;
  subtotal_minor?: number | null;
  shipping_fee_minor?: number | null;
  currency?: string | null;
  items?: OrderLine[];
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details: unknown;
  constructor(status: number, code: string, message: string, details?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

// ---- wire shapes ---------------------------------------------------------------

interface WireImage {
  media_id?: string;
  url: string;
  position?: number;
  alt?: string;
  width?: number;
  height?: number;
}
interface WireVariant {
  id: string;
  title: string;
  sku?: string | null;
  price_minor: number;
  compare_at_minor?: number | null;
  available: number | null;
  in_stock: boolean;
  option1?: string | null;
  option2?: string | null;
  option3?: string | null;
}
interface WireProduct {
  slug: string;
  title: string;
  body_html: string;
  product_type?: string;
  vendor?: string;
  price_min_minor?: number;
  images: WireImage[];
  variants: WireVariant[];
  option_name?: string | null;
  related_slugs?: string[];
}
interface WireSettings {
  currency: string;
  shipping_fee_minor: number;
  shipping_label?: string;
  country_iso2?: string | null;
  age_gate_min_age?: number | null;
  announcement_text?: string;
  contact_email?: string | null;
  shop_name?: string;
}
interface WireCatalog {
  org?: { slug: string; name: string; shop_domain?: string | null };
  settings: WireSettings;
  products: WireProduct[];
  pages: { slug: string; title: string }[];
}
interface WirePage {
  slug: string;
  title: string;
  body_html: string;
  position?: number;
  updated_at?: string;
}
interface WireOrderLine {
  product_title: string;
  variant_title: string;
  qty: number;
  unit_price_minor: number;
  total_minor: number;
}
interface WireCheckoutCreated {
  order_token: string;
  number?: number;
  status?: OrderStatusName;
  payment_url?: string | null;
  currency: string;
  subtotal?: number;
  shipping_fee?: number;
  total: number;
}
interface WireStatus {
  status: OrderStatusName;
  number?: number;
  currency?: string;
  subtotal?: number;
  shipping_fee?: number;
  total?: number;
  items?: WireOrderLine[];
  payment_url?: string | null;
}

// ---- transport -----------------------------------------------------------------

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(API_BASE + path, {
      ...init,
      headers: { Accept: 'application/json', ...(init?.headers ?? {}) },
    });
  } catch (e) {
    throw new ApiError(0, 'network', e instanceof Error ? e.message : 'network error');
  }
  let body: unknown = null;
  const text = await res.text();
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = null;
    }
  }
  if (!res.ok) {
    const env = (body as { error?: { code?: string; message?: string; details?: unknown } } | null)?.error;
    throw new ApiError(res.status, env?.code ?? 'http_' + res.status, env?.message ?? res.statusText, env?.details);
  }
  return body as T;
}

const seg = encodeURIComponent;

// ---- normalisation -------------------------------------------------------------

/**
 * The API serves policies as ordinary pages. These four slugs are the policies
 * (the footer's "Наши правила" menu and the /policies/{slug} routes); every
 * other page is a static page.
 */
export const POLICY_SLUGS = ['privacy-policy', 'refund-policy', 'terms-of-service', 'contact-information'] as const;

export const isPolicySlug = (slug: string): boolean => (POLICY_SLUGS as readonly string[]).includes(slug);

const encPath = (path: string): string =>
  path
    .split('/')
    .map((x) => (x === '' ? x : encodeURIComponent(x)))
    .join('/');

const SHOP_MENU_TITLE = 'Магазин';

/** The API has no menus: the header lists the shop plus every static page, the footer lists the policies. */
export function buildMenus(pages: { slug: string; title: string }[]): ShopData['menus'] {
  const statics = pages.filter((p) => !isPolicySlug(p.slug));
  const rules = POLICY_SLUGS.map((slug) => pages.find((p) => p.slug === slug)).filter(
    (p): p is { slug: string; title: string } => !!p,
  );
  return {
    main: [
      { title: SHOP_MENU_TITLE, url: '/collections/all' },
      ...statics.map((p) => ({ title: p.title, url: encPath('/pages/' + p.slug) })),
    ],
    footer_rules: rules.map((p) => ({ title: p.title, url: encPath('/policies/' + p.slug) })),
    footer_info: [],
  };
}

export function normalizeProduct(p: WireProduct): ShopProduct {
  return {
    id: p.slug,
    slug: p.slug,
    title: p.title,
    body_html: p.body_html ?? '',
    vendor: p.vendor || undefined,
    option_name: p.option_name ?? null,
    related_slugs: p.related_slugs,
    images: (p.images ?? [])
      .slice()
      .sort((a, b) => (a.position ?? 0) - (b.position ?? 0))
      .map((i) => ({ url: i.url, alt: i.alt ?? '', width: i.width, height: i.height })),
    variants: (p.variants ?? []).map((v) => ({
      id: v.id,
      title: v.title,
      price_minor: v.price_minor,
      compare_at_minor: v.compare_at_minor ?? null,
      available: v.in_stock,
      max: v.available ?? null,
    })),
  };
}

export function normalizeCatalog(c: WireCatalog): ShopData {
  const s = c.settings;
  const links = c.pages ?? [];
  return {
    settings: {
      currency: s.currency,
      shipping_fee_minor: s.shipping_fee_minor,
      shipping_label: s.shipping_label,
      age_gate_min_age: s.age_gate_min_age ?? null,
      announcement_text: s.announcement_text,
      contact_email: s.contact_email ?? null,
      country_iso2: s.country_iso2 ?? null,
      shop_name: s.shop_name || c.org?.name || undefined,
    },
    products: (c.products ?? []).map(normalizeProduct),
    pages: links.filter((p) => !isPolicySlug(p.slug)),
    menus: buildMenus(links),
  };
}

export function normalizeStatus(s: WireStatus): OrderStatus {
  return {
    status: s.status,
    payment_url: s.status === 'pending' && s.payment_url ? s.payment_url : null,
    number: s.number ?? null,
    total_minor: s.total ?? null,
    subtotal_minor: s.subtotal ?? null,
    shipping_fee_minor: s.shipping_fee ?? null,
    currency: s.currency ?? null,
    items: s.items,
  };
}

// ---- calls ---------------------------------------------------------------------

export async function getShop(orgSlug: string): Promise<ShopData> {
  return normalizeCatalog(await request<WireCatalog>(`/v1/public/shop/${seg(orgSlug)}`));
}

export async function getProduct(orgSlug: string, slug: string): Promise<ShopProduct> {
  const r = await request<{ currency?: string; product: WireProduct }>(
    `/v1/public/shop/${seg(orgSlug)}/products/${seg(slug)}`,
  );
  return normalizeProduct(r.product);
}

// One request carries every page of the shop, so keep the answer for the life
// of the tab (page texts, no signed URLs) and pick a page out of it.
const pagesMemo = new Map<string, Promise<WirePage[]>>();

export function getPage(orgSlug: string, slug: string): Promise<ShopPage> {
  let all = pagesMemo.get(orgSlug);
  if (!all) {
    all = request<{ pages: WirePage[] }>(`/v1/public/shop/pages/${seg(orgSlug)}`).then((r) => r.pages ?? []);
    pagesMemo.set(orgSlug, all);
    all.catch(() => pagesMemo.delete(orgSlug));
  }
  return all.then((pages) => {
    const p = pages.find((x) => x.slug === slug);
    if (!p) throw new ApiError(404, 'shop.page_not_found', 'page not found');
    return { slug: p.slug, title: p.title, body_html: p.body_html, kind: isPolicySlug(p.slug) ? 'policy' : 'pages' };
  });
}

export function resolveHost(host: string): Promise<{ org_slug: string }> {
  return request<{ org_slug: string }>(`/v1/public/shop/by-host?host=${encodeURIComponent(host)}`);
}

export async function createCheckout(orgSlug: string, body: CheckoutRequest): Promise<CheckoutCreated> {
  const r = await request<WireCheckoutCreated>(`/v1/public/shop/${seg(orgSlug)}/checkout`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  return {
    order_token: r.order_token,
    payment_url: r.payment_url ?? '',
    total_minor: r.total,
    currency: r.currency,
    number: r.number,
    status: r.status,
  };
}

export async function getOrderStatus(orderToken: string): Promise<OrderStatus> {
  return normalizeStatus(await request<WireStatus>(`/v1/public/shop/checkout/${seg(orderToken)}`));
}
