/**
 * V1: the organizer's private VIP mark of one ticket, on the order card.
 *
 * The badge shows for everybody who can read the order. The button shows only
 * for a person who holds ticket.update in this organization (A1: by permission,
 * never by role name), and only for an active ticket. The press carries the
 * WANTED value (like the bot's `or:vip:1:` / `or:vip:0:`), so a repeated press
 * repeats a state and never flips it back. The mark is never printed on the
 * PDF e-ticket and never written in a letter to the buyer.
 */
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Badge } from "@/components/ui";
import { setTicketVip } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";

export function TicketVipBadge({ vip }: { vip: boolean }) {
  const { t } = useI18n();
  if (!vip) return null;
  return (
    <span data-testid="ticket-vip-badge">
      <Badge tone="wait">★ {t("vip.badge")}</Badge>
    </span>
  );
}

export function TicketVipButton({
  orgId,
  orderId,
  ticketId,
  number,
  vip,
  active,
  canUpdate,
  compact = false,
}: {
  orgId: string;
  orderId: string;
  ticketId: string;
  number: number;
  vip: boolean;
  active: boolean;
  canUpdate: boolean;
  /** A small star button for a row of the tickets table; the full sentence is its accessible name. */
  compact?: boolean;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  if (!canUpdate || !active) return null;

  const press = async () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await setTicketVip(orgId, ticketId, !vip);
      await queryClient.invalidateQueries({ queryKey: ["order", orgId, orderId] });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  if (compact) {
    return (
      <>
        <button
          type="button"
          className={vip ? "btn btn-sm vip-toggle vip-on" : "btn btn-sm vip-toggle"}
          disabled={busy}
          aria-label={t(vip ? "vip.unmark" : "vip.mark", { number })}
          title={t(vip ? "vip.unmark" : "vip.mark", { number })}
          onClick={() => void press()}
          data-testid={vip ? "ticket-vip-off" : "ticket-vip-on"}
        >
          <span aria-hidden="true">{vip ? "★" : "☆"}</span> {t("vip.badge")}
        </button>
        {error !== null && <ErrorNotice error={error} testId="ticket-vip-error" />}
      </>
    );
  }

  return (
    <>
      <p>
        <button
          type="button"
          className="btn"
          disabled={busy}
          onClick={() => void press()}
          data-testid={vip ? "ticket-vip-off" : "ticket-vip-on"}
        >
          {t(vip ? "vip.unmark" : "vip.mark", { number })}
        </button>
      </p>
      {error !== null && <ErrorNotice error={error} testId="ticket-vip-error" />}
    </>
  );
}
