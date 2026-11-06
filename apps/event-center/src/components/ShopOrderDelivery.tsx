/**
 * The delivery of a shop order on its card (spec 61, section 7b): the current step and the history of the steps,
 * who set each one, from where and when (the shop's time zone). Pure: it shows the order it is given.
 * A digital or service order has no steps and shows nothing.
 */
import { Badge } from "@/components/ui";
import type { ShopOrder } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { formatShopTime } from "@/lib/shop";
import { isFulfillmentStep, orderStep, reasonLabelKey, stepLabelKey, stepTone, viaLabelKey, waitingDays } from "@/lib/shopFulfillment";

/** The badge of a step, or an empty cell for an order with no step. `now` is fixed in tests. */
export function ShopStepBadge({ order, now }: { order: Pick<ShopOrder, "status" | "delivery_kind"> & { fulfillment_step?: string | null; shipped_at?: string | null; paid_at?: string | null }; now?: Date }) {
  const { t } = useI18n();
  const step = orderStep(order);
  if (step === null) return <span className="muted">—</span>;
  const days = waitingDays(order, now);
  return (
    <>
      <Badge tone={stepTone(step)}>{t(stepLabelKey(step))}</Badge>
      {days !== null && (
        <span className="muted" data-testid="shop-order-waiting-days">
          {" "}
          {t("shop.fulfillment.waitingDays", { days })}
        </span>
      )}
    </>
  );
}

export function ShopOrderDelivery({ order, country }: { order: ShopOrder; country?: string | null }) {
  const { t, locale } = useI18n();
  const step = orderStep(order);
  if (step === null) return null;
  const events = order.events ?? [];
  return (
    <section aria-labelledby="shop-order-delivery" data-testid="shop-order-delivery">
      <h2 id="shop-order-delivery">{t("shop.fulfillment.title")}</h2>
      <p>
        <span className="muted">{t("shop.fulfillment.current")}: </span>
        <ShopStepBadge order={order} />
      </p>
      {events.length === 0 ? (
        <p className="muted" data-testid="shop-order-steps-empty">
          {t("shop.fulfillment.noHistory")}
        </p>
      ) : (
        <ol className="facts" data-testid="shop-order-steps">
          {events.map((e) => {
            const reasonKey = reasonLabelKey(e.reason);
            const viaKey = viaLabelKey(e.via);
            const parts: string[] = [
              isFulfillmentStep(e.step) ? t(stepLabelKey(e.step)) : e.step,
              e.actor_name,
              viaKey !== null ? t(viaKey) : e.via,
              formatShopTime(e.created_at, country, locale),
            ].filter((p) => p !== "");
            return (
              <li key={e.id} data-testid="shop-order-step">
                {parts.join(" · ")}
                {e.tracking !== "" && (
                  <>
                    <br />
                    <span className="muted">{t("shop.fulfillment.trackingLine", { tracking: e.tracking })}</span>
                  </>
                )}
                {e.reason !== "" && (
                  <>
                    <br />
                    <span className="muted">{t("shop.fulfillment.reasonLine", { reason: reasonKey !== null ? t(reasonKey) : e.reason })}</span>
                  </>
                )}
                {e.note !== "" && (
                  <>
                    <br />
                    <span className="muted">{e.note}</span>
                  </>
                )}
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}
