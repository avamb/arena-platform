/**
 * The card of a shop order (spec 47, section 6): buyer, address, items,
 * totals, payment, the history and the actions. An order in `manual_review`
 * carries a notice that says what happened and what to do.
 */
import type { ReactNode } from "react";
import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { NoShopAccess, ShopNav } from "@/components/ShopNav";
import { ShopOrderActions } from "@/components/ShopOrderActions";
import { ShopOrderNote } from "@/components/ShopOrderNote";
import { ShopOrderStatusBadge, deliveryLabel } from "@/components/ShopOrdersTable";
import { ErrorBox, Loading } from "@/components/ui";
import { fetchShopOrder, fetchShopSettings } from "@/lib/api/shopApi";
import type { ShopOrder } from "@/lib/api/types";
import { formatMoney, regionName } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { formatShopTime, orderTimeline, paymentRefundKind, refundable, reviewReasonOf, shopOrderNumber } from "@/lib/shop";
import { useOrg } from "@/screens/OrgLayout";

const PAY_STATES = ["created", "processing", "succeeded", "failed"] as const;

function sourceKey(source: string): MessageKey | null {
  return source === "web" || source === "shopify" ? (`shop.order.source.${source}` as MessageKey) : null;
}

/** The order as a card. Pure: the screen gives it the order, the actions are a child. */
export function ShopOrderCard({ order, orgId, actions, note, country }: { order: ShopOrder; orgId: string; actions?: ReactNode; note?: ReactNode; country?: string | null }) {
  const { t, locale } = useI18n();
  const money = (v: number) => formatMoney(v, order.currency, locale);
  const a = order.ship;
  // An order of digital products and services only has no address block at all.
  const noDelivery = order.delivery_kind === "none";
  const hasAddress = order.delivery_kind !== "pickup" && !noDelivery && (a.address1 !== "" || a.city !== "");
  const src = sourceKey(order.source);
  const payState = order.payment !== null && order.payment !== undefined ? order.payment.state : null;
  // M-12: the payment itself stays "succeeded" after a refund; the block says what came back.
  const refundKind = paymentRefundKind(order, payState);
  const reviewReason = reviewReasonOf(order.review_reason);

  return (
    <article className="shop-page" aria-labelledby="shop-order-title" data-testid="shop-order-card">
      <p>
        <Link to="/o/$orgId/shop/orders" params={{ orgId }} search={{}}>
          {t("shop.order.back")}
        </Link>
      </p>
      <h1 id="shop-order-title">{t("shop.order.title", { number: shopOrderNumber(order.number) })}</h1>
      <p className="card-meta">
        <ShopOrderStatusBadge status={order.status} shippedAt={order.shipped_at} />
        <strong>{money(order.total)}</strong>
        <span>{t(deliveryLabel(order.delivery_kind))}</span>
      </p>

      {order.status === "expired" && (
        <p className="muted" data-testid="shop-order-expired-hint">
          {t("shop.order.status.expired.hint")}
        </p>
      )}

      {order.status === "manual_review" && (
        <div className="notice notice-warn" role="status" data-testid="shop-order-review">
          <p>
            <strong>{t("shop.order.review.title")}</strong>
          </p>
          {reviewReason !== null && (
            <p data-testid="shop-order-review-reason">
              <strong>{t("shop.order.review.reason", { reason: t(`shop.order.review.reason.${reviewReason}` as MessageKey) })}</strong>
            </p>
          )}
          <p>{reviewReason !== null ? t(`shop.order.review.text.${reviewReason}` as MessageKey) : t("shop.order.review.text")}</p>
        </div>
      )}

      {actions}

      {note}

      <section aria-labelledby="shop-order-buyer">
        <h2 id="shop-order-buyer">{t("shop.order.buyer")}</h2>
        <ul className="facts" data-testid="shop-order-buyer">
          <li>{order.buyer.name}</li>
          {order.buyer.email !== "" && (
            <li>
              <a href={`mailto:${encodeURIComponent(order.buyer.email)}`}>{order.buyer.email}</a>
            </li>
          )}
          {order.buyer.phone !== undefined && order.buyer.phone !== "" && (
            <li>
              <a href={`tel:${order.buyer.phone.replace(/[^\d+]/g, "")}`}>{order.buyer.phone}</a>
            </li>
          )}
        </ul>
      </section>

      {!noDelivery && (
      <section aria-labelledby="shop-order-address">
        <h2 id="shop-order-address">{t("shop.order.address")}</h2>
        {hasAddress ? (
          <address data-testid="shop-order-address">
            {a.name !== undefined && a.name !== "" && <>{a.name}<br /></>}
            {a.address1}
            {a.address2 !== undefined && a.address2 !== "" && <>, {a.address2}</>}
            <br />
            {[a.postal, a.city, regionName(a.country, locale)].filter((p): p is string => p !== undefined && p !== "").join(" ")}
          </address>
        ) : (
          <p className="muted">{t("shop.order.noAddress")}</p>
        )}
        {order.delivery_note !== "" && (
          <p data-testid="shop-order-comment">
            <strong>{t("shop.order.comment")}:</strong> {order.delivery_note}
          </p>
        )}
        {order.tracking_note !== "" && (
          <p data-testid="shop-order-tracking">
            <strong>{t("shop.order.tracking")}:</strong> {order.tracking_note}
          </p>
        )}
      </section>
      )}
      {noDelivery && order.delivery_note !== "" && (
        <p data-testid="shop-order-comment">
          <strong>{t("shop.order.comment")}:</strong> {order.delivery_note}
        </p>
      )}

      <section aria-labelledby="shop-order-lines">
        <h2 id="shop-order-lines">{t("shop.order.lines")}</h2>
        <div className="table-wrap">
          <table className="table" data-testid="shop-order-lines">
            <thead>
              <tr>
                <th scope="col">{t("shop.order.col.product")}</th>
                <th scope="col">{t("shop.order.col.qty")}</th>
                <th scope="col">{t("shop.order.col.unit")}</th>
                <th scope="col">{t("shop.order.col.sum")}</th>
              </tr>
            </thead>
            <tbody>
              {order.items.map((it, i) => (
                <tr key={`${it.variant_id ?? "x"}-${i}`} data-testid="shop-order-line">
                  <td data-label={t("shop.order.col.product")}>
                    {it.product_title}
                    {it.variant_title !== "" && it.variant_title !== "Default Title" && <span className="muted"> · {it.variant_title}</span>}
                    {it.sku !== null && it.sku !== undefined && it.sku !== "" && <span className="muted"> · {it.sku}</span>}
                    {it.product_kind !== undefined && it.product_kind !== "physical" && (
                      <span className="muted" data-testid="shop-order-line-kind"> · {t(`shop.kind.${it.product_kind}` as MessageKey)}</span>
                    )}
                  </td>
                  <td data-label={t("shop.order.col.qty")}>{it.qty}</td>
                  <td data-label={t("shop.order.col.unit")}>{money(it.unit_price_minor)}</td>
                  <td data-label={t("shop.order.col.sum")}>{money(it.total_minor)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <dl className="facts-grid" data-testid="shop-order-totals">
          <dt>{t("shop.order.subtotal")}</dt>
          <dd>{money(order.subtotal)}</dd>
          {order.discount > 0 && (
            <>
              <dt>{t("shop.order.discount")}</dt>
              <dd>−{money(order.discount)}</dd>
            </>
          )}
          <dt>{t("shop.order.shipping")}</dt>
          <dd>{money(order.shipping_fee)}</dd>
          <dt>{t("shop.order.total")}</dt>
          <dd>
            <strong>{money(order.total)}</strong>
          </dd>
          {order.refunded_minor > 0 && (
            <>
              <dt>{t("shop.order.refunded")}</dt>
              <dd>{money(order.refunded_minor)}</dd>
              <dt>{t("shop.order.left")}</dt>
              <dd>{money(refundable(order))}</dd>
            </>
          )}
        </dl>
      </section>

      <section aria-labelledby="shop-order-payment">
        <h2 id="shop-order-payment">{t("shop.order.payment")}</h2>
        {order.payment === null || order.payment === undefined ? (
          <p className="muted">{t("shop.order.noPayment")}</p>
        ) : (
          <ul className="facts" data-testid="shop-order-payment">
            <li>{t("shop.order.provider", { name: order.payment.provider })}</li>
            <li data-testid="shop-order-pay-state">
              {t("shop.order.payState", {
                state:
                  refundKind === "refunded"
                    ? t("shop.order.payState.refunded")
                    : refundKind === "partly"
                      ? t("shop.order.payState.partlyRefunded", { refunded: money(order.refunded_minor), total: money(order.total) })
                      : (PAY_STATES as readonly string[]).includes(payState ?? "")
                        ? t(`shop.order.payState.${payState}` as MessageKey)
                        : (payState ?? ""),
              })}
            </li>
          </ul>
        )}
        {src !== null && <p className="muted">{t("shop.order.source", { source: t(src) })}</p>}
      </section>

      <section aria-labelledby="shop-order-history">
        <h2 id="shop-order-history">{t("shop.timeline.title")}</h2>
        <ol className="facts" data-testid="shop-order-timeline">
          {orderTimeline(order).map((e, i) => (
            <li key={`${e.kind}-${i}`}>
              {e.kind === "refunded" ? t("shop.timeline.refunded", { amount: money(order.refunded_minor) }) : t(`shop.timeline.${e.kind}` as MessageKey)}
              {e.kind !== "refunded" && e.at !== null && <> · {formatShopTime(e.at, country, locale)}</>}
            </li>
          ))}
        </ol>
      </section>
    </article>
  );
}

export function ShopOrderScreen() {
  const { orgId, access } = useOrg();
  const { orderId } = useParams({ from: "/o/$orgId/shop/orders/$orderId" });
  const canRead = access.can("shop.read");
  const order = useQuery({ queryKey: ["shopOrder", orgId, orderId], queryFn: () => fetchShopOrder(orgId, orderId), enabled: canRead });
  const settings = useQuery({ queryKey: ["shopSettings", orgId], queryFn: () => fetchShopSettings(orgId), enabled: canRead });
  const country = settings.data?.country_iso2;

  if (!canRead) return <NoShopAccess />;
  if (order.isLoading) return <Loading />;
  if (order.data === undefined) return <ErrorBox error={order.error} onRetry={() => void order.refetch()} />;
  return (
    <>
      <ShopNav orgId={orgId} />
      <ShopOrderCard
        order={order.data}
        orgId={orgId}
        country={country}
        note={<ShopOrderNote key={`note:${orgId}:${order.data.id}`} orgId={orgId} order={order.data} canManage={access.can("shop.manage")} country={country} />}
        actions={<ShopOrderActions key={`${orgId}:${order.data.id}`} orgId={orgId} order={order.data} country={country} canManage={access.can("shop.manage")} canRefund={access.can("refund.create") && access.can("refund.approve")} />}
      />
    </>
  );
}
