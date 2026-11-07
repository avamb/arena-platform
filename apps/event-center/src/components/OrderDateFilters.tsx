/**
 * The date filters of the orders list (EC-V3): the date of the ORDER and the
 * date of the SESSION (the show), each from-to. A range typed backwards says
 * so and is not sent. A date applies as soon as it is picked: the list is
 * narrowed together with the text of the search, and "Reset" clears the text
 * only.
 */
import { useId } from "react";
import { useI18n } from "@/lib/i18n";
import { NO_DATES, hasDates, type OrderDates } from "@/lib/orderCards";

export interface OrderDateFiltersProps {
  value: OrderDates;
  onChange: (next: OrderDates) => void;
  backwards: boolean;
}

export function OrderDateFilters({ value, onChange, backwards }: OrderDateFiltersProps) {
  const { t } = useI18n();
  const ids = useId();
  const field = (key: keyof OrderDates, label: string, testId: string) => (
    <label className="date-field" htmlFor={`${ids}-${key}`}>
      <span className="small">{label}</span>
      <input
        id={`${ids}-${key}`}
        type="date"
        value={value[key]}
        onChange={(e) => onChange({ ...value, [key]: e.target.value })}
        data-testid={testId}
      />
    </label>
  );
  return (
    <div className="date-filters" data-testid="order-date-filters">
      <fieldset>
        <legend>{t("orders.dates.order")}</legend>
        {field("orderFrom", t("orders.dates.from"), "orders-order-from")}
        {field("orderTo", t("orders.dates.to"), "orders-order-to")}
      </fieldset>
      <fieldset>
        <legend>{t("orders.dates.session")}</legend>
        {field("sessionFrom", t("orders.dates.from"), "orders-session-from")}
        {field("sessionTo", t("orders.dates.to"), "orders-session-to")}
      </fieldset>
      {backwards && (
        <p className="field-error" role="alert" data-testid="orders-dates-backwards">
          {t("orders.dates.backwards")}
        </p>
      )}
      {hasDates(value) && (
        <p>
          <button type="button" className="btn btn-outline" onClick={() => onChange(NO_DATES)} data-testid="orders-dates-clear">
            {t("orders.dates.clear")}
          </button>
        </p>
      )}
    </div>
  );
}
