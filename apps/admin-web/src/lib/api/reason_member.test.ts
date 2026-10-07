/**
 * An organization's own owner is not reading across tenants, so the
 * org-scoped paths must not ask them for an audit reason; only a platform
 * superadmin is asked (2026-10-07: an invited owner met the "read data across
 * tenants" prompt on the first screen).
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  __TEST_ONLY_resetReason,
  requiresAdminReason,
  setOrgScopedReasonApplies,
} from "@/lib/api/reason";

const ORG = "019fadae-0557-751f-8ec8-4b372b5c408c";

describe("org-scoped audit reason", () => {
  beforeEach(() => {
    __TEST_ONLY_resetReason();
  });
  afterEach(() => {
    __TEST_ONLY_resetReason();
  });

  it("asks by default, which is the superadmin behaviour", () => {
    expect(requiresAdminReason(`/v1/organizations/${ORG}/channels`)).toBe(true);
    expect(requiresAdminReason(`/v1/organizations/${ORG}/members`, "POST")).toBe(true);
  });

  it("does not ask an organization member on their own organization", () => {
    setOrgScopedReasonApplies(false);
    for (const path of [
      `/v1/organizations/${ORG}/channels`,
      `/v1/organizations/${ORG}/venues`,
      `/v1/organizations/${ORG}/payment-configs`,
      `/v1/organizations/${ORG}/events`,
      `/v1/organizations/${ORG}/api-keys`,
    ]) {
      expect(requiresAdminReason(path), path).toBe(false);
      expect(requiresAdminReason(path, "POST"), path).toBe(false);
    }
    expect(requiresAdminReason("/v1/venues/abc/seating-plans", "POST")).toBe(false);
  });

  it("still asks for the superadmin-only admin paths", () => {
    setOrgScopedReasonApplies(false);
    expect(requiresAdminReason("/v1/admin/organizations")).toBe(true);
    expect(requiresAdminReason("/v1/admin/users")).toBe(true);
    expect(requiresAdminReason("/v1/admin/networks", "POST")).toBe(true);
  });

  it("the reset puts the default back", () => {
    setOrgScopedReasonApplies(false);
    __TEST_ONLY_resetReason();
    expect(requiresAdminReason(`/v1/organizations/${ORG}/channels`)).toBe(true);
  });
});
