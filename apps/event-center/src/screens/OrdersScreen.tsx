import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { listOrders, type OrderTab } from "@/lib/api/endpoints";
import type { OrderListItem } from "@/lib/api/types";
import { formatDateTime, formatMoney, formatNumber } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { ORDER_STATUSES, orderNumber, orderTone } from "@/lib/orders";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
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

function OrderRow({ orgId, order }: { orgId: string; order: OrderListItem }) {
  const { locale } = useI18n();
  return (
    <li className="card" data-testid="order-row">
      <Link to="/o/$orgId/orders/$orderId" params={{ orgId, orderId: order.id }} className="card-link">
        <span className="card-title">
          {orderNumber(order.system_id)} · {formatMoney(order.total, order.currency, locale)}
        </span>
        <span className="card-meta">
          <OrderStatusBadge status={order.status} />
          {order.buyer_name !== null && <span>{order.buyer_name}</span>}
        </span>
        <span className="muted">
          {order.event_name} · {formatDateTime(order.session_start_at, order.session_timezone, locale)}
        </span>
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
  const [draft, setDraft] = useState(search.q ?? "");
  useEffect(() => setDraft(search.q ?? ""), [search.q]);
  const events = useEventsList(orgId);
  const scopeEvent = search.event !== undefined ? events.data?.events.find((e) => e.id === search.event) : undefined;

  const orders = useInfiniteQuery({
    queryKey: ["orders", orgId, tab, search.q ?? "", search.event ?? "", search.session ?? ""],
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      listOrders(orgId, {
        tab,
        q: search.q,
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
    void navigate({ search: (prev) => ({ ...prev, q: q.length > 0 ? q : undefined }) });
  };

  return (
    <section aria-labelledby="orders-title">
      <h1 id="orders-title">{t("orders.title")}</h1>
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
      <form className="search-form" role="search" onSubmit={onSearch}>
        <label htmlFor="orders-search" className="visually-hidden">
          {t("orders.searchLabel")}
        </label>
        <input
          id="orders-search"
          type="search"
          className="search"
          placeholder={t("orders.search")}
          value={draft}
          maxLength={200}
          onChange={(e) => setDraft(e.target.value)}
        />
        <button type="submit" className="btn">
          {t("orders.searchButton")}
        </button>
      </form>
      {(search.event !== undefined || search.session !== undefined) && (
        <p className="scope">
          {search.session !== undefined && firstRow !== undefined
            ? t("orders.scopeSession", {
                date: formatDateTime(firstRow.session_start_at, firstRow.session_timezone, locale),
              })
            : t("orders.scopeEvent", { name: scopeEvent?.name ?? firstRow?.event_name ?? "…" })}{" "}
          <Link to="/o/$orgId/orders" params={{ orgId }} search={{ tab: search.tab, q: search.q }}>
            {t("orders.clearScope")}
          </Link>
        </p>
      )}
      {orders.isLoading && <Loading />}
      {orders.error !== null && <ErrorBox error={orders.error} onRetry={() => void orders.refetch()} />}
      {orders.data !== undefined && rows.length === 0 && (
        <Empty>{search.q !== undefined ? t("orders.emptySearch", { query: search.q }) : t("orders.empty")}</Empty>
      )}
      <ul className="cards" data-testid="order-list">
        {rows.map((o) => (
          <OrderRow key={o.id} orgId={orgId} order={o} />
        ))}
      </ul>
      {rows.length > 0 && (
        <p className="muted" aria-live="polite">
          {t("orders.count", { shown: formatNumber(rows.length, locale), total: formatNumber(total, locale) })}
        </p>
      )}
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
