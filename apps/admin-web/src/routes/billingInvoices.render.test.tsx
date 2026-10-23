/**
 * Render tests for "Invoices", "Payment fees" and "Bank payments". The
 * admin-web Vitest environment is Node-only (no jsdom), so — as for
 * billingPrices.render.test.tsx — the router- and query-free presentational
 * parts are rendered with renderToStaticMarkup.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { createElement } from "react";
import {
  InvoiceDetailBody,
  InvoicesTable,
  IssueFormBody,
  MarkPaidFormBody,
  UsageFormBody,
  VoidFormBody,
} from "@/routes/billingInvoicesView";
import { BankTable, FeeFormBody, IgnoreFormBody, ImportFormBody, MatchFormBody, PaymentFeesTable } from "@/routes/billingFeesBankView";
import { emptyFeeForm, emptyMarkPaidForm, type BankTransaction, type Invoice, type PaymentFee } from "@/lib/api/billingInvoices";
import type { BillingAccount } from "@/lib/api/billingPrices";

const noop = (): void => undefined;
const orgNames = new Map([["org-1", "Acme Events"]]);

function invoice(over: Partial<Invoice> = {}): Invoice {
  return {
    id: "00000000-0000-7000-8000-000000000001",
    org_id: "org-1",
    account_id: "acc-1",
    kind: "usage",
    state: "issued",
    number: "INV-2026-0001",
    billing_period: "2026-09",
    payment_method: "bank_transfer",
    payment_reference: "ARENA-0001",
    currency: "EUR",
    subtotal_minor: 10000,
    card_fee_minor: 0,
    tax_minor: 0,
    total_minor: 10000,
    period_from: "2026-09-01T00:00:00Z",
    period_to: "2026-10-01T00:00:00Z",
    issued_at: "2026-10-02T00:00:00Z",
    due_at: "2026-10-16T00:00:00Z",
    paid_at: null,
    paid_amount_minor: null,
    paid_via: null,
    voided_at: null,
    payment_url: null,
    pack_id: null,
    lines: [
      {
        id: "l1",
        kind: "rate",
        rule_type: "fixed_per_ticket",
        description: "Tickets sold, September",
        quantity: 100,
        unit_amount_minor: 100,
        total_amount_minor: 10000,
        currency: "EUR",
      },
    ],
    ...over,
  };
}

describe("InvoicesTable", () => {
  const items = [
    invoice({ id: "d", state: "draft", number: null }),
    invoice({ id: "i", state: "issued" }),
    invoice({ id: "p", state: "paid" }),
    invoice({ id: "v", state: "void" }),
  ];
  const html = renderToStaticMarkup(
    createElement(InvoicesTable, { invoices: items, orgNames, onOpen: noop, onIssue: noop, onMarkPaid: noop, onVoid: noop }),
  );
  const row = (id: string): string => {
    const start = html.indexOf(`data-testid="billing-invoice-${id}"`);
    return html.slice(start, html.indexOf("</tr>", start));
  };

  it("shows the organization, the number or a draft label, and the total as money", () => {
    expect(html).toContain("Acme Events");
    expect(html).toContain("INV-2026-0001");
    expect(html).toContain("Draft d<");
    expect(html).toContain("100.00 EUR");
    expect(html).toContain("Bank transfer");
  });

  it("offers each action only in the state where it is allowed", () => {
    expect(row("d")).toContain("billing-invoice-issue");
    expect(row("d")).toContain("billing-invoice-void");
    expect(row("d")).not.toContain("billing-invoice-markpaid");
    expect(row("i")).toContain("billing-invoice-markpaid");
    expect(row("i")).toContain("billing-invoice-void");
    expect(row("i")).not.toContain("billing-invoice-issue");
    for (const id of ["p", "v"]) {
      expect(row(id)).not.toContain("billing-invoice-issue");
      expect(row(id)).not.toContain("billing-invoice-markpaid");
      expect(row(id)).not.toContain("billing-invoice-void");
    }
  });

  it("says so when there is nothing", () => {
    expect(
      renderToStaticMarkup(createElement(InvoicesTable, { invoices: [], orgNames, onOpen: noop, onIssue: noop, onMarkPaid: noop, onVoid: noop })),
    ).toContain("No invoice in this state");
  });
});

describe("InvoiceDetailBody", () => {
  it("prints lines, subtotal and total with the minor units", () => {
    const html = renderToStaticMarkup(createElement(InvoiceDetailBody, { invoice: invoice(), orgName: "Acme Events" }));
    expect(html).toContain("Tickets sold, September");
    expect(html).toContain("1.00 EUR");
    expect(html).toContain("100.00 EUR (10000)");
    expect(html).toContain("ARENA-0001");
    expect(html).not.toContain("billing-invoice-cardfee");
  });

  it("shows the card fee line, the payment page and the net-transfer hint for a card invoice", () => {
    const html = renderToStaticMarkup(
      createElement(InvoiceDetailBody, {
        invoice: invoice({ payment_method: "stripe_card", card_fee_minor: 325, total_minor: 10325, payment_url: "https://pay.example/x" }),
        orgName: "Acme Events",
      }),
    );
    expect(html).toContain("billing-invoice-cardfee");
    expect(html).toContain("3.25 EUR (325)");
    expect(html).toContain("103.25 EUR (10325)");
    expect(html).toContain("https://pay.example/x");
    expect(html).toContain("100.00 EUR");
  });

  it("shows when and how a paid invoice was paid, and a draft without a number", () => {
    const paid = renderToStaticMarkup(
      createElement(InvoiceDetailBody, {
        invoice: invoice({ state: "paid", paid_at: "2026-10-05T10:00:00Z", paid_amount_minor: 10000, paid_via: "bank_transfer" }),
        orgName: "Acme Events",
      }),
    );
    expect(paid).toContain("2026-10-05 10:00Z");
    expect(paid).toContain("via bank_transfer");
    const draft = renderToStaticMarkup(createElement(InvoiceDetailBody, { invoice: invoice({ state: "draft", number: null }), orgName: "Acme Events" }));
    expect(draft).toContain("a draft has no number");
  });
});

describe("invoice forms", () => {
  const account = { id: "acc-1", org_id: "org-1", org_name: "Acme Events", currency: "EUR" } as BillingAccount;

  it("lists the accounts and the month for a usage invoice", () => {
    const html = renderToStaticMarkup(
      createElement(UsageFormBody, { form: { accountId: "", period: "2026-09" }, onChange: noop, accounts: [account], errors: ["Account: pick one."] }),
    );
    expect(html).toContain("Acme Events (EUR)");
    expect(html).toContain('value="2026-09"');
    expect(html).toContain("Account: pick one.");
    expect(html).toContain("DRAFT");
  });

  it("explains the two payment methods when issuing", () => {
    const card = renderToStaticMarkup(createElement(IssueFormBody, { invoice: invoice({ state: "draft" }), orgName: "Acme Events", method: "stripe_card", onChange: noop }));
    expect(card).toContain("average card fee");
    expect(card).toContain("Card (Stripe)");
    const bank = renderToStaticMarkup(createElement(IssueFormBody, { invoice: invoice({ state: "draft" }), orgName: "Acme Events", method: "bank_transfer", onChange: noop }));
    expect(bank).toContain("adds no fee");
  });

  it("names both accepted amounts for a card invoice and previews what is typed", () => {
    const html = renderToStaticMarkup(
      createElement(MarkPaidFormBody, {
        invoice: invoice({ card_fee_minor: 325, total_minor: 10325 }),
        form: { ...emptyMarkPaidForm(), amountMinor: "10000" },
        onChange: noop,
        errors: [],
      }),
    );
    expect(html).toContain("103.25 EUR (10325)");
    expect(html).toContain("100.00 EUR (10000)");
    expect(html).toContain("net of the card fee");
    expect(html).toContain("= 100.00 EUR");
  });

  it("asks for a reason when voiding and warns that a paid invoice is never voided", () => {
    const html = renderToStaticMarkup(createElement(VoidFormBody, { invoice: invoice(), reason: "", onChange: noop, errors: ["Reason: say why the invoice is voided."] }));
    expect(html).toContain("credit note");
    expect(html).toContain("Reason: say why the invoice is voided.");
  });
});

describe("PaymentFeesTable and FeeFormBody", () => {
  const fee = (over: Partial<PaymentFee>): PaymentFee => ({
    id: "f",
    method: "stripe_card",
    currency: "EUR",
    pct_bp: 150,
    fixed_minor: 25,
    effective_from: "2026-01-01T00:00:00Z",
    reason: "contract with the processor",
    created_at: "2026-01-01T00:00:00Z",
    ...over,
  });
  const now = new Date("2026-10-17T10:00:00Z");

  it("prints percent and fixed part with their units and the status of each version", () => {
    const fees = [fee({ id: "old" }), fee({ id: "now", effective_from: "2026-06-01T00:00:00Z", pct_bp: 175 }), fee({ id: "next", effective_from: "2027-01-01T00:00:00Z" })];
    const html = renderToStaticMarkup(createElement(PaymentFeesTable, { fees, now }));
    expect(html).toContain("1.75%");
    expect(html).toContain("(175 bp)");
    expect(html).toContain("0.25 EUR");
    expect(html).toContain("In force");
    expect(html).toContain("Scheduled");
    expect(html).toContain("Superseded");
    expect(html).toContain("contract with the processor");
  });

  it("says what a missing fee means", () => {
    expect(renderToStaticMarkup(createElement(PaymentFeesTable, { fees: [], now }))).toContain("cannot be issued for a card payment");
  });

  it("starts with no number filled in and previews the fee on an example", () => {
    const empty = renderToStaticMarkup(createElement(FeeFormBody, { form: emptyFeeForm(), onChange: noop, errors: [] }));
    expect(empty).toContain("Nothing is filled in for you");
    expect(empty).not.toMatch(/value="\d/);
    expect(empty).not.toContain("billing-fee-example");
    const filled = renderToStaticMarkup(
      createElement(FeeFormBody, { form: { ...emptyFeeForm(), currency: "EUR", pctBp: "150", fixedMinor: "25" }, onChange: noop, errors: [] }),
    );
    expect(filled).toContain("= 1.5%");
    expect(filled).toContain("= 0.25 EUR");
    expect(filled).toContain("On an invoice of 100.00 EUR this fee is 1.78 EUR");
  });
});

describe("bank payments", () => {
  const tx = (over: Partial<BankTransaction>): BankTransaction => ({
    id: "t1",
    provider: "manual",
    external_id: "ext-1",
    booked_at: "2026-10-03T08:00:00Z",
    amount_minor: 10000,
    currency: "EUR",
    reference: "ARENA-0001",
    counterparty_name: "Acme s.r.o.",
    status: "unmatched",
    unmatched_reason: "amount_mismatch",
    invoice_id: null,
    matched_at: null,
    received_at: "2026-10-03T08:01:00Z",
    ...over,
  });
  const row = (html: string, id: string): string => {
    const start = html.indexOf(`data-testid="billing-bank-${id}"`);
    return html.slice(start, html.indexOf("</tr>", start));
  };

  it("shows the reason in words and offers match and ignore only for a waiting payment", () => {
    const html = renderToStaticMarkup(
      createElement(BankTable, {
        transactions: [
          tx({ id: "u" }),
          tx({ id: "m", status: "matched", unmatched_reason: null, invoice_id: "00000000-0000-7000-8000-000000000001" }),
          tx({ id: "g", status: "ignored", unmatched_reason: "test transfer" }),
        ],
        invoices: [invoice()],
        onMatch: noop,
        onIgnore: noop,
      }),
    );
    expect(row(html, "u")).toContain("The amount is not the invoice total");
    expect(row(html, "u")).toContain("billing-bank-match");
    expect(row(html, "u")).toContain("billing-bank-ignore");
    expect(row(html, "m")).toContain("Paid INV-2026-0001");
    expect(row(html, "m")).not.toContain("billing-bank-match");
    expect(row(html, "g")).toContain("Ignored: test transfer");
    expect(row(html, "g")).not.toContain("billing-bank-ignore");
    expect(html).toContain("100.00 EUR");
    expect(html).toContain("Acme s.r.o.");
  });

  it("matches only to issued invoices in the payment's currency and warns when the amount cannot fit", () => {
    const html = renderToStaticMarkup(
      createElement(MatchFormBody, {
        transaction: tx({}),
        issued: [invoice({ id: "a" }), invoice({ id: "b", currency: "USD", number: "INV-USD" }), invoice({ id: "c", total_minor: 5000, number: "INV-HALF" })],
        orgNames,
        invoiceId: "c",
        onChange: noop,
      }),
    );
    expect(html).toContain("INV-2026-0001");
    expect(html).not.toContain("INV-USD");
    expect(html).toContain("amount fits");
    expect(html).toContain("billing-match-mismatch");
    const none = renderToStaticMarkup(createElement(MatchFormBody, { transaction: tx({}), issued: [], orgNames, invoiceId: "", onChange: noop }));
    expect(none).toContain("No issued invoice in EUR");
  });

  it("asks for a reason to ignore and explains the statement format", () => {
    expect(renderToStaticMarkup(createElement(IgnoreFormBody, { transaction: tx({}), reason: "", onChange: noop, errors: [] }))).toContain("Why it is ignored");
    const html = renderToStaticMarkup(createElement(ImportFormBody, { text: "", onChange: noop, errors: ["Line 2: id is required, at most 200 characters."] }));
    expect(html).toContain("id;booked_at;amount_minor;currency;reference;counterparty");
    expect(html).toContain("whole");
    expect(html).toContain("Line 2: id is required");
  });
});
