import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { fetchSessionTickets } from "@/lib/api/endpoints";
import type { SessionTicketItem } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { isAdmitted } from "@/lib/scans";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { ScanOrigin } from "@/components/TicketScans";

export const SESSION_TICKETS_PAGE_SIZE = 50;

export type EntryFilter = "all" | "notEntered" | "entered";
const FILTERS: readonly EntryFilter[] = ["all", "notEntered", "entered"];

/** The API's `entered` parameter for a filter: absent, false or true. */
export function enteredParam(filter: EntryFilter): boolean | undefined {
  if (filter === "notEntered") return false;
  if (filter === "entered") return true;
  return undefined;
}

function TicketRow({ item, orgId, tz }: { item: SessionTicketItem; orgId: string; tz: string | null }) {
  const { t, locale } = useI18n();
  const scan = item.last_scan;
  return (
    <li className="card" data-testid="session-ticket" data-entered={String(item.entered)}>
      <p className="card-title">
        {t("ticket.number", { number: item.system_ticket_id })}
        {item.tier_name !== null && <> · {item.tier_name}</>}
        {item.seat_label !== null && <> · {item.seat_label}</>}
      </p>
      <p className="card-meta">
        {item.order_id !== null && item.order_number !== null ? (
          <Link to="/o/$orgId/orders/$orderId" params={{ orgId, orderId: item.order_id }}>
            {t("sessionTickets.order", { number: item.order_number })}
          </Link>
        ) : (
          <span>{t("sessionTickets.invitation")}</span>
        )}
        {item.buyer_name !== null && <span>{item.buyer_name}</span>}
        {item.buyer_email !== null && <span className="muted">{item.buyer_email}</span>}
      </p>
      <p className="card-meta">
        {item.entered && item.entered_at !== null ? (
          <Badge tone="ok">{t("sessionTickets.enteredAt", { time: formatDateTime(item.entered_at, tz, locale) })}</Badge>
        ) : (
          <Badge tone="neutral">{t("sessionTickets.filter.notEntered")}</Badge>
        )}
        {scan !== null && (
          <span data-testid="session-ticket-scan">
            {t("sessionTickets.lastScan", {
              result: t(isAdmitted(scan) ? "scan.admitted" : "scan.denied"),
              time: formatDateTime(scan.scanned_at, tz, locale),
            })}
            {" · "}
            <ScanOrigin scan={scan} />
          </span>
        )}
      </p>
    </li>
  );
}

/**
 * The tickets of one session with their entry at the door (M2), filterable to
 * "has not come yet". It reads the buyer's name and e-mail, so the screen
 * shows it only to a caller who holds `order.read`.
 */
export function SessionTickets({ orgId, sessionId, tz }: { orgId: string; sessionId: string; tz: string | null }) {
  const { t, locale } = useI18n();
  const [filter, setFilter] = useState<EntryFilter>("all");

  const list = useInfiniteQuery({
    queryKey: ["sessionTickets", orgId, sessionId, filter],
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      fetchSessionTickets(orgId, sessionId, {
        entered: enteredParam(filter),
        limit: SESSION_TICKETS_PAGE_SIZE,
        offset: pageParam,
      }),
    getNextPageParam: (last, all) => (last.has_more ? all.reduce((n, p) => n + p.items.length, 0) : undefined),
  });
  const items = list.data?.pages.flatMap((p) => p.items) ?? [];
  const total = list.data?.pages[0]?.total;

  return (
    <section aria-labelledby="session-tickets-title" data-testid="session-tickets">
      <h2 id="session-tickets-title">{t("sessionTickets.title")}</h2>
      <div className="tabs" role="group" aria-label={t("sessionTickets.filter")}>
        {FILTERS.map((f) => (
          <button
            key={f}
            type="button"
            className={filter === f ? "tab active" : "tab"}
            aria-pressed={filter === f}
            data-testid={`session-tickets-filter-${f}`}
            onClick={() => setFilter(f)}
          >
            {t(`sessionTickets.filter.${f}` as MessageKey)}
          </button>
        ))}
      </div>
      {total !== undefined && (
        <p className="muted" data-testid="session-tickets-total">
          {t("sessionTickets.total", { count: new Intl.NumberFormat(locale).format(total) })}
        </p>
      )}
      {list.isLoading && <Loading />}
      {list.error !== null && <ErrorBox error={list.error} onRetry={() => void list.refetch()} />}
      {list.data !== undefined && items.length === 0 && (
        <Empty>{t(filter === "notEntered" ? "sessionTickets.empty.notEntered" : "sessionTickets.empty.other")}</Empty>
      )}
      {items.length > 0 && (
        <ul className="cards">
          {items.map((item) => (
            <TicketRow key={item.ticket_id} item={item} orgId={orgId} tz={tz} />
          ))}
        </ul>
      )}
      {list.hasNextPage && (
        <p>
          <button
            type="button"
            className="btn"
            disabled={list.isFetchingNextPage}
            onClick={() => void list.fetchNextPage()}
            data-testid="session-tickets-more"
          >
            {t("sessionTickets.more")}
          </button>
        </p>
      )}
    </section>
  );
}
