import { Link } from "@tanstack/react-router";
import type { OrderDetail } from "@/lib/api/types";
import { formatDateTime, formatMoney } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { orderNumber } from "@/lib/orders";
import { cashbackRowKind } from "@/lib/referral";

interface Props {
  orgId: string;
  order: OrderDetail;
}

/**
 * The order's part in "Refer a friend" (spec 52), the bot's lines on the web:
 * on a referrer's order, the cashback each friend's order earned (paid with
 * its date, on its way, or not paid) with a link to the friend's order; on a
 * friend's order, the referrer's code it was bought with and the referrer's
 * order. Nothing for an order with no part in the programme.
 */
export function OrderReferral({ orgId, order }: Props) {
  const { t, locale } = useI18n();
  const r = order.referral;
  if (r === null || (r.cashback.length === 0 && r.used_code === null)) return null;
  const tz = order.venue_timezone;
  const friendOrder = (id: string | null, num: number | null) => {
    if (num === null) return null;
    const text = t("order.referral.friendOrder", { number: orderNumber(num) });
    return id === null ? (
      text
    ) : (
      <Link to="/o/$orgId/orders/$orderId" params={{ orgId, orderId: id }}>
        {text}
      </Link>
    );
  };
  return (
    <section className="ord-card" aria-labelledby="order-referral" data-testid="order-referral">
      <h2 id="order-referral">{t("order.referral.title")}</h2>
      <ul className="plain-list">
        {r.cashback.map((c, i) => {
          const amount = formatMoney(c.amount, c.currency, locale);
          const kind = cashbackRowKind(c.state);
          const text =
            kind === "paid"
              ? t("order.referral.cashbackPaid", {
                  amount,
                  date: c.paid_at === null ? "" : formatDateTime(c.paid_at, tz, locale),
                })
              : kind === "pending"
                ? t("order.referral.cashbackPending", { amount })
                : t("order.referral.cashbackUnpaid");
          const link = friendOrder(c.friend_order_id, c.friend_order_number);
          return (
            <li key={c.friend_order_id ?? i} data-testid="order-referral-cashback">
              {text}
              {link !== null && <> · {link}</>}
            </li>
          );
        })}
        {r.used_code !== null && (
          <li data-testid="order-referral-code">
            {t("order.referral.usedCode", { code: r.used_code.code })}
            {r.used_code.referrer_order_number !== null && (
              <>
                {" · "}
                {r.used_code.referrer_order_id === null ? (
                  t("order.referral.referrerOrder", { number: orderNumber(r.used_code.referrer_order_number) })
                ) : (
                  <Link to="/o/$orgId/orders/$orderId" params={{ orgId, orderId: r.used_code.referrer_order_id }}>
                    {t("order.referral.referrerOrder", { number: orderNumber(r.used_code.referrer_order_number) })}
                  </Link>
                )}
              </>
            )}
          </li>
        )}
      </ul>
    </section>
  );
}
