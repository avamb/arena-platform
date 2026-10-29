/**
 * "Earlier invitations": the invitations issued through the old route before
 * the screen moved to the staff-invitation routes (INV-04b). They live in
 * `complimentary_issuances`, so the new list does not show them, and they keep
 * their own list and their own annul. The section appears only when the
 * organization has at least one, and never offers a new issue.
 */
import { useQuery } from "@tanstack/react-query";
import { InvitationList } from "@/components/InvitationList";
import { listInvitations } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";

export function EarlierInvitations({ orgId, canIssue }: { orgId: string; canIssue: boolean }) {
  const { t } = useI18n();
  // One row is enough to know whether the section is needed. The key sits under ["invitations", orgId]
  // like the list itself, so annulling refreshes both.
  const probe = useQuery({
    queryKey: ["invitations", orgId, "probe"],
    queryFn: () => listInvitations(orgId, { limit: 1, offset: 0 }),
  });
  if (probe.data === undefined || probe.data.issuances.length === 0) return null;
  return (
    <div data-testid="earlier-invitations">
      <InvitationList orgId={orgId} canIssue={canIssue} title={t("orgInv.earlier.title")} intro={t("orgInv.earlier.intro")} />
    </div>
  );
}
