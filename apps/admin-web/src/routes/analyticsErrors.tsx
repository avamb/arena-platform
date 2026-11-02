/**
 * Analytics: errors (spec 50, section 10, stage A8) — the most frequent
 * screen errors, bot input errors and rage clicks.
 *
 *   GET /v1/admin/analytics/errors?surface=&from=&to=
 *
 * Counts in the period and in the previous period of equal length, most
 * frequent first. Superadmin only (superadmin.read). Mock data: NONE.
 */
import { createRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useState, type CSSProperties } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ApiError } from "@/lib/api/client";
import {
  fetchErrors,
  formatPct,
  presetRange,
  type AnalyticsQuery,
  type ErrorsResponse,
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
  subheadingStyle,
  tableStyle,
  tdStyle,
  thStyle,
} from "./onboardingUi";
import { ChangeBadge, FilterBar, type Translate } from "./analyticsUi";

const ERRORS_NAV_ENTRY = NAV_BY_PATH["/analytics/errors"]!;

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/analytics/errors",
  component: AnalyticsErrorsRoute,
});

function AnalyticsErrorsRoute() {
  return (
    <RequirePermission entry={ERRORS_NAV_ENTRY}>
      <AnalyticsErrorsPage />
    </RequirePermission>
  );
}

function AnalyticsErrorsPage(): JSX.Element {
  const { t } = useTranslation();
  const [query, setQuery] = useState<AnalyticsQuery>(() => ({
    surface: "",
    ...presetRange(7, new Date()),
  }));
  const errors = useQuery<ErrorsResponse, ApiError>({
    queryKey: ["analytics-errors", query.surface, query.from, query.to],
    queryFn: () => fetchErrors(query),
    refetchOnWindowFocus: false,
    retry: false,
  });

  return (
    <div style={pageStyle} data-testid="analytics-errors">
      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle}>{t("analytics.errors.title")}</h1>
          <p style={subheadingStyle}>{t("analytics.errors.subtitle")}</p>
        </div>
      </div>
      <FilterBar query={query} onChange={setQuery} t={t} />
      {errors.isLoading ? <p style={mutedStyle}>{t("analytics.loading")}</p> : null}
      {errors.error ? (
        <div style={errorStyle} data-testid="analytics-error">
          {t("analytics.loadError", { message: errors.error.message })}
        </div>
      ) : null}
      {errors.data ? <ErrorsView data={errors.data} t={t} /> : null}
    </div>
  );
}

const numCell: CSSProperties = { ...tdStyle, textAlign: "right", fontVariantNumeric: "tabular-nums" };

/** The translated kind, or the raw kind when the catalog has no label for it. */
export function kindLabel(kind: string, t: Translate): string {
  const key = `analytics.kind.${kind}`;
  const label = t(key);
  return label === key ? kind : label;
}

export function ErrorsView({ data, t }: { data: ErrorsResponse; t: Translate }): JSX.Element {
  return (
    <div data-testid="analytics-errors-view">
      <p style={hintStyle}>
        {t("analytics.zoneNote")} {t("analytics.compare", { from: data.previous_from, to: data.previous_to })}
      </p>
      <p style={hintStyle}>{t("analytics.errors.hint")}</p>
      {data.items.length === 0 ? (
        <p style={mutedStyle} data-testid="analytics-empty">
          {t("analytics.empty.errors")}
        </p>
      ) : (
        <section style={sectionStyle}>
          <div style={{ overflowX: "auto" }}>
            <table style={tableStyle}>
              <thead>
                <tr>
                  <th style={thStyle}>{t("analytics.col.kind")}</th>
                  <th style={thStyle}>{t("analytics.col.key")}</th>
                  <th style={thStyle}>{t("analytics.col.place")}</th>
                  <th style={thStyle}>{t("analytics.col.surface")}</th>
                  <th style={{ ...thStyle, textAlign: "right" }}>{t("analytics.col.count")}</th>
                  <th style={{ ...thStyle, textAlign: "right" }}>{t("analytics.col.prevCount")}</th>
                  <th style={thStyle}>{t("analytics.col.change")}</th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((e, i) => (
                  <tr key={`${e.kind}|${e.surface}|${e.key}|${e.place}|${i}`} data-testid="analytics-error-row" data-kind={e.kind}>
                    <td style={tdStyle}>{kindLabel(e.kind, t)}</td>
                    <td style={tdStyle}>{e.key === "" ? "—" : e.key}</td>
                    <td style={tdStyle}>{e.place === "" ? "—" : e.place}</td>
                    <td style={tdStyle}>{e.surface}</td>
                    <td style={numCell}>{e.count}</td>
                    <td style={{ ...numCell, color: "#64748b" }}>{e.previous_count}</td>
                    <td style={tdStyle} title={formatPct(e.change_pct)}>
                      <ChangeBadge current={e.count} previous={e.previous_count} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </div>
  );
}
