import { useMemo } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useQueries, useQuery } from "@tanstack/react-query";
import { fetchEventSummary, listEvents } from "@/lib/api/endpoints";
import type { EventItem, EventSummary } from "@/lib/api/types";
import { filterEvents, isKnownSalesState, searchEvents, type EventFilter } from "@/lib/events";
import { wizardGate } from "@/lib/features";
import { formatDate } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import type { Tone } from "@/lib/orders";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { SalesFigures } from "@/components/SalesFigures";
import { useOrg } from "@/screens/OrgLayout";

export const EVENTS_PAGE_SIZE = 10;

const STATE_TONE: Record<string, Tone> = { on_sale: "ok", upcoming: "wait", sold_out: "bad", archived: "neutral" };

export function useEventsList(orgId: string) {
  const { locale } = useI18n();
  return useQuery({ queryKey: ["events", orgId, locale], queryFn: () => listEvents(orgId, locale) });
}

export function SalesStateBadge({ state }: { state: string }) {
  const { t } = useI18n();
  if (!isKnownSalesState(state)) return null;
  return <Badge tone={STATE_TONE[state] ?? "neutral"}>{t(`state.${state}`)}</Badge>;
}

/** The time zone of the event's next session, read from its summary. */
export function nextSessionZone(summary: EventSummary | undefined, iso: string | null): string | null {
  if (summary === undefined || iso === null) return null;
  const s = summary.sessions.find((x) => x.start_at === iso) ?? summary.sessions[0];
  return s?.venue_timezone ?? null;
}

function EventRow({ orgId, event, summary }: { orgId: string; event: EventItem; summary?: EventSummary }) {
  const { t, locale } = useI18n();
  const date = event.next_session_at ?? event.last_session_at ?? event.first_session_at;
  return (
    <li className="card" data-testid="event-row">
      <Link to="/o/$orgId/events/$eventId" params={{ orgId, eventId: event.id }} className="card-link">
        <span className="card-title">{event.name}</span>
        <span className="card-meta">
          <SalesStateBadge state={event.sales_state} />
          {event.status === "draft" && <Badge tone="neutral">{t("events.draft")}</Badge>}
          <span>
            {date !== null
              ? t("events.next", { date: formatDate(date, nextSessionZone(summary, date), locale) })
              : t("events.noDate")}
          </span>
          {event.session_count > 1 && <span>{t("events.sessions", { count: event.session_count })}</span>}
        </span>
      </Link>
      {summary !== undefined && (
        <SalesFigures places={summary.places} money={summary.money} compact />
      )}
    </li>
  );
}

export function EventsScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const search = useSearch({ from: "/o/$orgId/events" });
  const navigate = useNavigate({ from: "/o/$orgId/events" });
  const filter: EventFilter = search.f ?? "run";
  const query = search.q ?? "";
  const events = useEventsList(orgId);

  const all = events.data?.events ?? [];
  const counts = useMemo(
    () => ({ run: filterEvents(all, "run").length, arc: filterEvents(all, "arc").length }),
    [all],
  );
  const list = useMemo(() => searchEvents(filterEvents(all, filter), query), [all, filter, query]);
  const pages = Math.max(1, Math.ceil(list.length / EVENTS_PAGE_SIZE));
  const page = Math.min(search.page ?? 1, pages);
  const visible = list.slice((page - 1) * EVENTS_PAGE_SIZE, page * EVENTS_PAGE_SIZE);

  // One summary per visible row: the list route carries no sales figures
  // yet (spec section 13, task WEB-LIST).
  const canSales = access.can("order.read");
  const summaries = useQueries({
    queries: visible.map((e) => ({
      queryKey: ["eventSummary", orgId, e.id],
      queryFn: () => fetchEventSummary(orgId, e.id),
      enabled: canSales,
      staleTime: 30_000,
    })),
  });

  const setSearch = (next: { f?: EventFilter; q?: string; page?: number }) =>
    void navigate({ search: (prev) => ({ ...prev, ...next }), replace: true });

  return (
    <section aria-labelledby="events-title">
      <h1 id="events-title">{t("events.title")}</h1>
      {/* 6-1c: same gate as the menu entry and the wizard screen: the build flag and the import permission. */}
      {wizardGate(access.can) === "ok" && (
        <p>
          <Link to="/o/$orgId/events/new" params={{ orgId }} className="btn btn-primary" data-testid="events-create">
            {t("wizard.events.create")}
          </Link>
        </p>
      )}
      <div className="tabs" role="group" aria-label={t("events.title")}>
        {(["run", "arc"] as const).map((f) => (
          <button
            key={f}
            type="button"
            className={filter === f ? "tab active" : "tab"}
            aria-pressed={filter === f}
            onClick={() => setSearch({ f: f === "run" ? undefined : f, page: undefined })}
            data-testid={`events-tab-${f}`}
          >
            {t(f === "run" ? "events.tab.run" : "events.tab.arc", { count: counts[f] })}
          </button>
        ))}
      </div>
      <label htmlFor="events-search" className="visually-hidden">
        {t("events.search")}
      </label>
      <input
        id="events-search"
        type="search"
        placeholder={t("events.search")}
        value={query}
        onChange={(e) => setSearch({ q: e.target.value.length > 0 ? e.target.value : undefined, page: undefined })}
        className="search"
      />
      {events.isLoading && <Loading />}
      {events.error !== null && <ErrorBox error={events.error} onRetry={() => void events.refetch()} />}
      {events.data !== undefined && visible.length === 0 && (
        <Empty>
          {query.length > 0
            ? t("events.empty.search", { query })
            : t(filter === "run" ? "events.empty.run" : "events.empty.arc")}
        </Empty>
      )}
      <ul className="cards" data-testid="event-list">
        {visible.map((e, i) => (
          <EventRow key={e.id} orgId={orgId} event={e} summary={summaries[i]?.data} />
        ))}
      </ul>
      {pages > 1 && (
        <nav className="pager" aria-label={t("events.page", { page, pages })}>
          <button type="button" className="btn" disabled={page <= 1} onClick={() => setSearch({ page: page - 1 > 1 ? page - 1 : undefined })}>
            {t("events.prev")}
          </button>
          <span aria-live="polite">{t("events.page", { page, pages })}</span>
          <button type="button" className="btn" disabled={page >= pages} onClick={() => setSearch({ page: page + 1 })}>
            {t("events.nextPage")}
          </button>
        </nav>
      )}
    </section>
  );
}
