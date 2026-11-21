/**
 * "Sales on the site" on the event screen (twin of the WordPress event center's switch): Open / Coming soon /
 * Sold out. The state belongs to a SESSION, so one session shows its switch directly, and several sessions show
 * one switch each under the date and time of the session. It is the session screen's component and API
 * (SessionSalesState, `.../sessions/{id}/sales-state`, right `session.update`), nothing new on the server.
 */
import { HelpPopover } from "@/components/HelpPopover";
import { SessionSalesState } from "@/components/SessionSalesState";
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";

export interface SalesSwitchSession {
  id: string;
  startAt: string;
  tz: string | null;
  cancelled: boolean;
}

export function EventSalesSwitch({
  orgId,
  eventId,
  sessions,
  canUpdate,
}: {
  orgId: string;
  eventId: string;
  sessions: SalesSwitchSession[];
  canUpdate: boolean;
}) {
  const { t, locale } = useI18n();
  if (sessions.length === 0) return null;
  return (
    <section aria-labelledby="event-sales-switch" data-testid="event-sales-switch">
      <h2 id="event-sales-switch">
        {t("eventSales.title")}
        <HelpPopover textKey="help.salesSwitch" testId="help-sales-switch" />
      </h2>
      {sessions.length === 1 ? (
        <SessionSalesState
          key={sessions[0].id}
          embedded
          orgId={orgId}
          eventId={eventId}
          sessionId={sessions[0].id}
          timezone={sessions[0].tz}
          canUpdate={canUpdate}
          cancelled={sessions[0].cancelled}
        />
      ) : (
        <ul className="cards" data-testid="event-sales-switch-list">
          {sessions.map((s) => (
            <li key={s.id} className="card" data-testid="event-sales-switch-row">
              <h3>{formatDateTime(s.startAt, s.tz, locale)}</h3>
              <SessionSalesState
                embedded
                orgId={orgId}
                eventId={eventId}
                sessionId={s.id}
                timezone={s.tz}
                canUpdate={canUpdate}
                cancelled={s.cancelled}
              />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
