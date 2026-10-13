import type { SessionPlaceCounts, SummaryMoneyItem } from "@/lib/api/types";
import { moneyLines, placeFigures } from "@/lib/events";
import { formatMoney, formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";

interface Props {
  places: { seats: SessionPlaceCounts; ga: SessionPlaceCounts };
  money: readonly SummaryMoneyItem[];
  entered?: { used: number; total: number };
  invitations?: number;
  /** Compact: one line per figure group, for list rows. */
  compact?: boolean;
}

/** Sold, free, on hold and money, the bot's card figures. */
export function SalesFigures({ places, money, entered, invitations, compact = false }: Props) {
  const { t, locale } = useI18n();
  const f = placeFigures(places);
  const n = (v: number) => formatNumber(v, locale);
  const lines = moneyLines(money);
  return (
    <ul className={compact ? "figures figures-compact" : "figures"} data-testid="sales-figures">
      <li>{t("figures.sold", { sold: n(f.sold), total: n(f.total) })}</li>
      <li>{t("figures.free", { count: n(f.available) })}</li>
      {f.held > 0 && <li>{t("figures.held", { count: n(f.held) })}</li>}
      {lines.map((m) => (
        <li key={m.currency}>
          {t("figures.paid", { amount: formatMoney(m.paid, m.currency, locale) })}
          {!compact && (
            <>
              {" · "}
              {t("figures.orders", { count: n(m.paid_orders) })}
              {m.refunded > 0 && <> · {t("figures.refunded", { amount: formatMoney(m.refunded, m.currency, locale) })}</>}
              {" · "}
              {t("figures.net", { amount: formatMoney(m.net, m.currency, locale) })}
              {m.pending > 0 && <> · {t("figures.pending", { amount: formatMoney(m.pending, m.currency, locale) })}</>}
            </>
          )}
        </li>
      ))}
      {!compact && entered !== undefined && entered.total > 0 && (
        <li>{t("figures.entered", { used: n(entered.used), total: n(entered.total) })}</li>
      )}
      {!compact && invitations !== undefined && invitations > 0 && (
        <li>{t("figures.invitations", { count: n(invitations) })}</li>
      )}
    </ul>
  );
}
