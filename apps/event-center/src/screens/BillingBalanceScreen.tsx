/**
 * B3-3/B4: "Billing → Balance and packs". The organization's credit balance
 * per currency, the packs on offer and how to order one, the commission as a
 * service charge, and the payer details. It is also the address Stripe sends
 * the buyer back to after a card payment: `?invoice=<id>&paid=1` or
 * `&cancelled=1`.
 *
 * Who sees what is decided from `GET .../me` (`useOrg().access.can`), never
 * from the role: `billing.read` shows the page, `billing.purchase` the order
 * buttons, `billing.payer_manage` the payer form. A person without
 * `billing.read` gets an explanation and no billing request is sent.
 */
import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearch } from "@tanstack/react-router";
import { BillingFeeQuote } from "@/components/BillingFeeQuote";
import { BillingInvoiceCard, viewerZone } from "@/components/BillingInvoiceCard";
import { BillingClosedNotice, BillingNav } from "@/components/BillingNav";
import { BillingPacks } from "@/components/BillingPacks";
import { BillingPayerForm } from "@/components/BillingPayerForm";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { fetchAccountInvoice, fetchBillingBalance, listBillingPacks } from "@/lib/api/endpoints";
import type { BillingAccountBalance } from "@/lib/api/types";
import { isOrganizerDisabled, quoteCurrencies } from "@/lib/billingAccount";
import { confirmPollDelayMs, parsePackReturn, returnStatus } from "@/lib/billingPacks";
import { formatDate, formatMoney } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

function BalanceCard({ b }: { b: BillingAccountBalance }) {
  const { t, locale } = useI18n();
  const money = (minor: number) => formatMoney(minor, b.currency, locale);
  const zone = viewerZone();
  return (
    <li className="card" data-testid={`billing-balance-${b.currency}`}>
      <span className="card-title" data-testid="billing-balance-total">
        {money(b.balance_minor)}
      </span>
      <span className="muted">{t("billing.balance.account", { currency: b.currency })}</span>
      <span className="card-meta">
        <span data-testid="billing-balance-purchased">{t("billing.balance.purchased", { amount: money(b.purchased_minor) })}</span>
        <span data-testid="billing-balance-bonus">{t("billing.balance.bonus", { amount: money(b.bonus_minor) })}</span>
      </span>
      {b.next_bonus_expires_at !== null && b.next_bonus_expires_at !== undefined && (
        <span className="muted" data-testid="billing-balance-expiry">
          {t("billing.balance.bonusExpires", { date: formatDate(b.next_bonus_expires_at, zone, locale) })}
        </span>
      )}
      {b.current_rate !== null &&
      b.current_rate !== undefined &&
      b.current_rate.unseated_rate_minor !== null &&
      b.current_rate.unseated_rate_minor !== undefined ? (
        <span className="muted" data-testid="billing-balance-rate">
          {t("billing.balance.rate", {
            unseated: money(b.current_rate.unseated_rate_minor),
            seated: money(b.current_rate.seated_surcharge_minor ?? 0),
            tier: b.current_rate.tier ?? 0,
          })}
        </span>
      ) : (
        <span className="muted" data-testid="billing-balance-rate-none">
          {t("billing.balance.rateNone")}
        </span>
      )}
      {b.lots.length > 0 && (
        <details>
          <summary>{t("billing.balance.lots")}</summary>
          <ul className="cards" data-testid="billing-balance-lots">
            {b.lots.map((l) => (
              <li key={l.id} className="card">
                {t("billing.lot.line", {
                  kind: l.kind === "bonus" ? t("billing.lot.bonus") : t("billing.lot.purchased"),
                  remaining: formatMoney(l.remaining_minor, l.currency, locale),
                  face: formatMoney(l.face_minor, l.currency, locale),
                })}
                {" · "}
                {l.expires_at !== null && l.expires_at !== undefined
                  ? t("billing.lot.expires", { date: formatDate(l.expires_at, zone, locale) })
                  : t("billing.lot.noExpiry")}
              </li>
            ))}
          </ul>
        </details>
      )}
    </li>
  );
}

/** The note after the buyer comes back from the Stripe page. */
function ReturnNote({ orgId, search, canPay }: { orgId: string; search: { invoice?: string; paid?: string; cancelled?: string }; canPay: boolean }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const ret = parsePackReturn(search);
  const invoiceId = ret?.invoiceId ?? "";
  const q = useQuery({
    queryKey: ["billingInvoice", orgId, invoiceId],
    enabled: ret !== null,
    retry: 1,
    queryFn: () => fetchAccountInvoice(orgId, invoiceId),
    // After a paid return the invoice is read again until it says paid, then the page stops asking.
    refetchInterval: (query) => {
      if (ret === null || ret.result !== "paid") return false;
      const state = query.state.data?.state;
      if (state === "paid" || state === "void") return false;
      return confirmPollDelayMs(query.state.dataUpdateCount) ?? false;
    },
  });
  const state = q.data?.state;
  useEffect(() => {
    if (state === "paid") {
      void queryClient.invalidateQueries({ queryKey: ["billingBalance", orgId] });
      void queryClient.invalidateQueries({ queryKey: ["billingAccountInvoices", orgId] });
      void queryClient.invalidateQueries({ queryKey: ["billingLedger", orgId] });
    }
  }, [state, orgId, queryClient]);

  if (ret === null) return null;
  // Reads made so far: the status turns to "still confirming" after the last one.
  const reads = queryClient.getQueryState(["billingInvoice", orgId, invoiceId])?.dataUpdateCount ?? 0;
  const status = returnStatus(ret.result, q.data ?? null, reads);
  return (
    <section className="card billing-return" aria-labelledby="billing-return-title" data-testid="billing-return" data-status={status} data-updated={q.dataUpdatedAt}>
      <h2 id="billing-return-title">{t("billing.return.title")}</h2>
      {q.isLoading && <Loading />}
      {q.error !== null && <ErrorNotice error={q.error} testId="billing-return-error" />}
      {(q.data !== undefined || q.error !== null) && (
        <p className={status === "paid" ? "notice notice-ok" : "notice"} role="status" data-testid="billing-return-text">
          {status === "paid" && t("billing.return.paid", { number: q.data?.number ?? "" })}
          {status === "confirming" && t("billing.return.confirming")}
          {status === "stillConfirming" && t("billing.return.stillConfirming")}
          {status === "cancelled" && t("billing.return.cancelled")}
          {status === "void" && t("billing.return.void")}
        </p>
      )}
      {q.data !== undefined && (
        <ul className="cards">
          <BillingInvoiceCard orgId={orgId} invoice={q.data} canPay={canPay} onRefresh={() => void q.refetch()} />
        </ul>
      )}
      {ret.result === "paid" && status !== "paid" && (
        <p>
          <button type="button" className="btn" onClick={() => void q.refetch()} data-testid="billing-return-check">
            {t("billing.return.check")}
          </button>
        </p>
      )}
    </section>
  );
}

export function BillingBalanceScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const search = useSearch({ from: "/o/$orgId/billing/balance" });
  const canRead = access.can("billing.read");
  const canPurchase = access.can("billing.purchase");
  const canPayer = access.can("billing.payer_manage");

  const balance = useQuery({ queryKey: ["billingBalance", orgId], enabled: canRead, queryFn: () => fetchBillingBalance(orgId) });
  const packs = useQuery({ queryKey: ["billingPacks", orgId], enabled: canRead, queryFn: () => listBillingPacks(orgId) });

  if (!canRead) {
    return (
      <article aria-labelledby="billing-balance-title">
        <h1 id="billing-balance-title">{t("billing.balance.title")}</h1>
        <p className="notice notice-bad" role="alert" data-testid="billing-no-access">
          {t("billing.noAccess")}
        </p>
      </article>
    );
  }

  if (isOrganizerDisabled(balance.error) || isOrganizerDisabled(packs.error)) {
    return (
      <article aria-labelledby="billing-balance-title">
        <h1 id="billing-balance-title">{t("billing.balance.title")}</h1>
        <BillingNav orgId={orgId} />
        <BillingClosedNotice />
      </article>
    );
  }

  const accounts = balance.data?.accounts ?? [];
  return (
    <article aria-labelledby="billing-balance-title">
      <h1 id="billing-balance-title">{t("billing.balance.title")}</h1>
      <BillingNav orgId={orgId} />
      <p className="muted">{t("billing.balance.intro")}</p>

      <ReturnNote orgId={orgId} search={search} canPay={canPurchase} />

      <section aria-labelledby="billing-balance-section">
        <h2 id="billing-balance-section">{t("billing.balance.section")}</h2>
        {balance.isLoading && <Loading />}
        {balance.error !== null && <ErrorBox error={balance.error} onRetry={() => void balance.refetch()} />}
        {balance.data !== undefined && accounts.length === 0 && <Empty>{t("billing.balance.none")}</Empty>}
        {accounts.length > 0 && (
          <ul className="cards" data-testid="billing-balances">
            {accounts.map((b) => (
              <BalanceCard key={b.account_id} b={b} />
            ))}
          </ul>
        )}
        {accounts.length > 0 && <p className="muted">{t("billing.balance.afterPayment")}</p>}
      </section>

      {packs.isLoading && <Loading />}
      {packs.error !== null && <ErrorBox error={packs.error} onRetry={() => void packs.refetch()} />}
      {packs.data !== undefined && <BillingPacks orgId={orgId} packs={packs.data.packs} canPurchase={canPurchase} />}

      {accounts.length > 0 && <BillingFeeQuote orgId={orgId} currencies={quoteCurrencies(accounts)} />}

      {canPayer && <BillingPayerForm orgId={orgId} />}
    </article>
  );
}
