import { describe, expect, it } from "vitest";
import { renderInRouter } from "@/test-utils/renderInRouter";
import type { OrgAccess } from "@/lib/auth";
import { CATALOGS, LOCALES } from "@/lib/i18n";
import { TabBar, navSectionOf } from "@/components/AppNav";

const ORG = "/o/0193f01a-1234-7000-8000-000000000001";

function access(perms: string[]): OrgAccess {
  const set = new Set(perms);
  return { data: undefined, isLoading: false, error: null, can: (p) => set.has(p) };
}

describe("navSectionOf", () => {
  it("maps every location onto one of the five items", () => {
    expect(navSectionOf(`${ORG}`, {})).toBe("events");
    expect(navSectionOf(`${ORG}/events`, {})).toBe("events");
    expect(navSectionOf(`${ORG}/events/abc/sessions/def`, {})).toBe("events");
    expect(navSectionOf(`${ORG}/events/abc/sessions/def/invitations`, {})).toBe("events");
    expect(navSectionOf(`${ORG}/events/new`, {})).toBe("menu");
    expect(navSectionOf(`${ORG}/summary`, {})).toBe("events");
    expect(navSectionOf(`${ORG}/orders`, {})).toBe("orders");
    expect(navSectionOf(`${ORG}/orders`, { tab: "refunds" })).toBe("orders");
    expect(navSectionOf(`${ORG}/orders/123`, {})).toBe("orders");
    expect(navSectionOf(`${ORG}/refunds`, {})).toBe("orders");
    for (const p of ["/promo", "/promo/new", "/promo/abc", "/invitations", "/links", "/links/new", "/links/abc", "/growth"]) {
      expect(navSectionOf(`${ORG}${p}`, {}), p).toBe("promotion");
    }
    expect(navSectionOf(`${ORG}/entrance`, {})).toBe("entrance");
    for (const p of ["/help", "/promoters", "/promoters/abc", "/venues", "/notifications", "/team", "/shop", "/shop/orders", "/billing", "/org-data"]) {
      expect(navSectionOf(`${ORG}${p}`, {}), p).toBe("menu");
    }
  });
});

describe("TabBar", () => {
  const bar = (perms: string[]) =>
    renderInRouter(<TabBar orgId="org1" access={access(perms)} shopMode="none" orgCount={1} onLogout={() => undefined} />);

  it("shows exactly five items to who reads orders and promotion", async () => {
    const html = await bar(["order.read", "refund.read", "promo.read"]);
    for (const id of ["nav-events", "nav-orders", "nav-promotion", "nav-entrance", "nav-more"]) {
      expect(html, id).toContain(`data-testid="${id}"`);
    }
    expect(html.match(/class="tab-item/g)?.length).toBe(5);
    // Orders goes to the list. Refunds and Summary are in the Menu sheet now.
    expect(html).toContain("/o/org1/orders");
    expect(html).toContain("/o/org1/entrance");
    expect(html).not.toContain('data-testid="nav-refunds"');
    expect(html).not.toContain('data-testid="nav-summary"');
    // The sheets are closed: their items are not on the page yet.
    expect(html).not.toContain('data-testid="nav-promo"');
    expect(html).not.toContain('data-testid="more-sheet"');
  });

  it("drops Orders without order.read and Promotion with no promotion item", async () => {
    const html = await bar([]);
    expect(html).not.toContain('data-testid="nav-orders"');
    expect(html).not.toContain('data-testid="nav-promotion"');
    expect(html).toContain('data-testid="nav-events"');
    expect(html).toContain('data-testid="nav-entrance"');
    expect(html).toContain('data-testid="nav-more"');
    expect(html.match(/class="tab-item/g)?.length).toBe(3);
  });

  it("makes the second item follow the role: Orders, Refunds or none", async () => {
    const both = await bar(["order.read", "refund.read"]);
    expect(both).toContain('data-testid="nav-orders"');
    expect(both).not.toContain('data-testid="nav-refunds"');
    const refundsOnly = await bar(["refund.read"]);
    expect(refundsOnly).not.toContain('data-testid="nav-orders"');
    expect(refundsOnly).toContain('data-testid="nav-refunds"');
    expect(refundsOnly).toContain("/o/org1/refunds");
    expect(refundsOnly.match(/class="tab-item/g)?.length).toBe(4);
    const neither = await bar([]);
    expect(neither).not.toContain('data-testid="nav-orders"');
    expect(neither).not.toContain('data-testid="nav-refunds"');
  });

  it("shows Promotion to who has only free tickets", async () => {
    const html = await bar(["complimentary.read"]);
    expect(html).toContain('data-testid="nav-promotion"');
  });

  it("names the five items in every language", () => {
    for (const locale of LOCALES) {
      for (const key of ["nav.events", "nav.orders", "nav.promotion", "nav.entrance", "nav.more"] as const) {
        expect(CATALOGS[locale][key].length, `${locale} ${key}`).toBeGreaterThan(0);
      }
    }
  });
});
