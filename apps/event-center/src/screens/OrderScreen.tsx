import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { fetchOrder } from "@/lib/api/endpoints";
import { formatDateTime, formatMoney } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { UNPAID_REASONS, orderNumber, seatText, ticketDelivery } from "@/lib/orders";
import { OrderLetters } from "@/components/OrderLetters";
import { CancelOrder } from "@/components/CancelOrder";
import { RefundOrder } from "@/components/RefundOrder";
import { TicketVipBadge, TicketVipButton } from "@/components/TicketVip";
import { TicketScans } from "@/components/TicketScans";
import { Badge, ErrorBox, Loading } from "@/components/ui";
import { useOrg } from "@/screens/OrgLayout";
import { OrderStatusBadge } from "@/screens/OrdersScreen";

function deliveryKey(status: string | undefined): MessageKey {
  switch (status) {
    case "sent":
      return "delivery.sent";
    case "pending":
    case "processing":
      return "delivery.pending";
    case "failed":
      return "delivery.failed";
    case "skipped":
    case "disabled":
      return "delivery.skipped";
    default:
      return "delivery.none";
  }
}

function channelKey(kind: string): MessageKey {
  return kind === "site" || kind === "hosted_page" || kind === "widget" ? `channel.${kind}` : "channel.other";
}

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
    <article aria-labelledby="order-title">
      <p>
        <Link to="/o/$orgId/orders" params={{ orgId }}>
          {t("common.back")}
        </Link>
      </p>
      <h1 id="order-title">{t("order.title", { number: orderNumber(o.system_id) })}</h1>
      <p className="card-meta">
        <OrderStatusBadge status={o.status} />
        <strong>{money(o.total)}</strong>
      </p>
      {o.event_name !== "" && (
        <p>
          <Link to="/o/$orgId/events/$eventId" params={{ orgId, eventId: o.event_id }}>
            {o.event_name}
          </Link>{" "}
          · {formatDateTime(o.session_start_at, tz, locale)}
        </p>
      )}

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

      <OrderLetters orgId={orgId} order={o} can={access.can} />
      {unpaid !== null && o.unpaid_reason.length > 0 && (
        <section aria-labelledby="order-unpaid">
          <h2 id="order-unpaid">{t("order.unpaid")}</h2>
          <p>{unpaid}</p>
          {o.payment?.failure_message !== null && o.payment?.failure_message !== undefined && (
            <p className="muted">{o.payment.failure_message.slice(0, 200)}</p>
          )}
        </section>
      )}

      <dl className="facts-grid">
        <dt>{t("order.subtotal")}</dt>
        <dd>{money(o.subtotal)}</dd>
        {o.discount > 0 && (
          <>
            <dt>{t("order.discount")}</dt>
            <dd>−{money(o.discount)}</dd>
          </>
        )}
        {o.charge > 0 && (
          <>
            <dt>{t("order.charge")}</dt>
            <dd>{money(o.charge)}</dd>
          </>
        )}
        <dt>{t("order.total")}</dt>
        <dd>{money(o.total)}</dd>
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

      <section aria-labelledby="order-buyer">
        <h2 id="order-buyer">{t("order.buyer")}</h2>
        {o.buyer_name === null && o.buyer_email === null && o.buyer_phone === null ? (
          <p className="muted">{t("order.noContacts")}</p>
        ) : (
          <ul className="facts" data-testid="buyer">
            {o.buyer_name !== null && <li>{o.buyer_name}</li>}
            {o.buyer_email !== null && (
              <li>
                <a href={`mailto:${encodeURIComponent(o.buyer_email)}`}>{o.buyer_email}</a>
              </li>
            )}
            {o.buyer_phone !== null && (
              <li>
                <a href={`tel:${o.buyer_phone.replace(/[^\d+]/g, "")}`}>{o.buyer_phone}</a>
              </li>
            )}
          </ul>
        )}
      </section>

      <section aria-labelledby="order-tickets">
        <h2 id="order-tickets">{t("order.tickets")}</h2>
        <ul className="cards" data-testid="ticket-list">
          {o.tickets.map((tk) => {
            const d = ticketDelivery(o, tk.id);
            const seat = seatText(tk);
            const statusKey: MessageKey =
              tk.status === "active" || tk.status === "cancelled" || tk.status === "transferred"
                ? `ticket.status.${tk.status}`
                : "ticket.status.active";
            return (
              <li key={tk.id} className="card" data-testid="ticket-row">
                <p className="card-title">
                  {t("ticket.number", { number: tk.system_ticket_id })}
                  {tk.tier_name !== null && <> · {tk.tier_name}</>} · {formatMoney(tk.price, tk.currency, locale)}
                </p>
                <p className="card-meta">
                  <Badge tone={tk.status === "active" ? "ok" : "neutral"}>{t(statusKey)}</Badge>
                  {tk.used_at !== null && <span>{t("ticket.used", { time: formatDateTime(tk.used_at, tz, locale) })}</span>}
                  <TicketVipBadge vip={tk.vip} />
                </p>
                <TicketVipButton
                  orgId={orgId}
                  orderId={o.id}
                  ticketId={tk.id}
                  number={tk.system_ticket_id}
                  vip={tk.vip}
                  active={tk.status === "active"}
                  canUpdate={access.can("ticket.update")}
                />
                <dl className="facts-grid">
                  {seat.length > 0 && (
                    <>
                      <dt>{t("ticket.seat")}</dt>
                      <dd>{seat}</dd>
                    </>
                  )}
                  <dt>{t("ticket.barcode")}</dt>
                  <dd className="mono">{tk.barcode}</dd>
                  <dt>{t("delivery.title")}</dt>
                  <dd data-testid="ticket-delivery">
                    {t(deliveryKey(d?.status), {
                      time: d?.sent_at !== null && d?.sent_at !== undefined ? formatDateTime(d.sent_at, tz, locale) : "",
                    })}
                  </dd>
                </dl>
                {/* M2 */}
                <TicketScans scans={tk.scans} tz={tz} />
              </li>
            );
          })}
        </ul>
      </section>
    </article>
  );
}
