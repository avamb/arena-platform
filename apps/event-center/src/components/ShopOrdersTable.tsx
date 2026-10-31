/**
 * The orders of the shop as a table (spec 47, section 6): number, date,
 * buyer, total, status and the kind of delivery. Pure: it shows the rows it
 * is given. On a phone every row becomes a card (the `.table` rules).
 */
import { Link } from "@tanstack/react-router";
import { Badge } from "@/components/ui";
import { formatMoney } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { formatShopTime, orderStatusLabelKey, shopOrderNumber, shopOrderTone } from "@/lib/shop";
import type { ShopOrder } from "@/lib/api/types";

/** `shippedAt`: a refund overwrites the status, so an order shipped earlier says both facts (M-10). */
export function ShopOrderStatusBadge({ status, shippedAt }: { status: string; shippedAt?: string | null }) {
  const { t } = useI18n();
  const key = orderStatusLabelKey(status, shippedAt);
  const badge = <Badge tone={shopOrderTone(status)}>{key !== null ? t(key as MessageKey) : status}</Badge>;
  // An unpaid order that ran out of time says what happened to the goods.
  return status === "expired" ? <span title={t("shop.order.status.expired.hint")}>{badge}</span> : badge;
}

export function deliveryLabel(kind: string): MessageKey {
  if (kind === "none") return "shop.delivery.none";
  return kind === "pickup" ? "shop.delivery.pickup" : "shop.delivery.shipping";
}

/** `country`: the shop's country (ISO 3166-1 alpha-2). Times are read in its zone and named by the country. */
export function ShopOrdersTable({ orgId, orders, country }: { orgId: string; orders: readonly ShopOrder[]; country?: string | null }) {
  const { t, locale } = useI18n();
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
              <td data-label={t("shop.orders.col.date")}>{formatShopTime(o.created_at, country, locale)}</td>
              <td data-label={t("shop.orders.col.buyer")}>
                {o.buyer.name}
                {o.buyer.email !== "" && <span className="muted"> · {o.buyer.email}</span>}
              </td>
              <td data-label={t("shop.orders.col.total")}>{formatMoney(o.total, o.currency, locale)}</td>
              <td data-label={t("shop.orders.col.status")}>
                <ShopOrderStatusBadge status={o.status} shippedAt={o.shipped_at} />
              </td>
              <td data-label={t("shop.orders.col.delivery")}>{t(deliveryLabel(o.delivery_kind))}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
