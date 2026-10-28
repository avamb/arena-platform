/**
 * api.ts - client of the public shop API (spec 47 section 4):
 *
 *   GET  /v1/public/shop/{org_slug}                    settings + catalog + pages + menus
 *   GET  /v1/public/shop/{org_slug}/products/{slug}
 *   GET  /v1/public/shop/{org_slug}/pages/{slug}       a static page or a policy
 *   POST /v1/public/shop/{org_slug}/checkout           -> 201 or 409 shop.out_of_stock
 *   GET  /v1/public/shop/checkout/{order_token}        status of one order
 *   GET  /v1/public/shop/by-host?host=                 custom domain -> org_slug
 *
 * Money is in MINOR units. Fields marked "optional" are extensions the
 * storefront tolerates the absence of (see README, "API contract notes").
 */

export const API_BASE: string = (import.meta.env.VITE_API_BASE_URL ?? '').replace(/\/+$/, '');

export interface ShopSettings {
  currency: string;
  shipping_fee_minor: number;
  shipping_label?: string;
  age_gate_min_age?: number;
  announcement_text?: string;
  contact_email?: string;
  /** Optional: the shop's display name (header logo alt, footer copyright). */
  shop_name?: string;
}

export interface ShopImage {
  url: string;
  alt?: string;
  /** Optional: natural pixel size, used for the card's aspect ratio before the image loads. */
  width?: number;
  height?: number;
}

export interface ShopVariant {
  id: string;
  title: string;
  price_minor: number;
  compare_at_minor?: number | null;
  available: boolean;
}

export interface ShopProduct {
  id: string;
  slug: string;
  title: string;
  body_html: string;
  images: ShopImage[];
  variants: ShopVariant[];
  /** Optional: printed above the title ("Einat Winery"). */
  vendor?: string;
  /** Optional: name of the variant option ("Выбор вина для набора"), for the picker's legend. */
  option_name?: string | null;
  /** Optional: slugs shown under "You may also like", in order. Absent = the next products of the catalog. */
  related_slugs?: string[];
}

export interface MenuItem {
  title: string;
  url: string;
}

export interface ShopData {
  settings: ShopSettings;
  products: ShopProduct[];
  pages: { slug: string; title: string }[];
  menus: { main: MenuItem[]; footer_rules: MenuItem[]; footer_info: MenuItem[] };
}

export interface ShopPage {
  title: string;
  body_html: string;
  /** Optional: 'policy' bodies are wrapped in the policy layout, 'pages' in the page layout. */
  kind?: string;
  slug?: string;
}

export interface CheckoutRequest {
  items: { variant_id: string; qty: number }[];
  buyer: { name: string; email: string; phone: string };
  ship: { address1: string; address2?: string; city: string; postal?: string; country: string };
  locale: string;
  return_url: string;
}

export interface CheckoutCreated {
  order_token: string;
  payment_url: string;
  total_minor: number;
  currency: string;
}

export type OrderStatusName = 'pending' | 'paid' | 'expired' | 'cancelled';

export interface OrderStatus {
  status: OrderStatusName;
  payment_url?: string | null;
  number?: string | number | null;
  total_minor?: number | null;
  currency?: string | null;
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

export function getShop(orgSlug: string): Promise<ShopData> {
  return request<ShopData>(`/v1/public/shop/${seg(orgSlug)}`);
}

export function getProduct(orgSlug: string, slug: string): Promise<ShopProduct> {
  return request<ShopProduct>(`/v1/public/shop/${seg(orgSlug)}/products/${seg(slug)}`);
}

export function getPage(orgSlug: string, slug: string): Promise<ShopPage> {
  return request<ShopPage>(`/v1/public/shop/${seg(orgSlug)}/pages/${seg(slug)}`);
}

export function resolveHost(host: string): Promise<{ org_slug: string }> {
  return request<{ org_slug: string }>(`/v1/public/shop/by-host?host=${encodeURIComponent(host)}`);
}

export function createCheckout(orgSlug: string, body: CheckoutRequest): Promise<CheckoutCreated> {
  return request<CheckoutCreated>(`/v1/public/shop/${seg(orgSlug)}/checkout`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
}

export function getOrderStatus(orderToken: string): Promise<OrderStatus> {
  return request<OrderStatus>(`/v1/public/shop/checkout/${seg(orderToken)}`);
}
