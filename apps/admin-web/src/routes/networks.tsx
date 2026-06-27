/**
 * Operator Networks CRUD module (SAUI-07).
 *
 * Backed by /v1/operator-networks (see
 * apps/backend/internal/platform/httpserver/networks.go):
 *
 *   GET    /v1/operator-networks            list (network.read)
 *   POST   /v1/operator-networks            create (network.create)
 *   PATCH  /v1/operator-networks/{id}       update (network.update)
 *   POST   /v1/operator-networks/{id}/archive  archive (network.archive)
 *
 * Endpoints are NOT under /v1/admin/*, so no X-Admin-Reason header is
 * required — the reason resolver is bypassed for this surface.
 *
 * Permission gating:
 *   - The route mount is wrapped in <RequirePermission /> for the
 *     "networks" nav entry (network.read OR network.create).
 *   - Create / Edit / Archive buttons are individually gated against
 *     `network.create`, `network.update`, `network.archive` so that
 *     operators with only `network.read` see a read-only view.
 *
 * Slug validation mirrors operatorNetworkSlugRE in the backend:
 *
 *     ^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$
 *
 * Validation runs locally to give immediate feedback; the backend still
 * authoritatively re-checks and any operator_network.invalid_slug /
 * operator_network.duplicate_slug envelope is rendered inline next to
 * the slug field.
 *
 * Archive uses POST /{id}/archive — destructive DELETE is intentionally
 * NOT used. The backend soft-archives by setting archived_at and
 * status='archived'.
 *
 * Mock data: NONE. The list, form, and archive flow all hit the live
 * backend. No globalThis / devStore / mockDb.
 */
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
import { ApiError, authedFetch } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import { useAuth } from "@/lib/auth/useAuth";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/networks",
  component: NetworksRoute,
});

// ---------------------------------------------------------------------------
// Backend response shapes
// ---------------------------------------------------------------------------

export interface OperatorNetwork {
  readonly id: string;
  readonly name: string;
  readonly slug: string;
  readonly status: string;
  readonly archived_at: string | null;
  readonly created_at: string;
  readonly updated_at: string;
}

interface OperatorNetworkListEnvelope {
  readonly operator_networks: readonly OperatorNetwork[];
  readonly total: number;
}

interface OperatorNetworkEnvelope {
  readonly operator_network: OperatorNetwork;
}

// ---------------------------------------------------------------------------
// Slug validation (mirrors backend regex)
// ---------------------------------------------------------------------------

/**
 * Matches the Go regex in networks.go: operatorNetworkSlugRE. Lowercase
 * alphanumerics and hyphens, must start and end with [a-z0-9], 1-64 chars.
 */
export const OPERATOR_NETWORK_SLUG_RE =
  /^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$/;

export function validateNetworkSlug(slug: string): string | null {
  if (slug === "") {
    return "Slug is required";
  }
  if (slug.length > 64) {
    return "Slug must be at most 64 characters";
  }
  if (!OPERATOR_NETWORK_SLUG_RE.test(slug)) {
    return "Slug must be lowercase letters, digits, or hyphens; start and end with [a-z0-9]";
  }
  return null;
}

export function validateNetworkName(name: string): string | null {
  if (name.trim() === "") {
    return "Name is required";
  }
  if (name.trim().length > 200) {
    return "Name must be at most 200 characters";
  }
  return null;
}

// ---------------------------------------------------------------------------
// Nav entry binding
// ---------------------------------------------------------------------------

const NETWORKS_NAV_ENTRY = NAV_BY_PATH["/networks"];
if (NETWORKS_NAV_ENTRY === undefined) {
  throw new Error("networks route: NAV_BY_PATH['/networks'] missing");
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

function NetworksRoute() {
  return (
    <RequirePermission entry={NETWORKS_NAV_ENTRY}>
      <NetworksModule />
    </RequirePermission>
  );
}

type FormMode =
  | { kind: "closed" }
  | { kind: "create" }
  | { kind: "edit"; network: OperatorNetwork };

function NetworksModule() {
  const { permissions } = useAuth();
  const canCreate = permissions.has("network.create");
  const canUpdate = permissions.has("network.update");
  const canArchive = permissions.has("network.archive");

  const [form, setForm] = useState<FormMode>({ kind: "closed" });
  const [pendingArchive, setPendingArchive] = useState<OperatorNetwork | null>(
    null,
  );

  const query = useQuery<OperatorNetworkListEnvelope, ApiError>({
    queryKey: ["operator-networks", "list"],
    queryFn: () =>
      authedFetch<OperatorNetworkListEnvelope>({
        method: "GET",
        path: "/v1/operator-networks",
      }),
    retry: (failureCount, err) => {
      if (err instanceof ApiError) {
        if (err.status === 401 || err.status === 403 || err.status === 0) {
          return false;
        }
        if (err.code === "permissions.denied") {
          return false;
        }
      }
      return failureCount < 2;
    },
    refetchOnWindowFocus: false,
  });

  const rows = query.data?.operator_networks ?? [];
  const sorted = useMemo(
    () => [...rows].sort((a, b) => a.name.localeCompare(b.name)),
    [rows],
  );

  return (
    <section aria-labelledby="networks-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="networks-heading" style={headingStyle}>
            Operator Networks
          </h1>
          <p style={subheadingStyle}>
            Manage operator networks. Networks group operators that work
            across multiple organizations as organizers or agents. Slugs
            must match{" "}
            <code style={monoStyle}>{OPERATOR_NETWORK_SLUG_RE.source}</code>.
          </p>
        </div>
        <div style={refreshWrapStyle}>
          <button
            type="button"
            onClick={() => query.refetch()}
            style={refreshButtonStyle}
            disabled={query.isFetching}
            data-testid="networks-refresh"
          >
            {query.isFetching ? "Refreshing…" : "Refresh"}
          </button>
          {canCreate ? (
            <button
              type="button"
              onClick={() => setForm({ kind: "create" })}
              style={primaryButtonStyle}
              data-testid="networks-new"
            >
              New network
            </button>
          ) : (
            <span style={mutedHintStyle} title="Requires network.create">
              Create requires network.create
            </span>
          )}
        </div>
      </header>

      <NetworksBody
        query={query}
        rows={sorted}
        canUpdate={canUpdate}
        canArchive={canArchive}
        onEdit={(network) => setForm({ kind: "edit", network })}
        onArchive={(network) => setPendingArchive(network)}
      />

      {form.kind !== "closed" ? (
        <NetworkFormDialog
          mode={form}
          onClose={() => setForm({ kind: "closed" })}
        />
      ) : null}

      {pendingArchive !== null ? (
        <ArchiveConfirmDialog
          network={pendingArchive}
          onClose={() => setPendingArchive(null)}
        />
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Table body and states
// ---------------------------------------------------------------------------

interface BodyProps {
  query: ReturnType<typeof useQuery<OperatorNetworkListEnvelope, ApiError>>;
  rows: readonly OperatorNetwork[];
  canUpdate: boolean;
  canArchive: boolean;
  onEdit: (network: OperatorNetwork) => void;
  onArchive: (network: OperatorNetwork) => void;
}

function NetworksBody({ query, rows, canUpdate, canArchive, onEdit, onArchive }: BodyProps) {
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading operator networks from /v1/operator-networks…
      </div>
    );
  }
  if (query.isError) {
    return <NetworksErrorState error={query.error} onRetry={() => query.refetch()} />;
  }
  if (rows.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="networks-empty">
        No operator networks exist yet. Create the first network to begin
        assigning operators and organizations.
      </div>
    );
  }
  return (
    <div style={tableWrapStyle} role="region" aria-label="Operator networks">
      <table style={tableStyle} data-testid="networks-table">
        <thead>
          <tr>
            <th scope="col" style={thStyle}>Name</th>
            <th scope="col" style={thStyle}>Slug</th>
            <th scope="col" style={thStyle}>Status</th>
            <th scope="col" style={thStyle}>Created</th>
            <th scope="col" style={thStyle}>Updated</th>
            <th scope="col" style={thStyle} aria-label="Actions" />
          </tr>
        </thead>
        <tbody>
          {rows.map((n) => (
            <tr key={n.id} data-testid={`networks-row-${n.slug}`}>
              <td style={tdStyle}>{n.name}</td>
              <td style={tdMonoStyle}>{n.slug}</td>
              <td style={tdStyle}>
                <StatusBadge status={n.status} />
              </td>
              <td style={tdStyle}>{formatDate(n.created_at)}</td>
              <td style={tdStyle}>{formatDate(n.updated_at)}</td>
              <td style={tdActionsStyle}>
                {canUpdate && n.status !== "archived" ? (
                  <button
                    type="button"
                    style={rowActionButtonStyle}
                    onClick={() => onEdit(n)}
                    data-testid={`networks-edit-${n.slug}`}
                  >
                    Edit
                  </button>
                ) : null}
                {canArchive && n.status !== "archived" ? (
                  <button
                    type="button"
                    style={rowDangerButtonStyle}
                    onClick={() => onArchive(n)}
                    data-testid={`networks-archive-${n.slug}`}
                  >
                    Archive
                  </button>
                ) : null}
                {!canUpdate && !canArchive ? (
                  <span style={mutedHintStyle}>read-only</span>
                ) : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function StatusBadge({ status }: { status: string }) {
  let style = badgeNeutralStyle;
  if (status === "active") {
    style = badgeActiveStyle;
  } else if (status === "archived") {
    style = badgeArchivedStyle;
  } else if (status === "suspended") {
    style = badgeSuspendedStyle;
  }
  return <span style={style}>{status}</span>;
}

function NetworksErrorState({
  error,
  onRetry,
}: {
  error: ApiError | null;
  onRetry: () => void;
}) {
  if (
    error instanceof ApiError &&
    (error.status === 403 || error.code === "permissions.denied")
  ) {
    return (
      <div style={errorBoxStyle} role="alert" data-testid="networks-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>network.read</code>.
          Ask a platform administrator to grant the permission.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div style={errorBoxStyle} role="status" data-testid="networks-session-expired">
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload operator networks.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="networks-error">
      <strong>Failed to load operator networks.</strong>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? <div style={errorParaStyle}>{error.message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Create / Edit modal
// ---------------------------------------------------------------------------

interface FormDialogProps {
  mode: Extract<FormMode, { kind: "create" } | { kind: "edit" }>;
  onClose: () => void;
}

interface ServerFieldErrors {
  name?: string;
  slug?: string;
  form?: string;
}

function NetworkFormDialog({ mode, onClose }: FormDialogProps) {
  const queryClient = useQueryClient();
  const isEdit = mode.kind === "edit";
  const initialName = isEdit ? mode.network.name : "";
  const initialSlug = isEdit ? mode.network.slug : "";

  const [name, setName] = useState(initialName);
  const [slug, setSlug] = useState(initialSlug);
  const [serverErrors, setServerErrors] = useState<ServerFieldErrors>({});

  const nameErr = validateNetworkName(name);
  const slugErr = validateNetworkSlug(slug);
  const localValid = nameErr === null && slugErr === null;

  // For edit, allow submission when at least one field changed.
  const dirty = !isEdit || name.trim() !== initialName || slug !== initialSlug;

  const mutation = useMutation<OperatorNetworkEnvelope, ApiError, void>({
    mutationFn: () => {
      if (isEdit) {
        const body: Record<string, string> = {};
        if (name.trim() !== initialName) {
          body.name = name.trim();
        }
        if (slug !== initialSlug) {
          body.slug = slug;
        }
        return authedFetch<OperatorNetworkEnvelope>({
          method: "PATCH",
          path: `/v1/operator-networks/${mode.network.id}`,
          body,
        });
      }
      return authedFetch<OperatorNetworkEnvelope>({
        method: "POST",
        path: "/v1/operator-networks",
        body: { name: name.trim(), slug },
      });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["operator-networks"] });
      onClose();
    },
    onError: (err) => {
      setServerErrors(mapServerError(err));
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setServerErrors({});
    if (!localValid || !dirty) {
      return;
    }
    mutation.mutate();
  }

  const title = isEdit ? `Edit ${mode.network.name}` : "New operator network";
  const submitLabel = isEdit ? "Save changes" : "Create network";

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="networks-form-title"
      style={dialogBackdropStyle}
      data-testid="networks-form-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="networks-form-title" style={dialogTitleStyle}>
            {title}
          </h2>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="networks-form-close"
          >
            ×
          </button>
        </header>
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          <FieldRow
            label="Name"
            htmlFor="network-name"
            error={serverErrors.name ?? null}
            localError={name.length > 0 ? nameErr : null}
            hint="Operator-visible name. Required."
          >
            <input
              id="network-name"
              type="text"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (serverErrors.name !== undefined) {
                  setServerErrors({ ...serverErrors, name: undefined });
                }
              }}
              style={inputStyle}
              required
              maxLength={200}
              data-testid="networks-form-name"
              autoFocus={!isEdit}
            />
          </FieldRow>
          <FieldRow
            label="Slug"
            htmlFor="network-slug"
            error={serverErrors.slug ?? null}
            localError={slug.length > 0 ? slugErr : null}
            hint={
              <>
                Lowercase letters, digits, and hyphens. Must start and end
                with <code style={monoStyle}>[a-z0-9]</code>. Max 64 chars.
              </>
            }
          >
            <input
              id="network-slug"
              type="text"
              value={slug}
              onChange={(e) => {
                setSlug(e.target.value.toLowerCase());
                if (serverErrors.slug !== undefined) {
                  setServerErrors({ ...serverErrors, slug: undefined });
                }
              }}
              style={inputMonoStyle}
              required
              maxLength={64}
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-testid="networks-form-slug"
            />
          </FieldRow>

          {serverErrors.form !== undefined ? (
            <div style={formErrorStyle} role="alert" data-testid="networks-form-error">
              {serverErrors.form}
            </div>
          ) : null}

          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="networks-form-cancel"
            >
              Cancel
            </button>
            <button
              type="submit"
              style={primaryButtonStyle}
              disabled={!localValid || !dirty || mutation.isPending}
              data-testid="networks-form-submit"
            >
              {mutation.isPending ? "Saving…" : submitLabel}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function FieldRow({
  label,
  htmlFor,
  error,
  localError,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  error: string | null;
  localError: string | null;
  hint: ReactNode;
  children: ReactNode;
}) {
  const visibleError = error ?? localError;
  return (
    <div style={fieldRowStyle}>
      <label htmlFor={htmlFor} style={fieldLabelStyle}>
        {label}
      </label>
      {children}
      {visibleError !== null ? (
        <div style={fieldErrorStyle} role="alert" data-testid={`${htmlFor}-error`}>
          {visibleError}
        </div>
      ) : (
        <div style={fieldHintStyle}>{hint}</div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Archive confirm dialog
// ---------------------------------------------------------------------------

function ArchiveConfirmDialog({
  network,
  onClose,
}: {
  network: OperatorNetwork;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [error, setError] = useState<ApiError | null>(null);

  const mutation = useMutation<OperatorNetworkEnvelope, ApiError, void>({
    mutationFn: () =>
      authedFetch<OperatorNetworkEnvelope>({
        method: "POST",
        path: `/v1/operator-networks/${network.id}/archive`,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["operator-networks"] });
      onClose();
    },
    onError: (err) => setError(err),
  });

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="networks-archive-title"
      style={dialogBackdropStyle}
      data-testid="networks-archive-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="networks-archive-title" style={dialogTitleStyle}>
            Archive {network.name}?
          </h2>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="networks-archive-close"
          >
            ×
          </button>
        </header>
        <div style={archiveBodyStyle}>
          <p style={archiveParaStyle}>
            Archiving an operator network removes it from the active list
            and prevents new operator / organization assignments. Existing
            roster and attachment rows remain on the archived record for
            audit purposes. The slug{" "}
            <code style={monoStyle}>{network.slug}</code> becomes available
            for reuse by a NEW network.
          </p>
          <p style={archiveParaStyle}>
            This is a <strong>soft-archive</strong>: the row is preserved in
            the database with <code style={monoStyle}>status = archived</code>.
            It is not a destructive delete.
          </p>
          {error !== null ? (
            <div style={formErrorStyle} role="alert" data-testid="networks-archive-error">
              {error.message} (<code style={monoStyle}>{error.code}</code>)
            </div>
          ) : null}
          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="networks-archive-cancel"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => mutation.mutate()}
              style={dangerButtonStyle}
              disabled={mutation.isPending}
              data-testid="networks-archive-confirm"
            >
              {mutation.isPending ? "Archiving…" : "Archive"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Server error mapping
// ---------------------------------------------------------------------------

/**
 * Map an envelope from networks.go onto field-level errors. The handler
 * emits `details.field = "name" | "slug"` for invalid_name/invalid_slug;
 * duplicate_slug is unconditionally a slug-field error.
 */
export function mapServerError(err: ApiError): ServerFieldErrors {
  const out: ServerFieldErrors = {};
  const field =
    err.details !== undefined && typeof err.details.field === "string"
      ? err.details.field
      : undefined;
  switch (err.code) {
    case "operator_network.invalid_name":
      out.name = err.message;
      return out;
    case "operator_network.invalid_slug":
      out.slug = err.message;
      return out;
    case "operator_network.duplicate_slug":
      out.slug = err.message;
      return out;
    case "operator_network.no_changes":
      out.form = err.message;
      return out;
    case "operator_network.not_found":
      out.form = err.message;
      return out;
    case "permissions.denied":
      out.form =
        "Your account is missing the required permission. Ask a platform administrator.";
      return out;
    default:
      if (field === "name") {
        out.name = err.message;
      } else if (field === "slug") {
        out.slug = err.message;
      } else {
        out.form = `${err.message} (${err.code})`;
      }
      return out;
  }
}

// ---------------------------------------------------------------------------
// Format helpers
// ---------------------------------------------------------------------------

function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  return d.toISOString().slice(0, 10);
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

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
  letterSpacing: -0.2,
};

const subheadingStyle: CSSProperties = {
  margin: "4px 0 0 0",
  fontSize: 13,
  color: "#475569",
  maxWidth: 720,
  lineHeight: 1.45,
};

const refreshWrapStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "center",
  flexWrap: "wrap",
};

const refreshButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const primaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#0369a1",
  border: "1px solid #0369a1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};

const secondaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const dangerButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#b91c1c",
  border: "1px solid #b91c1c",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};

const rowActionButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const rowDangerButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 10px",
  background: "#ffffff",
  border: "1px solid #fca5a5",
  borderRadius: 4,
  cursor: "pointer",
  color: "#7f1d1d",
};

const mutedHintStyle: CSSProperties = {
  fontSize: 11,
  color: "#94a3b8",
  fontStyle: "italic",
};

const tableWrapStyle: CSSProperties = {
  overflowX: "auto",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const tableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 13,
};

const thStyle: CSSProperties = {
  textAlign: "left",
  padding: "10px 12px",
  borderBottom: "1px solid #e2e8f0",
  background: "#f8fafc",
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const tdStyle: CSSProperties = {
  padding: "10px 12px",
  borderBottom: "1px solid #f1f5f9",
  color: "#0f172a",
  verticalAlign: "middle",
};

const tdMonoStyle: CSSProperties = {
  ...tdStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
  color: "#334155",
};

const tdActionsStyle: CSSProperties = {
  ...tdStyle,
  display: "flex",
  gap: 6,
  flexWrap: "wrap",
};

const badgeBaseStyle: CSSProperties = {
  fontSize: 10,
  padding: "2px 6px",
  borderRadius: 999,
  fontWeight: 600,
  textTransform: "uppercase",
  letterSpacing: 0.4,
  display: "inline-block",
};
const badgeActiveStyle: CSSProperties = {
  ...badgeBaseStyle,
  background: "#dcfce7",
  color: "#166534",
};
const badgeArchivedStyle: CSSProperties = {
  ...badgeBaseStyle,
  background: "#fee2e2",
  color: "#7f1d1d",
};
const badgeSuspendedStyle: CSSProperties = {
  ...badgeBaseStyle,
  background: "#fef3c7",
  color: "#78350f",
};
const badgeNeutralStyle: CSSProperties = {
  ...badgeBaseStyle,
  background: "#e2e8f0",
  color: "#334155",
};

const statusBoxStyle: CSSProperties = {
  padding: 16,
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  fontSize: 12,
  color: "#475569",
};

const errorBoxStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: 16,
  border: "1px solid #fca5a5",
  borderRadius: 6,
  background: "#fef2f2",
  color: "#7f1d1d",
  fontSize: 12,
};
const errorParaStyle: CSSProperties = { margin: 0, fontSize: 12 };
const errorCodeStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
};
const errorRetryStyle: CSSProperties = {
  alignSelf: "flex-start",
  fontSize: 12,
  padding: "6px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const dialogBackdropStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  background: "rgba(15, 23, 42, 0.4)",
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  padding: 16,
  zIndex: 100,
};

const dialogStyle: CSSProperties = {
  background: "#ffffff",
  borderRadius: 8,
  border: "1px solid #e2e8f0",
  boxShadow: "0 10px 25px rgba(15, 23, 42, 0.2)",
  width: "min(520px, 100%)",
  maxHeight: "90vh",
  overflowY: "auto",
};

const dialogHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  justifyContent: "space-between",
  padding: "12px 16px",
  borderBottom: "1px solid #e2e8f0",
};

const dialogTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 16,
  fontWeight: 600,
  color: "#0f172a",
};

const dialogCloseStyle: CSSProperties = {
  background: "transparent",
  border: "none",
  fontSize: 22,
  lineHeight: 1,
  cursor: "pointer",
  color: "#64748b",
  padding: "0 4px",
};

const formStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
  padding: 16,
};

const fieldRowStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const fieldLabelStyle: CSSProperties = {
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

const fieldHintStyle: CSSProperties = {
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
  fontSize: 12,
  padding: 8,
  background: "#fef2f2",
  border: "1px solid #fca5a5",
  color: "#7f1d1d",
  borderRadius: 4,
};

const formActionsStyle: CSSProperties = {
  display: "flex",
  justifyContent: "flex-end",
  gap: 8,
};

const archiveBodyStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
  padding: 16,
};

const archiveParaStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  color: "#334155",
  lineHeight: 1.5,
};

const monoStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};
