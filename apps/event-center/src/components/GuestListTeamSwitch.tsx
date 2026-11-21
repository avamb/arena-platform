/**
 * "Guest list for the team" on the owner's organization data screen (owner
 * request 2026-11-08, migration 0211). One switch for the whole organization,
 * on by default: while it is on, the current guest list of every session goes
 * to the owners and managers by e-mail and to the organization's sales
 * Telegram chats 24 hours and 3 hours before the start.
 *
 * It is the owner's switch (`org.update`); the screen that holds it is
 * already owner-only. A failed save shows why and keeps the old value.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Loading } from "@/components/ui";
import { getGuestListSettings, setGuestListSettings } from "@/lib/api/endpoints";
import type { GuestListSettings } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";

export function GuestListTeamSwitch({ orgId }: { orgId: string }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const key = ["guestListSettings", orgId] as const;
  const settings = useQuery({ queryKey: key, queryFn: () => getGuestListSettings(orgId) });
  const toggle = useMutation({
    mutationFn: (next: boolean) => setGuestListSettings(orgId, next),
    onSuccess: (s: GuestListSettings) => queryClient.setQueryData(key, s),
  });
  const on = settings.data?.auto_send === true;
  return (
    <section aria-labelledby="guest-list-team-title" data-testid="guest-list-team" data-on={on ? "true" : "false"}>
      <h2 id="guest-list-team-title">{t("guestList.team.title")}</h2>
      {settings.isPending && <Loading />}
      {settings.error !== null && <ErrorNotice error={settings.error} testId="guest-list-team-load-error" />}
      {settings.data !== undefined && (
        <>
          <p data-testid="guest-list-team-state">{on ? t("guestList.team.on") : t("guestList.team.off")}</p>
          {toggle.error !== null && <ErrorNotice error={toggle.error} testId="guest-list-team-error" />}
          <p>
            <button
              type="button"
              className="btn"
              disabled={toggle.isPending}
              onClick={() => toggle.mutate(!on)}
              data-testid="guest-list-team-toggle"
            >
              {on ? t("guestList.team.btnOff") : t("guestList.team.btnOn")}
            </button>
          </p>
        </>
      )}
    </section>
  );
}
