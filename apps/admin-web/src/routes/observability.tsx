/**
 * SuperAdmin Observability shell (SAUI-11).
 *
 * The backend exposes three operational probes -- /healthz, /readyz,
 * and /metrics -- registered in
 * apps/backend/internal/platform/httpserver/server.go. Those endpoints
 * are deployment-level signals (probe traffic for Dokploy, Prometheus
 * scrape) and are NOT part of the /v1 API contract; they have no
 * structured JSON envelope, no error envelope, and no admin-reason
 * gating.
 *
 * This shell:
 *   - is gated by `superadmin.read` (RequirePermission);
 *   - surfaces *links* to the absolute probe URLs derived from
 *     `config.apiBaseUrl`, so an operator can open them in a new tab
 *     if CORS / network reachability permits. We do NOT fetch them
 *     from the SPA -- /metrics is a Prometheus text exposition and
 *     /healthz / /readyz return plain JSON without the platform error
 *     envelope; parsing them client-side and rendering charts here
 *     would be exactly the "fake dashboard" failure mode that the
 *     spec forbids;
 *   - lists every dashboard family from the master observability spec
 *     (08_platform_superadmin_observability_ru.md, "Required
 *     Dashboards") as a backend gap with the missing API contract.
 *
 * When real APIs land (e.g. an HTTP latency histogram reader, an
 * error-event grouping endpoint, a queue-status endpoint) the
 * corresponding gap tile can be replaced in place with the real
 * dashboard component -- the shell layout will not need a redesign.
 *
 * Mock data: NONE. We deliberately do NOT fetch Prometheus text and
 * synthesize numeric rollups from it; that path produces dashboards
 * that look right at a glance but lie under load (cardinality
 * sampling, rate windowing, missing aggregations). When the platform
 * gets a real dashboard API, replace the tile body with the real
 * data; do not parse /metrics here.
 */
import { createRoute } from "@tanstack/react-router";
import type { CSSProperties } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { NAV_BY_PATH, describeRule } from "@/lib/auth/navConfig";
import { config } from "@/lib/config";
import * as S from "@/lib/admin/supportStyles";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/observability",
  component: ObservabilityRoute,
});

const NAV_ENTRY = NAV_BY_PATH["/observability"];
if (NAV_ENTRY === undefined) {
  throw new Error("observability route: NAV_BY_PATH['/observability'] missing");
}

/**
 * Build an absolute operational-probe URL.
 *
 * `apiBaseUrl` typically points at the /v1 mount (e.g.
 * `https://api.example.com`); the probes are siblings of /v1 at the
 * root, so we strip a trailing slash and append the probe path.
 *
 * Exported for unit testing.
 */
export function probeUrl(apiBaseUrl: string, probePath: string): string {
  const base = apiBaseUrl.replace(/\/+$/u, "");
  const path = probePath.startsWith("/") ? probePath : `/${probePath}`;
  return `${base}${path}`;
}

export interface ProbeLink {
  readonly id: string;
  readonly label: string;
  readonly path: string;
  readonly purpose: string;
  /**
   * When true, opening the probe from a browser is expected to work
   * if CORS / network reachability permits. /metrics is a Prometheus
   * text exposition and may be IP-allowlisted in production; we
   * still surface the URL but warn the operator inline.
   */
  readonly browserSafe: boolean;
}

export const OPERATIONAL_PROBES: readonly ProbeLink[] = [
  {
    id: "healthz",
    label: "Liveness check",
    path: "/healthz",
    purpose:
      "Shows that the API server is alive. The answer is 200 when the server works.",
    browserSafe: true,
  },
  {
    id: "readyz",
    label: "Readiness check",
    path: "/readyz",
    purpose:
      "Shows the result of each readiness check (database, outbox, other services). If a check is not successful, the answer is 503 with a map of the checks.",
    browserSafe: true,
  },
  {
    id: "metrics",
    label: "Prometheus metrics",
    path: "/metrics",
    purpose:
      "Prometheus text. The page does not turn this text into charts.",
    browserSafe: false,
  },
];

export interface ObservabilityGap {
  readonly id: string;
  readonly label: string;
  readonly endpoint: string;
  readonly purpose: string;
}

/**
 * Dashboard families from the master observability spec
 * (08_platform_superadmin_observability_ru.md, "Required Dashboards").
 * Each entry names the API endpoint that has to land before the
 * dashboard can be populated. The list is intentionally exhaustive --
 * absence of a tile would suggest the dashboard "isn't needed", which
 * is the wrong signal.
 */
export const OBSERVABILITY_GAPS: readonly ObservabilityGap[] = [
  {
    id: "GO1",
    label: "Platform overview",
    endpoint: "GET /v1/admin/observability/overview",
    purpose:
      "Request rate, error rate, p50/p95/p99 latency, active workers, and queue depth.",
  },
  {
    id: "GO2",
    label: "API traffic and latency",
    endpoint: "GET /v1/admin/observability/http",
    purpose:
      "RED metrics for each route (rate, errors, duration). The source is the http_request_duration_seconds histogram.",
  },
  {
    id: "GO3",
    label: "Checkout / order health",
    endpoint: "GET /v1/admin/observability/checkout",
    purpose:
      "Hold success rate, hold stop rate, idempotency conflicts, and checkouts that take a long time.",
  },
  {
    id: "GO4",
    label: "Payment / refund health",
    endpoint: "GET /v1/admin/observability/payments",
    purpose:
      "Authorization success rate, refund success rate, age of the last provider webhook, and dispute rate.",
  },
  {
    id: "GO5",
    label: "Webhook delivery",
    endpoint: "GET /v1/admin/observability/webhooks",
    purpose:
      "Outbox depth, delivery success rate, delivery failure rate, retry backlog, and dead-letter count.",
  },
  {
    id: "GO6",
    label: "Scanner sync",
    endpoint: "GET /v1/admin/observability/scanners",
    purpose:
      "Heartbeat of each scanner, scan-event ingestion lag, and sync failures.",
  },
  {
    id: "GO7",
    label: "Background jobs / queues",
    endpoint: "GET /v1/admin/observability/jobs",
    purpose:
      "Queue depth, age of the oldest job, worker pool load, and failure rate for each queue.",
  },
  {
    id: "GO8",
    label: "Billing / invoice health",
    endpoint: "GET /v1/admin/observability/billing",
    purpose:
      "Invoice generation success rate, payment collection success rate, and dunning status.",
  },
  {
    id: "GO9",
    label: "External ingestion",
    endpoint: "GET /v1/admin/observability/ingestion",
    purpose:
      "Lag and error rate of the ingestion jobs for Bil24, WordPress, and other external allocations.",
  },
  {
    id: "GO10",
    label: "Error grouping",
    endpoint: "GET /v1/admin/observability/errors",
    purpose:
      "Errors in groups by fingerprint. Each group shows severity, first and last time seen, the service, and the owner.",
  },
];

function ObservabilityRoute() {
  return (
    <RequirePermission entry={NAV_ENTRY}>
      <ObservabilityShell />
    </RequirePermission>
  );
}

function ObservabilityShell() {
  return (
    <section style={S.pageStyle} aria-labelledby="obs-h1">
      <header style={S.headerStyle}>
        <div>
          <h1 id="obs-h1" style={S.headingStyle}>
            Observability
          </h1>
          <p style={S.subheadingStyle}>
            This page is a shell. The platform gives operational probes
            for Dokploy and Prometheus. The links are below. The page
            has no dashboards. Charts made from Prometheus text here
            can show incorrect totals. When the dashboard APIs exist,
            they replace the gap tiles below. Permission rule:{" "}
            {describeRule(NAV_ENTRY.permission)}.
          </p>
        </div>
      </header>

      <div style={S.drawerSectionStyle} aria-labelledby="obs-probes-h2">
        <h2 id="obs-probes-h2" style={S.drawerSectionTitleStyle}>
          Operational probes
        </h2>
        <p style={S.drawerHelpStyle}>
          Links to the backend operational probes. These are
          deployment signals. They are not part of the /v1 API contract.
          If the network or CORS settings stop a link, it does not
          open. Each link opens in a new tab, so this page stays.
        </p>
        <div style={S.relatedGridStyle}>
          {OPERATIONAL_PROBES.map((probe) => (
            <ProbeTile
              key={probe.id}
              probe={probe}
              apiBaseUrl={config.apiBaseUrl}
            />
          ))}
        </div>
      </div>

      <div style={S.drawerSectionStyle} aria-labelledby="obs-gaps-h2">
        <h2 id="obs-gaps-h2" style={S.drawerSectionTitleStyle}>
          Dashboard endpoints not available
        </h2>
        <p style={S.drawerHelpStyle}>
          Each tile names a dashboard family from
          08_platform_superadmin_observability_ru.md. It also names the
          backend endpoint that the dashboard will use. No tile shows
          made-up data.
        </p>
        <div style={S.relatedGridStyle}>
          {OBSERVABILITY_GAPS.map((gap) => (
            <ObsGapTile key={gap.id} gap={gap} />
          ))}
        </div>
      </div>

      <div style={S.gapNoteStyle}>
        <strong>No charts on this page.</strong> The observability
        stack of the platform (Prometheus + OTLP) is the source of
        truth. Charts that this page makes from /metrics text can show
        incorrect totals when the load is high. Until a dashboard API
        exists, the SuperAdmin console sends operators to the upstream
        tools.
      </div>
    </section>
  );
}

function ProbeTile({
  probe,
  apiBaseUrl,
}: {
  readonly probe: ProbeLink;
  readonly apiBaseUrl: string;
}) {
  const url = probeUrl(apiBaseUrl, probe.path);
  return (
    <a
      style={S.relatedTileStyle}
      href={url}
      target="_blank"
      rel="noreferrer noopener"
      data-testid={`obs-probe-${probe.id}`}
    >
      <span style={S.relatedTileLabelStyle}>{probe.label}</span>
      <span style={S.relatedTileHintStyle}>{url}</span>
      <span style={hintParaStyle}>{probe.purpose}</span>
      {probe.browserSafe ? null : (
        <span style={S.warnBadgeStyle}>Plain text. An access list can block it.</span>
      )}
    </a>
  );
}

function ObsGapTile({ gap }: { readonly gap: ObservabilityGap }) {
  return (
    <div
      style={S.relatedTileDisabledStyle}
      data-testid={`obs-gap-${gap.id}`}
      aria-label={`Backend gap ${gap.id}: ${gap.label}`}
    >
      <span style={S.relatedTileGapBadgeStyle}>
        Backend gap {gap.id}
      </span>
      <span style={S.relatedTileLabelStyle}>{gap.label}</span>
      <span style={S.relatedTileHintStyle}>{gap.endpoint}</span>
      <span style={hintParaStyle}>{gap.purpose}</span>
    </div>
  );
}

const hintParaStyle: CSSProperties = {
  fontSize: 12,
  color: "#475569",
  lineHeight: 1.4,
};
