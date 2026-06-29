import { createRoute } from "@tanstack/react-router";
import { useMutation } from "@tanstack/react-query";
import {
  useMemo,
  useState,
  type CSSProperties,
  type FormEvent,
  type ReactNode,
} from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ApiError, createAdminUser } from "@/lib/api/client";
import type {
  AdminCreateUserRequest,
  AdminCreateUserResponse,
} from "@/lib/api/types";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/users",
  component: UsersRoute,
});

const USERS_NAV_ENTRY = NAV_BY_PATH["/users"];
if (USERS_NAV_ENTRY === undefined) {
  throw new Error("users route: NAV_BY_PATH['/users'] missing");
}

export type AdminUserRole = AdminCreateUserRequest["role"];

export const GLOBAL_USER_ROLES: readonly AdminUserRole[] = [
  "platform_operator",
  "platform_superadmin",
] as const;

export const ORG_SCOPED_USER_ROLES: readonly AdminUserRole[] = [
  "organizer",
  "agent",
  "network_operator",
  "external_ticketing_operator",
] as const;

export const ADMIN_USER_ROLES: readonly AdminUserRole[] = [
  ...GLOBAL_USER_ROLES,
  ...ORG_SCOPED_USER_ROLES,
] as const;

const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

interface CreateUserErrors {
  email?: string;
  role?: string;
  orgId?: string;
  form?: string;
}

function UsersRoute() {
  return (
    <RequirePermission entry={USERS_NAV_ENTRY}>
      <UsersProvisioning />
    </RequirePermission>
  );
}

function UsersProvisioning() {
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<AdminUserRole>("platform_operator");
  const [orgId, setOrgId] = useState("");
  const [locale, setLocale] = useState("en");
  const [localErrors, setLocalErrors] = useState<CreateUserErrors>({});
  const [serverErrors, setServerErrors] = useState<CreateUserErrors>({});
  const [created, setCreated] = useState<AdminCreateUserResponse | null>(null);

  const orgScoped = isOrgScopedAdminRole(role);
  const visibleErrors = useMemo(
    () => ({ ...localErrors, ...serverErrors }),
    [localErrors, serverErrors],
  );

  const mutation = useMutation<
    AdminCreateUserResponse,
    ApiError,
    AdminCreateUserRequest
  >({
    mutationFn: createAdminUser,
    onSuccess: (data) => {
      setCreated(data);
      setServerErrors({});
      setEmail("");
      setOrgId("");
    },
    onError: (err) => {
      setServerErrors(mapCreateUserServerError(err));
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setCreated(null);
    setServerErrors({});

    const nextErrors: CreateUserErrors = {};
    const emailError = validateAdminUserEmail(email);
    if (emailError !== null) {
      nextErrors.email = emailError;
    }
    if (!ADMIN_USER_ROLES.includes(role)) {
      nextErrors.role = "Select a supported role.";
    }
    if (orgScoped) {
      const orgError = validateAdminUserOrgId(orgId);
      if (orgError !== null) {
        nextErrors.orgId = orgError;
      }
    }
    setLocalErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) {
      return;
    }

    mutation.mutate(buildAdminCreateUserBody(email, role, orgId, locale));
  }

  return (
    <section aria-labelledby="users-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="users-heading" style={headingStyle}>
            Users
          </h1>
          <p style={subheadingStyle}>
            Create a new account and assign its first role.
          </p>
        </div>
      </header>

      <form onSubmit={onSubmit} style={formStyle} noValidate>
        <Field
          label="Email"
          htmlFor="users-email"
          error={visibleErrors.email}
          hint="The address is normalised before storage."
        >
          <input
            id="users-email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            style={inputStyle}
            autoComplete="email"
            data-testid="users-email"
          />
        </Field>

        <Field
          label="Role"
          htmlFor="users-role"
          error={visibleErrors.role}
          hint={
            orgScoped
              ? "This role is assigned inside one organization."
              : "This role is assigned globally."
          }
        >
          <select
            id="users-role"
            value={role}
            onChange={(e) => setRole(e.target.value as AdminUserRole)}
            style={inputStyle}
            data-testid="users-role"
          >
            {ADMIN_USER_ROLES.map((r) => (
              <option key={r} value={r}>
                {formatAdminUserRole(r)}
              </option>
            ))}
          </select>
        </Field>

        {orgScoped ? (
          <Field
            label="Organization ID"
            htmlFor="users-org-id"
            error={visibleErrors.orgId}
            hint="Required for organizer, agent, network operator, and external operator."
          >
            <input
              id="users-org-id"
              type="text"
              value={orgId}
              onChange={(e) => setOrgId(e.target.value)}
              style={inputMonoStyle}
              autoComplete="off"
              spellCheck={false}
              data-testid="users-org-id"
            />
          </Field>
        ) : null}

        <Field
          label="Locale"
          htmlFor="users-locale"
          error={undefined}
          hint="Defaults to en."
        >
          <input
            id="users-locale"
            type="text"
            value={locale}
            onChange={(e) => setLocale(e.target.value)}
            style={inputStyle}
            autoComplete="off"
            data-testid="users-locale"
          />
        </Field>

        {visibleErrors.form !== undefined ? (
          <div style={formErrorStyle} role="alert" data-testid="users-form-error">
            {visibleErrors.form}
          </div>
        ) : null}

        <div style={formActionsStyle}>
          <button
            type="submit"
            style={primaryButtonStyle}
            disabled={mutation.isPending}
            data-testid="users-submit"
          >
            {mutation.isPending ? "Creating..." : "Create user"}
          </button>
        </div>
      </form>

      {created !== null ? (
        <div style={successStyle} role="status" data-testid="users-created">
          <strong>{created.user.email}</strong>
          <span>
            {formatAdminUserRole(created.user.role as AdminUserRole)} assigned
            {created.user.scope === "organization" && created.user.org_id
              ? ` in organization ${created.user.org_id}`
              : " globally"}
            .
          </span>
          <span>
            Password setup issued; expires {formatDateTime(created.onboarding.expires_at)}.
          </span>
        </div>
      ) : null}
    </section>
  );
}

function Field({
  label,
  htmlFor,
  error,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  error: string | undefined;
  hint: string;
  children: ReactNode;
}) {
  return (
    <div style={fieldStyle}>
      <label htmlFor={htmlFor} style={labelStyle}>
        {label}
      </label>
      {children}
      {error !== undefined ? (
        <div style={fieldErrorStyle} role="alert" data-testid={`${htmlFor}-error`}>
          {error}
        </div>
      ) : (
        <div style={hintStyle}>{hint}</div>
      )}
    </div>
  );
}

export function isOrgScopedAdminRole(role: AdminUserRole): boolean {
  return ORG_SCOPED_USER_ROLES.includes(role);
}

export function validateAdminUserEmail(raw: string): string | null {
  const value = raw.trim().toLowerCase();
  if (value === "") {
    return "Email is required.";
  }
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)) {
    return "Enter a valid email address.";
  }
  return null;
}

export function validateAdminUserOrgId(raw: string): string | null {
  const value = raw.trim();
  if (value === "") {
    return "Organization ID is required for this role.";
  }
  if (!UUID_RE.test(value)) {
    return "Organization ID must be a UUID.";
  }
  return null;
}

export function buildAdminCreateUserBody(
  rawEmail: string,
  role: AdminUserRole,
  rawOrgId: string,
  rawLocale: string,
): AdminCreateUserRequest {
  const body: AdminCreateUserRequest = {
    email: rawEmail.trim().toLowerCase(),
    role,
  };
  const locale = rawLocale.trim();
  if (locale !== "") {
    body.locale = locale;
  }
  if (isOrgScopedAdminRole(role)) {
    body.org_id = rawOrgId.trim();
  }
  return body;
}

export function mapCreateUserServerError(err: ApiError): CreateUserErrors {
  if (err.details?.field === "email") {
    return { email: err.message };
  }
  if (err.details?.field === "role") {
    return { role: err.message };
  }
  if (err.details?.field === "org_id") {
    return { orgId: err.message };
  }
  switch (err.code) {
    case "admin_user.invalid_email":
    case "admin_user.email_already_registered":
      return { email: err.message };
    case "admin_user.invalid_role":
      return { role: err.message };
    case "admin_user.missing_org_id":
    case "admin_user.invalid_org_id":
    case "admin_user.org_not_allowed":
      return { orgId: err.message };
    case "permissions.denied":
      return { form: "Your account is missing superadmin.read." };
    case "superadmin.missing_reason":
    case "superadmin.reason_required":
      return { form: "An audit reason is required before creating users." };
    case "dependency.database_unavailable":
      return { form: "Database is unavailable. Retry after the backend recovers." };
    default:
      return { form: `${err.message} (${err.code})` };
  }
}

export function formatAdminUserRole(role: AdminUserRole | string): string {
  switch (role) {
    case "platform_operator":
      return "Platform operator";
    case "platform_superadmin":
      return "Platform superadmin";
    case "network_operator":
      return "Network operator";
    case "external_ticketing_operator":
      return "External ticketing operator";
    case "organizer":
      return "Organizer";
    case "agent":
      return "Agent";
    default:
      return role;
  }
}

function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  return `${d.toISOString().slice(0, 16).replace("T", " ")}Z`;
}

const pageStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
};

const headerStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 16,
  flexWrap: "wrap",
};

const headingStyle: CSSProperties = {
  margin: 0,
  fontSize: 22,
  fontWeight: 600,
  letterSpacing: 0,
};

const subheadingStyle: CSSProperties = {
  margin: "4px 0 0 0",
  fontSize: 13,
  color: "#475569",
  maxWidth: 720,
  lineHeight: 1.45,
};

const formStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(260px, 1fr))",
  gap: 14,
  padding: 16,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const fieldStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const labelStyle: CSSProperties = {
  fontSize: 12,
  fontWeight: 600,
  color: "#334155",
};

const inputStyle: CSSProperties = {
  fontSize: 13,
  padding: "8px 10px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const inputMonoStyle: CSSProperties = {
  ...inputStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};

const hintStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
  lineHeight: 1.4,
};

const fieldErrorStyle: CSSProperties = {
  fontSize: 11,
  color: "#b91c1c",
  fontWeight: 500,
};

const formErrorStyle: CSSProperties = {
  gridColumn: "1 / -1",
  fontSize: 12,
  padding: 8,
  background: "#fef2f2",
  border: "1px solid #fca5a5",
  color: "#7f1d1d",
  borderRadius: 4,
};

const formActionsStyle: CSSProperties = {
  gridColumn: "1 / -1",
  display: "flex",
  justifyContent: "flex-end",
};

const primaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "7px 14px",
  background: "#0369a1",
  border: "1px solid #0369a1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};

const successStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: 12,
  border: "1px solid #bbf7d0",
  borderRadius: 6,
  background: "#f0fdf4",
  color: "#14532d",
  fontSize: 12,
};
