/**
 * Presentational parts of the "Payment fees" and "Bank payments" screens
 * (billing wave B3/B4). Router- and query-free, rendered by Vitest with
 * renderToStaticMarkup (see billingPricesLists.tsx).
 *
 * There is no default number: the fee form starts empty and the operator types
 * every value.
 */
import type { CSSProperties, ReactNode } from "react";
import { formatBasisPoints, formatMinor, minorPreview } from "@/lib/api/billingPrices";
import {
  amountFits,
  bankStatusLabel,
  bankStatusTone,
  feeOnAmount,
  feeStatus,
  feeStatusLabel,
  feeStatusTone,
  invoiceLabel,
  matchCandidates,
  unmatchedReasonText,
  type BankTransaction,
  type FeeForm,
  type Invoice,
  type PaymentFee,
} from "@/lib/api/billingInvoices";
import { formatDateTime } from "@/lib/admin/supportConsole";
import {
  Pill,
  buttonStyle,
  fieldErrorStyle,
  hintStyle,
  inputStyle,
  labelStyle,
  mutedStyle,
  tableStyle,
  tdStyle,
  thStyle,
  warningStyle,
} from "./onboardingUi";
import { orgLabel } from "./billingInvoicesView";

const gridStyle: CSSProperties = { display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))", gap: 10 };

function FormMessages({ errors, serverError }: { errors: readonly string[]; serverError?: ReactNode }): JSX.Element {
  return (
    <>
      {errors.length > 0 ? (
        <ul style={{ ...warningStyle, margin: 0, paddingLeft: 28 }} data-testid="billing-form-errors">
          {errors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      ) : null}
      {serverError ? <p style={fieldErrorStyle}>{serverError}</p> : null}
    </>
  );
}

// ---------------------------------------------------------------------------
// Payment fees
// ---------------------------------------------------------------------------

export function PaymentFeesTable({ fees, now = new Date() }: { fees: readonly PaymentFee[]; now?: Date }): JSX.Element {
  if (fees.length === 0) {
    return (
      <p style={mutedStyle} data-testid="billing-fees-empty">
        No card fee yet. Without a version for a currency, an invoice in that currency cannot be issued for a card payment.
      </p>
    );
  }
  const sorted = [...fees].sort((a, b) => a.currency.localeCompare(b.currency) || b.effective_from.localeCompare(a.effective_from));
  return (
    <div style={{ overflowX: "auto" }}>
      <table style={tableStyle} data-testid="billing-fees-table">
        <thead>
          <tr>
            <th style={thStyle}>Currency</th>
            <th style={thStyle}>Method</th>
            <th style={thStyle}>Percentage</th>
            <th style={thStyle}>Fixed part</th>
            <th style={thStyle}>Starts (UTC)</th>
            <th style={thStyle}>Status</th>
            <th style={thStyle}>Reason</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((f) => {
            const st = feeStatus(f, fees, now);
            return (
              <tr key={f.id} data-testid={`billing-fee-${f.id}`}>
                <td style={tdStyle}>{f.currency}</td>
                <td style={tdStyle}>Card (Stripe)</td>
                <td style={tdStyle}>
                  {formatBasisPoints(f.pct_bp)} <span style={mutedStyle}>{`(${f.pct_bp} bp)`}</span>
                </td>
                <td style={tdStyle}>
                  {formatMinor(f.fixed_minor, f.currency)} <span style={mutedStyle}>{`(${f.fixed_minor})`}</span>
                </td>
                <td style={tdStyle}>{formatDateTime(f.effective_from)}</td>
                <td style={tdStyle}>
                  <Pill tone={feeStatusTone(st)}>{feeStatusLabel(st)}</Pill>
                </td>
                <td style={tdStyle}>{f.reason ?? "—"}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export interface FeeFormBodyProps {
  readonly form: FeeForm;
  readonly onChange: (next: FeeForm) => void;
  readonly errors: readonly string[];
  readonly serverError?: ReactNode;
}

export function FeeFormBody({ form, onChange, errors, serverError }: FeeFormBodyProps): JSX.Element {
  const set = (patch: Partial<FeeForm>): void => onChange({ ...form, ...patch });
  const pct = /^\d{1,4}$/.test(form.pctBp.trim()) ? Number(form.pctBp.trim()) : null;
  const fixed = /^\d{1,6}$/.test(form.fixedMinor.trim()) ? Number(form.fixedMinor.trim()) : null;
  const currency = form.currency.trim().toUpperCase();
  // An example only, to make the two numbers readable: 100.00 in the currency.
  const example = pct !== null && fixed !== null && /^[A-Z]{3}$/.test(currency) ? feeOnAmount(10000, pct, fixed) : null;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-fee-form">
      <p style={hintStyle}>
        The average fee a card payer adds to an invoice: percentage of the invoice plus a fixed part. A new version starts in
        the future and after the latest version of its currency, so no issued invoice changes. Nothing is filled in for you:
        type every value.
      </p>
      <div style={gridStyle}>
        <div>
          <label style={labelStyle} htmlFor="billing-fee-currency">
            Currency
          </label>
          <input id="billing-fee-currency" style={inputStyle} maxLength={3} value={form.currency} onChange={(e) => set({ currency: e.target.value.toUpperCase().trim() })} data-testid="billing-fee-currency" />
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-fee-pct">
            Percentage (basis points, 0-2000)
          </label>
          <input id="billing-fee-pct" style={inputStyle} inputMode="numeric" value={form.pctBp} onChange={(e) => set({ pctBp: e.target.value })} data-testid="billing-fee-pct" />
          <span style={mutedStyle}>{pct !== null ? `= ${formatBasisPoints(pct)}` : ""}</span>
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-fee-fixed">
            Fixed part (minor units)
          </label>
          <input id="billing-fee-fixed" style={inputStyle} inputMode="numeric" value={form.fixedMinor} onChange={(e) => set({ fixedMinor: e.target.value })} data-testid="billing-fee-fixed" />
          <span style={mutedStyle}>{minorPreview(form.fixedMinor, currency)}</span>
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-fee-from">
            Starts (UTC)
          </label>
          <input id="billing-fee-from" type="datetime-local" style={inputStyle} value={form.effectiveFromLocal} onChange={(e) => set({ effectiveFromLocal: e.target.value })} data-testid="billing-fee-from" />
        </div>
      </div>
      {example !== null ? (
        <p style={hintStyle} data-testid="billing-fee-example">
          {`On an invoice of ${formatMinor(10000, currency)} this fee is ${formatMinor(example, currency)}.`}
        </p>
      ) : null}
      <FormMessages errors={errors} serverError={serverError} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Bank payments
// ---------------------------------------------------------------------------

export interface BankTableProps {
  readonly transactions: readonly BankTransaction[];
  /** Invoices (any state) to print the number of the invoice a payment paid. */
  readonly invoices: readonly Invoice[];
  readonly onMatch: (t: BankTransaction) => void;
  readonly onIgnore: (t: BankTransaction) => void;
}

export function BankTable({ transactions, invoices, onMatch, onIgnore }: BankTableProps): JSX.Element {
  if (transactions.length === 0) {
    return (
      <p style={mutedStyle} data-testid="billing-bank-empty">
        No payment in this list.
      </p>
    );
  }
  const byId = new Map(invoices.map((i) => [i.id, i] as const));
  return (
    <div style={{ overflowX: "auto" }}>
      <table style={tableStyle} data-testid="billing-bank-table">
        <thead>
          <tr>
            <th style={thStyle}>Booked (UTC)</th>
            <th style={{ ...thStyle, textAlign: "right" }}>Amount</th>
            <th style={thStyle}>Payer</th>
            <th style={thStyle}>Reference</th>
            <th style={thStyle}>Status</th>
            <th style={thStyle}>Why / invoice</th>
            <th style={thStyle} />
          </tr>
        </thead>
        <tbody>
          {transactions.map((t) => {
            const inv = t.invoice_id !== null ? byId.get(t.invoice_id) : undefined;
            return (
              <tr key={t.id} data-testid={`billing-bank-${t.id}`}>
                <td style={tdStyle}>{formatDateTime(t.booked_at)}</td>
                <td style={{ ...tdStyle, textAlign: "right", whiteSpace: "nowrap" }}>
                  {formatMinor(t.amount_minor, t.currency)} <span style={mutedStyle}>{`(${t.amount_minor})`}</span>
                </td>
                <td style={tdStyle}>{t.counterparty_name ?? "—"}</td>
                <td style={tdStyle}>{t.reference ?? "—"}</td>
                <td style={tdStyle}>
                  <Pill tone={bankStatusTone(t.status)}>{bankStatusLabel(t.status)}</Pill>
                  <div style={mutedStyle}>{t.provider}</div>
                </td>
                <td style={tdStyle} data-testid="billing-bank-reason">
                  {t.status === "matched"
                    ? `Paid ${inv !== undefined ? invoiceLabel(inv) : (t.invoice_id ?? "").slice(0, 8)}`
                    : unmatchedReasonText(t.status, t.unmatched_reason)}
                </td>
                <td style={{ ...tdStyle, whiteSpace: "nowrap" }}>
                  {t.status === "unmatched" ? (
                    <span style={{ display: "inline-flex", gap: 6 }}>
                      <button type="button" style={buttonStyle} onClick={() => onMatch(t)} data-testid="billing-bank-match">
                        Match to invoice
                      </button>
                      <button type="button" style={buttonStyle} onClick={() => onIgnore(t)} data-testid="billing-bank-ignore">
                        Ignore
                      </button>
                    </span>
                  ) : null}
                  {t.status === "ignored" ? (
                    <button type="button" style={buttonStyle} onClick={() => onMatch(t)} data-testid="billing-bank-match">
                      Match to invoice
                    </button>
                  ) : null}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export interface MatchFormBodyProps {
  readonly transaction: BankTransaction;
  /** Issued invoices of any currency; the form keeps those in the payment's currency. */
  readonly issued: readonly Invoice[];
  readonly orgNames: ReadonlyMap<string, string>;
  readonly invoiceId: string;
  readonly onChange: (invoiceId: string) => void;
  readonly errors?: readonly string[];
  readonly serverError?: ReactNode;
}

export function MatchFormBody({ transaction, issued, orgNames, invoiceId, onChange, errors = [], serverError }: MatchFormBodyProps): JSX.Element {
  const candidates = matchCandidates(transaction, issued);
  const picked = candidates.find((i) => i.id === invoiceId);
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-match-form">
      <p style={hintStyle}>
        Payment of {formatMinor(transaction.amount_minor, transaction.currency)} from {transaction.counterparty_name ?? "an unknown payer"}
        {transaction.reference ? `, reference "${transaction.reference}"` : ""}. {unmatchedReasonText(transaction.status, transaction.unmatched_reason)} Pick the
        invoice it paid. The amount must be the invoice total, or for a card invoice the total without the card fee; the
        server checks this again.
      </p>
      <div>
        <label style={labelStyle} htmlFor="billing-match-invoice">
          Issued invoice
        </label>
        <select id="billing-match-invoice" style={inputStyle} value={invoiceId} onChange={(e) => onChange(e.target.value)} data-testid="billing-match-invoice">
          <option value="">Pick an invoice…</option>
          {candidates.map((i) => (
            <option key={i.id} value={i.id}>
              {invoiceLabel(i)} · {orgLabel(orgNames, i.org_id)} · {formatMinor(i.total_minor, i.currency)}
              {amountFits(transaction, i) ? " · amount fits" : ""}
            </option>
          ))}
        </select>
        {candidates.length === 0 ? (
          <p style={fieldErrorStyle} data-testid="billing-match-none">
            {`No issued invoice in ${transaction.currency}.`}
          </p>
        ) : null}
        {picked !== undefined && !amountFits(transaction, picked) ? (
          <p style={fieldErrorStyle} data-testid="billing-match-mismatch">
            The amount does not fit this invoice, so the server will refuse it.
          </p>
        ) : null}
      </div>
      <FormMessages errors={errors} serverError={serverError} />
    </div>
  );
}

export interface IgnoreFormBodyProps {
  readonly transaction: BankTransaction;
  readonly reason: string;
  readonly onChange: (reason: string) => void;
  readonly errors: readonly string[];
  readonly serverError?: ReactNode;
}

export function IgnoreFormBody({ transaction, reason, onChange, errors, serverError }: IgnoreFormBodyProps): JSX.Element {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-ignore-form">
      <p style={hintStyle}>
        Ignore the payment of {formatMinor(transaction.amount_minor, transaction.currency)} when it is not for an invoice,
        for example a refund from a supplier or a test transfer. An ignored payment can still be matched later.
      </p>
      <div>
        <label style={labelStyle} htmlFor="billing-ignore-reason">
          Why it is ignored
        </label>
        <input id="billing-ignore-reason" style={inputStyle} maxLength={500} value={reason} onChange={(e) => onChange(e.target.value)} data-testid="billing-ignore-reason" />
      </div>
      <FormMessages errors={errors} serverError={serverError} />
    </div>
  );
}

export interface ImportFormBodyProps {
  readonly text: string;
  readonly onChange: (text: string) => void;
  readonly errors: readonly string[];
  readonly serverError?: ReactNode;
}

export function ImportFormBody({ text, onChange, errors, serverError }: ImportFormBodyProps): JSX.Element {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-import-form">
      <p style={hintStyle}>
        For a bank without a feed. Paste JSON (a list of payments, or an object with <code>transactions</code>) or CSV lines
        separated by semicolons: <code>id;booked_at;amount_minor;currency;reference;counterparty</code>. Amounts are whole
        minor units (1000 means 10.00). A date alone is read as UTC midnight. Each payment is stored once by its id and
        matched to an invoice by its reference; a repeated id changes nothing. 1 to 500 payments.
      </p>
      <div>
        <label style={labelStyle} htmlFor="billing-import-text">
          Statement
        </label>
        <textarea
          id="billing-import-text"
          style={{ ...inputStyle, minHeight: 160, fontFamily: "ui-monospace, Menlo, monospace", fontSize: 12 }}
          value={text}
          onChange={(e) => onChange(e.target.value)}
          spellCheck={false}
          data-testid="billing-import-text"
        />
      </div>
      <FormMessages errors={errors} serverError={serverError} />
    </div>
  );
}
