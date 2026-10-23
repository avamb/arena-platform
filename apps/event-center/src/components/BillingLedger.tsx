/**
 * The credit journal of one billing account, newest first. Every entry has a
 * positive amount and the type says whether credits were added or taken, so
 * the sign is shown here, never guessed from a number. One account at a time:
 * credits of different currencies are never mixed.
 */
import { useId, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { SelectField } from "@/components/FormField";
import { viewerZone } from "@/components/BillingInvoiceCard";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { listBillingLedger } from "@/lib/api/endpoints";
import type { BillingLedgerEntry } from "@/lib/api/types";
import { LEDGER_PAGE_SIZE, ledgerKey, ledgerSign, ledgerSigned } from "@/lib/billingAccount";
import { formatDateTime, formatMoney } from "@/lib/format";
import { useI18n } from "@/lib/i18n";

export function BillingLedger({ orgId, accounts }: { orgId: string; accounts: readonly { id: string; currency: string }[] }) {
  const { t, locale } = useI18n();
  const ids = useId();
  const [accountId, setAccountId] = useState(accounts[0]?.id ?? "");
  const zone = viewerZone();

  const ledger = useInfiniteQuery({
    queryKey: ["billingLedger", orgId, accountId],
    enabled: accountId !== "",
    initialPageParam: undefined as number | undefined,
    queryFn: ({ pageParam }) => listBillingLedger(orgId, { accountId, before: pageParam, limit: LEDGER_PAGE_SIZE }),
    getNextPageParam: (last) => last.next_before ?? undefined,
  });
  const entries: BillingLedgerEntry[] = ledger.data?.pages.flatMap((p) => p.entries) ?? [];

  return (
    <section aria-labelledby={`${ids}-title`} data-testid="billing-ledger">
      <h2 id={`${ids}-title`}>{t("billing.ledger.title")}</h2>
      <p className="muted">{t("billing.ledger.intro")}</p>
      {accounts.length > 1 && (
        <SelectField label={t("billing.ledger.account")} value={accountId} onChange={setAccountId} testId="billing-ledger-account">
          {accounts.map((a) => (
            <option key={a.id} value={a.id}>
              {a.currency}
            </option>
          ))}
        </SelectField>
      )}
      {ledger.isLoading && <Loading />}
      {ledger.error !== null && <ErrorBox error={ledger.error} onRetry={() => void ledger.refetch()} />}
      {ledger.data !== undefined && entries.length === 0 && <Empty>{t("billing.ledger.empty")}</Empty>}
      {entries.length > 0 && (
        <ul className="cards" data-testid="billing-ledger-rows">
          {entries.map((e) => {
            const sign = ledgerSign(e.entry_type);
            const signed = ledgerSigned(e);
            return (
              <li key={e.seq} className="card" data-testid="billing-ledger-row" data-entry-type={e.entry_type}>
                <span className="card-title">{t(ledgerKey(e.entry_type))}</span>
                <span className="muted">{formatDateTime(e.created_at, zone, locale)}</span>
                <span className="card-meta">
                  <strong data-testid="billing-ledger-amount">
                    {sign > 0 ? "+" : ""}
                    {formatMoney(signed, e.currency, locale)}
                  </strong>
                  <span className="muted">{t("billing.ledger.remaining", { amount: formatMoney(e.remaining_after, e.currency, locale) })}</span>
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {ledger.hasNextPage && (
        <p>
          <button type="button" className="btn" disabled={ledger.isFetchingNextPage} onClick={() => void ledger.fetchNextPage()} data-testid="billing-ledger-more">
            {t("billing.ledger.more")}
          </button>
        </p>
      )}
    </section>
  );
}
