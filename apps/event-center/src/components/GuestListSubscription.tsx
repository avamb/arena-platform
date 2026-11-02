/**
 * "Send me the guest list before every session" (owner decision 2026-11-02,
 * migration 0188). The caller's OWN setting in this organization: when it is
 * on, the guest list of every session comes 24 hours before the start, 3 hours
 * before it and at doors open, to the caller's private chat with the event
 * center bot, or by e-mail without the bot. Never to a group: the file holds
 * buyers' contacts. The bot's twin is the toggle on its Notifications screen
 * (`guest_list.go`, `gl:on` / `gl:off`).
 *
 * The box is shown to people who may read sales (`order.read`); the server
 * sends files only to owners and managers.
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Loading } from "@/components/ui";
import { getGuestListSubscription, setGuestListSubscription } from "@/lib/api/endpoints";
import type { GuestListSubscription as Subscription } from "@/lib/api/types";
import { useI18n, type MessageKey } from "@/lib/i18n";

/** The line that says where the list goes. */
export function guestListChannelKey(channel: string): MessageKey {
  if (channel === "telegram") return "guestList.viaTelegram";
  if (channel === "email") return "guestList.viaEmail";
  return "guestList.viaNone";
}

export function GuestListSubscription({ orgId }: { orgId: string }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const key = ["guestListSubscription", orgId] as const;
  const sub = useQuery({ queryKey: key, queryFn: () => getGuestListSubscription(orgId) });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);

  // The box follows the click at once and goes back if the save fails.
  const change = async (enabled: boolean) => {
    const before = queryClient.getQueryData<Subscription>(key);
    if (before !== undefined) queryClient.setQueryData(key, { ...before, enabled });
    setSaving(true);
    setError(null);
    try {
      const next: Subscription = await setGuestListSubscription(orgId, enabled);
      queryClient.setQueryData(key, next);
    } catch (e) {
      if (before !== undefined) queryClient.setQueryData(key, before);
      setError(e);
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="card" aria-labelledby="guest-list-title" data-testid="guest-list-subscription">
      <h2 id="guest-list-title">{t("guestList.title")}</h2>
      <p className="muted">{t("guestList.explain")}</p>
      {sub.isPending && <Loading />}
      {sub.error !== null && <ErrorNotice error={sub.error} testId="guest-list-load-error" />}
      {sub.data !== undefined && (
        <>
          <label className="check">
            <input
              type="checkbox"
              checked={sub.data.enabled}
              disabled={saving}
              onChange={(e) => void change(e.target.checked)}
              data-testid="guest-list-toggle"
            />
            <span>{t("guestList.toggle")}</span>
          </label>
          {saving && <p className="muted">{t("guestList.saving")}</p>}
          {sub.data.enabled && (
            <p className="muted" data-testid="guest-list-channel">
              {t(guestListChannelKey(sub.data.channel))}
            </p>
          )}
        </>
      )}
      {error !== null && <ErrorNotice error={error} testId="guest-list-error" />}
    </section>
  );
}
