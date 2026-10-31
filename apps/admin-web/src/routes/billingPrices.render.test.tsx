/**
 * Render tests for "Price lists and rates" (B1-5). The admin-web Vitest
 * environment is Node-only (no jsdom), so — as for the Onboarding screens —
 * the router- and query-free presentational parts are rendered with
 * renderToStaticMarkup.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { createElement } from "react";
import { VersionDetailBody, VersionFormBody, VersionsTable } from "@/routes/billingPricesLists";
import { AccountsTable, RateFormBody, RatesTable, TermsFormBody } from "@/routes/billingPricesRates";
import {
  emptyRateForm,
  emptyRule,
  emptyVersionForm,
  groupSeries,
  termsFormFrom,
  type BillingAccount,
  type PriceListDetail,
  type PriceListVersion,
  type RateOverride,
} from "@/lib/api/billingPrices";

const noop = (): void => undefined;

function version(over: Partial<PriceListVersion>): PriceListVersion {
  return {
    id: "00000000-0000-7000-8000-000000000001",
    code: "b15-test",
    version: 1,
    currency: "EUR",
    effective_from: "2026-11-01T00:00:00Z",
    revoked_at: null,
    locked: false,
    free_tickets_annual_limit: null,
    floor_per_ticket_minor: null,
    bonus_share_cap_bp: 3000,
    bonus_covers_surcharge: false,
    created_by: null,
    reason: "test reason",
    created_at: "2026-10-17T00:00:00Z",
    status: "scheduled",
    rules_count: 2,
    packs_count: 1,
    ...over,
  };
}

describe("VersionsTable", () => {
  const items = [
    version({ id: "a", version: 1, status: "in_force", effective_from: "2026-01-01T00:00:00Z" }),
    version({ id: "b", version: 2, status: "scheduled" }),
    version({ id: "c", version: 3, status: "revoked", revoked_at: "2026-10-17T01:00:00Z" }),
  ];
  const html = renderToStaticMarkup(
    createElement(VersionsTable, { series: groupSeries(items), onOpen: noop, onRevoke: noop, onNewVersion: noop }),
  );

  it("shows one block per price list with a status pill per version", () => {
    expect(html).toContain("b15-test");
    expect(html).toContain("In force");
    expect(html).toContain("Scheduled");
    expect(html).toContain("Revoked");
    expect(html).toContain("2026-11-01 00:00Z");
  });

  it("offers Revoke on the scheduled version only", () => {
    expect((html.match(/data-testid="billing-version-revoke"/g) ?? []).length).toBe(1);
    const scheduledRow = html.slice(html.indexOf('data-testid="billing-version-2"'), html.indexOf('data-testid="billing-version-1"'));
    expect(scheduledRow).toContain("billing-version-revoke");
  });

  it("says so when there is nothing", () => {
    expect(renderToStaticMarkup(createElement(VersionsTable, { series: [], onOpen: noop, onRevoke: noop, onNewVersion: noop }))).toContain(
      "No price list yet",
    );
  });
});

describe("VersionDetailBody", () => {
  const detail = {
    ...version({ status: "in_force" }),
    free_tickets_annual_limit: 12,
    floor_per_ticket_minor: 9,
    rules: [
      {
        id: "r1",
        rule_type: "fixed_per_ticket",
        line: "standard",
        event_kind: "ticket_sold",
        sale_currency: null,
        params: { unseated_minor: 71, seated_minor: 133 },
        sort_order: 10,
      },
      {
        id: "r2",
        rule_type: "percent_of_price_with_cap",
        line: "venue",
        event_kind: "ticket_sold",
        sale_currency: null,
        params: { pct_bp: 150, cap_minor: 300 },
        sort_order: 20,
      },
    ],
    packs: [
      { id: "p1", tier: 1, currency: "EUR", face_minor: 1000, unseated_rate_minor: 50, seated_surcharge_minor: 30, active: true, sort_order: 1 },
    ],
  } as PriceListDetail;
  const html = renderToStaticMarkup(createElement(VersionDetailBody, { detail }));

  it("prints amounts as money next to the minor units and percentages as percent", () => {
    expect(html).toContain("0.71 EUR (71)");
    expect(html).toContain("1.33 EUR (133)");
    expect(html).toContain("1.5%");
    expect(html).toContain("3.00 EUR (300)");
    expect(html).toContain("10.00 EUR");
    expect(html).toContain("Fixed per ticket");
  });

  it("shows the version parameters", () => {
    expect(html).toContain("Bonus share cap 30%");
    expect(html).toContain("free");
    expect(html).toContain("0.09 EUR");
    expect(html).toContain("test reason");
  });
});

describe("VersionFormBody", () => {
  it("starts with no number filled in", () => {
    const html = renderToStaticMarkup(
      createElement(VersionFormBody, {
        form: emptyVersionForm(),
        onChange: noop,
        errors: [],
        bases: [],
        onPickBase: noop,
        onAddRule: noop,
        onAddPack: noop,
      }),
    );
    expect(html).toContain("nothing is filled in for you");
    expect(html).toContain('data-testid="billing-add-rule-fixed_per_ticket"');
    expect(html).not.toMatch(/value="\d/);
  });

  it("shows a rule with its parameters, a live money preview and the errors", () => {
    const rule = { ...emptyRule("fixed_per_ticket", "EUR"), params: { unseated_minor: "71", seated_minor: "" } };
    const html = renderToStaticMarkup(
      createElement(VersionFormBody, {
        form: { ...emptyVersionForm("code-a", "EUR"), rules: [rule] },
        onChange: noop,
        errors: ["Start: it must be in the future."],
        bases: [version({ id: "x" })],
        onPickBase: noop,
        onAddRule: noop,
        onAddPack: noop,
      }),
    );
    expect(html).toContain("= 0.71 EUR");
    expect(html).toContain("Ticket without a seat");
    expect(html).toContain("Start: it must be in the future.");
    expect(html).toContain("b15-test v1");
  });
});

const rate = (over: Partial<RateOverride>): RateOverride => ({
  id: "r-1",
  org_id: "o-1",
  org_name: "Acme Events",
  currency: "EUR",
  unseated_minor: 50,
  seated_minor: 100,
  effective_from: "2026-11-01T00:00:00Z",
  effective_to: null,
  note: null,
  created_by: null,
  created_at: "2026-10-17T00:00:00Z",
  status: "scheduled",
  ...over,
});

describe("RatesTable", () => {
  it("offers Set end and Remove only where the rules allow", () => {
    const html = renderToStaticMarkup(
      createElement(RatesTable, {
        rates: [rate({ id: "s", status: "scheduled" }), rate({ id: "f", status: "in_force" }), rate({ id: "e", status: "ended", effective_to: "2026-09-01T00:00:00Z" })],
        onClose: noop,
        onRemove: noop,
      }),
    );
    const row = (id: string): string => {
      const start = html.indexOf(`data-testid="billing-rate-${id}"`);
      return html.slice(start, html.indexOf("</tr>", start));
    };
    expect(row("s")).toContain("billing-rate-close");
    expect(row("s")).toContain("billing-rate-remove");
    expect(row("f")).toContain("billing-rate-close");
    expect(row("f")).not.toContain("billing-rate-remove");
    expect(row("e")).not.toContain("billing-rate-close");
    expect(row("e")).not.toContain("billing-rate-remove");
    expect(html).toContain("0.50 EUR");
    expect(html).toContain("open");
    expect(html).toContain("Acme Events");
  });
});

describe("RateFormBody and TermsFormBody", () => {
  it("lists the organizations and previews the amounts", () => {
    const html = renderToStaticMarkup(
      createElement(RateFormBody, {
        form: { ...emptyRateForm(), currency: "EUR", unseatedMinor: "50" },
        onChange: noop,
        orgs: [{ id: "o-1", name: "Acme Events" }],
        errors: [],
      }),
    );
    expect(html).toContain("Acme Events");
    expect(html).toContain("= 0.50 EUR");
    expect(html).toContain("Starts (UTC)");
  });

  const account = {
    id: "acc-1",
    org_id: "o-1",
    org_name: "Acme Events",
    currency: "EUR",
    issuer_id: "i-1",
    account_kind: "organizer",
    current_terms_id: "t-1",
    flags: { venue: true, tools: false, existing_client: true },
    terms: [
      {
        id: "t-1",
        effective_from: "2026-01-01T00:00:00Z",
        price_list_code: "standard-2026",
        plan_kind: "venue",
        billing_cycle: "monthly",
        shortfall_policy: "invoice_postpaid",
        issuer_id: "i-1",
        invoice_delay_days: null,
        due_days: null,
        locked: false,
        created_by: null,
        reason: null,
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "t-2",
        effective_from: "2026-12-01T00:00:00Z",
        price_list_code: "standard-2026",
        plan_kind: "standard",
        billing_cycle: "monthly",
        shortfall_policy: "invoice_postpaid",
        issuer_id: "i-1",
        invoice_delay_days: null,
        due_days: null,
        locked: false,
        created_by: null,
        reason: null,
        created_at: "2026-10-17T00:00:00Z",
      },
    ],
  } as BillingAccount;

  it("shows the venue, existing-client and tools marks and the next terms", () => {
    const html = renderToStaticMarkup(createElement(AccountsTable, { accounts: [account, { ...account, id: "acc-2", account_kind: "tools", flags: { venue: false, tools: true, existing_client: false } }], onChangeTerms: noop }));
    expect(html).toContain("Permanent venue");
    expect(html).toContain("Existing client");
    expect(html).toContain("Tools");
    expect(html).toContain("2026-12-01 00:00Z: standard");
  });

  it("pre-fills the terms form from the terms in force and says a tools account is prepaid only", () => {
    const html = renderToStaticMarkup(
      createElement(TermsFormBody, {
        account: { ...account, account_kind: "tools" },
        form: termsFormFrom(account.terms[0]),
        onChange: noop,
        priceLists: ["standard-2026"],
        errors: [],
      }),
    );
    expect(html).toContain("tools account");
    expect(html).toContain("prepaid only");
    expect(html).toContain('value="standard-2026" selected');
  });

  it("asks who pays the platform fee and explains buyer pays", () => {
    const base = { account, onChange: noop, priceLists: ["standard-2026"], errors: [] };
    const organizer = renderToStaticMarkup(createElement(TermsFormBody, { ...base, form: termsFormFrom(account.terms[0]) }));
    expect(organizer).toContain("Who pays the platform fee");
    expect(organizer).toContain('value="organizer_pays" selected');
    expect(organizer).toContain("The organizer pays the platform rate");
    const buyer = renderToStaticMarkup(createElement(TermsFormBody, { ...base, form: { ...termsFormFrom(account.terms[0]), feeMode: "buyer_pays" } }));
    expect(buyer).toContain('value="buyer_pays" selected');
    expect(buyer).toContain("The organizer receives the net ticket price");
    expect(buyer).toContain("service charge is the platform commission");
  });

  it("shows the invitations control with the current value and the explanation", () => {
    const base = { account, onChange: noop, priceLists: ["standard-2026"], errors: [] };
    const free = renderToStaticMarkup(createElement(TermsFormBody, { ...base, form: termsFormFrom(account.terms[0]) }));
    expect(free).toContain("Invitations issued by staff");
    expect(free).toContain('value="free" selected');
    expect(free).toContain("A CSV upload is always charged at the standard rate");
    expect(free).toContain("A cancelled invitation keeps its charge");
    expect(free).toContain("applies from the date these terms start");
    const std = renderToStaticMarkup(createElement(TermsFormBody, { ...base, form: { ...termsFormFrom(account.terms[0]), complimentaryBilling: "standard" } }));
    expect(std).toContain('value="standard" selected');
    expect(std).toContain("Standard per-ticket rate");
  });

  it("marks standard-rate invitations in the accounts table", () => {
    const withMode = { ...account, terms: [{ ...account.terms[0], complimentary_billing: "standard" }, account.terms[1]] } as BillingAccount;
    expect(renderToStaticMarkup(createElement(AccountsTable, { accounts: [withMode], onChangeTerms: noop }))).toContain("Invitations: standard rate");
    expect(renderToStaticMarkup(createElement(AccountsTable, { accounts: [account], onChangeTerms: noop }))).not.toContain("Invitations: standard rate");
  });

  it("marks a buyer-pays account in the accounts table", () => {
    const withMode = { ...account, terms: [{ ...account.terms[0], fee_mode: "buyer_pays" }, account.terms[1]] } as BillingAccount;
    const html = renderToStaticMarkup(createElement(AccountsTable, { accounts: [withMode], onChangeTerms: noop }));
    expect(html).toContain("Buyer pays");
  });
});
