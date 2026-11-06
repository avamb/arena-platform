/**
 * The "Modules" tab of the organization drawer (spec 08_architecture/61, section 6): the shop as a paid module
 * of one organization. The superadmin sees the state (connected since ..., disconnected since ..., never
 * connected), whether the storefront is open, the domain and the monthly price, and acts on it: connect,
 * change the price, disconnect, set the domain. The history of periods sits underneath.
 *
 * A period is never edited or deleted: it is ended, and a new one is created. Dates only go forward (the API
 * answers 422 for a day in the past), and every write asks for a reason of its own, kept in the audit log.
 *
 * The presentational parts (`ShopModuleView`, the form bodies, `PeriodsTable`) take plain props so they can be
 * rendered without a router or a query client; `OrganizationModulesTab` owns the queries and the writes.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import {
  buildConnectRequest,
  buildEndRequest,
  buildRepriceRequest,
  canConnect,
  canEnd,
  canReprice,
  checkDomain,
  connectShopModule,
  currentPeriod,
  domainUpdateBody,
  effectivePriceLabel,
  emptyConnectForm,
  emptyEndForm,
  emptyRepriceForm,
  endOrgModule,
  getOrgModules,
  getShopSettings,
  moduleErrorMessage,
  periodStatusLabel,
  priceLabel,
  putShopSettings,
  repriceOrgModule,
  shopModeLabel,
  shopPeriodState,
  shopPeriods,
  type AdminOrgModulesResponse,
  type ConnectForm,
  type EndForm,
  type OrgModulePeriod,
  type PricingChoice,
  type PricingFields,
  type RepriceForm,
} from "@/lib/api/orgModules";
import {
  Dialog,
  Pill,
  buttonStyle,
  disabledButtonStyle,
  errorStyle,
  inputStyle,
  labelStyle,
  mutedStyle,
  noticeStyle,
  primaryButtonStyle,
  sectionStyle,
  sectionTitleStyle,
  tableStyle,
  tdStyle,
  thStyle,
} from "./onboardingUi";

export type ModuleAction = "connect" | "reprice" | "end" | "domain";

type Modal =
  | { kind: "none" }
  | { kind: "connect"; form: ConnectForm }
  | { kind: "reprice"; period: OrgModulePeriod; form: RepriceForm }
  | { kind: "end"; period: OrgModulePeriod; form: EndForm }
  | { kind: "domain"; domain: string | null; reason: string };

const PRICING_OPTIONS: ReadonlyArray<{ value: PricingChoice; label: string }> = [
  { value: "price_list", label: "Price list" },
  { value: "individual", label: "Individual price" },
  { value: "free", label: "Free" },
];

// ---------------------------------------------------------------------------
// Presentational parts
// ---------------------------------------------------------------------------

/** The state line: "Connected since ...", "Disconnected since ...", "Never connected". */
export function stateText(items: readonly OrgModulePeriod[]): string {
  const s = shopPeriodState(items);
  switch (s.kind) {
    case "connected":
      return s.until != null ? `Connected since ${s.since}, the last day is ${s.until}` : `Connected since ${s.since}`;
    case "scheduled":
      return `Connects on ${s.from}`;
    case "disconnected":
      return `Disconnected since ${s.since}`;
    default:
      return "Never connected";
  }
}

export function ShopModuleView({
  data,
  now,
  notice,
  onAction,
}: {
  data: AdminOrgModulesResponse;
  now?: Date;
  notice?: string | null;
  onAction: (a: ModuleAction) => void;
}): JSX.Element {
  const items = data.items;
  const shop = data.shop;
  const domainOpen = shop.module_active;
  return (
    <section style={sectionStyle} data-testid="org-module-shop">
      <h3 style={sectionTitleStyle}>Shop</h3>
      {notice != null ? (
        <div style={noticeStyle} role="status" data-testid="org-module-notice">
          {notice}
        </div>
      ) : null}
      <dl style={{ display: "grid", gridTemplateColumns: "max-content 1fr", gap: "6px 16px", margin: "0 0 12px 0", fontSize: 13 }}>
        <dt style={mutedStyle}>State</dt>
        <dd style={{ margin: 0 }} data-testid="org-module-state">
          {stateText(items)}
          {" "}
          <Pill tone={shop.mode === "full" ? "good" : shop.mode === "orders_only" ? "warn" : "muted"} testId="org-module-mode">
            {shopModeLabel(shop.mode)}
          </Pill>
        </dd>
        <dt style={mutedStyle}>Storefront</dt>
        <dd style={{ margin: 0 }} data-testid="org-module-storefront">
          {shop.enabled ? "Open" : "Closed"}
          <span style={{ ...mutedStyle, display: "inline", marginLeft: 8 }}>The organization opens and closes it itself.</span>
        </dd>
        <dt style={mutedStyle}>Domain</dt>
        <dd style={{ margin: 0 }} data-testid="org-module-domain">
          {shop.shop_domain != null && shop.shop_domain !== "" ? shop.shop_domain : "Not set"}{" "}
          <button
            type="button"
            style={{ ...buttonStyle, ...(domainOpen ? {} : disabledButtonStyle) }}
            disabled={!domainOpen}
            onClick={() => onAction("domain")}
            data-testid="org-module-domain-edit"
            title={domainOpen ? undefined : "The shop settings are closed while the module is not connected."}
          >
            Domain
          </button>
        </dd>
        <dt style={mutedStyle}>Monthly price</dt>
        <dd style={{ margin: 0 }} data-testid="org-module-price">
          {effectivePriceLabel(shop)}
        </dd>
        {shop.open_shop_orders ? (
          <>
            <dt style={mutedStyle}>Open orders</dt>
            <dd style={{ margin: 0 }} data-testid="org-module-open-orders">
              The organization has open shop orders. Orders stay available after the module ends.
            </dd>
          </>
        ) : null}
      </dl>
      <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
        {canConnect(items) ? (
          <button type="button" style={primaryButtonStyle} onClick={() => onAction("connect")} data-testid="org-module-connect">
            Connect
          </button>
        ) : null}
        {canReprice(items) ? (
          <button type="button" style={buttonStyle} onClick={() => onAction("reprice")} data-testid="org-module-reprice">
            Change the price
          </button>
        ) : null}
        {canEnd(items, now) ? (
          <button type="button" style={buttonStyle} onClick={() => onAction("end")} data-testid="org-module-end">
            Disconnect
          </button>
        ) : null}
      </div>
      <PeriodsTable items={items} />
    </section>
  );
}

/** The history of periods, oldest first. */
export function PeriodsTable({ items }: { items: readonly OrgModulePeriod[] }): JSX.Element {
  const periods = shopPeriods(items);
  if (periods.length === 0) {
    return (
      <p style={{ ...mutedStyle, marginTop: 12 }} data-testid="org-module-empty">
        The shop module was never connected for this organization.
      </p>
    );
  }
  return (
    <div style={{ overflowX: "auto", marginTop: 12 }}>
      <table style={tableStyle} data-testid="org-module-periods">
        <thead>
          <tr>
            {["First day", "Last day", "Monthly price", "State", "Reason"].map((h) => (
              <th key={h} style={thStyle}>
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {periods.map((p) => (
            <tr key={p.id} data-testid={`org-module-period-${p.id}`}>
              <td style={tdStyle}>{p.starts_on}</td>
              <td style={tdStyle}>{p.ends_on ?? "No end"}</td>
              <td style={tdStyle}>{priceLabel(p.monthly_price_minor, p.currency)}</td>
              <td style={tdStyle}>{periodStatusLabel(p.status)}</td>
              <td style={tdStyle}>{p.reason ?? ""}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** The price choice shared by "connect" and "change the price". */
export function PricingFieldsBody({ value, onChange }: { value: PricingFields; onChange: (v: PricingFields) => void }): JSX.Element {
  return (
    <>
      <label style={labelStyle}>
        Monthly price
        <select
          style={inputStyle}
          value={value.pricing}
          onChange={(e) => onChange({ ...value, pricing: e.target.value as PricingChoice })}
          data-testid="org-module-pricing"
        >
          {PRICING_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </label>
      {value.pricing === "individual" ? (
        <div style={{ display: "grid", gridTemplateColumns: "2fr 1fr", gap: 10 }}>
          <label style={labelStyle}>
            Amount per month
            <input
              inputMode="decimal"
              style={inputStyle}
              value={value.amount}
              onChange={(e) => onChange({ ...value, amount: e.target.value })}
              data-testid="org-module-amount"
            />
          </label>
          <label style={labelStyle}>
            Currency
            <input
              style={inputStyle}
              maxLength={3}
              placeholder="billing"
              value={value.currency}
              onChange={(e) => onChange({ ...value, currency: e.target.value })}
              data-testid="org-module-currency"
            />
          </label>
        </div>
      ) : null}
      <p style={{ ...mutedStyle, fontSize: 12 }}>
        {value.pricing === "price_list"
          ? "The price list sets the price (0 today)."
          : value.pricing === "free"
            ? "The module costs nothing for this organization."
            : "Leave the currency empty for the billing currency of the organization. A different currency is refused."}
      </p>
    </>
  );
}

export function ReasonField({ value, onChange }: { value: string; onChange: (v: string) => void }): JSX.Element {
  return (
    <label style={labelStyle}>
      Reason (kept in the audit log)
      <input style={inputStyle} value={value} onChange={(e) => onChange(e.target.value)} data-testid="org-module-reason" />
    </label>
  );
}

export function ConnectFormBody({ form, onChange }: { form: ConnectForm; onChange: (f: ConnectForm) => void }): JSX.Element {
  return (
    <>
      <label style={labelStyle}>
        First day (UTC, today or later)
        <input type="date" style={inputStyle} value={form.startsOn} onChange={(e) => onChange({ ...form, startsOn: e.target.value })} data-testid="org-module-starts" />
      </label>
      <PricingFieldsBody value={form} onChange={(v) => onChange({ ...form, ...v })} />
      <ReasonField value={form.reason} onChange={(reason) => onChange({ ...form, reason })} />
    </>
  );
}

export function RepriceFormBody({ form, onChange }: { form: RepriceForm; onChange: (f: RepriceForm) => void }): JSX.Element {
  return (
    <>
      <label style={labelStyle}>
        New price from (UTC, today or later)
        <input type="date" style={inputStyle} value={form.from} onChange={(e) => onChange({ ...form, from: e.target.value })} data-testid="org-module-from" />
      </label>
      <PricingFieldsBody value={form} onChange={(v) => onChange({ ...form, ...v })} />
      <ReasonField value={form.reason} onChange={(reason) => onChange({ ...form, reason })} />
    </>
  );
}

export function EndFormBody({ form, onChange }: { form: EndForm; onChange: (f: EndForm) => void }): JSX.Element {
  return (
    <>
      <p style={{ margin: 0, fontSize: 13 }}>
        From the day after the last day the storefront is closed and the shop falls to orders only, which stays for good while the organization has a
        paid or an open order. Products and pages are kept. Days already charged stay charged.
      </p>
      <label style={labelStyle}>
        Last day (UTC, yesterday or later)
        <input type="date" style={inputStyle} value={form.endsOn} onChange={(e) => onChange({ ...form, endsOn: e.target.value })} data-testid="org-module-ends" />
      </label>
      <ReasonField value={form.reason} onChange={(reason) => onChange({ ...form, reason })} />
    </>
  );
}

export function DomainFormBody({
  domain,
  reason,
  configured,
  onDomain,
  onReason,
}: {
  domain: string;
  reason: string;
  /** False while the organization never saved its shop settings. */
  configured: boolean;
  onDomain: (v: string) => void;
  onReason: (v: string) => void;
}): JSX.Element {
  if (!configured) {
    return (
      <p style={{ margin: 0, fontSize: 13 }} data-testid="org-module-domain-unconfigured">
        The organization has not saved its shop settings yet. The domain is stored with them, so ask the organization to save the settings first.
      </p>
    );
  }
  return (
    <>
      <label style={labelStyle}>
        Storefront domain (empty removes it)
        <input style={inputStyle} value={domain} placeholder="shop.example.com" onChange={(e) => onDomain(e.target.value)} data-testid="org-module-domain-input" />
      </label>
      <p style={{ ...mutedStyle, fontSize: 12 }}>Only the domain changes. The other shop settings are sent back as they were read.</p>
      <ReasonField value={reason} onChange={onReason} />
    </>
  );
}

export function Problems({ errors, serverError }: { errors: readonly string[]; serverError: string | null }): JSX.Element | null {
  if (errors.length === 0 && serverError === null) return null;
  return (
    <div style={errorStyle} role="alert" data-testid="org-module-problems">
      {errors.map((e) => (
        <div key={e}>{e}</div>
      ))}
      {serverError !== null ? <div>{serverError}</div> : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// The tab
// ---------------------------------------------------------------------------

export function OrganizationModulesTab({ orgId }: { orgId: string }): JSX.Element {
  const qc = useQueryClient();
  const key = ["admin", "org-modules", orgId] as const;
  const [modal, setModal] = useState<Modal>({ kind: "none" });
  const [errors, setErrors] = useState<readonly string[]>([]);
  const [serverError, setServerError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const modules = useQuery({ queryKey: key, queryFn: () => getOrgModules(orgId), retry: false, refetchOnWindowFocus: false });

  const close = (): void => {
    setModal({ kind: "none" });
    setErrors([]);
    setServerError(null);
  };
  const done = (text: string): void => {
    close();
    setNotice(text);
    void qc.invalidateQueries({ queryKey: key });
  };

  const connect = useMutation({
    mutationFn: (v: { request: Parameters<typeof connectShopModule>[1]; reason: string }) => connectShopModule(orgId, v.request, v.reason),
    onSuccess: (p) => done(`The shop module is connected from ${p.starts_on}.`),
    onError: (e) => setServerError(moduleErrorMessage(e)),
  });
  const reprice = useMutation({
    mutationFn: (v: { id: string; request: Parameters<typeof repriceOrgModule>[1]; reason: string }) => repriceOrgModule(v.id, v.request, v.reason),
    onSuccess: (p) => done(`The new price applies from ${p.starts_on}.`),
    onError: (e) => setServerError(moduleErrorMessage(e)),
  });
  const end = useMutation({
    mutationFn: (v: { id: string; request: Parameters<typeof endOrgModule>[1]; reason: string }) => endOrgModule(v.id, v.request, v.reason),
    onSuccess: (p) => done(`The last day of the shop module is ${p.ends_on ?? ""}.`),
    onError: (e) => setServerError(moduleErrorMessage(e)),
  });

  // The domain lives in the shop settings, which PUT replaces as a whole: read them fresh when the dialog opens.
  const domainOpen = modal.kind === "domain";
  const settings = useQuery({
    queryKey: ["admin", "org-modules", orgId, "shop-settings"],
    queryFn: () => getShopSettings(orgId),
    enabled: domainOpen,
    retry: false,
    refetchOnWindowFocus: false,
    gcTime: 0,
  });
  const saveDomain = useMutation({
    mutationFn: (v: { domain: string; reason: string }) => {
      if (settings.data === undefined) throw new Error("The shop settings are not loaded.");
      return putShopSettings(orgId, domainUpdateBody(settings.data, v.domain), v.reason);
    },
    onSuccess: (s) => done(s.shop_domain != null && s.shop_domain !== "" ? `The domain is ${s.shop_domain}.` : "The domain is removed."),
    onError: (e) => setServerError(moduleErrorMessage(e)),
  });
  const busy = connect.isPending || reprice.isPending || end.isPending || saveDomain.isPending;

  const open = (a: ModuleAction): void => {
    if (modules.data === undefined) return;
    setNotice(null);
    setErrors([]);
    setServerError(null);
    const items = modules.data.items;
    const period = currentPeriod(items);
    if (a === "connect") setModal({ kind: "connect", form: emptyConnectForm() });
    else if (a === "reprice" && period !== null) setModal({ kind: "reprice", period, form: emptyRepriceForm(period) });
    else if (a === "end" && period !== null) setModal({ kind: "end", period, form: emptyEndForm() });
    else if (a === "domain") setModal({ kind: "domain", domain: null, reason: "" });
  };

  const submit = (): void => {
    setServerError(null);
    if (modal.kind === "connect") {
      const built = buildConnectRequest(modal.form);
      if (!built.ok) return setErrors(built.errors);
      setErrors([]);
      connect.mutate({ request: built.request, reason: built.reason });
    } else if (modal.kind === "reprice") {
      const built = buildRepriceRequest(modal.form, modal.period);
      if (!built.ok) return setErrors(built.errors);
      setErrors([]);
      reprice.mutate({ id: modal.period.id, request: built.request, reason: built.reason });
    } else if (modal.kind === "end") {
      const built = buildEndRequest(modal.form, modal.period);
      if (!built.ok) return setErrors(built.errors);
      setErrors([]);
      end.mutate({ id: modal.period.id, request: built.request, reason: built.reason });
    } else if (modal.kind === "domain") {
      const domain = modal.domain ?? settings.data?.shop_domain ?? "";
      const problems: string[] = [];
      const dp = checkDomain(domain);
      if (dp !== null) problems.push(dp);
      if (modal.reason.trim() === "") problems.push("Reason: write why. It is kept in the audit log.");
      if (problems.length > 0) return setErrors(problems);
      setErrors([]);
      saveDomain.mutate({ domain, reason: modal.reason.trim() });
    }
  };

  const footer = (saveTestId: string, canSave = true): JSX.Element => (
    <>
      <button type="button" style={buttonStyle} onClick={close}>
        Cancel
      </button>
      <button
        type="submit"
        style={{ ...primaryButtonStyle, ...(busy || !canSave ? disabledButtonStyle : {}) }}
        disabled={busy || !canSave}
        data-testid={saveTestId}
      >
        Save
      </button>
    </>
  );

  return (
    <div data-testid="org-modules-tab">
      {modules.isPending ? <p style={mutedStyle}>Loading...</p> : null}
      {modules.error ? (
        <div style={errorStyle} role="alert" data-testid="org-modules-error">
          {moduleErrorMessage(modules.error)}
        </div>
      ) : null}
      {modules.data !== undefined ? <ShopModuleView data={modules.data} notice={notice} onAction={open} /> : null}

      {modal.kind === "connect" ? (
        <Dialog title="Connect the shop module" testId="org-module-connect-dialog" onClose={close} onSubmit={submit} footer={footer("org-module-connect-save")}>
          <ConnectFormBody form={modal.form} onChange={(form) => setModal({ kind: "connect", form })} />
          <Problems errors={errors} serverError={serverError} />
        </Dialog>
      ) : null}

      {modal.kind === "reprice" ? (
        <Dialog title="Change the monthly price" testId="org-module-reprice-dialog" onClose={close} onSubmit={submit} footer={footer("org-module-reprice-save")}>
          <p style={{ margin: 0, fontSize: 13 }}>
            The current period ends the day before and a new one starts with the new price. Current period: from {modal.period.starts_on},{" "}
            {priceLabel(modal.period.monthly_price_minor, modal.period.currency).toLowerCase()}.
          </p>
          <RepriceFormBody form={modal.form} onChange={(form) => setModal({ ...modal, form })} />
          <Problems errors={errors} serverError={serverError} />
        </Dialog>
      ) : null}

      {modal.kind === "end" ? (
        <Dialog title="Disconnect the shop module" testId="org-module-end-dialog" onClose={close} onSubmit={submit} footer={footer("org-module-end-save")}>
          <EndFormBody form={modal.form} onChange={(form) => setModal({ ...modal, form })} />
          <Problems errors={errors} serverError={serverError} />
        </Dialog>
      ) : null}

      {modal.kind === "domain" ? (
        <Dialog
          title="Storefront domain"
          testId="org-module-domain-dialog"
          onClose={close}
          onSubmit={submit}
          footer={footer("org-module-domain-save", settings.data?.configured === true)}
        >
          {settings.isPending ? <p style={mutedStyle}>Loading the shop settings...</p> : null}
          {settings.error ? (
            <div style={errorStyle} role="alert">
              {moduleErrorMessage(settings.error)}
            </div>
          ) : null}
          {settings.data !== undefined ? (
            <DomainFormBody
              domain={modal.domain ?? settings.data.shop_domain ?? ""}
              reason={modal.reason}
              configured={settings.data.configured}
              onDomain={(domain) => setModal({ ...modal, domain })}
              onReason={(reason) => setModal({ ...modal, reason })}
            />
          ) : null}
          <Problems errors={errors} serverError={serverError} />
        </Dialog>
      ) : null}
    </div>
  );
}
