/**
 * Render tests for the Promo codes table. The admin-web Vitest environment
 * is Node-only (no jsdom), so — following the sessionOverview precedent —
 * the router- and query-free presentational table is rendered with
 * renderToStaticMarkup.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  PromoCodesTable,
  type PromoCode,
  type PromoRedemption,
} from "@/routes/promoCodes";

const PERCENT_ID = "11111111-1111-7111-8111-111111111111";
const FIXED_ID = "22222222-2222-7222-8222-222222222222";

const percentCode: PromoCode = {
  id: PERCENT_ID,
  org_id: "org-1",
  code: "SUMMER25",
  discount_type: "percent",
  discount_value: 25,
  currency: null,
  applies_to_tier_ids: [],
  applies_to_session_ids: [],
  uses: 3,
  discount_total: 0,
  last_used_at: null,
  max_uses: 100,
  max_uses_per_customer: 1,
  valid_from: "2026-06-01T00:00:00Z",
  valid_until: "2026-08-31T23:59:00Z",
  min_order_amount: 0,
  status: "active",
  created_at: "2026-05-01T00:00:00Z",
  updated_at: "2026-05-01T00:00:00Z",
};

const fixedCode: PromoCode = {
  id: FIXED_ID,
  org_id: "org-1",
  code: "WELCOME10",
  discount_type: "fixed_amount",
  discount_value: 1000,
  currency: "EUR",
  applies_to_tier_ids: [],
  applies_to_session_ids: ["s1"],
  uses: 5,
  discount_total: 5000,
  last_used_at: "2026-09-20T12:00:00Z",
  max_uses: null,
  max_uses_per_customer: null,
  valid_from: null,
  valid_until: null,
  min_order_amount: 0,
  status: "paused",
  created_at: "2026-05-01T00:00:00Z",
  updated_at: "2026-05-01T00:00:00Z",
};

const redemptions: readonly PromoRedemption[] = [
  {
    id: "r1",
    promo_code_id: FIXED_ID,
    code: "WELCOME10",
    redeemed_at: "2026-09-20T12:00:00Z",
    discount_amount: 1000,
    order_amount: 5000,
    order_id: "order-1",
    order_number: 1000038286,
    order_status: "paid",
    currency: "EUR",
    buyer_email: "buyer@example.test",
    session_id: "s1",
    channel_id: "chan-1",
    channel_name: "Widget",
  },
];

function noop(): void {
  // renderToStaticMarkup never invokes event handlers; these exist only to
  // satisfy the component's prop types.
}

function markup(overrides: Partial<Parameters<typeof PromoCodesTable>[0]> = {}): string {
  return renderToStaticMarkup(
    <PromoCodesTable
      codes={[percentCode, fixedCode]}
      sessionLabelsById={new Map([["s1", "Gala — 2026-10-13 17:00Z (Main Hall)"]])}
      expandedId={null}
      onToggleUsage={noop}
      onPauseToggle={noop}
      onDelete={noop}
      onDownloadCodeCsv={noop}
      csvBusyId={null}
      redemptions={[]}
      redemptionsLoading={false}
      redemptionsError={null}
      {...overrides}
    />,
  );
}

/** The row of one code, isolated so its cells can be asserted in order. */
function codeRow(html: string, id: string): string {
  const start = html.indexOf(`data-testid="promo-codes-row-${id}"`);
  return html.slice(start, html.indexOf("</tr>", start));
}

describe("PromoCodesTable", () => {
  it("renders the percent code's cells", () => {
    const row = codeRow(markup(), PERCENT_ID);
    expect(row).toContain("SUMMER25");
    expect(row).toContain("25 %");
    expect(row).toContain(">any<"); // Sessions cell: applies to any session
    expect(row).toContain("100 / 1"); // Limits: max_uses / max_uses_per_customer
    expect(row).toContain("2026-06-01 00:00Z – 2026-08-31 23:59Z"); // Valid
    expect(row).toContain(">active<"); // Status badge
    expect(row).toContain(">3<"); // Uses
    expect(row).toContain(">0<"); // Discount total (no currency -> bare number)
    expect(row).toContain(">—<"); // Last used: never
  });

  it("renders a fixed-amount row with its currency in the discount and discount-total cells", () => {
    const row = codeRow(markup(), FIXED_ID);
    expect(row).toContain("10.00 EUR"); // discount
    expect(row).toContain("50.00 EUR"); // discount_total
    expect(row).toContain("1 session"); // sessions cell (applies_to_session_ids: ["s1"])
    expect(row).toContain("∞ / ∞"); // unlimited usage caps
    expect(row).toContain("always"); // no validity window
    expect(row).toContain("paused");
    expect(row).toContain("2026-09-20 12:00Z"); // last used
  });

  it("titles the Sessions cell with the resolved session labels", () => {
    const row = codeRow(markup(), FIXED_ID);
    expect(row).toContain("Gala — 2026-10-13 17:00Z (Main Hall)");
  });

  it("shows the empty state when there are no codes", () => {
    const html = markup({ codes: [] });
    expect(html).toContain('data-testid="promo-codes-empty"');
    expect(html).toContain("No promo codes yet.");
    expect(html).not.toContain("promo-codes-table");
  });

  it("expands the usage panel for the selected code with its redemptions", () => {
    const html = markup({ expandedId: FIXED_ID, redemptions });
    expect(html).toContain(`data-testid="promo-codes-usage-row-${FIXED_ID}"`);
    expect(html).toContain(`data-testid="promo-codes-usage-panel-${FIXED_ID}"`);
    expect(html).toContain("Usage — WELCOME10");
    expect(html).toContain("1000038286");
    expect(html).toContain("paid");
    expect(html).toContain("buyer@example.test");
    expect(html).toContain("Widget");
    expect(html).toContain("50.00 EUR");
    expect(html).toContain("10.00 EUR");
    // Only the expanded code gets a usage row.
    expect(html).not.toContain(`data-testid="promo-codes-usage-row-${PERCENT_ID}"`);
  });

  it("shows a loading state in the usage panel", () => {
    const html = markup({ expandedId: FIXED_ID, redemptionsLoading: true });
    expect(html).toContain("Loading redemptions…");
  });

  it("shows an error state in the usage panel", () => {
    const html = markup({ expandedId: FIXED_ID, redemptionsError: "network down" });
    expect(html).toContain(`data-testid="promo-codes-usage-error-${FIXED_ID}"`);
    expect(html).toContain("network down");
  });

  it("shows an honest empty state when a code has no redemptions yet", () => {
    const html = markup({ expandedId: PERCENT_ID, redemptions: [] });
    expect(html).toContain(`data-testid="promo-codes-usage-empty-${PERCENT_ID}"`);
    expect(html).toContain("has not been redeemed yet");
  });
});
