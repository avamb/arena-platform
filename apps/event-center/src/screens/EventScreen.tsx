import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { ApiError } from "@/lib/api/http";
import { downloadCsv, downloadSampleTicket, fetchEventSummary, listSessions } from "@/lib/api/endpoints";
import type { EventSummary } from "@/lib/api/types";
import { formatDateTime, formatMoney, formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { Badge, DownloadButton, Empty, ErrorBox, Loading } from "@/components/ui";
import { SalesFigures } from "@/components/SalesFigures";
import { EventStatusBlock } from "@/components/EventStatusBlock";
import { EventPoster } from "@/components/EventPoster";
import { EmailNoteSection } from "@/components/EmailNoteSection";
import { MaterialsSection } from "@/components/materials/MaterialsSection";
import { posterCapabilities } from "@/lib/posters";
import { PageStats } from "@/components/PageStats";
import { useOrg } from "@/screens/OrgLayout";
import { SalesStateBadge, useEventsList } from "@/screens/EventsScreen";

interface SessionRef {
  id: string;
  startAt: string;
  tz: string | null;
  venue: string | null;
  cancelled: boolean;
}

/** The session a sample ticket is printed for: the next live one, else the first. */
export function sampleSession(sessions: readonly SessionRef[], now: number = Date.now()): SessionRef | undefined {
  const live = sessions.filter((s) => !s.cancelled);
  return live.find((s) => Date.parse(s.startAt) >= now) ?? live[0] ?? sessions[0];
}

function TiersTable({ summary }: { summary: EventSummary }) {
  const { t, locale } = useI18n();
  if (summary.tiers.length === 0) return null;
  return (
    <div className="table-wrap">
      <table className="table">
        <caption>{t("event.categories")}</caption>
        <thead>
          <tr>
            <th scope="col">{t("col.category")}</th>
            <th scope="col">{t("col.price")}</th>
            <th scope="col">{t("col.sold")}</th>
            <th scope="col">{t("col.free")}</th>
            <th scope="col">{t("col.paid")}</th>
          </tr>
        </thead>
        <tbody>
          {summary.tiers.map((tier) => (
            <tr key={`${tier.name}-${tier.currency}-${tier.price_amount}`}>
              <th scope="row" data-label={t("col.category")}>
                {tier.name} {!tier.is_open && <Badge tone="neutral">{t("tier.closed")}</Badge>}
              </th>
              <td data-label={t("col.price")}>{formatMoney(tier.price_amount, tier.currency, locale)}</td>
              <td data-label={t("col.sold")}>
                {formatNumber(tier.places.sold, locale)} / {formatNumber(tier.places.total, locale)}
              </td>
              <td data-label={t("col.free")}>{formatNumber(tier.places.available, locale)}</td>
              <td data-label={t("col.paid")}>{formatMoney(tier.paid_revenue, tier.currency, locale)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function EventScreen() {
  const { t, locale } = useI18n();
  const { orgId, orgName, access } = useOrg();
  const { eventId } = useParams({ from: "/o/$orgId/events/$eventId" });
  const canSales = access.can("order.read");
  const canSessions = access.can("session.read");
  // The export routes need both permissions (mount_export.go).
  const canCsv = canSales && canSessions;
  // 2-3: the page statistics need event.read (the route's own gate).
  const canPageStats = access.can("event.read");
  const events = useEventsList(orgId);
  const summary = useQuery({
    queryKey: ["eventSummary", orgId, eventId],
    queryFn: () => fetchEventSummary(orgId, eventId),
    enabled: canSales,
  });
  const sessions = useQuery({
    queryKey: ["sessions", orgId, eventId],
    queryFn: () => listSessions(orgId, eventId),
    enabled: canSessions && !canSales,
  });

  const event = events.data?.events.find((e) => e.id === eventId);
  if (events.isLoading) return <Loading />;
  if (events.error !== null) return <ErrorBox error={events.error} onRetry={() => void events.refetch()} />;
  if (event === undefined) return <ErrorBox error={new ApiError(404, "event.not_found", "")} />;

  const refs: SessionRef[] =
    summary.data !== undefined
      ? summary.data.sessions.map((s) => ({
          id: s.id,
          startAt: s.start_at,
          tz: s.venue_timezone,
          venue: s.venue_name,
          cancelled: s.status === "cancelled",
        }))
      : (sessions.data?.sessions ?? []).map((s) => ({
          id: s.id,
          startAt: s.start_at,
          tz: null,
          venue: null,
          cancelled: s.status === "cancelled",
        }));
  const sample = sampleSession(refs);
  const poster = posterCapabilities(access.can);

  return (
    <article aria-labelledby="event-title">
      <p>
        <Link to="/o/$orgId/events" params={{ orgId }}>
          {t("common.back")}
        </Link>
      </p>
      <h1 id="event-title">{event.name}</h1>
      <p className="card-meta">
        <SalesStateBadge event={event} />
        {event.status === "draft" && <Badge tone="neutral">{t("events.draft")}</Badge>}
        {event.promoter_name !== null && event.promoter_name !== undefined && (
          <span>{t("event.promoter", { name: event.promoter_name })}</span>
        )}
      </p>

      <div className="actions">
        {canCsv && (
          <DownloadButton
            label="event.csvSales"
            testId="csv-event-sales"
            fetchFile={() => downloadCsv(orgId, "eventSales", eventId, locale)}
          />
        )}
        {canSessions && sample !== undefined && (
          <DownloadButton
            label="event.sample"
            testId="sample-ticket"
            fetchFile={() => downloadSampleTicket(orgId, sample.id, locale)}
          />
        )}
        {canSales && (
          <Link to="/o/$orgId/orders" params={{ orgId }} search={{ event: eventId }} className="btn">
            {t("event.orders")}
          </Link>
        )}
        {/* QL-12: a short link and QR code that lead to this event. */}
        {access.can("sales_link.manage") && (
          <Link to="/o/$orgId/links/new" params={{ orgId }} search={{ event: eventId }} className="btn" data-testid="event-qr-link">
            {t("links.button")}
          </Link>
        )}
      </div>

      <EventStatusBlock
        orgId={orgId}
        eventId={eventId}
        eventName={event.name}
        status={event.status}
        canPublish={access.can("event.publish")}
        canDelete={access.can("event.delete")}
      />

      <EventPoster
        key={`${orgId}:${eventId}`}
        orgId={orgId}
        eventId={eventId}
        eventName={event.name}
        posterMediaId={event.poster_media_id ?? null}
        canView={poster.canView}
        canChange={poster.canChange}
      />

      {/* RT-11: the organizer's own text of the buyer letters. The events list is the route that carries it. */}
      {access.can("event.update") && (
        <EmailNoteSection
          key={`${orgId}:${eventId}`}
          orgId={orgId}
          eventId={eventId}
          note={event.email_note}
          noteFormat={event.email_note_format}
          canEdit
        />
      )}

      {canSales && (
        <section aria-labelledby="event-totals">
          <h2 id="event-totals">{t("event.totals")}</h2>
          {summary.isLoading && <Loading />}
          {summary.error !== null && <ErrorBox error={summary.error} onRetry={() => void summary.refetch()} />}
          {summary.data !== undefined && (
            <>
              <SalesFigures
                places={summary.data.places}
                money={summary.data.money}
                entered={summary.data.entered}
                invitations={summary.data.complimentary.tickets}
              />
              <TiersTable summary={summary.data} />
            </>
          )}
        </section>
      )}
      {!canSales && (
        <p className="notice" data-testid="no-sales-note">
          {t(canSessions ? "event.noSales" : "event.noSessions", { org: orgName })}
        </p>
      )}

      {canPageStats && <PageStats orgId={orgId} eventId={eventId} />}

      {(canSales || canSessions) && (
        <section aria-labelledby="event-sessions">
          <h2 id="event-sessions">{t("event.sessions")}</h2>
          {sessions.isLoading && <Loading />}
          {refs.length === 0 && !summary.isLoading && !sessions.isLoading && <Empty>{t("events.noDate")}</Empty>}
          <ul className="cards" data-testid="session-list">
            {refs.map((s) => {
              const fig = summary.data?.sessions.find((x) => x.id === s.id);
              return (
                <li key={s.id} className="card" data-testid="session-row">
                  <Link
                    to="/o/$orgId/events/$eventId/sessions/$sessionId"
                    params={{ orgId, eventId, sessionId: s.id }}
                    className="card-link"
                  >
                    <span className="card-title">{formatDateTime(s.startAt, s.tz, locale)}</span>
                    <span className="card-meta">
                      {s.venue !== null && <span>{s.venue}</span>}
                      {s.cancelled && <Badge tone="bad">{t("event.cancelled")}</Badge>}
                    </span>
                  </Link>
                  {fig !== undefined && <SalesFigures places={fig.places} money={fig.money} compact />}
                </li>
              );
            })}
          </ul>
        </section>
      )}

      <MaterialsSection
        key={`${orgId}:${eventId}`}
        orgId={orgId}
        eventId={eventId}
        status={event.status}
        publicUrl={event.public_url}
        can={access.can}
      />
    </article>
  );
}
