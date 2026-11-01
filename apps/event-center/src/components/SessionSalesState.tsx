/**
 * The sales state of ONE session (migration 0183): "open", "coming soon" or
 * "sold out" for every channel at once, the widget, the tickets page and the
 * WordPress sites. The bot's twin is `sessions_sales_state.go` (`ses:ss:`).
 *
 * - The state set by hand wins. Without one the platform decides: no free
 *   place is "sold out", a category whose sale has not started is "coming
 *   soon". The block says which of the two is in force.
 * - A switch is reversible and emails nobody, so it is one press in a dialog
 *   (ConfirmPressDialog), never a typed word.
 * - After a change the block says whether the sites heard about it, and offers
 *   to ask again while the delivery is still under way.
 * - The journal (who, when, from which client) opens on request.
 *
 * Everyone who may read the session sees the state. Only a person with
 * `session.update` gets the buttons, and never on a canceled session.
 */
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Badge, ErrorBox, Loading } from "@/components/ui";
import { getSessionSalesState, listSessionSalesStateHistory, setSessionSalesState } from "@/lib/api/endpoints";
import type { SessionSalesState as SalesStateResponse } from "@/lib/api/types";
import { formatDateTime, formatNumber } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  effectiveState,
  salesStateChoices,
  salesStateTone,
  siteSyncInFlight,
  siteSyncStatus,
  type EffectiveSalesState,
  type SalesStateChoice,
} from "@/lib/salesState";

export interface SessionSalesStateProps {
  orgId: string;
  eventId: string;
  sessionId: string;
  /** The venue's IANA zone; null shows times in the browser's zone. */
  timezone: string | null;
  /** The person may change a session (`session.update`). */
  canUpdate: boolean;
  /** A canceled session: the state is shown, never changed. */
  cancelled?: boolean;
}

const SOURCES = ["telegram_bot", "event_center_web", "site_plugin", "admin_web", "api"];

export function SessionSalesState({ orgId, eventId, sessionId, timezone, canUpdate, cancelled = false }: SessionSalesStateProps) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const key = ["salesState", orgId, sessionId] as const;
  const state = useQuery({ queryKey: key, queryFn: () => getSessionSalesState(orgId, sessionId) });
  const [asking, setAsking] = useState<SalesStateChoice | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [checkError, setCheckError] = useState<unknown>(null);
  const [historyOpen, setHistoryOpen] = useState(false);

  const stateName = (s: EffectiveSalesState | null, raw: string) => (s === null ? raw : t(`salesState.state.${s}` as MessageKey));
  const sourceName = (raw: string) => (SOURCES.includes(raw) ? t(`salesState.source.${raw}` as MessageKey) : raw);

  const apply = async (to: SalesStateChoice) => {
    const res = await setSessionSalesState(orgId, sessionId, to);
    queryClient.setQueryData<SalesStateResponse>(key, res);
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["sessions", orgId, eventId] }),
      queryClient.invalidateQueries({ queryKey: ["salesStateHistory", orgId, sessionId] }),
    ]);
    const now = effectiveState(res.effective);
    let note = res.changed === false ? t("salesState.noChange") : t("salesState.done", { state: stateName(now, res.effective) });
    if (to === "open" && now !== "open") note += " " + t("salesState.autoNote", { state: stateName(now, res.effective) });
    setDone(note);
    setAsking(null);
  };

  const check = async () => {
    setCheckError(null);
    try {
      await state.refetch({ throwOnError: true });
    } catch (err) {
      setCheckError(err);
    }
  };

  return (
    <section aria-labelledby="sales-state-title" data-testid="sales-state">
      <h2 id="sales-state-title">{t("salesState.title")}</h2>
      <p className="muted">{t("salesState.hint")}</p>
      {state.isLoading && <Loading />}
      {state.error !== null && state.data === undefined && <ErrorBox error={state.error} onRetry={() => void state.refetch()} />}
      {state.data !== undefined &&
        (() => {
          const d = state.data;
          const now = effectiveState(d.effective);
          const site = siteSyncStatus(d.site_sync.status);
          const choices = canUpdate && !cancelled ? salesStateChoices(d.manual, d.effective) : [];
          return (
            <>
              <p data-testid="sales-state-value">
                <Badge tone={salesStateTone(now)}>{stateName(now, d.effective)}</Badge>{" "}
                <span className="muted" data-testid="sales-state-origin">
                  {d.manual !== null ? t("salesState.manual") : t("salesState.auto")}
                </span>
              </p>
              {d.changed_by !== null && d.changed_at !== null && (
                <p className="muted" data-testid="sales-state-changed-by">
                  {t("salesState.changedBy", {
                    name: d.changed_by.name !== "" ? d.changed_by.name : t("salesState.someone"),
                    when: formatDateTime(d.changed_at, timezone, locale),
                    source: sourceName(d.changed_by.source),
                  })}
                </p>
              )}
              <p data-testid="sales-state-places">
                {t("salesState.places", { free: formatNumber(d.free_places, locale), held: formatNumber(d.held_places, locale) })}
              </p>
              {d.external_allocations > 0 && (
                <p className="notice" data-testid="sales-state-external">
                  {t("salesState.external", { count: formatNumber(d.external_allocations, locale) })}
                </p>
              )}
              {site !== "none" && (
                <p className={site === "failed" ? "notice notice-bad" : "notice"} role="status" data-testid="sales-state-site">
                  <span data-site-status={site}>{t(`salesState.site.${site}` as MessageKey, { sites: d.site_sync.sites, attempts: d.site_sync.attempts })}</span>
                  {siteSyncInFlight(site) && (
                    <>
                      {" "}
                      <button type="button" className="btn btn-link" onClick={() => void check()} data-testid="sales-state-site-check">
                        {t("salesState.site.check")}
                      </button>
                    </>
                  )}
                </p>
              )}
              {checkError !== null && <ErrorNotice error={checkError} testId="sales-state-check-error" />}
              {canUpdate && !cancelled && d.org_sales_chat === false && (
                <p className="muted" data-testid="sales-state-no-chat">
                  {t("salesState.noChat")}
                </p>
              )}
              {cancelled && (
                <p className="notice" data-testid="sales-state-cancelled">
                  {t("salesState.cancelledNote")}
                </p>
              )}
              {choices.length > 0 && (
                <div className="actions">
                  {choices.map((c) => (
                    <button
                      key={c}
                      type="button"
                      className="btn"
                      onClick={() => {
                        setDone(null);
                        setAsking(c);
                      }}
                      data-testid={`sales-state-set-${c}`}
                    >
                      {t(`salesState.choice.${c}` as MessageKey)}
                    </button>
                  ))}
                </div>
              )}
              {done !== null && (
                <p role="status" className="notice" data-testid="sales-state-done">
                  {done}
                </p>
              )}
              {asking !== null && (
                <ConfirmPressDialog
                  title={t(`salesState.confirm.${asking}.title` as MessageKey)}
                  explain={t(`salesState.confirm.${asking}.explain` as MessageKey)}
                  confirmLabel={t(`salesState.confirm.${asking}.button` as MessageKey)}
                  testId="sales-state-confirm"
                  onConfirm={() => apply(asking)}
                  onCancel={() => setAsking(null)}
                />
              )}
              <p>
                <button
                  type="button"
                  className="btn btn-link"
                  aria-expanded={historyOpen}
                  onClick={() => setHistoryOpen(!historyOpen)}
                  data-testid="sales-state-history-toggle"
                >
                  {historyOpen ? t("salesState.history.hide") : t("salesState.history.show")}
                </button>
              </p>
              {historyOpen && <History orgId={orgId} sessionId={sessionId} timezone={timezone} stateName={stateName} sourceName={sourceName} />}
            </>
          );
        })()}
    </section>
  );
}

function History({
  orgId,
  sessionId,
  timezone,
  stateName,
  sourceName,
}: {
  orgId: string;
  sessionId: string;
  timezone: string | null;
  stateName: (s: EffectiveSalesState | null, raw: string) => string;
  sourceName: (raw: string) => string;
}) {
  const { t, locale } = useI18n();
  const history = useQuery({
    queryKey: ["salesStateHistory", orgId, sessionId],
    queryFn: () => listSessionSalesStateHistory(orgId, sessionId),
  });
  if (history.isLoading) return <Loading />;
  if (history.error !== null) return <ErrorBox error={history.error} onRetry={() => void history.refetch()} />;
  const items = history.data?.items ?? [];
  if (items.length === 0) {
    return (
      <p className="muted" data-testid="sales-state-history-empty">
        {t("salesState.history.empty")}
      </p>
    );
  }
  return (
    <ul className="facts" data-testid="sales-state-history">
      {items.map((it) => (
        <li key={it.id} data-testid="sales-state-history-row">
          {t("salesState.history.line", {
            when: formatDateTime(it.created_at, timezone, locale),
            from: stateName(effectiveState(it.old_state), it.old_state),
            to: stateName(effectiveState(it.new_state), it.new_state),
            name: it.changed_by.name !== "" ? it.changed_by.name : t("salesState.someone"),
            source: sourceName(it.changed_by.source),
          })}
        </li>
      ))}
    </ul>
  );
}
