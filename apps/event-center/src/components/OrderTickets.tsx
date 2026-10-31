/**
 * The tickets of an order as one table (owner decision 2026-10-30): number,
 * seat or category, price, barcode, status, actions. On a phone every row
 * folds into three short lines (styles.css, .ticket-table). Each ticket is its
 * own <tbody>, so its door scans and a letter that did not go out stay with it.
 */
import type { OrderDetail } from "@/lib/api/types";
import { formatDateTime, formatMoney } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { seatText, ticketDelivery } from "@/lib/orders";
import { TicketScans } from "@/components/TicketScans";
import { TicketVipBadge, TicketVipButton } from "@/components/TicketVip";

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

export function OrderTickets({ orgId, order, canUpdate }: { orgId: string; order: OrderDetail; canUpdate: boolean }) {
  const { t, locale } = useI18n();
  const tz = order.venue_timezone;
  return (
    <div className="table-scroll">
      <table className="ticket-table" data-testid="ticket-list">
        <thead>
          <tr>
            <th scope="col">{t("order.col.number")}</th>
            <th scope="col">{t("order.col.seat")}</th>
            <th scope="col" className="num">
              {t("order.col.price")}
            </th>
            <th scope="col">{t("order.col.barcode")}</th>
            <th scope="col">{t("order.col.status")}</th>
            <th scope="col">
              <span className="visually-hidden">{t("order.col.actions")}</span>
            </th>
          </tr>
        </thead>
        {order.tickets.map((tk) => {
          const d = ticketDelivery(order, tk.id);
          const seat = seatText(tk);
          const statusKey: MessageKey =
            tk.status === "active" || tk.status === "cancelled" || tk.status === "transferred"
              ? `ticket.status.${tk.status}`
              : "ticket.status.active";
          const letterProblem = d !== null && d.status !== "sent";
          const extra = letterProblem || (tk.scans?.length ?? 0) > 0;
          return (
            <tbody key={tk.id} data-testid="ticket-row">
              <tr>
                <td className="tk-num mono">{tk.system_ticket_id}</td>
                <td className="tk-seat">
                  {seat.length > 0 ? seat : tk.tier_name}
                  {seat.length > 0 && tk.tier_name !== null && <span className="muted tk-tier">{tk.tier_name}</span>}
                </td>
                <td className="tk-price num">{formatMoney(tk.price, tk.currency, locale)}</td>
                <td className="tk-code mono">{tk.barcode}</td>
                <td className="tk-status">
                  <span className={`badge badge-dot ${tk.status === "active" ? "badge-ok" : "badge-neutral"}`}>{t(statusKey)}</span>
                  <TicketVipBadge vip={tk.vip} />
                  {tk.used_at !== null && <span className="muted small">{t("ticket.used", { time: formatDateTime(tk.used_at, tz, locale) })}</span>}
                </td>
                <td className="tk-act">
                  <TicketVipButton
                    orgId={orgId}
                    orderId={order.id}
                    ticketId={tk.id}
                    number={tk.system_ticket_id}
                    vip={tk.vip}
                    active={tk.status === "active"}
                    canUpdate={canUpdate}
                    compact
                  />
                </td>
              </tr>
              {extra && (
                <tr className="tk-extra">
                  <td colSpan={6}>
                    {letterProblem && (
                      <p className="small" data-testid="ticket-delivery">
                        {t("delivery.title")}: {t(deliveryKey(d.status), { time: "" })}
                      </p>
                    )}
                    <TicketScans scans={tk.scans} tz={tz} />
                  </td>
                </tr>
              )}
            </tbody>
          );
        })}
      </table>
    </div>
  );
}
