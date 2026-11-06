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
    expect(navSectionOf(`${ORG}/events`, {})).toBe("events");
    expect(navSectionOf(`${ORG}/events/abc/sessions/def`, {})).toBe("events");
    expect(navSectionOf(`${ORG}/events/new`, {})).toBe("more");
    expect(navSectionOf(`${ORG}/orders`, {})).toBe("orders");
    expect(navSectionOf(`${ORG}/orders`, { tab: "paid" })).toBe("orders");
    expect(navSectionOf(`${ORG}/orders`, { tab: "refunds" })).toBe("refunds");
    expect(navSectionOf(`${ORG}/orders/123`, {})).toBe("orders");
    expect(navSectionOf(`${ORG}/summary`, {})).toBe("summary");
    for (const p of ["/promo", "/invitations", "/promoters", "/links", "/growth", "/notifications", "/team", "/shop", "/billing", "/org-data"]) {
      expect(navSectionOf(`${ORG}${p}`, {}), p).toBe("more");
    }
  });
});

describe("TabBar", () => {
  it("shows exactly five items to who reads orders", async () => {
    const html = await renderInRouter(
      <TabBar orgId="org1" access={access(["order.read"])} shopMode="none" orgCount={1} onLogout={() => undefined} />,
    );
    for (const id of ["nav-events", "nav-orders", "nav-refunds", "nav-summary", "nav-more"]) {
      expect(html, id).toContain(`data-testid="${id}"`);
    }
    expect(html.match(/class="tab-item/g)?.length).toBe(5);
    expect(html).toContain("tab=refunds");
    // "More" is closed: its sections are not on the page yet.
    expect(html).not.toContain('data-testid="nav-promo"');
  });

  it("drops Orders and Refunds without order.read", async () => {
    const html = await renderInRouter(
      <TabBar orgId="org1" access={access([])} shopMode="none" orgCount={1} onLogout={() => undefined} />,
    );
    expect(html).not.toContain('data-testid="nav-orders"');
    expect(html).not.toContain('data-testid="nav-refunds"');
    expect(html).toContain('data-testid="nav-more"');
  });

  it("names the five items in every language", () => {
    for (const locale of LOCALES) {
      for (const key of ["nav.events", "nav.orders", "nav.refunds", "nav.summary", "nav.more"] as const) {
        expect(CATALOGS[locale][key].length, `${locale} ${key}`).toBeGreaterThan(0);
      }
    }
  });
});
