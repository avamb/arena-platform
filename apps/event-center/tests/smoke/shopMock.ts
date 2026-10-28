/**
 * The mocked shop API of the shop mock smoke (shop.mock.spec.ts).
 *
 * It sits on top of mockApi.ts: call mockApi(page) first, then mockShop(page).
 * Playwright runs the LAST registered route first and route.fallback() hands a
 * request down to the earlier one, so everything this file does not answer
 * (login, events, the organization list) still comes from mockApi.
 */
import type { Page, Route } from "@playwright/test";
import { API, CORS, ORG_A } from "./mockApi";

export const PRODUCT_WINE = "0193f01a-0000-7000-8000-0000000000b1";
export const PRODUCT_BAG = "0193f01a-0000-7000-8000-0000000000b2";
export const ORDER_PAID = "0193f01a-0000-7000-8000-0000000000c1";
export const ORDER_REVIEW = "0193f01a-0000-7000-8000-0000000000c2";
export const ORDER_UNPAID = "0193f01a-0000-7000-8000-0000000000c3";

interface Variant {
  id: string;
  title: string;
  sku: string | null;
  price_minor: number;
  compare_at_minor: number | null;
  stock: number;
  track_stock: boolean;
  held: number;
  available: number | null;
  position: number;
  option1: string | null;
  option2: string | null;
  option3: string | null;
}

interface Product {
  id: string;
  org_id: string;
  slug: string;
  title: string;
  body_html: string;
  status: string;
  product_type: string;
  vendor: string;
  sort_order: number;
  kind: string;
  images: unknown[];
  variants: Variant[];
  created_at: string;
  updated_at: string;
}

const variant = (id: string, title: string, price: number, stock: number, extra: Partial<Variant> = {}): Variant => ({
  id,
  title,
  sku: null,
  price_minor: price,
  compare_at_minor: null,
  stock,
  track_stock: true,
  held: 0,
  available: stock,
  position: 0,
  option1: null,
  option2: null,
  option3: null,
  ...extra,
});

const product = (id: string, title: string, variants: Variant[], extra: Partial<Product> = {}): Product => ({
  id,
  org_id: ORG_A,
  slug: title.toLowerCase().replace(/\s+/g, "-"),
  title,
  body_html: "<p>Dry white wine</p>",
  status: "active",
  product_type: "",
  vendor: "",
  sort_order: 0,
  kind: "physical",
  images: [],
  variants,
  created_at: "2026-10-01T10:00:00Z",
  updated_at: "2026-10-01T10:00:00Z",
  ...extra,
});

function order(id: string, number: number, status: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    org_id: ORG_A,
    number,
    status,
    source: "web",
    buyer: { name: "Anna Smith", email: "anna@example.test", phone: "+34 600 111 222" },
    ship: { name: "", address1: "Calle Mayor 1", address2: "2B", city: "Madrid", postal: "28013", country: "ES" },
    delivery_kind: "shipping",
    delivery_note: "Ring twice",
    currency: "EUR",
    subtotal: 4000,
    discount: 0,
    shipping_fee: 490,
    total: 4490,
    refunded_minor: 0,
    tracking_note: "",
    items: [{ variant_id: "v1", product_title: "Riesling", variant_title: "2021 / 0.75 l", sku: "RIE-21", qty: 2, unit_price_minor: 2000, total_minor: 4000 }],
    payment: { id: "pay1", provider: "stripe", state: "succeeded", amount: 4490, currency: "EUR" },
    expires_at: null,
    paid_at: "2026-10-05T10:05:00Z",
    shipped_at: null,
    cancelled_at: null,
    created_at: "2026-10-05T10:00:00Z",
    updated_at: "2026-10-05T10:05:00Z",
    ...extra,
  };
}

export interface ShopMockState {
  products: Product[];
  orders: ReturnType<typeof order>[];
  /** "METHOD path body" of every call that changes something. */
  writes: string[];
  /** The order list queries the screen sent. */
  orderQueries: string[];
  /** The next PUT .../variants answers 409 shop.stock_changed once. */
  stockConflict: boolean;
  /** The permissions the mocked person holds in ORG_A. */
  permissions: string[];
  settings: Record<string, unknown>;
}

function json(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}

const MANAGER = ["event.read", "order.read", "shop.read", "shop.manage", "channel.read"];

export async function mockShop(page: Page, permissions: string[] = MANAGER): Promise<ShopMockState> {
  const state: ShopMockState = {
    permissions,
    writes: [],
    orderQueries: [],
    stockConflict: false,
    products: [
      product(
        PRODUCT_WINE,
        "Riesling",
        [
          variant("v1", "2021 / 0.75 l", 2000, 12, { option1: "2021", option2: "0.75 l", held: 2, available: 10, sku: "RIE-21" }),
          variant("v2", "2022 / 0.75 l", 2200, 0, { option1: "2022", option2: "0.75 l", position: 1 }),
        ],
        {},
      ),
      product(PRODUCT_BAG, "Tote bag", [variant("v3", "Default Title", 900, 5)], { status: "draft" }),
    ],
    orders: [
      order(ORDER_PAID, 1042, "paid"),
      order(ORDER_REVIEW, 1043, "manual_review", { buyer: { name: "Boris Mock", email: "boris@example.test" } }),
      order(ORDER_UNPAID, 1044, "pending_payment", { payment: null, paid_at: null, expires_at: "2026-10-05T10:33:00Z", delivery_kind: "pickup" }),
    ],
    settings: {
      org_id: ORG_A,
      configured: true,
      currency: "EUR",
      shipping_fee_minor: 490,
      shipping_label: "Courier",
      country_iso2: "ES",
      age_gate_min_age: null,
      announcement_text: "",
      contact_email: "shop@example.test",
      logistics_note: "",
      sales_channel_id: "ch1",
      shop_domain: "shop.example.test",
      max_line_qty: 24,
      enabled: true,
      updated_at: "2026-10-01T10:00:00Z",
    },
  };

  // The password question of a money action.
  await page.route(`${API}/v1/auth/step-up`, async (route) => {
    const body = route.request().postDataJSON() as { password: string };
    if (body.password !== "right-password") return json(route, 403, { error: { code: "auth.step_up_wrong_password", message: "no" } });
    return json(route, 200, { step_up_token: "su-token", expires_at: new Date(Date.now() + 300_000).toISOString() });
  });

  await page.route(`${API}/v1/organizations/${ORG_A}/**`, async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace(`/v1/organizations/${ORG_A}`, "");
    const m = req.method();
    if (m === "OPTIONS") return route.fallback();
    const write = (extra = "") => state.writes.push(`${m} ${path}${extra === "" ? "" : ` ${extra}`}`);

    if (path === "/me") {
      return json(route, 200, {
        org_id: ORG_A,
        org_name: "Teatr Kolybel",
        org_display_number: 11,
        caller_type: "user",
        role: "organizer",
        membership_roles: ["organizer"],
        roles: ["organizer"],
        permissions: state.permissions,
        platform_superadmin: false,
      });
    }
    if (path === "/channels" && m === "GET") {
      return json(route, 200, { channels: [{ id: "ch1", display_number: 1, org_id: ORG_A, name: "Own site", type: "online", created_at: "2026-01-01T00:00:00Z" }] });
    }
    if (!path.startsWith("/shop/")) return route.fallback();
    if (!state.permissions.includes("shop.read")) return json(route, 403, { error: { code: "permissions.denied", message: "no" } });

    if (path === "/shop/settings") {
      if (m === "PUT") {
        const body = req.postDataJSON() as Record<string, unknown>;
        write(JSON.stringify(body));
        state.settings = { ...state.settings, ...body, updated_at: "2026-10-06T10:00:00Z" };
      }
      return json(route, 200, state.settings);
    }
    if (path === "/shop/products" && m === "GET") {
      const status = url.searchParams.get("status");
      return json(route, 200, { products: state.products.filter((p) => status === null || p.status === status) });
    }
    if (path === "/shop/products" && m === "POST") {
      const body = req.postDataJSON() as { title: string; variants?: Variant[]; status?: string };
      write(JSON.stringify(body));
      const made = product(
        "0193f01a-0000-7000-8000-0000000000b9",
        body.title,
        (body.variants ?? []).map((v, i) => variant(`nv${i}`, v.title || "Default Title", v.price_minor, v.stock, { position: i })),
        { status: body.status ?? "draft" },
      );
      state.products.push(made);
      return json(route, 201, made);
    }
    const one = /^\/shop\/products\/([^/]+)$/.exec(path);
    if (one !== null) {
      const p = state.products.find((x) => x.id === one[1]);
      if (p === undefined) return json(route, 404, { error: { code: "shop.product_not_found", message: "no" } });
      if (m === "PATCH") {
        const body = req.postDataJSON() as Partial<Product>;
        write(JSON.stringify(body));
        Object.assign(p, body);
      }
      if (m === "DELETE") {
        write();
        state.products = state.products.filter((x) => x.id !== p.id);
        return route.fulfill({ status: 204, headers: CORS });
      }
      return json(route, 200, p);
    }
    const vars = /^\/shop\/products\/([^/]+)\/variants$/.exec(path);
    if (vars !== null && m === "PUT") {
      const p = state.products.find((x) => x.id === vars[1]);
      if (p === undefined) return json(route, 404, { error: { code: "shop.product_not_found", message: "no" } });
      const body = req.postDataJSON() as { variants: (Variant & { expected_stock?: number })[] };
      write(JSON.stringify(body));
      if (state.stockConflict) {
        state.stockConflict = false;
        // Somebody sold two bottles meanwhile.
        p.variants[0] = { ...p.variants[0], stock: 9, available: 7 };
        return json(route, 409, { error: { code: "shop.stock_changed", message: "moved" } });
      }
      p.variants = body.variants.map((v, i) => variant(v.id ?? `nv${i}`, v.title || "Default Title", v.price_minor, v.stock, { position: i, option1: v.option1, option2: v.option2, option3: v.option3, track_stock: v.track_stock }));
      return json(route, 200, p);
    }

    if (path === "/shop/orders" && m === "GET") {
      state.orderQueries.push(url.search);
      const status = url.searchParams.get("status");
      const q = (url.searchParams.get("q") ?? "").toLowerCase();
      const rows = state.orders.filter((o) => (status === null || o.status === status) && (q === "" || o.buyer.name.toLowerCase().includes(q) || o.buyer.email.includes(q) || String(o.number) === q));
      return json(route, 200, { orders: rows, next_offset: null });
    }
    const ord = /^\/shop\/orders\/([^/]+)(\/[a-z-]+)?$/.exec(path);
    if (ord !== null) {
      const o = state.orders.find((x) => x.id === ord[1]);
      if (o === undefined) return json(route, 404, { error: { code: "shop.order_not_found", message: "no" } });
      const action = ord[2];
      if (action === undefined) return json(route, 200, o);
      if (action === "/courier-text") return route.fulfill({ status: 200, contentType: "text/plain", headers: CORS, body: "Anna Smith\n+34 600 111 222\nMadrid\nCalle Mayor 1, 2B" });
      const body = (req.postDataJSON() ?? {}) as Record<string, unknown>;
      if (action === "/ship") {
        write(JSON.stringify(body));
        Object.assign(o, { status: "shipped", shipped_at: "2026-10-06T09:00:00Z", tracking_note: String(body.tracking_note ?? "") });
        return json(route, 200, o);
      }
      if (action === "/cancel" || action === "/refund") {
        write(`${req.headers()["x-step-up-token"] ?? "NO-TOKEN"} ${JSON.stringify(body)}`);
        if (req.headers()["x-step-up-token"] !== "su-token") return json(route, 403, { error: { code: "auth.step_up_required", message: "password" } });
        if (action === "/cancel") {
          Object.assign(o, { status: "cancelled", cancelled_at: "2026-10-06T09:00:00Z" });
          return json(route, 200, o);
        }
        const amount = typeof body.amount_minor === "number" ? body.amount_minor : o.total - o.refunded_minor;
        o.refunded_minor += amount;
        o.status = o.refunded_minor >= o.total ? "refunded" : "partially_refunded";
        return json(route, 200, { order: o, refund: { id: "rf1", state: "succeeded", amount, currency: "EUR" } });
      }
    }
    return route.fallback();
  });
  return state;
}
