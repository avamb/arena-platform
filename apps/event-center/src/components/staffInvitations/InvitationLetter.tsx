/**
 * The state of the guest's letter on an invitation row (spec 46, section 3.4):
 * "Letter sent 14:05", "Sending the letter...", "Letter failed" with a hint to
 * send it again. Nothing for an invitation without a letter (pdf_only).
 */
import { Badge } from "@/components/ui";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { letterView } from "@/lib/staffInvitations";
import type { StaffInvitationListItem } from "@/lib/api/types";

function localZone(): string | undefined {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    return undefined;
  }
}

export function InvitationLetter({ item, testId }: { item: StaffInvitationListItem; testId: string }) {
  const { t, locale } = useI18n();
  const view = letterView(item, localZone(), locale);
  if (view.kind === "none") return null;
  return (
    <span className="card-meta" data-testid={testId} data-letter={view.kind}>
      <Badge tone={view.tone}>{t(`seatInv.letter.${view.kind}` as MessageKey, { when: view.when })}</Badge>
      {view.hint !== null && (
        <span className="muted" data-testid={`${testId}-hint`}>
          {" "}
          {t(view.hint)}
        </span>
      )}
    </span>
  );
}
