/**
 * SAUI-11 -- observability shell unit tests.
 *
 * The route component itself is a thin permission gate + static
 * markup; render-level coverage lives in the navConfig + RequirePermission
 * suites. These cases pin the pure helpers and configuration tables
 * that drive what the shell shows so a future edit cannot silently
 * regress the "no fake dashboards" guarantee or mis-build a probe URL.
 */
import { describe, expect, it } from "vitest";
import {
  OBSERVABILITY_GAPS,
  OPERATIONAL_PROBES,
  probeUrl,
} from "@/routes/observability";

describe("probeUrl", () => {
  it("joins base + probe path with a single slash", () => {
    expect(probeUrl("https://api.example.com", "/healthz")).toBe(
      "https://api.example.com/healthz",
    );
  });

  it("strips a trailing slash from the base", () => {
    expect(probeUrl("https://api.example.com/", "/readyz")).toBe(
      "https://api.example.com/readyz",
    );
  });

  it("strips multiple trailing slashes from the base", () => {
    expect(probeUrl("https://api.example.com///", "/healthz")).toBe(
      "https://api.example.com/healthz",
    );
  });

  it("adds a missing leading slash to the probe path", () => {
    expect(probeUrl("https://api.example.com", "metrics")).toBe(
      "https://api.example.com/metrics",
    );
  });

  it("preserves a port and host-only base", () => {
    expect(probeUrl("http://localhost:8080", "/healthz")).toBe(
      "http://localhost:8080/healthz",
    );
  });
});

describe("OPERATIONAL_PROBES table", () => {
  it("covers exactly the three documented probes in stable order", () => {
    expect(OPERATIONAL_PROBES.map((p) => p.id)).toEqual([
      "healthz",
      "readyz",
      "metrics",
    ]);
  });

  it("flags /metrics as not browser-safe (Prometheus text)", () => {
    const metrics = OPERATIONAL_PROBES.find((p) => p.id === "metrics");
    expect(metrics).toBeDefined();
    expect(metrics?.browserSafe).toBe(false);
  });

  it("flags /healthz and /readyz as browser-safe", () => {
    const healthz = OPERATIONAL_PROBES.find((p) => p.id === "healthz");
    const readyz = OPERATIONAL_PROBES.find((p) => p.id === "readyz");
    expect(healthz?.browserSafe).toBe(true);
    expect(readyz?.browserSafe).toBe(true);
  });

  it("every probe path is rooted (starts with '/')", () => {
    for (const probe of OPERATIONAL_PROBES) {
      expect(probe.path.startsWith("/")).toBe(true);
    }
  });
});

describe("OBSERVABILITY_GAPS table", () => {
  it("ids are unique and stable (GO1..GOn)", () => {
    const ids = OBSERVABILITY_GAPS.map((g) => g.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const id of ids) {
      expect(id).toMatch(/^GO\d+$/u);
    }
  });

  it("every gap names an /v1/admin/observability/* endpoint", () => {
    for (const gap of OBSERVABILITY_GAPS) {
      expect(gap.endpoint).toMatch(/^GET \/v1\/admin\/observability\/[a-z]+$/u);
    }
  });

  it("endpoints are unique (one tile per dashboard family)", () => {
    const endpoints = OBSERVABILITY_GAPS.map((g) => g.endpoint);
    expect(new Set(endpoints).size).toBe(endpoints.length);
  });

  it("covers the master spec's required dashboard families", () => {
    // Sanity check: at minimum overview / http / payments / webhooks /
    // jobs / errors must be present. This pins the contract with
    // 08_platform_superadmin_observability_ru.md so a future edit that
    // drops a tile fails fast.
    const labels = OBSERVABILITY_GAPS.map((g) => g.label.toLowerCase());
    for (const required of [
      "platform overview",
      "api traffic and latency",
      "payment / refund health",
      "webhook delivery",
      "background jobs / queues",
      "error grouping",
    ]) {
      expect(labels).toContain(required);
    }
  });
});
