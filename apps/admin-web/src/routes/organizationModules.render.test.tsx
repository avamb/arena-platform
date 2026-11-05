/**
 * The "Modules" tab of the organization drawer. The admin-web Vitest environment is Node-only (no jsdom), so the
 * query-free presentational parts are rendered with renderToStaticMarkup.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import { describe, expect, it } from "vitest";
import {
  ConnectFormBody,
  DomainFormBody,
  EndFormBody,
  PeriodsTable,
  Problems,
  RepriceFormBody,
  ShopModuleView,
  stateText,
} from "@/routes/organizationModules";
import {
  emptyConnectForm,
  emptyEndForm,
  emptyRepriceForm,
  type AdminOrgModulesResponse,
  type OrgModulePeriod,
} from "@/lib/api/orgModules";

const NOW = new Date("2026-11-06T10:00:00Z");
const noop = (): void => undefined;

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
    reason: "Pilot client",
    ...over,
  } as OrgModulePeriod;
}

function data(items: OrgModulePeriod[], shop: Partial<AdminOrgModulesResponse["shop"]> = {}): AdminOrgModulesResponse {
  return {
    org_id: "o1",
    modules: items.some((p) => p.status === "in_force") ? ["shop"] : [],
    items,
    shop: {
      mode: "full",
      module_active: true,
      open_shop_orders: false,
      enabled: true,
      shop_domain: "shop.example.com",
      effective_monthly_minor: 0,
      currency: "EUR",
      price_source: "price_list",
      ...shop,
    },
  } as AdminOrgModulesResponse;
}

function view(d: AdminOrgModulesResponse, notice: string | null = null): string {
  return renderToStaticMarkup(createElement(ShopModuleView, { data: d, now: NOW, notice, onAction: noop }));
}

describe("ShopModuleView", () => {
  it("connected: state, storefront, domain, price, and the change and disconnect actions", () => {
    const html = view(data([period({})]));
    expect(html).toContain("Connected since 2026-10-01");
    expect(html).toContain("Full shop");
    expect(html).toContain("Open");
    expect(html).toContain("shop.example.com");
    expect(html).toContain("Free (price list)");
    expect(html).toContain('data-testid="org-module-reprice"');
    expect(html).toContain('data-testid="org-module-end"');
    expect(html).not.toContain('data-testid="org-module-connect"');
  });

  it("never connected: only Connect, and the domain is closed", () => {
    const html = view(data([], { mode: null, module_active: false, enabled: false, shop_domain: null, effective_monthly_minor: null, currency: null, price_source: null }));
    expect(html).toContain("Never connected");
    expect(html).toContain("No shop section");
    expect(html).toContain("Closed");
    expect(html).toContain("Not set");
    expect(html).toContain("Not known");
    expect(html).toContain('data-testid="org-module-connect"');
    expect(html).not.toContain('data-testid="org-module-reprice"');
    expect(html).not.toContain('data-testid="org-module-end"');
    expect(html).toMatch(/disabled=""[^>]*data-testid="org-module-domain-edit"/);
    expect(html).toContain("The shop module was never connected for this organization.");
  });

  it("disconnected: since the day after the last day, orders only, Connect again", () => {
    const html = view(data([period({ ends_on: "2026-10-31", status: "ended" })], { mode: "orders_only", module_active: false, enabled: false, open_shop_orders: true }));
    expect(html).toContain("Disconnected since 2026-11-01");
    expect(html).toContain("Orders only");
    expect(html).toContain("open shop orders");
    expect(html).toContain('data-testid="org-module-connect"');
    expect(html).not.toContain('data-testid="org-module-end"');
  });

  it("shows an individual price and the notice", () => {
    const html = view(
      data([period({ monthly_price_minor: 1900, currency: "EUR" })], { effective_monthly_minor: 1900, price_source: "individual" }),
      "The new price applies from 2026-11-06.",
    );
    expect(html).toContain("19.00 EUR (individual price)");
    expect(html).toContain("The new price applies from 2026-11-06.");
  });

  it("enables the domain button while the module is connected", () => {
    const html = view(data([period({})]));
    expect(html).not.toMatch(/disabled=""[^>]*data-testid="org-module-domain-edit"/);
  });
});

describe("stateText", () => {
  it("names the last day when there is one", () => {
    expect(stateText([period({ ends_on: "2026-12-31" })])).toBe("Connected since 2026-10-01, the last day is 2026-12-31");
    expect(stateText([period({ starts_on: "2026-12-01", status: "scheduled" })])).toBe("Connects on 2026-12-01");
  });
});

describe("PeriodsTable", () => {
  it("lists the periods oldest first with price, state and reason", () => {
    const html = renderToStaticMarkup(
      createElement(PeriodsTable, {
        items: [
          period({ id: "b", starts_on: "2026-06-01", status: "in_force", monthly_price_minor: 0, currency: "EUR", reason: "Second" }),
          period({ id: "a", starts_on: "2026-01-01", ends_on: "2026-05-31", status: "ended", reason: "First" }),
        ],
      }),
    );
    expect(html.indexOf("First")).toBeGreaterThan(-1);
    expect(html.indexOf("First")).toBeLessThan(html.indexOf("Second"));
    expect(html).toContain("2026-05-31");
    expect(html).toContain("No end");
    expect(html).toContain("Free");
    expect(html).toContain("Price list");
    expect(html).toContain("In force");
    expect(html).toContain("Ended");
  });
});

describe("the forms", () => {
  it("connect: today by default, three price choices, a reason field", () => {
    const html = renderToStaticMarkup(createElement(ConnectFormBody, { form: emptyConnectForm(NOW), onChange: noop }));
    expect(html).toContain('value="2026-11-06"');
    expect(html).toContain("Price list");
    expect(html).toContain("Individual price");
    expect(html).toContain("Free");
    expect(html).toContain('data-testid="org-module-reason"');
    expect(html).not.toContain('data-testid="org-module-amount"');
  });

  it("connect with an individual price asks for the amount and the currency", () => {
    const html = renderToStaticMarkup(createElement(ConnectFormBody, { form: { ...emptyConnectForm(NOW), pricing: "individual" }, onChange: noop }));
    expect(html).toContain('data-testid="org-module-amount"');
    expect(html).toContain('data-testid="org-module-currency"');
    expect(html).toContain("billing currency");
  });

  it("reprice: today by default, with the price choices and a reason", () => {
    const html = renderToStaticMarkup(createElement(RepriceFormBody, { form: emptyRepriceForm({ starts_on: "2026-10-01" }, NOW), onChange: noop }));
    expect(html).toContain('value="2026-11-06"');
    expect(html).toContain('data-testid="org-module-pricing"');
    expect(html).toContain('data-testid="org-module-reason"');
  });

  it("disconnect: the last day, what happens next, a reason", () => {
    const html = renderToStaticMarkup(createElement(EndFormBody, { form: emptyEndForm(NOW), onChange: noop }));
    expect(html).toContain('value="2026-11-06"');
    expect(html).toContain("orders only");
    expect(html).toContain('data-testid="org-module-reason"');
  });

  it("domain: a field, a reason, and a note that only the domain changes", () => {
    const html = renderToStaticMarkup(createElement(DomainFormBody, { domain: "shop.example.com", reason: "", configured: true, onDomain: noop, onReason: noop }));
    expect(html).toContain('value="shop.example.com"');
    expect(html).toContain("Only the domain changes");
    expect(html).toContain('data-testid="org-module-reason"');
  });

  it("domain: says so instead of overwriting settings the organization never saved", () => {
    const html = renderToStaticMarkup(createElement(DomainFormBody, { domain: "", reason: "", configured: false, onDomain: noop, onReason: noop }));
    expect(html).toContain('data-testid="org-module-domain-unconfigured"');
    expect(html).not.toContain('data-testid="org-module-domain-input"');
  });
});

describe("Problems", () => {
  it("draws nothing without problems, the list when there are some", () => {
    expect(renderToStaticMarkup(createElement(Problems, { errors: [], serverError: null }))).toBe("");
    const html = renderToStaticMarkup(createElement(Problems, { errors: ["Reason: write why."], serverError: "The first day is in the past." }));
    expect(html).toContain('role="alert"');
    expect(html).toContain("Reason: write why.");
    expect(html).toContain("The first day is in the past.");
  });
});
