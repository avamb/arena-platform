/**
 * Sales Channels CRUD module (feature #243).
 *
 * Replaces the SAUI-12 /channels placeholder shell with a real CRUD
 * screen backed by the channels API in
 * apps/backend/internal/platform/httpserver/channels.go:
 *
 *   GET    /v1/organizations/{org_id}/channels         list   (channel.read)
 *   GET    /v1/organizations/{org_id}/channels/{id}    get    (channel.read)
 *   POST   /v1/organizations/{org_id}/channels         create (channel.create)
 *   PATCH  /v1/organizations/{org_id}/channels/{id}    update (channel.update)
 *   DELETE /v1/organizations/{org_id}/channels/{id}    delete (channel.delete)
 *
 * All endpoints are owner-gated on org_id; the route accepts the org_id
 * from the active organization scope when present, otherwise the
 * operator may paste one. This mirrors the legacy "Frontends and
 * Channels" surface but trimmed to the modern unified contract:
 * name, payment_mode, provider, provider_account_id (masked on read),
 * fee_percent, reservation_ttl_override, and settings (JSON object).
 *
 * Validation mirrors the backend contract:
 *   - name required, 1-200 chars
 *   - payment_mode in {"direct_merchant", "merchant_of_record"}
 *   - provider     in {"stripe", "allpay"}
 *   - provider_account_id required iff payment_mode=direct_merchant
 *   - fee_percent must parse as a decimal with up to 2 fractional digits
 *   - reservation_ttl_override is an optional positive integer (seconds)
 *   - settings is an optional JSON object (arrays/scalars rejected)
 *
 * Credential discipline:
 *   - GET / LIST responses mask provider_account_id ("****abcd"); the
 *     UI displays exactly what the backend returns.
 *   - On Edit, the masked value is shown read-only with a "Replace
 *     credential" toggle that prompts a fresh raw value before PATCH.
 *     The masked value is NEVER sent back as the credential.
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
import {
  ResponsiveTable,
  ResponsiveDrawer,
  useIsDesktop,
  type ResponsiveTableColumn,
} from "@/components/layout";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/channels",
  component: ChannelsRoute,
});

// ---------------------------------------------------------------------------
// Backend response shapes
// ---------------------------------------------------------------------------

export type PaymentMode = "direct_merchant" | "merchant_of_record";
export type Provider = "stripe" | "allpay";

export const PAYMENT_MODES: readonly PaymentMode[] = [
  "direct_merchant",
  "merchant_of_record",
];
export const PROVIDERS: readonly Provider[] = ["stripe", "allpay"];

export interface Channel {
  readonly id: string;
  readonly org_id: string;
  readonly name: string;
  readonly payment_mode: PaymentMode | string;
  readonly provider: Provider | string;
  readonly provider_account_id: string | null;
  readonly fee_percent: string;
  readonly reservation_ttl_override: number | null;
  readonly settings: Record<string, unknown> | null;
  readonly created_at: string;
  readonly updated_at: string;
}

interface ChannelListEnvelope {
  readonly channels: readonly Channel[];
}

interface ChannelEnvelope {
  readonly channel: Channel;
}

// ---------------------------------------------------------------------------
// Validators (mirror backend contracts)
// ---------------------------------------------------------------------------

export const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function validateChannelOrgID(orgID: string): string | null {
  if (orgID.trim() === "") {
    return "Organization ID is required";
  }
  if (!UUID_RE.test(orgID.trim())) {
    return "Organization ID must be a UUID";
  }
  return null;
}

export function validateChannelName(name: string): string | null {
  const trimmed = name.trim();
  if (trimmed === "") {
    return "Name is required";
  }
  if (trimmed.length > 200) {
    return "Name must be at most 200 characters";
  }
  return null;
}

export function validatePaymentMode(mode: string): string | null {
  if (!(PAYMENT_MODES as readonly string[]).includes(mode)) {
    return "Payment mode must be direct_merchant or merchant_of_record";
  }
  return null;
}

export function validateProvider(provider: string): string | null {
  if (!(PROVIDERS as readonly string[]).includes(provider)) {
    return "Provider must be stripe or allpay";
  }
  return null;
}

/**
 * direct_merchant requires the provider_account_id to be non-empty;
 * merchant_of_record permits an empty credential.
 */
export function validateProviderAccountID(
  mode: string,
  accountID: string,
): string | null {
  if (mode === "direct_merchant" && accountID.trim() === "") {
    return "Provider account ID is required for direct_merchant";
  }
  return null;
}

/**
 * fee_percent must be parseable as a non-negative decimal with up to
 * two fractional digits. Mirrors the NUMERIC(5,2) constraint on the
 * backend column.
 */
export function validateFeePercent(raw: string): string | null {
  if (raw.trim() === "") {
    return "Fee percent is required";
  }
  if (!/^\d{1,3}(\.\d{1,2})?$/.test(raw.trim())) {
    return "Fee percent must be a non-negative decimal (e.g. 2.50)";
  }
  const parsed = Number(raw);
  if (Number.isNaN(parsed) || parsed < 0 || parsed > 100) {
    return "Fee percent must be between 0 and 100";
  }
  return null;
}

export function validateReservationTTL(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  const parsed = Number(raw);
  if (!Number.isInteger(parsed)) {
    return "Reservation TTL must be a whole number of seconds";
  }
  if (parsed <= 0) {
    return "Reservation TTL must be positive";
  }
  if (parsed > 86_400) {
    return "Reservation TTL must be 24 hours (86400s) or less";
  }
  return null;
}

/**
 * settings must be empty (use defaults) or a JSON OBJECT. Arrays and
 * scalars are rejected to match the backend normaliseChannelSettings.
 */
export function validateSettingsJSON(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return "Settings must be valid JSON";
  }
  if (
    typeof parsed !== "object" ||
    parsed === null ||
    Array.isArray(parsed)
  ) {
    return "Settings must be a JSON object (not an array or scalar)";
  }
  return null;
}

// ---------------------------------------------------------------------------
// Nav entry binding
// ---------------------------------------------------------------------------

const CHANNELS_NAV_ENTRY = NAV_BY_PATH["/channels"];
if (CHANNELS_NAV_ENTRY === undefined) {
  throw new Error("channels route: NAV_BY_PATH['/channels'] missing");
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

function ChannelsRoute() {
  return (
    <RequirePermission entry={CHANNELS_NAV_ENTRY}>
      <ChannelsModule />
    </RequirePermission>
  );
}

type FormMode =
  | { kind: "closed" }
  | { kind: "create" }
  | { kind: "edit"; channel: Channel };

function ChannelsModule() {
  const { permissions } = useAuth();
  const { activeScope } = useScope();
  const canCreate = permissions.has("channel.create");
  const canUpdate = permissions.has("channel.update");
  const canDelete = permissions.has("channel.delete");

  const scopeOrgID =
    activeScope?.kind === "organization" && activeScope.id !== null
      ? activeScope.id
      : "";

  // The org being viewed. Defaults to the active scope org; the operator
  // can paste a different UUID when working outside an org scope (e.g.
  // a platform_superadmin investigating tenant configuration).
  const [orgID, setOrgID] = useState(scopeOrgID);
  const trimmedOrgID = orgID.trim();
  const orgIDError = orgID === "" ? null : validateChannelOrgID(orgID);
  const orgReady = trimmedOrgID !== "" && orgIDError === null;

  const [form, setForm] = useState<FormMode>({ kind: "closed" });
  const [pendingDelete, setPendingDelete] = useState<Channel | null>(null);
  const isDesktop = useIsDesktop(true);
  const [filtersOpen, setFiltersOpen] = useState<boolean>(false);

  const query = useQuery<ChannelListEnvelope, ApiError>({
    queryKey: ["channels", "list", trimmedOrgID],
    enabled: orgReady,
    queryFn: () =>
      authedFetch<ChannelListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${trimmedOrgID}/channels`,
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

  const rows = query.data?.channels ?? [];
  const sorted = useMemo(
    () => [...rows].sort((a, b) => a.name.localeCompare(b.name)),
    [rows],
  );

  return (
    <section aria-labelledby="channels-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="channels-heading" style={headingStyle}>
            Sales Channels
          </h1>
          <p style={subheadingStyle}>
            Each channel defines a payment mode (direct_merchant or
            merchant_of_record), a provider (stripe / allpay), an
            optional merchant account credential (shown masked on
            read), the commission fee percent, and an optional
            reservation TTL override that supersedes the parent
            organization's seat-hold window.
          </p>
        </div>
        <div style={refreshWrapStyle}>
          <button
            type="button"
            onClick={() => query.refetch()}
            style={refreshButtonStyle}
            disabled={!orgReady || query.isFetching}
            data-testid="channels-refresh"
          >
            {query.isFetching ? "Refreshing…" : "Refresh"}
          </button>
          {canCreate ? (
            <button
              type="button"
              onClick={() => setForm({ kind: "create" })}
              style={primaryButtonStyle}
              disabled={!orgReady}
              data-testid="channels-new"
            >
              New channel
            </button>
          ) : (
            <span style={mutedHintStyle} title="Requires channel.create">
              Create requires channel.create
            </span>
          )}
        </div>
      </header>

      {(() => {
        const toolbar = (
          <div style={orgPickerStyle}>
            <label htmlFor="channels-org-id" style={fieldLabelStyle}>
              Organization ID
            </label>
            <input
              id="channels-org-id"
              type="text"
              value={orgID}
              onChange={(e) => setOrgID(e.target.value)}
              style={inputMonoStyle}
              placeholder="00000000-0000-0000-0000-000000000000"
              maxLength={36}
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-testid="channels-org-id"
            />
            {orgIDError !== null ? (
              <div
                style={fieldErrorStyle}
                role="alert"
                data-testid="channels-org-id-error"
              >
                {orgIDError}
              </div>
            ) : (
              <div style={fieldHintStyle}>
                {scopeOrgID !== ""
                  ? "Prefilled from the active organization scope. Paste a different UUID to switch."
                  : "Paste the UUID of the organization to manage. Activate an organization scope to prefill."}
              </div>
            )}
          </div>
        );
        if (isDesktop) {
          return toolbar;
        }
        return (
          <>
            <button
              type="button"
              style={secondaryButtonStyle}
              onClick={() => setFiltersOpen(true)}
              data-testid="channels-filters-open"
            >
              Filters
            </button>
            <ResponsiveDrawer
              id="channels-filters-drawer"
              open={filtersOpen}
              onClose={() => setFiltersOpen(false)}
              title="Filters"
            >
              {toolbar}
            </ResponsiveDrawer>
          </>
        );
      })()}

      <ChannelsBody
        query={query}
        orgReady={orgReady}
        rows={sorted}
        canUpdate={canUpdate}
        canDelete={canDelete}
        onEdit={(channel) => setForm({ kind: "edit", channel })}
        onDelete={(channel) => setPendingDelete(channel)}
      />

      {form.kind !== "closed" ? (
        <ChannelFormDialog
          mode={form}
          defaultOrgID={trimmedOrgID}
          onClose={() => setForm({ kind: "closed" })}
        />
      ) : null}

      {pendingDelete !== null ? (
        <DeleteConfirmDialog
          channel={pendingDelete}
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
  query: ReturnType<typeof useQuery<ChannelListEnvelope, ApiError>>;
  orgReady: boolean;
  rows: readonly Channel[];
  canUpdate: boolean;
  canDelete: boolean;
  onEdit: (channel: Channel) => void;
  onDelete: (channel: Channel) => void;
}

function ChannelsBody({
  query,
  orgReady,
  rows,
  canUpdate,
  canDelete,
  onEdit,
  onDelete,
}: BodyProps) {
  if (!orgReady) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="channels-needs-org">
        Enter an organization UUID above to load its sales channels.
      </div>
    );
  }
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading channels from /v1/organizations/{"{org_id}"}/channels…
      </div>
    );
  }
  if (query.isError) {
    return (
      <ChannelsErrorState
        error={query.error}
        onRetry={() => query.refetch()}
      />
    );
  }
  if (rows.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="channels-empty">
        This organization has no active sales channels. Create the
        first channel to begin accepting payments.
      </div>
    );
  }
  const columns: ResponsiveTableColumn<Channel>[] = [
    {
      id: "name",
      header: "Name",
      primary: true,
      renderCell: (c) => <span data-testid={`channels-row-${c.id}`}>{c.name}</span>,
    },
    {
      id: "payment_mode",
      header: "Payment mode",
      renderCell: (c) => c.payment_mode,
    },
    {
      id: "provider",
      header: "Provider",
      renderCell: (c) => c.provider,
    },
    {
      id: "account",
      header: "Merchant account",
      renderCell: (c) => (
        <span
          title={c.provider_account_id ?? ""}
          data-testid={`channels-account-${c.id}`}
        >
          {c.provider_account_id !== null && c.provider_account_id !== ""
            ? c.provider_account_id
            : "—"}
        </span>
      ),
    },
    {
      id: "fee",
      header: "Fee %",
      renderCell: (c) => c.fee_percent,
    },
    {
      id: "ttl",
      header: "Reservation TTL",
      renderCell: (c) =>
        c.reservation_ttl_override !== null
          ? `${c.reservation_ttl_override.toLocaleString()} s`
          : "—",
    },
    {
      id: "updated",
      header: "Updated",
      renderCell: (c) => formatDate(c.updated_at),
    },
    {
      id: "actions",
      header: "Actions",
      hideOnMobile: true,
      renderCell: (c) => (
        <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
          {canUpdate ? (
            <button
              type="button"
              style={rowActionButtonStyle}
              onClick={() => onEdit(c)}
              data-testid={`channels-edit-${c.id}`}
            >
              Edit
            </button>
          ) : null}
          {canDelete ? (
            <button
              type="button"
              style={rowDangerButtonStyle}
              onClick={() => onDelete(c)}
              data-testid={`channels-delete-${c.id}`}
            >
              Archive
            </button>
          ) : null}
          {!canUpdate && !canDelete ? (
            <span style={mutedHintStyle}>read-only</span>
          ) : null}
        </div>
      ),
    },
  ];
  return (
    <div style={tableWrapStyle} role="region" aria-label="Channels">
      <ResponsiveTable<Channel>
        id="channels-table"
        columns={columns}
        rows={rows}
        rowKey={(c) => c.id}
      />
    </div>
  );
}

function ChannelsErrorState({
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
      <div style={errorBoxStyle} role="alert" data-testid="channels-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>channel.read</code>{" "}
          for this organization. Ask a platform administrator to grant the
          permission, or activate a scope that owns this org.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div style={errorBoxStyle} role="status" data-testid="channels-session-expired">
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload channels.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="channels-error">
      <strong>Failed to load channels.</strong>
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
  payment_mode?: string;
  provider?: string;
  provider_account_id?: string;
  fee_percent?: string;
  reservation_ttl_override?: string;
  settings?: string;
  form?: string;
}

function ChannelFormDialog({ mode, defaultOrgID, onClose }: FormDialogProps) {
  const queryClient = useQueryClient();
  const isEdit = mode.kind === "edit";

  const initialOrgID = isEdit ? mode.channel.org_id : defaultOrgID;
  const initialName = isEdit ? mode.channel.name : "";
  const initialMode: string = isEdit ? mode.channel.payment_mode : "direct_merchant";
  const initialProvider: string = isEdit ? mode.channel.provider : "stripe";
  const initialAccountMasked = isEdit
    ? (mode.channel.provider_account_id ?? "")
    : "";
  const initialFee = isEdit ? mode.channel.fee_percent : "0.00";
  const initialTTL =
    isEdit && mode.channel.reservation_ttl_override !== null
      ? String(mode.channel.reservation_ttl_override)
      : "";
  const initialSettings = isEdit && mode.channel.settings !== null
    ? JSON.stringify(mode.channel.settings, null, 2)
    : "";

  const [name, setName] = useState(initialName);
  const [paymentMode, setPaymentMode] = useState<string>(initialMode);
  const [provider, setProvider] = useState<string>(initialProvider);
  const [accountID, setAccountID] = useState(""); // raw, user-entered
  const [replaceCredential, setReplaceCredential] = useState(!isEdit);
  const [feePercent, setFeePercent] = useState(initialFee);
  const [reservationTTL, setReservationTTL] = useState(initialTTL);
  const [settingsJSON, setSettingsJSON] = useState(initialSettings);
  const [serverErrors, setServerErrors] = useState<ServerFieldErrors>({});

  const nameErr = validateChannelName(name);
  const modeErr = validatePaymentMode(paymentMode);
  const providerErr = validateProvider(provider);
  const accountErr = replaceCredential
    ? validateProviderAccountID(paymentMode, accountID)
    : null;
  const feeErr = validateFeePercent(feePercent);
  const ttlErr = validateReservationTTL(reservationTTL);
  const settingsErr = validateSettingsJSON(settingsJSON);

  const localValid =
    nameErr === null &&
    modeErr === null &&
    providerErr === null &&
    accountErr === null &&
    feeErr === null &&
    ttlErr === null &&
    settingsErr === null;

  const dirty =
    !isEdit ||
    name.trim() !== initialName ||
    paymentMode !== initialMode ||
    provider !== initialProvider ||
    replaceCredential ||
    feePercent.trim() !== initialFee ||
    reservationTTL.trim() !== initialTTL ||
    settingsJSON.trim() !== initialSettings.trim();

  const mutation = useMutation<ChannelEnvelope, ApiError, void>({
    mutationFn: () => {
      const orgIDForPath = isEdit ? mode.channel.org_id : defaultOrgID;
      if (isEdit) {
        const body: Record<string, unknown> = {};
        if (name.trim() !== initialName) {
          body.name = name.trim();
        }
        if (paymentMode !== initialMode) {
          body.payment_mode = paymentMode;
        }
        if (provider !== initialProvider) {
          body.provider = provider;
        }
        if (replaceCredential) {
          body.provider_account_id = accountID.trim();
        }
        if (feePercent.trim() !== initialFee) {
          body.fee_percent = feePercent.trim();
        }
        if (reservationTTL.trim() !== initialTTL) {
          body.reservation_ttl_override =
            reservationTTL.trim() === "" ? null : Number(reservationTTL);
        }
        if (settingsJSON.trim() !== initialSettings.trim()) {
          body.settings =
            settingsJSON.trim() === ""
              ? {}
              : (JSON.parse(settingsJSON) as Record<string, unknown>);
        }
        return authedFetch<ChannelEnvelope>({
          method: "PATCH",
          path: `/v1/organizations/${orgIDForPath}/channels/${mode.channel.id}`,
          body,
        });
      }
      const body: Record<string, unknown> = {
        name: name.trim(),
        payment_mode: paymentMode,
        provider,
        provider_account_id: accountID.trim(),
        fee_percent: feePercent.trim(),
      };
      if (reservationTTL.trim() !== "") {
        body.reservation_ttl_override = Number(reservationTTL);
      }
      if (settingsJSON.trim() !== "") {
        body.settings = JSON.parse(settingsJSON) as Record<string, unknown>;
      }
      return authedFetch<ChannelEnvelope>({
        method: "POST",
        path: `/v1/organizations/${orgIDForPath}/channels`,
        body,
      });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["channels"] });
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

  const title = isEdit ? `Edit ${mode.channel.name}` : "New channel";
  const submitLabel = isEdit ? "Save changes" : "Create channel";

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="channels-form-title"
      style={dialogBackdropStyle}
      data-testid="channels-form-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="channels-form-title" style={dialogTitleStyle}>
            {title}
          </h2>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="channels-form-close"
          >
            ×
          </button>
        </header>
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          {isEdit ? (
            <FieldRow
              label="Organization ID"
              htmlFor="channel-org-id-readonly"
              error={null}
              localError={null}
              hint="Channels are owner-gated; the org cannot be changed by edit."
            >
              <input
                id="channel-org-id-readonly"
                type="text"
                value={initialOrgID}
                readOnly
                style={inputMonoStyle}
                data-testid="channels-form-org-id-readonly"
              />
            </FieldRow>
          ) : null}

          <FieldRow
            label="Name"
            htmlFor="channel-name"
            error={serverErrors.name ?? null}
            localError={name.length > 0 ? nameErr : null}
            hint="Operator-visible name. Must be unique within the organization."
          >
            <input
              id="channel-name"
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
              data-testid="channels-form-name"
            />
          </FieldRow>

          <FieldRow
            label="Payment mode"
            htmlFor="channel-payment-mode"
            error={serverErrors.payment_mode ?? null}
            localError={modeErr}
            hint="direct_merchant routes funds to the merchant's own provider account; merchant_of_record routes to the platform's account."
          >
            <select
              id="channel-payment-mode"
              value={paymentMode}
              onChange={(e) => {
                setPaymentMode(e.target.value);
                if (serverErrors.payment_mode !== undefined) {
                  setServerErrors({ ...serverErrors, payment_mode: undefined });
                }
              }}
              style={inputStyle}
              data-testid="channels-form-payment-mode"
            >
              {PAYMENT_MODES.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
          </FieldRow>

          <FieldRow
            label="Provider"
            htmlFor="channel-provider"
            error={serverErrors.provider ?? null}
            localError={providerErr}
            hint="Which payment provider this channel is wired to."
          >
            <select
              id="channel-provider"
              value={provider}
              onChange={(e) => {
                setProvider(e.target.value);
                if (serverErrors.provider !== undefined) {
                  setServerErrors({ ...serverErrors, provider: undefined });
                }
              }}
              style={inputStyle}
              data-testid="channels-form-provider"
            >
              {PROVIDERS.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </FieldRow>

          <FieldRow
            label="Provider account ID"
            htmlFor="channel-provider-account-id"
            error={serverErrors.provider_account_id ?? null}
            localError={accountErr}
            hint={
              paymentMode === "direct_merchant"
                ? "Required for direct_merchant. The merchant account credential at the provider (Stripe acct_*, AllPay merchant id, …)."
                : "Optional for merchant_of_record."
            }
          >
            {isEdit && !replaceCredential ? (
              <div style={maskedRowStyle}>
                <input
                  id="channel-provider-account-id-masked"
                  type="text"
                  value={initialAccountMasked === "" ? "(unset)" : initialAccountMasked}
                  readOnly
                  style={inputMonoStyle}
                  data-testid="channels-form-account-id-masked"
                />
                <button
                  type="button"
                  onClick={() => {
                    setAccountID("");
                    setReplaceCredential(true);
                  }}
                  style={secondaryButtonStyle}
                  data-testid="channels-form-account-id-replace"
                >
                  Replace credential
                </button>
              </div>
            ) : (
              <input
                id="channel-provider-account-id"
                type="text"
                value={accountID}
                onChange={(e) => {
                  setAccountID(e.target.value);
                  if (serverErrors.provider_account_id !== undefined) {
                    setServerErrors({
                      ...serverErrors,
                      provider_account_id: undefined,
                    });
                  }
                }}
                style={inputMonoStyle}
                maxLength={256}
                autoCapitalize="off"
                autoCorrect="off"
                spellCheck={false}
                data-testid="channels-form-account-id"
              />
            )}
          </FieldRow>

          <FieldRow
            label="Fee percent"
            htmlFor="channel-fee-percent"
            error={serverErrors.fee_percent ?? null}
            localError={feePercent.length > 0 ? feeErr : null}
            hint="Commission charged on this channel, expressed as a decimal between 0 and 100 (e.g. 2.50 for 2.5%)."
          >
            <input
              id="channel-fee-percent"
              type="text"
              value={feePercent}
              onChange={(e) => {
                setFeePercent(e.target.value);
                if (serverErrors.fee_percent !== undefined) {
                  setServerErrors({ ...serverErrors, fee_percent: undefined });
                }
              }}
              style={inputMonoStyle}
              inputMode="decimal"
              data-testid="channels-form-fee-percent"
            />
          </FieldRow>

          <FieldRow
            label="Reservation TTL override (seconds)"
            htmlFor="channel-reservation-ttl"
            error={serverErrors.reservation_ttl_override ?? null}
            localError={reservationTTL.length > 0 ? ttlErr : null}
            hint="Optional override for the parent organization's seat-hold window. Leave blank to inherit."
          >
            <input
              id="channel-reservation-ttl"
              type="number"
              value={reservationTTL}
              onChange={(e) => {
                setReservationTTL(e.target.value);
                if (serverErrors.reservation_ttl_override !== undefined) {
                  setServerErrors({
                    ...serverErrors,
                    reservation_ttl_override: undefined,
                  });
                }
              }}
              style={inputStyle}
              min={1}
              max={86_400}
              step={1}
              data-testid="channels-form-reservation-ttl"
            />
          </FieldRow>

          <FieldRow
            label="Settings (JSON object)"
            htmlFor="channel-settings"
            error={serverErrors.settings ?? null}
            localError={settingsJSON.length > 0 ? settingsErr : null}
            hint="Optional JSON object for provider-specific or channel-specific configuration. Must be an object — arrays and scalars are rejected."
          >
            <textarea
              id="channel-settings"
              value={settingsJSON}
              onChange={(e) => {
                setSettingsJSON(e.target.value);
                if (serverErrors.settings !== undefined) {
                  setServerErrors({ ...serverErrors, settings: undefined });
                }
              }}
              style={textareaStyle}
              rows={5}
              spellCheck={false}
              data-testid="channels-form-settings"
            />
          </FieldRow>

          {serverErrors.form !== undefined ? (
            <div
              style={formErrorStyle}
              role="alert"
              data-testid="channels-form-error"
            >
              {serverErrors.form}
            </div>
          ) : null}

          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="channels-form-cancel"
            >
              Cancel
            </button>
            <button
              type="submit"
              style={primaryButtonStyle}
              disabled={!localValid || !dirty || mutation.isPending}
              data-testid="channels-form-submit"
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
  channel,
  onClose,
}: {
  channel: Channel;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [error, setError] = useState<ApiError | null>(null);

  const mutation = useMutation<unknown, ApiError, void>({
    mutationFn: () =>
      authedFetch<unknown>({
        method: "DELETE",
        path: `/v1/organizations/${channel.org_id}/channels/${channel.id}`,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["channels"] });
      onClose();
    },
    onError: (err) => setError(err),
  });

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="channels-delete-title"
      style={dialogBackdropStyle}
      data-testid="channels-delete-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="channels-delete-title" style={dialogTitleStyle}>
            Archive {channel.name}?
          </h2>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="channels-delete-close"
          >
            ×
          </button>
        </header>
        <div style={archiveBodyStyle}>
          <p style={archiveParaStyle}>
            Archiving a channel removes it from the active list. This is
            a <strong>soft-delete</strong>: the row is preserved with{" "}
            <code style={monoStyle}>deleted_at</code> set and a{" "}
            <code style={monoStyle}>v1.channel.delete</code> audit
            event is written atomically. Orders that have already used
            this channel keep their reference.
          </p>
          {error !== null ? (
            <div
              style={formErrorStyle}
              role="alert"
              data-testid="channels-delete-error"
            >
              {error.message} (<code style={monoStyle}>{error.code}</code>)
            </div>
          ) : null}
          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="channels-delete-cancel"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => mutation.mutate()}
              style={dangerButtonStyle}
              disabled={mutation.isPending}
              data-testid="channels-delete-confirm"
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
 * Map an envelope from channels.go onto field-level errors. The handler
 * emits `details.field` for invalid_name / invalid_config /
 * invalid_settings; duplicate is unconditionally a name-field error
 * (uniqueness is per (org_id, name)).
 */
export function mapServerError(err: ApiError): ServerFieldErrors {
  const out: ServerFieldErrors = {};
  const field =
    err.details !== undefined && typeof err.details.field === "string"
      ? err.details.field
      : undefined;
  switch (err.code) {
    case "channel.invalid_name":
      out.name = err.message;
      return out;
    case "channel.invalid_config":
      if (field === "provider") {
        out.provider = err.message;
      } else {
        // The backend reports both payment_mode and the missing
        // provider_account_id under field="payment_mode". Surface the
        // message on whichever field the operator most likely needs
        // to fix.
        if (/provider_account_id/i.test(err.message)) {
          out.provider_account_id = err.message;
        } else {
          out.payment_mode = err.message;
        }
      }
      return out;
    case "channel.invalid_settings":
      out.settings = err.message;
      return out;
    case "channel.duplicate":
      out.name = err.message;
      return out;
    case "channel.not_found":
      out.form = err.message;
      return out;
    case "channel.empty_body":
    case "channel.invalid_body":
    case "channel.invalid_json":
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
      } else if (field === "payment_mode") {
        out.payment_mode = err.message;
      } else if (field === "provider") {
        out.provider = err.message;
      } else if (field === "settings") {
        out.settings = err.message;
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

// ---------------------------------------------------------------------------
// Styles (mirror venues.tsx / networks.tsx)
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

const orgPickerStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: 12,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#f8fafc",
  maxWidth: 520,
};

const tableWrapStyle: CSSProperties = {
  overflowX: "auto",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
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
  width: "min(560px, 100%)",
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

const textareaStyle: CSSProperties = {
  ...inputStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
  resize: "vertical",
  minHeight: 80,
};

const maskedRowStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "center",
  flexWrap: "wrap",
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
