/**
 * 4-2: "Cancel order" on the card of an order that waits for payment.
 *
 * The button opens the typed-word dialog with the ORDER's word (CANCEL
 * ORDER / ОТМЕНИТЬ ЗАКАЗ / CANCELAR PEDIDO, the English one in any
 * language). A wrong word sends nothing and keeps the dialog
 * (ConfirmWordDialog). With the right word the web asks for the password
 * again (spec 37 4.6, `withStepUp`), then calls POST .../orders/{id}/cancel.
 *
 * A 409 means the status changed while the card was open: the dialog
 * closes, the card is reloaded and says what the order is now. Nothing is
 * pretended cancelled. This mirrors the bot's `or:c:` (order_card.go).
 */
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ConfirmWordDialog } from "@/components/ConfirmWordDialog";
import { useStepUp } from "@/components/StepUpDialog";
import { cancelOrder } from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import { useI18n } from "@/lib/i18n";
import { StepUpCancelled, withStepUp } from "@/lib/stepUp";
import { canCancelOrder, orderNumber } from "@/lib/orders";
import { statusKey } from "@/screens/OrdersScreen";

export interface CancelOrderProps {
  orgId: string;
  orderId: string;
  systemId: number;
  /** The status the card shows now. */
  status: string;
  canWrite: boolean;
}

type Outcome = "done" | "changed" | null;

export function CancelOrder({ orgId, orderId, systemId, status, canWrite }: CancelOrderProps) {
  const { t } = useI18n();
  const stepUp = useStepUp();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [outcome, setOutcome] = useState<Outcome>(null);

  const reload = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["order", orgId, orderId] }),
      queryClient.invalidateQueries({ queryKey: ["orders", orgId] }),
    ]);

  const onConfirm = async () => {
    try {
      await withStepUp(orgId, (headers) => cancelOrder(orgId, orderId, headers), stepUp.ask);
    } catch (err) {
      // The person closed the password question: nothing was sent, the dialog stays.
      if (err instanceof StepUpCancelled) return;
      if (err instanceof ApiError && err.status === 409) {
        setOpen(false);
        setOutcome("changed");
        await reload();
        return;
      }
      throw err; // the dialog shows what went wrong and what to do
    }
    setOpen(false);
    setOutcome("done");
    await reload();
  };

  const key = statusKey(status);
  return (
    <>
      {outcome === "done" && (
        <p className="notice notice-ok" role="status" data-testid="cancel-order-done">
          {t("order.cancel.done")}
        </p>
      )}
      {outcome === "changed" && (
        <div className="notice notice-bad" role="alert" data-testid="cancel-order-changed">
          <p>
            <strong>{t("order.cancel.changed.title")}</strong>
          </p>
          <p>{t("order.cancel.changed.next", { status: key !== null ? t(key) : status })}</p>
        </div>
      )}
      {canCancelOrder(status, canWrite) && (
        <p>
          <button type="button" className="btn" onClick={() => setOpen(true)} data-testid="cancel-order">
            {t("order.cancel.button")}
          </button>
        </p>
      )}
      {open && (
        <ConfirmWordDialog
          kind="cancel_order"
          subject={t("order.title", { number: orderNumber(systemId) })}
          onConfirm={onConfirm}
          onCancel={() => setOpen(false)}
        />
      )}
    </>
  );
}
