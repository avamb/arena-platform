/**
 * The open/closed switch of the storefront (spec 61, section 3.4 and 5).
 *
 * The organization opens and closes its storefront itself (`shop.manage`: owner and manager). Closing always
 * works. Opening needs saved settings, one active product and a payment method the provider has verified;
 * what is missing comes from the API as `open_blockers` and is listed here in plain words, each with the
 * place to fix it where the web has one.
 *
 * Pure: the screen owns the call and passes the result in.
 */
import { Link } from "@tanstack/react-router";
import { ErrorNotice } from "@/components/ErrorNotice";
import { useI18n } from "@/lib/i18n";
import { blockerKey, blockerTarget, type OpenBlocker } from "@/lib/shopStorefront";

export interface ShopStorefrontProps {
  orgId: string;
  enabled: boolean;
  /** What keeps the storefront from opening (`open_blockers`), unknown codes already dropped. */
  blockers: readonly OpenBlocker[];
  /** `shop.manage`: without it the panel only tells the state. */
  canManage: boolean;
  /** `org.update`: the payment note lives on the organization data screen. */
  canOpenOrgData: boolean;
  busy: boolean;
  error?: unknown;
  onToggle: (enabled: boolean) => void;
}

export function ShopStorefront(p: ShopStorefrontProps) {
  const { t } = useI18n();
  const blocked = !p.enabled && p.blockers.length > 0;
  return (
    <section aria-labelledby="shop-storefront-title" data-testid="shop-storefront">
      <h2 id="shop-storefront-title">{t("shop.settings.f.enabled")}</h2>
      <p data-testid="shop-storefront-status">
        <strong>{p.enabled ? t("shop.settings.f.enabledOn") : t("shop.settings.f.enabledOff")}</strong>
      </p>
      <p className="field-hint">{p.enabled ? t("shop.storefront.hintOpen") : t("shop.storefront.hintClosed")}</p>
      {blocked && (
        <div data-testid="shop-storefront-blockers">
          <p>{t("shop.storefront.blockedIntro")}</p>
          <ul>
            {p.blockers.map((b) => {
              const target = blockerTarget(b);
              return (
                <li key={b} data-testid={`shop-storefront-blocker-${b}`}>
                  {t(blockerKey(b))}
                  {target === "products" && (
                    <>
                      {" "}
                      <Link to="/o/$orgId/shop/products" params={{ orgId: p.orgId }} search={{}} data-testid="shop-storefront-fix-products">
                        {t("shop.storefront.fix.products")}
                      </Link>
                    </>
                  )}
                  {target === "orgData" && p.canOpenOrgData && (
                    <>
                      {" "}
                      <Link to="/o/$orgId/org-data" params={{ orgId: p.orgId }} data-testid="shop-storefront-fix-payments">
                        {t("shop.storefront.fix.payments")}
                      </Link>
                    </>
                  )}
                </li>
              );
            })}
          </ul>
        </div>
      )}
      {p.canManage && (
        <p>
          <button
            type="button"
            className={p.enabled ? "btn" : "btn btn-primary"}
            disabled={p.busy || blocked}
            onClick={() => p.onToggle(!p.enabled)}
            data-testid="shop-storefront-toggle"
          >
            {p.enabled ? t("shop.storefront.close") : t("shop.storefront.open")}
          </button>
        </p>
      )}
      {p.error !== undefined && p.error !== null && <ErrorNotice error={p.error} testId="shop-storefront-error" />}
    </section>
  );
}
