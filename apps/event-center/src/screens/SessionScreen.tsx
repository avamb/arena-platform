import { useState } from "react";
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
import { CategoryCard } from "@/components/CategoryCard";
import { SalesFigures } from "@/components/SalesFigures";
import { SessionSaleTimes } from "@/components/SessionSaleTimes";
import { SessionChange } from "@/components/SessionChange";
import { SessionTickets } from "@/components/SessionTickets";
import { useOrg } from "@/screens/OrgLayout";

function tierPrice(tier: TicketTierItem, locale: string, t: ReturnType<typeof useI18n>["t"]): string {
  if (tier.pricing_mode === "free") return t("tier.free");
  if (tier.pricing_mode === "pwyw") return t("tier.pwyw");
  return formatMoney(tier.current_price ?? tier.price_amount, tier.currency, locale);
}

function TiersTable({
  tiers,
  selectedId,
  onManage,
}: {
  tiers: readonly TicketTierItem[];
  /** The category whose card is open (5-4). */
  selectedId?: string | null;
  /** Present only for a person who may change categories: adds the button column. */
  onManage?: (id: string) => void;
}) {
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
            {onManage !== undefined && <th scope="col">{t("categories.col.actions")}</th>}
          </tr>
        </thead>
        <tbody>
          {[...tiers]
            .sort((a, b) => a.sort_order - b.sort_order)
            .map((tier) => (
              <tr key={tier.id} data-testid="tier-row" data-tier-name={tier.name}>
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
                {onManage !== undefined && (
                  <td data-label={t("categories.col.actions")}>
                    <button
                      type="button"
                      className="btn"
                      aria-pressed={selectedId === tier.id}
                      aria-label={t("categories.manageOne", { name: tier.name })}
                      onClick={() => onManage(tier.id)}
                      data-testid="tier-manage"
                    >
                      {t("categories.manage")}
                    </button>
                  </td>
                )}
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
  const canUpdate = access.can("session.update"); // 5-3
  // 5-4: a person who may change categories gets a card per category.
  const canChangeTiers = access.can("tier.update");
  const [openTier, setOpenTier] = useState<string | null>(null);

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
        {/* S2b: the hall of a seated date (paint with categories, close and open seats, lay out). */}
        {access.can("session.seats.manage") && item !== undefined && item.admission_mode !== "general_admission" && (
          <Link
            to="/o/$orgId/events/$eventId/sessions/$sessionId/seats"
            params={{ orgId, eventId, sessionId }}
            search={{}}
            className="btn"
            data-testid="session-seats-link"
          >
            {t("seats.open")}
          </Link>
        )}
        {/* Spec 46: invitations on chosen seats (and general admission places) of this date. */}
        {access.can("complimentary.read") && (
          <Link
            to="/o/$orgId/events/$eventId/sessions/$sessionId/invitations"
            params={{ orgId, eventId, sessionId }}
            className="btn"
            data-testid="session-invitations-link"
          >
            {t("seatInv.open")}
          </Link>
        )}
        {/* QL-12: a short link and QR code that lead to this date. */}
        {access.can("sales_link.manage") && (
          <Link to="/o/$orgId/links/new" params={{ orgId }} search={{ event: eventId, session: sessionId }} className="btn" data-testid="session-qr-link">
            {t("links.button")}
          </Link>
        )}
      </div>

      {startAt !== null && (
        <SessionChange
          // Another organization, event or session is another card: an open panel, a typed
          // message, a dry run in flight and its answer never carry over (client-side navigation
          // between two such pages keeps this component mounted otherwise).
          key={`${orgId}:${eventId}:${sessionId}`}
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
          {tiers.data !== undefined && (
            <TiersTable
              tiers={tiers.data.tiers}
              selectedId={openTier}
              onManage={canChangeTiers ? (id) => setOpenTier(id) : undefined}
            />
          )}
          {tiers.data !== undefined && !canChangeTiers && (
            <p className="notice" data-testid="categories-read-only">
              {t("categories.readOnly")}
            </p>
          )}
          {tiers.data !== undefined && canChangeTiers && openTier !== null && (
            <CategoryCard
              key={openTier}
              orgId={orgId}
              eventId={eventId}
              sessionId={sessionId}
              tierId={openTier}
              tiers={tiers.data.tiers}
              timezone={tz}
              salesEndAt={item?.sales_end_at ?? null}
              onClose={() => setOpenTier(null)}
            />
          )}
        </section>
      )}

      {/* 5-3 */}
      {canUpdate && item !== undefined && (
        <SessionSaleTimes
          orgId={orgId}
          eventId={eventId}
          sessionId={sessionId}
          startAt={item.start_at}
          salesEndAt={item.sales_end_at}
          doorsOpenAt={item.doors_open_at ?? null}
          timezone={tz}
          zoneUnloaded={summary.data === undefined}
          readOnly={(head?.status ?? item.status) === "cancelled"}
        />
      )}
      {/* M2 */}
      {canSales && <SessionTickets orgId={orgId} sessionId={sessionId} tz={tz} />}
    </article>
  );
}
