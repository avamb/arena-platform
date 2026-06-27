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
    label: "Liveness probe",
    path: "/healthz",
    purpose:
      "Process-level liveness check. Returns 200 when the API server is running.",
    browserSafe: true,
  },
  {
    id: "readyz",
    label: "Readiness probe",
    path: "/readyz",
    purpose:
      "Aggregates registered readiness checks (DB, outbox, dependent services). 503 with structured checks map on failure.",
    browserSafe: true,
  },
  {
    id: "metrics",
    label: "Prometheus metrics",
    path: "/metrics",
    purpose:
      "Prometheus text exposition. Plain text only -- the UI deliberately does not parse this into charts.",
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
      "Request rate, error rate, p50/p95/p99 latency, active workers, queue depth.",
  },
  {
    id: "GO2",
    label: "API traffic and latency",
    endpoint: "GET /v1/admin/observability/http",
    purpose:
      "Per-route RED metrics (rate, errors, duration). Backed by the http_request_duration_seconds histogram.",
  },
  {
    id: "GO3",
    label: "Checkout / order health",
    endpoint: "GET /v1/admin/observability/checkout",
    purpose:
      "Reservation success / abandon rate, idempotency conflicts, slow-checkout outliers.",
  },
  {
    id: "GO4",
    label: "Payment / refund health",
    endpoint: "GET /v1/admin/observability/payments",
    purpose:
      "Authorization success rate, refund success rate, provider webhook freshness, dispute rate.",
  },
  {
    id: "GO5",
    label: "Webhook delivery",
    endpoint: "GET /v1/admin/observability/webhooks",
    purpose:
      "Outbox depth, delivery success / failure rate, retry backlog, dead-letter count.",
  },
  {
    id: "GO6",
    label: "Scanner sync",
    endpoint: "GET /v1/admin/observability/scanners",
    purpose:
      "Per-scanner heartbeat, scan-event ingestion lag, sync failures.",
  },
  {
    id: "GO7",
    label: "Background jobs / queues",
    endpoint: "GET /v1/admin/observability/jobs",
    purpose:
      "Queue depth, oldest job age, worker pool saturation, failure rate per queue.",
  },
  {
    id: "GO8",
    label: "Billing / invoice health",
    endpoint: "GET /v1/admin/observability/billing",
    purpose:
      "Invoice generation success rate, collection attempt success rate, dunning state.",
  },
  {
    id: "GO9",
    label: "External ingestion",
    endpoint: "GET /v1/admin/observability/ingestion",
    purpose:
      "Bil24 / WordPress / external allocation ingestion job lag and error rate.",
  },
  {
    id: "GO10",
    label: "Error grouping",
    endpoint: "GET /v1/admin/observability/errors",
    purpose:
      "Errors grouped by fingerprint with severity, first/last seen, affected service, owner.",
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
            Honest shell. The platform exposes operational probes for
            Dokploy and Prometheus, listed below as direct links. No
            in-page dashboards are rendered: parsing
            Prometheus text into charts here would produce
            misleading rollups (cardinality sampling, rate windowing).
            When real dashboard APIs land they replace the gap tiles
            below. Permission rule:{" "}
            {describeRule(NAV_ENTRY.permission)}.
          </p>
        </div>
      </header>

      <div style={S.drawerSectionStyle} aria-labelledby="obs-probes-h2">
        <h2 id="obs-probes-h2" style={S.drawerSectionTitleStyle}>
          Operational probes
        </h2>
        <p style={S.drawerHelpStyle}>
          Direct links to the backend operational probes. These are
          deployment-level signals, not part of the /v1 API contract.
          Reachability depends on network and CORS configuration --
          opens in a new tab so a failed request never replaces this
          shell.
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
          Dashboard endpoints not wired yet
        </h2>
        <p style={S.drawerHelpStyle}>
          Each tile names a dashboard family from
          08_platform_superadmin_observability_ru.md and the exact
          backend endpoint that has to land before the dashboard can
          be populated. No tile renders synthesized data.
        </p>
        <div style={S.relatedGridStyle}>
          {OBSERVABILITY_GAPS.map((gap) => (
            <ObsGapTile key={gap.id} gap={gap} />
          ))}
        </div>
      </div>

      <div style={S.gapNoteStyle}>
        <strong>Why no inline charts?</strong> The platform&apos;s
        observability stack (Prometheus + OTLP) is the source of truth.
        Re-parsing /metrics text into charts in the SPA produces
        rollups that look right at a glance but lie under load. The
        SuperAdmin console intentionally points operators at the
        upstream tooling until a dedicated dashboard API ships.
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
        <span style={S.warnBadgeStyle}>Plain text -- may be ACLed</span>
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
