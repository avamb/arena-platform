/**
 * The payment reference of a bank transfer, in big selectable text with a
 * copy button. The person writes it in the transfer, and without it the
 * platform cannot match the payment.
 */
import { useState } from "react";
import { useI18n } from "@/lib/i18n";

export function PaymentReference({ value }: { value: string }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
    } catch {
      /* the text is selectable: the person can copy it by hand */
    }
  };
  return (
    <div className="pay-ref" data-testid="payment-reference-box">
      <p className="muted">{t("billing.result.reference")}</p>
      <p>
        <code className="pay-ref-code mono" data-testid="payment-reference">
          {value}
        </code>
      </p>
      <button type="button" className="btn" onClick={() => void copy()} data-testid="payment-reference-copy">
        {copied ? t("billing.result.copied") : t("billing.result.copy")}
      </button>
      <span role="status" className="visually-hidden">
        {copied ? t("billing.result.copied") : ""}
      </span>
    </div>
  );
}
