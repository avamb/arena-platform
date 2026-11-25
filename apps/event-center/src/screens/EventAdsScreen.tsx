/**
 * "Advertising" of one event (spec 66, M2): what the Meta ads of the event cost and how many tickets they sold, for a
 * period (the server's last 30 days, or 7, 30 or 90 by the buttons).
 *
 * Two columns for the same steps: "by Meta data" and "by our data" (orders that carry the campaign id in the link).
 * Money is never converted: with two currencies both amounts are shown and MER is "—". The view is a pure component
 * (`EventAdsView`); the body owns the query and the refresh. Visible to everyone who reads reports; only the link to
 * the Meta settings needs the owner's right.
 */
import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { ErrorNotice } from "@/components/ErrorNotice";
import { ErrorBox, Loading } from "@/components/ui";
import {
  fetchEventMetaAds,
  refreshOrgMetaAds,
  type MetaAdsEventReport,
  type MetaAdsRefreshResult,
} from "@/lib/api/adsApi";
import { formatDateTime, formatMoney, formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { currenciesDiffer, formatMer, merToShow, refreshLocked, untaggedPercent } from "@/lib/metaAds";
import { lastDays } from "@/lib/salesFunnel";
import { useOrg } from "@/screens/OrgLayout";

const PRESETS = [7, 30, 90] as const;

export interface EventAdsViewProps {
  orgId: string;
  report: MetaAdsEventReport;
  /** The person may open the Meta settings (the owner). */
  canConnect: boolean;
  /** A refresh request is in flight. */
  refreshing: boolean;
  /** The answer of the last refresh request, or null. */
  refreshed: MetaAdsRefreshResult | null;
  /** Now, in ms: the refresh button is locked until the server's `not_before`. */
  nowMs: number;
  failure: unknown;
  onRefresh: () => void;
  onPreset: (days: number) => void;
}

/** The page without queries: it shows exactly what it is given. */
export function EventAdsView(p: EventAdsViewProps) {
  const { t, locale } = useI18n();
  const { report, orgId } = p;
  const { sync, meta, ours } = report;
  const n = (v: number) => formatNumber(v, locale);
  const money = (minor: number, currency: string | null) => (currency === null || currency === "" ? "—" : formatMoney(minor, currency, locale));
  const differ = currenciesDiffer(meta.currency, ours.currency);
  const mer = merToShow(report);
  const percent = untaggedPercent(report.untagged);
  const connected = sync.status === "ok" || sync.status === "error";
  const locked = p.refreshing || refreshLocked(p.refreshed?.not_before, p.nowMs);

  const syncBlock = (() => {
    switch (sync.status) {
      case "not_connected":
        return (
          <p className="notice" role="status" data-testid="ads-sync" data-status="not_connected">
            {t("ads.sync.notConnected")}{" "}
            {p.canConnect ? (
              <Link to="/o/$orgId/ads/meta" params={{ orgId }} search={{}} data-testid="ads-connect-link">
                {t("ads.sync.connectLink")}
              </Link>
            ) : (
              t("ads.sync.notConnectedAsk")
            )}
          </p>
        );
      case "no_ad_account":
        return (
          <p className="notice" role="status" data-testid="ads-sync" data-status="no_ad_account">
            {t("ads.sync.noAccount")}{" "}
            {p.canConnect ? (
              <Link to="/o/$orgId/ads/meta" params={{ orgId }} search={{}} data-testid="ads-connect-link">
                {t("ads.sync.connectLink")}
              </Link>
            ) : (
              t("ads.sync.noAccountAsk")
            )}
          </p>
        );
      case "error":
        return (
          <p className="notice notice-bad" role="alert" data-testid="ads-sync" data-status="error">
            {t("ads.sync.error")}
            {sync.last_error !== null && sync.last_error !== "" && (
              <>
                {" "}
                <code data-testid="ads-sync-code">{t("ads.sync.errorCode", { code: sync.last_error })}</code>
              </>
            )}
          </p>
        );
      default:
        return (
          <p className="card-meta" role="status" data-testid="ads-sync" data-status="ok">
            {sync.last_success_at === null
              ? t("ads.sync.neverRead")
              : t("ads.sync.updated", {
                  time: formatDateTime(sync.last_success_at, sync.timezone, locale),
                  account: sync.ad_account_name ?? sync.ad_account_id ?? "—",
                })}
          </p>
        );
    }
  })();

  const template = (
    <section aria-labelledby="ads-template-title" data-testid="ads-template">
      <h2 id="ads-template-title">{t("ads.template.title")}</h2>
      <p>{t("ads.template.how")}</p>
      <TemplateCopy value={report.url_parameters_template} />
    </section>
  );

  return (
    <>
      {syncBlock}
      {connected && (
        <p className="actions">
          <button type="button" className="btn" disabled={locked} onClick={p.onRefresh} data-testid="ads-refresh">
            {p.refreshing ? t("ads.refreshing") : t("ads.refresh")}
          </button>{" "}
          {p.refreshed?.queued === true && (
            <span role="status" data-testid="ads-refresh-queued">
              {t("ads.refresh.queued")}
            </span>
          )}
          {p.refreshed?.queued === false && p.refreshed.not_before !== null && (
            <span role="status" data-testid="ads-refresh-throttled">
              {t("ads.refresh.tooSoon", { time: formatDateTime(p.refreshed.not_before, sync.timezone, locale) })}
            </span>
          )}
        </p>
      )}
      {p.failure !== null && p.failure !== undefined && <ErrorNotice error={p.failure} testId="ads-error" />}

      {connected && (
        <>
          <div className="funnel-presets" data-testid="ads-presets">
            {PRESETS.map((d) => (
              <button key={d} type="button" className="btn" onClick={() => p.onPreset(d)} data-testid={`ads-preset-${d}`}>
                {t("ads.lastDays", { days: d })}
              </button>
            ))}
          </div>
          <p className="card-meta" data-testid="ads-period">
            {t("ads.period", { from: report.period.from, to: report.period.to })}
          </p>

          <section aria-labelledby="ads-funnel-title">
            <h2 id="ads-funnel-title">{t("ads.funnel.title")}</h2>
            <table className="table" data-testid="ads-funnel">
              <thead>
                <tr>
                  <th scope="col">{t("ads.col.step")}</th>
                  <th scope="col">{t("ads.col.meta")}</th>
                  <th scope="col">{t("ads.col.ours")}</th>
                </tr>
              </thead>
              <tbody>
                <tr data-testid="ads-row-spend">
                  <th scope="row">{t("ads.step.spend")}</th>
                  <td data-testid="ads-meta-spend">{money(meta.spend_minor, meta.currency)}</td>
                  <td>—</td>
                </tr>
                <tr data-testid="ads-row-clicks">
                  <th scope="row">{t("ads.step.clicks")}</th>
                  <td data-testid="ads-meta-clicks">{n(meta.link_clicks)}</td>
                  <td>—</td>
                </tr>
                <tr data-testid="ads-row-checkout">
                  <th scope="row">{t("ads.step.checkout")}</th>
                  <td>—</td>
                  <td data-testid="ads-ours-checkout">{n(ours.checkouts_started)}</td>
                </tr>
                <tr data-testid="ads-row-paid">
                  <th scope="row">{t("ads.step.paid")}</th>
                  <td data-testid="ads-meta-paid">{n(meta.purchases)}</td>
                  <td data-testid="ads-ours-paid">{n(ours.paid_orders)}</td>
                </tr>
                <tr data-testid="ads-row-revenue">
                  <th scope="row">{t("ads.step.revenue")}</th>
                  <td data-testid="ads-meta-revenue">{money(meta.purchase_value_minor, meta.currency)}</td>
                  <td data-testid="ads-ours-revenue">{money(ours.revenue_net_minor, ours.currency ?? meta.currency)}</td>
                </tr>
                <tr data-testid="ads-row-mer">
                  <th scope="row">{t("ads.step.mer")}</th>
                  <td>—</td>
                  <td data-testid="ads-mer">{formatMer(mer, locale)}</td>
                </tr>
              </tbody>
            </table>
            <p className="card-meta" data-testid="ads-why">
              {t("ads.why")}
            </p>
            {differ && (
              <p className="notice" role="status" data-testid="ads-currency-note">
                {t("ads.currencyDiffers", { adCurrency: meta.currency ?? "", orderCurrency: ours.currency ?? "" })}
              </p>
            )}
            {report.shared_spend && (
              <p className="notice" role="status" data-testid="ads-shared-spend">
                {t("ads.sharedSpend")}
              </p>
            )}
          </section>

          <section aria-labelledby="ads-untagged-title" data-testid="ads-untagged">
            <h2 id="ads-untagged-title">{t("ads.untagged.title")}</h2>
            {percent === null ? (
              <p data-testid="ads-untagged-text">{t("ads.untagged.noOrders")}</p>
            ) : report.untagged.untagged_orders === 0 ? (
              <p data-testid="ads-untagged-text">{t("ads.untagged.none")}</p>
            ) : (
              <>
                <p data-testid="ads-untagged-text">
                  {t("ads.untagged.text", {
                    untagged: n(report.untagged.untagged_orders),
                    paid: n(report.untagged.paid_orders),
                    percent,
                  })}
                </p>
                <p>{t("ads.untagged.advice")}</p>
              </>
            )}
          </section>

          <section aria-labelledby="ads-campaigns-title">
            <h2 id="ads-campaigns-title">{t("ads.campaigns.title")}</h2>
            {report.campaigns.length === 0 ? (
              <p data-testid="ads-campaigns-empty">{t("ads.campaigns.empty")}</p>
            ) : (
              <div className="table-wrap">
                <table className="table" data-testid="ads-campaigns">
                  <thead>
                    <tr>
                      <th scope="col">{t("ads.col.campaign")}</th>
                      <th scope="col">{t("ads.col.spend")}</th>
                      <th scope="col">{t("ads.col.clicks")}</th>
                      <th scope="col">{t("ads.col.metaPurchases")}</th>
                      <th scope="col">{t("ads.col.paidOrders")}</th>
                      <th scope="col">{t("ads.col.revenue")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {report.campaigns.map((c) => (
                      <tr key={c.campaign_id} data-testid="ads-campaign" data-campaign={c.campaign_id}>
                        <th scope="row">
                          {c.campaign_name !== "" ? c.campaign_name : c.campaign_id}
                          {c.shared && (
                            <>
                              {" "}
                              <span className="badge" data-testid="ads-campaign-shared">
                                {t("ads.shared")}
                              </span>
                            </>
                          )}
                        </th>
                        <td>{money(c.spend_minor, c.currency)}</td>
                        <td>{n(c.link_clicks)}</td>
                        <td>{n(c.meta_purchases)}</td>
                        <td>{n(c.paid_orders)}</td>
                        <td>{money(c.revenue_net_minor, c.revenue_currency ?? c.currency)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      )}
      {template}
    </>
  );
}

function TemplateCopy({ value }: { value: string }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
    } catch {
      /* the text is selectable: the person can copy it by hand */
    }
  };
  return (
    <div>
      <p>
        <code className="mono" data-testid="ads-template-value">
          {value}
        </code>
      </p>
      <button type="button" className="btn" onClick={() => void copy()} data-testid="ads-template-copy">
        {copied ? t("ads.template.copied") : t("ads.template.copy")}
      </button>
    </div>
  );
}

export interface EventAdsBodyProps {
  orgId: string;
  eventId: string;
  canConnect: boolean;
}

export function EventAdsBody({ orgId, eventId, canConnect }: EventAdsBodyProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [range, setWindow] = useState<{ from?: string; to?: string }>({});
  const query = useQuery({
    queryKey: ["eventMetaAds", orgId, eventId, range.from ?? "", range.to ?? ""],
    queryFn: () => fetchEventMetaAds(orgId, eventId, range),
    retry: false,
  });
  const [refreshed, setRefreshed] = useState<MetaAdsRefreshResult | null>(null);
  const [failure, setFailure] = useState<unknown>(null);
  const [nowMs, setNowMs] = useState(() => Date.now());
  const refresh = useMutation({
    mutationFn: () => refreshOrgMetaAds(orgId),
    onSuccess: (result) => {
      setFailure(null);
      setRefreshed(result);
      setNowMs(Date.now());
      void queryClient.invalidateQueries({ queryKey: ["eventMetaAds", orgId, eventId] });
    },
    onError: (error) => setFailure(error),
  });

  // The button opens again at the server's time.
  const notBefore = refreshed?.not_before ?? null;
  useEffect(() => {
    if (notBefore === null) return;
    const wait = Date.parse(notBefore) - Date.now();
    if (!Number.isFinite(wait) || wait <= 0) return;
    const timer = setTimeout(() => setNowMs(Date.now()), Math.min(wait + 50, 2_147_000_000));
    return () => clearTimeout(timer);
  }, [notBefore]);

  return (
    <section aria-labelledby="ads-title" data-testid="event-ads">
      <div className="screen-head">
        <h1 id="ads-title">{t("ads.title")}</h1>
      </div>
      <p>{t("ads.intro")}</p>
      {query.isLoading && <Loading />}
      {query.error !== null && <ErrorBox error={query.error} onRetry={() => void query.refetch()} />}
      {query.data !== undefined && (
        <EventAdsView
          orgId={orgId}
          report={query.data}
          canConnect={canConnect}
          refreshing={refresh.isPending}
          refreshed={refreshed}
          nowMs={nowMs}
          failure={failure}
          onRefresh={() => refresh.mutate()}
          onPreset={(days) => setWindow(lastDays(days, new Date(), query.data?.sync.timezone))}
        />
      )}
    </section>
  );
}

export function EventAdsScreen() {
  const { t } = useI18n();
  const { orgId, orgName, access } = useOrg();
  const { eventId } = useParams({ from: "/o/$orgId/events/$eventId/ads" });
  return (
    <article>
      <p>
        <Link to="/o/$orgId/events/$eventId" params={{ orgId, eventId }} data-testid="ads-back">
          {t("common.back")}
        </Link>
      </p>
      {access.can("report.read") ? (
        <EventAdsBody orgId={orgId} eventId={eventId} canConnect={access.can("org.update")} />
      ) : (
        <>
          <h1 id="ads-title">{t("ads.title")}</h1>
          <p className="notice" data-testid="ads-no-access">
            {t("ads.noAccess", { org: orgName })}
          </p>
        </>
      )}
    </article>
  );
}
