/**
 * 4-4: Invitations (free tickets for named guests): issue, list with the
 * state of each letter, annul. The twin of the bot's «Пригласительные»
 * (eventbot/invite*.go). Decides by `can()` from GET .../me, never by role:
 * the list needs `complimentary.read`, issuing and annulling need
 * `complimentary.issue`, and the pickers need event, session and category
 * reads.
 */
import { InvitationList } from "@/components/InvitationList";
import { InviteForm } from "@/components/InviteForm";
import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

export function InvitationsScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const canRead = access.can("complimentary.read");
  const canIssue = access.can("complimentary.issue");
  const canPick = access.can("session.read") && access.can("tier.read");

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
      <InvitationList orgId={orgId} canIssue={canIssue} />
    </article>
  );
}
