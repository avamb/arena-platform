import { Link, useParams } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";
import { useEmbedded } from "@/lib/seatManager/embed";
import { SeatManager } from "@/components/seatManager/SeatManager";
import { useOrg } from "@/screens/OrgLayout";

/**
 * "Hall" of a seated session (tasks S2b and S2c): paint the seats with
 * categories, close and open them for sale, lay the hall out automatically.
 * With ?embed=1 the page drops its header and its link back, for the Telegram
 * mini app.
 */
export function SessionSeatsScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const { eventId, sessionId } = useParams({ from: "/o/$orgId/events/$eventId/sessions/$sessionId/seats" });
  const embed = useEmbedded();

  if (!access.can("session.seats.manage")) {
    return (
      <article>
        <p className="notice" role="status" data-testid="seats-no-access">
          {t("seats.noAccess")}
        </p>
        {!embed && (
          <p>
            <Link to="/o/$orgId/events/$eventId/sessions/$sessionId" params={{ orgId, eventId, sessionId }}>
              {t("common.back")}
            </Link>
          </p>
        )}
      </article>
    );
  }
  return (
    <article aria-labelledby="seats-title" className={embed ? "sm-article sm-article-embed" : "sm-article"}>
      {!embed && (
        <>
          <p>
            <Link to="/o/$orgId/events/$eventId/sessions/$sessionId" params={{ orgId, eventId, sessionId }} data-testid="seats-back">
              {t("common.back")}
            </Link>
          </p>
          <h1 id="seats-title">{t("seats.title")}</h1>
        </>
      )}
      <SeatManager key={`${orgId}:${sessionId}`} orgId={orgId} eventId={eventId} sessionId={sessionId} embed={embed} />
    </article>
  );
}
