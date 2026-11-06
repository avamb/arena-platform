import { Link } from "@tanstack/react-router";
import { filterEvents } from "@/lib/events";
import { useI18n } from "@/lib/i18n";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { useOrg } from "@/screens/OrgLayout";
import { useEventsList } from "@/screens/EventsScreen";

/**
 * "Summary", the fourth item of the bottom bar (spec 62 §12.1). A summary of
 * the whole organization (sessions × currency, prices × categories) is
 * EC-V1 of spec 62 §12.2 and is not built yet: until then the screen says so
 * and lists the running events, each of which opens its own summary.
 */
export function SummaryScreen() {
  const { t } = useI18n();
  const { orgId } = useOrg();
  const events = useEventsList(orgId);
  const running = events.data !== undefined ? filterEvents(events.data.events, "run") : [];

  return (
    <section aria-labelledby="summary-title">
      <h1 id="summary-title">{t("summary.title")}</h1>
      <p className="notice" role="status" data-testid="summary-soon">
        {t("summary.soon")}
      </p>
      {events.isLoading && <Loading />}
      {events.error !== null && events.data === undefined && (
        <ErrorBox error={events.error} onRetry={() => void events.refetch()} />
      )}
      {events.data !== undefined && running.length === 0 && <Empty>{t("events.empty.run")}</Empty>}
      {running.length > 0 && (
        <ul className="cards" data-testid="summary-events">
          {running.map((event) => (
            <li key={event.id} className="card">
              <Link to="/o/$orgId/events/$eventId" params={{ orgId, eventId: event.id }} className="card-link">
                <span className="card-title">{event.name}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
