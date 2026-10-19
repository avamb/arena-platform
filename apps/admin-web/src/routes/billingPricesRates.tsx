/**
 * Individual rates and billing accounts tabs of "Price lists and rates"
 * (billing wave B-A, task B1-5). Presentational and query-free (see
 * billingPricesLists.tsx): the page owns data and requests.
 *
 * An individual rate replaces the price-list rate for one organization in one
 * currency (owner decision B-2). It starts in the future; the one before it is
 * ended at that moment; a rate that started is closed, never edited, and one
 * that has not started can be removed.
 */
import type { CSSProperties, ReactNode } from "react";
import {
  BILLING_CYCLES,
  PLAN_KINDS,
  SHORTFALL_POLICIES,
  canCloseRate,
  canRemoveRate,
  currentTerms,
  formatMinor,
  minorPreview,
  rateStatusLabel,
  rateStatusTone,
  type BillingAccount,
  type RateForm,
  type RateOverride,
  type TermsForm,
} from "@/lib/api/billingPrices";
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

// ---------------------------------------------------------------------------
// Individual rates
// ---------------------------------------------------------------------------

export interface RatesTableProps {
  readonly rates: readonly RateOverride[];
  readonly onClose: (r: RateOverride) => void;
  readonly onRemove: (r: RateOverride) => void;
}

export function RatesTable({ rates, onClose, onRemove }: RatesTableProps): JSX.Element {
  if (rates.length === 0) {
    return (
      <p style={mutedStyle} data-testid="billing-rates-empty">
        No individual rate yet.
      </p>
    );
  }
  return (
    <div style={{ overflowX: "auto" }}>
      <table style={tableStyle} data-testid="billing-rates-table">
        <thead>
          <tr>
            <th style={thStyle}>Organization</th>
            <th style={thStyle}>Currency</th>
            <th style={thStyle}>Without a seat</th>
            <th style={thStyle}>Seated</th>
            <th style={thStyle}>Starts</th>
            <th style={thStyle}>Ends</th>
            <th style={thStyle}>Status</th>
            <th style={thStyle}>Note</th>
            <th style={thStyle} />
          </tr>
        </thead>
        <tbody>
          {rates.map((r) => (
            <tr key={r.id} data-testid={`billing-rate-${r.id}`}>
              <td style={tdStyle}>{r.org_name ?? r.org_id}</td>
              <td style={tdStyle}>{r.currency}</td>
              <td style={tdStyle}>{formatMinor(r.unseated_minor, r.currency)}</td>
              <td style={tdStyle}>{formatMinor(r.seated_minor, r.currency)}</td>
              <td style={tdStyle}>{formatDateTime(r.effective_from)}</td>
              <td style={tdStyle}>{r.effective_to ? formatDateTime(r.effective_to) : "open"}</td>
              <td style={tdStyle}>
                <Pill tone={rateStatusTone(r.status)}>{rateStatusLabel(r.status)}</Pill>
              </td>
              <td style={{ ...tdStyle, maxWidth: 220 }}>{r.note ?? "—"}</td>
              <td style={{ ...tdStyle, whiteSpace: "nowrap" }}>
                {canCloseRate(r) ? (
                  <button type="button" style={buttonStyle} onClick={() => onClose(r)} data-testid="billing-rate-close">
                    Set end
                  </button>
                ) : null}{" "}
                {canRemoveRate(r) ? (
                  <button type="button" style={dangerButtonStyle} onClick={() => onRemove(r)} data-testid="billing-rate-remove">
                    Remove
                  </button>
                ) : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export interface OrgChoice {
  readonly id: string;
  readonly name: string;
}

export interface RateFormBodyProps {
  readonly form: RateForm;
  readonly onChange: (next: RateForm) => void;
  readonly orgs: readonly OrgChoice[];
  readonly errors: readonly string[];
  readonly serverError?: ReactNode;
}

export function RateFormBody({ form, onChange, orgs, errors, serverError }: RateFormBodyProps): JSX.Element {
  const set = (patch: Partial<RateForm>): void => onChange({ ...form, ...patch });
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-rate-form">
      <p style={hintStyle}>
        The rate replaces the price list for this organization in this currency, from the start you pick (in the future).
        A rate that is open at that moment is ended there. Amounts are in minor units, for example 50 is 0.50.
      </p>
      <div style={gridStyle}>
        <div>
          <label style={labelStyle} htmlFor="billing-rate-org">
            Organization
          </label>
          <select id="billing-rate-org" style={inputStyle} value={form.orgId} onChange={(e) => set({ orgId: e.target.value })} data-testid="billing-rate-org">
            <option value="">Pick an organization…</option>
            {orgs.map((o) => (
              <option key={o.id} value={o.id}>
                {o.name}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-rate-currency">
            Currency
          </label>
          <input
            id="billing-rate-currency"
            style={inputStyle}
            maxLength={3}
            value={form.currency}
            onChange={(e) => set({ currency: e.target.value.toUpperCase().trim() })}
            data-testid="billing-rate-currency"
          />
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-rate-unseated">
            Ticket without a seat (minor units)
          </label>
          <input id="billing-rate-unseated" style={inputStyle} inputMode="numeric" value={form.unseatedMinor} onChange={(e) => set({ unseatedMinor: e.target.value })} data-testid="billing-rate-unseated" />
          <span style={mutedStyle}>{minorPreview(form.unseatedMinor, form.currency)}</span>
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-rate-seated">
            Seated ticket (minor units)
          </label>
          <input id="billing-rate-seated" style={inputStyle} inputMode="numeric" value={form.seatedMinor} onChange={(e) => set({ seatedMinor: e.target.value })} data-testid="billing-rate-seated" />
          <span style={mutedStyle}>{minorPreview(form.seatedMinor, form.currency)}</span>
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-rate-from">
            Starts (UTC)
          </label>
          <input id="billing-rate-from" type="datetime-local" style={inputStyle} value={form.effectiveFromLocal} onChange={(e) => set({ effectiveFromLocal: e.target.value })} data-testid="billing-rate-from" />
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-rate-to">
            Ends (UTC, empty: open)
          </label>
          <input id="billing-rate-to" type="datetime-local" style={inputStyle} value={form.effectiveToLocal} onChange={(e) => set({ effectiveToLocal: e.target.value })} data-testid="billing-rate-to" />
        </div>
      </div>
      <div>
        <label style={labelStyle} htmlFor="billing-rate-note">
          Note, for example the contract it comes from
        </label>
        <input id="billing-rate-note" style={inputStyle} maxLength={500} value={form.note} onChange={(e) => set({ note: e.target.value })} data-testid="billing-rate-note" />
      </div>
      {errors.length > 0 ? (
        <ul style={{ ...warningStyle, margin: 0, paddingLeft: 28 }} data-testid="billing-form-errors">
          {errors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      ) : null}
      {serverError ? <p style={fieldErrorStyle}>{serverError}</p> : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

export interface AccountsTableProps {
  readonly accounts: readonly BillingAccount[];
  readonly onChangeTerms: (a: BillingAccount) => void;
}

export function AccountsTable({ accounts, onChangeTerms }: AccountsTableProps): JSX.Element {
  if (accounts.length === 0) {
    return (
      <p style={mutedStyle} data-testid="billing-accounts-empty">
        No billing account yet.
      </p>
    );
  }
  return (
    <div style={{ overflowX: "auto" }}>
      <table style={tableStyle} data-testid="billing-accounts-table">
        <thead>
          <tr>
            <th style={thStyle}>Organization</th>
            <th style={thStyle}>Currency</th>
            <th style={thStyle}>Marks</th>
            <th style={thStyle}>Price list</th>
            <th style={thStyle}>Cycle</th>
            <th style={thStyle}>Next terms</th>
            <th style={thStyle} />
          </tr>
        </thead>
        <tbody>
          {accounts.map((a) => {
            const cur = currentTerms(a);
            const upcoming = a.terms.filter((t) => cur === undefined || t.effective_from > cur.effective_from);
            return (
              <tr key={a.id} data-testid={`billing-account-${a.id}`}>
                <td style={tdStyle}>{a.org_name}</td>
                <td style={tdStyle}>{a.currency}</td>
                <td style={tdStyle}>
                  <span style={{ display: "inline-flex", gap: 4, flexWrap: "wrap" }}>
                    {a.flags.venue ? <Pill tone="info">Permanent venue</Pill> : null}
                    {a.flags.existing_client ? <Pill tone="good">Existing client</Pill> : null}
                    {a.flags.tools ? <Pill tone="warn">Tools</Pill> : null}
                    {!a.flags.venue && !a.flags.existing_client && !a.flags.tools ? <span style={mutedStyle}>—</span> : null}
                  </span>
                </td>
                <td style={tdStyle}>{cur ? cur.price_list_code : "—"}</td>
                <td style={tdStyle}>{cur ? cur.billing_cycle : "—"}</td>
                <td style={tdStyle}>
                  {upcoming.length === 0
                    ? "—"
                    : upcoming.map((t) => (
                        <div key={t.id}>
                          {formatDateTime(t.effective_from)}: {t.plan_kind}, {t.price_list_code}
                        </div>
                      ))}
                </td>
                <td style={{ ...tdStyle, whiteSpace: "nowrap" }}>
                  <button type="button" style={buttonStyle} onClick={() => onChangeTerms(a)} data-testid="billing-account-terms">
                    Change terms
                  </button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export interface TermsFormBodyProps {
  readonly account: BillingAccount;
  readonly form: TermsForm;
  readonly onChange: (next: TermsForm) => void;
  readonly priceLists: readonly string[];
  readonly errors: readonly string[];
  readonly serverError?: ReactNode;
}

export function TermsFormBody({ account, form, onChange, priceLists, errors, serverError }: TermsFormBodyProps): JSX.Element {
  const set = (patch: Partial<TermsForm>): void => onChange({ ...form, ...patch });
  const codes = priceLists.includes(form.priceListCode) || form.priceListCode === "" ? priceLists : [form.priceListCode, ...priceLists];
  const tools = account.account_kind === "tools";
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-terms-form">
      <p style={hintStyle}>
        New terms for {account.org_name} ({account.currency}
        {tools ? ", tools account" : ""}). They start at the time you pick, in the future, and do not change what was
        already rated. The price list must have a version in force at that moment with the rule the plan is rated by.
        {tools ? " A tools account is prepaid only." : ""}
      </p>
      <div style={gridStyle}>
        <div>
          <label style={labelStyle} htmlFor="billing-terms-from">
            Starts (UTC)
          </label>
          <input id="billing-terms-from" type="datetime-local" style={inputStyle} value={form.effectiveFromLocal} onChange={(e) => set({ effectiveFromLocal: e.target.value })} data-testid="billing-terms-from" />
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-terms-list">
            Price list
          </label>
          <select id="billing-terms-list" style={inputStyle} value={form.priceListCode} onChange={(e) => set({ priceListCode: e.target.value })} data-testid="billing-terms-list">
            <option value="">Pick a price list…</option>
            {codes.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-terms-plan">
            Plan
          </label>
          <select id="billing-terms-plan" style={inputStyle} value={form.planKind} onChange={(e) => set({ planKind: e.target.value })} data-testid="billing-terms-plan">
            {PLAN_KINDS.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-terms-cycle">
            Billing cycle
          </label>
          <select id="billing-terms-cycle" style={inputStyle} value={form.billingCycle} onChange={(e) => set({ billingCycle: e.target.value })} data-testid="billing-terms-cycle">
            {BILLING_CYCLES.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-terms-shortfall">
            When credits run short
          </label>
          <select id="billing-terms-shortfall" style={inputStyle} value={form.shortfallPolicy} onChange={(e) => set({ shortfallPolicy: e.target.value })} data-testid="billing-terms-shortfall">
            {SHORTFALL_POLICIES.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-terms-delay">
            Invoice delay, days (empty: none)
          </label>
          <input id="billing-terms-delay" style={inputStyle} inputMode="numeric" value={form.invoiceDelayDays} onChange={(e) => set({ invoiceDelayDays: e.target.value })} />
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-terms-due">
            Days to pay (empty: none)
          </label>
          <input id="billing-terms-due" style={inputStyle} inputMode="numeric" value={form.dueDays} onChange={(e) => set({ dueDays: e.target.value })} />
        </div>
      </div>
      {errors.length > 0 ? (
        <ul style={{ ...warningStyle, margin: 0, paddingLeft: 28 }} data-testid="billing-form-errors">
          {errors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      ) : null}
      {serverError ? <p style={fieldErrorStyle}>{serverError}</p> : null}
    </div>
  );
}
