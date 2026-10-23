/**
 * Price lists tab of "Price lists and rates" (billing wave B-A, task B1-5):
 * the versions of every price list, one version's rules and packs, the
 * "new version" form, and the revoke confirmation. Presentational and
 * query-free so the Node-only Vitest environment can render it with
 * renderToStaticMarkup; the page (billingPrices.tsx) owns data and requests.
 */
import type { CSSProperties, ReactNode } from "react";
import {
  RULE_SHAPES,
  canRevokeVersion,
  formatBasisPoints,
  formatMinor,
  minorPreview,
  ruleShape,
  versionStatusLabel,
  versionStatusTone,
  type PackForm,
  type PriceListDetail,
  type PriceListVersion,
  type RuleForm,
  type VersionForm,
  type VersionSeries,
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
  sectionStyle,
  tableStyle,
  tdStyle,
  thStyle,
  warningStyle,
} from "./onboardingUi";

// ---------------------------------------------------------------------------
// Versions list
// ---------------------------------------------------------------------------

export interface VersionsTableProps {
  readonly series: readonly VersionSeries[];
  readonly onOpen: (v: PriceListVersion) => void;
  readonly onRevoke: (v: PriceListVersion) => void;
  readonly onNewVersion: (s: VersionSeries) => void;
}

export function VersionsTable({ series, onOpen, onRevoke, onNewVersion }: VersionsTableProps): JSX.Element {
  if (series.length === 0) {
    return (
      <p style={mutedStyle} data-testid="billing-prices-empty">
        No price list yet.
      </p>
    );
  }
  return (
    <div data-testid="billing-prices-lists">
      {series.map((s) => (
        <section key={s.key} style={sectionStyle} data-testid={`billing-series-${s.code}-${s.currency}`}>
          <div style={{ display: "flex", justifyContent: "space-between", gap: 8, flexWrap: "wrap", marginBottom: 8 }}>
            <h3 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>
              {s.code} <span style={{ color: "#64748b", fontWeight: 400 }}>· {s.currency}</span>
            </h3>
            <button type="button" style={buttonStyle} onClick={() => onNewVersion(s)} data-testid="billing-new-version-from">
              New version
            </button>
          </div>
          <div style={{ overflowX: "auto" }}>
            <table style={tableStyle}>
              <thead>
                <tr>
                  <th style={thStyle}>Version</th>
                  <th style={thStyle}>Status</th>
                  <th style={thStyle}>Starts</th>
                  <th style={thStyle}>Rules</th>
                  <th style={thStyle}>Packs</th>
                  <th style={thStyle}>Reason</th>
                  <th style={thStyle} />
                </tr>
              </thead>
              <tbody>
                {s.versions.map((v) => (
                  <tr key={v.id} data-testid={`billing-version-${v.version}`}>
                    <td style={tdStyle}>v{v.version}</td>
                    <td style={tdStyle}>
                      <Pill tone={versionStatusTone(v.status)}>{versionStatusLabel(v.status)}</Pill>
                      {v.locked ? (
                        <span style={{ marginLeft: 6 }}>
                          <Pill tone="warn">Locked</Pill>
                        </span>
                      ) : null}
                    </td>
                    <td style={tdStyle}>{formatDateTime(v.effective_from)}</td>
                    <td style={tdStyle}>{v.rules_count ?? "—"}</td>
                    <td style={tdStyle}>{v.packs_count ?? "—"}</td>
                    <td style={{ ...tdStyle, maxWidth: 260 }}>{v.reason ?? "—"}</td>
                    <td style={{ ...tdStyle, whiteSpace: "nowrap" }}>
                      <button type="button" style={buttonStyle} onClick={() => onOpen(v)} data-testid="billing-version-open">
                        View
                      </button>{" "}
                      {canRevokeVersion(v) ? (
                        <button type="button" style={dangerButtonStyle} onClick={() => onRevoke(v)} data-testid="billing-version-revoke">
                          Revoke
                        </button>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// One version
// ---------------------------------------------------------------------------

function paramText(key: string, value: unknown, currency: string): string {
  if (typeof value === "boolean") return value ? "yes" : "no";
  if (typeof value !== "number") return String(value);
  if (key.endsWith("_bp")) return formatBasisPoints(value);
  if (key.endsWith("_minor")) return `${formatMinor(value, currency)} (${value})`;
  return String(value);
}

export function VersionDetailBody({ detail }: { detail: PriceListDetail }): JSX.Element {
  return (
    <div data-testid="billing-version-detail" style={{ display: "flex", flexDirection: "column", gap: 12 }}>
      <p style={{ ...mutedStyle, lineHeight: 1.5 }}>
        <strong>
          {detail.code} v{detail.version}
        </strong>{" "}
        in {detail.currency} · starts {formatDateTime(detail.effective_from)} ·{" "}
        <Pill tone={versionStatusTone(detail.status)}>{versionStatusLabel(detail.status)}</Pill>
        <br />
        Bonus share cap {formatBasisPoints(detail.bonus_share_cap_bp)}
        {detail.bonus_covers_surcharge ? ", bonus pays the seat surcharge" : ", bonus does not pay the seat surcharge"} · free
        tickets a year: {detail.free_tickets_annual_limit ?? "not set"} · floor per ticket:{" "}
        {detail.floor_per_ticket_minor == null ? "not set" : formatMinor(detail.floor_per_ticket_minor, detail.currency)}
        <br />
        Created {formatDateTime(detail.created_at)}
        {detail.reason ? ` · reason: ${detail.reason}` : ""}
        {detail.revoked_at ? ` · revoked ${formatDateTime(detail.revoked_at)}` : ""}
      </p>
      <div style={{ overflowX: "auto" }}>
        <table style={tableStyle} data-testid="billing-version-rules">
          <thead>
            <tr>
              <th style={thStyle}>Rule</th>
              <th style={thStyle}>Line</th>
              <th style={thStyle}>Numbers</th>
            </tr>
          </thead>
          <tbody>
            {detail.rules.map((r) => (
              <tr key={r.id}>
                <td style={tdStyle}>
                  {ruleShape(r.rule_type)?.label ?? r.rule_type}
                  {r.sale_currency ? ` (${r.sale_currency})` : ""}
                </td>
                <td style={tdStyle}>{r.line}</td>
                <td style={tdStyle}>
                  {Object.entries(r.params)
                    .map(([k, v]) => `${k}: ${paramText(k, v, detail.currency)}`)
                    .join(" · ")}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {detail.packs.length > 0 ? (
        <div style={{ overflowX: "auto" }}>
          <table style={tableStyle} data-testid="billing-version-packs">
            <thead>
              <tr>
                <th style={thStyle}>Tier</th>
                <th style={thStyle}>Face value</th>
                <th style={thStyle}>Ticket without a seat</th>
                <th style={thStyle}>Seated surcharge</th>
                <th style={thStyle}>Active</th>
              </tr>
            </thead>
            <tbody>
              {detail.packs.map((p) => (
                <tr key={p.id}>
                  <td style={tdStyle}>{p.tier}</td>
                  <td style={tdStyle}>{formatMinor(p.face_minor, p.currency)}</td>
                  <td style={tdStyle}>{formatMinor(p.unseated_rate_minor, p.currency)}</td>
                  <td style={tdStyle}>{formatMinor(p.seated_surcharge_minor, p.currency)}</td>
                  <td style={tdStyle}>{p.active ? "yes" : "no"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p style={mutedStyle}>This version has no credit packs.</p>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// New version form
// ---------------------------------------------------------------------------

export interface VersionFormBodyProps {
  readonly form: VersionForm;
  readonly onChange: (next: VersionForm) => void;
  readonly errors: readonly string[];
  /** Existing versions the operator can copy numbers from. */
  readonly bases: readonly PriceListVersion[];
  readonly onPickBase: (id: string) => void;
  readonly onAddRule: (type: string) => void;
  readonly onAddPack: () => void;
  readonly serverError?: ReactNode;
}

const gridStyle: CSSProperties = { display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))", gap: 10 };
const rowStyle: CSSProperties = { border: "1px solid #e2e8f0", borderRadius: 6, padding: 10, display: "flex", flexDirection: "column", gap: 8 };

function checkbox(label: string, checked: boolean, onChange: (v: boolean) => void, testId: string): JSX.Element {
  return (
    <label style={{ display: "flex", gap: 6, alignItems: "center", fontSize: 13 }}>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} data-testid={testId} />
      {label}
    </label>
  );
}

export function VersionFormBody(p: VersionFormBodyProps): JSX.Element {
  const { form, onChange } = p;
  const set = (patch: Partial<VersionForm>): void => onChange({ ...form, ...patch });
  const setRule = (uid: number, patch: Partial<RuleForm>): void =>
    set({ rules: form.rules.map((r) => (r.uid === uid ? { ...r, ...patch } : r)) });
  const setPack = (uid: number, patch: Partial<PackForm>): void =>
    set({ packs: form.packs.map((x) => (x.uid === uid ? { ...x, ...patch } : x)) });
  const currency = form.currency;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }} data-testid="billing-version-form">
      <p style={hintStyle}>
        A new price is a new version. It starts at the time you pick, which must be in the future, and never changes a sale
        that already happened. Every number is yours: nothing is filled in for you. To save typing, copy the numbers of an
        existing version and change them.
      </p>
      {p.bases.length > 0 ? (
        <div>
          <label style={labelStyle} htmlFor="billing-base">
            Start from an existing version
          </label>
          <select
            id="billing-base"
            style={inputStyle}
            value=""
            onChange={(e) => {
              if (e.target.value !== "") p.onPickBase(e.target.value);
            }}
            data-testid="billing-base"
          >
            <option value="">Pick a version to copy…</option>
            {p.bases.map((b) => (
              <option key={b.id} value={b.id}>
                {b.code} v{b.version} · {b.currency} · {versionStatusLabel(b.status)}
              </option>
            ))}
          </select>
        </div>
      ) : null}
      <div style={gridStyle}>
        <div>
          <label style={labelStyle} htmlFor="billing-code">
            Price list code
          </label>
          <input id="billing-code" style={inputStyle} value={form.code} onChange={(e) => set({ code: e.target.value.trim() })} data-testid="billing-code" />
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-currency">
            Currency
          </label>
          <input
            id="billing-currency"
            style={inputStyle}
            value={form.currency}
            maxLength={3}
            onChange={(e) => set({ currency: e.target.value.toUpperCase().trim() })}
            data-testid="billing-currency"
          />
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-from">
            Starts (UTC)
          </label>
          <input
            id="billing-from"
            type="datetime-local"
            style={inputStyle}
            value={form.effectiveFromLocal}
            onChange={(e) => set({ effectiveFromLocal: e.target.value })}
            data-testid="billing-from"
          />
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-cap">
            Bonus share cap (basis points)
          </label>
          <input id="billing-cap" style={inputStyle} inputMode="numeric" value={form.bonusShareCapBp} onChange={(e) => set({ bonusShareCapBp: e.target.value })} data-testid="billing-cap" />
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-free">
            Free tickets a year (empty: not set)
          </label>
          <input id="billing-free" style={inputStyle} inputMode="numeric" value={form.freeLimit} onChange={(e) => set({ freeLimit: e.target.value })} data-testid="billing-free" />
        </div>
        <div>
          <label style={labelStyle} htmlFor="billing-floor">
            Floor per ticket, minor units (empty: not set)
          </label>
          <input id="billing-floor" style={inputStyle} inputMode="numeric" value={form.floorMinor} onChange={(e) => set({ floorMinor: e.target.value })} data-testid="billing-floor" />
          <span style={mutedStyle}>{minorPreview(form.floorMinor, currency)}</span>
        </div>
      </div>
      <div style={{ display: "flex", gap: 16, flexWrap: "wrap" }}>
        {checkbox("Locked", form.locked, (v) => set({ locked: v }), "billing-locked")}
        {checkbox("Bonus credits pay the seat surcharge", form.bonusCoversSurcharge, (v) => set({ bonusCoversSurcharge: v }), "billing-bonus-surcharge")}
      </div>

      <h4 style={{ margin: "4px 0 0", fontSize: 14 }}>Rules</h4>
      {form.rules.map((r, i) => {
        const shape = ruleShape(r.ruleType);
        return (
          <div key={r.uid} style={rowStyle} data-testid={`billing-rule-${i + 1}`}>
            <div style={{ display: "flex", justifyContent: "space-between", gap: 8, flexWrap: "wrap" }}>
              <strong style={{ fontSize: 13 }}>
                {i + 1}. {shape?.label ?? r.ruleType}
              </strong>
              <button type="button" style={buttonStyle} onClick={() => set({ rules: form.rules.filter((x) => x.uid !== r.uid) })}>
                Remove rule
              </button>
            </div>
            <div style={gridStyle}>
              {shape !== undefined && shape.lines.length > 1 ? (
                <div>
                  <label style={labelStyle}>Line</label>
                  <select style={inputStyle} value={r.line} onChange={(e) => setRule(r.uid, { line: e.target.value })}>
                    {shape.lines.map((l) => (
                      <option key={l} value={l}>
                        {l}
                      </option>
                    ))}
                  </select>
                </div>
              ) : null}
              {shape?.needsSaleCurrency === true ? (
                <div>
                  <label style={labelStyle}>Sale currency</label>
                  <input
                    style={inputStyle}
                    value={r.saleCurrency}
                    maxLength={3}
                    onChange={(e) => setRule(r.uid, { saleCurrency: e.target.value.toUpperCase().trim() })}
                  />
                </div>
              ) : null}
              {(shape?.params ?? []).map((pd) =>
                pd.kind === "bool" ? (
                  <div key={pd.key} style={{ alignSelf: "end" }}>
                    {checkbox(pd.label, r.params[pd.key] === "true", (v) => setRule(r.uid, { params: { ...r.params, [pd.key]: String(v) } }), `billing-rule-${i + 1}-${pd.key}`)}
                  </div>
                ) : (
                  <div key={pd.key}>
                    <label style={labelStyle}>{pd.label}</label>
                    <input
                      style={inputStyle}
                      inputMode="numeric"
                      value={r.params[pd.key] ?? ""}
                      onChange={(e) => setRule(r.uid, { params: { ...r.params, [pd.key]: e.target.value } })}
                      data-testid={`billing-rule-${i + 1}-${pd.key}`}
                    />
                    <span style={mutedStyle}>
                      {pd.kind === "amount"
                        ? minorPreview(r.params[pd.key] ?? "", shape?.needsSaleCurrency === true ? r.saleCurrency || currency : currency)
                        : ""}
                    </span>
                  </div>
                ),
              )}
            </div>
          </div>
        );
      })}
      <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
        {RULE_SHAPES.map((s) => (
          <button key={s.type} type="button" style={buttonStyle} onClick={() => p.onAddRule(s.type)} data-testid={`billing-add-rule-${s.type}`}>
            + {s.label}
          </button>
        ))}
      </div>

      <h4 style={{ margin: "4px 0 0", fontSize: 14 }}>Credit packs</h4>
      {form.packs.map((pk, i) => (
        <div key={pk.uid} style={rowStyle} data-testid={`billing-pack-${i + 1}`}>
          <div style={gridStyle}>
            <div>
              <label style={labelStyle}>Tier</label>
              <input style={inputStyle} inputMode="numeric" value={pk.tier} onChange={(e) => setPack(pk.uid, { tier: e.target.value })} />
            </div>
            <div>
              <label style={labelStyle}>Face value (minor units)</label>
              <input style={inputStyle} inputMode="numeric" value={pk.faceMinor} onChange={(e) => setPack(pk.uid, { faceMinor: e.target.value })} />
              <span style={mutedStyle}>{minorPreview(pk.faceMinor, currency)}</span>
            </div>
            <div>
              <label style={labelStyle}>Ticket without a seat (minor units)</label>
              <input style={inputStyle} inputMode="numeric" value={pk.unseatedRateMinor} onChange={(e) => setPack(pk.uid, { unseatedRateMinor: e.target.value })} />
              <span style={mutedStyle}>{minorPreview(pk.unseatedRateMinor, currency)}</span>
            </div>
            <div>
              <label style={labelStyle}>Seated surcharge (minor units)</label>
              <input style={inputStyle} inputMode="numeric" value={pk.seatedSurchargeMinor} onChange={(e) => setPack(pk.uid, { seatedSurchargeMinor: e.target.value })} />
              <span style={mutedStyle}>{minorPreview(pk.seatedSurchargeMinor, currency)}</span>
            </div>
          </div>
          <div style={{ display: "flex", justifyContent: "space-between", gap: 8, flexWrap: "wrap" }}>
            {checkbox("Can be bought", pk.active, (v) => setPack(pk.uid, { active: v }), `billing-pack-${i + 1}-active`)}
            <button type="button" style={buttonStyle} onClick={() => set({ packs: form.packs.filter((x) => x.uid !== pk.uid) })}>
              Remove pack
            </button>
          </div>
        </div>
      ))}
      <div>
        <button type="button" style={buttonStyle} onClick={p.onAddPack} data-testid="billing-add-pack">
          + Credit pack
        </button>
      </div>

      {p.errors.length > 0 ? (
        <ul style={{ ...warningStyle, margin: 0, paddingLeft: 28 }} data-testid="billing-form-errors">
          {p.errors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      ) : null}
      {p.serverError ? <p style={fieldErrorStyle}>{p.serverError}</p> : null}
    </div>
  );
}
