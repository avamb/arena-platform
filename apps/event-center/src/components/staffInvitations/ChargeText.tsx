/**
 * The billing preview of a CSV file (spec 46, 3.6), as a sentence in the
 * person's language built from the numbers of `charge`. The server's own
 * `text` is English and is never shown. Nothing is rendered when the server
 * could not price the file: it is issued either way.
 */
import { formatMoney, formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { chargeView } from "@/lib/invitationBatch";

export function ChargeText({ charge }: { charge: unknown }) {
  const { t, locale } = useI18n();
  const v = chargeView(charge);
  if (v === null) return null;
  const charged = v.amountMinor > 0 || v.chargedTickets > 0;
  return (
    <div data-testid="csv-charge" data-charged={charged ? "1" : "0"}>
      {charged ? (
        <>
          <p>
            <strong>{t("invBatch.charge.total", { amount: formatMoney(v.amountMinor, v.currency, locale) })}</strong>
          </p>
          {v.lines.length > 0 && (
            <ul className="inv-figures">
              {v.lines.map((l) => (
                <li key={`${l.unitMinor}-${l.tickets}`}>
                  {t("invBatch.charge.line", {
                    count: formatNumber(l.tickets, locale),
                    unit: formatMoney(l.unitMinor, v.currency, locale),
                    amount: formatMoney(l.amountMinor, v.currency, locale),
                  })}
                </li>
              ))}
            </ul>
          )}
        </>
      ) : v.freeLeft !== null ? (
        <p>{t("invBatch.charge.freeLeft", { count: formatNumber(v.freeLeft, locale) })}</p>
      ) : (
        <p>{t("invBatch.charge.free")}</p>
      )}
    </div>
  );
}
