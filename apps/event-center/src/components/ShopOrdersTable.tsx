/**
 * The orders of the shop as a table (spec 47, section 6): number, date,
 * buyer, total, status and the kind of delivery. Pure: it shows the rows it
 * is given. On a phone every row becomes a card (the `.table` rules).
 */
import { Link } from "@tanstack/react-router";
import { Badge } from "@/components/ui";
import { formatDateTime, formatMoney } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { browserZone, isShopOrderStatus, shopOrderNumber, shopOrderTone } from "@/lib/shop";
import type { ShopOrder } from "@/lib/api/types";

export function ShopOrderStatusBadge({ status }: { status: string }) {
  const { t } = useI18n();
  return <Badge tone={shopOrderTone(status)}>{isShopOrderStatus(status) ? t(`shop.order.status.${status}` as MessageKey) : status}</Badge>;
}

export function deliveryLabel(kind: string): MessageKey {
  if (kind === "none") return "shop.delivery.none";
  return kind === "pickup" ? "shop.delivery.pickup" : "shop.delivery.shipping";
}

export function ShopOrdersTable({ orgId, orders }: { orgId: string; orders: readonly ShopOrder[] }) {
  const { t, locale } = useI18n();
  const zone = browserZone();
  return (
    <div className="table-wrap">
      <table className="table" data-testid="shop-orders-table">
        <thead>
          <tr>
            <th scope="col">{t("shop.orders.col.number")}</th>
            <th scope="col">{t("shop.orders.col.date")}</th>
            <th scope="col">{t("shop.orders.col.buyer")}</th>
            <th scope="col">{t("shop.orders.col.total")}</th>
            <th scope="col">{t("shop.orders.col.status")}</th>
            <th scope="col">{t("shop.orders.col.delivery")}</th>
          </tr>
        </thead>
        <tbody>
          {orders.map((o) => (
            <tr key={o.id} data-testid="shop-order-row">
              <td data-label={t("shop.orders.col.number")}>
                <Link to="/o/$orgId/shop/orders/$orderId" params={{ orgId, orderId: o.id }} data-testid="shop-order-link">
                  {shopOrderNumber(o.number)}
                </Link>
              </td>
              <td data-label={t("shop.orders.col.date")}>{formatDateTime(o.created_at, zone, locale)}</td>
              <td data-label={t("shop.orders.col.buyer")}>
                {o.buyer.name}
                {o.buyer.email !== "" && <span className="muted"> · {o.buyer.email}</span>}
              </td>
              <td data-label={t("shop.orders.col.total")}>{formatMoney(o.total, o.currency, locale)}</td>
              <td data-label={t("shop.orders.col.status")}>
                <ShopOrderStatusBadge status={o.status} />
              </td>
              <td data-label={t("shop.orders.col.delivery")}>{t(deliveryLabel(o.delivery_kind))}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
