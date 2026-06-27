/**
 * SAUI-11 -- audit shell unit tests.
 *
 * The route component is a permission gate + static markup with no
 * fetches. These cases pin the gap configuration so a future edit
 * cannot silently drop the masked/sensitive-log tile or rename the
 * sensitive-logs permission contract.
 */
import { describe, expect, it } from "vitest";
import { AUDIT_GAPS, SENSITIVE_LOGS_PERMISSION } from "@/routes/audit";

describe("AUDIT_GAPS table", () => {
  it("ids are unique and stable (GA1..GAn)", () => {
    const ids = AUDIT_GAPS.map((g) => g.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const id of ids) {
      expect(id).toMatch(/^GA\d+$/u);
    }
  });

  it("every gap names a concrete endpoint", () => {
    for (const gap of AUDIT_GAPS) {
      expect(gap.endpoint).toMatch(/^GET \/v1\/admin\//u);
    }
  });

  it("endpoints are unique (one tile per backend gap)", () => {
    const endpoints = AUDIT_GAPS.map((g) => g.endpoint);
    expect(new Set(endpoints).size).toBe(endpoints.length);
  });

  it("includes a sensitive-log tile gated by the documented permission", () => {
    const sensitive = AUDIT_GAPS.find((g) => g.gatedBy !== undefined);
    expect(sensitive).toBeDefined();
    expect(sensitive?.gatedBy).toBe(SENSITIVE_LOGS_PERMISSION);
  });

  it("only the sensitive-log tile has a gatedBy permission", () => {
    // Tightening this prevents silently widening the permission gate
    // to other tiles (a regression that would mask normal audit list
    // access behind a permission that almost no operator holds).
    const gated = AUDIT_GAPS.filter((g) => g.gatedBy !== undefined);
    expect(gated.length).toBe(1);
  });

  it("documents the network-scoped audit reader gap (SAUI-08 G3 follow-up)", () => {
    const endpoints = AUDIT_GAPS.map((g) => g.endpoint);
    expect(endpoints).toContain(
      "GET /v1/admin/networks/{id}/audit",
    );
  });

  it("documents the org-scoped audit reader gap", () => {
    const endpoints = AUDIT_GAPS.map((g) => g.endpoint);
    expect(endpoints).toContain("GET /v1/admin/orgs/{id}/audit");
  });

  it("does not silently ship an /v1/admin/audit list reader claim", () => {
    // The list reader is the first gap; if a future edit ever moves a
    // tile out of "gap" rendering this assertion is a reminder to
    // double-check the wiring before deleting the tile.
    const listReader = AUDIT_GAPS.find(
      (g) => g.endpoint === "GET /v1/admin/audit",
    );
    expect(listReader).toBeDefined();
  });
});

describe("SENSITIVE_LOGS_PERMISSION", () => {
  it("matches the documented platform.superadmin.view_sensitive_logs intent", () => {
    expect(SENSITIVE_LOGS_PERMISSION).toBe(
      "superadmin.view_sensitive_logs",
    );
  });
});
