import { describe, expect, it } from "vitest";
import {
  acceptedAmounts,
  amountFits,
  buildFeeRequest,
  buildMarkPaidRequest,
  buildReasonRequest,
  buildUsageRequest,
  canIssue,
  canMarkPaid,
  canVoid,
  emptyFeeForm,
  emptyMarkPaidForm,
  feeInForce,
  feeOnAmount,
  feeStatus,
  ingestSummary,
  invoiceLabel,
  isPeriod,
  latestFeeStart,
  matchCandidates,
  normalizeBookedAt,
  parseStatement,
  previousMonth,
  unmatchedReasonText,
  type BankTransaction,
  type Invoice,
  type PaymentFee,
} from "@/lib/api/billingInvoices";
import { requiresAdminReason } from "@/lib/api/reason";

// Test numbers, not the policy's: the screen never supplies a number itself.
const NOW = new Date("2026-10-17T10:00:00Z");

const inv = (over: Partial<Invoice> = {}): Invoice =>
  ({
    id: "11111111-2222-7333-8444-555555555555",
    state: "issued",
    number: "INV-1",
    currency: "EUR",
    subtotal_minor: 10000,
    card_fee_minor: 0,
    tax_minor: 0,
    total_minor: 10000,
    lines: [],
    ...over,
  }) as Invoice;

const fee = (over: Partial<PaymentFee>): PaymentFee => ({
  id: "f1",
  method: "stripe_card",
  currency: "EUR",
  pct_bp: 150,
  fixed_minor: 25,
  effective_from: "2026-01-01T00:00:00Z",
  reason: null,
  created_at: "2026-01-01T00:00:00Z",
  ...over,
});

describe("reason gate", () => {
  it("asks for X-Admin-Reason on billing writes only", () => {
    for (const p of ["/invoices/x/issue", "/invoices/x/mark-paid", "/invoices/x/void", "/payment-fees", "/bank-transactions", "/bank-transactions/x/match", "/bank-transactions/x/ignore", "/accounts/x/invoices"]) {
      expect(requiresAdminReason(`/v1/admin/billing${p}`, "POST"), p).toBe(true);
    }
    expect(requiresAdminReason("/v1/admin/billing/invoices?state=draft", "GET")).toBe(false);
  });
});

describe("what can be done to an invoice", () => {
  it("issues drafts, marks issued ones paid, voids drafts and issued ones", () => {
    expect([canIssue({ state: "draft" }), canIssue({ state: "issued" })]).toEqual([true, false]);
    expect([canMarkPaid({ state: "issued" }), canMarkPaid({ state: "draft" }), canMarkPaid({ state: "paid" })]).toEqual([true, false, false]);
    expect(["draft", "issued"].map((state) => canVoid({ state }))).toEqual([true, true]);
    expect(["paid", "void"].map((state) => canVoid({ state }))).toEqual([false, false]);
  });

  it("labels a draft by its id and an issued invoice by its number", () => {
    expect(invoiceLabel({ id: "abcdef12-0000", number: null })).toBe("Draft abcdef12");
    expect(invoiceLabel({ id: "x", number: "INV-7" })).toBe("INV-7");
  });

  it("accepts the total, and for a card invoice the total without the fee", () => {
    expect(acceptedAmounts({ total_minor: 10000, card_fee_minor: 0 })).toEqual([10000]);
    expect(acceptedAmounts({ total_minor: 10325, card_fee_minor: 325 })).toEqual([10325, 10000]);
  });
});

describe("usage invoice request", () => {
  it("knows a real month and suggests the month before now", () => {
    expect(isPeriod("2026-09")).toBe(true);
    for (const bad of ["2026-13", "2026-00", "26-09", "2026-9", "2026-09-01", ""]) expect(isPeriod(bad), bad).toBe(false);
    expect(previousMonth(NOW)).toBe("2026-09");
    expect(previousMonth(new Date("2027-01-05T00:00:00Z"))).toBe("2026-12");
  });

  it("needs an account and a month", () => {
    expect(buildUsageRequest({ accountId: "", period: "2026-09" }).ok).toBe(false);
    expect(buildUsageRequest({ accountId: "a", period: "Sept" }).ok).toBe(false);
    const ok = buildUsageRequest({ accountId: "a", period: " 2026-09 " });
    expect(ok.ok && ok.request).toEqual({ accountId: "a", period: "2026-09" });
  });
});

describe("mark paid", () => {
  const card = inv({ total_minor: 10325, card_fee_minor: 325 });
  const base = { ...emptyMarkPaidForm(), paidAtLocal: "2026-10-16T09:00", reference: "stmt line 12" };

  it("accepts the total and, for a card invoice, the net amount; integers only", () => {
    const total = buildMarkPaidRequest({ ...base, amountMinor: "10325" }, card, NOW);
    expect(total.ok && total.request).toEqual({ amount_minor: 10325, paid_at: "2026-10-16T09:00:00.000Z", reference: "stmt line 12" });
    expect(buildMarkPaidRequest({ ...base, amountMinor: "10000" }, card, NOW).ok).toBe(true);
    expect(buildMarkPaidRequest({ ...base, amountMinor: "10000" }, inv(), NOW).ok).toBe(true);
    for (const bad of ["", "0", "103.25", "-5", "9999", "1e4"]) {
      expect(buildMarkPaidRequest({ ...base, amountMinor: bad }, card, NOW).ok, bad).toBe(false);
    }
  });

  it("names the accepted amounts when the amount is wrong", () => {
    const r = buildMarkPaidRequest({ ...base, amountMinor: "9999" }, card, NOW);
    expect(!r.ok && r.errors.join(" ")).toContain("10325");
    expect(!r.ok && r.errors.join(" ")).toContain("10000");
  });

  it("refuses a future date and an empty or long reference", () => {
    const withAmount = { ...base, amountMinor: "10000" };
    expect(buildMarkPaidRequest({ ...withAmount, paidAtLocal: "2026-10-18T09:00" }, inv(), NOW).ok).toBe(false);
    expect(buildMarkPaidRequest({ ...withAmount, paidAtLocal: "" }, inv(), NOW).ok).toBe(false);
    expect(buildMarkPaidRequest({ ...withAmount, reference: "  " }, inv(), NOW).ok).toBe(false);
    expect(buildMarkPaidRequest({ ...withAmount, reference: "x".repeat(201) }, inv(), NOW).ok).toBe(false);
  });
});

describe("reason text", () => {
  it("is required and at most 500 characters", () => {
    expect(buildReasonRequest("", "it is voided").ok).toBe(false);
    expect(buildReasonRequest("x".repeat(501), "it is voided").ok).toBe(false);
    const ok = buildReasonRequest("  duplicate invoice  ", "it is voided");
    expect(ok.ok && ok.request).toBe("duplicate invoice");
  });
});

describe("payment fee request", () => {
  const filled = { ...emptyFeeForm(), currency: "eur", pctBp: "150", fixedMinor: "25", effectiveFromLocal: "2026-11-01T00:00" };

  it("starts empty and builds only what the operator typed", () => {
    expect(emptyFeeForm()).toEqual({ currency: "", pctBp: "", fixedMinor: "", effectiveFromLocal: "" });
    const r = buildFeeRequest(filled, [], NOW);
    expect(r.ok && r.request).toEqual({
      method: "stripe_card",
      currency: "EUR",
      pct_bp: 150,
      fixed_minor: 25,
      effective_from: "2026-11-01T00:00:00.000Z",
    });
  });

  it("keeps basis points and the fixed part inside the server's bounds, whole numbers only", () => {
    expect(buildFeeRequest({ ...filled, pctBp: "2000" }, [], NOW).ok).toBe(true);
    expect(buildFeeRequest({ ...filled, pctBp: "2001" }, [], NOW).ok).toBe(false);
    expect(buildFeeRequest({ ...filled, pctBp: "1.5" }, [], NOW).ok).toBe(false);
    expect(buildFeeRequest({ ...filled, pctBp: "" }, [], NOW).ok).toBe(false);
    expect(buildFeeRequest({ ...filled, fixedMinor: "100001" }, [], NOW).ok).toBe(false);
    expect(buildFeeRequest({ ...filled, fixedMinor: "0.25" }, [], NOW).ok).toBe(false);
    expect(buildFeeRequest({ ...filled, currency: "EU" }, [], NOW).ok).toBe(false);
  });

  it("starts in the future and after the latest version of its currency", () => {
    expect(buildFeeRequest({ ...filled, effectiveFromLocal: "2026-10-01T00:00" }, [], NOW).ok).toBe(false);
    const fees = [fee({ effective_from: "2026-12-01T00:00:00Z" }), fee({ id: "f2", currency: "USD", effective_from: "2027-06-01T00:00:00Z" })];
    const early = buildFeeRequest(filled, fees, NOW);
    expect(!early.ok && early.errors.join(" ")).toContain("after the latest version of EUR");
    expect(buildFeeRequest({ ...filled, effectiveFromLocal: "2026-12-02T00:00" }, fees, NOW).ok).toBe(true);
    expect(latestFeeStart(fees, "EUR")).toBe("2026-12-01T00:00:00Z");
    expect(latestFeeStart(fees, "GBP")).toBeNull();
  });

  it("shows where each version stands and previews the fee with integer maths", () => {
    const fees = [fee({ id: "old" }), fee({ id: "now", effective_from: "2026-06-01T00:00:00Z" }), fee({ id: "next", effective_from: "2027-01-01T00:00:00Z" })];
    expect(fees.map((f) => feeStatus(f, fees, NOW))).toEqual(["superseded", "in_force", "scheduled"]);
    expect(feeInForce(fees, "EUR", NOW)?.id).toBe("now");
    expect(feeInForce(fees, "GBP", NOW)).toBeUndefined();
    // The vectors of the server's TestCardFee_GrossesUpSoThePlatformNetsTheInvoice.
    expect(feeOnAmount(1650, 150, 25)).toBe(51);
    expect(feeOnAmount(1650, 0, 0)).toBe(0);
    expect(feeOnAmount(0, 150, 25)).toBe(0);
    expect(feeOnAmount(10000, 290, 30)).toBe(330);
  });
});

describe("bank payments", () => {
  it("says why a payment waits, in words", () => {
    for (const code of ["no_reference", "unknown_reference", "invoice_not_open", "amount_mismatch", "currency_mismatch"]) {
      const text = unmatchedReasonText("unmatched", code);
      expect(text, code).not.toBe(code);
      expect(text.endsWith("."), code).toBe(true);
    }
    expect(unmatchedReasonText("unmatched", "something_new")).toBe("something_new");
    expect(unmatchedReasonText("unmatched", null)).toBe("No reason recorded.");
    expect(unmatchedReasonText("ignored", "test transfer")).toBe("Ignored: test transfer");
    expect(unmatchedReasonText("matched", null)).toBe("");
  });

  const tx = (over: Partial<BankTransaction> = {}): BankTransaction => ({ amount_minor: 10000, currency: "EUR", ...over }) as BankTransaction;

  it("offers issued invoices in the payment's currency, and tells when the amount fits", () => {
    const list = [inv({ id: "a" }), inv({ id: "b", currency: "USD" }), inv({ id: "c", state: "paid" }), inv({ id: "d", total_minor: 10325, card_fee_minor: 325 })];
    expect(matchCandidates(tx(), list).map((i) => i.id)).toEqual(["a", "d"]);
    expect(amountFits(tx(), list[0] as Invoice)).toBe(true);
    expect(amountFits(tx(), list[3] as Invoice)).toBe(true); // the net of the card fee
    expect(amountFits(tx({ amount_minor: 9000 }), list[0] as Invoice)).toBe(false);
  });

  it("summarises an import", () => {
    expect(ingestSummary({ received: 4, skipped: 1, duplicate: 1, matched: 1, unmatched: 1, items: [] })).toBe(
      "4 received, 1 matched to an invoice, now paid, 1 waiting for you, 1 already stored, 1 skipped (not an incoming payment).",
    );
    expect(ingestSummary({ received: 1, skipped: 0, duplicate: 0, matched: 0, unmatched: 0, items: [] })).toBe("1 received.");
  });
});

describe("parseStatement", () => {
  it("reads CSV lines separated by semicolons, with an optional header, comments and blank lines", () => {
    const text = [
      "id;booked_at;amount_minor;currency;reference;counterparty",
      "# a comment",
      "",
      "tx-1;2026-10-01;10000;eur;INV-1;Acme Events",
      "tx-2;2026-10-02 09:30;5500;EUR",
      "tx-3;2026-10-03T08:00:00+02:00;100;USD;ref;",
    ].join("\r\n");
    const r = parseStatement(text);
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.transfers).toEqual([
      { id: "tx-1", booked_at: "2026-10-01T00:00:00Z", amount_minor: 10000, currency: "EUR", reference: "INV-1", counterparty_name: "Acme Events" },
      { id: "tx-2", booked_at: "2026-10-02T09:30:00Z", amount_minor: 5500, currency: "EUR" },
      { id: "tx-3", booked_at: "2026-10-03T08:00:00+02:00", amount_minor: 100, currency: "USD", reference: "ref" },
    ]);
  });

  it("reads a JSON list and a {transactions} object; amounts are integers", () => {
    const row = { id: "a", booked_at: "2026-10-01T10:00:00Z", amount_minor: 1234, currency: "EUR", reference: "r" };
    const list = parseStatement(JSON.stringify([row]));
    expect(list.ok && list.transfers[0]).toEqual({ ...row });
    const obj = parseStatement(JSON.stringify({ transactions: [{ ...row, amount_minor: "1234" }] }));
    expect(obj.ok && obj.transfers[0]?.amount_minor).toBe(1234);
  });

  it("never turns a fraction into money: 12.34 is refused, not rounded", () => {
    const csv = parseStatement("a;2026-10-01;12.34;EUR");
    expect(!csv.ok && csv.errors.join(" ")).toContain("amount_minor");
    const json = parseStatement(JSON.stringify([{ id: "a", booked_at: "2026-10-01", amount_minor: 12.34, currency: "EUR" }]));
    expect(json.ok).toBe(false);
  });

  it("points at the line or payment that is wrong, and reports every error", () => {
    const r = parseStatement("ok;2026-10-01;100;EUR\n;2026-10-01;100;EUR\nb;yesterday;100;EUR\nc;2026-10-01;100;EURO\nonly;three;fields");
    expect(r.ok).toBe(false);
    const text = !r.ok ? r.errors.join("\n") : "";
    expect(text).toContain("Line 2: id is required");
    expect(text).toContain("Line 3: booked_at");
    expect(text).toContain("Line 4: currency");
    expect(text).toContain("Line 5: write id;booked_at");
    expect(text).not.toContain("Line 1");
  });

  it("refuses broken JSON, an empty paste, and more than 500 payments", () => {
    expect(parseStatement("[{").ok).toBe(false);
    expect(parseStatement('{"nope": 1}').ok).toBe(false);
    expect(parseStatement("   ").ok).toBe(false);
    expect(parseStatement("# only a comment").ok).toBe(false);
    const many = Array.from({ length: 501 }, (_, i) => `t${i};2026-10-01;1;EUR`).join("\n");
    expect(parseStatement(many).ok).toBe(false);
    const max = Array.from({ length: 500 }, (_, i) => `t${i};2026-10-01;1;EUR`).join("\n");
    expect(parseStatement(max).ok).toBe(true);
  });

  it("limits reference and counterparty length like the server", () => {
    expect(parseStatement(`a;2026-10-01;1;EUR;${"r".repeat(1001)}`).ok).toBe(false);
    expect(parseStatement(`a;2026-10-01;1;EUR;r;${"n".repeat(301)}`).ok).toBe(false);
  });

  it("reads booked_at as UTC when it has no zone, and rejects impossible dates", () => {
    expect(normalizeBookedAt("2026-10-01")).toBe("2026-10-01T00:00:00Z");
    expect(normalizeBookedAt("2026-10-01 09:30:15")).toBe("2026-10-01T09:30:15Z");
    expect(normalizeBookedAt("2026-10-01T09:30:00Z")).toBe("2026-10-01T09:30:00Z");
    expect(normalizeBookedAt("2026-02-30")).toBeNull();
    expect(normalizeBookedAt("01.10.2026")).toBeNull();
  });
});
