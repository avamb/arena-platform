/**
 * The working part of the invitations of ONE session (spec 46, 4.1): pick
 * seats, name the guest, issue free tickets, see and cancel the session's
 * invitations. Shared by the web screen (SessionInvitationsScreen) and the
 * Telegram mini app page (TgInvitationsScreen), which differ only in the frame
 * around it.
 *
 * Decides by `can()` from GET .../me, never by role: the list needs
 * `complimentary.read`, issuing `complimentary.issue`, cancelling
 * `complimentary.revoke`, the hall map `session.seats.manage`.
 */
import { CsvUpload } from "@/components/staffInvitations/CsvUpload";
import { SeatInvite } from "@/components/staffInvitations/SeatInvite";
import { StaffInvitationList } from "@/components/staffInvitations/StaffInvitationList";
import { useI18n } from "@/lib/i18n";

interface Props {
  orgId: string;
  eventId: string;
  sessionId: string;
  can: (permission: string) => boolean;
  /** The CSV upload (INV-06b): the web screen only, the mini app links to it instead. */
  withCsv?: boolean;
}

export function SessionInvitations({ orgId, eventId, sessionId, can, withCsv = false }: Props) {
  const { t } = useI18n();
  const canRead = can("complimentary.read");
  const canIssue = can("complimentary.issue");
  const canRevoke = can("complimentary.revoke");

  if (!canRead && !canIssue) {
    return (
      <p className="notice notice-bad" role="alert" data-testid="inv-no-access">
        {t("seatInv.noAccess")}
      </p>
    );
  }
  return (
    <>
      <p className="muted">{t("seatInv.intro")}</p>
      {canIssue && (
        <SeatInvite key={`${orgId}:${sessionId}`} orgId={orgId} eventId={eventId} sessionId={sessionId} canSeeHall={can("session.seats.manage")} canRead={canRead} />
      )}
      {withCsv && canIssue && <CsvUpload key={`${orgId}:${sessionId}:csv`} orgId={orgId} eventId={eventId} sessionId={sessionId} canRead={canRead} />}
      {canRead && <StaffInvitationList key={`${orgId}:${sessionId}:list`} orgId={orgId} sessionId={sessionId} canIssue={canIssue} canRevoke={canRevoke} />}
    </>
  );
}
