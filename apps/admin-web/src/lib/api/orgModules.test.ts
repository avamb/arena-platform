/**
 * The shop module of an organization, superadmin side: the forms are checked before they are sent (reason
 * required, dates only forward, the price in minor units), the state is read from the periods, and the domain
 * goes through the settings PUT without wiping any other field.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { __TEST_ONLY_resetTokenStore, setSession } from "@/lib/api/tokenStore";
import { ApiError } from "@/lib/api/client";
import {
  addDays,
  buildConnectRequest,
  buildEndRequest,
  buildRepriceRequest,
  canConnect,
  canEnd,
  canReprice,
  checkDomain,
  connectShopModule,
  currentPeriod,
  domainUpdateBody,
  effectivePriceLabel,
  emptyConnectForm,
  emptyEndForm,
  emptyRepriceForm,
  moduleErrorMessage,
  priceLabel,
  shopPeriodState,
  todayUTC,
  type ConnectForm,
  type OrgModulePeriod,
  type ShopSettings,
} from "@/lib/api/orgModules";

const NOW = new Date("2026-11-06T10:00:00Z");

function period(over: Partial<OrgModulePeriod>): OrgModulePeriod {
  return {
    id: "p1",
    org_id: "o1",
    module: "shop",
    starts_on: "2026-10-01",
    ends_on: null,
    monthly_price_minor: null,
    currency: null,
    status: "in_force",
    created_at: "2026-10-01T00:00:00Z",
    ended_at: null,
    reason: "test",
    ...over,
  } as OrgModulePeriod;
}

function connectForm(over: Partial<ConnectForm> = {}): ConnectForm {
  return { ...emptyConnectForm(NOW), reason: "Pilot client", ...over };
}

describe("days", () => {
  it("counts UTC days", () => {
    expect(todayUTC(NOW)).toBe("2026-11-06");
    expect(addDays("2026-11-06", -1)).toBe("2026-11-05");
    expect(addDays("2026-12-31", 1)).toBe("2027-01-01");
    expect(addDays("2026-03-01", -1)).toBe("2026-02-28");
  });
});

describe("buildConnectRequest", () => {
  it("starts today by default and takes the price list without a price", () => {
    const form = connectForm();
    expect(form.startsOn).toBe("2026-11-06");
    const built = buildConnectRequest(form, NOW);
    expect(built).toEqual({ ok: true, reason: "Pilot client", request: { starts_on: "2026-11-06", reason: "Pilot client" } });
  });

  it("sends 0 for free and no currency", () => {
    const built = buildConnectRequest(connectForm({ pricing: "free", currency: "eur" }), NOW);
    expect(built.ok && built.request).toMatchObject({ monthly_price_minor: 0 });
    expect(built.ok && "currency" in built.request).toBe(false);
  });

  it("turns an individual price into minor units and upper-cases the currency", () => {
    const built = buildConnectRequest(connectForm({ pricing: "individual", amount: "19,90", currency: "eur" }), NOW);
    expect(built.ok && built.request).toMatchObject({ monthly_price_minor: 1990, currency: "EUR" });
    const noCurrency = buildConnectRequest(connectForm({ pricing: "individual", amount: "19" }), NOW);
    expect(noCurrency.ok && noCurrency.request).toMatchObject({ monthly_price_minor: 1900 });
    expect(noCurrency.ok && "currency" in noCurrency.request).toBe(false);
  });

  it("needs a reason", () => {
    for (const reason of ["", "   "]) {
      const built = buildConnectRequest(connectForm({ reason }), NOW);
      expect(built.ok).toBe(false);
      expect(!built.ok && built.errors.join(" ")).toContain("Reason");
    }
  });

  it("refuses a first day in the past before sending", () => {
    const built = buildConnectRequest(connectForm({ startsOn: "2026-11-05" }), NOW);
    expect(built.ok).toBe(false);
    expect(!built.ok && built.errors.join(" ")).toContain("First day");
    expect(buildConnectRequest(connectForm({ startsOn: "2026-11-06" }), NOW).ok).toBe(true);
    expect(buildConnectRequest(connectForm({ startsOn: "2027-01-01" }), NOW).ok).toBe(true);
  });

  it("refuses a missing or wrong date, price and currency", () => {
    expect(buildConnectRequest(connectForm({ startsOn: "" }), NOW).ok).toBe(false);
    expect(buildConnectRequest(connectForm({ startsOn: "06.11.2026" }), NOW).ok).toBe(false);
    for (const amount of ["", "abc", "-5", "0", "0.00", "1.234", "1e3"]) {
      expect(buildConnectRequest(connectForm({ pricing: "individual", amount }), NOW).ok, amount).toBe(false);
    }
    expect(buildConnectRequest(connectForm({ pricing: "individual", amount: "10", currency: "EURO" }), NOW).ok).toBe(false);
    expect(buildConnectRequest(connectForm({ pricing: "individual", amount: "10", currency: "E1" }), NOW).ok).toBe(false);
  });

  it("collects every problem at once", () => {
    const built = buildConnectRequest(connectForm({ startsOn: "2020-01-01", pricing: "individual", amount: "x", reason: "" }), NOW);
    expect(!built.ok && built.errors).toHaveLength(3);
  });
});

describe("buildRepriceRequest", () => {
  const p = period({ starts_on: "2026-10-01" });

  it("starts today by default, or the day the period starts when that is later", () => {
    expect(emptyRepriceForm(p, NOW).from).toBe("2026-11-06");
    expect(emptyRepriceForm(period({ starts_on: "2027-01-01" }), NOW).from).toBe("2027-01-01");
  });

  it("builds the request with the price and the reason", () => {
    const form = { ...emptyRepriceForm(p, NOW), pricing: "individual" as const, amount: "25", currency: "usd", reason: "Annual deal" };
    expect(buildRepriceRequest(form, p, NOW)).toEqual({
      ok: true,
      reason: "Annual deal",
      request: { from: "2026-11-06", monthly_price_minor: 2500, currency: "USD", reason: "Annual deal" },
    });
  });

  it("returns to the price list when asked, and needs a reason", () => {
    const form = { ...emptyRepriceForm(p, NOW), reason: "Back to list" };
    const built = buildRepriceRequest(form, p, NOW);
    expect(built.ok && "monthly_price_minor" in built.request).toBe(false);
    expect(buildRepriceRequest({ ...form, reason: " " }, p, NOW).ok).toBe(false);
  });

  it("refuses a day in the past or before the period starts", () => {
    const form = { ...emptyRepriceForm(p, NOW), reason: "x" };
    const past = buildRepriceRequest({ ...form, from: "2026-11-05" }, p, NOW);
    expect(!past.ok && past.errors.join(" ")).toContain("From");
    const early = buildRepriceRequest({ ...form, from: "2026-11-06" }, period({ starts_on: "2026-12-01" }), NOW);
    expect(!early.ok && early.errors.join(" ")).toContain("2026-12-01");
  });
});

describe("buildEndRequest", () => {
  const p = period({ starts_on: "2026-10-01" });

  it("ends today by default and needs a reason", () => {
    const form = emptyEndForm(NOW);
    expect(form.endsOn).toBe("2026-11-06");
    expect(buildEndRequest(form, p, NOW).ok).toBe(false);
    expect(buildEndRequest({ ...form, reason: "Client left" }, p, NOW)).toEqual({
      ok: true,
      reason: "Client left",
      request: { ends_on: "2026-11-06", reason: "Client left" },
    });
  });

  it("takes yesterday but not the day before", () => {
    expect(buildEndRequest({ endsOn: "2026-11-05", reason: "x" }, p, NOW).ok).toBe(true);
    const old = buildEndRequest({ endsOn: "2026-11-04", reason: "x" }, p, NOW);
    expect(!old.ok && old.errors.join(" ")).toContain("Days that we charged");
  });

  it("refuses an empty or malformed day", () => {
    expect(buildEndRequest({ endsOn: "", reason: "x" }, p, NOW).ok).toBe(false);
    expect(buildEndRequest({ endsOn: "tomorrow", reason: "x" }, p, NOW).ok).toBe(false);
  });
});

describe("the state of the shop module", () => {
  it("never connected", () => {
    expect(shopPeriodState([])).toEqual({ kind: "never" });
    expect(canConnect([])).toBe(true);
    expect(canReprice([])).toBe(false);
    expect(canEnd([], NOW)).toBe(false);
  });

  it("connected since the first day of a run of periods that continue each other", () => {
    const items = [
      period({ id: "a", starts_on: "2026-01-01", ends_on: "2026-05-31", status: "ended" }),
      period({ id: "b", starts_on: "2026-06-01", ends_on: null, status: "in_force", monthly_price_minor: 0, currency: "EUR" }),
    ];
    expect(shopPeriodState(items)).toEqual({ kind: "connected", since: "2026-01-01", until: null });
    expect(currentPeriod(items)?.id).toBe("b");
    expect(canConnect(items)).toBe(false);
    expect(canReprice(items)).toBe(true);
    expect(canEnd(items, NOW)).toBe(true);
  });

  it("a gap between periods starts a new connection", () => {
    const items = [
      period({ id: "a", starts_on: "2026-01-01", ends_on: "2026-03-31", status: "ended" }),
      period({ id: "b", starts_on: "2026-06-01", status: "in_force" }),
    ];
    expect(shopPeriodState(items)).toEqual({ kind: "connected", since: "2026-06-01", until: null });
  });

  it("disconnected since the day after the last day", () => {
    const items = [period({ starts_on: "2026-01-01", ends_on: "2026-03-31", status: "ended" })];
    expect(shopPeriodState(items)).toEqual({ kind: "disconnected", since: "2026-04-01", lastDay: "2026-03-31" });
    expect(canConnect(items)).toBe(true);
    expect(canReprice(items)).toBe(false);
  });

  it("scheduled to start later", () => {
    const items = [period({ starts_on: "2026-12-01", status: "scheduled" })];
    expect(shopPeriodState(items)).toEqual({ kind: "scheduled", from: "2026-12-01", until: null });
    expect(canReprice(items)).toBe(true);
    expect(canConnect(items)).toBe(false);
  });

  it("a period that already has its last day cannot be disconnected again", () => {
    expect(canEnd([period({ ends_on: "2026-11-05" })], NOW)).toBe(false);
    expect(canEnd([period({ ends_on: "2026-11-30" })], NOW)).toBe(true);
  });

  it("reads other modules as none", () => {
    expect(shopPeriodState([period({ module: "other" })])).toEqual({ kind: "never" });
  });
});

describe("labels", () => {
  it("names the price", () => {
    expect(priceLabel(null)).toBe("Price list");
    expect(priceLabel(0)).toBe("Free");
    expect(priceLabel(1990, "EUR")).toBe("19.90 EUR");
    expect(effectivePriceLabel({ effective_monthly_minor: 0, currency: "EUR", price_source: "price_list" })).toBe("Free (price list)");
    expect(effectivePriceLabel({ effective_monthly_minor: 1900, currency: "EUR", price_source: "individual" })).toBe("19.00 EUR (individual price)");
    expect(effectivePriceLabel({ effective_monthly_minor: null, currency: null, price_source: null })).toBe("Not known");
  });
});

describe("moduleErrorMessage", () => {
  const err = (status: number, code: string) => new ApiError(status, { code, message: "raw" });

  it("explains a day in the past, for each of the three routes", () => {
    expect(moduleErrorMessage(err(422, "org_module.starts_on_in_past"))).toContain("first day is before today");
    expect(moduleErrorMessage(err(422, "org_module.from_in_past"))).toContain("new price cannot start before today");
    expect(moduleErrorMessage(err(422, "org_module.ends_on_in_past"))).toContain("last day is before yesterday");
  });

  it("explains an overlap and the rest", () => {
    expect(moduleErrorMessage(err(409, "org_module.overlap"))).toContain("has the module");
    expect(moduleErrorMessage(err(409, "org_module.already_ended"))).toContain("ends on or before");
    expect(moduleErrorMessage(err(403, "org_module.superadmin_only"))).toContain("platform superadmin");
    expect(moduleErrorMessage(err(409, "shop.duplicate_domain"))).toContain("A different organization");
    expect(moduleErrorMessage(err(409, "shop.settings_changed"))).toContain("A different person saved");
  });

  it("falls back to the server's message, or a plain one", () => {
    expect(moduleErrorMessage(err(500, "org_module.failed"))).toBe("raw");
    expect(moduleErrorMessage(new Error("boom"))).toBe("boom");
    expect(moduleErrorMessage("x")).toContain("did not complete");
  });
});

describe("the domain", () => {
  const settings = {
    org_id: "o1",
    configured: true,
    currency: "EUR",
    shipping_fee_minor: 490,
    shipping_label: "Courier",
    country_iso2: "ES",
    age_gate_min_age: 18,
    announcement_text: "Hello",
    contact_email: "shop@example.com",
    logistics_note: "Note",
    sales_channel_id: "11111111-1111-4111-8111-111111111111",
    shop_domain: "old.example.com",
    max_line_qty: 12,
    enabled: true,
    updated_at: "2026-11-01T10:00:00.000001Z",
    module_active: true,
    shop_mode: "full",
    can_open: true,
    open_blockers: [],
  } as ShopSettings;

  it("sends every other field back as it was read, so the PUT wipes nothing", () => {
    const body = domainUpdateBody(settings, " shop.example.com ");
    expect(body).toEqual({
      currency: "EUR",
      shipping_fee_minor: 490,
      shipping_label: "Courier",
      country_iso2: "ES",
      age_gate_min_age: 18,
      announcement_text: "Hello",
      contact_email: "shop@example.com",
      logistics_note: "Note",
      sales_channel_id: "11111111-1111-4111-8111-111111111111",
      shop_domain: "shop.example.com",
      max_line_qty: 12,
      expected_updated_at: "2026-11-01T10:00:00.000001Z",
    });
  });

  it("leaves the storefront switch alone and clears the domain with an empty string", () => {
    const body = domainUpdateBody({ ...settings, country_iso2: null, contact_email: null, sales_channel_id: null, age_gate_min_age: null }, "");
    expect("enabled" in body).toBe(false);
    expect(body.shop_domain).toBe("");
    expect(body).toMatchObject({ country_iso2: null, contact_email: null, sales_channel_id: null, age_gate_min_age: null });
  });

  it("checks the host name", () => {
    expect(checkDomain("")).toBeNull();
    expect(checkDomain("shop.example.com")).toBeNull();
    expect(checkDomain("a-b.example.co.uk")).toBeNull();
    for (const bad of ["https://shop.example.com", "shop.example.com/path", "shop", "shop example.com", "-x.example.com", "shop..example.com"]) {
      expect(checkDomain(bad), bad).not.toBeNull();
    }
  });
});

describe("a write carries the reason the person wrote", () => {
  beforeEach(() => {
    __TEST_ONLY_resetTokenStore();
    setSession({ accessToken: "t", refreshToken: "r", expiresAt: new Date(Date.now() + 3_600_000).toISOString(), userId: "u" });
  });
  afterEach(() => {
    __TEST_ONLY_resetTokenStore();
    vi.restoreAllMocks();
  });

  it("sends X-Admin-Reason of the dialog on the connect call, without asking the session for one", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "p", starts_on: "2026-11-06" }), { status: 201, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await connectShopModule("org-1", { starts_on: "2026-11-06", reason: "Pilot" }, "Pilot");
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/v1/admin/organizations/org-1/modules/shop");
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>)["X-Admin-Reason"]).toBe("Pilot");
    expect(JSON.parse(String(init.body))).toEqual({ starts_on: "2026-11-06", reason: "Pilot" });
  });
});
