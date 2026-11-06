/**
 * The shop module line of the billing screen (spec 61, section 5). Read only: the platform connects the
 * module and sets its price, the organization only sees them. Nothing is drawn while the organization
 * has never had the module, and a failed read is silent too (the charges below are the screen's job).
 */
import { useQuery } from "@tanstack/react-query";
import { fetchBillingModules } from "@/lib/api/endpoints";
import { shopModuleLine, type ShopModuleLine } from "@/lib/billingModules";
import { formatDate, formatMoney } from "@/lib/format";
import { useI18n } from "@/lib/i18n";

export function ShopModuleLineView({ line }: { line: ShopModuleLine }) {
  const { t, locale } = useI18n();
  const date = (d: string) => formatDate(d, "UTC", locale);
  const state =
    line.state === "scheduled"
      ? t("billing.shop.scheduled", { date: date(line.since) })
      : line.state === "ended"
        ? t("billing.shop.ended", { date: date(line.until ?? line.since) })
        : line.until !== null
          ? t("billing.shop.activeUntil", { date: date(line.since), until: date(line.until) })
          : t("billing.shop.active", { date: date(line.since) });
  const price =
    line.price.kind === "free"
      ? t("billing.shop.free")
      : line.price.kind === "amount"
        ? t("billing.shop.perMonth", { amount: formatMoney(line.price.minor, line.price.currency, locale) })
        : t("billing.shop.priceList");
  return (
    <section aria-labelledby="billing-shop-title" data-testid="billing-shop-module">
      <h2 id="billing-shop-title">{t("billing.shop.title")}</h2>
      <ul className="cards">
        <li className="card">
          <span className="card-title" data-testid="billing-shop-state">
            {state}
          </span>
          {line.state !== "ended" && (
            <span className="muted" data-testid="billing-shop-price">
              {t("billing.shop.priceLabel")}: {price}
            </span>
          )}
        </li>
      </ul>
      <p className="muted">{t("billing.shop.note")}</p>
    </section>
  );
}

export function BillingShopModule({ orgId }: { orgId: string }) {
  const modules = useQuery({ queryKey: ["billingModules", orgId], queryFn: () => fetchBillingModules(orgId), retry: false });
  const line = shopModuleLine(modules.data);
  if (line === null) return null;
  return <ShopModuleLineView line={line} />;
}
