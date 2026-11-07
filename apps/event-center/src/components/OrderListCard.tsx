/**
 * One order of the list as an EXPANDED card (EC-V3, spec 62 section 12.2, the
 * WordPress ops center's list): number, status and total; the event and the
 * session; the buyer with call and WhatsApp buttons; every ticket with its
 * status and the state of its letter. The order screen stays for the actions
 * (refund, resend, cancel), reached by the number or the button.
 */
import { Link } from "@tanstack/react-router";
import { Badge } from "@/components/ui";
import { BuyerContact } from "@/components/BuyerContact";
import type { OrderListItem, OrderListTicket } from "@/lib/api/types";
import { formatDateTime, formatMoney } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { ticketLetterKey, ticketLetterTone } from "@/lib/orderCards";
import { orderNumber } from "@/lib/orders";
import { OrderStatusBadge } from "@/screens/OrdersScreen";

function ticketStatusKey(status: string): MessageKey {
  return status === "active" || status === "cancelled" || status === "transferred" ? `ticket.status.${status}` : "ticket.status.active";
}

function TicketLine({ ticket, currency }: { ticket: OrderListTicket; currency: string }) {
  const { t, locale } = useI18n();
  const what = [ticket.tier_name, ticket.seat_label].filter((x): x is string => x !== null && x !== "").join(" · ");
  return (
    <li className="oc-ticket" data-testid="order-card-ticket" data-letter={ticket.delivery_status}>
      <span className="oc-tk-num mono">{ticket.system_ticket_id}</span>
      <span className="oc-tk-what">{what}</span>
      <span className="oc-tk-price num">{formatMoney(ticket.price, currency, locale)}</span>
      <span className="oc-tk-badges">
        <span className={`badge badge-dot ${ticket.status === "active" ? "badge-ok" : "badge-neutral"}`}>{t(ticketStatusKey(ticket.status))}</span>
        <Badge tone={ticketLetterTone(ticket.delivery_status)}>
          {t("orders.card.letter")}: {t(ticketLetterKey(ticket.delivery_status))}
        </Badge>
      </span>
      {ticket.recipient_email !== null && ticket.delivery_status !== "none" && <span className="oc-tk-to muted small">{ticket.recipient_email}</span>}
    </li>
  );
}

export function OrderListCard({ orgId, order }: { orgId: string; order: OrderListItem }) {
  const { t, locale } = useI18n();
  const tickets = order.tickets ?? [];
  return (
    <li className="order-card" data-testid="order-row">
      <div className="oc-head">
        <Link to="/o/$orgId/orders/$orderId" params={{ orgId, orderId: order.id }} className="oc-num">
          {orderNumber(order.system_id)}
        </Link>
        <span className="oc-badges">
          <OrderStatusBadge status={order.status} />
          {order.letter_failed && <Badge tone="bad">{t("orders.letterFailed")}</Badge>}
          {order.is_test && <Badge tone="wait">{t("orders.testOrder")}</Badge>}
        </span>
        <strong className="oc-total">{formatMoney(order.total, order.currency, locale)}</strong>
      </div>
      <p className="oc-event">
        <span>{order.event_name}</span> <span className="muted">{formatDateTime(order.session_start_at, order.session_timezone, locale)}</span>
      </p>
      <BuyerContact order={order} />
      {tickets.length > 0 && (
        <ul className="oc-tickets" aria-label={t("orders.card.tickets", { count: tickets.length })}>
          {tickets.map((tk) => (
            <TicketLine key={tk.id} ticket={tk} currency={order.currency} />
          ))}
        </ul>
      )}
      <p className="oc-foot">
        <Link to="/o/$orgId/orders/$orderId" params={{ orgId, orderId: order.id }} className="btn btn-outline" data-testid="order-card-open">
          {t("orders.card.open")}
        </Link>
      </p>
    </li>
  );
}
