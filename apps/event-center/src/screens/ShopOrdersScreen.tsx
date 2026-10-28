/**
 * The orders of the shop (spec 47, section 6): filter by status, search by
 * number, e-mail or name, and by period. Newest first, 50 at a time.
 *
 * Only the status goes into the address. The text a person searches for (a
 * buyer's e-mail or name) never does: the address is written by every proxy
 * log (review finding L5 of the ticket orders).
 */
import { useState, type FormEvent } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { NoShopAccess, ShopNav } from "@/components/ShopNav";
import { ShopOrdersTable } from "@/components/ShopOrdersTable";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { listShopOrders } from "@/lib/api/shopApi";
import { formatNumber } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { EMPTY_FILTERS, SHOP_ORDER_STATUSES, isShopOrderStatus, ordersQuery, type ShopOrdersFilters } from "@/lib/shop";
import { useOrg } from "@/screens/OrgLayout";

export const SHOP_ORDERS_PAGE_SIZE = 50;

export function ShopOrdersScreen() {
  const { t, locale } = useI18n();
  const { orgId, access } = useOrg();
  const search = useSearch({ from: "/o/$orgId/shop/orders" });
  const navigate = useNavigate({ from: "/o/$orgId/shop/orders" });
  const canRead = access.can("shop.read");
  const [applied, setApplied] = useState<Omit<ShopOrdersFilters, "status">>({ q: "", from: "", to: "" });
  const [draft, setDraft] = useState(applied);
  const filters: ShopOrdersFilters = { ...EMPTY_FILTERS, ...applied, status: search.status ?? "" };

  const orders = useInfiniteQuery({
    queryKey: ["shopOrders", orgId, filters],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => listShopOrders(orgId, ordersQuery(filters, SHOP_ORDERS_PAGE_SIZE, pageParam)),
    getNextPageParam: (last) => last.next_offset ?? undefined,
    enabled: canRead,
  });

  if (!canRead) return <NoShopAccess />;
  const rows = orders.data?.pages.flatMap((p) => p.orders) ?? [];
  const filtered = filters.status !== "" || filters.q.trim() !== "" || filters.from !== "" || filters.to !== "";

  const onSearch = (e: FormEvent) => {
    e.preventDefault();
    setApplied({ q: draft.q.trim(), from: draft.from, to: draft.to });
  };
  const reset = () => {
    const empty = { q: "", from: "", to: "" };
    setDraft(empty);
    setApplied(empty);
    void navigate({ search: {} });
  };

  return (
    <section aria-labelledby="shop-orders-title">
      <h1 id="shop-orders-title">{t("shop.orders.title")}</h1>
      <ShopNav orgId={orgId} />
      <form className="shop-filters" role="search" onSubmit={onSearch} data-testid="shop-orders-filters">
        <div>
          <label htmlFor="shop-orders-status">{t("shop.orders.f.status")}</label>
          <select
            id="shop-orders-status"
            value={filters.status}
            onChange={(e) => void navigate({ search: { status: isShopOrderStatus(e.target.value) ? e.target.value : undefined } })}
            data-testid="shop-orders-status"
          >
            <option value="">{t("shop.orders.f.all")}</option>
            {SHOP_ORDER_STATUSES.map((s) => (
              <option key={s} value={s}>
                {t(`shop.order.status.${s}` as MessageKey)}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="shop-orders-q">{t("shop.orders.f.search")}</label>
          <input
            id="shop-orders-q"
            type="search"
            value={draft.q}
            maxLength={200}
            autoComplete="off"
            onChange={(e) => setDraft({ ...draft, q: e.target.value })}
            data-testid="shop-orders-q"
          />
        </div>
        <div>
          <label htmlFor="shop-orders-from">{t("shop.orders.f.from")}</label>
          <input id="shop-orders-from" type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} data-testid="shop-orders-from" />
        </div>
        <div>
          <label htmlFor="shop-orders-to">{t("shop.orders.f.to")}</label>
          <input id="shop-orders-to" type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} data-testid="shop-orders-to" />
        </div>
        <div className="shop-filters-actions">
          <button type="submit" className="btn btn-primary" data-testid="shop-orders-apply">
            {t("shop.orders.f.apply")}
          </button>
          {filtered && (
            <button type="button" className="btn" onClick={reset} data-testid="shop-orders-reset">
              {t("shop.orders.f.reset")}
            </button>
          )}
        </div>
      </form>
      {orders.isLoading && <Loading />}
      {orders.error !== null && <ErrorBox error={orders.error} onRetry={() => void orders.refetch()} />}
      {orders.data !== undefined && rows.length === 0 && <Empty>{filtered ? t("shop.orders.emptySearch") : t("shop.orders.empty")}</Empty>}
      {rows.length > 0 && (
        <>
          <ShopOrdersTable orgId={orgId} orders={rows} />
          <p className="muted" role="status" aria-live="polite" data-testid="shop-orders-count">
            {t("shop.orders.count", { shown: formatNumber(rows.length, locale) })}
          </p>
        </>
      )}
      {orders.hasNextPage && (
        <button type="button" className="btn" disabled={orders.isFetchingNextPage} onClick={() => void orders.fetchNextPage()} data-testid="shop-orders-more">
          {t("shop.orders.more")}
        </button>
      )}
    </section>
  );
}
