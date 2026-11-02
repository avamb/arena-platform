/**
 * Shared pieces of the Analytics screens (spec 50, stage A8): the filter bar
 * (surface and period with the previous-period note) and the change badge.
 * State-, query- and router-free except for the bar's own draft inputs, so
 * the Node-only Vitest environment renders them with renderToStaticMarkup.
 */
import { useState, type CSSProperties } from "react";
import {
  PERIOD_PRESETS,
  SURFACE_FILTERS,
  changeDirection,
  changeLabel,
  isValidRange,
  presetRange,
  type AnalyticsQuery,
} from "@/lib/api/analytics";
import { inputStyle, labelStyle, primaryButtonStyle, fieldErrorStyle } from "./onboardingUi";

export type Translate = (key: string, params?: Readonly<Record<string, string | number>>) => string;

const barStyle: CSSProperties = {
  display: "flex",
  gap: 12,
  flexWrap: "wrap",
  alignItems: "flex-end",
  marginBottom: 12,
};

/** The preset (in days) that the query equals, or null for a custom range. */
export function matchingPreset(q: AnalyticsQuery, now: Date): number | null {
  for (const days of PERIOD_PRESETS) {
    const r = presetRange(days, now);
    if (r.from === q.from && r.to === q.to) return days;
  }
  return null;
}

export interface FilterBarProps {
  readonly query: AnalyticsQuery;
  readonly onChange: (q: AnalyticsQuery) => void;
  readonly t: Translate;
  /** Clock for the presets; tests pass a fixed one. */
  readonly now?: Date;
}

const CUSTOM = "custom";

export function FilterBar({ query, onChange, t, now = new Date() }: FilterBarProps): JSX.Element {
  const [from, setFrom] = useState(query.from);
  const [to, setTo] = useState(query.to);
  const [invalid, setInvalid] = useState(false);
  const preset = matchingPreset(query, now);

  const apply = () => {
    if (!isValidRange(from, to)) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    onChange({ ...query, from, to });
  };

  return (
    <div data-testid="analytics-filter">
      <div style={barStyle}>
        <div>
          <label style={labelStyle} htmlFor="analytics-surface">
            {t("analytics.filter.surface")}
          </label>
          <select
            id="analytics-surface"
            style={inputStyle}
            value={query.surface}
            onChange={(e) => onChange({ ...query, surface: e.target.value })}
            data-testid="analytics-surface"
          >
            {SURFACE_FILTERS.map((s) => (
              <option key={s || "all"} value={s}>
                {t(`analytics.surface.${s === "" ? "all" : s}`)}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label style={labelStyle} htmlFor="analytics-period">
            {t("analytics.filter.period")}
          </label>
          <select
            id="analytics-period"
            style={inputStyle}
            value={preset === null ? CUSTOM : String(preset)}
            onChange={(e) => {
              if (e.target.value === CUSTOM) return;
              const r = presetRange(Number(e.target.value), now);
              setFrom(r.from);
              setTo(r.to);
              setInvalid(false);
              onChange({ ...query, ...r });
            }}
            data-testid="analytics-period"
          >
            {PERIOD_PRESETS.map((days) => (
              <option key={days} value={String(days)}>
                {t("analytics.period.days", { n: days })}
              </option>
            ))}
            <option value={CUSTOM}>{t("analytics.period.custom")}</option>
          </select>
        </div>
        <div>
          <label style={labelStyle} htmlFor="analytics-from">
            {t("analytics.filter.from")}
          </label>
          <input
            id="analytics-from"
            type="date"
            style={inputStyle}
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            data-testid="analytics-from"
          />
        </div>
        <div>
          <label style={labelStyle} htmlFor="analytics-to">
            {t("analytics.filter.to")}
          </label>
          <input
            id="analytics-to"
            type="date"
            style={inputStyle}
            value={to}
            onChange={(e) => setTo(e.target.value)}
            data-testid="analytics-to"
          />
        </div>
        <button type="button" style={primaryButtonStyle} onClick={apply} data-testid="analytics-apply">
          {t("analytics.filter.apply")}
        </button>
      </div>
      {invalid ? (
        <p style={fieldErrorStyle} data-testid="analytics-invalid">
          {t("analytics.filter.invalid")}
        </p>
      ) : null}
    </div>
  );
}

const badgeBase: CSSProperties = { fontVariantNumeric: "tabular-nums", whiteSpace: "nowrap" };

/**
 * The change of a count against the previous period. More exits or errors is
 * worse, so "up" is red and "down" is green.
 */
export function ChangeBadge({ current, previous }: { current: number; previous: number }): JSX.Element {
  const dir = changeDirection(current, previous);
  const color = dir === "up" ? "#b91c1c" : dir === "down" ? "#15803d" : "#64748b";
  return (
    <span style={{ ...badgeBase, color }} data-testid="analytics-change" data-direction={dir}>
      {changeLabel(current, previous)}
    </span>
  );
}
