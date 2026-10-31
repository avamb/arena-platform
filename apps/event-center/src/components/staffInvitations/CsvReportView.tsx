/**
 * The check table of a CSV upload (spec 46, 4.1): the totals, the problems row
 * by row with the line number of the file, the guests as the validation sees
 * them and the billing preview. Used for the answer of a dry run and for the
 * report a refused real upload carries.
 */
import { ChargeText } from "@/components/staffInvitations/ChargeText";
import { formatNumber } from "@/lib/format";
import type { InvitationBatchReport } from "@/lib/api/types";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { issueField, issueTextKey, shownIssues, splitIssues } from "@/lib/invitationBatch";

const DELIVERY_KEY: Record<string, MessageKey> = {
  guest_email: "invBatch.report.delivery.guest_email",
  my_email: "invBatch.report.delivery.my_email",
  pdf_only: "invBatch.report.delivery.pdf_only",
};

export function CsvReportView({ report }: { report: InvitationBatchReport }) {
  const { t, locale } = useI18n();
  const n = (v: number) => formatNumber(v, locale);
  const { file, rows } = splitIssues(report);
  const { shown, hidden } = shownIssues(rows);
  const bad = report.errors.length > 0 || !report.valid;
  const issues = [...file, ...shown];

  return (
    <section aria-labelledby="csv-report-title" data-testid="csv-report" data-valid={bad ? "0" : "1"}>
      <h3 id="csv-report-title">{t("invBatch.report.title")}</h3>
      {bad ? (
        <p className="notice notice-bad" role="status" data-testid="csv-report-summary">
          {t("invBatch.report.bad", { count: n(report.errors.length) })}
        </p>
      ) : (
        <p className="notice notice-ok" role="status" data-testid="csv-report-summary">
          {t("invBatch.report.ok", { guests: n(report.guests), places: n(report.places) })}
        </p>
      )}
      <ul className="inv-figures" data-testid="csv-report-figures">
        <li>{t("invBatch.report.rows", { count: n(report.rows) })}</li>
        <li>{t("invBatch.report.guests", { count: n(report.guests) })}</li>
        <li>{t("invBatch.report.places", { count: n(report.places) })}</li>
        {report.seats > 0 && <li>{t("invBatch.report.seats", { count: n(report.seats) })}</li>}
        {report.ga_places > 0 && <li>{t("invBatch.report.ga", { count: n(report.ga_places) })}</li>}
        {report.from_reserve > 0 && <li data-testid="csv-report-reserve">{t("invBatch.report.reserve", { count: n(report.from_reserve) })}</li>}
      </ul>
      {!bad && <ChargeText charge={report.charge} />}

      {issues.length > 0 && (
        <div className="table-wrap">
          <table className="table" data-testid="csv-problems">
            <caption>{t("invBatch.report.issues")}</caption>
            <thead>
              <tr>
                <th scope="col">{t("invBatch.report.col.line")}</th>
                <th scope="col">{t("invBatch.report.col.column")}</th>
                <th scope="col">{t("invBatch.report.col.problem")}</th>
              </tr>
            </thead>
            <tbody>
              {issues.map((i, k) => (
                <tr key={`${i.row}-${i.field}-${i.code}-${k}`} data-testid="csv-problem" data-line={i.row} data-code={i.code}>
                  <td data-label={t("invBatch.report.col.line")}>{i.row > 0 ? i.row : t("invBatch.report.wholeFile")}</td>
                  <td data-label={t("invBatch.report.col.column")}>{issueField(i.field) || "—"}</td>
                  <td data-label={t("invBatch.report.col.problem")}>{t(issueTextKey(i.code))}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {hidden > 0 && (
        <p className="muted" data-testid="csv-problems-more">
          {t("invBatch.report.more", { count: n(hidden) })}
        </p>
      )}

      {report.preview.length > 0 && (
        <div className="table-wrap">
          <table className="table" data-testid="csv-preview">
            <caption>{t("invBatch.report.preview")}</caption>
            <thead>
              <tr>
                <th scope="col">{t("invBatch.report.col.guest")}</th>
                <th scope="col">{t("invBatch.report.col.name")}</th>
                <th scope="col">{t("invBatch.report.col.email")}</th>
                <th scope="col">{t("invBatch.report.col.delivery")}</th>
                <th scope="col">{t("invBatch.report.col.places")}</th>
              </tr>
            </thead>
            <tbody>
              {report.preview.map((g) => (
                <tr key={g.guest} data-testid="csv-preview-guest">
                  <td data-label={t("invBatch.report.col.guest")}>{g.guest}</td>
                  <td data-label={t("invBatch.report.col.name")}>{g.name}</td>
                  <td data-label={t("invBatch.report.col.email")}>{g.email === "" ? t("invBatch.report.noEmail") : g.email}</td>
                  <td data-label={t("invBatch.report.col.delivery")}>{DELIVERY_KEY[g.delivery] !== undefined ? t(DELIVERY_KEY[g.delivery]) : g.delivery}</td>
                  <td data-label={t("invBatch.report.col.places")}>{n(g.places)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
