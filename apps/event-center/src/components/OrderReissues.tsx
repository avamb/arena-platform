/**
 * The history of ticket replacements on the order card (spec 63): when, the
 * earlier and the new address (masked), the state of the letter, the reason and
 * the team's note, the old and new numbers, and the hint about the payment
 * system's copy of the address when arena could not move it. Per replaced
 * ticket it also says what happened to the OLD code (O-5): an entry with it
 * before the replacement, a try after it with the scanner's answer, and
 * whether arena told the organization. Nothing is drawn
 * for an order that was never replaced, or when the history cannot be read
 * (the rest of the card stays usable).
 */
import { useQuery } from "@tanstack/react-query";
import { fetchReissueHistory } from "@/lib/api/endpoints";
import type { OrderDetail } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import {
  attemptResultKey,
  historyStateKey,
  maskedOrDash,
  oldCodeMarks,
  pairsText,
  providerNoteKey,
  reasonKey,
  scannerSourceKey,
} from "@/lib/reissue";

export function OrderReissues({ orgId, order }: { orgId: string; order: Pick<OrderDetail, "id" | "venue_timezone"> }) {
  const { t, locale } = useI18n();
  const history = useQuery({
    queryKey: ["reissue-history", orgId, order.id],
    queryFn: () => fetchReissueHistory(orgId, order.id),
    retry: false,
  });
  const entries = history.data?.reissues ?? [];
  if (entries.length === 0) return null;
  const tz = order.venue_timezone;
  const p = history.data?.provider;
  const provider = p !== undefined && p !== null ? (p.title !== "" ? p.title : p.name) : t("reissue.history.providerUnknown");

  return (
    <section className="ord-card" aria-labelledby="order-reissues" data-testid="order-reissues">
      <div className="ord-card-head">
        <h2 id="order-reissues">{t("reissue.history.title")}</h2>
      </div>
      <ul className="plain-list">
        {entries.map((e) => {
          const providerKey = providerNoteKey(e);
          const marks = oldCodeMarks(e.tickets);
          return (
            <li key={e.id} data-testid="reissue-entry" data-state={e.state}>
              <p>
                <strong>{formatDateTime(e.created_at, tz, locale)}</strong>{" "}
                <span data-testid="reissue-entry-addresses">
                  {t("reissue.history.addresses", { from: maskedOrDash(e.old_email), to: maskedOrDash(e.new_email) })}
                </span>
              </p>
              <p className={e.state === "send_failed" ? "notice notice-bad" : "muted"} data-testid="reissue-entry-state">
                {t(historyStateKey(e.state))}
              </p>
              <p className="small">{t("reissue.history.reason", { reason: t(reasonKey(e.reason)) })}</p>
              {e.note.trim() !== "" && <p className="small">{t("reissue.history.note", { note: e.note })}</p>}
              {e.tickets.length > 0 && (
                <p className="small" data-testid="reissue-entry-tickets">
                  {t("reissue.history.tickets", { pairs: pairsText(e.tickets) })}
                </p>
              )}
              {marks.map((m, i) => {
                const time = formatDateTime(m.at, tz, locale);
                const told = m.told ? ` ${t("reissue.history.told")}` : "";
                if (m.kind === "before") {
                  return (
                    <p key={`b${i}`} className="notice notice-warn small" data-testid="reissue-entry-old-before">
                      {t("reissue.history.oldBefore", { old: m.oldNumber, new: m.newNumber, time })}
                      {told}
                    </p>
                  );
                }
                const scanner = t(scannerSourceKey(m.source));
                const resultKey = attemptResultKey(m.result);
                return (
                  <p
                    key={`a${i}`}
                    className={m.result === "admitted" ? "notice notice-bad small" : "notice notice-warn small"}
                    data-testid="reissue-entry-old-after"
                    data-result={m.result}
                  >
                    {m.gate !== ""
                      ? t("reissue.history.oldAfterGate", { old: m.oldNumber, time, scanner, gate: m.gate })
                      : t("reissue.history.oldAfter", { old: m.oldNumber, time, scanner })}
                    {resultKey !== null ? ` ${t(resultKey)}` : ""}
                    {told}
                  </p>
                );
              })}
              {marks.length === 0 && typeof e.old_code_scanned_at === "string" && e.old_code_scanned_at !== "" && (
                <p className="small">{t("reissue.history.scanned", { time: formatDateTime(e.old_code_scanned_at, tz, locale) })}</p>
              )}
              {providerKey !== null && (
                <p className="notice notice-warn small" data-testid="reissue-entry-provider">
                  {t(providerKey, { provider })}
                </p>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
