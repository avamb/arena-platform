/**
 * The letters block of the order card (4-1): the ticket letter as a scale of
 * three points (sent, delivered, opened), the refund letter as a line, the
 * "Resend tickets" action for a person who holds ticket.update in this
 * organization (A1: by permission, never by role name), and the history of
 * every letter behind a disclosure. When the ticket letter failed the same
 * action is offered as "Send the ticket e-mail again". "Replace tickets" (spec
 * 63) stands beside it: its first question sends a buyer who writes from the
 * order's own address back to the resend.
 *
 * The status sentence is always in the page: visible when something needs
 * words (queued, partial, delayed, stuck, bounced, failed, none), read only by
 * screen readers when the scale says it all.
 */
import { useState } from "react";
import { LetterHistory } from "@/components/LetterHistory";
import { LetterSteps } from "@/components/LetterSteps";
import { ReissueTicketsDialog } from "@/components/ReissueTicketsDialog";
import { ResendTicketsDialog } from "@/components/ResendTicketsDialog";
import type { OrderDetail } from "@/lib/api/types";
import { zoneLabel } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { letterLine } from "@/lib/letterText";
import { letterFailed, letterTone, refundLetterStatus, ticketLetterStatus, ticketLetterSteps, type LetterStatus } from "@/lib/letters";
import { orderNumber } from "@/lib/orders";
import { reissueOffered } from "@/lib/reissue";
import { resendOffered } from "@/lib/resend";

function toneClass(s: LetterStatus): string {
  const tone = letterTone(s);
  return tone === "ok" ? "notice-ok" : tone === "bad" ? "notice-bad" : "";
}

/** States the scale shows completely, so the sentence needs no box of its own. */
const SAID_BY_SCALE = new Set(["sent", "delivered", "opened"]);

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
  const [reissueOpen, setReissueOpen] = useState(false);
  const tz = order.venue_timezone;
  const tickets = order.tickets.length === 0 ? null : ticketLetterStatus(order);
  const refund = refundLetterStatus(order);
  const offered = resendOffered(order, can);
  const reissue = reissueOffered(order, can);
  if (tickets === null && refund === null && !offered && !reissue) return null;
  const failed = tickets !== null && letterFailed(tickets);
  const steps = tickets !== null ? ticketLetterSteps(order, tickets) : null;
  const ticketText = tickets !== null ? letterLine(t, "tickets", tickets, tz, locale) : "";
  const quiet = tickets !== null && steps !== null && SAID_BY_SCALE.has(tickets.state);

  return (
    <section className="ord-card" aria-labelledby="order-letters" data-testid="order-letters">
      <div className="ord-card-head">
        <h2 id="order-letters">{t("letter.title")}</h2>
      </div>
      {tickets !== null && steps !== null && <LetterSteps steps={steps} status={tickets} tz={tz} />}
      {tickets !== null && ticketText !== "" && (
        <p
          className={quiet ? "visually-hidden" : `notice ${toneClass(tickets)}`}
          data-testid="delivery-state"
          data-letter-state={tickets.state}
        >
          {ticketText}
        </p>
      )}
      {quiet && tickets !== null && tickets.to !== "" && (
        <p className="steps-caption muted small">{t("letter.stepsTo", { to: tickets.to, zone: zoneLabel(tz) || "UTC" })}</p>
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
      {(offered || reissue) && (
        <p className="ord-card-action">
          {offered && (
            <button
              type="button"
              className={failed ? "btn btn-primary" : "btn"}
              onClick={() => setOpen(true)}
              data-testid={failed ? "resend-retry-open" : "resend-open"}
            >
              {t(failed ? "resend.retryBtn" : "resend.btn")}
            </button>
          )}{" "}
          {reissue && (
            <button type="button" className="btn" onClick={() => setReissueOpen(true)} data-testid="reissue-open">
              {t("reissue.btn")}
            </button>
          )}
        </p>
      )}
      <LetterHistory order={order} />
      {open && <ResendTicketsDialog orgId={orgId} order={order} onClose={() => setOpen(false)} />}
      {reissueOpen && (
        <ReissueTicketsDialog
          orgId={orgId}
          target={{ kind: "order", id: order.id, number: orderNumber(order.system_id), timezone: order.venue_timezone }}
          onClose={() => setReissueOpen(false)}
          onUseResend={() => {
            // The buyer writes from the order's own address: the same tickets, sent elsewhere.
            setReissueOpen(false);
            setOpen(true);
          }}
        />
      )}
    </section>
  );
}
