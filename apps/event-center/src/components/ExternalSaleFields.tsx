/**
 * The pieces of "Who sells the tickets?" (spec 63, L-3), shared by the wizard
 * (a date of a new event) and the date's own card:
 *
 *  - `SalesModeChoice`: "We do, on the site and in the widget" or "A partner";
 *  - `ExternalFields`: the partner box office (link, name, optional lowest
 *    price and its currency);
 *  - `ExternalLine`: the one line a partner date shows where its sales would be.
 *
 * Nothing here decides a rule: lib/externalSale.ts does.
 */
import { useId } from "react";
import { TextField } from "@/components/FormField";
import type { ExternalField, ExternalForm, SalesMode } from "@/lib/externalSale";
import { useI18n } from "@/lib/i18n";

export function SalesModeChoice({
  value,
  onChange,
  disabled = false,
  testId,
}: {
  value: SalesMode;
  onChange: (mode: SalesMode) => void;
  disabled?: boolean;
  /** Prefix of the test ids: `<prefix>-platform`, `<prefix>-external`. */
  testId: string;
}) {
  const { t } = useI18n();
  const name = useId();
  return (
    <div role="radiogroup" aria-label={t("external.who")} data-testid={testId}>
      <p className="field-label">{t("external.who")}</p>
      {(["platform", "external"] as const).map((mode) => (
        <label key={mode} className="check">
          <input
            type="radio"
            name={name}
            checked={value === mode}
            disabled={disabled}
            onChange={() => onChange(mode)}
            data-testid={`${testId}-${mode}`}
          />
          <span>{t(mode === "platform" ? "external.mode.platform" : "external.mode.external")}</span>
        </label>
      ))}
    </div>
  );
}

export function ExternalFields({
  value,
  onChange,
  onBlur,
  errorOf,
  testId,
}: {
  value: ExternalForm;
  onChange: (field: ExternalField, value: string) => void;
  /** The person left a field (a field shows its error once it was reached). */
  onBlur?: (field: ExternalField) => void;
  errorOf: (field: ExternalField) => string | undefined;
  /** Prefix of the test ids: `<prefix>-url`, `-seller`, `-price`, `-currency`. */
  testId: string;
}) {
  const { t } = useI18n();
  const field = (f: ExternalField, label: Parameters<typeof t>[0], hint: Parameters<typeof t>[0], extra: { inputMode?: "decimal"; maxLength?: number } = {}) => (
    <TextField
      label={t(label)}
      hint={t(hint)}
      value={value[f]}
      onChange={(v) => onChange(f, v)}
      onBlur={onBlur === undefined ? undefined : () => onBlur(f)}
      error={errorOf(f)}
      testId={`${testId}-${f}`}
      {...extra}
    />
  );
  return (
    <div data-testid={testId}>
      {field("url", "external.url.label", "external.url.hint")}
      {field("seller", "external.seller.label", "external.seller.hint")}
      {field("price", "external.price.label", "external.price.hint", { inputMode: "decimal" })}
      {field("currency", "external.currency.label", "external.currency.hint", { maxLength: 3 })}
    </div>
  );
}

/**
 * "Sold by a partner: Eventim" and a link to the box office (event screen, session card). The link is the
 * organizer's own address: opened in a new tab, without the opener, and only when it is https.
 */
export function ExternalLabel({
  external,
  testId = "external-label",
}: {
  external: { url: string; seller_name: string };
  testId?: string;
}) {
  const { t } = useI18n();
  return (
    <span className="external-label" data-testid={testId}>
      <span data-testid={`${testId}-name`}>{t("external.label", { name: external.seller_name })}</span>
      {external.url.startsWith("https://") && (
        <>
          {" "}
          <a href={external.url} target="_blank" rel="noopener noreferrer" data-testid={`${testId}-link`}>
            {t("external.open")}
          </a>
        </>
      )}
    </span>
  );
}

/** The one line a partner date shows instead of sales, refunds, guests, orders or categories. */
export function ExternalLine({ testId = "external-line" }: { testId?: string }) {
  const { t } = useI18n();
  return (
    <p className="notice" role="status" data-testid={testId}>
      {t("external.line")}
    </p>
  );
}
