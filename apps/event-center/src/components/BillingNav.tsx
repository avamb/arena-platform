/**
 * The sections of the Billing area: usage, balance and packs, invoices.
 * Every one needs `billing.read`, so the nav is drawn only on screens that
 * already know the person has it.
 *
 * Balance and invoices are hidden while the API says organizer billing is not
 * open yet (`billing.organizer_disabled`, BILLING_ORGANIZER_ENABLED off). The
 * nav reads the same balance query the two screens use, so it costs one
 * request per organization at most.
 */
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { fetchBillingBalance } from "@/lib/api/endpoints";
import { isOrganizerDisabled } from "@/lib/billingAccount";
import { useI18n } from "@/lib/i18n";

/** True once the API has said organizer billing is closed for this organization. */
export function useOrganizerBillingClosed(orgId: string): boolean {
  const balance = useQuery({ queryKey: ["billingBalance", orgId], queryFn: () => fetchBillingBalance(orgId) });
  return isOrganizerDisabled(balance.error);
}

export function BillingNav({ orgId }: { orgId: string }) {
  const { t } = useI18n();
  const closed = useOrganizerBillingClosed(orgId);
  const active = { "aria-current": "page" as const, className: "tab active" };
  return (
    <nav aria-label={t("billing.nav.label")} className="tabs" data-testid="billing-nav">
      <Link to="/o/$orgId/billing" params={{ orgId }} className="tab" activeOptions={{ exact: true }} activeProps={active} data-testid="billing-nav-usage">
        {t("billing.nav.usage")}
      </Link>
      {!closed && (
        <Link to="/o/$orgId/billing/balance" params={{ orgId }} search={{}} className="tab" activeProps={active} data-testid="billing-nav-balance">
          {t("billing.nav.balance")}
        </Link>
      )}
      {!closed && (
        <Link to="/o/$orgId/billing/invoices" params={{ orgId }} className="tab" activeProps={active} data-testid="billing-nav-invoices">
          {t("billing.nav.invoices")}
        </Link>
      )}
    </nav>
  );
}

/** What a person who opens balance or invoices by address sees while organizer billing is closed. */
export function BillingClosedNotice() {
  const { t } = useI18n();
  return (
    <p className="notice" role="status" data-testid="billing-closed">
      {t("billing.closed")}
    </p>
  );
}
