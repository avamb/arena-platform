import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { listOrders, type OrderTab } from "@/lib/api/endpoints";
import type { OrderListItem } from "@/lib/api/types";
import { formatDateTime, formatMoney, formatNumber } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { ORDER_STATUSES, orderNumber, orderTone } from "@/lib/orders";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { rememberOrderSearch, rememberedOrderSearch } from "@/lib/orderSearchMemory";
import { useOrg } from "@/screens/OrgLayout";
import { useEventsList } from "@/screens/EventsScreen";

export const ORDERS_PAGE_SIZE = 20;

export function statusKey(status: string): MessageKey | null {
  return (ORDER_STATUSES as readonly string[]).includes(status) ? (`status.${status}` as MessageKey) : null;
}

export function OrderStatusBadge({ status }: { status: string }) {
  const { t } = useI18n();
  const key = statusKey(status);
  return <Badge tone={orderTone(status)}>{key !== null ? t(key) : status}</Badge>;
}

/**
 * One order of the list as a dense row (owner decision 2026-10-30): number,
 * status and total, then the buyer, then the event and session. From 900 px
 * the same cells line up under the column header.
 */
function OrderRow({ orgId, order }: { orgId: string; order: OrderListItem }) {
  const { t, locale } = useI18n();
  return (
    <li className="order-row" data-testid="order-row">
      <Link to="/o/$orgId/orders/$orderId" params={{ orgId, orderId: order.id }} className="order-row-link">
        <span className="or-num">{orderNumber(order.system_id)}</span>
        <span className="or-status">
          <OrderStatusBadge status={order.status} />
          {order.letter_failed && <Badge tone="bad">{t("orders.letterFailed")}</Badge>}
        </span>
        <span className="or-buyer">
          {order.buyer_name !== null && <span>{order.buyer_name}</span>}
          {order.buyer_email !== null && <span className="muted">{order.buyer_email}</span>}
        </span>
        <span className="or-event">
          <span>{order.event_name}</span>
          <span className="muted">{formatDateTime(order.session_start_at, order.session_timezone, locale)}</span>
        </span>
        <span className="or-total">{formatMoney(order.total, order.currency, locale)}</span>
      </Link>
    </li>
  );
}

const TABS: readonly OrderTab[] = ["recent", "paid", "unpaid"];

export function OrdersScreen() {
  const { t, locale } = useI18n();
  const { orgId } = useOrg();
  const search = useSearch({ from: "/o/$orgId/orders" });
  const navigate = useNavigate({ from: "/o/$orgId/orders" });
  const tab: OrderTab = search.tab ?? "recent";
  const [query, setQuery] = useState(() => rememberedOrderSearch(orgId));
  const [draft, setDraft] = useState(query);
  const events = useEventsList(orgId);
  const scopeEvent = search.event !== undefined ? events.data?.events.find((e) => e.id === search.event) : undefined;

  const orders = useInfiniteQuery({
    queryKey: ["orders", orgId, tab, query, search.event ?? "", search.session ?? ""],
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      listOrders(orgId, {
        tab,
        q: query.length > 0 ? query : undefined,
        eventId: search.event,
        sessionId: search.session,
        limit: ORDERS_PAGE_SIZE,
        offset: pageParam,
      }),
    getNextPageParam: (last) => (last.has_more ? last.offset + last.orders.length : undefined),
  });
  const rows = orders.data?.pages.flatMap((p) => p.orders) ?? [];
  const total = orders.data?.pages[0]?.total_count ?? 0;
  const firstRow = rows[0];

  const onSearch = (e: FormEvent) => {
    e.preventDefault();
    const q = draft.trim();
    rememberOrderSearch(orgId, q);
    setQuery(q);
  };
  const onReset = () => {
    rememberOrderSearch(orgId, "");
    setDraft("");
    setQuery("");
  };

  return (
    <section className="dense" aria-labelledby="orders-title">
      <h1 id="orders-title">{t("orders.title")}</h1>
      <form className="search-card" role="search" onSubmit={onSearch}>
        <label htmlFor="orders-search">{t("orders.searchLabel")}</label>
        <div className="search-card-row">
          <span className="search-field">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <circle cx="11" cy="11" r="7" />
              <path d="M21 21l-4.35-4.35" />
            </svg>
            <input
              id="orders-search"
              type="search"
              placeholder={t("orders.searchExample")}
              aria-describedby="orders-search-hint"
              value={draft}
              maxLength={200}
              onChange={(e) => setDraft(e.target.value)}
            />
          </span>
          <button type="submit" className="btn btn-primary">
            {t("orders.searchButton")}
          </button>
          {(query.length > 0 || draft.length > 0) && (
            <button type="button" className="btn btn-outline" onClick={onReset} data-testid="orders-search-reset">
              <span aria-hidden="true">×</span> {t("orders.reset")}
            </button>
          )}
        </div>
        <p id="orders-search-hint" className="muted small">
          {t("orders.search")}
        </p>
      </form>
      <div className="tabs" role="group" aria-label={t("orders.title")}>
        {TABS.map((tb) => (
          <button
            key={tb}
            type="button"
            className={tab === tb ? "tab active" : "tab"}
            aria-pressed={tab === tb}
            onClick={() => void navigate({ search: (prev) => ({ ...prev, tab: tb === "recent" ? undefined : tb }) })}
            data-testid={`orders-tab-${tb}`}
          >
            {t(`orders.tab.${tb}`)}
          </button>
        ))}
      </div>
      {(search.event !== undefined || search.session !== undefined) && (
        <p className="scope">
          {search.session !== undefined && firstRow !== undefined
            ? t("orders.scopeSession", {
                date: formatDateTime(firstRow.session_start_at, firstRow.session_timezone, locale),
              })
            : t("orders.scopeEvent", { name: scopeEvent?.name ?? firstRow?.event_name ?? "…" })}{" "}
          <Link to="/o/$orgId/orders" params={{ orgId }} search={{ tab: search.tab }}>
            {t("orders.clearScope")}
          </Link>
        </p>
      )}
      {orders.isLoading && <Loading />}
      {orders.error !== null && <ErrorBox error={orders.error} onRetry={() => void orders.refetch()} />}
      {orders.data !== undefined && rows.length === 0 && (
        <Empty>{query.length > 0 ? t("orders.emptySearch", { query }) : t("orders.empty")}</Empty>
      )}
      {rows.length > 0 && (
        <p className="muted small" aria-live="polite">
          {t("orders.count", { shown: formatNumber(rows.length, locale), total: formatNumber(total, locale) })}
        </p>
      )}
      {rows.length > 0 && (
        <div className="order-list-head" aria-hidden="true">
          <span>{t("orders.col.order")}</span>
          <span>{t("orders.col.status")}</span>
          <span>{t("orders.col.buyer")}</span>
          <span>{t("orders.col.event")}</span>
          <span className="or-total">{t("orders.col.total")}</span>
        </div>
      )}
      <ul className="order-list" data-testid="order-list">
        {rows.map((o) => (
          <OrderRow key={o.id} orgId={orgId} order={o} />
        ))}
      </ul>
      {orders.hasNextPage && (
        <button
          type="button"
          className="btn"
          disabled={orders.isFetchingNextPage}
          onClick={() => void orders.fetchNextPage()}
        >
          {t("orders.more")}
        </button>
      )}
    </section>
  );
}
