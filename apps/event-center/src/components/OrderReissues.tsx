/**
 * The history of ticket replacements on the order card (spec 63): when, the
 * earlier and the new address (masked), the state of the letter, the reason and
 * the team's note, the old and new numbers, and the hint about the payment
 * system's copy of the address when arena could not move it. Nothing is drawn
 * for an order that was never replaced, or when the history cannot be read
 * (the rest of the card stays usable).
 */
import { useQuery } from "@tanstack/react-query";
import { fetchReissueHistory } from "@/lib/api/endpoints";
import type { OrderDetail } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { historyStateKey, maskedOrDash, pairsText, providerNoteKey, reasonKey } from "@/lib/reissue";

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
              {typeof e.old_code_scanned_at === "string" && e.old_code_scanned_at !== "" && (
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
