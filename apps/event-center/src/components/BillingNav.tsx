/**
 * The sections of the Billing area: usage, balance and packs, invoices.
 * Every one needs `billing.read`, so the nav is drawn only on screens that
 * already know the person has it.
 */
import { Link } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";

export function BillingNav({ orgId }: { orgId: string }) {
  const { t } = useI18n();
  const active = { "aria-current": "page" as const, className: "tab active" };
  return (
    <nav aria-label={t("billing.nav.label")} className="tabs" data-testid="billing-nav">
      <Link to="/o/$orgId/billing" params={{ orgId }} className="tab" activeOptions={{ exact: true }} activeProps={active} data-testid="billing-nav-usage">
        {t("billing.nav.usage")}
      </Link>
      <Link to="/o/$orgId/billing/balance" params={{ orgId }} search={{}} className="tab" activeProps={active} data-testid="billing-nav-balance">
        {t("billing.nav.balance")}
      </Link>
      <Link to="/o/$orgId/billing/invoices" params={{ orgId }} className="tab" activeProps={active} data-testid="billing-nav-invoices">
        {t("billing.nav.invoices")}
      </Link>
    </nav>
  );
}
