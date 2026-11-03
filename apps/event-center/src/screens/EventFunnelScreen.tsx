import { Link, useParams } from "@tanstack/react-router";
import { SalesFunnel } from "@/components/funnel/SalesFunnel";
import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

/**
 * "Sales funnel" of one event (spec 50, A7). Opened from the event's page. The
 * working part is the SalesFunnel component; this is the frame around it. It
 * needs `report.read`, the route's own gate: the owner and the manager hold it.
 */
export function EventFunnelScreen() {
  const { t } = useI18n();
  const { orgId, orgName, access } = useOrg();
  const { eventId } = useParams({ from: "/o/$orgId/events/$eventId/funnel" });

  return (
    <article aria-labelledby="funnel-title">
      <p>
        <Link to="/o/$orgId/events/$eventId" params={{ orgId, eventId }} data-testid="funnel-back">
          {t("common.back")}
        </Link>
      </p>
      <h1 id="funnel-title">{t("funnel.title")}</h1>
      {access.can("report.read") ? (
        <SalesFunnel orgId={orgId} eventId={eventId} />
      ) : (
        <p className="notice" data-testid="funnel-no-access">
          {t("funnel.noAccess", { org: orgName })}
        </p>
      )}
    </article>
  );
}
