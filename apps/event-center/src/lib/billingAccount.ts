/**
 * B3-3/B4: the pure rules of the billing balance, invoices, credit journal,
 * the commission helper and the payer form. No React here.
 *
 * Money is minor units next to a currency. Nothing here adds amounts of
 * different currencies: a balance, a pack and an invoice each carry their own.
 */
import type { BillingAccountBalance, BillingAccountInvoice, BillingLedgerEntry, BillingPackOffer, BillingPayerRequest } from "@/lib/api/types";
import { ApiError } from "@/lib/api/http";
import { currencyFractionDigits } from "@/lib/format";

// ─── packs and balances grouped by currency ───────────────────────────────

/** The packs of one currency, cheapest first. A pack list is never mixed across currencies. */
export function packsByCurrency(packs: readonly BillingPackOffer[]): { currency: string; packs: BillingPackOffer[] }[] {
  const map = new Map<string, BillingPackOffer[]>();
  for (const p of packs) {
    const list = map.get(p.currency) ?? [];
    list.push(p);
    map.set(p.currency, list);
  }
  return [...map.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([currency, list]) => ({ currency, packs: [...list].sort((x, y) => x.face_minor - y.face_minor) }));
}

/** The balance of an account has nothing to spend and nothing bought yet. */
export function isEmptyBalance(b: Pick<BillingAccountBalance, "balance_minor" | "lots">): boolean {
  return b.balance_minor === 0 && b.lots.length === 0;
}

// ─── invoices ──────────────────────────────────────────────────────

/** The states the API filters by. Drafts are never shown to the organizer. */
export const INVOICE_STATE_FILTERS = ["issued", "paid", "void"] as const;
export type InvoiceStateFilter = (typeof INVOICE_STATE_FILTERS)[number];

export function isInvoiceStateFilter(v: string): v is InvoiceStateFilter {
  return (INVOICE_STATE_FILTERS as readonly string[]).includes(v);
}

export const INVOICES_PAGE_SIZE = 20;

const STATES = ["issued", "paid", "void", "draft"] as const;
export type InvoiceStateKey = `billing.invoice.state.${(typeof STATES)[number] | "other"}`;
export function invoiceStateKey(state: string): InvoiceStateKey {
  return (STATES as readonly string[]).includes(state) ? (`billing.invoice.state.${state}` as InvoiceStateKey) : "billing.invoice.state.other";
}

const KINDS = ["credit_pack", "usage", "credit_note", "subscription", "manual"] as const;
export type InvoiceKindKey = `billing.invoice.kind.${(typeof KINDS)[number] | "other"}`;
export function invoiceKindKey(kind: string): InvoiceKindKey {
  return (KINDS as readonly string[]).includes(kind) ? (`billing.invoice.kind.${kind}` as InvoiceKindKey) : "billing.invoice.kind.other";
}

const METHODS = ["bank_transfer", "stripe_card"] as const;
export type MethodKey = `billing.method.${(typeof METHODS)[number] | "other"}`;
export function methodKey(method: string | null | undefined): MethodKey | null {
  if (method === null || method === undefined || method === "") return null;
  return (METHODS as readonly string[]).includes(method) ? (`billing.method.${method}` as MethodKey) : "billing.method.other";
}

const LINE_KINDS = ["pack", "card_fee", "usage", "credit_note", "manual"] as const;
export type LineKindKey = `billing.line.${(typeof LINE_KINDS)[number] | "other"}`;
export function lineKindKey(kind: string): LineKindKey {
  return (LINE_KINDS as readonly string[]).includes(kind) ? (`billing.line.${kind}` as LineKindKey) : "billing.line.other";
}

/** "Pay by card" is offered for an issued invoice that was ordered with a card. */
export function canPayByCard(inv: Pick<BillingAccountInvoice, "state" | "payment_method">): boolean {
  return inv.state === "issued" && inv.payment_method === "stripe_card";
}

/** A bank transfer that waits for the money: the reference must be written in the transfer. */
export function awaitsBankTransfer(inv: Pick<BillingAccountInvoice, "state" | "payment_method" | "payment_reference">): boolean {
  return (
    inv.state === "issued" &&
    inv.payment_method === "bank_transfer" &&
    inv.payment_reference !== null &&
    inv.payment_reference !== undefined &&
    inv.payment_reference !== ""
  );
}

/** What is to be paid for an invoice: the subtotal plus the card fee plus tax is `total_minor`. */
export function invoiceBreakdown(inv: Pick<BillingAccountInvoice, "subtotal_minor" | "card_fee_minor" | "tax_minor" | "total_minor">): {
  subtotal: number;
  cardFee: number;
  tax: number;
  total: number;
} {
  return { subtotal: inv.subtotal_minor, cardFee: inv.card_fee_minor, tax: inv.tax_minor, total: inv.total_minor };
}

// ─── the credit journal ────────────────────────────────────────────

const ENTRY_TYPES = [
  "purchase",
  "bonus_grant",
  "consume",
  "reversal",
  "reversal_dropped",
  "expire",
  "hold",
  "release",
  "adjustment_credit",
  "adjustment_debit",
] as const;

export type LedgerKey = `billing.ledger.type.${(typeof ENTRY_TYPES)[number] | "other"}`;
export function ledgerKey(entryType: string): LedgerKey {
  return (ENTRY_TYPES as readonly string[]).includes(entryType) ? (`billing.ledger.type.${entryType}` as LedgerKey) : "billing.ledger.type.other";
}

const ADDS = new Set(["purchase", "bonus_grant", "reversal", "release", "adjustment_credit"]);
const TAKES = new Set(["consume", "expire", "hold", "adjustment_debit"]);

/** +1 adds credits, -1 takes them, 0 moves nothing. The API amount is always positive. */
export function ledgerSign(entryType: string): 1 | -1 | 0 {
  if (ADDS.has(entryType)) return 1;
  if (TAKES.has(entryType)) return -1;
  return 0;
}

/** The signed amount of an entry, for display. */
export function ledgerSigned(e: Pick<BillingLedgerEntry, "entry_type" | "amount_minor">): number {
  return ledgerSign(e.entry_type) * e.amount_minor;
}

export const LEDGER_PAGE_SIZE = 30;

// ─── the commission as a service charge ─────────────────────────────

/** The API allows prices up to this many minor units. */
export const MAX_QUOTE_PRICE_MINOR = 1_000_000_000;

export type QuoteInput = { ok: true; priceMinor: number } | { ok: false; problem: "empty" | "invalid" | "tooBig" };

/**
 * Reads a ticket price typed in major units ("20", "20.5", "20,50") as minor
 * units of the currency. Thousands separators are not guessed: a value with
 * more fraction digits than the currency has is refused, not rounded.
 */
export function parseQuotePrice(raw: string, currency: string): QuoteInput {
  const s = raw.trim().replace(/\s+/g, "");
  if (s === "") return { ok: false, problem: "empty" };
  const m = /^(\d{1,12})(?:[.,](\d+))?$/.exec(s);
  if (m === null) return { ok: false, problem: "invalid" };
  const digits = currencyFractionDigits(currency);
  const frac = m[2] ?? "";
  if (frac.length > digits) return { ok: false, problem: "invalid" };
  const minor = Number(m[1]) * 10 ** digits + (frac === "" ? 0 : Number(frac.padEnd(digits, "0")));
  if (!Number.isSafeInteger(minor) || minor > MAX_QUOTE_PRICE_MINOR) return { ok: false, problem: "tooBig" };
  return { ok: true, priceMinor: minor };
}

/** The currencies a quote can be asked in: those of the organization's accounts. */
export function quoteCurrencies(accounts: readonly Pick<BillingAccountBalance, "currency">[]): string[] {
  return [...new Set(accounts.map((a) => a.currency))].sort();
}

// ─── the payer form ────────────────────────────────────────────────

export interface PayerForm {
  legal_name: string;
  address: string;
  country: string;
  tax_id: string;
  email: string;
}

export const EMPTY_PAYER: PayerForm = { legal_name: "", address: "", country: "", tax_id: "", email: "" };

export type PayerField = keyof PayerForm;
export type PayerProblem = { field: PayerField; code: "required" | "tooLong" | "country" | "email" };

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

/** The same limits the API enforces, checked first so the person is told what to change. */
export function validatePayer(f: PayerForm): PayerProblem[] {
  const out: PayerProblem[] = [];
  const name = f.legal_name.trim();
  if (name === "") out.push({ field: "legal_name", code: "required" });
  else if ([...name].length > 200) out.push({ field: "legal_name", code: "tooLong" });
  if ([...f.address.trim()].length > 500) out.push({ field: "address", code: "tooLong" });
  if ([...f.tax_id.trim()].length > 50) out.push({ field: "tax_id", code: "tooLong" });
  const country = f.country.trim();
  if (country !== "" && !/^[A-Za-z]{2}$/.test(country)) out.push({ field: "country", code: "country" });
  const email = f.email.trim();
  if (email !== "" && !EMAIL.test(email)) out.push({ field: "email", code: "email" });
  return out;
}

/** The body of PUT payer: trimmed, the country in capitals. */
export function payerRequest(f: PayerForm): BillingPayerRequest {
  return {
    legal_name: f.legal_name.trim(),
    address: f.address.trim(),
    country: f.country.trim().toUpperCase(),
    tax_id: f.tax_id.trim(),
    email: f.email.trim(),
  };
}

/** A form prefilled from the organization's own legal data (the person can change it). */
export function payerFromOrg(org: {
  legal_name?: string | null;
  tax_id?: string | null;
  contact_email?: string | null;
  legal_address_line1?: string | null;
  legal_address_postal_code?: string | null;
  legal_address_city?: string | null;
  legal_address_country?: string | null;
}): PayerForm {
  const address = [org.legal_address_line1, [org.legal_address_postal_code, org.legal_address_city].filter(Boolean).join(" ")]
    .map((x) => (x ?? "").trim())
    .filter((x) => x !== "")
    .join(", ");
  return {
    legal_name: (org.legal_name ?? "").trim(),
    address,
    country: (org.legal_address_country ?? "").trim().toUpperCase(),
    tax_id: (org.tax_id ?? "").trim(),
    email: (org.contact_email ?? "").trim(),
  };
}

/**
 * The API answers 404 `billing.organizer_disabled` on every organizer route of
 * credit packs and platform invoices while BILLING_ORGANIZER_ENABLED is off.
 * The screens then hide those sections instead of showing an error.
 */
export const ORGANIZER_DISABLED_CODE = "billing.organizer_disabled";

export function isOrganizerDisabled(err: unknown): boolean {
  return err instanceof ApiError && err.code === ORGANIZER_DISABLED_CODE;
}
