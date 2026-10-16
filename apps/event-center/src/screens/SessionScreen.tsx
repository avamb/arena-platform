import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import {
  downloadCsv,
  downloadSampleTicket,
  fetchSessionSummary,
  listSessions,
  listTiers,
} from "@/lib/api/endpoints";
import type { TicketTierItem } from "@/lib/api/types";
import { formatDateTime, formatMoney, formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { Badge, DownloadButton, ErrorBox, Loading } from "@/components/ui";
import { SalesFigures } from "@/components/SalesFigures";
import { SessionChange } from "@/components/SessionChange";
import { useOrg } from "@/screens/OrgLayout";

function tierPrice(tier: TicketTierItem, locale: string, t: ReturnType<typeof useI18n>["t"]): string {
  if (tier.pricing_mode === "free") return t("tier.free");
  if (tier.pricing_mode === "pwyw") return t("tier.pwyw");
  return formatMoney(tier.current_price ?? tier.price_amount, tier.currency, locale);
}

function TiersTable({ tiers }: { tiers: readonly TicketTierItem[] }) {
  const { t, locale } = useI18n();
  const n = (v: number | null | undefined) => (v === null || v === undefined ? "—" : formatNumber(v, locale));
  return (
    <div className="table-wrap">
      <table className="table" data-testid="tiers-table">
        <caption>{t("event.categories")}</caption>
        <thead>
          <tr>
            <th scope="col">{t("col.category")}</th>
            <th scope="col">{t("col.price")}</th>
            <th scope="col">{t("col.sold")}</th>
            <th scope="col">{t("col.held")}</th>
            <th scope="col">{t("col.free")}</th>
            <th scope="col">{t("col.status")}</th>
          </tr>
        </thead>
        <tbody>
          {[...tiers]
            .sort((a, b) => a.sort_order - b.sort_order)
            .map((tier) => (
              <tr key={tier.id}>
                <th scope="row" data-label={t("col.category")}>
                  {tier.name}
                </th>
                <td data-label={t("col.price")}>{tierPrice(tier, locale, t)}</td>
                <td data-label={t("col.sold")}>{n(tier.sold)}</td>
                <td data-label={t("col.held")}>{n(tier.held)}</td>
                <td data-label={t("col.free")}>{n(tier.available)}</td>
                <td data-label={t("col.status")}>
                  {tier.is_open === false ? (
                    <Badge tone="neutral">{t("tier.closed")}</Badge>
                  ) : (
                    <Badge tone="ok">{t("tier.open")}</Badge>
                  )}
                </td>
              </tr>
            ))}
        </tbody>
      </table>
    </div>
  );
}

export function SessionScreen() {
  const { t, locale } = useI18n();
  const { orgId, access } = useOrg();
  const { eventId, sessionId } = useParams({ from: "/o/$orgId/events/$eventId/sessions/$sessionId" });
  const canSales = access.can("order.read");
  const canSessions = access.can("session.read");
  const canTiers = access.can("tier.read");

  const summary = useQuery({
    queryKey: ["sessionSummary", orgId, sessionId],
    queryFn: () => fetchSessionSummary(orgId, sessionId),
    enabled: canSales,
  });
  const sessions = useQuery({
    queryKey: ["sessions", orgId, eventId],
    queryFn: () => listSessions(orgId, eventId),
    enabled: canSessions,
  });
  const tiers = useQuery({
    queryKey: ["tiers", orgId, eventId, sessionId],
    queryFn: () => listTiers(orgId, eventId, sessionId),
    enabled: canTiers,
  });

  const item = sessions.data?.sessions.find((s) => s.id === sessionId);
  const head = summary.data?.session;
  const tz = head?.venue_timezone ?? null;
  const startAt = head?.start_at ?? item?.start_at ?? null;

  if (summary.isLoading || sessions.isLoading) return <Loading />;
  if (head === undefined && item === undefined) {
    return <ErrorBox error={summary.error ?? sessions.error} onRetry={() => void summary.refetch()} />;
  }

  return (
    <article aria-labelledby="session-title">
      <p>
        <Link to="/o/$orgId/events/$eventId" params={{ orgId, eventId }}>
          {t("common.back")}
        </Link>
      </p>
      {head !== undefined && <p className="muted">{head.event_name}</p>}
      <h1 id="session-title">{startAt !== null ? formatDateTime(startAt, tz, locale) : t("session.title")}</h1>
      <ul className="facts">
        {head?.venue_name !== null && head?.venue_name !== undefined && (
          <li>{t("session.venue", { name: head.venue_name })}</li>
        )}
        {item?.doors_open_at !== null && item?.doors_open_at !== undefined && (
          <li>{t("session.doors", { time: formatDateTime(item.doors_open_at, tz, locale) })}</li>
        )}
        {item !== undefined && <li>{t("session.salesEnd", { time: formatDateTime(item.sales_end_at, tz, locale) })}</li>}
        {(head?.status ?? item?.status) === "cancelled" && (
          <li>
            <Badge tone="bad">{t("event.cancelled")}</Badge>
          </li>
        )}
      </ul>

      <div className="actions">
        {canSales && canSessions && (
          <>
            <DownloadButton
              label="session.csvSales"
              testId="csv-session-sales"
              fetchFile={() => downloadCsv(orgId, "sessionSales", sessionId, locale)}
            />
            <DownloadButton
              label="session.csvSummary"
              testId="csv-session-summary"
              fetchFile={() => downloadCsv(orgId, "sessionSummary", sessionId, locale)}
            />
          </>
        )}
        {canSales && (
          <>
            <Link to="/o/$orgId/orders" params={{ orgId }} search={{ session: sessionId }} className="btn">
              {t("session.orders")}
            </Link>
          </>
        )}
        {canSessions && (
          <DownloadButton
            label="event.sample"
            testId="sample-ticket"
            fetchFile={() => downloadSampleTicket(orgId, sessionId, locale)}
          />
        )}
      </div>

      {startAt !== null && (
        <SessionChange
          orgId={orgId}
          eventId={eventId}
          sessionId={sessionId}
          status={head?.status ?? item?.status}
          startAt={startAt}
          venueId={item?.venue_id ?? null}
          timezone={tz}
        />
      )}

      {summary.data !== undefined && (
        <section aria-labelledby="session-summary">
          <h2 id="session-summary">{t("session.summary")}</h2>
          <SalesFigures
            places={summary.data.places}
            money={summary.data.money}
            entered={summary.data.entered}
            invitations={summary.data.complimentary.tickets}
          />
          <p>
            {t("session.tickets", {
              active: formatNumber(summary.data.tickets.active, locale),
              used: formatNumber(summary.data.tickets.used, locale),
              cancelled: formatNumber(summary.data.tickets.cancelled, locale),
            })}
          </p>
          {summary.data.promos.length > 0 && (
            <>
              <h3>{t("session.promos")}</h3>
              <ul className="facts">
                {summary.data.promos.map((p) => (
                  <li key={`${p.id}-${p.currency}`}>
                    {t("session.promoLine", {
                      code: p.code,
                      orders: formatNumber(p.orders, locale),
                      amount: formatMoney(p.discount, p.currency, locale),
                    })}
                  </li>
                ))}
              </ul>
            </>
          )}
        </section>
      )}
      {summary.error !== null && <ErrorBox error={summary.error} onRetry={() => void summary.refetch()} />}
      {!canSales && <p className="notice">{t("event.noSales")}</p>}

      {canTiers && (
        <section aria-labelledby="session-tiers">
          <h2 id="session-tiers">{t("event.categories")}</h2>
          {tiers.isLoading && <Loading />}
          {tiers.error !== null && <ErrorBox error={tiers.error} onRetry={() => void tiers.refetch()} />}
          {tiers.data !== undefined && <TiersTable tiers={tiers.data.tiers} />}
        </section>
      )}
    </article>
  );
}
