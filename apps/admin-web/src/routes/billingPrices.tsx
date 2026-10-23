/**
 * Price lists and rates — the superadmin screen of billing wave B-A (task
 * B1-5, spec 08_architecture/42_billing_execution_spec_ru.md card B1-5, 41
 * section 6.9).
 *
 * Three tabs over /v1/admin/billing:
 *   - Price lists: every version of every price list; create a new version
 *     that starts in the future, revoke one before it starts;
 *   - Individual rates: per-organization per-ticket rates; create, set an end,
 *     remove one that has not started;
 *   - Accounts: billing accounts with the marks "permanent venue", "existing
 *     client" and "tools", and the dated terms the operator can add.
 *
 * The page is gated by billing.admin; the server also demands a platform
 * superadmin and X-Admin-Reason on every write (reason.ts asks once per tab).
 * Mock data: NONE. Every number on this screen is typed by the operator or
 * copied from an existing version; the screen has no defaults.
 */
import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ApiError } from "@/lib/api/client";
import {
  buildCreateRequest,
  buildRateRequest,
  buildTermsRequest,
  closeRateOverride,
  createAccountTerms,
  createPriceList,
  createRateOverride,
  currentTerms,
  deleteRateOverride,
  emptyPack,
  emptyRateForm,
  emptyRule,
  emptyVersionForm,
  formFromDetail,
  getPriceList,
  groupSeries,
  listBillingAccounts,
  listPriceLists,
  listRateOverrides,
  localInputToIso,
  priceListCodes,
  revokePriceList,
  termsFormFrom,
  versionStatusLabel,
  type BillingAccount,
  type PriceListDetail,
  type PriceListVersion,
  type RateForm,
  type RateOverride,
  type TermsForm,
  type VersionForm,
  type VersionSeries,
} from "@/lib/api/billingPrices";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import {
  Dialog,
  buttonStyle,
  disabledButtonStyle,
  dangerButtonStyle,
  errorStyle,
  headerRowStyle,
  headingStyle,
  inputStyle,
  labelStyle,
  noticeStyle,
  pageStyle,
  primaryButtonStyle,
  subheadingStyle,
  warningStyle,
} from "./onboardingUi";
import { VersionDetailBody, VersionFormBody, VersionsTable } from "./billingPricesLists";
import { AccountsTable, RateFormBody, RatesTable, TermsFormBody, type OrgChoice } from "./billingPricesRates";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/billing/prices",
  component: BillingPricesRoute,
});

const BILLING_PRICES_NAV_ENTRY = NAV_BY_PATH["/billing/prices"];
if (BILLING_PRICES_NAV_ENTRY === undefined) {
  throw new Error("billing prices route: NAV_BY_PATH['/billing/prices'] missing");
}

function BillingPricesRoute() {
  return (
    <RequirePermission entry={BILLING_PRICES_NAV_ENTRY}>
      <BillingPricesPage />
    </RequirePermission>
  );
}

type Tab = "lists" | "rates" | "accounts";

const TABS: readonly { id: Tab; label: string }[] = [
  { id: "lists", label: "Price lists" },
  { id: "rates", label: "Individual rates" },
  { id: "accounts", label: "Accounts" },
];

/** What the operator is doing in a dialog, if anything. */
type Modal =
  | { kind: "none" }
  | { kind: "detail"; version: PriceListVersion }
  | { kind: "new-version"; form: VersionForm }
  | { kind: "revoke"; version: PriceListVersion }
  | { kind: "new-rate"; form: RateForm }
  | { kind: "close-rate"; rate: RateOverride; endLocal: string }
  | { kind: "remove-rate"; rate: RateOverride }
  | { kind: "terms"; account: BillingAccount; form: TermsForm };

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : "The request failed. Please try again.";
}

function BillingPricesPage(): JSX.Element {
  const qc = useQueryClient();
  const [tab, setTab] = useState<Tab>("lists");
  const [modal, setModal] = useState<Modal>({ kind: "none" });
  const [formErrors, setFormErrors] = useState<readonly string[]>([]);
  const [serverError, setServerError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [warnings, setWarnings] = useState<readonly string[]>([]);

  const lists = useQuery({ queryKey: ["admin", "billing", "price-lists"], queryFn: listPriceLists, retry: false, refetchOnWindowFocus: false });
  const rates = useQuery({ queryKey: ["admin", "billing", "rate-overrides"], queryFn: listRateOverrides, retry: false, refetchOnWindowFocus: false });
  const accounts = useQuery({ queryKey: ["admin", "billing", "accounts"], queryFn: listBillingAccounts, retry: false, refetchOnWindowFocus: false });

  const versions = useMemo(() => lists.data?.items ?? [], [lists.data]);
  const series = useMemo<VersionSeries[]>(() => groupSeries(versions), [versions]);
  const orgs = useMemo<OrgChoice[]>(() => {
    const seen = new Map<string, string>();
    for (const a of accounts.data?.items ?? []) seen.set(a.org_id, a.org_name);
    return [...seen.entries()].map(([id, name]) => ({ id, name })).sort((a, b) => a.name.localeCompare(b.name));
  }, [accounts.data]);

  const detailId = modal.kind === "detail" ? modal.version.id : null;
  const detail = useQuery<PriceListDetail, ApiError>({
    queryKey: ["admin", "billing", "price-list", detailId],
    queryFn: () => getPriceList(detailId ?? ""),
    enabled: detailId !== null,
    retry: false,
    refetchOnWindowFocus: false,
  });

  const close = (): void => {
    setModal({ kind: "none" });
    setFormErrors([]);
    setServerError(null);
  };
  const refresh = (): void => {
    void qc.invalidateQueries({ queryKey: ["admin", "billing"] });
  };
  const done = (text: string, extra: readonly string[] = []): void => {
    close();
    setNotice(text);
    setWarnings(extra);
    refresh();
  };
  const failed = (err: unknown): void => setServerError(messageOf(err));

  const create = useMutation({
    mutationFn: (req: Parameters<typeof createPriceList>[0]) => createPriceList(req),
    onSuccess: (d) =>
      done(`Version ${d.version} of ${d.code} (${d.currency}) is saved. It starts ${d.effective_from}.`, d.warnings ?? []),
    onError: failed,
  });
  const revoke = useMutation({
    mutationFn: (id: string) => revokePriceList(id),
    onSuccess: (d) => done(`Version ${d.version} of ${d.code} (${d.currency}) is revoked.`),
    onError: failed,
  });
  const addRate = useMutation({
    mutationFn: (req: Parameters<typeof createRateOverride>[0]) => createRateOverride(req),
    onSuccess: (r) =>
      done(
        r.closed_rate_ids && r.closed_rate_ids.length > 0
          ? "The individual rate is saved. The earlier open rate ends when this one starts."
          : "The individual rate is saved.",
      ),
    onError: failed,
  });
  const endRate = useMutation({
    mutationFn: (v: { id: string; to: string }) => closeRateOverride(v.id, v.to),
    onSuccess: () => done("The end of the individual rate is saved."),
    onError: failed,
  });
  const removeRate = useMutation({
    mutationFn: (id: string) => deleteRateOverride(id),
    onSuccess: () => done("The individual rate is removed."),
    onError: failed,
  });
  const addTerms = useMutation({
    mutationFn: (v: { id: string; req: Parameters<typeof createAccountTerms>[1] }) => createAccountTerms(v.id, v.req),
    onSuccess: () => done("The new terms are saved. They start at the time you picked."),
    onError: failed,
  });

  const busy = create.isPending || revoke.isPending || addRate.isPending || endRate.isPending || removeRate.isPending || addTerms.isPending;

  // ---- opening dialogs ----
  const openNewVersion = (s?: VersionSeries): void => {
    setServerError(null);
    setFormErrors([]);
    const latest = s?.versions[0];
    setModal({ kind: "new-version", form: emptyVersionForm(s?.code ?? "", s?.currency ?? "") });
    if (latest !== undefined) void pickBase(latest.id, emptyVersionForm(s?.code ?? "", s?.currency ?? ""));
  };
  const pickBase = async (id: string, keep: VersionForm | null): Promise<void> => {
    try {
      const d = await getPriceList(id);
      const base = formFromDetail(d);
      setModal((m) => {
        if (m.kind !== "new-version") return m;
        const target = keep ?? m.form;
        return { kind: "new-version", form: { ...base, code: target.code || base.code, currency: target.currency || base.currency } };
      });
    } catch (err) {
      failed(err);
    }
  };

  const submitVersion = (form: VersionForm): void => {
    const built = buildCreateRequest(form);
    if (!built.ok) {
      setFormErrors(built.errors);
      return;
    }
    setFormErrors([]);
    setServerError(null);
    create.mutate(built.request);
  };
  const submitRate = (form: RateForm): void => {
    const built = buildRateRequest(form);
    if (!built.ok) {
      setFormErrors(built.errors);
      return;
    }
    setFormErrors([]);
    setServerError(null);
    addRate.mutate(built.request);
  };
  const submitTerms = (account: BillingAccount, form: TermsForm): void => {
    const built = buildTermsRequest(form, account.account_kind);
    if (!built.ok) {
      setFormErrors(built.errors);
      return;
    }
    setFormErrors([]);
    setServerError(null);
    addTerms.mutate({ id: account.id, req: built.request });
  };
  const submitEnd = (rate: RateOverride, endLocal: string): void => {
    const iso = localInputToIso(endLocal);
    if (iso === null || new Date(iso).getTime() <= Date.now()) {
      setFormErrors(["End: pick a date and time in the future."]);
      return;
    }
    setFormErrors([]);
    setServerError(null);
    endRate.mutate({ id: rate.id, to: iso });
  };

  const loadError = lists.error ?? rates.error ?? accounts.error;

  return (
    <div style={pageStyle} data-testid="billing-prices-page">
      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle}>Price lists and rates</h1>
          <p style={subheadingStyle}>
            Versions of the price lists, the individual rates of organizations, and the terms of billing accounts. A new
            price is always a new version that starts in the future. Every change asks for a reason and is written to the
            audit log. Times are in UTC.
          </p>
        </div>
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          <button type="button" style={buttonStyle} onClick={refresh} data-testid="billing-refresh">
            Refresh
          </button>
          {tab === "lists" ? (
            <button type="button" style={primaryButtonStyle} onClick={() => openNewVersion()} data-testid="billing-new-version">
              New price list version
            </button>
          ) : null}
          {tab === "rates" ? (
            <button
              type="button"
              style={primaryButtonStyle}
              onClick={() => {
                setServerError(null);
                setFormErrors([]);
                setModal({ kind: "new-rate", form: emptyRateForm() });
              }}
              data-testid="billing-new-rate"
            >
              New individual rate
            </button>
          ) : null}
        </div>
      </div>

      <div role="tablist" style={{ display: "flex", gap: 6, marginBottom: 16, flexWrap: "wrap" }}>
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            style={{ ...buttonStyle, ...(tab === t.id ? { background: "#0f172a", color: "#ffffff", borderColor: "#0f172a" } : {}) }}
            onClick={() => setTab(t.id)}
            data-testid={`billing-tab-${t.id}`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {notice !== null ? (
        <div style={noticeStyle} role="status" data-testid="billing-notice">
          {notice}
        </div>
      ) : null}
      {warnings.length > 0 ? (
        <div style={{ ...warningStyle, marginBottom: 16 }} data-testid="billing-warnings">
          <strong>Check this:</strong>
          <ul style={{ margin: "4px 0 0", paddingLeft: 20 }}>
            {warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </div>
      ) : null}
      {loadError ? (
        <div style={errorStyle} role="alert" data-testid="billing-load-error">
          {messageOf(loadError)}
        </div>
      ) : null}

      {tab === "lists" ? (
        <VersionsTable
          series={series}
          onOpen={(v) => setModal({ kind: "detail", version: v })}
          onRevoke={(v) => {
            setServerError(null);
            setModal({ kind: "revoke", version: v });
          }}
          onNewVersion={openNewVersion}
        />
      ) : null}
      {tab === "rates" ? (
        <RatesTable
          rates={rates.data?.items ?? []}
          onClose={(r) => {
            setServerError(null);
            setFormErrors([]);
            setModal({ kind: "close-rate", rate: r, endLocal: "" });
          }}
          onRemove={(r) => {
            setServerError(null);
            setModal({ kind: "remove-rate", rate: r });
          }}
        />
      ) : null}
      {tab === "accounts" ? (
        <AccountsTable
          accounts={accounts.data?.items ?? []}
          onChangeTerms={(a) => {
            setServerError(null);
            setFormErrors([]);
            setModal({ kind: "terms", account: a, form: termsFormFrom(currentTerms(a)) });
          }}
        />
      ) : null}

      {modal.kind === "detail" ? (
        <Dialog
          title={`${modal.version.code} v${modal.version.version} · ${modal.version.currency}`}
          testId="billing-detail-dialog"
          onClose={close}
          footer={
            <button type="button" style={buttonStyle} onClick={close}>
              Close
            </button>
          }
        >
          {detail.isPending ? <p>Loading…</p> : null}
          {detail.error ? <div style={errorStyle}>{messageOf(detail.error)}</div> : null}
          {detail.data ? <VersionDetailBody detail={detail.data} /> : null}
        </Dialog>
      ) : null}

      {modal.kind === "new-version" ? (
        <Dialog
          title="New price list version"
          testId="billing-version-dialog"
          onClose={close}
          onSubmit={() => submitVersion(modal.form)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-version-save">
                Save version
              </button>
            </>
          }
        >
          <VersionFormBody
            form={modal.form}
            onChange={(form) => setModal({ kind: "new-version", form })}
            errors={formErrors}
            serverError={serverError}
            bases={versions}
            onPickBase={(id) => void pickBase(id, null)}
            onAddRule={(type) => setModal({ kind: "new-version", form: { ...modal.form, rules: [...modal.form.rules, emptyRule(type, modal.form.currency)] } })}
            onAddPack={() => setModal({ kind: "new-version", form: { ...modal.form, packs: [...modal.form.packs, emptyPack()] } })}
          />
        </Dialog>
      ) : null}

      {modal.kind === "revoke" ? (
        <Dialog
          title="Revoke this version?"
          testId="billing-revoke-dialog"
          onClose={close}
          onSubmit={() => revoke.mutate(modal.version.id)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Keep it
              </button>
              <button type="submit" style={{ ...dangerButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-revoke-confirm">
                Revoke version
              </button>
            </>
          }
        >
          <p style={{ margin: 0, lineHeight: 1.5 }}>
            {modal.version.code} v{modal.version.version} ({modal.version.currency}) is {versionStatusLabel(modal.version.status).toLowerCase()}{" "}
            and starts {modal.version.effective_from}. After you revoke it, events are rated by the version before it. A
            version that has started cannot be revoked: publish a new version instead.
          </p>
          {serverError ? <div style={errorStyle}>{serverError}</div> : null}
        </Dialog>
      ) : null}

      {modal.kind === "new-rate" ? (
        <Dialog
          title="New individual rate"
          testId="billing-rate-dialog"
          onClose={close}
          onSubmit={() => submitRate(modal.form)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-rate-save">
                Save rate
              </button>
            </>
          }
        >
          <RateFormBody form={modal.form} onChange={(form) => setModal({ kind: "new-rate", form })} orgs={orgs} errors={formErrors} serverError={serverError} />
        </Dialog>
      ) : null}

      {modal.kind === "close-rate" ? (
        <Dialog
          title="Set the end of the rate"
          testId="billing-end-dialog"
          onClose={close}
          onSubmit={() => submitEnd(modal.rate, modal.endLocal)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-end-save">
                Save end
              </button>
            </>
          }
        >
          <p style={{ margin: 0, lineHeight: 1.5 }}>
            The rate of {modal.rate.org_name ?? modal.rate.org_id} in {modal.rate.currency} ends at the time you pick. It must
            be in the future: sales the rate already priced stay as they are.
          </p>
          <div>
            <label style={labelStyle} htmlFor="billing-end-to">
              Ends (UTC)
            </label>
            <input
              id="billing-end-to"
              type="datetime-local"
              style={inputStyle}
              value={modal.endLocal}
              onChange={(e) => setModal({ kind: "close-rate", rate: modal.rate, endLocal: e.target.value })}
              data-testid="billing-end-to"
            />
          </div>
          {formErrors.length > 0 ? (
            <ul style={{ ...warningStyle, margin: 0, paddingLeft: 28 }} data-testid="billing-form-errors">
              {formErrors.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
          ) : null}
          {serverError ? <div style={errorStyle}>{serverError}</div> : null}
        </Dialog>
      ) : null}

      {modal.kind === "remove-rate" ? (
        <Dialog
          title="Remove this rate?"
          testId="billing-remove-dialog"
          onClose={close}
          onSubmit={() => removeRate.mutate(modal.rate.id)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Keep it
              </button>
              <button type="submit" style={{ ...dangerButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-remove-confirm">
                Remove rate
              </button>
            </>
          }
        >
          <p style={{ margin: 0, lineHeight: 1.5 }}>
            The rate of {modal.rate.org_name ?? modal.rate.org_id} in {modal.rate.currency} has not started, so it can be
            removed. The audit log keeps its numbers.
          </p>
          {serverError ? <div style={errorStyle}>{serverError}</div> : null}
        </Dialog>
      ) : null}

      {modal.kind === "terms" ? (
        <Dialog
          title="Change account terms"
          testId="billing-terms-dialog"
          onClose={close}
          onSubmit={() => submitTerms(modal.account, modal.form)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-terms-save">
                Save terms
              </button>
            </>
          }
        >
          <TermsFormBody
            account={modal.account}
            form={modal.form}
            onChange={(form) => setModal({ kind: "terms", account: modal.account, form })}
            priceLists={priceListCodes(versions, modal.account.currency)}
            errors={formErrors}
            serverError={serverError}
          />
        </Dialog>
      ) : null}
    </div>
  );
}
