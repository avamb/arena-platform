/**
 * The shop settings (spec 47, section 6): currency, delivery fee and name,
 * country, age check, announcement, contact e-mail, the note for the
 * logistician and the sales channel whose payment provider takes the money.
 *
 * The storefront domain belongs to the platform operator: it is shown read only, and becomes editable only
 * for a platform superadmin (the API refuses it for anybody else, so the screen never sends it for an
 * organizer). The open/closed switch is the organization's own (spec 61): the panel above the form
 * (`ShopStorefront`) uses `PUT .../shop/storefront`, never the settings form, so the form can never open
 * the shop by accident and a switch never rewrites the other settings.
 *
 * The form is a pure component (`ShopSettingsForm`); the screen owns the
 * queries and the save. PUT replaces the whole row, so the form always sends
 * every field it shows.
 *
 * SA-12: the version (`updated_at`) the form was built from goes back with the save. When a colleague saved the
 * settings meanwhile the API answers 409 `shop.settings_changed`, nothing is overwritten, and the person is offered
 * to reload. While anything is unsaved, leaving the page asks first (`UnsavedChangesGuard`, as on the product card).
 * SA-23: with no sales channel chosen the form warns where the shop's money goes.
 */
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { SelectField, TextAreaField, TextField } from "@/components/FormField";
import { NoShopAccess, ShopNav, ShopReadOnlyNote } from "@/components/ShopNav";
import { ShopStorefront } from "@/components/ShopStorefront";
import { ReferralSection } from "@/components/ReferralSection";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import { ErrorBox, Loading } from "@/components/ui";
import { fetchShopSettings, listSalesChannels, listShopProducts, saveShopSettings, setShopStorefront } from "@/lib/api/shopApi";
import { listCountries } from "@/lib/api/endpoints";
import type { ShopSettingsUpdateRequest } from "@/lib/api/types";
import { currencyLabel, regionName } from "@/lib/format";
import { canManageReferral, canViewReferral } from "@/lib/referral";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  SHOP_LIMITS,
  browserZone,
  channelWarning,
  checkSettings,
  currencyState,
  isSettingsChanged,
  settingsDirty,
  settingsDraft,
  settingsRequest,
  type SettingsDraft,
  type SettingsProblem,
  type ShopSettingsView,
} from "@/lib/shop";
import { blockersOf, blockersOfError, withBlockers } from "@/lib/shopStorefront";
import { useOrg } from "@/screens/OrgLayout";

const PROBLEM_KEY: Record<SettingsProblem, MessageKey> = {
  currency: "shop.settings.err.currency",
  currencyUnknown: "shop.settings.err.currencyUnknown",
  shippingFee: "shop.settings.err.shippingFee",
  shippingLabel: "shop.settings.err.shippingLabel",
  country: "shop.settings.err.country",
  ageGate: "shop.settings.err.ageGate",
  announcement: "shop.settings.err.announcement",
  contactEmail: "shop.settings.err.contactEmail",
  logisticsNote: "shop.settings.err.logisticsNote",
  maxLineQty: "shop.settings.err.maxLineQty",
};

export interface ShopSettingsFormProps {
  draft: SettingsDraft;
  onDraft: (d: SettingsDraft) => void;
  /** The problems to show (after the person tried to save), else none. */
  problems: Partial<Record<SettingsProblem, true>>;
  countries: readonly { iso2: string; name: string }[];
  /** The channels of the organization; null when the person may not list them (`channel.read`). */
  channels: readonly { id: string; name: string }[] | null;
  /** The list of channels is still on its way: the channel warning waits for it. */
  channelsLoading?: boolean;
  settings: ShopSettingsView;
  /** The platform operator: may change the domain. */
  operator: boolean;
  operatorDomain: string;
  onOperatorDomain: (v: string) => void;
  /**
   * How many products the shop has: with at least one the currency is shown, not typed (every price
   * is stored in it). null while it is not known; undefined means none.
   */
  productCount?: number | null;
  /**
   * SB-13: a helper list that did not load says so, with a way to try again, instead of leaving the form on
   * "checking" / "no country" / "no rights" for good. The rest of the form stays usable.
   */
  productsError?: unknown;
  onRetryProducts?: () => void;
  countriesError?: unknown;
  onRetryCountries?: () => void;
  channelsError?: unknown;
  onRetryChannels?: () => void;
  /** `shop.manage`: without it the form is read only. */
  canManage: boolean;
  busy: boolean;
  onSubmit: () => void;
}

/** The form of the settings. Pure: it shows the draft it is given. */
export function ShopSettingsForm(p: ShopSettingsFormProps) {
  const { t, locale } = useI18n();
  const { draft, problems, settings } = p;
  const set = (change: Partial<SettingsDraft>) => p.onDraft({ ...draft, ...change });
  const err = (k: SettingsProblem): string | undefined =>
    problems[k] === true
      ? t(PROBLEM_KEY[k], { max: k === "shippingLabel" ? SHOP_LIMITS.shippingLabel : k === "announcement" ? SHOP_LIMITS.announcement : SHOP_LIMITS.logisticsNote })
      : undefined;
  const currency = draft.currency.trim().toUpperCase() || settings.currency;
  const currencyMode = currencyState(p.productCount);
  // Country names in the language of the screen, not the list the API answers in English.
  const countries = p.countries
    .map((c) => ({ iso2: c.iso2, name: regionName(c.iso2, locale) || c.name }))
    .sort((a, b) => a.name.localeCompare(b.name, locale));
  // The stored country stays selectable even when the list did not load, so the field never claims "no country".
  if (draft.country !== "" && !countries.some((c) => c.iso2 === draft.country)) {
    countries.unshift({ iso2: draft.country, name: regionName(draft.country, locale) || draft.country });
  }

  const submit = (e: FormEvent) => {
    e.preventDefault();
    p.onSubmit();
  };

  return (
    <form onSubmit={submit} noValidate data-testid="shop-settings-form">
      <fieldset disabled={!p.canManage || p.busy}>
        {currencyMode === "editable" ? (
          <TextField
            label={t("shop.settings.f.currency")}
            hint={t("shop.settings.f.currencyHint")}
            error={err("currency") ?? err("currencyUnknown")}
            value={draft.currency}
            maxLength={3}
            onChange={(v) => set({ currency: v.toUpperCase() })}
            testId="shop-settings-currency"
          />
        ) : (
          <>
            <p>
              <strong>{t("shop.settings.f.currency")}</strong>
            </p>
            <p data-testid="shop-settings-currency-value">
              {settings.currency} · {currencyLabel(settings.currency, locale)}
            </p>
            {currencyMode === "checking" && p.productsError !== undefined && p.productsError !== null ? (
              <div data-testid="shop-settings-products-error">
                <ErrorBox error={p.productsError} onRetry={p.onRetryProducts} />
                <p className="field-hint">{t("shop.settings.f.currencyCheckFailed")}</p>
              </div>
            ) : (
              <p className="field-hint" data-testid="shop-settings-currency-locked">
                {currencyMode === "locked" ? t("shop.settings.f.currencyLocked") : t("shop.settings.f.currencyChecking")}
              </p>
            )}
          </>
        )}
        <TextField
          label={t("shop.settings.f.shippingFee", { currency })}
          hint={t("shop.settings.f.shippingFeeHint")}
          error={err("shippingFee")}
          value={draft.shippingFee}
          onChange={(v) => set({ shippingFee: v })}
          testId="shop-settings-fee"
        />
        <TextField
          label={t("shop.settings.f.shippingLabel")}
          hint={t("shop.settings.f.shippingLabelHint")}
          error={err("shippingLabel")}
          value={draft.shippingLabel}
          maxLength={SHOP_LIMITS.shippingLabel}
          onChange={(v) => set({ shippingLabel: v })}
          testId="shop-settings-label"
        />
        {p.countriesError !== undefined && p.countriesError !== null && (
          <div data-testid="shop-settings-countries-error">
            <ErrorBox error={p.countriesError} onRetry={p.onRetryCountries} />
          </div>
        )}
        <SelectField label={t("shop.settings.f.country")} error={err("country")} value={draft.country} onChange={(v) => set({ country: v })} testId="shop-settings-country">
          <option value="">{t("shop.settings.f.countryNone")}</option>
          {countries.map((c) => (
            <option key={c.iso2} value={c.iso2}>
              {c.name}
            </option>
          ))}
        </SelectField>
        <TextField
          label={t("shop.settings.f.ageGate")}
          hint={t("shop.settings.f.ageGateHint")}
          error={err("ageGate")}
          value={draft.ageGate}
          inputMode="numeric"
          onChange={(v) => set({ ageGate: v })}
          testId="shop-settings-age"
        />
        <TextField
          label={t("shop.settings.f.maxLineQty")}
          hint={t("shop.settings.f.maxLineQtyHint")}
          error={err("maxLineQty")}
          value={draft.maxLineQty}
          inputMode="numeric"
          onChange={(v) => set({ maxLineQty: v })}
          testId="shop-settings-maxqty"
        />
        <TextField
          label={t("shop.settings.f.announcement")}
          hint={t("shop.settings.f.announcementHint", { max: SHOP_LIMITS.announcement })}
          error={err("announcement")}
          value={draft.announcement}
          onChange={(v) => set({ announcement: v })}
          testId="shop-settings-announcement"
        />
        <TextField
          label={t("shop.settings.f.contactEmail")}
          hint={t("shop.settings.f.contactEmailHint")}
          error={err("contactEmail")}
          type="email"
          value={draft.contactEmail}
          onChange={(v) => set({ contactEmail: v })}
          testId="shop-settings-email"
        />
        <TextAreaField
          label={t("shop.settings.f.logisticsNote")}
          hint={t("shop.settings.f.logisticsNoteHint", { max: SHOP_LIMITS.logisticsNote })}
          error={err("logisticsNote")}
          value={draft.logisticsNote}
          rows={3}
          onChange={(v) => set({ logisticsNote: v })}
          testId="shop-settings-note"
        />
        {p.channels !== null ? (
          <SelectField
            label={t("shop.settings.f.channel")}
            hint={t("shop.settings.f.channelHint")}
            value={draft.salesChannelId}
            onChange={(v) => set({ salesChannelId: v })}
            testId="shop-settings-channel"
          >
            <option value="">{t("shop.settings.f.channelDefault")}</option>
            {p.channels.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </SelectField>
        ) : p.channelsError !== undefined && p.channelsError !== null ? (
          <>
            <p>
              <strong>{t("shop.settings.f.channel")}</strong>
            </p>
            <div data-testid="shop-settings-channels-error">
              <ErrorBox error={p.channelsError} onRetry={p.onRetryChannels} />
            </div>
          </>
        ) : (
          <>
            <p>
              <strong>{t("shop.settings.f.channel")}</strong>
            </p>
            <p className="muted" data-testid="shop-settings-channel-locked">
              {t("shop.settings.f.channelLocked")}
            </p>
          </>
        )}
        {p.channelsLoading !== true && (p.channelsError === undefined || p.channelsError === null) && channelWarning(draft, p.channels === null ? null : p.channels.length) !== "none" && (
          <p className="notice notice-warn" role="status" data-testid="shop-settings-channel-warning">
            {channelWarning(draft, p.channels === null ? null : p.channels.length) === "noChannels"
              ? t("shop.settings.channelNone")
              : p.channels === null
                ? t("shop.settings.channelWarnRights")
                : t("shop.settings.channelWarn")}
          </p>
        )}
      </fieldset>

      <fieldset disabled={!p.operator || p.busy} data-testid="shop-settings-operator">
        <legend>{t("shop.settings.f.domain")}</legend>
        {p.operator ? (
          <TextField label={t("shop.settings.f.domain")} value={p.operatorDomain} onChange={p.onOperatorDomain} testId="shop-settings-domain" />
        ) : (
          <>
            <p data-testid="shop-settings-domain-value">{settings.shop_domain !== null && settings.shop_domain !== undefined && settings.shop_domain !== "" ? settings.shop_domain : t("shop.settings.f.domainNone")}</p>
            <p className="field-hint">{t("shop.settings.operatorOnly")}</p>
          </>
        )}
      </fieldset>

      {p.canManage && (
        <p>
          <button type="submit" className="btn btn-primary" disabled={p.busy} data-testid="shop-settings-save">
            {p.busy ? t("shop.settings.saving") : t("shop.settings.save")}
          </button>
        </p>
      )}
    </form>
  );
}

/** The form with its queries and its save. Exported for the tests of the version check and the leave question. */
export function SettingsPage({ settings }: { settings: ShopSettingsView }) {
  const { t, locale } = useI18n();
  const { orgId, access } = useOrg();
  const queryClient = useQueryClient();
  const canManage = access.can("shop.manage");
  const operator = access.data?.platform_superadmin === true;
  const [draft, setDraft] = useState<SettingsDraft>(() => settingsDraft(settings));
  const [domain, setDomain] = useState(settings.shop_domain ?? "");
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [reloaded, setReloaded] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  // The storefront switch has its own call and its own failure (spec 61). `switched` is the last answer of the
  // switch; a newer read of the settings (a refetch, a save) takes over again.
  const [switched, setSwitched] = useState<ShopSettingsView | null>(null);
  const [switching, setSwitching] = useState(false);
  const [switchFailure, setSwitchFailure] = useState<unknown>(null);
  // The settings the form was built from. The query may refresh behind the form (a colleague saved): the form
  // keeps its own baseline, so the version it sends back is the one the person actually saw.
  const [baseline, setBaseline] = useState<ShopSettingsView>(settings);
  const allowLeave = useRef(false);
  const saving = useRef(false);

  useEffect(() => setSwitched(null), [settings]);
  const storefront = switched ?? settings;

  const countries = useQuery({ queryKey: ["geo", "countries", locale], queryFn: () => listCountries(locale), staleTime: Number.POSITIVE_INFINITY, retry: false });
  const canChannels = access.can("channel.read");
  const channels = useQuery({ queryKey: ["salesChannels", orgId], queryFn: () => listSalesChannels(orgId), enabled: canChannels, retry: false });

  const products = useQuery({ queryKey: ["shopProducts", orgId, ""], queryFn: () => listShopProducts(orgId, undefined), retry: false });
  // Unknown (still loading, or the list failed): the currency stays closed.
  const productCount = products.data !== undefined ? products.data.products.length : null;
  const currencyLocked = currencyState(productCount) !== "editable";

  const problems = tried ? checkSettings(draft, { currencyLocked }) : {};
  const dirty = canManage && (settingsDirty(draft, baseline) || (operator && domain !== (baseline.shop_domain ?? "")));

  /** Saves the form; true when the settings were stored (the question on leaving the page uses it). */
  const submit = async (): Promise<boolean> => {
    // The leave question and the button may both call this: a second PUT with the same version would answer 409.
    if (saving.current) return false;
    setTried(true);
    setSaved(false);
    setReloaded(false);
    if (Object.keys(checkSettings(draft, { currencyLocked })).length > 0) return false;
    const body: ShopSettingsUpdateRequest = settingsRequest(draft, baseline.updated_at);
    if (operator) {
      if (domain !== (baseline.shop_domain ?? "")) body.shop_domain = domain.trim();
    }
    saving.current = true;
    setBusy(true);
    setFailure(null);
    try {
      const stored = await saveShopSettings(orgId, body);
      queryClient.setQueryData(["shopSettings", orgId], stored);
      setBaseline(stored);
      setDraft(settingsDraft(stored));
      setDomain(stored.shop_domain ?? "");
      setSaved(true);
      return true;
    } catch (error) {
      setFailure(error);
      return false;
    } finally {
      saving.current = false;
      setBusy(false);
    }
  };

  /** Opens or closes the storefront. The form is untouched, only the version it will send back moves on. */
  const toggleStorefront = async (next: boolean) => {
    setSwitching(true);
    setSwitchFailure(null);
    try {
      const stored = await setShopStorefront(orgId, next);
      queryClient.setQueryData(["shopSettings", orgId], stored);
      setSwitched(stored);
      // The switch may move `updated_at`: the next save of the form must send the version that now stands.
      setBaseline((b) => ({ ...b, updated_at: stored.updated_at, enabled: stored.enabled }));
    } catch (error) {
      setSwitchFailure(error);
      // A refused opening names what is missing right now: show exactly that.
      const blockers = blockersOfError(error);
      if (blockers !== null) setSwitched(withBlockers(storefront, blockers));
    } finally {
      setSwitching(false);
    }
  };

  /** After 409 `shop.settings_changed`: read the settings again and drop the typed changes. */
  const reload = async () => {
    setBusy(true);
    setFailure(null);
    try {
      const fresh = await fetchShopSettings(orgId);
      queryClient.setQueryData(["shopSettings", orgId], fresh);
      setBaseline(fresh);
      setDraft(settingsDraft(fresh));
      setDomain(fresh.shop_domain ?? "");
      setTried(false);
      setReloaded(true);
    } catch (error) {
      setFailure(error);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <UnsavedChangesGuard dirty={dirty} allowLeave={allowLeave} onSaveAndLeave={canManage ? submit : undefined} />
      {!settings.configured && (
        <p className="notice notice-warn" role="status" data-testid="shop-settings-unconfigured">
          {t("shop.settings.notConfigured")}
        </p>
      )}
      <ShopStorefront
        orgId={orgId}
        enabled={storefront.enabled}
        blockers={blockersOf(storefront)}
        canManage={canManage}
        canOpenOrgData={access.can("org.update")}
        busy={switching || busy}
        error={switchFailure}
        onToggle={(next) => void toggleStorefront(next)}
      />
      <ShopSettingsForm
        draft={draft}
        onDraft={setDraft}
        problems={problems}
        countries={countries.data?.countries ?? []}
        channels={canChannels && channels.data !== undefined ? channels.data.channels : null}
        channelsLoading={canChannels && channels.isPending}
        settings={baseline}
        productCount={productCount}
        productsError={products.isError ? products.error : undefined}
        onRetryProducts={() => void products.refetch()}
        countriesError={countries.isError ? countries.error : undefined}
        onRetryCountries={() => void countries.refetch()}
        channelsError={canChannels && channels.isError ? channels.error : undefined}
        onRetryChannels={() => void channels.refetch()}
        operator={operator}
        operatorDomain={domain}
        onOperatorDomain={setDomain}
        canManage={canManage}
        busy={busy}
        onSubmit={() => void submit()}
      />
      {failure !== null && <ErrorNotice error={failure} testId="shop-settings-error" />}
      {isSettingsChanged(failure) && (
        <p>
          <button type="button" className="btn" disabled={busy} onClick={() => void reload()} data-testid="shop-settings-reload">
            {t("shop.settings.reload")}
          </button>
        </p>
      )}
      {reloaded && (
        <p className="notice notice-ok" role="status" data-testid="shop-settings-reloaded">
          {t("shop.settings.reloaded")}
        </p>
      )}
      {saved && (
        <p className="notice notice-ok" role="status" data-testid="shop-settings-saved">
          {t("shop.settings.saved")}
        </p>
      )}
    </>
  );
}

export function ShopSettingsScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const canRead = access.can("shop.read");
  const settings = useQuery({ queryKey: ["shopSettings", orgId], queryFn: () => fetchShopSettings(orgId), enabled: canRead });
  if (!canRead) return <NoShopAccess />;
  return (
    <section className="shop-page" aria-labelledby="shop-settings-title">
      <h1 id="shop-settings-title">{t("shop.settings.title")}</h1>
      <ShopNav orgId={orgId} />
      {!access.can("shop.manage") && <ShopReadOnlyNote />}
      {settings.isLoading && <Loading />}
      {settings.error !== null && <ErrorBox error={settings.error} onRetry={() => void settings.refetch()} />}
      {settings.data !== undefined && <SettingsPage key="settings" settings={settings.data} />}
      {/* Spec 52: "Refer a friend" for the shop. The route's own gate for looking is promo.read. */}
      {canViewReferral(access.can) && (
        <ReferralSection key={`referral:${orgId}:shop`} orgId={orgId} target={{ kind: "shop" }} canManage={canManageReferral(access.can)} timezone={browserZone()} />
      )}
    </section>
  );
}
