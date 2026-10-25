/**
 * The numbers of one sales link (spec 43, QL-12): the bot's "Statistics"
 * screen plus the per-day and per-country tables the API answers. Days are
 * the API's UTC days, and the text says so.
 */
import { useQuery } from "@tanstack/react-query";
import { fetchSalesLinkStats } from "@/lib/api/endpoints";
import { formatMoney, formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { countryName, scanSums } from "@/lib/salesLinks";
import { Empty, ErrorBox, Loading } from "@/components/ui";

export function SalesLinkStatsBlock({ orgId, linkId }: { orgId: string; linkId: string }) {
  const { t, locale } = useI18n();
  const stats = useQuery({
    queryKey: ["salesLinkStats", orgId, linkId],
    queryFn: () => fetchSalesLinkStats(orgId, linkId),
  });
  return (
    <section aria-labelledby="link-stats-title" data-testid="link-stats">
      <h2 id="link-stats-title">{t("links.stats.title")}</h2>
      {stats.isLoading && <Loading />}
      {stats.error !== null && <ErrorBox error={stats.error} onRetry={() => void stats.refetch()} />}
      {stats.data !== undefined &&
        (() => {
          const s = stats.data;
          const sums = scanSums(s.by_day, new Date());
          const n = (v: number) => formatNumber(v, locale);
          return (
            <>
              <p data-testid="link-stats-scans">{t("links.stats.scans", { total: n(s.scans_total), week: n(sums.week), today: n(sums.today) })}</p>
              <p className="muted">
                {t("links.stats.utc")} {s.suspect_total > 0 ? t("links.stats.bots", { count: n(s.suspect_total) }) : ""}
              </p>
              <p data-testid="link-stats-orders">{t("links.stats.orders", { orders: n(s.orders.count), tickets: n(s.orders.tickets) })}</p>
              <p data-testid="link-stats-revenue">
                {s.orders.revenue.length === 0
                  ? t("links.stats.noRevenue")
                  : t("links.stats.revenue", { amount: s.orders.revenue.map((r) => formatMoney(r.amount_minor, r.currency, locale)).join(", ") })}
              </p>
              <p className="muted">{t("links.stats.range")}</p>
              {s.by_day.length === 0 && <Empty>{t("links.stats.noScans")}</Empty>}
              {s.by_day.length > 0 && (
                <div className="table-wrap">
                  <table className="table" data-testid="link-stats-days">
                    <caption>{t("links.stats.byDay")}</caption>
                    <thead>
                      <tr>
                        <th scope="col">{t("links.stats.col.day")}</th>
                        <th scope="col">{t("links.stats.col.scans")}</th>
                        <th scope="col">{t("links.stats.col.bots")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {[...s.by_day].reverse().map((d) => (
                        <tr key={d.day}>
                          <th scope="row" data-label={t("links.stats.col.day")}>
                            {d.day}
                          </th>
                          <td data-label={t("links.stats.col.scans")}>{n(d.hits)}</td>
                          <td data-label={t("links.stats.col.bots")}>{n(d.suspect_hits)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              {s.by_country.length > 0 && (
                <div className="table-wrap">
                  <table className="table" data-testid="link-stats-countries">
                    <caption>{t("links.stats.byCountry")}</caption>
                    <thead>
                      <tr>
                        <th scope="col">{t("links.stats.col.country")}</th>
                        <th scope="col">{t("links.stats.col.scans")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {s.by_country.map((c) => (
                        <tr key={c.country === "" ? "?" : c.country}>
                          <th scope="row" data-label={t("links.stats.col.country")}>
                            {c.country === "" ? t("links.stats.unknownCountry") : countryName(c.country, locale)}
                          </th>
                          <td data-label={t("links.stats.col.scans")}>{n(c.hits)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          );
        })()}
    </section>
  );
}
