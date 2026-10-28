/**
 * The sections of the Shop area: products, orders, settings. Every one needs
 * `shop.read`, so the nav is drawn only on screens that already know the
 * person has it.
 */
import { Link } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

export function ShopNav({ orgId }: { orgId: string }) {
  const { t } = useI18n();
  const active = { "aria-current": "page" as const, className: "tab active" };
  return (
    <nav aria-label={t("shop.nav.label")} className="tabs" data-testid="shop-nav">
      <Link to="/o/$orgId/shop/products" params={{ orgId }} search={{}} className="tab" activeProps={active} data-testid="shop-nav-products">
        {t("shop.nav.products")}
      </Link>
      <Link to="/o/$orgId/shop/orders" params={{ orgId }} search={{}} className="tab" activeProps={active} data-testid="shop-nav-orders">
        {t("shop.nav.orders")}
      </Link>
      <Link to="/o/$orgId/shop/settings" params={{ orgId }} className="tab" activeProps={active} data-testid="shop-nav-settings">
        {t("shop.nav.settings")}
      </Link>
    </nav>
  );
}

/** What a person sees when their role holds no shop access in this organization. */
export function NoShopAccess() {
  const { t } = useI18n();
  const { orgName } = useOrg();
  return (
    <section aria-labelledby="shop-title">
      <h1 id="shop-title">{t("shop.title")}</h1>
      <p className="notice" role="status" data-testid="shop-no-access">
        {t("shop.noAccess", { org: orgName })}
      </p>
    </section>
  );
}

/** A line for a person who can read the shop but not change it. */
export function ShopReadOnlyNote() {
  const { t } = useI18n();
  return (
    <p className="notice" role="status" data-testid="shop-read-only">
      {t("shop.readOnly")}
    </p>
  );
}
