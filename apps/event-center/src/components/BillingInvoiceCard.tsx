/**
 * One platform invoice of the organization: number, state, totals, the lines,
 * the payment reference of a bank transfer and "Pay by card" for an issued
 * card invoice. Used by the invoices list and by the balance page.
 *
 * Money is shown in the invoice's own currency. The card button opens the
 * Stripe page in the same tab; the page that Stripe returns to is the balance
 * page. Whether the person may pay is decided by the caller (`canPay`).
 */
import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { PaymentReference } from "@/components/PaymentReference";
import { Badge, ErrorBox, Loading } from "@/components/ui";
import { fetchAccountInvoice, payInvoiceByCard } from "@/lib/api/endpoints";
import type { BillingAccountInvoice } from "@/lib/api/types";
import {
  awaitsBankTransfer,
  canPayByCard,
  invoiceKindKey,
  invoiceStateKey,
  lineKindKey,
  methodKey,
} from "@/lib/billingAccount";
import { isSafePaymentUrl } from "@/lib/billingPacks";
import { formatDate, formatMoney } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import type { Tone } from "@/lib/orders";

/** The viewer's own time zone: invoice dates are shown as the person's calendar says. */
export function viewerZone(): string | undefined {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    return undefined;
  }
}

export function invoiceTone(state: string): Tone {
  if (state === "paid") return "ok";
  if (state === "issued") return "wait";
  if (state === "void") return "bad";
  return "neutral";
}

export interface BillingInvoiceCardProps {
  orgId: string;
  invoice: BillingAccountInvoice;
  /** The person holds `billing.purchase`. */
  canPay: boolean;
  defaultOpen?: boolean;
  /** Called after the person asks to check the payment again. */
  onRefresh?: () => void;
}

export function BillingInvoiceCard({ orgId, invoice, canPay, defaultOpen = false, onRefresh }: BillingInvoiceCardProps) {
  const { t, locale } = useI18n();
  const [open, setOpen] = useState(defaultOpen);
  const zone = viewerZone();
  const money = (minor: number) => formatMoney(minor, invoice.currency, locale);
  const method = methodKey(invoice.payment_method);

  const pay = useMutation({
    mutationFn: () => payInvoiceByCard(orgId, invoice.id),
    onSuccess: (res) => {
      if (isSafePaymentUrl(res.payment_url)) window.location.assign(res.payment_url);
    },
  });
  // The list answers invoices without their lines: opening one reads it whole.
  const needsLines = invoice.lines.length === 0;
  const detail = useQuery({
    queryKey: ["billingInvoice", orgId, invoice.id],
    enabled: open && needsLines,
    queryFn: () => fetchAccountInvoice(orgId, invoice.id),
  });
  const lines = needsLines ? (detail.data?.lines ?? []) : invoice.lines;
  const payUrl = pay.data?.payment_url;
  const showPay = canPay && canPayByCard(invoice);

  return (
    <li className="card billing-invoice" data-testid="billing-invoice" data-invoice-id={invoice.id} data-state={invoice.state}>
      <p className="card-title">
        {invoice.number !== null && invoice.number !== undefined
          ? t("billing.invoice.number", { number: invoice.number })
          : t("billing.invoice.noNumber")}{" "}
        <Badge tone={invoiceTone(invoice.state)}>{t(invoiceStateKey(invoice.state))}</Badge>
      </p>
      <p className="card-meta">
        <strong data-testid="billing-invoice-total">{money(invoice.total_minor)}</strong>
        <span className="muted">{t(invoiceKindKey(invoice.kind))}</span>
        {method !== null && <span className="muted">{t(method)}</span>}
        {invoice.issued_at !== null && invoice.issued_at !== undefined && (
          <span className="muted">{t("billing.invoice.issuedAt", { date: formatDate(invoice.issued_at, zone, locale) })}</span>
        )}
        {invoice.state === "issued" && invoice.due_at !== null && invoice.due_at !== undefined && (
          <span className="muted">{t("billing.invoice.dueAt", { date: formatDate(invoice.due_at, zone, locale) })}</span>
        )}
        {invoice.state === "paid" && invoice.paid_at !== null && invoice.paid_at !== undefined && (
          <span className="muted">{t("billing.invoice.paidAt", { date: formatDate(invoice.paid_at, zone, locale) })}</span>
        )}
      </p>

      {awaitsBankTransfer(invoice) && (
        <>
          <PaymentReference value={invoice.payment_reference as string} />
          <p className="muted" data-testid="billing-invoice-waiting">
            {t("billing.invoice.waitingBank")}
          </p>
        </>
      )}
      {invoice.state === "issued" && invoice.payment_method === "stripe_card" && (
        <p className="muted" data-testid="billing-invoice-waiting">
          {t("billing.invoice.waitingCard")}
        </p>
      )}
      {invoice.state === "issued" && <p className="muted">{t("billing.invoice.creditsAfter")}</p>}

      <p className="card-meta">
        {showPay && (
          <button type="button" className="btn btn-primary" disabled={pay.isPending} onClick={() => pay.mutate()} data-testid="billing-invoice-pay">
            {pay.isPending ? t("billing.invoice.paying") : t("billing.invoice.payCard")}
          </button>
        )}
        {invoice.state === "issued" && onRefresh !== undefined && (
          <button type="button" className="btn" onClick={onRefresh} data-testid="billing-invoice-refresh">
            {t("billing.invoice.refresh")}
          </button>
        )}
        <button type="button" className="btn btn-link" aria-expanded={open} onClick={() => setOpen(!open)} data-testid="billing-invoice-toggle">
          {open ? t("billing.invoice.hide") : t("billing.invoice.show")}
        </button>
      </p>
      {pay.error !== null && <ErrorNotice error={pay.error} testId="billing-invoice-pay-error" />}
      {pay.isSuccess && !isSafePaymentUrl(payUrl) && (
        <p className="notice notice-bad" role="alert" data-testid="billing-invoice-pay-nourl">
          {t("billing.result.cardError")}
        </p>
      )}

      {open && (
        <div data-testid="billing-invoice-detail">
          {invoice.payment_reference !== null &&
            invoice.payment_reference !== undefined &&
            invoice.payment_reference !== "" &&
            !awaitsBankTransfer(invoice) && (
              <p className="muted">
                {t("billing.invoice.reference")}: <span className="mono">{invoice.payment_reference}</span>
              </p>
            )}
          <h3>{t("billing.invoice.lines")}</h3>
          {detail.isLoading && <Loading />}
          {detail.error !== null && <ErrorBox error={detail.error} onRetry={() => void detail.refetch()} />}
          <ul className="cards" data-testid="billing-invoice-lines">
            {lines.map((l) => (
              <li key={l.id} className="card">
                <span className="card-title">{t(lineKindKey(l.kind))}</span>
                <span className="muted">{l.description}</span>
                <span className="card-meta">
                  <span className="muted">
                    {t("billing.invoice.lineQty", { qty: l.quantity, unit: formatMoney(l.unit_amount_minor, l.currency, locale) })}
                  </span>
                  <strong>{formatMoney(l.total_amount_minor, l.currency, locale)}</strong>
                </span>
              </li>
            ))}
          </ul>
          <dl className="billing-totals-list">
            <dt>{t("billing.invoice.subtotal")}</dt>
            <dd>{money(invoice.subtotal_minor)}</dd>
            {invoice.card_fee_minor > 0 && (
              <>
                <dt>{t("billing.invoice.cardFee")}</dt>
                <dd>{money(invoice.card_fee_minor)}</dd>
              </>
            )}
            {invoice.tax_minor > 0 && (
              <>
                <dt>{t("billing.invoice.tax")}</dt>
                <dd>{money(invoice.tax_minor)}</dd>
              </>
            )}
            <dt>
              <strong>{t("billing.invoice.total")}</strong>
            </dt>
            <dd>
              <strong>{money(invoice.total_minor)}</strong>
            </dd>
          </dl>
        </div>
      )}
    </li>
  );
}
