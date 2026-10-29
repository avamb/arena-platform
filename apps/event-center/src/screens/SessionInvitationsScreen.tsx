import { Link, useParams } from "@tanstack/react-router";
import { SeatInvite } from "@/components/staffInvitations/SeatInvite";
import { StaffInvitationList } from "@/components/staffInvitations/StaffInvitationList";
import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

/**
 * Invitations of ONE session on its seat map (spec 46, 4.1): pick seats, name
 * the guest, issue free tickets, see and cancel the session's invitations.
 * Decides by `can()` from GET .../me, never by role: the list needs
 * `complimentary.read`, issuing `complimentary.issue`, cancelling
 * `complimentary.revoke`, the hall map `session.seats.manage`.
 *
 * The organization-wide screen of general-admission invitations is
 * `InvitationsScreen` (route /invitations).
 */
export function SessionInvitationsScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const { eventId, sessionId } = useParams({ from: "/o/$orgId/events/$eventId/sessions/$sessionId/invitations" });
  const canRead = access.can("complimentary.read");
  const canIssue = access.can("complimentary.issue");
  const canRevoke = access.can("complimentary.revoke");

  return (
    <article aria-labelledby="seat-inv-title" className="inv-article">
      <p>
        <Link to="/o/$orgId/events/$eventId/sessions/$sessionId" params={{ orgId, eventId, sessionId }} data-testid="inv-back">
          {t("common.back")}
        </Link>
      </p>
      <h1 id="seat-inv-title">{t("seatInv.title")}</h1>
      {!canRead && !canIssue ? (
        <p className="notice notice-bad" role="alert" data-testid="inv-no-access">
          {t("seatInv.noAccess")}
        </p>
      ) : (
        <>
          <p className="muted">{t("seatInv.intro")}</p>
          {canIssue && (
            <SeatInvite
              key={`${orgId}:${sessionId}`}
              orgId={orgId}
              eventId={eventId}
              sessionId={sessionId}
              canSeeHall={access.can("session.seats.manage")}
              canRead={canRead}
            />
          )}
          {canRead && (
            <StaffInvitationList key={`${orgId}:${sessionId}:list`} orgId={orgId} sessionId={sessionId} canIssue={canIssue} canRevoke={canRevoke} />
          )}
        </>
      )}
    </article>
  );
}
