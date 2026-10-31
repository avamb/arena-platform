/**
 * The products of the shop (spec 47, section 6): name, kind, status, price
 * range, stock and the number of pictures. Reads need `shop.read`; the
 * "+ Product" button needs `shop.manage`.
 */
import { useState } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { NoShopAccess, ShopNav } from "@/components/ShopNav";
import { ShopStockCell } from "@/components/ShopStockCell";
import { ShopProductThumb } from "@/components/ShopProductThumb";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { fetchShopSettings, listShopProducts } from "@/lib/api/shopApi";
import { formatMoney, formatNumber } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  PRODUCT_SORTS,
  PRODUCT_STATUSES,
  filterAndSortProducts,
  isProductSort,
  isProductStatus,
  oversold,
  priceRange,
  productKind,
  productTone,
  shelfEmpty,
  stockEditMode,
  stockSum,
  type ProductSort,
  type ShopProductView,
} from "@/lib/shop";
import { useOrg } from "@/screens/OrgLayout";

export interface ShopProductsTableProps {
  orgId: string;
  products: readonly ShopProductView[];
  currency: string;
  /** `media.read`: the first picture can be drawn. */
  canViewMedia?: boolean;
  /** `shop.manage`: the stock can be edited in the row. Without it the stock is only shown. */
  canManage?: boolean;
}

/** The table of products. Pure: it shows what it is given. */
export function ShopProductsTable({ orgId, products, currency, canViewMedia = false, canManage = false }: ShopProductsTableProps) {
  const { t, locale } = useI18n();
  return (
    <div className="table-wrap">
      <table className="table" data-testid="shop-products-table">
        <thead>
          <tr>
            <th scope="col">{t("shop.products.col.title")}</th>
            <th scope="col">{t("shop.products.col.kind")}</th>
            <th scope="col">{t("shop.products.col.status")}</th>
            <th scope="col">{t("shop.products.col.price")}</th>
            <th scope="col">{t("shop.products.col.stock")}</th>
            <th scope="col">{t("shop.products.col.pictures")}</th>
          </tr>
        </thead>
        <tbody>
          {products.map((p) => {
            const range = priceRange(p);
            const stock = stockSum(p);
            return (
              <tr key={p.id} data-testid="shop-product-row">
                <td data-label={t("shop.products.col.title")}>
                  <Link to="/o/$orgId/shop/products/$productId" params={{ orgId, productId: p.id }} search={{}}>
                    {p.title}
                  </Link>
                </td>
                <td data-label={t("shop.products.col.kind")}>{t(`shop.kind.${productKind(p)}` as MessageKey)}</td>
                <td data-label={t("shop.products.col.status")}>
                  <Badge tone={productTone(p.status)}>{isProductStatus(p.status) ? t(`shop.product.status.${p.status}` as MessageKey) : p.status}</Badge>
                  {p.status === "active" && shelfEmpty(p) && (
                    <>
                      {" "}
                      <span data-testid="shop-product-soldout-badge">
                        <Badge tone="bad">{t("shop.product.soldOut")}</Badge>
                      </span>
                    </>
                  )}
                </td>
                <td data-label={t("shop.products.col.price")}>
                  {range === null
                    ? ""
                    : range.min === range.max
                      ? formatMoney(range.min, currency, locale)
                      : t("shop.products.priceRange", { min: formatMoney(range.min, currency, locale), max: formatMoney(range.max, currency, locale) })}
                </td>
                <td data-label={t("shop.products.col.stock")} className="shop-stock-cell">
                  {canManage && stockEditMode(p) !== "none" ? (
                    <ShopStockCell orgId={orgId} product={p} currency={currency} />
                  ) : (
                    <>{stock === null ? t("shop.products.stockNone") : formatNumber(stock, locale)}</>
                  )}
                  {oversold(p) && <> ({t("shop.products.oversold")})</>}
                </td>
                <td data-label={t("shop.products.col.pictures")}>
                  <span className="shop-pictures">
                    {p.images.length > 0 && <ShopProductThumb mediaId={p.images[0].media_id} canView={canViewMedia} alt={p.images[0].alt !== "" ? p.images[0].alt : p.title} />}
                    {(p.images.length > 1 || p.images.length === 0 || !canViewMedia) && (
                      <span className="badge badge-neutral" title={t("shop.products.pictureCount", { count: p.images.length })} data-testid="shop-product-picture-count">
                        {formatNumber(p.images.length, locale)}
                      </span>
                    )}
                  </span>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export function ShopProductsScreen() {
  const { t, locale } = useI18n();
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<ProductSort>("title");
  const { orgId, access } = useOrg();
  const search = useSearch({ from: "/o/$orgId/shop/products" });
  const navigate = useNavigate({ from: "/o/$orgId/shop/products" });
  const canRead = access.can("shop.read");
  const status = search.status;
  const products = useQuery({ queryKey: ["shopProducts", orgId, status ?? ""], queryFn: () => listShopProducts(orgId, status), enabled: canRead });
  const settings = useQuery({ queryKey: ["shopSettings", orgId], queryFn: () => fetchShopSettings(orgId), enabled: canRead });

  if (!canRead) return <NoShopAccess />;
  const all = products.data?.products ?? [];
  const list = filterAndSortProducts(all, query, sort, locale);
  return (
    <section className="shop-page" aria-labelledby="shop-title">
      <h1 id="shop-title">{t("shop.products.title")}</h1>
      <ShopNav orgId={orgId} />
      {search.deleted !== undefined && (
        <p className="notice notice-ok" role="status" data-testid="shop-products-notice">
          {/* The address only says that a delete just happened. Its value is not printed: anybody can write a link. */}
          {t("shop.product.deletedGeneric")}
        </p>
      )}
      <div className="actions">
        {access.can("shop.manage") && (
          <Link to="/o/$orgId/shop/products/new" params={{ orgId }} className="btn btn-primary" data-testid="shop-product-new">
            {t("shop.products.new")}
          </Link>
        )}
        <label htmlFor="shop-products-status">{t("shop.products.filter")}</label>
        <select
          id="shop-products-status"
          value={status ?? ""}
          onChange={(e) => void navigate({ search: { status: isProductStatus(e.target.value) ? e.target.value : undefined } })}
          data-testid="shop-products-status"
        >
          <option value="">{t("shop.products.filterAll")}</option>
          {PRODUCT_STATUSES.map((s) => (
            <option key={s} value={s}>
              {t(`shop.product.status.${s}` as MessageKey)}
            </option>
          ))}
        </select>
        <label htmlFor="shop-products-search">{t("shop.products.search")}</label>
        <input
          id="shop-products-search"
          type="search"
          value={query}
          maxLength={200}
          autoComplete="off"
          onChange={(e) => setQuery(e.target.value)}
          data-testid="shop-products-search"
        />
        <label htmlFor="shop-products-sort">{t("shop.products.sort")}</label>
        <select id="shop-products-sort" value={sort} onChange={(e) => setSort(isProductSort(e.target.value) ? e.target.value : "title")} data-testid="shop-products-sort">
          {PRODUCT_SORTS.map((s) => (
            <option key={s} value={s}>
              {t(`shop.products.sort.${s}` as MessageKey)}
            </option>
          ))}
        </select>
      </div>
      {(products.isLoading || settings.isLoading) && <Loading />}
      {products.error !== null && <ErrorBox error={products.error} onRetry={() => void products.refetch()} />}
      {settings.error !== null && <ErrorBox error={settings.error} onRetry={() => void settings.refetch()} />}
      {products.data !== undefined && all.length === 0 && <Empty>{status === undefined ? t("shop.products.empty") : t("shop.products.emptyFilter")}</Empty>}
      {all.length > 0 && list.length === 0 && <Empty>{t("shop.products.emptySearch")}</Empty>}
      {list.length > 0 && settings.data !== undefined && (
        <>
          <p className="muted" role="status" data-testid="shop-products-count">
            {t("shop.products.count", { count: list.length })}
          </p>
          <ShopProductsTable orgId={orgId} products={list} currency={settings.data.currency} canViewMedia={access.can("media.read")} canManage={access.can("shop.manage")} />
        </>
      )}
    </section>
  );
}
