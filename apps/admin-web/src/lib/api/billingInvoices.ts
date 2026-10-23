/**
 * Platform invoices, card fees and bank payments — the superadmin billing
 * screens of billing wave B3/B4 (invoices, payment fees, bank payments).
 *
 * Served by apps/backend/internal/platform/httpserver/hbilling
 * (admin_invoices.go, admin_accounts.go), all under /v1/admin/billing,
 * billing.admin AND a platform superadmin. Every write needs X-Admin-Reason
 * (reason.ts asks the operator once per tab; /v1/admin/billing is a
 * mutation-gated prefix) and writes an audit row on the server.
 *
 *   GET  /invoices?state=&limit=                 GET  /invoices/{id}
 *   POST /accounts/{id}/invoices {period}        POST /invoices/{id}/issue
 *   POST /invoices/{id}/mark-paid                POST /invoices/{id}/void
 *   GET|POST /payment-fees
 *   GET  /bank-transactions?status=&limit=       POST /bank-transactions
 *   POST /bank-transactions/{id}/match           POST /bank-transactions/{id}/ignore
 *
 * Rules mirrored here only to hide buttons and to catch a typo before the
 * round trip (the server stays the judge and checks all of it again): a draft
 * is issued, an issued invoice is marked paid or voided, a paid invoice is
 * never voided. Money is always whole minor units (integers) in requests;
 * there is NO default number anywhere in this file.
 *
 * Mock data: NONE.
 */
import type { components } from "@openapi/index";
import { authedFetch } from "@/lib/api/client";
import { formatMinor, localInputToIso, parseMinor, type Tone } from "@/lib/api/billingPrices";

type Schemas = components["schemas"];

export type Invoice = Schemas["BillingAccountInvoice"];
export type InvoiceLine = Schemas["BillingAccountInvoiceLine"];
export type PaymentFee = Schemas["BillingPaymentFee"];
export type BankTransaction = Schemas["BillingBankTransaction"];
export type BankTransfer = Schemas["BillingBankTransfer"];
export type BankIngestResult = Schemas["BillingBankIngestResult"];
export type MarkPaidRequest = Schemas["BillingMarkInvoicePaidRequest"];
export type CreatePaymentFeeRequest = Schemas["BillingCreatePaymentFeeRequest"];

const BASE = "/v1/admin/billing";

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

export type InvoiceState = "draft" | "issued" | "paid" | "void";
export type PaymentMethod = "bank_transfer" | "stripe_card";
export type BankStatus = "matched" | "unmatched" | "ignored";

export function listInvoices(state: InvoiceState, limit = 200): Promise<{ invoices: Invoice[] }> {
  return authedFetch({ method: "GET", path: `${BASE}/invoices?state=${state}&limit=${limit}` });
}

export function getInvoice(id: string): Promise<Invoice> {
  return authedFetch({ method: "GET", path: `${BASE}/invoices/${encodeURIComponent(id)}` });
}

/** 201 with the draft, or 200 with `invoice: null` when there was nothing to bill. */
export function createUsageInvoice(accountId: string, period: string): Promise<{ invoice: Invoice | null }> {
  return authedFetch({
    method: "POST",
    path: `${BASE}/accounts/${encodeURIComponent(accountId)}/invoices`,
    body: { period },
  });
}

export function issueInvoice(id: string, method: PaymentMethod): Promise<Invoice> {
  return authedFetch({
    method: "POST",
    path: `${BASE}/invoices/${encodeURIComponent(id)}/issue`,
    body: { payment_method: method },
  });
}

export function markInvoicePaid(id: string, req: MarkPaidRequest): Promise<Invoice> {
  return authedFetch({ method: "POST", path: `${BASE}/invoices/${encodeURIComponent(id)}/mark-paid`, body: req });
}

export function voidInvoice(id: string, reason: string): Promise<Invoice> {
  return authedFetch({ method: "POST", path: `${BASE}/invoices/${encodeURIComponent(id)}/void`, body: { reason } });
}

export function listPaymentFees(): Promise<{ payment_fees: PaymentFee[] }> {
  return authedFetch({ method: "GET", path: `${BASE}/payment-fees` });
}

export function createPaymentFee(req: CreatePaymentFeeRequest): Promise<PaymentFee> {
  return authedFetch({ method: "POST", path: `${BASE}/payment-fees`, body: req });
}

export function listBankTransactions(status: BankStatus, limit = 200): Promise<{ transactions: BankTransaction[] }> {
  return authedFetch({ method: "GET", path: `${BASE}/bank-transactions?status=${status}&limit=${limit}` });
}

export function importBankTransactions(transactions: readonly BankTransfer[]): Promise<BankIngestResult> {
  return authedFetch({ method: "POST", path: `${BASE}/bank-transactions`, body: { transactions } });
}

export function matchBankTransaction(transactionId: string, invoiceId: string): Promise<BankTransaction> {
  return authedFetch({
    method: "POST",
    path: `${BASE}/bank-transactions/${encodeURIComponent(transactionId)}/match`,
    body: { invoice_id: invoiceId },
  });
}

export function ignoreBankTransaction(transactionId: string, reason: string): Promise<BankTransaction> {
  return authedFetch({
    method: "POST",
    path: `${BASE}/bank-transactions/${encodeURIComponent(transactionId)}/ignore`,
    body: { reason },
  });
}

// ---------------------------------------------------------------------------
// Result shape used by the forms
// ---------------------------------------------------------------------------

export type BuildResult<T> =
  | { readonly ok: true; readonly request: T }
  | { readonly ok: false; readonly errors: readonly string[] };

// ---------------------------------------------------------------------------
// Invoice states, methods and what can be done to an invoice
// ---------------------------------------------------------------------------

export const INVOICE_STATES: readonly { value: InvoiceState; label: string }[] = [
  { value: "draft", label: "Drafts" },
  { value: "issued", label: "Issued" },
  { value: "paid", label: "Paid" },
  { value: "void", label: "Void" },
];

export function invoiceStateLabel(state: string): string {
  switch (state) {
    case "draft":
      return "Draft";
    case "issued":
      return "Issued";
    case "paid":
      return "Paid";
    case "void":
      return "Void";
    default:
      return state;
  }
}

export function invoiceStateTone(state: string): Tone {
  switch (state) {
    case "paid":
      return "good";
    case "issued":
      return "info";
    case "draft":
      return "warn";
    case "void":
      return "bad";
    default:
      return "muted";
  }
}

export function paymentMethodLabel(method: string | null | undefined): string {
  switch (method) {
    case "bank_transfer":
      return "Bank transfer";
    case "stripe_card":
      return "Card (Stripe)";
    case null:
    case undefined:
    case "":
      return "—";
    default:
      return method;
  }
}

export const PAYMENT_METHODS: readonly { value: PaymentMethod; label: string }[] = [
  { value: "bank_transfer", label: "Bank transfer" },
  { value: "stripe_card", label: "Card (Stripe)" },
];

export function invoiceKindLabel(kind: string): string {
  switch (kind) {
    case "usage":
      return "Usage";
    case "credit_pack":
      return "Credit pack";
    case "credit_note":
      return "Credit note";
    case "subscription":
      return "Subscription";
    case "manual":
      return "Manual";
    default:
      return kind;
  }
}

export function lineKindLabel(kind: string): string {
  switch (kind) {
    case "card_fee":
    case "payment_fee":
      return "Card payment fee";
    default:
      return kind.replace(/_/g, " ");
  }
}

/** A draft is issued; nothing else is. */
export function canIssue(inv: Pick<Invoice, "state">): boolean {
  return inv.state === "draft";
}

/** Money arrives for an issued invoice only. */
export function canMarkPaid(inv: Pick<Invoice, "state">): boolean {
  return inv.state === "issued";
}

/** A draft or an issued invoice can be voided; a paid one needs a credit note. */
export function canVoid(inv: Pick<Invoice, "state">): boolean {
  return inv.state === "draft" || inv.state === "issued";
}

/** "INV-1" for an issued invoice, "Draft 1a2b3c4d" before it has a number. */
export function invoiceLabel(inv: Pick<Invoice, "id" | "number">): string {
  return inv.number ?? `Draft ${inv.id.slice(0, 8)}`;
}

/** Amounts a payment may carry: the total, and for a card invoice a transfer net of the card fee. */
export function acceptedAmounts(inv: Pick<Invoice, "total_minor" | "card_fee_minor">): number[] {
  const amounts = [inv.total_minor];
  if (inv.card_fee_minor > 0) amounts.push(inv.total_minor - inv.card_fee_minor);
  return amounts;
}

// ---------------------------------------------------------------------------
// Usage invoice (generate for an account and a finished month)
// ---------------------------------------------------------------------------

/** "YYYY-MM" is a real month. */
export function isPeriod(text: string): boolean {
  return /^\d{4}-(0[1-9]|1[0-2])$/.test(text);
}

/** The calendar month before `now`, UTC: a suggestion only, the server judges "finished" in the issuer's zone. */
export function previousMonth(now: Date = new Date()): string {
  const y = now.getUTCFullYear();
  const m = now.getUTCMonth(); // 0-11, this month
  const prev = m === 0 ? { y: y - 1, m: 12 } : { y, m };
  return `${prev.y}-${String(prev.m).padStart(2, "0")}`;
}

export interface UsageForm {
  readonly accountId: string;
  readonly period: string;
}

export function buildUsageRequest(form: UsageForm): BuildResult<{ accountId: string; period: string }> {
  const errors: string[] = [];
  if (form.accountId === "") errors.push("Account: pick one.");
  if (!isPeriod(form.period.trim())) errors.push("Month: write it as YYYY-MM, for example 2026-09.");
  if (errors.length > 0) return { ok: false, errors };
  return { ok: true, request: { accountId: form.accountId, period: form.period.trim() } };
}

// ---------------------------------------------------------------------------
// Mark paid
// ---------------------------------------------------------------------------

export interface MarkPaidForm {
  readonly amountMinor: string;
  readonly paidAtLocal: string;
  readonly reference: string;
}

export function emptyMarkPaidForm(): MarkPaidForm {
  return { amountMinor: "", paidAtLocal: "", reference: "" };
}

export function buildMarkPaidRequest(
  form: MarkPaidForm,
  inv: Pick<Invoice, "total_minor" | "card_fee_minor" | "currency">,
  now: Date = new Date(),
): BuildResult<MarkPaidRequest> {
  const errors: string[] = [];
  const amount = parseMinor(form.amountMinor);
  if (amount === null || amount < 1) {
    errors.push("Amount: a whole number of minor units, more than 0.");
  } else if (!acceptedAmounts(inv).includes(amount)) {
    errors.push(
      `Amount: it must be ${acceptedAmounts(inv)
        .map((a) => `${a} (${formatMinor(a, inv.currency)})`)
        .join(" or ")}.`,
    );
  }
  const at = localInputToIso(form.paidAtLocal);
  if (at === null) errors.push("Paid at: pick the date and time the money arrived.");
  else if (new Date(at).getTime() > now.getTime() + 60_000) errors.push("Paid at: it cannot be in the future.");
  const reference = form.reference.trim();
  if (reference === "" || reference.length > 200) errors.push("Reference: the bank statement line or other proof, 1-200 characters.");
  if (errors.length > 0 || amount === null || at === null) return { ok: false, errors };
  return { ok: true, request: { amount_minor: amount, paid_at: at, reference } };
}

// ---------------------------------------------------------------------------
// Reasons typed by the operator (void an invoice, ignore a bank payment)
// ---------------------------------------------------------------------------

export function buildReasonRequest(text: string, what: string): BuildResult<string> {
  const reason = text.trim();
  if (reason === "") return { ok: false, errors: [`Reason: say why ${what}.`] };
  if (reason.length > 500) return { ok: false, errors: ["Reason: 500 characters at most."] };
  return { ok: true, request: reason };
}

// ---------------------------------------------------------------------------
// Payment fees
// ---------------------------------------------------------------------------

export interface FeeForm {
  readonly currency: string;
  readonly pctBp: string;
  readonly fixedMinor: string;
  readonly effectiveFromLocal: string;
}

export function emptyFeeForm(): FeeForm {
  return { currency: "", pctBp: "", fixedMinor: "", effectiveFromLocal: "" };
}

/** The latest start among the versions of one currency, as an RFC 3339 string; null when there is none. */
export function latestFeeStart(fees: readonly PaymentFee[], currency: string): string | null {
  let best: string | null = null;
  for (const f of fees) {
    if (f.method !== "stripe_card" || f.currency !== currency) continue;
    if (best === null || new Date(f.effective_from).getTime() > new Date(best).getTime()) best = f.effective_from;
  }
  return best;
}

export function buildFeeRequest(
  form: FeeForm,
  fees: readonly PaymentFee[] = [],
  now: Date = new Date(),
): BuildResult<CreatePaymentFeeRequest> {
  const errors: string[] = [];
  const currency = form.currency.trim().toUpperCase();
  if (!/^[A-Z]{3}$/.test(currency)) errors.push("Currency: three capital letters, for example EUR.");
  const pct = parseMinor(form.pctBp);
  if (pct === null || pct > 2000) errors.push("Percentage: basis points, a whole number from 0 to 2000 (250 means 2.5%).");
  const fixed = parseMinor(form.fixedMinor);
  if (fixed === null || fixed > 100000) errors.push("Fixed part: whole minor units, from 0 to 100000.");
  const from = localInputToIso(form.effectiveFromLocal);
  if (from === null) errors.push("Start: pick the date and time the version starts.");
  else if (new Date(from).getTime() <= now.getTime()) errors.push("Start: it must be in the future.");
  else {
    const latest = /^[A-Z]{3}$/.test(currency) ? latestFeeStart(fees, currency) : null;
    if (latest !== null && new Date(from).getTime() <= new Date(latest).getTime()) {
      errors.push(`Start: it must be after the latest version of ${currency}, which starts ${latest}.`);
    }
  }
  if (errors.length > 0 || pct === null || fixed === null || from === null) return { ok: false, errors };
  return {
    ok: true,
    request: { method: "stripe_card", currency, pct_bp: pct, fixed_minor: fixed, effective_from: from },
  };
}

/**
 * Preview of the card fee on an example amount, the same gross-up as the
 * server's invoicing.CardFee: the payer pays ceil((amount + fixed) * 10000 /
 * (10000 - bp)) so that the platform still nets the amount after the card
 * fee. Integer maths through BigInt, never a float.
 */
export function feeOnAmount(amountMinor: number, pctBp: number, fixedMinor: number): number {
  if (amountMinor <= 0 || pctBp < 0 || pctBp >= 10000 || fixedMinor < 0) return 0;
  const num = (BigInt(amountMinor) + BigInt(fixedMinor)) * 10000n;
  const den = BigInt(10000 - pctBp);
  const gross = num / den + (num % den === 0n ? 0n : 1n);
  return Number(gross) - amountMinor;
}

/** The version of one currency in force at `at`, or undefined. */
export function feeInForce(fees: readonly PaymentFee[], currency: string, at: Date): PaymentFee | undefined {
  let best: PaymentFee | undefined;
  for (const f of fees) {
    if (f.method !== "stripe_card" || f.currency !== currency) continue;
    if (new Date(f.effective_from).getTime() > at.getTime()) continue;
    if (best === undefined || new Date(f.effective_from).getTime() > new Date(best.effective_from).getTime()) best = f;
  }
  return best;
}

export type FeeStatus = "in_force" | "scheduled" | "superseded";

/** Where one version stands at `at`: the newest started version of its currency is in force. */
export function feeStatus(fee: PaymentFee, fees: readonly PaymentFee[], at: Date): FeeStatus {
  if (new Date(fee.effective_from).getTime() > at.getTime()) return "scheduled";
  return feeInForce(fees, fee.currency, at)?.id === fee.id ? "in_force" : "superseded";
}

export function feeStatusLabel(status: FeeStatus): string {
  return status === "in_force" ? "In force" : status === "scheduled" ? "Scheduled" : "Superseded";
}

export function feeStatusTone(status: FeeStatus): Tone {
  return status === "in_force" ? "good" : status === "scheduled" ? "info" : "muted";
}

// ---------------------------------------------------------------------------
// Bank payments
// ---------------------------------------------------------------------------

export const BANK_STATUSES: readonly { value: BankStatus; label: string }[] = [
  { value: "unmatched", label: "Waiting for you" },
  { value: "matched", label: "Matched" },
  { value: "ignored", label: "Ignored" },
];

export function bankStatusLabel(status: string): string {
  switch (status) {
    case "unmatched":
      return "Waiting";
    case "matched":
      return "Matched";
    case "ignored":
      return "Ignored";
    default:
      return status;
  }
}

export function bankStatusTone(status: string): Tone {
  switch (status) {
    case "matched":
      return "good";
    case "unmatched":
      return "warn";
    default:
      return "muted";
  }
}

/** Why a payment is not matched, in words (codes of billing/invoicing). */
export function unmatchedReasonText(status: string, reason: string | null | undefined): string {
  if (status === "ignored") return reason ? `Ignored: ${reason}` : "Ignored";
  switch (reason) {
    case "no_reference":
      return "The payer wrote no payment reference.";
    case "unknown_reference":
      return "The reference does not belong to any invoice.";
    case "invoice_not_open":
      return "The invoice is not open for payment (paid, void or still a draft).";
    case "amount_mismatch":
      return "The amount is not the invoice total (or the total without the card fee).";
    case "currency_mismatch":
      return "The currency is not the invoice currency.";
    case null:
    case undefined:
    case "":
      return status === "matched" ? "" : "No reason recorded.";
    default:
      return reason;
  }
}

/** Invoices a waiting payment can be tied to: issued, same currency. */
export function matchCandidates(tx: Pick<BankTransaction, "currency">, issued: readonly Invoice[]): Invoice[] {
  return issued.filter((i) => i.state === "issued" && i.currency === tx.currency);
}

/** True when the payment's amount is one the invoice accepts: a hint next to the choice. */
export function amountFits(tx: Pick<BankTransaction, "amount_minor">, inv: Pick<Invoice, "total_minor" | "card_fee_minor">): boolean {
  return acceptedAmounts(inv).includes(tx.amount_minor);
}

export function ingestSummary(r: BankIngestResult): string {
  const parts = [`${r.received} received`];
  if (r.matched > 0) parts.push(`${r.matched} matched to an invoice, now paid`);
  if (r.unmatched > 0) parts.push(`${r.unmatched} waiting for you`);
  if (r.duplicate > 0) parts.push(`${r.duplicate} already stored`);
  if (r.skipped > 0) parts.push(`${r.skipped} skipped (not an incoming payment)`);
  return `${parts.join(", ")}.`;
}

// ---------------------------------------------------------------------------
// Manual statement import: pasted JSON or simple CSV lines
// ---------------------------------------------------------------------------

export const MAX_TRANSFERS = 500;

export type ParseResult =
  | { readonly ok: true; readonly transfers: readonly BankTransfer[] }
  | { readonly ok: false; readonly errors: readonly string[] };

/**
 * `booked_at` as an RFC 3339 instant. A full RFC 3339 value is kept as is
 * (the server reads it); a plain date or "YYYY-MM-DD HH:MM[:SS]" is read as
 * UTC, because bank statements often carry nothing more.
 */
export function normalizeBookedAt(text: string): string | null {
  const t = text.trim();
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})$/.test(t)) {
    return Number.isNaN(new Date(t).getTime()) ? null : t;
  }
  const m = /^(\d{4}-\d{2}-\d{2})(?:[ T](\d{2}:\d{2})(?::(\d{2}))?)?$/.exec(t);
  if (m === null) return null;
  const iso = `${m[1]}T${m[2] ?? "00:00"}:${m[3] ?? "00"}Z`;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.toISOString().slice(0, 10) !== m[1]) return null;
  return iso;
}

interface RawTransfer {
  id: unknown;
  booked_at: unknown;
  amount_minor: unknown;
  currency: unknown;
  reference?: unknown;
  counterparty_name?: unknown;
}

function asText(v: unknown): string | null {
  if (typeof v === "string") return v;
  if (typeof v === "number" && Number.isFinite(v)) return String(v);
  return null;
}

/** An integer amount: a JSON integer, or a string of digits with an optional minus. Never a fraction. */
function asAmount(v: unknown): number | null {
  if (typeof v === "number") return Number.isSafeInteger(v) ? v : null;
  if (typeof v === "string" && /^-?\d{1,15}$/.test(v.trim())) {
    const n = Number(v.trim());
    return Number.isSafeInteger(n) ? n : null;
  }
  return null;
}

function checkTransfer(raw: RawTransfer, at: string, errors: string[]): BankTransfer | null {
  const before = errors.length;
  const id = (asText(raw.id) ?? "").trim();
  if (id === "" || id.length > 200) errors.push(`${at}: id is required, at most 200 characters.`);
  const bookedAt = asText(raw.booked_at);
  const booked = bookedAt === null ? null : normalizeBookedAt(bookedAt);
  if (booked === null) errors.push(`${at}: booked_at must be a date like 2026-10-01 or 2026-10-01T09:30:00Z.`);
  const amount = asAmount(raw.amount_minor);
  if (amount === null) errors.push(`${at}: amount_minor must be a whole number of minor units (no decimals).`);
  const currency = (asText(raw.currency) ?? "").trim().toUpperCase();
  if (!/^[A-Z]{3}$/.test(currency)) errors.push(`${at}: currency is a three-letter code.`);
  const reference = (asText(raw.reference) ?? "").trim();
  if (reference.length > 1000) errors.push(`${at}: reference is at most 1000 characters.`);
  const counterparty = (asText(raw.counterparty_name) ?? "").trim();
  if (counterparty.length > 300) errors.push(`${at}: counterparty is at most 300 characters.`);
  if (errors.length > before || booked === null || amount === null) return null;
  return {
    id,
    booked_at: booked,
    amount_minor: amount,
    currency,
    ...(reference !== "" ? { reference } : {}),
    ...(counterparty !== "" ? { counterparty_name: counterparty } : {}),
  };
}

function parseJsonStatement(text: string): ParseResult {
  let data: unknown;
  try {
    data = JSON.parse(text);
  } catch {
    return { ok: false, errors: ["The text starts like JSON but is not valid JSON."] };
  }
  const list = Array.isArray(data)
    ? data
    : data !== null && typeof data === "object" && Array.isArray((data as { transactions?: unknown }).transactions)
      ? (data as { transactions: unknown[] }).transactions
      : null;
  if (list === null) return { ok: false, errors: ['JSON must be a list of payments, or {"transactions": [...]}.'] };
  return finish(list.map((item, i) => ({ at: `Payment ${i + 1}`, raw: item })));
}

function parseCsvStatement(text: string): ParseResult {
  const rows: { at: string; raw: unknown }[] = [];
  const errors: string[] = [];
  let sawLine = false;
  text.split(/\r?\n/).forEach((line, i) => {
    const l = line.trim();
    if (l === "" || l.startsWith("#")) return;
    const fields = l.split(";").map((f) => f.trim());
    const first = !sawLine;
    sawLine = true;
    if (first && (fields[0] ?? "").toLowerCase() === "id") return; // header line
    const at = `Line ${i + 1}`;
    if (fields.length < 4 || fields.length > 6) {
      errors.push(`${at}: write id;booked_at;amount_minor;currency;reference;counterparty (reference and counterparty may be left out).`);
      return;
    }
    rows.push({
      at,
      raw: {
        id: fields[0],
        booked_at: fields[1],
        amount_minor: fields[2],
        currency: fields[3],
        reference: fields[4] ?? "",
        counterparty_name: fields[5] ?? "",
      },
    });
  });
  const r = finish(rows);
  if (errors.length === 0) return r;
  return { ok: false, errors: [...errors, ...(r.ok ? [] : r.errors)] };
}

function finish(items: readonly { at: string; raw: unknown }[]): ParseResult {
  if (items.length === 0) return { ok: false, errors: ["Paste at least one payment."] };
  if (items.length > MAX_TRANSFERS) return { ok: false, errors: [`At most ${MAX_TRANSFERS} payments at a time.`] };
  const errors: string[] = [];
  const out: BankTransfer[] = [];
  for (const it of items) {
    if (it.raw === null || typeof it.raw !== "object" || Array.isArray(it.raw)) {
      errors.push(`${it.at}: not a payment.`);
      continue;
    }
    const t = checkTransfer(it.raw as RawTransfer, it.at, errors);
    if (t !== null) out.push(t);
  }
  return errors.length > 0 ? { ok: false, errors } : { ok: true, transfers: out };
}

/**
 * Reads a pasted bank statement: JSON (a list of payments, or an object with a
 * `transactions` list) or CSV lines `id;booked_at;amount_minor;currency;reference;counterparty`
 * (semicolons, an optional header line, `#` comment lines, blank lines
 * ignored). Amounts are whole minor units; a fraction is refused, never
 * rounded. Pure: no request is made here.
 */
export function parseStatement(text: string): ParseResult {
  const t = text.trim();
  if (t === "") return { ok: false, errors: ["Paste at least one payment."] };
  return t.startsWith("[") || t.startsWith("{") ? parseJsonStatement(t) : parseCsvStatement(t);
}
