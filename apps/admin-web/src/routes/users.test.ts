import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  ADMIN_USER_ROLES,
  buildAdminCreateUserBody,
  formatAdminUserRole,
  isOrgScopedAdminRole,
  mapCreateUserServerError,
  validateAdminUserEmail,
  validateAdminUserOrgId,
  type AdminUserRole,
} from "./users";

describe("Users provisioning helpers", () => {
  it("exposes the supported admin role set", () => {
    expect(ADMIN_USER_ROLES).toEqual([
      "platform_operator",
      "platform_superadmin",
      "organizer",
      "agent",
      "network_operator",
      "external_ticketing_operator",
    ]);
  });

  it("distinguishes global and organization-scoped roles", () => {
    expect(isOrgScopedAdminRole("platform_operator")).toBe(false);
    expect(isOrgScopedAdminRole("platform_superadmin")).toBe(false);
    expect(isOrgScopedAdminRole("organizer")).toBe(true);
    expect(isOrgScopedAdminRole("agent")).toBe(true);
    expect(isOrgScopedAdminRole("network_operator")).toBe(true);
    expect(isOrgScopedAdminRole("external_ticketing_operator")).toBe(true);
  });

  it("validates email input", () => {
    expect(validateAdminUserEmail("")).not.toBeNull();
    expect(validateAdminUserEmail("not-an-email")).not.toBeNull();
    expect(validateAdminUserEmail("op@example.com")).toBeNull();
    expect(validateAdminUserEmail(" First.Last+tag@Example.co ")).toBeNull();
  });

  it("validates organization UUID input", () => {
    expect(validateAdminUserOrgId("")).not.toBeNull();
    expect(validateAdminUserOrgId("not-a-uuid")).not.toBeNull();
    expect(
      validateAdminUserOrgId("00000000-0000-0000-0000-000000000001"),
    ).toBeNull();
  });

  it("builds a global-role payload without org_id", () => {
    expect(
      buildAdminCreateUserBody(
        " OP@Example.com ",
        "platform_operator",
        "00000000-0000-0000-0000-000000000001",
        " en ",
      ),
    ).toEqual({
      email: "op@example.com",
      role: "platform_operator",
      locale: "en",
    });
  });

  it("builds an organization-scoped payload with org_id", () => {
    expect(
      buildAdminCreateUserBody(
        "agent@example.com",
        "agent",
        "00000000-0000-0000-0000-000000000001",
        "",
      ),
    ).toEqual({
      email: "agent@example.com",
      role: "agent",
      org_id: "00000000-0000-0000-0000-000000000001",
    });
  });

  it("formats role labels", () => {
    expect(formatAdminUserRole("platform_superadmin")).toBe("Platform superadmin");
    expect(formatAdminUserRole("network_operator")).toBe("Network operator");
    expect(formatAdminUserRole("future_role")).toBe("future_role");
  });

  it("maps server errors to form fields", () => {
    function err(
      code: string,
      message = "boom",
      details?: Record<string, unknown>,
    ): ApiError {
      return new ApiError(400, { code, message, details });
    }

    expect(mapCreateUserServerError(err("admin_user.invalid_email")).email).toBe(
      "boom",
    );
    expect(mapCreateUserServerError(err("admin_user.invalid_role")).role).toBe(
      "boom",
    );
    expect(mapCreateUserServerError(err("admin_user.missing_org_id")).orgId).toBe(
      "boom",
    );
    expect(mapCreateUserServerError(err("admin_user.org_not_allowed")).orgId).toBe(
      "boom",
    );
    expect(
      mapCreateUserServerError(
        err("admin_user.unknown", "bad org", { field: "org_id" }),
      ).orgId,
    ).toBe("bad org");
    expect(mapCreateUserServerError(err("permissions.denied")).form).toMatch(
      /membership\.grant/,
    );
    expect(
      mapCreateUserServerError(err("superadmin.reason_required")).form,
    ).toMatch(/audit reason/i);
  });

  it("keeps the role type compatible with OpenAPI enum values", () => {
    const role: AdminUserRole = "platform_operator";
    expect(role).toBe("platform_operator");
  });
});
