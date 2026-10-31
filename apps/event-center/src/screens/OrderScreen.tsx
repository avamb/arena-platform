import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { fetchOrder } from "@/lib/api/endpoints";
import { formatDateTime, formatMoney } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { UNPAID_REASONS, orderNumber } from "@/lib/orders";
import { BuyerContact } from "@/components/BuyerContact";
import { OrderLetters } from "@/components/OrderLetters";
import { OrderTickets } from "@/components/OrderTickets";
import { CancelOrder } from "@/components/CancelOrder";
import { RefundOrder } from "@/components/RefundOrder";
import { ErrorBox, Loading } from "@/components/ui";
import { useOrg } from "@/screens/OrgLayout";
import { OrderStatusBadge } from "@/screens/OrdersScreen";

function channelKey(kind: string): MessageKey {
  return kind === "site" || kind === "hosted_page" || kind === "widget" ? `channel.${kind}` : "channel.other";
}

/**
 * The order card. Compact layout (owner decision 2026-10-30, after the
 * WordPress ops center): number, status and total on one line, the event under
 * it, the buyer with call and WhatsApp buttons, the tickets as one table, the
 * letters as a scale with their history, the money at the end.
 */
export function OrderScreen() {
  const { t, locale } = useI18n();
  const { orgId, access } = useOrg();
  const { orderId } = useParams({ from: "/o/$orgId/orders/$orderId" });
  const order = useQuery({ queryKey: ["order", orgId, orderId], queryFn: () => fetchOrder(orgId, orderId) });

  if (order.isLoading) return <Loading />;
  if (order.data === undefined) return <ErrorBox error={order.error} onRetry={() => void order.refetch()} />;
  const o = order.data;
  // The card carries the event name, the session start and the venue zone itself (0-2).
  const tz = o.venue_timezone;
  const money = (v: number) => formatMoney(v, o.currency, locale);
  const unpaid = (UNPAID_REASONS as readonly string[]).includes(o.unpaid_reason)
    ? t(`unpaid.${o.unpaid_reason}` as MessageKey)
    : null;

  return (
    <article className="dense" aria-labelledby="order-title">
      <p className="back-link">
        <Link to="/o/$orgId/orders" params={{ orgId }}>
          {t("common.back")}
        </Link>
      </p>

      <section className="ord-card ord-head">
        <div className="ord-head-main">
          <div className="ord-title-row">
            <h1 id="order-title">{t("order.title", { number: orderNumber(o.system_id) })}</h1>
            <OrderStatusBadge status={o.status} />
            <strong className="ord-total">{money(o.total)}</strong>
          </div>
          {o.event_name !== "" && (
            <p className="ord-event">
              <Link to="/o/$orgId/events/$eventId" params={{ orgId, eventId: o.event_id }}>
                {o.event_name}
              </Link>{" "}
              <span className="muted">{formatDateTime(o.session_start_at, tz, locale)}</span>
            </p>
          )}
        </div>
        <div className="ord-head-buyer" aria-label={t("order.buyer")} role="group">
          <BuyerContact order={o} />
        </div>
      </section>

      {unpaid !== null && o.unpaid_reason.length > 0 && (
        <section className="notice notice-warn" aria-labelledby="order-unpaid">
          <h2 id="order-unpaid">{t("order.unpaid")}</h2>
          <p>{unpaid}</p>
          {o.payment?.failure_message !== null && o.payment?.failure_message !== undefined && (
            <p className="small">{o.payment.failure_message.slice(0, 200)}</p>
          )}
        </section>
      )}

      <section className="ord-card ord-flush" aria-labelledby="order-tickets">
        <div className="ord-card-head">
          <h2 id="order-tickets">{t("order.ticketsCount", { count: o.tickets.length })}</h2>
        </div>
        <OrderTickets orgId={orgId} order={o} canUpdate={access.can("ticket.update")} />
        <div className="ord-card-foot">
          <CancelOrder orgId={orgId} orderId={o.id} systemId={o.system_id} status={o.status} canWrite={access.can("order.write")} />
          <RefundOrder
            // Another organization or order is another refund: its ticked tickets, its reason, its
            // Idempotency-Key and an answer in flight must never be reused for this one.
            key={`${orgId}:${o.id}`}
            orgId={orgId}
            orderId={o.id}
            systemId={o.system_id}
            status={o.status}
            canCreate={access.can("refund.create") && access.can("refund.read")}
            canApprove={access.can("refund.approve")}
          />
        </div>
      </section>

      <OrderLetters orgId={orgId} order={o} can={access.can} />

      <section className="ord-card" aria-labelledby="order-payment">
        <h2 id="order-payment">{t("order.payment")}</h2>
        <div className="ord-money">
          <dl className="facts-grid compact">
            <dt>{t("order.subtotal")}</dt>
            <dd className="num">{money(o.subtotal)}</dd>
            {o.discount > 0 && (
              <>
                <dt>{t("order.discount")}</dt>
                <dd className="num">−{money(o.discount)}</dd>
              </>
            )}
            {o.charge > 0 && (
              <>
                <dt>{t("order.charge")}</dt>
                <dd className="num">{money(o.charge)}</dd>
              </>
            )}
            <dt className="strong">{t("order.total")}</dt>
            <dd className="num strong">{money(o.total)}</dd>
          </dl>
          <dl className="facts-grid compact">
            <dt>{t("order.created")}</dt>
            <dd>{formatDateTime(o.created_at, tz, locale)}</dd>
            {o.paid_at !== null && (
              <>
                <dt>{t("order.paidAt")}</dt>
                <dd>{formatDateTime(o.paid_at, tz, locale)}</dd>
              </>
            )}
            <dt>{t("order.channel")}</dt>
            <dd>
              {t(channelKey(o.channel.kind))} · {o.channel.name}
            </dd>
            {o.payment !== null && (
              <>
                <dt>{t("order.payment")}</dt>
                <dd>{t("order.provider", { name: o.payment.provider })}</dd>
              </>
            )}
          </dl>
        </div>
      </section>
    </article>
  );
}
