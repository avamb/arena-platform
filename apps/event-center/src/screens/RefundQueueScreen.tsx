/**
 * "Refunds" of the bottom bar (EC-V5, spec 62 section 12.2): everything that
 * needs a person. Failed or stuck refunds with a retry, and orders whose buyer
 * has no letter with a "send again". The counter on the nav item is this
 * screen's `count`. Orders that WERE refunded are one link away (the orders
 * list, Refunds tab).
 */
import { HelpPopover } from "@/components/HelpPopover";
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { LetterRow, RefundRow } from "@/components/RefundQueueRows";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { fetchRefundQueue } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import { letterRowKey } from "@/lib/refundQueue";
import { useOrg } from "@/screens/OrgLayout";

export const REFUND_QUEUE_POLL_MS = 60_000;

export function RefundQueueScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const canRead = access.can("refund.read");
  const queue = useQuery({
    queryKey: ["refund-queue", orgId],
    queryFn: () => fetchRefundQueue(orgId),
    enabled: canRead,
    refetchInterval: REFUND_QUEUE_POLL_MS,
  });

  if (!canRead) {
    return (
      <section aria-labelledby="queue-title">
        <h1 id="queue-title">{t("queue.title")}</h1>
        <p className="notice" role="status" data-testid="queue-no-access">
          {t("queue.noAccess")}
        </p>
      </section>
    );
  }

  const q = queue.data;
  return (
    <section className="dense" aria-labelledby="queue-title">
      <h1 id="queue-title">
        {t("queue.title")}
        <HelpPopover textKey="help.refundQueue" testId="help-refund-queue" />
      </h1>
      <p className="muted">{t("queue.intro")}</p>
      {access.can("order.read") && (
        <p>
          <Link to="/o/$orgId/orders" params={{ orgId }} search={{ tab: "refunds" }} data-testid="queue-refunded-orders">
            {t("queue.refundedOrders")}
          </Link>
        </p>
      )}
      {queue.isLoading && <Loading />}
      {queue.error !== null && q === undefined && <ErrorBox error={queue.error} onRetry={() => void queue.refetch()} />}
      {q !== undefined && q.count === 0 && <Empty>{t("queue.empty")}</Empty>}
      {q !== undefined && q.refunds.length > 0 && (
        <section aria-labelledby="queue-refunds-title">
          <h2 id="queue-refunds-title">{t("queue.refunds.title", { count: q.refunds_total })}</h2>
          <ul className="queue-list" data-testid="queue-refunds">
            {q.refunds.map((r) => (
              <RefundRow key={r.id} orgId={orgId} item={r} canApprove={access.can("refund.approve")} />
            ))}
          </ul>
        </section>
      )}
      {q !== undefined && q.letters.length > 0 && (
        <section aria-labelledby="queue-letters-title">
          <h2 id="queue-letters-title">{t("queue.letters.title", { count: q.letters_total })}</h2>
          <p className="muted small">{t("queue.letters.hint")}</p>
          <ul className="queue-list" data-testid="queue-letters">
            {q.letters.map((l) => (
              <LetterRow key={letterRowKey(l)} orgId={orgId} letter={l} canUpdate={access.can("ticket.update")} />
            ))}
          </ul>
        </section>
      )}
    </section>
  );
}
