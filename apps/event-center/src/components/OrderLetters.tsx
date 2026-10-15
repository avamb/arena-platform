/**
 * The letters block of the order card (4-1): one line per letter the buyer got
 * (tickets, refund), and the "Resend tickets" action for a person who holds
 * ticket.update in this organization (A1: by permission, never by role name).
 * When the ticket letter failed the same action is offered as "Send the ticket
 * e-mail again".
 */
import { useState } from "react";
import { ResendTicketsDialog } from "@/components/ResendTicketsDialog";
import type { OrderDetail } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { letterLine } from "@/lib/letterText";
import { letterFailed, letterTone, refundLetterStatus, ticketLetterStatus, type LetterStatus } from "@/lib/letters";
import { resendOffered } from "@/lib/resend";

function toneClass(s: LetterStatus): string {
  const tone = letterTone(s);
  return tone === "ok" ? "notice-ok" : tone === "bad" ? "notice-bad" : "";
}

export function OrderLetters({
  orgId,
  order,
  can,
}: {
  orgId: string;
  order: OrderDetail;
  can: (permission: string) => boolean;
}) {
  const { t, locale } = useI18n();
  const [open, setOpen] = useState(false);
  const tz = order.venue_timezone;
  const tickets = order.tickets.length === 0 ? null : ticketLetterStatus(order);
  const refund = refundLetterStatus(order);
  const offered = resendOffered(order, can);
  if (tickets === null && refund === null && !offered) return null;
  const failed = tickets !== null && letterFailed(tickets);

  return (
    <section aria-labelledby="order-letters" data-testid="order-letters">
      <h2 id="order-letters">{t("letter.title")}</h2>
      {tickets !== null && letterLine(t, "tickets", tickets, tz, locale) !== "" && (
        <p
          className={`notice ${toneClass(tickets)}`}
          data-testid="delivery-state"
          data-letter-state={tickets.state}
        >
          {letterLine(t, "tickets", tickets, tz, locale)}
        </p>
      )}
      {refund !== null && letterLine(t, "refund", refund, tz, locale) !== "" && (
        <p
          className={`notice ${toneClass(refund)}`}
          data-testid="refund-letter-state"
          data-letter-state={refund.state}
        >
          {letterLine(t, "refund", refund, tz, locale)}
        </p>
      )}
      {offered && (
        <p>
          <button
            type="button"
            className={failed ? "btn btn-primary" : "btn"}
            onClick={() => setOpen(true)}
            data-testid={failed ? "resend-retry-open" : "resend-open"}
          >
            {t(failed ? "resend.retryBtn" : "resend.btn")}
          </button>
        </p>
      )}
      {open && <ResendTicketsDialog orgId={orgId} order={order} onClose={() => setOpen(false)} />}
    </section>
  );
}
