/**
 * Where a promo code works and what looks wrong with it: the list of its
 * sessions (event, start, end of sales in the venue's zone) and the yellow
 * warning panel. Both read what the API answers with every code.
 */
import type { PromoCodeItem } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { isClubCode } from "@/lib/promo";
import { sessionLine, warningKey } from "@/lib/promoScope";

export function PromoScopeList({ item }: { item: PromoCodeItem }) {
  const { t, locale } = useI18n();
  return (
    <section aria-labelledby="promo-scope-title" data-testid="promo-scope">
      <h2 id="promo-scope-title">{t("promo.scope.title")}</h2>
      {isClubCode(item) ? (
        <p data-testid="promo-scope-all">{t("promo.scope.allNote")}</p>
      ) : (
        <ul data-testid="promo-scope-list">
          {item.sessions.map((s) => (
            <li key={s.session_id} data-testid="promo-scope-row">
              {sessionLine(s, t, locale)}
              {s.status === "cancelled" && <strong> {t("promo.warn.cancelledTag")}</strong>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function PromoWarnings({ item }: { item: PromoCodeItem }) {
  const { t, locale } = useI18n();
  if (item.warnings.length === 0) return null;
  return (
    <div className="notice notice-warn" role="status" data-testid="promo-warnings">
      <strong>{t("promo.warn.title")}</strong>
      <p>{t("promo.warn.hint")}</p>
      <ul>
        {item.warnings.map((w, i) => {
          const s = item.sessions.find((x) => x.session_id === w.session_id);
          return (
            <li key={`${w.code}-${w.session_id}-${i}`} data-testid="promo-warning">
              {t(warningKey(w.code), {
                event: w.event_name,
                start: formatDateTime(w.start_at, s?.timezone ?? "UTC", locale),
              })}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
