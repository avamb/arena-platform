import { useQuery } from "@tanstack/react-query";
import { downloadPageStatsCsv, fetchPageStats } from "@/lib/api/endpoints";
import { formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { hasPageStats, kindFigure, sourceRows, type KindFigure } from "@/lib/pageStats";
import { DownloadButton } from "@/components/ui";

/**
 * "Page statistics" of an event (task 2-3, HP-2a): how many people opened the
 * event's hosted page, pressed play on a video or followed a partner link in
 * the last 30 days, with the figures of probable bots named apart and the
 * campaigns the visitors came from. A guide, not proof for an advertising
 * account, and the block says so.
 *
 * It is a side figure, like the bot's card line: a failed read shows nothing
 * rather than an error over the sales figures, and an event nobody opened
 * shows no block at all.
 */
export function PageStats({ orgId, eventId }: { orgId: string; eventId: string }) {
  const { t, locale } = useI18n();
  const stats = useQuery({
    queryKey: ["pageStats", orgId, eventId],
    queryFn: () => fetchPageStats(orgId, eventId),
    retry: false,
  });
  if (stats.data === undefined || !hasPageStats(stats.data)) return null;
  const data = stats.data;
  const n = (v: number) => formatNumber(v, locale);

  const figure = (label: string, f: KindFigure, testId: string) => (
    <li data-testid={testId}>
      <span>{label}: </span>
      <strong>{n(f.total)}</strong>
      {f.suspect > 0 && <span className="card-meta"> ({t("pageStats.suspect", { count: n(f.suspect) })})</span>}
    </li>
  );
  const sources = sourceRows(data);

  return (
    <section aria-labelledby="event-page-stats" data-testid="page-stats">
      <h2 id="event-page-stats">{t("pageStats.title")}</h2>
      <p className="card-meta">{t("pageStats.note")}</p>
      <ul className="figures">
        {figure(t("pageStats.views"), kindFigure(data.totals.page_view), "stat-views")}
        {figure(t("pageStats.plays"), kindFigure(data.totals.video_play), "stat-plays")}
        {figure(t("pageStats.clicks"), kindFigure(data.totals.partner_click), "stat-clicks")}
      </ul>
      {sources.length > 0 && (
        <div className="table-wrap">
          <table className="table">
            <caption>{t("pageStats.sources")}</caption>
            <thead>
              <tr>
                <th scope="col">{t("pageStats.col.source")}</th>
                <th scope="col">{t("pageStats.col.campaign")}</th>
                <th scope="col">{t("pageStats.col.views")}</th>
              </tr>
            </thead>
            <tbody>
              {sources.map((s) => (
                <tr key={s.key} data-testid="stat-source">
                  <th scope="row" data-label={t("pageStats.col.source")}>
                    {s.source === "" && s.campaign === "" ? t("pageStats.direct") : s.source}
                  </th>
                  <td data-label={t("pageStats.col.campaign")}>{s.campaign}</td>
                  <td data-label={t("pageStats.col.views")}>
                    {n(s.figure.total)}
                    {s.figure.suspect > 0 && <span className="card-meta"> ({t("pageStats.suspect", { count: n(s.figure.suspect) })})</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="actions">
        <DownloadButton
          label="pageStats.csv"
          testId="csv-page-stats"
          fetchFile={() => downloadPageStatsCsv(orgId, eventId, locale)}
        />
      </div>
    </section>
  );
}
