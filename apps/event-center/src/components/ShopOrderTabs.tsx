/**
 * The quick lists over the shop's orders (A-08): All, To ship (N), Unpaid (N), Shipped, Refunds. The numbers
 * come from one request (`GET .../shop/orders/counts`); while they are unknown a tab shows its name alone.
 * The tab that is open says so (`aria-pressed`), and every tab is a 44 px touch target.
 */
import { useI18n, type MessageKey } from "@/lib/i18n";
import { formatNumber } from "@/lib/format";
import type { ShopOrderCounts } from "@/lib/api/types";
import { ORDER_TABS, tabCount, type OrderTab } from "@/lib/shop";

export function ShopOrderTabs({ active, counts, onPick }: { active: OrderTab; counts: ShopOrderCounts | undefined; onPick: (tab: OrderTab) => void }) {
  const { t, locale } = useI18n();
  return (
    <div className="tabs" role="group" aria-label={t("shop.orders.tabs.label")} data-testid="shop-orders-tabs">
      {ORDER_TABS.map((k) => {
        const n = tabCount(k, counts);
        const label = t(`shop.orders.tab.${k}` as MessageKey);
        return (
          <button key={k} type="button" className={k === active ? "tab active" : "tab"} aria-pressed={k === active} onClick={() => onPick(k)} data-testid={`shop-orders-tab-${k}`}>
            {n === null ? label : t("shop.orders.tabCount", { label, count: formatNumber(n, locale) })}
          </button>
        );
      })}
    </div>
  );
}
