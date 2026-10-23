/**
 * 4-7: "Organization data", the owner's screen (the web twin of the bot's
 * org_data.go). The owner fills the legal data, sees what the sales page
 * still lacks to show the seller block, sets each page's public contact and
 * the Telegram leg of the contact form, and is told when payments are not
 * connected yet.
 *
 * Access follows `useOrgAccess(orgId).can("org.update")`: a manager or an
 * agent who opens the address sees why it is not for them, and no call is
 * sent. The platform operator's "hide the seller block" switch is only
 * displayed here.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { OrgDataForm } from "@/components/OrgDataForm";
import { SellerChannelCard } from "@/components/SellerChannelCard";
import { SellerErrorNotice } from "@/components/SellerErrorNotice";
import { ErrorBox, Loading } from "@/components/ui";
import { fetchOrganization, fetchSellerProfile, listPaymentConfigs, patchSellerProfile } from "@/lib/api/endpoints";
import type { SellerProfile } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { missingKey, paymentsNotConnected } from "@/lib/orgData";
import { useOrg } from "@/screens/OrgLayout";

export function OrgDataScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  if (!access.can("org.update")) {
    return (
      <section aria-labelledby="org-data-title" data-testid="org-data-owner-only">
        <h1 id="org-data-title">{t("orgData.title")}</h1>
        <p className="notice" role="status">
          <strong>{t("orgData.ownerOnly.title")}</strong> {t("orgData.ownerOnly.text")}
        </p>
        <p>
          <Link to="/o/$orgId/events" params={{ orgId }}>
            {t("common.backToList")}
          </Link>
        </p>
      </section>
    );
  }
  return <OrgDataBody orgId={orgId} canReadPayments={access.can("payment_config.read")} />;
}

function OrgDataBody({ orgId, canReadPayments }: { orgId: string; canReadPayments: boolean }) {
  const { t } = useI18n();
  const org = useQuery({ queryKey: ["orgDetail", orgId], queryFn: () => fetchOrganization(orgId) });
  const profile = useQuery({ queryKey: ["sellerProfile", orgId], queryFn: () => fetchSellerProfile(orgId) });
  // A failed read says nothing, like the bot: the note is a courtesy.
  const payments = useQuery({
    queryKey: ["paymentConfigs", orgId],
    queryFn: () => listPaymentConfigs(orgId),
    enabled: canReadPayments,
    retry: false,
  });

  const failure = org.error ?? profile.error;
  return (
    <section aria-labelledby="org-data-title" className="org-data" data-testid="org-data">
      <h1 id="org-data-title">{t("orgData.title")}</h1>
      <p>{t("orgData.intro")}</p>
      {canReadPayments && paymentsNotConnected(payments.data?.payment_configs) && (
        <p className="notice" role="status" data-testid="org-data-payments">
          {t("orgData.payments")}
        </p>
      )}
      {failure !== null && failure !== undefined ? (
        <ErrorBox error={failure} onRetry={() => void Promise.all([org.refetch(), profile.refetch()])} />
      ) : org.data === undefined || profile.data === undefined ? (
        <Loading />
      ) : (
        <>
          <SellerStatus profile={profile.data} />
          <OrgDataForm orgId={orgId} org={org.data.organization} profile={profile.data} />
          <SalesPages orgId={orgId} profile={profile.data} />
          <ContactFormSwitch orgId={orgId} profile={profile.data} />
        </>
      )}
    </section>
  );
}

type StatusState = "complete" | "legal" | "incomplete";

/** What the seller data lacks, from the server's own `missing` lists. */
export function statusOf(p: SellerProfile): StatusState {
  if (p.missing.length > 0) return "incomplete";
  if (p.channels.some((c) => !c.complete)) return "legal";
  return "complete";
}

export function SellerStatus({ profile }: { profile: SellerProfile }) {
  const { t } = useI18n();
  const state = statusOf(profile);
  const missing = profile.missing.map((n) => missingKey(n)).filter((k): k is NonNullable<ReturnType<typeof missingKey>> => k !== null);
  return (
    <section aria-labelledby="org-data-status-title" data-testid="org-data-status" data-state={state} data-seller-shown={profile.seller_shown ? "true" : "false"}>
      <h2 id="org-data-status-title">{t("orgData.status.title")}</h2>
      <p data-testid="org-data-status-auto">{t("orgData.status.auto")}</p>
      {state === "complete" && <p className="notice notice-ok" role="status">{t("orgData.status.complete")}</p>}
      {state === "legal" && <p className="notice" role="status">{t("orgData.status.legalComplete")}</p>}
      {state === "incomplete" && (
        <div className="notice" role="status">
          <p>{t("orgData.status.incomplete")}</p>
          <ul className="plain-list" data-testid="org-data-missing">
            {missing.map((k) => (
              <li key={k}>{t(k)}</li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}

function SalesPages({ orgId, profile }: { orgId: string; profile: SellerProfile }) {
  const { t } = useI18n();
  return (
    <section aria-labelledby="org-data-pages-title">
      <h2 id="org-data-pages-title">{t("orgData.pages.title")}</h2>
      {profile.channels.length === 0 ? (
        <p className="muted" role="status" data-testid="org-data-no-pages">
          {t("orgData.pages.none")}
        </p>
      ) : (
        <ul className="cards">
          {profile.channels.map((c) => (
            <SellerChannelCard key={c.id} orgId={orgId} channel={c} />
          ))}
        </ul>
      )}
    </section>
  );
}

function ContactFormSwitch({ orgId, profile }: { orgId: string; profile: SellerProfile }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  // An older server that does not answer the switch means the default, on.
  const on = profile.contact_form_to_telegram !== false;
  const toggle = useMutation({
    mutationFn: (next: boolean) => patchSellerProfile(orgId, next),
    onSuccess: (p) => queryClient.setQueryData(["sellerProfile", orgId], p),
  });
  return (
    <section aria-labelledby="org-data-tg-title" data-testid="org-data-tg" data-on={on ? "true" : "false"}>
      <h2 id="org-data-tg-title">{t("orgData.tg.title")}</h2>
      <p data-testid="org-data-tg-state">{on ? t("orgData.tg.on") : t("orgData.tg.off")}</p>
      {toggle.error !== null && <SellerErrorNotice error={toggle.error} testId="org-data-tg-error" />}
      <p>
        <button type="button" className="btn" disabled={toggle.isPending} onClick={() => toggle.mutate(!on)} data-testid="org-data-tg-toggle">
          {on ? t("orgData.tg.btnOff") : t("orgData.tg.btnOn")}
        </button>
      </p>
    </section>
  );
}
