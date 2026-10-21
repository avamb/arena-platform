/**
 * Payment fees — the superadmin screen for the average card fee that a card
 * payer adds to a platform invoice (billing wave B3/B4).
 *
 * A fee is versioned per currency: a new version starts in the future and after
 * the latest one, so no issued invoice changes. There is no default number: the
 * operator types the percentage (basis points) and the fixed part.
 *
 * Gated by billing.admin; the server also demands a platform superadmin and
 * X-Admin-Reason on the write. Mock data: NONE.
 */
import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ApiError } from "@/lib/api/client";
import { buildFeeRequest, createPaymentFee, emptyFeeForm, listPaymentFees, type FeeForm } from "@/lib/api/billingInvoices";
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
import { FeeFormBody, PaymentFeesTable } from "./billingFeesBankView";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/billing/payment-fees",
  component: BillingPaymentFeesRoute,
});

const BILLING_FEES_NAV_ENTRY = NAV_BY_PATH["/billing/payment-fees"];
if (BILLING_FEES_NAV_ENTRY === undefined) {
  throw new Error("billing payment fees route: NAV_BY_PATH['/billing/payment-fees'] missing");
}

function BillingPaymentFeesRoute() {
  return (
    <RequirePermission entry={BILLING_FEES_NAV_ENTRY}>
      <BillingPaymentFeesPage />
    </RequirePermission>
  );
}

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : "The request failed. Please try again.";
}

function BillingPaymentFeesPage(): JSX.Element {
  const qc = useQueryClient();
  const [form, setForm] = useState<FeeForm | null>(null);
  const [formErrors, setFormErrors] = useState<readonly string[]>([]);
  const [serverError, setServerError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const fees = useQuery({ queryKey: ["admin", "billing", "payment-fees"], queryFn: listPaymentFees, retry: false, refetchOnWindowFocus: false });

  const close = (): void => {
    setForm(null);
    setFormErrors([]);
    setServerError(null);
  };
  const refresh = (): void => {
    void qc.invalidateQueries({ queryKey: ["admin", "billing", "payment-fees"] });
  };

  const create = useMutation({
    mutationFn: (req: Parameters<typeof createPaymentFee>[0]) => createPaymentFee(req),
    onSuccess: (f) => {
      close();
      setNotice(`The card fee for ${f.currency} is saved. It starts ${f.effective_from}.`);
      refresh();
    },
    onError: (err) => setServerError(messageOf(err)),
  });

  const submit = (f: FeeForm): void => {
    const built = buildFeeRequest(f, fees.data?.payment_fees ?? []);
    if (!built.ok) {
      setFormErrors(built.errors);
      return;
    }
    setFormErrors([]);
    setServerError(null);
    create.mutate(built.request);
  };

  return (
    <div style={pageStyle} data-testid="billing-fees-page">
      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle}>Payment fees</h1>
          <p style={subheadingStyle}>
            The average card fee added to an invoice that is paid by card: a percentage plus a fixed part, per currency. A new
            version starts in the future, so no issued invoice changes. Every change asks for a reason and is written to the
            audit log. Times are in UTC.
          </p>
        </div>
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          <button type="button" style={buttonStyle} onClick={refresh} data-testid="billing-refresh">
            Refresh
          </button>
          <button
            type="button"
            style={primaryButtonStyle}
            onClick={() => {
              setServerError(null);
              setFormErrors([]);
              setForm(emptyFeeForm());
            }}
            data-testid="billing-new-fee"
          >
            New fee version
          </button>
        </div>
      </div>

      {notice !== null ? (
        <div style={noticeStyle} role="status" data-testid="billing-notice">
          {notice}
        </div>
      ) : null}
      {fees.error ? (
        <div style={errorStyle} role="alert" data-testid="billing-load-error">
          {messageOf(fees.error)}
        </div>
      ) : null}
      {fees.isPending ? <p>Loading…</p> : null}
      {fees.data ? <PaymentFeesTable fees={fees.data.payment_fees} /> : null}

      {form !== null ? (
        <Dialog
          title="New card fee version"
          testId="billing-fee-dialog"
          onClose={close}
          onSubmit={() => submit(form)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(create.isPending ? disabledButtonStyle : {}) }} disabled={create.isPending} data-testid="billing-fee-save">
                Save version
              </button>
            </>
          }
        >
          <FeeFormBody form={form} onChange={setForm} errors={formErrors} serverError={serverError} />
        </Dialog>
      ) : null}
    </div>
  );
}
