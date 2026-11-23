/**
 * The tour of an event on its edit screen (spec 63, L-2): a select of the organization's tours and ONE save that
 * links the event (PATCH `series_id` with a UUID) or unlinks it (null). A refusal of the API, such as a tour that
 * was archived meanwhile, is shown in words, and the select keeps what is stored.
 */
import { useId, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { setEventSeries } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import { ErrorNotice } from "@/components/ErrorNotice";
import { useSeriesList } from "@/screens/SeriesScreen";
import { eventSeriesErrorKey, linkableSeries } from "@/lib/series";

export interface EventSeriesSectionProps {
  orgId: string;
  eventId: string;
  /** The tour the event is in now, as the events list says. */
  seriesId: string | null;
  seriesName: string | null;
}

export function EventSeriesSection({ orgId, eventId, seriesId, seriesName }: EventSeriesSectionProps) {
  const { t } = useI18n();
  const ids = useId();
  const queryClient = useQueryClient();
  const list = useSeriesList(orgId, true);
  const [choice, setChoice] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const stored = seriesId ?? "";
  const value = choice ?? stored;
  const save = useMutation({
    mutationFn: (next: string) => setEventSeries(orgId, eventId, next === "" ? null : next),
    onSuccess: () => {
      setSaved(true);
      void queryClient.invalidateQueries({ queryKey: ["events", orgId] });
      void queryClient.invalidateQueries({ queryKey: ["series", orgId] });
    },
  });

  const options = linkableSeries(list.data?.items ?? [], seriesId);
  // The stored tour is always an option, even when the list has not come (yet) or the tour is gone from it.
  const missing = seriesId !== null && !options.some((s) => s.id === seriesId);
  const dirty = value !== stored;
  const apiKey = save.error !== null ? eventSeriesErrorKey(save.error) : null;

  return (
    <section aria-labelledby={`${ids}-title`} data-testid="event-series">
      <h2 id={`${ids}-title`}>{t("series.event.title")}</h2>
      <p className="field-hint">{t("series.event.hint")}</p>
      {list.isLoading && <p className="muted">{t("series.event.loading")}</p>}
      {list.error !== null && <ErrorNotice error={list.error} testId="event-series-list-error" />}
      {list.data !== undefined && list.data.items.length === 0 && seriesId === null && (
        <p className="muted" data-testid="event-series-none">
          {t("series.event.noTours")}
        </p>
      )}
      <label htmlFor={`${ids}-select`}>{t("series.event.label")}</label>
      <select
        id={`${ids}-select`}
        value={value}
        disabled={save.isPending}
        onChange={(e) => {
          setSaved(false);
          save.reset();
          setChoice(e.target.value);
        }}
        data-testid="event-series-select"
      >
        <option value="">{t("series.event.none")}</option>
        {missing && <option value={seriesId}>{seriesName ?? ""}</option>}
        {options.map((s) => (
          <option key={s.id} value={s.id}>
            {s.archived_at !== null ? t("series.event.archivedOption", { name: s.name }) : s.name}
          </option>
        ))}
      </select>
      {save.error !== null &&
        (apiKey !== null ? (
          <p className="bad-text" role="alert" data-testid="event-series-error">
            {t(apiKey)}
          </p>
        ) : (
          <ErrorNotice error={save.error} testId="event-series-error" />
        ))}
      <div className="actions">
        <button type="button" className="btn btn-primary" disabled={!dirty || save.isPending} onClick={() => save.mutate(value)} data-testid="event-series-save">
          {save.isPending ? t("series.event.saving") : t("series.event.save")}
        </button>
        {seriesId !== null && (
          <Link to="/o/$orgId/series/$seriesId" params={{ orgId, seriesId }} data-testid="event-series-open">
            {seriesName ?? t("series.event.title")}
          </Link>
        )}
      </div>
      <p className="notice notice-ok" role="status" aria-live="polite" data-testid="event-series-saved" hidden={!saved || dirty}>
        {saved && !dirty ? t("series.event.saved") : ""}
      </p>
    </section>
  );
}
