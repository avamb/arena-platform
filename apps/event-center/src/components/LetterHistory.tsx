/**
 * Every letter the buyer was sent for this order, one line each, behind a
 * disclosure (owner decision 2026-10-30): the first ticket letter, every
 * resend and the address it went to, the change, cancellation and refund
 * letters, and what the mail service reported about each one. This is what
 * the organizer reads when a buyer says "I never got it" and when a
 * chargeback asks for proof of delivery. It shows only what the order card
 * already carries (letters[]); nothing is fetched.
 */
import type { OrderDetail } from "@/lib/api/types";
import { formatShortDateTime, zoneLabel } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { letterEvents, letterHistory } from "@/lib/letters";

const KINDS = ["ticket", "change", "cancel", "refund"] as const;
const FAILURES = ["hard_bounce", "soft_bounce", "invalid_email", "blocked", "error", "send_failed"] as const;

export function LetterHistory({ order }: { order: OrderDetail }) {
  const { t, locale } = useI18n();
  const entries = letterHistory(order);
  if (entries.length === 0) return null;
  const tz = order.venue_timezone;
  const numbers = new Map(order.tickets.map((tk) => [tk.id, String(tk.system_ticket_id)]));

  return (
    <details className="letter-history" data-testid="letter-history">
      <summary>{t("letter.hist.title", { count: entries.length })}</summary>
      <ol className="letter-history-list">
        {entries.map(({ letter: l, resend, otherAddress }, i) => {
          const kind = (KINDS as readonly string[]).includes(l.kind) ? l.kind : "other";
          const events = letterEvents(l);
          const failure = (FAILURES as readonly string[]).includes(l.failure_event ?? "") ? l.failure_event : null;
          const tickets = l.ticket_ids.map((id) => numbers.get(id) ?? "?");
          const first = events[0]?.at ?? null;
          return (
            <li key={l.id ?? `${l.kind}-${i}`} className="letter-entry" data-testid="letter-entry" data-kind={l.kind} data-status={l.status}>
              <div className="letter-entry-head">
                <strong>
                  {t(`letter.hist.kind.${kind}` as MessageKey)}
                  {l.kind === "ticket" && <span className="muted"> · {t(resend ? "letter.hist.resend" : "letter.hist.first")}</span>}
                </strong>
                {first !== null && <time dateTime={first}>{formatShortDateTime(first, tz, locale)}</time>}
              </div>
              {l.recipient.trim() !== "" && (
                <div className="letter-entry-to">
                  <span className="mono-soft">{l.recipient}</span>{" "}
                  <span className={`badge ${otherAddress ? "badge-wait" : "badge-neutral"}`}>
                    {t(otherAddress ? "letter.hist.otherAddress" : "letter.hist.orderAddress")}
                  </span>
                </div>
              )}
              {tickets.length > 0 && <div className="muted small">{t("letter.hist.tickets", { list: tickets.join(", ") })}</div>}
              {events.length > 0 && (
                <ul className="letter-events">
                  {events.map((e) => (
                    <li key={e.key} className={`letter-ev letter-ev-${e.key}`}>
                      {t(`letter.hist.ev.${e.key}` as MessageKey, { time: formatShortDateTime(e.at, tz, locale) })}
                    </li>
                  ))}
                </ul>
              )}
              {l.status === "failed" && (failure !== null || (l.failure_reason ?? "") !== "") && (
                <p className="bad-text small">
                  {failure !== null && t(`letter.hist.fail.${failure}` as MessageKey)}
                  {(l.failure_reason ?? "") !== "" && <> {t("letter.hist.why", { why: (l.failure_reason ?? "").slice(0, 200) })}</>}
                </p>
              )}
              {l.stuck && <p className="bad-text small">{t("letter.hist.stuck")}</p>}
              {l.proxy_opened_at !== null && <p className="muted small">{t("letter.hist.proxyNote")}</p>}
            </li>
          );
        })}
      </ol>
      <p className="muted small">{t("letter.hist.note", { zone: zoneLabel(tz) || "UTC" })}</p>
    </details>
  );
}
