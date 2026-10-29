/**
 * 4-4 / INV-04b: Invitations (free tickets for named guests) of the whole
 * organization: issue general admission invitations, list every staff
 * invitation of the organization (spec 46) with filters, cancel, download the
 * PDF, send the letter again. The twin of the bot's «Пригласительные».
 *
 * Issuing and listing go through the staff-invitation routes. A date with a
 * seat map is issued on its own seat screen (the form links to it). The
 * invitations issued by the old route stay visible in "Earlier invitations"
 * with their old annul.
 *
 * Decides by `can()` from GET .../me, never by role: the list needs
 * `complimentary.read`, issuing `complimentary.issue`, cancelling
 * `complimentary.revoke` (the old annul reuses `complimentary.issue`), and the
 * pickers need event, session and category reads.
 */
import { EarlierInvitations } from "@/components/EarlierInvitations";
import { InviteForm } from "@/components/InviteForm";
import { OrgInvitationList } from "@/components/staffInvitations/OrgInvitationList";
import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

export function InvitationsScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const canRead = access.can("complimentary.read");
  const canIssue = access.can("complimentary.issue");
  const canRevoke = access.can("complimentary.revoke");
  const canPick = access.can("session.read") && access.can("tier.read");
  const canFilter = access.can("event.read") && access.can("session.read");

  if (!canRead) {
    return (
      <article aria-labelledby="invitations-title">
        <h1 id="invitations-title">{t("invitations.title")}</h1>
        <p className="notice notice-bad" role="alert" data-testid="invitations-no-access">
          {t("invitations.noAccess")}
        </p>
      </article>
    );
  }
  return (
    <article aria-labelledby="invitations-title">
      <h1 id="invitations-title">{t("invitations.title")}</h1>
      <p className="muted">{t("invitations.intro")}</p>
      {canIssue && canPick && <InviteForm orgId={orgId} />}
      <OrgInvitationList orgId={orgId} canIssue={canIssue} canRevoke={canRevoke} canFilter={canFilter} />
      <EarlierInvitations orgId={orgId} canIssue={canIssue} />
    </article>
  );
}
