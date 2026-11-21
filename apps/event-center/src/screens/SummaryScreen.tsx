import { useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { OrgSummaryView } from "@/components/OrgSummaryView";
import { DownloadButton, ErrorBox, Loading } from "@/components/ui";
import { downloadAccountingXlsx, fetchOrgSummary } from "@/lib/api/endpoints";
import { todayInZone } from "@/lib/dates";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { RANGE_PRESETS, activePreset, presetRange, rangeIsBackwards, type DayRange, type RangePreset } from "@/lib/orgSummary";
import { useOrg } from "@/screens/OrgLayout";

const PRESET_LABEL: Record<RangePreset, MessageKey> = {
  month: "summary.preset.month",
  last: "summary.preset.last",
  quarter: "summary.preset.quarter",
  all: "summary.preset.all",
};

/**
 * "Summary", the fourth item of the bottom bar (spec 62 §12.1, EC-V1 of §12.2):
 * the whole organization for the sessions that start in a date range, a card per
 * session and currency, and the accountant's Excel file (EC-V2) for the same
 * range.
 */
export function SummaryScreen() {
  const { t, locale } = useI18n();
  const { orgId, access } = useOrg();
  const canRead = access.can("order.read");
  const today = todayInZone(undefined);
  const [range, setRange] = useState<DayRange>(() => presetRange("month", today));
  const ids = useId();
  const backwards = rangeIsBackwards(range);
  const query = {
    from: range.from === "" ? undefined : range.from,
    to: range.to === "" ? undefined : range.to,
  };
  const summary = useQuery({
    queryKey: ["orgSummary", orgId, query.from ?? "", query.to ?? ""],
    queryFn: () => fetchOrgSummary(orgId, query),
    enabled: canRead && !backwards,
  });
  const preset = activePreset(range, today);

  return (
    <section aria-labelledby="summary-title">
      <h1 id="summary-title">{t("summary.title")}</h1>
      {!canRead && (
        <p className="notice" data-testid="summary-no-access">
          {t("summary.noAccess")}
        </p>
      )}
      {canRead && (
        <>
          <fieldset className="scope" data-testid="summary-range">
            <legend>{t("summary.range")}</legend>
            <div role="group">
              {RANGE_PRESETS.map((p) => (
                <button
                  key={p}
                  type="button"
                  className={preset === p ? "btn btn-primary" : "btn"}
                  aria-pressed={preset === p}
                  data-testid={"summary-preset-" + p}
                  onClick={() => setRange(presetRange(p, today))}
                >
                  {t(PRESET_LABEL[p])}
                </button>
              ))}
            </div>
            <label htmlFor={ids + "-from"}>{t("summary.from")}</label>
            <input
              id={ids + "-from"}
              type="date"
              value={range.from}
              data-testid="summary-from"
              onChange={(e) => setRange({ ...range, from: e.target.value })}
            />
            <label htmlFor={ids + "-to"}>{t("summary.to")}</label>
            <input
              id={ids + "-to"}
              type="date"
              value={range.to}
              aria-invalid={backwards}
              data-testid="summary-to"
              onChange={(e) => setRange({ ...range, to: e.target.value })}
            />
          </fieldset>
          <p>
            <DownloadButton
              label="summary.download"
              testId="xlsx-accounting"
              variant="primary"
              fetchFile={() => downloadAccountingXlsx(orgId, query, locale)}
            />
          </p>
          {summary.isLoading && <Loading />}
          {summary.error !== null && summary.data === undefined && <ErrorBox error={summary.error} onRetry={() => void summary.refetch()} />}
          {summary.data !== undefined && <OrgSummaryView orgId={orgId} summary={summary.data} />}
        </>
      )}
    </section>
  );
}
