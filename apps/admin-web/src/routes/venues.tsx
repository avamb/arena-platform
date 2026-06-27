/**
 * Venues CRUD module (feature #242).
 *
 * Replaces the legacy /venues SAUI-12 placeholder shell with a real
 * end-to-end CRUD screen backed by the venues API in
 * apps/backend/internal/platform/httpserver/venues.go:
 *
 *   GET    /v1/venues                                  list (venue.read)
 *   GET    /v1/venues/{id}                             get  (venue.read)
 *   POST   /v1/organizations/{org_id}/venues           create (venue.create, owner-gated)
 *   PATCH  /v1/organizations/{org_id}/venues/{id}      update (venue.update, owner-gated)
 *   DELETE /v1/organizations/{org_id}/venues/{id}      soft-delete (venue.delete, owner-gated)
 *
 * Reads are platform-wide (any authenticated org may read any active
 * venue); writes are owner-gated -- the org_id in the URL path MUST
 * match the venue's owning org. For create, the form requires the
 * caller to supply org_id (prefilled from the active scope when it is
 * an "organization" scope). For edit/delete, the venue itself carries
 * org_id so the UI never has to ask.
 *
 * Permission gating:
 *   - Route wrapped in <RequirePermission /> using the "venues" nav
 *     entry (anyOf venue.read | venue.create | venue.update |
 *     venue.delete | superadmin.read).
 *   - Create / Edit / Delete buttons are individually gated against
 *     venue.create / venue.update / venue.delete so an operator with
 *     only venue.read sees a read-only view.
 *
 * Delete uses DELETE /v1/organizations/{org_id}/venues/{id} which is a
 * SOFT-DELETE on the backend (sets deleted_at). A confirm dialog is
 * shown before the call. "Archive" in operator language ==
 * soft-delete on the wire.
 *
 * Mock data: NONE. List, form, and delete all hit the live backend.
 * No globalThis / devStore / mockDb.
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
import { useScope } from "@/lib/auth/ScopeContext";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/venues",
  component: VenuesRoute,
});

// ---------------------------------------------------------------------------
// Backend response shapes
// ---------------------------------------------------------------------------

export interface Venue {
  readonly id: string;
  readonly org_id: string;
  readonly city_id: string | null;
  readonly name: string;
  readonly address: string | null;
  readonly capacity_default: number | null;
  readonly created_at: string;
  readonly updated_at: string;
}

interface VenueListEnvelope {
  readonly venues: readonly Venue[];
}

interface VenueEnvelope {
  readonly venue: Venue;
}

// ---------------------------------------------------------------------------
// UUID validation (mirrors backend uuid.Parse contract)
// ---------------------------------------------------------------------------

export const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function validateVenueName(name: string): string | null {
  const trimmed = name.trim();
  if (trimmed === "") {
    return "Name is required";
  }
  if (trimmed.length > 200) {
    return "Name must be at most 200 characters";
  }
  return null;
}

export function validateVenueOrgID(orgID: string): string | null {
  if (orgID.trim() === "") {
    return "Organization ID is required";
  }
  if (!UUID_RE.test(orgID.trim())) {
    return "Organization ID must be a UUID";
  }
  return null;
}

export function validateVenueCityID(cityID: string): string | null {
  // Optional; empty is fine.
  if (cityID.trim() === "") {
    return null;
  }
  if (!UUID_RE.test(cityID.trim())) {
    return "City ID must be a UUID";
  }
  return null;
}

export function validateVenueCapacity(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  const parsed = Number(raw);
  if (!Number.isInteger(parsed)) {
    return "Capacity must be a whole number";
  }
  if (parsed < 0) {
    return "Capacity cannot be negative";
  }
  if (parsed > 2_000_000_000) {
    return "Capacity is unreasonably large";
  }
  return null;
}

// ---------------------------------------------------------------------------
// Nav entry binding
// ---------------------------------------------------------------------------

const VENUES_NAV_ENTRY = NAV_BY_PATH["/venues"];
if (VENUES_NAV_ENTRY === undefined) {
  throw new Error("venues route: NAV_BY_PATH['/venues'] missing");
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

function VenuesRoute() {
  return (
    <RequirePermission entry={VENUES_NAV_ENTRY}>
      <VenuesModule />
    </RequirePermission>
  );
}

type FormMode =
  | { kind: "closed" }
  | { kind: "create" }
  | { kind: "edit"; venue: Venue };

function VenuesModule() {
  const { permissions } = useAuth();
  const { activeScope } = useScope();
  const canCreate = permissions.has("venue.create");
  const canUpdate = permissions.has("venue.update");
  const canDelete = permissions.has("venue.delete");

  const defaultOrgID =
    activeScope?.kind === "organization" && activeScope.id !== null
      ? activeScope.id
      : "";

  const [form, setForm] = useState<FormMode>({ kind: "closed" });
  const [pendingDelete, setPendingDelete] = useState<Venue | null>(null);

  const query = useQuery<VenueListEnvelope, ApiError>({
    queryKey: ["venues", "list"],
    queryFn: () =>
      authedFetch<VenueListEnvelope>({
        method: "GET",
        path: "/v1/venues",
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

  const rows = query.data?.venues ?? [];
  const sorted = useMemo(
    () => [...rows].sort((a, b) => a.name.localeCompare(b.name)),
    [rows],
  );

  return (
    <section aria-labelledby="venues-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="venues-heading" style={headingStyle}>
            Venues
          </h1>
          <p style={subheadingStyle}>
            Physical event locations. Reads are shared across organizations;
            writes are owner-gated -- a venue can only be created, edited, or
            archived by the organization that owns it. The visual seating
            editor remains explicitly deferred and is not part of this CRUD
            scope.
          </p>
        </div>
        <div style={refreshWrapStyle}>
          <button
            type="button"
            onClick={() => query.refetch()}
            style={refreshButtonStyle}
            disabled={query.isFetching}
            data-testid="venues-refresh"
          >
            {query.isFetching ? "Refreshing…" : "Refresh"}
          </button>
          {canCreate ? (
            <button
              type="button"
              onClick={() => setForm({ kind: "create" })}
              style={primaryButtonStyle}
              data-testid="venues-new"
            >
              New venue
            </button>
          ) : (
            <span style={mutedHintStyle} title="Requires venue.create">
              Create requires venue.create
            </span>
          )}
        </div>
      </header>

      <VenuesBody
        query={query}
        rows={sorted}
        canUpdate={canUpdate}
        canDelete={canDelete}
        onEdit={(venue) => setForm({ kind: "edit", venue })}
        onDelete={(venue) => setPendingDelete(venue)}
      />

      {form.kind !== "closed" ? (
        <VenueFormDialog
          mode={form}
          defaultOrgID={defaultOrgID}
          onClose={() => setForm({ kind: "closed" })}
        />
      ) : null}

      {pendingDelete !== null ? (
        <DeleteConfirmDialog
          venue={pendingDelete}
          onClose={() => setPendingDelete(null)}
        />
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Table body and states
// ---------------------------------------------------------------------------

interface BodyProps {
  query: ReturnType<typeof useQuery<VenueListEnvelope, ApiError>>;
  rows: readonly Venue[];
  canUpdate: boolean;
  canDelete: boolean;
  onEdit: (venue: Venue) => void;
  onDelete: (venue: Venue) => void;
}

function VenuesBody({ query, rows, canUpdate, canDelete, onEdit, onDelete }: BodyProps) {
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading venues from /v1/venues…
      </div>
    );
  }
  if (query.isError) {
    return <VenuesErrorState error={query.error} onRetry={() => query.refetch()} />;
  }
  if (rows.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="venues-empty">
        No venues exist yet. Create the first venue to begin attaching events
        and seating plans.
      </div>
    );
  }
  return (
    <div style={tableWrapStyle} role="region" aria-label="Venues">
      <table style={tableStyle} data-testid="venues-table">
        <thead>
          <tr>
            <th scope="col" style={thStyle}>Name</th>
            <th scope="col" style={thStyle}>Organization</th>
            <th scope="col" style={thStyle}>City</th>
            <th scope="col" style={thStyle}>Address</th>
            <th scope="col" style={thStyle}>Capacity</th>
            <th scope="col" style={thStyle}>Updated</th>
            <th scope="col" style={thStyle} aria-label="Actions" />
          </tr>
        </thead>
        <tbody>
          {rows.map((v) => (
            <tr key={v.id} data-testid={`venues-row-${v.id}`}>
              <td style={tdStyle}>{v.name}</td>
              <td style={tdMonoStyle} title={v.org_id}>
                {shortenUUID(v.org_id)}
              </td>
              <td style={tdMonoStyle} title={v.city_id ?? ""}>
                {v.city_id !== null ? shortenUUID(v.city_id) : "—"}
              </td>
              <td style={tdStyle}>{v.address ?? "—"}</td>
              <td style={tdStyle}>
                {v.capacity_default !== null
                  ? v.capacity_default.toLocaleString()
                  : "—"}
              </td>
              <td style={tdStyle}>{formatDate(v.updated_at)}</td>
              <td style={tdActionsStyle}>
                {canUpdate ? (
                  <button
                    type="button"
                    style={rowActionButtonStyle}
                    onClick={() => onEdit(v)}
                    data-testid={`venues-edit-${v.id}`}
                  >
                    Edit
                  </button>
                ) : null}
                {canDelete ? (
                  <button
                    type="button"
                    style={rowDangerButtonStyle}
                    onClick={() => onDelete(v)}
                    data-testid={`venues-delete-${v.id}`}
                  >
                    Archive
                  </button>
                ) : null}
                {!canUpdate && !canDelete ? (
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

function VenuesErrorState({
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
      <div style={errorBoxStyle} role="alert" data-testid="venues-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>venue.read</code>.
          Ask a platform administrator to grant the permission.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div style={errorBoxStyle} role="status" data-testid="venues-session-expired">
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload venues.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="venues-error">
      <strong>Failed to load venues.</strong>
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
  defaultOrgID: string;
  onClose: () => void;
}

interface ServerFieldErrors {
  name?: string;
  org_id?: string;
  city_id?: string;
  address?: string;
  capacity_default?: string;
  form?: string;
}

function VenueFormDialog({ mode, defaultOrgID, onClose }: FormDialogProps) {
  const queryClient = useQueryClient();
  const isEdit = mode.kind === "edit";

  const initialOrgID = isEdit ? mode.venue.org_id : defaultOrgID;
  const initialName = isEdit ? mode.venue.name : "";
  const initialCityID = isEdit ? (mode.venue.city_id ?? "") : "";
  const initialAddress = isEdit ? (mode.venue.address ?? "") : "";
  const initialCapacity = isEdit && mode.venue.capacity_default !== null
    ? String(mode.venue.capacity_default)
    : "";

  const [orgID, setOrgID] = useState(initialOrgID);
  const [name, setName] = useState(initialName);
  const [cityID, setCityID] = useState(initialCityID);
  const [address, setAddress] = useState(initialAddress);
  const [capacity, setCapacity] = useState(initialCapacity);
  const [serverErrors, setServerErrors] = useState<ServerFieldErrors>({});

  const nameErr = validateVenueName(name);
  const orgIDErr = validateVenueOrgID(orgID);
  const cityIDErr = validateVenueCityID(cityID);
  const capacityErr = validateVenueCapacity(capacity);
  const localValid =
    nameErr === null && orgIDErr === null && cityIDErr === null && capacityErr === null;

  // For edit, allow submission when at least one field changed.
  const dirty =
    !isEdit ||
    name.trim() !== initialName ||
    cityID.trim() !== initialCityID ||
    address.trim() !== initialAddress ||
    capacity.trim() !== initialCapacity;

  const mutation = useMutation<VenueEnvelope, ApiError, void>({
    mutationFn: () => {
      const trimmedOrgID = orgID.trim();
      if (isEdit) {
        const body: Record<string, unknown> = {};
        if (name.trim() !== initialName) {
          body.name = name.trim();
        }
        if (cityID.trim() !== initialCityID) {
          body.city_id = cityID.trim();
        }
        if (address.trim() !== initialAddress) {
          body.address = address.trim();
        }
        if (capacity.trim() !== initialCapacity) {
          body.capacity_default =
            capacity.trim() === "" ? null : Number(capacity);
        }
        return authedFetch<VenueEnvelope>({
          method: "PATCH",
          path: `/v1/organizations/${trimmedOrgID}/venues/${mode.venue.id}`,
          body,
        });
      }
      const body: Record<string, unknown> = {
        name: name.trim(),
        city_id: cityID.trim(),
        address: address.trim(),
      };
      if (capacity.trim() !== "") {
        body.capacity_default = Number(capacity);
      }
      return authedFetch<VenueEnvelope>({
        method: "POST",
        path: `/v1/organizations/${trimmedOrgID}/venues`,
        body,
      });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["venues"] });
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

  const title = isEdit ? `Edit ${mode.venue.name}` : "New venue";
  const submitLabel = isEdit ? "Save changes" : "Create venue";

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="venues-form-title"
      style={dialogBackdropStyle}
      data-testid="venues-form-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="venues-form-title" style={dialogTitleStyle}>
            {title}
          </h2>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="venues-form-close"
          >
            ×
          </button>
        </header>
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          {!isEdit ? (
            <FieldRow
              label="Organization ID"
              htmlFor="venue-org-id"
              error={serverErrors.org_id ?? null}
              localError={orgID.length > 0 ? orgIDErr : null}
              hint="UUID of the owning organization. Prefilled from the active org scope if set."
            >
              <input
                id="venue-org-id"
                type="text"
                value={orgID}
                onChange={(e) => {
                  setOrgID(e.target.value);
                  if (serverErrors.org_id !== undefined) {
                    setServerErrors({ ...serverErrors, org_id: undefined });
                  }
                }}
                style={inputMonoStyle}
                required
                maxLength={36}
                autoCapitalize="off"
                autoCorrect="off"
                spellCheck={false}
                data-testid="venues-form-org-id"
              />
            </FieldRow>
          ) : null}
          <FieldRow
            label="Name"
            htmlFor="venue-name"
            error={serverErrors.name ?? null}
            localError={name.length > 0 ? nameErr : null}
            hint="Operator-visible venue name. Required."
          >
            <input
              id="venue-name"
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
              autoFocus={!isEdit}
              data-testid="venues-form-name"
            />
          </FieldRow>
          <FieldRow
            label="City ID"
            htmlFor="venue-city-id"
            error={serverErrors.city_id ?? null}
            localError={cityID.length > 0 ? cityIDErr : null}
            hint="Optional UUID from the geo catalog."
          >
            <input
              id="venue-city-id"
              type="text"
              value={cityID}
              onChange={(e) => {
                setCityID(e.target.value);
                if (serverErrors.city_id !== undefined) {
                  setServerErrors({ ...serverErrors, city_id: undefined });
                }
              }}
              style={inputMonoStyle}
              maxLength={36}
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-testid="venues-form-city-id"
            />
          </FieldRow>
          <FieldRow
            label="Address"
            htmlFor="venue-address"
            error={serverErrors.address ?? null}
            localError={null}
            hint="Free-form street address. Optional."
          >
            <input
              id="venue-address"
              type="text"
              value={address}
              onChange={(e) => {
                setAddress(e.target.value);
              }}
              style={inputStyle}
              maxLength={500}
              data-testid="venues-form-address"
            />
          </FieldRow>
          <FieldRow
            label="Default capacity"
            htmlFor="venue-capacity"
            error={serverErrors.capacity_default ?? null}
            localError={capacity.length > 0 ? capacityErr : null}
            hint="Optional whole number. Leave blank if unknown."
          >
            <input
              id="venue-capacity"
              type="number"
              value={capacity}
              onChange={(e) => {
                setCapacity(e.target.value);
                if (serverErrors.capacity_default !== undefined) {
                  setServerErrors({
                    ...serverErrors,
                    capacity_default: undefined,
                  });
                }
              }}
              style={inputStyle}
              min={0}
              step={1}
              data-testid="venues-form-capacity"
            />
          </FieldRow>

          {serverErrors.form !== undefined ? (
            <div style={formErrorStyle} role="alert" data-testid="venues-form-error">
              {serverErrors.form}
            </div>
          ) : null}

          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="venues-form-cancel"
            >
              Cancel
            </button>
            <button
              type="submit"
              style={primaryButtonStyle}
              disabled={!localValid || !dirty || mutation.isPending}
              data-testid="venues-form-submit"
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
// Delete confirm dialog (soft-delete on the backend)
// ---------------------------------------------------------------------------

function DeleteConfirmDialog({
  venue,
  onClose,
}: {
  venue: Venue;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [error, setError] = useState<ApiError | null>(null);

  const mutation = useMutation<unknown, ApiError, void>({
    mutationFn: () =>
      authedFetch<unknown>({
        method: "DELETE",
        path: `/v1/organizations/${venue.org_id}/venues/${venue.id}`,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["venues"] });
      onClose();
    },
    onError: (err) => setError(err),
  });

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="venues-delete-title"
      style={dialogBackdropStyle}
      data-testid="venues-delete-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="venues-delete-title" style={dialogTitleStyle}>
            Archive {venue.name}?
          </h2>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="venues-delete-close"
          >
            ×
          </button>
        </header>
        <div style={archiveBodyStyle}>
          <p style={archiveParaStyle}>
            Archiving a venue removes it from the active list. This is a{" "}
            <strong>soft-delete</strong>: the row is preserved with{" "}
            <code style={monoStyle}>deleted_at</code> set and a{" "}
            <code style={monoStyle}>v1.venue.delete</code> audit event is
            written atomically. Existing events that reference this venue
            keep their reference.
          </p>
          {error !== null ? (
            <div style={formErrorStyle} role="alert" data-testid="venues-delete-error">
              {error.message} (<code style={monoStyle}>{error.code}</code>)
            </div>
          ) : null}
          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="venues-delete-cancel"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => mutation.mutate()}
              style={dangerButtonStyle}
              disabled={mutation.isPending}
              data-testid="venues-delete-confirm"
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
 * Map an envelope from venues.go onto field-level errors. The handler
 * emits `details.field = "name" | "city_id"` for invalid_name /
 * invalid_city_id; duplicate is unconditionally a name-field error
 * (uniqueness is per (org_id, name)).
 */
export function mapServerError(err: ApiError): ServerFieldErrors {
  const out: ServerFieldErrors = {};
  const field =
    err.details !== undefined && typeof err.details.field === "string"
      ? err.details.field
      : undefined;
  switch (err.code) {
    case "venue.invalid_name":
      out.name = err.message;
      return out;
    case "venue.invalid_city_id":
      out.city_id = err.message;
      return out;
    case "venue.duplicate":
      out.name = err.message;
      return out;
    case "venue.not_found":
      out.form = err.message;
      return out;
    case "venue.empty_body":
    case "venue.invalid_body":
    case "venue.invalid_json":
      out.form = err.message;
      return out;
    case "permissions.denied":
      out.form =
        "Your account is missing the required permission. Ask a platform administrator.";
      return out;
    case "superadmin.missing_reason":
    case "superadmin.reason_required":
      out.form =
        "An audit reason is required for cross-tenant changes. Provide a reason in the prompt and retry.";
      return out;
    default:
      if (field === "name") {
        out.name = err.message;
      } else if (field === "city_id") {
        out.city_id = err.message;
      } else if (field === "org_id") {
        out.org_id = err.message;
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

function shortenUUID(id: string): string {
  return id.length > 8 ? `${id.slice(0, 8)}…` : id;
}

// ---------------------------------------------------------------------------
// Styles (mirror networks.tsx)
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
