/**
 * "Commission as a service charge": the platform commission of ONE ticket at
 * a price. An organizer who wants the net price lets the buyer pay the
 * commission as the service charge; this tells how large that charge is.
 * The number comes from the server (`service_fee_minor`), the same rating
 * code that bills the sale. Nothing is computed here.
 */
import { useId, useState, type FormEvent } from "react";
import { useMutation } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { SelectField, TextField } from "@/components/FormField";
import { fetchServiceFeeQuote } from "@/lib/api/endpoints";
import { parseQuotePrice } from "@/lib/billingAccount";
import { formatMoney } from "@/lib/format";
import { useI18n } from "@/lib/i18n";

export function BillingFeeQuote({ orgId, currencies }: { orgId: string; currencies: readonly string[] }) {
  const { t, locale } = useI18n();
  const ids = useId();
  const [currency, setCurrency] = useState(currencies[0] ?? "");
  const [price, setPrice] = useState("");
  const [seated, setSeated] = useState(false);
  const [problem, setProblem] = useState<"empty" | "invalid" | "tooBig" | null>(null);

  const quote = useMutation({
    mutationFn: (priceMinor: number) => fetchServiceFeeQuote(orgId, { currency, priceMinor, seated }),
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    quote.reset();
    const parsed = parseQuotePrice(price, currency);
    if (!parsed.ok) {
      setProblem(parsed.problem);
      return;
    }
    setProblem(null);
    quote.mutate(parsed.priceMinor);
  };

  const q = quote.data;
  return (
    <section aria-labelledby={`${ids}-title`} data-testid="billing-fee">
      <h2 id={`${ids}-title`}>{t("billing.fee.title")}</h2>
      <p className="muted">{t("billing.fee.intro")}</p>
      <form onSubmit={onSubmit} noValidate className="org-form">
        {currencies.length > 1 && (
          <SelectField label={t("billing.fee.currency")} value={currency} onChange={setCurrency} testId="billing-fee-currency">
            {currencies.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </SelectField>
        )}
        <TextField
          label={`${t("billing.fee.price")}${currencies.length === 1 ? ` (${currency})` : ""}`}
          hint={t("billing.fee.priceHint")}
          value={price}
          onChange={(v) => {
            setPrice(v);
            setProblem(null);
          }}
          error={problem === null ? undefined : t(`billing.fee.problem.${problem}`)}
          testId="billing-fee-price"
          maxLength={20}
        />
        <label className="check">
          <input type="checkbox" checked={seated} onChange={(e) => setSeated(e.target.checked)} data-testid="billing-fee-seated" />
          <span>{t("billing.fee.seated")}</span>
        </label>
        <p>
          <button type="submit" className="btn btn-primary" disabled={quote.isPending || currency === ""} data-testid="billing-fee-submit">
            {quote.isPending ? t("billing.fee.working") : t("billing.fee.submit")}
          </button>
        </p>
      </form>

      {quote.error !== null && <ErrorNotice error={quote.error} testId="billing-fee-error" />}
      {q !== undefined && (
        <div className="notice notice-ok" role="status" data-testid="billing-fee-result">
          <p>
            <strong data-testid="billing-fee-amount">{t("billing.fee.result", { amount: formatMoney(q.service_fee_minor, q.currency, locale) })}</strong>
          </p>
          {q.service_fee_minor === 0 ? (
            <p>{t("billing.fee.free")}</p>
          ) : (
            <p>{t("billing.fee.explain", { version: q.price_list_version })}</p>
          )}
          {q.pricing_class === "low_price" && <p>{t("billing.fee.lowPrice")}</p>}
          {q.fee_mode === "buyer_pays" || q.fee_mode === "organizer_pays" ? <p>{t(`billing.fee.mode.${q.fee_mode}`)}</p> : null}
          {q.ticket_currency !== q.currency && <p>{t("billing.fee.otherCurrency", { ticket: q.ticket_currency, currency: q.currency })}</p>}
        </div>
      )}
    </section>
  );
}
