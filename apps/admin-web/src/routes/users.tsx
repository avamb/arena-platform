import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useMemo,
  useState,
  type CSSProperties,
  type FormEvent,
  type ReactNode,
} from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ResponsiveDrawer, ResponsiveTable, type ResponsiveTableColumn } from "@/components/layout";
import { ApiError, authedFetch, createAdminUser } from "@/lib/api/client";
import type {
  AdminCreateUserRequest,
  AdminCreateUserResponse,
} from "@/lib/api/types";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { orgContextFromLocation } from "@/lib/routing/orgContext";

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

// Minimal slice of the /v1/admin/organizations envelope used by the
// organization picker (AB-17). Kept local — the full shape lives in
// routes/organizations.tsx and this form only needs id/name/slug.
interface OrgPickerOption {
  readonly id: string;
  readonly display_number: number;
  readonly name: string;
  readonly slug: string;
  readonly deleted_at: string | null;
}

interface OrgPickerEnvelope {
  readonly organizations: readonly OrgPickerOption[];
  readonly total: number;
}

export interface AdminDirectoryMembership { readonly id: string; readonly org_id: string; readonly name: string; readonly slug: string; readonly role: string; }
export interface AdminDirectoryUser { readonly id: string; readonly display_number: number; readonly email: string; readonly created_at: string; readonly email_verified_at: string | null; readonly deactivated_at: string | null; readonly global_roles: readonly string[]; readonly memberships: readonly AdminDirectoryMembership[]; }
export interface AdminDirectoryEnvelope { readonly users: readonly AdminDirectoryUser[]; readonly total: number; readonly limit: number; readonly offset: number; }

function UsersRoute() {
  return (
    <RequirePermission entry={USERS_NAV_ENTRY}>
      <UsersProvisioning />
    </RequirePermission>
  );
}

function UsersProvisioning() {
  const queryClient = useQueryClient();
  const deepLinkedOrgID = orgContextFromLocation();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<AdminUserRole>(
    deepLinkedOrgID === "" ? "platform_operator" : "organizer",
  );
  const [orgId, setOrgId] = useState(deepLinkedOrgID);
  const [locale, setLocale] = useState("en");
  const [localErrors, setLocalErrors] = useState<CreateUserErrors>({});
  const [serverErrors, setServerErrors] = useState<CreateUserErrors>({});
  const [created, setCreated] = useState<AdminCreateUserResponse | null>(null);
  const [search, setSearch] = useState("");
  const [submittedSearch, setSubmittedSearch] = useState("");
  const [selectedUser, setSelectedUser] = useState<AdminDirectoryUser | null>(null);

  const orgScoped = isOrgScopedAdminRole(role);
  const visibleErrors = useMemo(
    () => ({ ...localErrors, ...serverErrors }),
    [localErrors, serverErrors],
  );

  // AB-17: feed the organization picker. When the list cannot be loaded the
  // form falls back to the raw UUID input so provisioning is never blocked.
  const orgsQuery = useQuery<OrgPickerEnvelope, ApiError>({
    queryKey: ["admin", "organizations", "picker"],
    queryFn: () =>
      authedFetch<OrgPickerEnvelope>({
        method: "GET",
        path: "/v1/admin/organizations",
      }),
    enabled: orgScoped,
    staleTime: 60_000,
    retry: false,
  });
  const orgOptions = useMemo(
    () =>
      (orgsQuery.data?.organizations ?? []).filter(
        (o) => o.deleted_at === null,
      ),
    [orgsQuery.data?.organizations],
  );
  const showOrgSelect = orgsQuery.isSuccess && orgOptions.length > 0;
  const usersQuery = useQuery<AdminDirectoryEnvelope, ApiError>({
    queryKey: ["admin", "users", submittedSearch],
    queryFn: () => authedFetch<AdminDirectoryEnvelope>({ method: "GET", path: buildAdminUserDirectoryPath(submittedSearch) }),
    retry: false,
  });
  const refreshDirectoryAfterChange = async (): Promise<void> => {
    // Keep the drawer in sync with the server response as well as the table.
    // In particular, a lifecycle change swaps its Deactivate/Reactivate action
    // while the drawer remains open.
    const result = await usersQuery.refetch();
    if (result.data === undefined) {
      return;
    }
    setSelectedUser((current) => {
      if (current === null) {
        return null;
      }
      return result.data.users.find((candidate) => candidate.id === current.id) ?? null;
    });
  };

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
      // The directory is real server data; refresh it after provisioning so
      // the newly created account appears without a manual page reload.
      void queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
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
            {deepLinkedOrgID !== "" ? " This organization was preselected from the organization directory." : ""}
          </p>
        </div>
      </header>

      <section aria-labelledby="user-directory-heading" style={directoryStyle}>
        <div style={directoryHeaderStyle}>
          <div><h2 id="user-directory-heading" style={directoryHeadingStyle}>User directory</h2><p style={subheadingStyle}>Search users and review their current access.</p></div>
          <form onSubmit={(event) => { event.preventDefault(); setSubmittedSearch(search.trim()); }} style={searchFormStyle}>
            <input aria-label="Search users by email" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Search email" style={inputStyle} data-testid="users-search" />
            <button type="submit" style={secondaryButtonStyle}>Search</button>
          </form>
        </div>
        {usersQuery.isPending ? <p role="status">Loading users…</p> : null}
        {usersQuery.isError ? <p role="alert" style={formErrorStyle}>Unable to load users: {usersQuery.error.message}</p> : null}
        {usersQuery.isSuccess ? <UserDirectoryTable users={usersQuery.data.users} onSelect={setSelectedUser} /> : null}
      </section>

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
            label="Organization"
            htmlFor="users-org-id"
            error={visibleErrors.orgId}
            hint={
              showOrgSelect
                ? "Required for organizer, agent, network operator, and external operator."
                : orgsQuery.isPending
                  ? "Loading organizations..."
                  : "Organization list unavailable — paste the organization UUID (Organizations → Details → ID)."
            }
          >
            {showOrgSelect ? (
              <select
                id="users-org-id"
                value={orgId}
                onChange={(e) => setOrgId(e.target.value)}
                style={inputStyle}
                data-testid="users-org-id"
              >
                <option value="">— Select organization —</option>
                {orgOptions.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.name} · #{o.display_number} ({o.slug})
                  </option>
                ))}
              </select>
            ) : (
              <input
                id="users-org-id"
                type="text"
                value={orgId}
                onChange={(e) => setOrgId(e.target.value)}
                style={inputMonoStyle}
                autoComplete="off"
                spellCheck={false}
                disabled={orgsQuery.isPending}
                data-testid="users-org-id"
              />
            )}
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
      <UserDirectoryDrawer user={selectedUser} organizations={orgOptions} onClose={() => setSelectedUser(null)} onChanged={refreshDirectoryAfterChange} />
    </section>
  );
}

function UserDirectoryTable({ users, onSelect }: { users: readonly AdminDirectoryUser[]; onSelect: (user: AdminDirectoryUser) => void }) {
  const columns: readonly ResponsiveTableColumn<AdminDirectoryUser>[] = [
    { id: "email", header: "Email", primary: true, renderCell: (user) => <button type="button" onClick={() => onSelect(user)} style={linkButtonStyle}>{user.email} · #{user.display_number}</button> },
    { id: "status", header: "Status", renderCell: (user) => user.deactivated_at === null ? "Active" : "Deactivated" },
    { id: "verified", header: "Verified", renderCell: (user) => user.email_verified_at === null ? "No" : "Yes" },
    { id: "roles", header: "Global roles", renderCell: (user) => user.global_roles.length === 0 ? "—" : user.global_roles.map(formatAdminUserRole).join(", ") },
    { id: "memberships", header: "Memberships", renderCell: (user) => user.memberships.length === 0 ? "—" : `${user.memberships.length} organization${user.memberships.length === 1 ? "" : "s"}` },
    { id: "created", header: "Created", renderCell: (user) => formatDateTime(user.created_at) },
  ];
  return <ResponsiveTable id="users-directory-table" caption="User directory" columns={columns} rows={users} rowKey={(user) => user.id} empty={<p>No users match this search.</p>} />;
}

function UserDirectoryDrawer({ user, organizations, onClose, onChanged }: { user: AdminDirectoryUser | null; organizations: readonly OrgPickerOption[]; onClose: () => void; onChanged: () => Promise<void> }) {
	const [globalRole, setGlobalRole] = useState<AdminUserRole>("platform_operator");
	const [membershipRole, setMembershipRole] = useState<AdminUserRole>("organizer");
	const [membershipOrgID, setMembershipOrgID] = useState("");
	const [error, setError] = useState<string | null>(null);
	const mutation = useMutation<void, ApiError, { method: "POST" | "DELETE"; path: string; body?: unknown }>({ mutationFn: ({ method, path, body }) => authedFetch<void>({ method, path, body }), onSuccess: async () => { setError(null); await onChanged(); }, onError: (err) => setError(err.message) });
	const confirm = (message: string, run: () => void) => { if (window.confirm(message)) run(); };
  return <ResponsiveDrawer id="user-directory-drawer" open={user !== null} onClose={onClose} closeLabel="Close user" title={user?.email ?? "User"} subtitle={user === null ? undefined : `Created ${formatDateTime(user.created_at)}`}>
    {user === null ? null : <div style={drawerContentStyle}>
      <div><strong>Global roles</strong><p>{user.global_roles.length === 0 ? "No global roles" : user.global_roles.map(formatAdminUserRole).join(", ")}</p><div style={drawerActionStyle}>{user.global_roles.map((role) => <button key={role} type="button" style={secondaryButtonStyle} disabled={mutation.isPending} onClick={() => confirm(`Remove ${formatAdminUserRole(role)} from ${user.email}?`, () => mutation.mutate({ method: "DELETE", path: `/v1/admin/users/${user.id}/global-roles/${encodeURIComponent(role)}` }))}>Remove {formatAdminUserRole(role)}</button>)}<select aria-label="Global role" value={globalRole} onChange={(e) => setGlobalRole(e.target.value as AdminUserRole)} style={inputStyle}>{GLOBAL_USER_ROLES.map((role) => <option key={role} value={role}>{formatAdminUserRole(role)}</option>)}</select><button type="button" style={secondaryButtonStyle} disabled={mutation.isPending} onClick={() => confirm(`Grant ${formatAdminUserRole(globalRole)} to ${user.email}?`, () => mutation.mutate({ method: "POST", path: `/v1/admin/users/${user.id}/global-roles`, body: { role: globalRole } }))}>Add role</button></div></div>
      <div><strong>Organization memberships</strong>{user.memberships.length === 0 ? <p>No active organization memberships.</p> : <ul>{user.memberships.map((membership) => <li key={`${membership.id}-${membership.role}`}>{membership.name} ({membership.slug}) — {formatAdminUserRole(membership.role)}</li>)}</ul>}</div>
      <div style={drawerActionStyle}><select aria-label="Membership organization" value={membershipOrgID} onChange={(e) => setMembershipOrgID(e.target.value)} style={inputStyle}><option value="">Select organization</option>{organizations.map((org) => <option key={org.id} value={org.id}>{org.name} · #{org.display_number}</option>)}</select><select aria-label="Membership role" value={membershipRole} onChange={(e) => setMembershipRole(e.target.value as AdminUserRole)} style={inputStyle}>{ORG_SCOPED_USER_ROLES.map((role) => <option key={role} value={role}>{formatAdminUserRole(role)}</option>)}</select><button type="button" style={secondaryButtonStyle} disabled={mutation.isPending || membershipOrgID === ""} onClick={() => confirm(`Add ${formatAdminUserRole(membershipRole)} membership to ${user.email}?`, () => mutation.mutate({ method: "POST", path: `/v1/admin/organizations/${membershipOrgID}/members`, body: { user_id: user.id, role: membershipRole } }))}>Add membership</button></div>
      <div style={drawerActionStyle}>{user.memberships.map((membership) => <button key={membership.id} type="button" style={secondaryButtonStyle} disabled={mutation.isPending} onClick={() => confirm(`Remove ${formatAdminUserRole(membership.role)} membership in ${membership.name}?`, () => mutation.mutate({ method: "DELETE", path: `/v1/admin/organizations/${membership.org_id}/members/${membership.id}` }))}>Remove {membership.name} membership</button>)}</div>
      <div><strong>Email verified</strong><p>{user.email_verified_at === null ? "Not verified" : formatDateTime(user.email_verified_at)}</p></div>
      <div><strong>Account status</strong><p>{user.deactivated_at === null ? "Active — this user can sign in." : `Deactivated ${formatDateTime(user.deactivated_at)} — sign-in and sessions are blocked.`}</p><div style={drawerActionStyle}>{user.deactivated_at === null ? <button type="button" style={secondaryButtonStyle} disabled={mutation.isPending} onClick={() => confirm(`Deactivate ${user.email}? This immediately revokes their sessions and blocks sign-in.`, () => mutation.mutate({ method: "POST", path: `/v1/admin/users/${user.id}/deactivate` }))}>Deactivate user</button> : <button type="button" style={secondaryButtonStyle} disabled={mutation.isPending} onClick={() => confirm(`Reactivate ${user.email}? They will need to sign in again.`, () => mutation.mutate({ method: "POST", path: `/v1/admin/users/${user.id}/reactivate` }))}>Reactivate user</button>}<button type="button" style={{ ...secondaryButtonStyle, color: "#b91c1c", borderColor: "#fca5a5" }} disabled={mutation.isPending} onClick={() => confirm(`Delete ${user.email}? Accounts with retained activity are deactivated instead. This action cannot be undone when deletion is allowed.`, () => mutation.mutate({ method: "DELETE", path: `/v1/admin/users/${user.id}` }))}>Delete user</button></div></div>
      {error === null ? null : <p role="alert" style={formErrorStyle}>{error}</p>}
    </div>}
  </ResponsiveDrawer>;
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

export function buildAdminUserDirectoryPath(search: string): string {
  return `/v1/admin/users?limit=50&search=${encodeURIComponent(search.trim())}`;
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

const directoryStyle: CSSProperties = {
  padding: 16,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const directoryHeaderStyle: CSSProperties = {
  display: "flex",
  justifyContent: "space-between",
  gap: 12,
  alignItems: "flex-start",
  flexWrap: "wrap",
  marginBottom: 12,
};

const directoryHeadingStyle: CSSProperties = { margin: 0, fontSize: 16, fontWeight: 600 };
const searchFormStyle: CSSProperties = { display: "flex", gap: 8, flexWrap: "wrap" };
const secondaryButtonStyle: CSSProperties = { fontSize: 12, padding: "7px 14px", background: "#ffffff", border: "1px solid #94a3b8", borderRadius: 4, cursor: "pointer", color: "#0f172a", fontWeight: 600 };
const drawerActionStyle: CSSProperties = { display: "flex", flexWrap: "wrap", gap: 8, alignItems: "center" };
const linkButtonStyle: CSSProperties = { padding: 0, border: 0, background: "transparent", color: "#0369a1", cursor: "pointer", font: "inherit", fontWeight: 600, textAlign: "left" };
const drawerContentStyle: CSSProperties = { display: "flex", flexDirection: "column", gap: 16, color: "#334155", fontSize: 13 };
