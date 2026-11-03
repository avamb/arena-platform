/**
 * "Mailings and reminders" (spec 51-1): the organizer switches the one
 * unpaid-order reminder and the buyer's mailing tick, picks the promo code the
 * tick earns, and sees who subscribed (with a CSV for their mailing tool).
 *
 * Who sees what follows the API: the settings need `promo.read` to read and
 * `org.update` to save, the subscribers need `order.read`.
 */
import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  downloadMarketingSubscribersCsv,
  fetchGrowthSettings,
  listMarketingSubscribers,
  listPromoCodes,
  saveGrowthSettings,
} from "@/lib/api/endpoints";
import type { MarketingSubscriber } from "@/lib/api/types";
import {
  SUBSCRIBER_FILTERS,
  formChanged,
  formFromSettings,
  rewardBroken,
  rewardChoices,
  settingsBody,
  type GrowthForm,
  type SubscriberFilter,
} from "@/lib/growth";
import { useI18n } from "@/lib/i18n";
import { DownloadButton, Empty, ErrorBox, Loading } from "@/components/ui";
import { useOrg } from "@/screens/OrgLayout";

function SettingsCard() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const canSave = access.can("org.update");
  const qc = useQueryClient();
  const settings = useQuery({ queryKey: ["growthSettings", orgId], queryFn: () => fetchGrowthSettings(orgId) });
  const codes = useQuery({ queryKey: ["promoCodes", orgId], queryFn: () => listPromoCodes(orgId) });
  const [form, setForm] = useState<GrowthForm | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (settings.data !== undefined) setForm(formFromSettings(settings.data));
  }, [settings.data]);

  const save = useMutation({
    mutationFn: (f: GrowthForm) => saveGrowthSettings(orgId, settingsBody(f)),
    onSuccess: (data) => {
      qc.setQueryData(["growthSettings", orgId], data);
      setSaved(true);
    },
  });

  if (settings.isLoading || form === null) {
    return settings.error !== null ? <ErrorBox error={settings.error} onRetry={() => void settings.refetch()} /> : <Loading />;
  }
  const stored = formFromSettings(settings.data!);
  const allCodes = codes.data?.promo_codes ?? [];
  const choices = rewardChoices(allCodes, stored.rewardId);
  const broken = form.optIn && rewardBroken(allCodes, form.rewardId);
  const update = (patch: Partial<GrowthForm>) => {
    setSaved(false);
    setForm({ ...form, ...patch });
  };

  return (
    <section className="card" aria-labelledby="growth-settings-title" data-testid="growth-settings">
      <h2 id="growth-settings-title">{t("growth.settings.title")}</h2>
      <fieldset disabled={!canSave || save.isPending}>
        <label className="check">
          <input
            type="checkbox"
            checked={form.cartReminder}
            onChange={(e) => update({ cartReminder: e.target.checked })}
            data-testid="growth-reminder"
          />
          <span>{t("growth.reminder.label")}</span>
        </label>
        <p className="muted">{t("growth.reminder.help")}</p>

        <label className="check">
          <input
            type="checkbox"
            checked={form.optIn}
            onChange={(e) => update({ optIn: e.target.checked })}
            data-testid="growth-optin"
          />
          <span>{t("growth.optin.label")}</span>
        </label>
        <p className="muted">{t("growth.optin.help")}</p>

        {form.optIn && (
          <div className="field">
            <label htmlFor="growth-reward">{t("growth.reward.label")}</label>
            <select
              id="growth-reward"
              value={form.rewardId ?? ""}
              onChange={(e) => update({ rewardId: e.target.value === "" ? null : e.target.value })}
              data-testid="growth-reward"
            >
              <option value="">{t("growth.reward.none")}</option>
              {choices.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.code}
                </option>
              ))}
            </select>
            <p className="muted">{t("growth.reward.help")}</p>
            {choices.length === 0 && <p className="muted">{t("growth.reward.noCodes")}</p>}
            {broken && (
              <p className="bad-text" role="alert" data-testid="growth-reward-broken">
                {t("growth.reward.broken")}
              </p>
            )}
          </div>
        )}
      </fieldset>
      {!canSave && <p className="muted">{t("growth.readOnly")}</p>}
      {canSave && (
        <div className="actions">
          <button
            type="button"
            className="btn btn-primary"
            disabled={!formChanged(form, stored) || save.isPending}
            onClick={() => save.mutate(form)}
            data-testid="growth-save"
          >
            {t("growth.save")}
          </button>
          {saved && !save.isPending && (
            <span className="muted" role="status" data-testid="growth-saved">
              {t("growth.saved")}
            </span>
          )}
        </div>
      )}
      {save.error !== null && <ErrorBox error={save.error} />}
    </section>
  );
}

function SubscriberRow({ s }: { s: MarketingSubscriber }) {
  const { t, locale } = useI18n();
  const when = s.confirmed_at ?? s.granted_at;
  return (
    <li className="card" data-testid="subscriber-row">
      <span className="card-title">{s.email}</span>
      <span className="card-meta">
        <span>{t(`growth.status.${s.status}` as "growth.status.confirmed")}</span>
        {s.name !== "" && <span>{s.name}</span>}
        <span className="muted">{new Date(when).toLocaleDateString(locale)}</span>
      </span>
    </li>
  );
}

function SubscribersCard() {
  const { t, locale } = useI18n();
  const { orgId } = useOrg();
  const [filter, setFilter] = useState<SubscriberFilter>("confirmed");
  const list = useQuery({
    queryKey: ["marketingSubscribers", orgId, filter],
    queryFn: () => listMarketingSubscribers(orgId, filter),
  });
  const items = list.data?.items ?? [];
  return (
    <section className="card" aria-labelledby="growth-subs-title" data-testid="growth-subscribers">
      <h2 id="growth-subs-title">{t("growth.subscribers.title")}</h2>
      <p className="muted">{t("growth.subscribers.help")}</p>
      <div className="actions">
        <label htmlFor="growth-filter">{t("growth.subscribers.filter")}</label>
        <select
          id="growth-filter"
          value={filter}
          onChange={(e) => setFilter(e.target.value as SubscriberFilter)}
          data-testid="growth-filter"
        >
          {SUBSCRIBER_FILTERS.map((f) => (
            <option key={f} value={f}>
              {t(`growth.filter.${f}` as "growth.filter.all")}
            </option>
          ))}
        </select>
        <DownloadButton
          label="growth.subscribers.csv"
          fetchFile={() => downloadMarketingSubscribersCsv(orgId, filter, locale)}
          testId="growth-csv"
        />
      </div>
      {list.isLoading && <Loading />}
      {list.error !== null && <ErrorBox error={list.error} onRetry={() => void list.refetch()} />}
      {list.data !== undefined && items.length === 0 && <Empty>{t("growth.subscribers.empty")}</Empty>}
      {list.data !== undefined && items.length > 0 && (
        <p className="muted" role="status" data-testid="growth-count">
          {t("growth.subscribers.count", { count: list.data.total })}
        </p>
      )}
      <ul className="cards">
        {items.map((s) => (
          <SubscriberRow key={s.id} s={s} />
        ))}
      </ul>
    </section>
  );
}

export function GrowthScreen() {
  const { t } = useI18n();
  const { access } = useOrg();
  const canSettings = access.can("promo.read");
  const canSubscribers = access.can("order.read");
  return (
    <section aria-labelledby="growth-title">
      <h1 id="growth-title">{t("growth.title")}</h1>
      {!canSettings && !canSubscribers && <p className="muted">{t("growth.noAccess")}</p>}
      {canSettings && <SettingsCard />}
      {canSubscribers && <SubscribersCard />}
    </section>
  );
}
