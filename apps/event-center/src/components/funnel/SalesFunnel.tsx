import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchEventFunnel } from "@/lib/api/endpoints";
import type { SalesFunnelMoney, SalesFunnelResponse, SalesFunnelStep } from "@/lib/api/types";
import { formatDateTime, formatMoney, formatNumber } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { FUNNEL_STEPS, formatShare, funnelRows, isEmptyFunnel, lastDays, sourceRows } from "@/lib/salesFunnel";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import "./funnel.css";

/**
 * "Sales funnel" of an event (spec 50, A7): how far buyers got, from seeing the
 * event to paying, with where they came from. The first three steps are
 * anonymous counters of the widget and the pages, the last three are counted
 * from the orders. The view counters are a guide, not proof for an advertising
 * account, and the screen says so.
 *
 * Sites that sell through the plugin have no browser steps: they are listed
 * apart and start at the chosen seats, instead of showing zeros that look like
 * a failure.
 */
export function SalesFunnel({ orgId, eventId }: { orgId: string; eventId: string }) {
  const { t, locale } = useI18n();
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [sessionId, setSessionId] = useState("");

  // The picker must not empty while the next window loads.
  const lastSessions = useRef<SalesFunnelResponse["sessions"]>([]);
  const funnel = useQuery({
    queryKey: ["eventFunnel", orgId, eventId, from, to, sessionId],
    queryFn: () => fetchEventFunnel(orgId, eventId, { from, to, sessionId }),
    retry: false,
  });

  const n = (v: number) => formatNumber(v, locale);
  const stepLabel = (step: string) => t(`funnel.step.${step}` as MessageKey);
  const money = (list: readonly SalesFunnelMoney[]) =>
    list.length === 0 ? "—" : list.map((m) => formatMoney(m.amount_minor, m.currency, locale)).join(" + ");

  const preset = (days: number) => {
    // The venue's days, not the browser's: the API reads from and to in the venue's zone.
    const w = lastDays(days, new Date(), funnel.data?.timezone);
    setFrom(w.from);
    setTo(w.to);
  };

  const stepList = (steps: readonly SalesFunnelStep[], testId: string) => (
    <ol className="funnel-steps" data-testid={testId}>
      {funnelRows(steps).map((r) => (
        <li key={r.step} className="funnel-step" data-testid="funnel-step" data-step={r.step}>
          <span className="funnel-step-name">{stepLabel(r.step)}</span>
          <progress max={100} value={r.bar} aria-label={stepLabel(r.step)} />
          <span className="funnel-step-figures">
            <strong data-testid="funnel-count">{r.count === null ? "—" : n(r.count)}</strong>
            {r.share !== null && <span>{t("funnel.share", { share: formatShare(r.share, locale) })}</span>}
            {r.lost !== null && <span className="card-meta">{t("funnel.lost", { percent: r.lost })}</span>}
            {r.suspect > 0 && <span className="card-meta">{t("funnel.suspect", { count: n(r.suspect) })}</span>}
          </span>
        </li>
      ))}
    </ol>
  );

  const data = funnel.data;
  if (data !== undefined) lastSessions.current = data.sessions;
  const rows = data === undefined ? [] : sourceRows(data.sources);

  return (
    <div data-testid="sales-funnel">
      <p className="card-meta">{t("funnel.note")}</p>

      <form className="funnel-filters" onSubmit={(e) => e.preventDefault()} data-testid="funnel-filters">
        <div>
          <label htmlFor="funnel-session">{t("funnel.session")}</label>
          <select id="funnel-session" value={sessionId} onChange={(e) => setSessionId(e.target.value)} data-testid="funnel-session">
            <option value="">{t("funnel.allSessions")}</option>
            {/* Each date on the clock of its own venue. The list comes with the answer, so a screen that
                changes the session keeps showing the list it had. */}
            {(funnel.data?.sessions ?? lastSessions.current).map((s) => (
              <option key={s.id} value={s.id}>
                {formatDateTime(s.start_at, s.timezone, locale)}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="funnel-from">{t("funnel.from")}</label>
          <input id="funnel-from" type="date" value={from} onChange={(e) => setFrom(e.target.value)} data-testid="funnel-from" />
        </div>
        <div>
          <label htmlFor="funnel-to">{t("funnel.to")}</label>
          <input id="funnel-to" type="date" value={to} onChange={(e) => setTo(e.target.value)} data-testid="funnel-to" />
        </div>
      </form>
      <div className="funnel-presets">
        {[7, 30, 90].map((d) => (
          <button key={d} type="button" className="btn" onClick={() => preset(d)} data-testid={`funnel-preset-${d}`}>
            {t("funnel.lastDays", { days: d })}
          </button>
        ))}
        {(from !== "" || to !== "") && (
          <button
            type="button"
            className="btn"
            onClick={() => {
              setFrom("");
              setTo("");
            }}
            data-testid="funnel-preset-reset"
          >
            {t("funnel.defaultWindow")}
          </button>
        )}
      </div>

      {funnel.isLoading && <Loading />}
      {funnel.error !== null && <ErrorBox error={funnel.error} onRetry={() => void funnel.refetch()} />}

      {data !== undefined && (
        <>
          <p className="card-meta" data-testid="funnel-window">
            {t("funnel.window", { from: data.from, to: data.to })}
          </p>

          {isEmptyFunnel(data) ? (
            <Empty>{t("funnel.empty")}</Empty>
          ) : (
            <>
              <section aria-labelledby="funnel-steps-title">
                <h2 id="funnel-steps-title">{t("funnel.stepsTitle")}</h2>
                {data.session_id !== null && <p className="card-meta">{t("funnel.viewOneSession")}</p>}
                {stepList(data.steps, "funnel-main")}
                <p data-testid="funnel-revenue">
                  <span>{t("funnel.revenue")}: </span>
                  <strong>{money(data.revenue)}</strong>
                </p>
              </section>

              {rows.length > 0 && (
                <section aria-labelledby="funnel-sources-title">
                  <h2 id="funnel-sources-title">{t("funnel.sources")}</h2>
                  <div className="table-wrap">
                    <table className="table">
                      <thead>
                        <tr>
                          <th scope="col">{t("funnel.col.source")}</th>
                          <th scope="col">{t("funnel.col.campaign")}</th>
                          {FUNNEL_STEPS.map((s) => (
                            <th key={s} scope="col">
                              {stepLabel(s)}
                            </th>
                          ))}
                        </tr>
                      </thead>
                      <tbody>
                        {rows.map((r) => (
                          <tr key={r.key} data-testid="funnel-source">
                            <th scope="row" data-label={t("funnel.col.source")}>
                              {r.direct ? t("funnel.direct") : r.source}
                            </th>
                            <td data-label={t("funnel.col.campaign")}>{r.campaign}</td>
                            {FUNNEL_STEPS.map((s) => (
                              <td key={s} data-label={stepLabel(s)}>
                                {n(r.counts[s] ?? 0)}
                              </td>
                            ))}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </section>
              )}

              {data.sales_links.length > 0 && (
                <section aria-labelledby="funnel-links-title">
                  <h2 id="funnel-links-title">{t("funnel.links")}</h2>
                  <div className="table-wrap">
                    <table className="table">
                      <thead>
                        <tr>
                          <th scope="col">{t("funnel.col.link")}</th>
                          <th scope="col">{stepLabel("checkout_start")}</th>
                          <th scope="col">{stepLabel("paid")}</th>
                          <th scope="col">{t("funnel.revenue")}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.sales_links.map((l) => (
                          <tr key={l.sales_link_id} data-testid="funnel-link">
                            <th scope="row" data-label={t("funnel.col.link")}>
                              {l.label ?? l.code ?? l.sales_link_id}
                            </th>
                            <td data-label={stepLabel("checkout_start")}>{n(l.checkout_start)}</td>
                            <td data-label={stepLabel("paid")}>{n(l.paid)}</td>
                            <td data-label={t("funnel.revenue")}>{money(l.revenue)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </section>
              )}

              {data.gateway !== null && (
                <section aria-labelledby="funnel-gateway-title" data-testid="funnel-gateway">
                  <h2 id="funnel-gateway-title">{t("funnel.gateway.title")}</h2>
                  <p className="card-meta">{t("funnel.gateway.note")}</p>
                  {stepList(data.gateway.steps, "funnel-gateway-steps")}
                  <p>
                    <span>{t("funnel.revenue")}: </span>
                    <strong>{money(data.gateway.revenue)}</strong>
                  </p>
                </section>
              )}
            </>
          )}
        </>
      )}
    </div>
  );
}
