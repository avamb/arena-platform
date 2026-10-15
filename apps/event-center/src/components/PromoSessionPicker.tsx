/**
 * The chooser of the sessions a promo code is for: an event, then its
 * sessions as checkboxes. It is shared by the new-code form and the
 * "Choose sessions" edit of an existing code.
 *
 * The selection is ONE list across events, so the sessions a code already has
 * on other events stay untouched while one event's sessions are re-chosen
 * (the bot's per-event merge, `mergeSessions`). A session sold in another
 * currency than a fixed discount can never take the code, so it cannot be
 * ticked.
 */
import { useId, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchEventSummary, listSessions } from "@/lib/api/endpoints";
import { filterEvents } from "@/lib/events";
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { mergeSessions, sessionTakesCode } from "@/lib/promo";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { useEventsList } from "@/screens/EventsScreen";

export interface PromoSessionPickerProps {
  orgId: string;
  selected: readonly string[];
  onChange: (next: string[]) => void;
  /** The currency of a fixed discount; "" for a percent code. */
  fixedCurrency: string;
  /** The summary route needs order.read; without it the zone is not known and UTC is said. */
  canSummary: boolean;
  disabled?: boolean;
}

export function PromoSessionPicker({ orgId, selected, onChange, fixedCurrency, canSummary, disabled = false }: PromoSessionPickerProps) {
  const { t, locale } = useI18n();
  const ids = useId();
  const events = useEventsList(orgId);
  const [eventId, setEventId] = useState("");
  const running = useMemo(() => filterEvents(events.data?.events ?? [], "run"), [events.data]);

  const sessions = useQuery({
    queryKey: ["sessions", orgId, eventId],
    queryFn: () => listSessions(orgId, eventId),
    enabled: eventId !== "",
  });
  // The venue's time zone of each date, when the person may read the summary.
  const summary = useQuery({
    queryKey: ["eventSummary", orgId, eventId],
    queryFn: () => fetchEventSummary(orgId, eventId),
    enabled: eventId !== "" && canSummary,
    staleTime: 30_000,
  });

  const live = (sessions.data?.sessions ?? []).filter((s) => s.status !== "cancelled");
  const eventSessionIds = new Set(live.map((s) => s.id));
  const chosen = new Set(selected);
  const zoneOf = (id: string): string | null => summary.data?.sessions.find((x) => x.id === id)?.venue_timezone ?? null;

  const toggle = (id: string) => {
    const next = new Set(chosen);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    onChange([...next]);
  };
  const setAll = (on: boolean) => {
    const wanted = on ? live.filter((s) => sessionTakesCode(fixedCurrency, s.currency)).map((s) => s.id) : [];
    onChange(mergeSessions(selected, eventSessionIds, wanted));
  };
  const onOtherEvents = selected.filter((id) => !eventSessionIds.has(id)).length;

  return (
    <div className="picker" data-testid="promo-picker">
      <label htmlFor={`${ids}-event`}>{t("promo.pick.event")}</label>
      {events.isLoading && <Loading />}
      {events.error !== null && <ErrorBox error={events.error} onRetry={() => void events.refetch()} />}
      {events.data !== undefined && running.length === 0 && <Empty>{t("promo.pick.noEvents")}</Empty>}
      {running.length > 0 && (
        <select id={`${ids}-event`} value={eventId} disabled={disabled} onChange={(e) => setEventId(e.target.value)} data-testid="promo-pick-event">
          <option value="">{t("promo.pick.choose")}</option>
          {running.map((e) => (
            <option key={e.id} value={e.id}>
              {e.name}
            </option>
          ))}
        </select>
      )}

      {eventId !== "" && sessions.isLoading && <Loading />}
      {sessions.error !== null && <ErrorBox error={sessions.error} onRetry={() => void sessions.refetch()} />}
      {eventId !== "" && sessions.data !== undefined && live.length === 0 && <Empty>{t("promo.pick.noSessions")}</Empty>}
      {live.length > 0 && (
        <fieldset className="check-list" disabled={disabled}>
          <legend>{t("promo.pick.sessions")}</legend>
          <ul>
            {live.map((s) => {
              const on = chosen.has(s.id);
              const blocked = !on && !sessionTakesCode(fixedCurrency, s.currency);
              return (
                <li key={s.id}>
                  <label className="check">
                    <input
                      type="checkbox"
                      checked={on}
                      disabled={blocked}
                      onChange={() => toggle(s.id)}
                      aria-describedby={blocked ? `${ids}-${s.id}-why` : undefined}
                      data-testid="promo-pick-session"
                    />
                    <span>{formatDateTime(s.start_at, zoneOf(s.id), locale)}</span>
                  </label>
                  {blocked && (
                    <span id={`${ids}-${s.id}-why`} className="muted">
                      {t("promo.pick.otherCurrency", { currency: s.currency, fixed: fixedCurrency })}
                    </span>
                  )}
                </li>
              );
            })}
          </ul>
          <div className="actions">
            <button type="button" className="btn" onClick={() => setAll(true)} data-testid="promo-pick-all">
              {t("promo.pick.selectAll")}
            </button>
            <button type="button" className="btn" onClick={() => setAll(false)} data-testid="promo-pick-none">
              {t("promo.pick.clear")}
            </button>
          </div>
        </fieldset>
      )}
      <p className="muted" role="status" data-testid="promo-pick-count">
        {t("promo.pick.chosen", { count: selected.length })}
        {onOtherEvents > 0 && eventId !== "" ? ` ${t("promo.pick.others", { count: onOtherEvents })}` : ""}
      </p>
    </div>
  );
}
