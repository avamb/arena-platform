/**
 * The products of the shop (spec 47, section 6): name, kind, status, price
 * range, stock and the number of pictures. Reads need `shop.read`; the
 * "+ Product" button needs `shop.manage`.
 */
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { NoShopAccess, ShopNav } from "@/components/ShopNav";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { fetchShopSettings, listShopProducts } from "@/lib/api/shopApi";
import { formatMoney, formatNumber } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { PRODUCT_STATUSES, isProductStatus, oversold, priceRange, productKind, productTone, stockSum, type ShopProductView } from "@/lib/shop";
import { useOrg } from "@/screens/OrgLayout";

export interface ShopProductsTableProps {
  orgId: string;
  products: readonly ShopProductView[];
  currency: string;
}

/** The table of products. Pure: it shows what it is given. */
export function ShopProductsTable({ orgId, products, currency }: ShopProductsTableProps) {
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
                </td>
                <td data-label={t("shop.products.col.price")}>
                  {range === null
                    ? ""
                    : range.min === range.max
                      ? formatMoney(range.min, currency, locale)
                      : t("shop.products.priceRange", { min: formatMoney(range.min, currency, locale), max: formatMoney(range.max, currency, locale) })}
                </td>
                <td data-label={t("shop.products.col.stock")}>
                  {stock === null ? t("shop.products.stockNone") : formatNumber(stock, locale)}
                  {oversold(p) && <> ({t("shop.products.oversold")})</>}
                </td>
                <td data-label={t("shop.products.col.pictures")}>{formatNumber(p.images.length, locale)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export function ShopProductsScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const search = useSearch({ from: "/o/$orgId/shop/products" });
  const navigate = useNavigate({ from: "/o/$orgId/shop/products" });
  const canRead = access.can("shop.read");
  const status = search.status;
  const products = useQuery({ queryKey: ["shopProducts", orgId, status ?? ""], queryFn: () => listShopProducts(orgId, status), enabled: canRead });
  const settings = useQuery({ queryKey: ["shopSettings", orgId], queryFn: () => fetchShopSettings(orgId), enabled: canRead });

  if (!canRead) return <NoShopAccess />;
  const list = products.data?.products ?? [];
  return (
    <section aria-labelledby="shop-title">
      <h1 id="shop-title">{t("shop.products.title")}</h1>
      <ShopNav orgId={orgId} />
      {search.deleted !== undefined && (
        <p className="notice notice-ok" role="status" data-testid="shop-products-notice">
          {t("shop.product.deleted", { title: search.deleted })}
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
      </div>
      {(products.isLoading || settings.isLoading) && <Loading />}
      {products.error !== null && <ErrorBox error={products.error} onRetry={() => void products.refetch()} />}
      {settings.error !== null && <ErrorBox error={settings.error} onRetry={() => void settings.refetch()} />}
      {products.data !== undefined && list.length === 0 && <Empty>{status === undefined ? t("shop.products.empty") : t("shop.products.emptyFilter")}</Empty>}
      {list.length > 0 && settings.data !== undefined && (
        <>
          <p className="muted" role="status" data-testid="shop-products-count">
            {t("shop.products.count", { count: list.length })}
          </p>
          <ShopProductsTable orgId={orgId} products={list} currency={settings.data.currency} />
        </>
      )}
    </section>
  );
}
