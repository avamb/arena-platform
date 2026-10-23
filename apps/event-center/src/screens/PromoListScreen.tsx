import { Link, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { listPromoCodes } from "@/lib/api/endpoints";
import type { PromoCodeItem } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { eventNames } from "@/lib/promoScope";
import { promoDiscountText, promoLimitsText, promoSessionsText, promoUsageText, promoValidText } from "@/lib/promoView";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { PromoStateBadge } from "@/components/PromoStateBadge";
import { NoPromoAccess } from "@/screens/PromoNoAccess";
import { useOrg } from "@/screens/OrgLayout";

function PromoRow({ orgId, item }: { orgId: string; item: PromoCodeItem }) {
  const { t, locale } = useI18n();
  return (
    <li className="card" data-testid="promo-row">
      <Link to="/o/$orgId/promo/$codeId" params={{ orgId, codeId: item.id }} className="card-link">
        <span className="card-title mono" data-testid="promo-code">
          {item.code}
        </span>
        <span className="card-meta">
          <PromoStateBadge item={item} />
          <span>{promoDiscountText(item, t, locale)}</span>
        </span>
        <span className="muted">
          {promoSessionsText(item, t)} · {promoLimitsText(item, t)} · {promoValidText(item, t, locale)}
        </span>
        {item.sessions.length > 0 && (
          <span className="muted" data-testid="promo-events">
            {t("promo.scope.eventsShort", { events: eventNames(item).join(", ") })}
          </span>
        )}
        {item.warnings.length > 0 && (
          <span className="notice notice-warn" data-testid="promo-row-warn">
            {t("promo.warn.count", { count: item.warnings.length })}
          </span>
        )}
        <span className="muted" data-testid="promo-usage">
          {promoUsageText(item, t, locale)}
        </span>
      </Link>
    </li>
  );
}

export function PromoListScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const search = useSearch({ from: "/o/$orgId/promo" });
  const canRead = access.can("promo.read");
  const codes = useQuery({
    queryKey: ["promoCodes", orgId],
    queryFn: () => listPromoCodes(orgId),
    enabled: canRead,
  });

  if (!canRead) return <NoPromoAccess />;
  const list = codes.data?.promo_codes ?? [];
  return (
    <section aria-labelledby="promo-title">
      <h1 id="promo-title">{t("promo.title")}</h1>
      {search.deleted !== undefined && (
        <p className="notice notice-ok" role="status" data-testid="promo-notice">
          {t("promo.deleted", { code: search.deleted })}
        </p>
      )}
      {access.can("promo.create") && (
        <div className="actions">
          <Link to="/o/$orgId/promo/new" params={{ orgId }} className="btn btn-primary" data-testid="promo-new">
            {t("promo.new")}
          </Link>
        </div>
      )}
      {codes.isLoading && <Loading />}
      {codes.error !== null && <ErrorBox error={codes.error} onRetry={() => void codes.refetch()} />}
      {codes.data !== undefined && list.length === 0 && <Empty>{t("promo.empty")}</Empty>}
      {list.length > 0 && (
        <p className="muted" role="status" data-testid="promo-count">
          {t("promo.count", { count: list.length })}
        </p>
      )}
      <ul className="cards" data-testid="promo-list">
        {list.map((item) => (
          <PromoRow key={item.id} orgId={orgId} item={item} />
        ))}
      </ul>
    </section>
  );
}
