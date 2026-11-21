/**
 * Bank payments — the superadmin screen for incoming bank payments (billing
 * wave B3/B4).
 *
 * The queue of payments that wait for an operator comes first, each with the
 * reason the bank feed could not match it, in words. An operator ties one to an
 * issued invoice, or ignores it with a reason. A bank without a feed is covered
 * by a manual import of a pasted statement (JSON or semicolon-separated lines).
 *
 * Gated by billing.admin; the server also demands a platform superadmin and
 * X-Admin-Reason on every write. Mock data: NONE.
 */
import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ApiError } from "@/lib/api/client";
import { listBillingAccounts } from "@/lib/api/billingPrices";
import {
  BANK_STATUSES,
  buildReasonRequest,
  ignoreBankTransaction,
  importBankTransactions,
  ingestSummary,
  listBankTransactions,
  listInvoices,
  matchBankTransaction,
  parseStatement,
  type BankStatus,
  type BankTransaction,
} from "@/lib/api/billingInvoices";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import {
  Dialog,
  buttonStyle,
  disabledButtonStyle,
  errorStyle,
  headerRowStyle,
  headingStyle,
  noticeStyle,
  pageStyle,
  primaryButtonStyle,
  subheadingStyle,
} from "./onboardingUi";
import { BankTable, IgnoreFormBody, ImportFormBody, MatchFormBody } from "./billingFeesBankView";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/billing/bank",
  component: BillingBankRoute,
});

const BILLING_BANK_NAV_ENTRY = NAV_BY_PATH["/billing/bank"];
if (BILLING_BANK_NAV_ENTRY === undefined) {
  throw new Error("billing bank route: NAV_BY_PATH['/billing/bank'] missing");
}

function BillingBankRoute() {
  return (
    <RequirePermission entry={BILLING_BANK_NAV_ENTRY}>
      <BillingBankPage />
    </RequirePermission>
  );
}

type Modal =
  | { kind: "none" }
  | { kind: "match"; tx: BankTransaction; invoiceId: string }
  | { kind: "ignore"; tx: BankTransaction; reason: string }
  | { kind: "import"; text: string };

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : "We did not complete the request. Please try again.";
}

function BillingBankPage(): JSX.Element {
  const qc = useQueryClient();
  const [status, setStatus] = useState<BankStatus>("unmatched");
  const [modal, setModal] = useState<Modal>({ kind: "none" });
  const [formErrors, setFormErrors] = useState<readonly string[]>([]);
  const [serverError, setServerError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const txs = useQuery({
    queryKey: ["admin", "billing", "bank-transactions", status],
    queryFn: () => listBankTransactions(status),
    retry: false,
    refetchOnWindowFocus: false,
  });
  // Issued invoices are what a waiting payment can be matched to; paid ones
  // are read so a matched payment can show the number it paid.
  const issued = useQuery({ queryKey: ["admin", "billing", "invoices", "issued"], queryFn: () => listInvoices("issued"), retry: false, refetchOnWindowFocus: false });
  const paid = useQuery({
    queryKey: ["admin", "billing", "invoices", "paid"],
    queryFn: () => listInvoices("paid"),
    retry: false,
    refetchOnWindowFocus: false,
    enabled: status === "matched",
  });
  const accounts = useQuery({ queryKey: ["admin", "billing", "accounts"], queryFn: listBillingAccounts, retry: false, refetchOnWindowFocus: false });

  const orgNames = useMemo(() => {
    const m = new Map<string, string>();
    for (const a of accounts.data?.items ?? []) m.set(a.org_id, a.org_name);
    return m as ReadonlyMap<string, string>;
  }, [accounts.data]);
  const issuedList = useMemo(() => issued.data?.invoices ?? [], [issued.data]);
  const allInvoices = useMemo(() => [...issuedList, ...(paid.data?.invoices ?? [])], [issuedList, paid.data]);

  const close = (): void => {
    setModal({ kind: "none" });
    setFormErrors([]);
    setServerError(null);
  };
  const refresh = (): void => {
    void qc.invalidateQueries({ queryKey: ["admin", "billing"] });
  };
  const done = (text: string): void => {
    close();
    setNotice(text);
    refresh();
  };
  const failed = (err: unknown): void => setServerError(messageOf(err));

  const match = useMutation({
    mutationFn: (v: { txId: string; invoiceId: string }) => matchBankTransaction(v.txId, v.invoiceId),
    onSuccess: () => done("We linked the payment to the invoice. The invoice is paid."),
    onError: failed,
  });
  const ignore = useMutation({
    mutationFn: (v: { txId: string; reason: string }) => ignoreBankTransaction(v.txId, v.reason),
    onSuccess: () => done("We ignored the payment."),
    onError: failed,
  });
  const importer = useMutation({
    mutationFn: (transfers: Parameters<typeof importBankTransactions>[0]) => importBankTransactions(transfers),
    onSuccess: (r) => done(`We imported the statement: ${ingestSummary(r)}`),
    onError: failed,
  });
  const busy = match.isPending || ignore.isPending || importer.isPending;

  const submitMatch = (tx: BankTransaction, invoiceId: string): void => {
    if (invoiceId === "") {
      setFormErrors(["Invoice: select the invoice that this payment paid."]);
      return;
    }
    setFormErrors([]);
    setServerError(null);
    match.mutate({ txId: tx.id, invoiceId });
  };
  const submitIgnore = (tx: BankTransaction, reasonText: string): void => {
    const built = buildReasonRequest(reasonText, "the payment is ignored");
    if (!built.ok) {
      setFormErrors(built.errors);
      return;
    }
    setFormErrors([]);
    setServerError(null);
    ignore.mutate({ txId: tx.id, reason: built.request });
  };
  const submitImport = (text: string): void => {
    const parsed = parseStatement(text);
    if (!parsed.ok) {
      setFormErrors(parsed.errors);
      return;
    }
    setFormErrors([]);
    setServerError(null);
    importer.mutate(parsed.transfers);
  };

  const loadError = txs.error ?? issued.error ?? accounts.error;
  const tabStyle = (active: boolean) => ({
    ...buttonStyle,
    ...(active ? { background: "#0f172a", color: "#ffffff", borderColor: "#0f172a" } : {}),
  });

  return (
    <div style={pageStyle} data-testid="billing-bank-page">
      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle}>Bank payments</h1>
          <p style={subheadingStyle}>
            Bank payments. First, you see the payments that you must examine. Each one shows the cause that
            stopped the bank feed from linking it. Link it to the invoice that it paid, or ignore it and give a reason.
            If the bank has no feed, import a statement by hand. Each change needs a reason. The platform saves it in
            the audit log. Times are in UTC.
          </p>
        </div>
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          <button type="button" style={buttonStyle} onClick={refresh} data-testid="billing-refresh">
            Reload
          </button>
          <button
            type="button"
            style={primaryButtonStyle}
            onClick={() => {
              setServerError(null);
              setFormErrors([]);
              setModal({ kind: "import", text: "" });
            }}
            data-testid="billing-import"
          >
            Import a statement
          </button>
        </div>
      </div>

      <div role="tablist" style={{ display: "flex", gap: 6, marginBottom: 16, flexWrap: "wrap" }}>
        {BANK_STATUSES.map((t) => (
          <button
            key={t.value}
            type="button"
            role="tab"
            aria-selected={status === t.value}
            style={tabStyle(status === t.value)}
            onClick={() => setStatus(t.value)}
            data-testid={`billing-bank-tab-${t.value}`}
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
      {loadError ? (
        <div style={errorStyle} role="alert" data-testid="billing-load-error">
          {messageOf(loadError)}
        </div>
      ) : null}
      {txs.isPending ? <p>Loading…</p> : null}
      {txs.data ? (
        <BankTable
          transactions={txs.data.transactions}
          invoices={allInvoices}
          onMatch={(tx) => {
            setServerError(null);
            setFormErrors([]);
            setModal({ kind: "match", tx, invoiceId: "" });
          }}
          onIgnore={(tx) => {
            setServerError(null);
            setFormErrors([]);
            setModal({ kind: "ignore", tx, reason: "" });
          }}
        />
      ) : null}

      {modal.kind === "match" ? (
        <Dialog
          title="Link the payment to an invoice"
          testId="billing-match-dialog"
          onClose={close}
          onSubmit={() => submitMatch(modal.tx, modal.invoiceId)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-match-confirm">
                Link and mark paid
              </button>
            </>
          }
        >
          <MatchFormBody
            transaction={modal.tx}
            issued={issuedList}
            orgNames={orgNames}
            invoiceId={modal.invoiceId}
            onChange={(invoiceId) => setModal({ kind: "match", tx: modal.tx, invoiceId })}
            errors={formErrors}
            serverError={serverError}
          />
        </Dialog>
      ) : null}

      {modal.kind === "ignore" ? (
        <Dialog
          title="Ignore the payment?"
          testId="billing-ignore-dialog"
          onClose={close}
          onSubmit={() => submitIgnore(modal.tx, modal.reason)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Keep the payment
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-ignore-confirm">
                Ignore payment
              </button>
            </>
          }
        >
          <IgnoreFormBody
            transaction={modal.tx}
            reason={modal.reason}
            onChange={(reason) => setModal({ kind: "ignore", tx: modal.tx, reason })}
            errors={formErrors}
            serverError={serverError}
          />
        </Dialog>
      ) : null}

      {modal.kind === "import" ? (
        <Dialog
          title="Import a bank statement"
          testId="billing-import-dialog"
          onClose={close}
          onSubmit={() => submitImport(modal.text)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-import-confirm">
                Import
              </button>
            </>
          }
        >
          <ImportFormBody text={modal.text} onChange={(text) => setModal({ kind: "import", text })} errors={formErrors} serverError={serverError} />
        </Dialog>
      ) : null}
    </div>
  );
}
