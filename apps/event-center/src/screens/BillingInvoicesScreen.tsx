/**
 * B3-3/B4: "Billing → Invoices". The platform's invoices to the organization
 * (credit packs and usage), filtered by state, each with its lines, totals,
 * payment reference and, for an issued card invoice, "Pay by card"; and the
 * credit journal below.
 *
 * Seen by who holds `billing.read` in THIS organization (`useOrg().access`),
 * never decided by the role. The card button needs `billing.purchase`. A
 * person without `billing.read` gets an explanation and no request is sent.
 */
import { useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { BillingInvoiceCard } from "@/components/BillingInvoiceCard";
import { BillingLedger } from "@/components/BillingLedger";
import { BillingClosedNotice, BillingNav } from "@/components/BillingNav";
import { SelectField } from "@/components/FormField";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { fetchBillingBalance, listAccountInvoices } from "@/lib/api/endpoints";
import { INVOICES_PAGE_SIZE, INVOICE_STATE_FILTERS, invoiceStateKey, isInvoiceStateFilter, isOrganizerDisabled } from "@/lib/billingAccount";
import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

export function BillingInvoicesScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const canRead = access.can("billing.read");
  const canPay = access.can("billing.purchase");
  const [state, setState] = useState("");

  const invoices = useInfiniteQuery({
    queryKey: ["billingAccountInvoices", orgId, state],
    enabled: canRead,
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      listAccountInvoices(orgId, { state: isInvoiceStateFilter(state) ? state : undefined, limit: INVOICES_PAGE_SIZE, offset: pageParam }),
    getNextPageParam: (last, all) => (last.has_more ? all.length * INVOICES_PAGE_SIZE : undefined),
  });
  // The accounts only tell the journal which currencies exist.
  const balance = useQuery({ queryKey: ["billingBalance", orgId], enabled: canRead, queryFn: () => fetchBillingBalance(orgId) });

  if (!canRead) {
    return (
      <article aria-labelledby="billing-invoices-title">
        <h1 id="billing-invoices-title">{t("billing.invoices.title")}</h1>
        <p className="notice notice-bad" role="alert" data-testid="billing-no-access">
          {t("billing.noAccess")}
        </p>
      </article>
    );
  }

  if (isOrganizerDisabled(balance.error) || isOrganizerDisabled(invoices.error)) {
    return (
      <article aria-labelledby="billing-invoices-title">
        <h1 id="billing-invoices-title">{t("billing.invoices.title")}</h1>
        <BillingNav orgId={orgId} />
        <BillingClosedNotice />
      </article>
    );
  }

  const rows = invoices.data?.pages.flatMap((p) => p.invoices) ?? [];
  const accounts = (balance.data?.accounts ?? []).map((a) => ({ id: a.account_id, currency: a.currency }));
  return (
    <article aria-labelledby="billing-invoices-title">
      <h1 id="billing-invoices-title">{t("billing.invoices.title")}</h1>
      <BillingNav orgId={orgId} />
      <p className="muted">{t("billing.invoices.intro")}</p>

      <SelectField label={t("billing.invoices.filter")} value={state} onChange={setState} testId="billing-invoices-filter">
        <option value="">{t("billing.invoices.all")}</option>
        {INVOICE_STATE_FILTERS.map((s) => (
          <option key={s} value={s}>
            {t(invoiceStateKey(s))}
          </option>
        ))}
      </SelectField>

      {invoices.isLoading && <Loading />}
      {invoices.error !== null && <ErrorBox error={invoices.error} onRetry={() => void invoices.refetch()} />}
      {invoices.data !== undefined && rows.length === 0 && <Empty>{t("billing.invoices.empty")}</Empty>}
      {rows.length > 0 && (
        <ul className="cards" data-testid="billing-invoices">
          {rows.map((inv) => (
            <BillingInvoiceCard key={inv.id} orgId={orgId} invoice={inv} canPay={canPay} onRefresh={() => void invoices.refetch()} />
          ))}
        </ul>
      )}
      {invoices.hasNextPage && (
        <p>
          <button type="button" className="btn" disabled={invoices.isFetchingNextPage} onClick={() => void invoices.fetchNextPage()} data-testid="billing-invoices-more">
            {t("billing.invoices.more")}
          </button>
        </p>
      )}

      {accounts.length > 0 && <BillingLedger key={accounts.map((a) => a.id).join(",")} orgId={orgId} accounts={accounts} />}
    </article>
  );
}
