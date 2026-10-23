/**
 * Presentational parts of the "Invoices" screen (billing wave B3/B4). Router-
 * and query-free (see billingPricesLists.tsx): the page owns data and requests,
 * and the Node-only Vitest environment renders these with renderToStaticMarkup.
 *
 * All amounts are printed as money next to the minor units; nothing here has a
 * default number.
 */
import type { CSSProperties, ReactNode } from "react";
import { formatMinor, minorPreview, type BillingAccount } from "@/lib/api/billingPrices";
import {
  PAYMENT_METHODS,
  acceptedAmounts,
  canIssue,
  canMarkPaid,
  canVoid,
  invoiceKindLabel,
  invoiceLabel,
  invoiceStateLabel,
  invoiceStateTone,
  lineKindLabel,
  paymentMethodLabel,
  type Invoice,
  type MarkPaidForm,
  type PaymentMethod,
  type UsageForm,
} from "@/lib/api/billingInvoices";
import { formatDateTime } from "@/lib/admin/supportConsole";
import {
  Pill,
  buttonStyle,
  dangerButtonStyle,
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

const gridStyle: CSSProperties = { display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))", gap: 10 };
const rightStyle: CSSProperties = { textAlign: "right", whiteSpace: "nowrap" };

/** Money with the minor units beside it: "10.00 EUR (1000)". */
function money(minor: number, currency: string): string {
  return `${formatMinor(minor, currency)} (${minor})`;
}

export function orgLabel(orgNames: ReadonlyMap<string, string>, orgId: string): string {
  return orgNames.get(orgId) ?? `${orgId.slice(0, 8)}…`;
}

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
// List
// ---------------------------------------------------------------------------

export interface InvoicesTableProps {
  readonly invoices: readonly Invoice[];
  readonly orgNames: ReadonlyMap<string, string>;
  readonly onOpen: (inv: Invoice) => void;
  readonly onIssue: (inv: Invoice) => void;
  readonly onMarkPaid: (inv: Invoice) => void;
  readonly onVoid: (inv: Invoice) => void;
}

export function InvoicesTable({ invoices, orgNames, onOpen, onIssue, onMarkPaid, onVoid }: InvoicesTableProps): JSX.Element {
  if (invoices.length === 0) {
    return (
      <p style={mutedStyle} data-testid="billing-invoices-empty">
        No invoice in this state.
      </p>
    );
  }
  return (
    <div style={{ overflowX: "auto" }}>
      <table style={tableStyle} data-testid="billing-invoices-table">
        <thead>
          <tr>
            <th style={thStyle}>Invoice</th>
            <th style={thStyle}>Organization</th>
            <th style={thStyle}>Kind</th>
            <th style={thStyle}>Month</th>
            <th style={thStyle}>State</th>
            <th style={thStyle}>Method</th>
            <th style={{ ...thStyle, textAlign: "right" }}>Total</th>
            <th style={thStyle}>Due</th>
            <th style={thStyle} />
          </tr>
        </thead>
        <tbody>
          {invoices.map((inv) => (
            <tr key={inv.id} data-testid={`billing-invoice-${inv.id}`}>
              <td style={tdStyle}>
                <button type="button" style={{ ...buttonStyle, border: "none", padding: 0, textDecoration: "underline" }} onClick={() => onOpen(inv)} data-testid="billing-invoice-open">
                  {invoiceLabel(inv)}
                </button>
              </td>
              <td style={tdStyle}>{orgLabel(orgNames, inv.org_id)}</td>
              <td style={tdStyle}>{invoiceKindLabel(inv.kind)}</td>
              <td style={tdStyle}>{inv.billing_period}</td>
              <td style={tdStyle}>
                <Pill tone={invoiceStateTone(inv.state)}>{invoiceStateLabel(inv.state)}</Pill>
              </td>
              <td style={tdStyle}>{paymentMethodLabel(inv.payment_method)}</td>
              <td style={{ ...tdStyle, ...rightStyle }}>{formatMinor(inv.total_minor, inv.currency)}</td>
              <td style={tdStyle}>{formatDateTime(inv.due_at)}</td>
              <td style={{ ...tdStyle, whiteSpace: "nowrap" }}>
                <span style={{ display: "inline-flex", gap: 6 }}>
                  {canIssue(inv) ? (
                    <button type="button" style={buttonStyle} onClick={() => onIssue(inv)} data-testid="billing-invoice-issue">
                      Issue
                    </button>
                  ) : null}
                  {canMarkPaid(inv) ? (
                    <button type="button" style={buttonStyle} onClick={() => onMarkPaid(inv)} data-testid="billing-invoice-markpaid">
                      Mark paid
                    </button>
                  ) : null}
                  {canVoid(inv) ? (
                    <button type="button" style={dangerButtonStyle} onClick={() => onVoid(inv)} data-testid="billing-invoice-void">
                      Void
                    </button>
                  ) : null}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Detail
// ---------------------------------------------------------------------------

export function InvoiceDetailBody({ invoice, orgName }: { invoice: Invoice; orgName: string }): JSX.Element {
  const cur = invoice.currency;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-invoice-detail">
      <div style={{ display: "flex", gap: 6, flexWrap: "wrap", alignItems: "center" }}>
        <Pill tone={invoiceStateTone(invoice.state)}>{invoiceStateLabel(invoice.state)}</Pill>
        <span>{invoiceKindLabel(invoice.kind)}</span>
        <span style={mutedStyle}>
          {orgName}, month {invoice.billing_period}
        </span>
      </div>
      <dl style={{ display: "grid", gridTemplateColumns: "max-content 1fr", gap: "4px 14px", margin: 0, fontSize: 13 }}>
        <dt style={mutedStyle}>Number</dt>
        <dd style={{ margin: 0 }}>{invoice.number ?? "— (a draft has no number)"}</dd>
        <dt style={mutedStyle}>Payment method</dt>
        <dd style={{ margin: 0 }}>{paymentMethodLabel(invoice.payment_method)}</dd>
        <dt style={mutedStyle}>Payment reference</dt>
        <dd style={{ margin: 0 }} data-testid="billing-invoice-reference">
          {invoice.payment_reference ?? "—"}
        </dd>
        <dt style={mutedStyle}>Period</dt>
        <dd style={{ margin: 0 }}>
          {formatDateTime(invoice.period_from)} – {formatDateTime(invoice.period_to)}
        </dd>
        <dt style={mutedStyle}>Issued</dt>
        <dd style={{ margin: 0 }}>{formatDateTime(invoice.issued_at)}</dd>
        <dt style={mutedStyle}>Due</dt>
        <dd style={{ margin: 0 }}>{formatDateTime(invoice.due_at)}</dd>
        {invoice.paid_at !== null ? (
          <>
            <dt style={mutedStyle}>Paid</dt>
            <dd style={{ margin: 0 }}>
              {formatDateTime(invoice.paid_at)}
              {invoice.paid_amount_minor !== null ? `, ${money(invoice.paid_amount_minor, cur)}` : ""}
              {invoice.paid_via ? ` via ${invoice.paid_via}` : ""}
            </dd>
          </>
        ) : null}
        {invoice.voided_at !== null ? (
          <>
            <dt style={mutedStyle}>Voided</dt>
            <dd style={{ margin: 0 }}>{formatDateTime(invoice.voided_at)}</dd>
          </>
        ) : null}
        {invoice.payment_url ? (
          <>
            <dt style={mutedStyle}>Card payment page</dt>
            <dd style={{ margin: 0, wordBreak: "break-all" }} data-testid="billing-invoice-payment-url">
              {invoice.payment_url}
            </dd>
          </>
        ) : null}
      </dl>
      <table style={tableStyle} data-testid="billing-invoice-lines">
        <thead>
          <tr>
            <th style={thStyle}>Line</th>
            <th style={{ ...thStyle, textAlign: "right" }}>Quantity</th>
            <th style={{ ...thStyle, textAlign: "right" }}>Unit</th>
            <th style={{ ...thStyle, textAlign: "right" }}>Total</th>
          </tr>
        </thead>
        <tbody>
          {invoice.lines.length === 0 ? (
            <tr>
              <td style={tdStyle} colSpan={4}>
                <span style={mutedStyle}>No lines.</span>
              </td>
            </tr>
          ) : (
            invoice.lines.map((l) => (
              <tr key={l.id}>
                <td style={tdStyle}>
                  {l.description}
                  <div style={mutedStyle}>{lineKindLabel(l.kind)}</div>
                </td>
                <td style={{ ...tdStyle, textAlign: "right" }}>{l.quantity}</td>
                <td style={{ ...tdStyle, ...rightStyle }}>{formatMinor(l.unit_amount_minor, l.currency)}</td>
                <td style={{ ...tdStyle, ...rightStyle }}>{formatMinor(l.total_amount_minor, l.currency)}</td>
              </tr>
            ))
          )}
        </tbody>
        <tfoot>
          <tr>
            <td style={tdStyle} colSpan={3}>
              Subtotal
            </td>
            <td style={{ ...tdStyle, ...rightStyle }} data-testid="billing-invoice-subtotal">
              {money(invoice.subtotal_minor, cur)}
            </td>
          </tr>
          {invoice.card_fee_minor > 0 ? (
            <tr>
              <td style={tdStyle} colSpan={3}>
                Card payment fee
              </td>
              <td style={{ ...tdStyle, ...rightStyle }} data-testid="billing-invoice-cardfee">
                {money(invoice.card_fee_minor, cur)}
              </td>
            </tr>
          ) : null}
          {invoice.tax_minor > 0 ? (
            <tr>
              <td style={tdStyle} colSpan={3}>
                Tax
              </td>
              <td style={{ ...tdStyle, ...rightStyle }}>{money(invoice.tax_minor, cur)}</td>
            </tr>
          ) : null}
          <tr>
            <td style={{ ...tdStyle, fontWeight: 600 }} colSpan={3}>
              Total
            </td>
            <td style={{ ...tdStyle, ...rightStyle, fontWeight: 600 }} data-testid="billing-invoice-total">
              {money(invoice.total_minor, cur)}
            </td>
          </tr>
        </tfoot>
      </table>
      {canMarkPaid(invoice) && invoice.card_fee_minor > 0 ? (
        <p style={hintStyle}>
          A bank transfer for this card invoice may be the total ({formatMinor(invoice.total_minor, cur)}) or the total
          without the card fee ({formatMinor(invoice.total_minor - invoice.card_fee_minor, cur)}).
        </p>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Forms
// ---------------------------------------------------------------------------

export interface UsageFormBodyProps {
  readonly form: UsageForm;
  readonly onChange: (next: UsageForm) => void;
  readonly accounts: readonly BillingAccount[];
  readonly errors: readonly string[];
  readonly serverError?: ReactNode;
}

export function UsageFormBody({ form, onChange, accounts, errors, serverError }: UsageFormBodyProps): JSX.Element {
  const set = (patch: Partial<UsageForm>): void => onChange({ ...form, ...patch });
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-usage-form">
      <p style={hintStyle}>
        Makes a DRAFT usage invoice of a finished month from every charge that no credit lot or earlier invoice covers.
        If there is nothing to bill, no invoice is made. Issue the draft afterwards to give it a number and a due date.
      </p>
      <div style={gridStyle}>
        <div>
          <label style={labelStyle} htmlFor="billing-usage-account">
            Billing account
          </label>
          <select id="billing-usage-account" style={inputStyle} value={form.accountId} onChange={(e) => set({ accountId: e.target.value })} data-testid="billing-usage-account">
            <option value="">Pick an account…</option>
            {accounts.map((a) => (
              <option key={a.id} value={a.id}>
                {a.org_name} ({a.currency})
              </option>
            ))}
          </select>
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-usage-period">
            Month (YYYY-MM)
          </label>
          <input id="billing-usage-period" style={inputStyle} maxLength={7} value={form.period} onChange={(e) => set({ period: e.target.value })} data-testid="billing-usage-period" />
        </div>
      </div>
      <FormMessages errors={errors} serverError={serverError} />
    </div>
  );
}

export interface IssueFormBodyProps {
  readonly invoice: Invoice;
  readonly orgName: string;
  readonly method: PaymentMethod;
  readonly onChange: (m: PaymentMethod) => void;
  readonly errors?: readonly string[];
  readonly serverError?: ReactNode;
}

export function IssueFormBody({ invoice, orgName, method, onChange, errors = [], serverError }: IssueFormBodyProps): JSX.Element {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-issue-form">
      <p style={hintStyle}>
        Issuing gives the draft for {orgName} ({formatMinor(invoice.subtotal_minor, invoice.currency)} before any card fee)
        a number, a payment reference and a due date. After that its lines do not change; a mistake is corrected by
        voiding it before it is paid.
      </p>
      <div>
        <label style={labelStyle} htmlFor="billing-issue-method">
          How the organizer pays
        </label>
        <select id="billing-issue-method" style={inputStyle} value={method} onChange={(e) => onChange(e.target.value === "stripe_card" ? "stripe_card" : "bank_transfer")} data-testid="billing-issue-method">
          {PAYMENT_METHODS.map((m) => (
            <option key={m.value} value={m.value}>
              {m.label}
            </option>
          ))}
        </select>
      </div>
      <p style={hintStyle} data-testid="billing-issue-method-hint">
        {method === "stripe_card"
          ? "A card payment adds the average card fee as its own line (set under Payment fees; without a version for this currency the invoice cannot be issued for a card). A Stripe payment page is made after issuing."
          : "A bank transfer adds no fee. The organizer writes the payment reference in the transfer, and the bank feed matches it."}
      </p>
      <FormMessages errors={errors} serverError={serverError} />
    </div>
  );
}

export interface MarkPaidFormBodyProps {
  readonly invoice: Invoice;
  readonly form: MarkPaidForm;
  readonly onChange: (next: MarkPaidForm) => void;
  readonly errors: readonly string[];
  readonly serverError?: ReactNode;
}

export function MarkPaidFormBody({ invoice, form, onChange, errors, serverError }: MarkPaidFormBodyProps): JSX.Element {
  const set = (patch: Partial<MarkPaidForm>): void => onChange({ ...form, ...patch });
  const accepted = acceptedAmounts(invoice);
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-markpaid-form">
      <p style={hintStyle}>
        Use this for money that arrived outside the feeds, for example a transfer the bank feed missed. The amount must be{" "}
        {accepted.map((a, i) => (
          <span key={a}>
            {i > 0 ? " or " : ""}
            <strong>{money(a, invoice.currency)}</strong>
            {i === 1 ? " (a transfer is net of the card fee)" : ""}
          </span>
        ))}
        . If the invoice is for credits, they are written in the same step.
      </p>
      <div style={gridStyle}>
        <div>
          <label style={labelStyle} htmlFor="billing-markpaid-amount">
            Amount received (minor units)
          </label>
          <input id="billing-markpaid-amount" style={inputStyle} inputMode="numeric" value={form.amountMinor} onChange={(e) => set({ amountMinor: e.target.value })} data-testid="billing-markpaid-amount" />
          <span style={mutedStyle}>{minorPreview(form.amountMinor, invoice.currency)}</span>
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-markpaid-at">
            Arrived (UTC)
          </label>
          <input id="billing-markpaid-at" type="datetime-local" style={inputStyle} value={form.paidAtLocal} onChange={(e) => set({ paidAtLocal: e.target.value })} data-testid="billing-markpaid-at" />
        </div>
      </div>
      <div>
        <label style={labelStyle} htmlFor="billing-markpaid-ref">
          Reference: the bank statement line or other proof
        </label>
        <input id="billing-markpaid-ref" style={inputStyle} maxLength={200} value={form.reference} onChange={(e) => set({ reference: e.target.value })} data-testid="billing-markpaid-ref" />
      </div>
      <FormMessages errors={errors} serverError={serverError} />
    </div>
  );
}

export interface VoidFormBodyProps {
  readonly invoice: Invoice;
  readonly reason: string;
  readonly onChange: (reason: string) => void;
  readonly errors: readonly string[];
  readonly serverError?: ReactNode;
}

export function VoidFormBody({ invoice, reason, onChange, errors, serverError }: VoidFormBodyProps): JSX.Element {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-void-form">
      <p style={hintStyle}>
        Voiding {invoiceLabel(invoice)} ({formatMinor(invoice.total_minor, invoice.currency)}) sends its charges back to the
        next invoice. A paid invoice is never voided: correct it with a credit note. If a Stripe payment is already in, the
        server refuses and the payment is booked instead.
      </p>
      <div>
        <label style={labelStyle} htmlFor="billing-void-reason">
          Why it is voided (kept in the audit log)
        </label>
        <input id="billing-void-reason" style={inputStyle} maxLength={500} value={reason} onChange={(e) => onChange(e.target.value)} data-testid="billing-void-reason" />
      </div>
      <FormMessages errors={errors} serverError={serverError} />
    </div>
  );
}
