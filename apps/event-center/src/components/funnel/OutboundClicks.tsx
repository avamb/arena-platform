import { useQuery } from "@tanstack/react-query";
import { fetchEventOutboundClicks } from "@/lib/api/endpoints";
import { formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { dayClickRows, formatClickShare, sellerClickRows, sourceClickRows, type ClickRow } from "@/lib/outboundClicks";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import "./funnel.css";

/**
 * "Clicks to box offices": how many visitors of the organizer's site went on to
 * an outside box office, by day, by box office and by source. Uses the same
 * window as the funnel above it.
 */
export function OutboundClicks({ orgId, eventId, from, to }: { orgId: string; eventId: string; from: string; to: string }) {
  const { t, locale } = useI18n();
  const q = useQuery({
    queryKey: ["eventOutboundClicks", orgId, eventId, from, to],
    queryFn: () => fetchEventOutboundClicks(orgId, eventId, { from, to }),
    retry: false,
  });
  const n = (v: number) => formatNumber(v, locale);
  const data = q.data;

  const table = (title: string, firstCol: string, rows: ClickRow[], testId: string) => (
    <section aria-label={title} data-testid={testId}>
      <h3>{title}</h3>
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th scope="col">{firstCol}</th>
              <th scope="col">{t("outbound.col.clicks")}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.key} data-testid={`${testId}-row`}>
                <th scope="row" data-label={firstCol}>
                  {r.direct ? t("outbound.direct") : r.label}
                </th>
                <td data-label={t("outbound.col.clicks")}>{n(r.clicks)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );

  const share = data === undefined ? null : formatClickShare(data.share_of_visitors, locale);

  return (
    <section aria-labelledby="outbound-title" data-testid="outbound-clicks">
      <h2 id="outbound-title">{t("outbound.title")}</h2>
      {q.isLoading && <Loading />}
      {q.error !== null && <ErrorBox error={q.error} onRetry={() => void q.refetch()} />}
      {data !== undefined &&
        (data.total <= 0 ? (
          <Empty>{t("outbound.empty")}</Empty>
        ) : (
          <>
            <p data-testid="outbound-total">
              <span>{t("outbound.total")}: </span>
              <strong>{n(data.total)}</strong>
            </p>
            {share !== null && (
              <p data-testid="outbound-share">
                <span>{t("outbound.share")}: </span>
                <strong>{share}</strong>
              </p>
            )}
            {table(t("outbound.byDay"), t("outbound.col.day"), dayClickRows(data.by_day), "outbound-days")}
            {table(t("outbound.bySeller"), t("outbound.col.seller"), sellerClickRows(data.by_seller), "outbound-sellers")}
            {table(t("outbound.bySource"), t("outbound.col.source"), sourceClickRows(data.by_source), "outbound-sources")}
          </>
        ))}
    </section>
  );
}
