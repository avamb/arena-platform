import { Link, useParams } from "@tanstack/react-router";
import { SessionInvitations } from "@/components/staffInvitations/SessionInvitations";
import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

/**
 * Invitations of ONE session on its seat map (spec 46, 4.1). The working part
 * is shared with the Telegram mini app page (SessionInvitations); this is the
 * web frame around it.
 *
 * The organization-wide screen of general-admission invitations is
 * `InvitationsScreen` (route /invitations).
 */
export function SessionInvitationsScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const { eventId, sessionId } = useParams({ from: "/o/$orgId/events/$eventId/sessions/$sessionId/invitations" });

  return (
    <article aria-labelledby="seat-inv-title" className="inv-article">
      <p>
        <Link to="/o/$orgId/events/$eventId/sessions/$sessionId" params={{ orgId, eventId, sessionId }} data-testid="inv-back">
          {t("common.back")}
        </Link>
      </p>
      <h1 id="seat-inv-title">{t("seatInv.title")}</h1>
      <SessionInvitations orgId={orgId} eventId={eventId} sessionId={sessionId} can={access.can} />
    </article>
  );
}
