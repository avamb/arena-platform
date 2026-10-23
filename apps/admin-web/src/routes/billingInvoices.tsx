/**
 * Invoices — the superadmin screen for platform invoices (billing wave B3/B4).
 *
 * State tabs (drafts, issued, paid, void) over /v1/admin/billing/invoices; one
 * invoice opens with its lines and totals. Actions: generate a draft usage
 * invoice for an account and a finished month, issue a draft (bank transfer or
 * card), mark an issued invoice paid with money that arrived outside the feeds,
 * and void a draft or issued invoice with a reason.
 *
 * The page is gated by billing.admin; the server also demands a platform
 * superadmin and X-Admin-Reason on every write (reason.ts asks once per tab).
 * Mock data: NONE. Money is whole minor units in requests, never a float.
 */
import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ApiError } from "@/lib/api/client";
import { listBillingAccounts } from "@/lib/api/billingPrices";
import {
  INVOICE_STATES,
  buildMarkPaidRequest,
  buildReasonRequest,
  buildUsageRequest,
  createUsageInvoice,
  emptyMarkPaidForm,
  getInvoice,
  invoiceLabel,
  issueInvoice,
  listInvoices,
  markInvoicePaid,
  previousMonth,
  voidInvoice,
  type Invoice,
  type InvoiceState,
  type MarkPaidForm,
  type PaymentMethod,
  type UsageForm,
} from "@/lib/api/billingInvoices";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import {
  Dialog,
  buttonStyle,
  dangerButtonStyle,
  disabledButtonStyle,
  errorStyle,
  headerRowStyle,
  headingStyle,
  noticeStyle,
  pageStyle,
  primaryButtonStyle,
  subheadingStyle,
  warningStyle,
} from "./onboardingUi";
import {
  InvoiceDetailBody,
  InvoicesTable,
  IssueFormBody,
  MarkPaidFormBody,
  UsageFormBody,
  VoidFormBody,
  orgLabel,
} from "./billingInvoicesView";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/billing/invoices",
  component: BillingInvoicesRoute,
});

const BILLING_INVOICES_NAV_ENTRY = NAV_BY_PATH["/billing/invoices"];
if (BILLING_INVOICES_NAV_ENTRY === undefined) {
  throw new Error("billing invoices route: NAV_BY_PATH['/billing/invoices'] missing");
}

function BillingInvoicesRoute() {
  return (
    <RequirePermission entry={BILLING_INVOICES_NAV_ENTRY}>
      <BillingInvoicesPage />
    </RequirePermission>
  );
}

type Modal =
  | { kind: "none" }
  | { kind: "detail"; invoice: Invoice }
  | { kind: "generate"; form: UsageForm }
  | { kind: "issue"; invoice: Invoice; method: PaymentMethod }
  | { kind: "mark-paid"; invoice: Invoice; form: MarkPaidForm }
  | { kind: "void"; invoice: Invoice; reason: string };

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : "The request failed. Please try again.";
}

function BillingInvoicesPage(): JSX.Element {
  const qc = useQueryClient();
  const [state, setState] = useState<InvoiceState>("issued");
  const [modal, setModal] = useState<Modal>({ kind: "none" });
  const [formErrors, setFormErrors] = useState<readonly string[]>([]);
  const [serverError, setServerError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [warnings, setWarnings] = useState<readonly string[]>([]);

  const invoices = useQuery({
    queryKey: ["admin", "billing", "invoices", state],
    queryFn: () => listInvoices(state),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const accounts = useQuery({ queryKey: ["admin", "billing", "accounts"], queryFn: listBillingAccounts, retry: false, refetchOnWindowFocus: false });

  const orgNames = useMemo(() => {
    const m = new Map<string, string>();
    for (const a of accounts.data?.items ?? []) m.set(a.org_id, a.org_name);
    return m as ReadonlyMap<string, string>;
  }, [accounts.data]);

  const detailId = modal.kind === "detail" ? modal.invoice.id : null;
  const detail = useQuery<Invoice, ApiError>({
    queryKey: ["admin", "billing", "invoice", detailId],
    queryFn: () => getInvoice(detailId ?? ""),
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

  const generate = useMutation({
    mutationFn: (v: { accountId: string; period: string }) => createUsageInvoice(v.accountId, v.period),
    onSuccess: (r, v) => {
      if (r.invoice === null) {
        done(`Nothing to bill for ${v.period}: every charge of that month is already covered.`);
        return;
      }
      done(`Draft invoice made for ${v.period}. Issue it to give it a number.`);
      setState("draft");
    },
    onError: failed,
  });
  const issue = useMutation({
    mutationFn: (v: { id: string; method: PaymentMethod }) => issueInvoice(v.id, v.method),
    onSuccess: (inv) => {
      done(
        `Invoice ${invoiceLabel(inv)} is issued${inv.due_at ? `, due ${inv.due_at}` : ""}. Payment reference: ${inv.payment_reference ?? "—"}.`,
        inv.payment_url_error ? [inv.payment_url_error] : [],
      );
    },
    onError: failed,
  });
  const markPaid = useMutation({
    mutationFn: (v: { id: string; req: Parameters<typeof markInvoicePaid>[1] }) => markInvoicePaid(v.id, v.req),
    onSuccess: (inv) => {
      done(`Invoice ${invoiceLabel(inv)} is marked paid.`);
      setState("paid");
    },
    onError: failed,
  });
  const voidIt = useMutation({
    mutationFn: (v: { id: string; reason: string }) => voidInvoice(v.id, v.reason),
    onSuccess: (inv) => done(`Invoice ${invoiceLabel(inv)} is void. Its charges go to the next invoice.`),
    onError: failed,
  });

  const busy = generate.isPending || issue.isPending || markPaid.isPending || voidIt.isPending;

  const openGenerate = (): void => {
    setServerError(null);
    setFormErrors([]);
    setModal({ kind: "generate", form: { accountId: "", period: previousMonth() } });
  };
  const openIssue = (invoice: Invoice): void => {
    setServerError(null);
    setFormErrors([]);
    setModal({ kind: "issue", invoice, method: "bank_transfer" });
  };
  const openMarkPaid = (invoice: Invoice): void => {
    setServerError(null);
    setFormErrors([]);
    setModal({ kind: "mark-paid", invoice, form: emptyMarkPaidForm() });
  };
  const openVoid = (invoice: Invoice): void => {
    setServerError(null);
    setFormErrors([]);
    setModal({ kind: "void", invoice, reason: "" });
  };

  const submitGenerate = (form: UsageForm): void => {
    const built = buildUsageRequest(form);
    if (!built.ok) {
      setFormErrors(built.errors);
      return;
    }
    setFormErrors([]);
    setServerError(null);
    generate.mutate(built.request);
  };
  const submitMarkPaid = (invoice: Invoice, form: MarkPaidForm): void => {
    const built = buildMarkPaidRequest(form, invoice);
    if (!built.ok) {
      setFormErrors(built.errors);
      return;
    }
    setFormErrors([]);
    setServerError(null);
    markPaid.mutate({ id: invoice.id, req: built.request });
  };
  const submitVoid = (invoice: Invoice, reasonText: string): void => {
    const built = buildReasonRequest(reasonText, "the invoice is voided");
    if (!built.ok) {
      setFormErrors(built.errors);
      return;
    }
    setFormErrors([]);
    setServerError(null);
    voidIt.mutate({ id: invoice.id, reason: built.request });
  };

  const loadError = invoices.error ?? accounts.error;
  const tabStyle = (active: boolean) => ({
    ...buttonStyle,
    ...(active ? { background: "#0f172a", color: "#ffffff", borderColor: "#0f172a" } : {}),
  });

  return (
    <div style={pageStyle} data-testid="billing-invoices-page">
      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle}>Invoices</h1>
          <p style={subheadingStyle}>
            Platform invoices to organizations. Make a draft usage invoice for a finished month, issue it for a bank transfer
            or a card payment, record money that arrived outside the feeds, or void it. Every change asks for a reason and is
            written to the audit log. Times are in UTC.
          </p>
        </div>
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          <button type="button" style={buttonStyle} onClick={refresh} data-testid="billing-refresh">
            Refresh
          </button>
          <button type="button" style={primaryButtonStyle} onClick={openGenerate} data-testid="billing-generate">
            Make a usage invoice
          </button>
        </div>
      </div>

      <div role="tablist" style={{ display: "flex", gap: 6, marginBottom: 16, flexWrap: "wrap" }}>
        {INVOICE_STATES.map((t) => (
          <button
            key={t.value}
            type="button"
            role="tab"
            aria-selected={state === t.value}
            style={tabStyle(state === t.value)}
            onClick={() => setState(t.value)}
            data-testid={`billing-invoices-tab-${t.value}`}
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

      {invoices.isPending ? <p>Loading…</p> : null}
      {invoices.data ? (
        <InvoicesTable
          invoices={invoices.data.invoices}
          orgNames={orgNames}
          onOpen={(inv) => setModal({ kind: "detail", invoice: inv })}
          onIssue={openIssue}
          onMarkPaid={openMarkPaid}
          onVoid={openVoid}
        />
      ) : null}

      {modal.kind === "detail" ? (
        <Dialog
          title={`Invoice ${invoiceLabel(modal.invoice)}`}
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
          {detail.data ? <InvoiceDetailBody invoice={detail.data} orgName={orgLabel(orgNames, detail.data.org_id)} /> : null}
        </Dialog>
      ) : null}

      {modal.kind === "generate" ? (
        <Dialog
          title="Make a usage invoice"
          testId="billing-generate-dialog"
          onClose={close}
          onSubmit={() => submitGenerate(modal.form)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-generate-save">
                Make draft
              </button>
            </>
          }
        >
          <UsageFormBody
            form={modal.form}
            onChange={(form) => setModal({ kind: "generate", form })}
            accounts={accounts.data?.items ?? []}
            errors={formErrors}
            serverError={serverError}
          />
        </Dialog>
      ) : null}

      {modal.kind === "issue" ? (
        <Dialog
          title={`Issue ${invoiceLabel(modal.invoice)}`}
          testId="billing-issue-dialog"
          onClose={close}
          onSubmit={() => issue.mutate({ id: modal.invoice.id, method: modal.method })}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-issue-confirm">
                Issue invoice
              </button>
            </>
          }
        >
          <IssueFormBody
            invoice={modal.invoice}
            orgName={orgLabel(orgNames, modal.invoice.org_id)}
            method={modal.method}
            onChange={(method) => setModal({ kind: "issue", invoice: modal.invoice, method })}
            serverError={serverError}
          />
        </Dialog>
      ) : null}

      {modal.kind === "mark-paid" ? (
        <Dialog
          title={`Mark ${invoiceLabel(modal.invoice)} paid`}
          testId="billing-markpaid-dialog"
          onClose={close}
          onSubmit={() => submitMarkPaid(modal.invoice, modal.form)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-markpaid-confirm">
                Mark paid
              </button>
            </>
          }
        >
          <MarkPaidFormBody
            invoice={modal.invoice}
            form={modal.form}
            onChange={(form) => setModal({ kind: "mark-paid", invoice: modal.invoice, form })}
            errors={formErrors}
            serverError={serverError}
          />
        </Dialog>
      ) : null}

      {modal.kind === "void" ? (
        <Dialog
          title={`Void ${invoiceLabel(modal.invoice)}?`}
          testId="billing-void-dialog"
          onClose={close}
          onSubmit={() => submitVoid(modal.invoice, modal.reason)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Keep it
              </button>
              <button type="submit" style={{ ...dangerButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="billing-void-confirm">
                Void invoice
              </button>
            </>
          }
        >
          <VoidFormBody
            invoice={modal.invoice}
            reason={modal.reason}
            onChange={(reason) => setModal({ kind: "void", invoice: modal.invoice, reason })}
            errors={formErrors}
            serverError={serverError}
          />
        </Dialog>
      ) : null}
    </div>
  );
}
