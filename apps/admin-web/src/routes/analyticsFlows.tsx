/**
 * Analytics: flows (spec 50, section 10, stage A8) — where people leave the
 * bot wizard and the event center flows.
 *
 *   GET /v1/admin/analytics/flows?surface=&from=&to=
 *
 * Per flow a table of its steps with how many entered, how many left there,
 * the drop-off as a number and a bar, the errors, the median time on the
 * step, and the same numbers of the previous period of equal length.
 * Superadmin only (superadmin.read). Mock data: NONE.
 */
import { createRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useState, type CSSProperties } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ApiError } from "@/lib/api/client";
import {
  barWidth,
  fetchFlows,
  formatPct,
  formatSeconds,
  presetRange,
  type AnalyticsQuery,
  type Flow,
  type FlowsResponse,
} from "@/lib/api/analytics";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { useTranslation } from "@/lib/i18n/I18nContext";
import {
  errorStyle,
  headerRowStyle,
  headingStyle,
  hintStyle,
  mutedStyle,
  pageStyle,
  sectionStyle,
  sectionTitleStyle,
  subheadingStyle,
  tableStyle,
  tdStyle,
  thStyle,
} from "./onboardingUi";
import { ChangeBadge, FilterBar, type Translate } from "./analyticsUi";

const FLOWS_NAV_ENTRY = NAV_BY_PATH["/analytics/flows"]!;

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/analytics/flows",
  component: AnalyticsFlowsRoute,
});

function AnalyticsFlowsRoute() {
  return (
    <RequirePermission entry={FLOWS_NAV_ENTRY}>
      <AnalyticsFlowsPage />
    </RequirePermission>
  );
}

function AnalyticsFlowsPage(): JSX.Element {
  const { t } = useTranslation();
  const [query, setQuery] = useState<AnalyticsQuery>(() => ({
    surface: "",
    ...presetRange(7, new Date()),
  }));
  const flows = useQuery<FlowsResponse, ApiError>({
    queryKey: ["analytics-flows", query.surface, query.from, query.to],
    queryFn: () => fetchFlows(query),
    refetchOnWindowFocus: false,
    retry: false,
  });

  return (
    <div style={pageStyle} data-testid="analytics-flows">
      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle}>{t("analytics.flows.title")}</h1>
          <p style={subheadingStyle}>{t("analytics.flows.subtitle")}</p>
        </div>
      </div>
      <FilterBar query={query} onChange={setQuery} t={t} />
      {flows.isLoading ? <p style={mutedStyle}>{t("analytics.loading")}</p> : null}
      {flows.error ? (
        <div style={errorStyle} data-testid="analytics-error">
          {t("analytics.loadError", { message: flows.error.message })}
        </div>
      ) : null}
      {flows.data ? <FlowsView data={flows.data} t={t} /> : null}
    </div>
  );
}

const barTrack: CSSProperties = {
  display: "inline-block",
  width: 120,
  height: 8,
  background: "#e2e8f0",
  borderRadius: 4,
  verticalAlign: "middle",
  overflow: "hidden",
};

/** A horizontal bar for a drop-off share, 0-100. */
export function DropBar({ pct }: { pct: number | null }): JSX.Element {
  const w = barWidth(pct);
  return (
    <span style={barTrack} aria-hidden="true">
      <span
        data-testid="analytics-dropbar"
        data-width={w}
        style={{ display: "block", height: "100%", width: `${w}%`, background: w >= 50 ? "#dc2626" : "#f59e0b" }}
      />
    </span>
  );
}

const numCell: CSSProperties = { ...tdStyle, textAlign: "right", fontVariantNumeric: "tabular-nums" };

export function FlowTable({ flow, t }: { flow: Flow; t: Translate }): JSX.Element {
  return (
    <section style={sectionStyle} data-testid="analytics-flow" data-flow={`${flow.surface}/${flow.flow}`}>
      <h2 style={sectionTitleStyle}>
        {flow.surface} / {flow.flow}
      </h2>
      <div style={{ overflowX: "auto" }}>
        <table style={tableStyle}>
          <thead>
            <tr>
              <th style={thStyle}>{t("analytics.col.step")}</th>
              <th style={{ ...thStyle, textAlign: "right" }}>{t("analytics.col.entered")}</th>
              <th style={{ ...thStyle, textAlign: "right" }}>{t("analytics.col.left")}</th>
              <th style={thStyle}>{t("analytics.col.dropoff")}</th>
              <th style={{ ...thStyle, textAlign: "right" }}>{t("analytics.col.errors")}</th>
              <th style={{ ...thStyle, textAlign: "right" }}>{t("analytics.col.median")}</th>
              <th style={thStyle}>{t("analytics.col.previous")}</th>
              <th style={thStyle}>{t("analytics.col.change")}</th>
            </tr>
          </thead>
          <tbody>
            {flow.steps.map((s) => (
              <tr key={s.step} data-testid="analytics-step" data-step={s.step}>
                <td style={tdStyle}>{s.step}</td>
                <td style={numCell}>{s.entered}</td>
                <td style={numCell}>{s.left_here}</td>
                <td style={tdStyle}>
                  <DropBar pct={s.drop_off_pct} /> <span style={{ fontVariantNumeric: "tabular-nums" }}>{formatPct(s.drop_off_pct)}</span>
                </td>
                <td style={numCell}>{s.errors}</td>
                <td style={numCell}>{formatSeconds(s.median_seconds)}</td>
                <td style={{ ...tdStyle, color: "#64748b", whiteSpace: "nowrap" }}>
                  {s.previous.entered} / {formatPct(s.previous.drop_off_pct)}
                </td>
                <td style={tdStyle}>
                  <ChangeBadge current={s.left_here} previous={s.previous.left_here} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

export function FlowsView({ data, t }: { data: FlowsResponse; t: Translate }): JSX.Element {
  return (
    <div data-testid="analytics-flows-view">
      <p style={hintStyle}>
        {t("analytics.zoneNote")} {t("analytics.compare", { from: data.previous_from, to: data.previous_to })}
      </p>
      <p style={hintStyle}>{t("analytics.flows.hint")}</p>
      {data.flows.length === 0 ? (
        <p style={mutedStyle} data-testid="analytics-empty">
          {t("analytics.empty.flows")}
        </p>
      ) : (
        data.flows.map((f) => <FlowTable key={`${f.surface}/${f.flow}`} flow={f} t={t} />)
      )}
    </div>
  );
}
