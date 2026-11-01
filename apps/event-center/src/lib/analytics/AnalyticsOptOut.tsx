import { useId } from "react";
import { useI18n } from "@/lib/i18n";
import { analyticsAvailable } from "@/lib/analytics/client";
import { useAnalyticsOptOut } from "@/lib/analytics/optOut";

/** The checkbox itself, for tests and for any screen that wants it. `checked` means "help improve the service". */
export function AnalyticsOptOutView({ checked, onChange }: { checked: boolean; onChange: (checked: boolean) => void }) {
  const { t } = useI18n();
  const id = useId();
  return (
    <span className="analytics-opt">
      <input
        id={id}
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        aria-describedby={`${id}-hint`}
        data-testid="analytics-opt"
      />
      <label htmlFor={id}>{t("analytics.optOut.label")}</label>
      <span id={`${id}-hint`} className="visually-hidden">
        {t("analytics.optOut.hint")}
      </span>
    </span>
  );
}

/** Shown only in a build that collects (VITE_ANALYTICS=1): otherwise there is nothing to opt out of. */
export function AnalyticsOptOut() {
  const [optedOut, setOptedOut] = useAnalyticsOptOut();
  if (!analyticsAvailable()) return null;
  return <AnalyticsOptOutView checked={!optedOut} onChange={(on) => setOptedOut(!on)} />;
}
