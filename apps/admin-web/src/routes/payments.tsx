/**
 * Payment Provider Configs CRUD module (feature #244).
 *
 * Replaces the SAUI-12 /payments placeholder shell with a real CRUD
 * screen backed by the payment-provider-configs API in
 * apps/backend/internal/platform/httpserver/payment_configs*.go
 * (feature #237):
 *
 *   GET    /v1/organizations/{org_id}/payment-configs        list   (payment_config.read)
 *   GET    /v1/organizations/{org_id}/payment-configs/{id}   get    (payment_config.read)
 *   POST   /v1/organizations/{org_id}/payment-configs        create (payment_config.write)
 *   PATCH  /v1/organizations/{org_id}/payment-configs/{id}   update (payment_config.write)
 *   DELETE /v1/organizations/{org_id}/payment-configs/{id}   delete (payment_config.write)
 *
 * The resource carries the credentials and public connection metadata
 * for one (org, provider, mode) tuple. It is the resource an org admin
 * manages to wire Stripe / AllPay / CloudPayments / YooKassa into the
 * platform.
 *
 * Secret discipline (CRITICAL):
 *   - LIST / GET responses NEVER include the raw `secrets` jsonb. The
 *     backend strips it in paymentConfigFromRow; the UI only ever sees
 *     `secret_fields_set` (key names of populated secrets) and
 *     `missing_required_fields` (required keys still unset).
 *   - The Edit form shows masked "••• stored" placeholders for keys
 *     already present and empty inputs for the missing ones. The
 *     submitted PATCH body sends ONLY the keys the operator typed into
 *     (non-empty REPLACES, single-character "" CLEARS via the explicit
 *     "Clear" button); untouched fields are omitted, so the existing
 *     secret is preserved.
 *   - The Create form sends the full provider catalogue but only keys
 *     the operator filled (empty fields are skipped).
 *   - A status badge ("configured" vs "missing required fields") is
 *     rendered from the backend-derived `status` column, NOT from
 *     UI-side guessing about secret presence.
 *
 * All endpoints are owner-gated through the org_id path segment. The
 * route accepts the org_id from the active organization scope when
 * present, otherwise the operator may paste one (e.g. a
 * platform_superadmin investigating tenant configuration).
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
  path: "/payments",
  component: PaymentsRoute,
});

// ---------------------------------------------------------------------------
// Backend response shapes
// ---------------------------------------------------------------------------

export type PaymentProvider =
  | "stripe"
  | "allpay"
  | "cloudpayments"
  | "yookassa"
  | "manual";

export type PaymentMode = "test" | "live";

export type PaymentConfigStatus = "configured" | "missing_required_fields";

export const PAYMENT_PROVIDERS: readonly PaymentProvider[] = [
  "stripe",
  "allpay",
  "cloudpayments",
  "yookassa",
  "manual",
];

export const PAYMENT_MODES: readonly PaymentMode[] = ["test", "live"];

/**
 * Required secret field names per provider. Mirrors
 * apps/backend/internal/platform/httpserver/payment_configs_types.go
 * (requiredSecretFields). The backend rejects providers we do not know,
 * and recomputes status after every write based on these fields. If the
 * backend list grows, this table must follow.
 */
export const PROVIDER_REQUIRED_SECRETS: Readonly<
  Record<PaymentProvider, readonly string[]>
> = {
  stripe: ["api_key", "webhook_secret"],
  allpay: ["merchant_id", "secret_key"],
  cloudpayments: ["public_id", "api_secret"],
  yookassa: ["shop_id", "secret_key"],
  manual: [],
};

export interface PaymentConfig {
  readonly id: string;
  readonly org_id: string;
  readonly provider: PaymentProvider | string;
  readonly mode: PaymentMode | string;
  readonly provider_account_id: string | null;
  readonly public_config: Record<string, unknown> | null;
  readonly secret_fields_set: readonly string[];
  readonly status: PaymentConfigStatus | string;
  readonly missing_required_fields: readonly string[];
  readonly is_active: boolean;
  readonly created_at: string;
  readonly updated_at: string;
}

interface PaymentConfigListEnvelope {
  readonly payment_configs: readonly PaymentConfig[];
}

interface PaymentConfigEnvelope {
  readonly payment_config: PaymentConfig;
}

// ---------------------------------------------------------------------------
// Validators (mirror backend contracts)
// ---------------------------------------------------------------------------

export const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function validateOrgID(orgID: string): string | null {
  if (orgID.trim() === "") {
    return "Organization ID is required";
  }
  if (!UUID_RE.test(orgID.trim())) {
    return "Organization ID must be a UUID";
  }
  return null;
}

export function validateProvider(provider: string): string | null {
  if (!(PAYMENT_PROVIDERS as readonly string[]).includes(provider)) {
    return `Provider must be one of ${PAYMENT_PROVIDERS.join(", ")}`;
  }
  return null;
}

export function validateMode(mode: string): string | null {
  if (!(PAYMENT_MODES as readonly string[]).includes(mode)) {
    return "Mode must be 'test' or 'live'";
  }
  return null;
}

export function validatePublicConfigJSON(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return "Public config must be valid JSON";
  }
  if (
    typeof parsed !== "object" ||
    parsed === null ||
    Array.isArray(parsed)
  ) {
    return "Public config must be a JSON object (not an array or scalar)";
  }
  return null;
}

// ---------------------------------------------------------------------------
// Nav entry binding
// ---------------------------------------------------------------------------

const PAYMENTS_NAV_ENTRY = NAV_BY_PATH["/payments"];
if (PAYMENTS_NAV_ENTRY === undefined) {
  throw new Error("payments route: NAV_BY_PATH['/payments'] missing");
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

function PaymentsRoute() {
  return (
    <RequirePermission entry={PAYMENTS_NAV_ENTRY}>
      <PaymentsModule />
    </RequirePermission>
  );
}

type FormMode =
  | { kind: "closed" }
  | { kind: "create" }
  | { kind: "edit"; config: PaymentConfig };

function PaymentsModule() {
  const { permissions } = useAuth();
  const { activeScope } = useScope();
  const canWrite = permissions.has("payment_config.write");

  const scopeOrgID =
    activeScope?.kind === "organization" && activeScope.id !== null
      ? activeScope.id
      : "";

  const [orgID, setOrgID] = useState(scopeOrgID);
  const trimmedOrgID = orgID.trim();
  const orgIDError = orgID === "" ? null : validateOrgID(orgID);
  const orgReady = trimmedOrgID !== "" && orgIDError === null;

  const [form, setForm] = useState<FormMode>({ kind: "closed" });
  const [pendingDelete, setPendingDelete] =
    useState<PaymentConfig | null>(null);

  const query = useQuery<PaymentConfigListEnvelope, ApiError>({
    queryKey: ["payment_configs", "list", trimmedOrgID],
    enabled: orgReady,
    queryFn: () =>
      authedFetch<PaymentConfigListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${trimmedOrgID}/payment-configs`,
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

  const rows = query.data?.payment_configs ?? [];
  const sorted = useMemo(
    () =>
      [...rows].sort((a, b) => {
        const byProvider = a.provider.localeCompare(b.provider);
        if (byProvider !== 0) {
          return byProvider;
        }
        return a.mode.localeCompare(b.mode);
      }),
    [rows],
  );

  return (
    <section aria-labelledby="payments-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="payments-heading" style={headingStyle}>
            Payment Configs
          </h1>
          <p style={subheadingStyle}>
            Per-organization payment provider configurations. Each row
            holds the provider slug, mode (test / live), the
            provider-side merchant account identifier (when applicable),
            and the credential set. Secrets are stored encrypted on the
            backend and are NEVER returned to the UI — only the names
            of stored secret fields and the list of required fields
            still missing are visible. The fiscal printer and POS-side
            settings remain out of scope.
          </p>
        </div>
        <div style={refreshWrapStyle}>
          <button
            type="button"
            onClick={() => query.refetch()}
            style={refreshButtonStyle}
            disabled={!orgReady || query.isFetching}
            data-testid="payments-refresh"
          >
            {query.isFetching ? "Refreshing…" : "Refresh"}
          </button>
          {canWrite ? (
            <button
              type="button"
              onClick={() => setForm({ kind: "create" })}
              style={primaryButtonStyle}
              disabled={!orgReady}
              data-testid="payments-new"
            >
              New payment config
            </button>
          ) : (
            <span style={mutedHintStyle} title="Requires payment_config.write">
              Create requires payment_config.write
            </span>
          )}
        </div>
      </header>

      <div style={orgPickerStyle}>
        <label htmlFor="payments-org-id" style={fieldLabelStyle}>
          Organization ID
        </label>
        <input
          id="payments-org-id"
          type="text"
          value={orgID}
          onChange={(e) => setOrgID(e.target.value)}
          style={inputMonoStyle}
          placeholder="00000000-0000-0000-0000-000000000000"
          maxLength={36}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          data-testid="payments-org-id"
        />
        {orgIDError !== null ? (
          <div
            style={fieldErrorStyle}
            role="alert"
            data-testid="payments-org-id-error"
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

      <PaymentsBody
        query={query}
        orgReady={orgReady}
        rows={sorted}
        canWrite={canWrite}
        onEdit={(config) => setForm({ kind: "edit", config })}
        onDelete={(config) => setPendingDelete(config)}
      />

      {form.kind !== "closed" ? (
        <PaymentConfigFormDialog
          mode={form}
          orgID={trimmedOrgID}
          onClose={() => setForm({ kind: "closed" })}
        />
      ) : null}

      {pendingDelete !== null ? (
        <DeleteConfirmDialog
          config={pendingDelete}
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
  query: ReturnType<typeof useQuery<PaymentConfigListEnvelope, ApiError>>;
  orgReady: boolean;
  rows: readonly PaymentConfig[];
  canWrite: boolean;
  onEdit: (config: PaymentConfig) => void;
  onDelete: (config: PaymentConfig) => void;
}

function PaymentsBody({
  query,
  orgReady,
  rows,
  canWrite,
  onEdit,
  onDelete,
}: BodyProps) {
  if (!orgReady) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="payments-org-required">
        Enter an organization UUID above to load its payment configs.
      </div>
    );
  }
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading payment configs…
      </div>
    );
  }
  if (query.isError) {
    return (
      <PaymentsErrorState
        error={query.error}
        onRetry={() => query.refetch()}
      />
    );
  }
  if (rows.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="payments-empty">
        No payment configs exist for this organization yet. Create one
        to wire a provider (Stripe / AllPay / CloudPayments / YooKassa)
        to checkout.
      </div>
    );
  }
  return (
    <div style={tableWrapStyle} role="region" aria-label="Payment configs">
      <table style={tableStyle} data-testid="payments-table">
        <thead>
          <tr>
            <th scope="col" style={thStyle}>Provider</th>
            <th scope="col" style={thStyle}>Mode</th>
            <th scope="col" style={thStyle}>Account ID</th>
            <th scope="col" style={thStyle}>Status</th>
            <th scope="col" style={thStyle}>Secrets</th>
            <th scope="col" style={thStyle}>Active</th>
            <th scope="col" style={thStyle}>Updated</th>
            <th scope="col" style={thStyle} aria-label="Actions" />
          </tr>
        </thead>
        <tbody>
          {rows.map((c) => (
            <tr key={c.id} data-testid={`payments-row-${c.id}`}>
              <td style={tdStyle}>{c.provider}</td>
              <td style={tdStyle}>
                <ModeBadge mode={c.mode} />
              </td>
              <td style={tdMonoStyle}>
                {c.provider_account_id ?? "—"}
              </td>
              <td style={tdStyle}>
                <StatusBadge
                  status={c.status}
                  missing={c.missing_required_fields}
                />
              </td>
              <td style={tdStyle}>
                <SecretsSummary
                  set={c.secret_fields_set}
                  missing={c.missing_required_fields}
                />
              </td>
              <td style={tdStyle}>{c.is_active ? "yes" : "no"}</td>
              <td style={tdStyle}>{formatDate(c.updated_at)}</td>
              <td style={tdActionsStyle}>
                {canWrite ? (
                  <button
                    type="button"
                    style={rowActionButtonStyle}
                    onClick={() => onEdit(c)}
                    data-testid={`payments-edit-${c.id}`}
                  >
                    Edit
                  </button>
                ) : null}
                {canWrite ? (
                  <button
                    type="button"
                    style={rowDangerButtonStyle}
                    onClick={() => onDelete(c)}
                    data-testid={`payments-delete-${c.id}`}
                  >
                    Archive
                  </button>
                ) : null}
                {!canWrite ? (
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

function ModeBadge({ mode }: { mode: string }) {
  const live = mode === "live";
  return (
    <span
      style={{
        ...badgeBaseStyle,
        background: live ? "#dcfce7" : "#fef9c3",
        color: live ? "#166534" : "#854d0e",
        borderColor: live ? "#bbf7d0" : "#fde68a",
      }}
      data-testid={`payments-mode-${mode}`}
    >
      {mode}
    </span>
  );
}

function StatusBadge({
  status,
  missing,
}: {
  status: string;
  missing: readonly string[];
}) {
  if (status === "configured") {
    return (
      <span
        style={{
          ...badgeBaseStyle,
          background: "#dcfce7",
          color: "#166534",
          borderColor: "#bbf7d0",
        }}
        data-testid="payments-status-configured"
      >
        configured
      </span>
    );
  }
  const title =
    missing.length > 0
      ? `Missing: ${missing.join(", ")}`
      : "Missing required fields";
  return (
    <span
      style={{
        ...badgeBaseStyle,
        background: "#fee2e2",
        color: "#7f1d1d",
        borderColor: "#fecaca",
      }}
      title={title}
      data-testid="payments-status-missing"
    >
      missing required fields
    </span>
  );
}

function SecretsSummary({
  set,
  missing,
}: {
  set: readonly string[];
  missing: readonly string[];
}) {
  if (set.length === 0 && missing.length === 0) {
    return <span style={mutedHintStyle}>no required secrets</span>;
  }
  return (
    <div style={secretsSummaryStyle}>
      {set.map((k) => (
        <span
          key={`set-${k}`}
          style={secretChipSetStyle}
          title={`Stored: ${k} (value masked)`}
        >
          {k}: •••
        </span>
      ))}
      {missing.map((k) => (
        <span
          key={`missing-${k}`}
          style={secretChipMissingStyle}
          title={`Required but not set: ${k}`}
        >
          {k}: missing
        </span>
      ))}
    </div>
  );
}

function PaymentsErrorState({
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
      <div style={errorBoxStyle} role="alert" data-testid="payments-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing{" "}
          <code style={monoStyle}>payment_config.read</code>. Ask a
          platform administrator to grant the permission.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div
        style={errorBoxStyle}
        role="status"
        data-testid="payments-session-expired"
      >
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload payment configs.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="payments-error">
      <strong>Failed to load payment configs.</strong>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? (
        <div style={errorParaStyle}>{error.message}</div>
      ) : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Create / Edit modal
// ---------------------------------------------------------------------------

interface ServerFieldErrors {
  provider?: string;
  mode?: string;
  provider_account_id?: string;
  public_config?: string;
  secrets?: string;
  form?: string;
}

interface FormDialogProps {
  mode: Extract<FormMode, { kind: "create" } | { kind: "edit" }>;
  orgID: string;
  onClose: () => void;
}

function PaymentConfigFormDialog({ mode, orgID, onClose }: FormDialogProps) {
  const queryClient = useQueryClient();
  const isEdit = mode.kind === "edit";

  const initialProvider: PaymentProvider = isEdit
    ? (mode.config.provider as PaymentProvider)
    : "stripe";
  const initialMode: PaymentMode = isEdit
    ? (mode.config.mode as PaymentMode)
    : "test";
  const initialAccountID = isEdit
    ? (mode.config.provider_account_id ?? "")
    : "";
  const initialPublicConfig = isEdit && mode.config.public_config !== null
    ? JSON.stringify(mode.config.public_config, null, 2)
    : "";
  const initialIsActive = isEdit ? mode.config.is_active : true;

  const [provider, setProvider] = useState<PaymentProvider>(initialProvider);
  const [paymentMode, setPaymentMode] = useState<PaymentMode>(initialMode);
  const [accountID, setAccountID] = useState(initialAccountID);
  const [publicConfig, setPublicConfig] = useState(initialPublicConfig);
  const [isActive, setIsActive] = useState(initialIsActive);
  // Per-secret-key inputs. For create: all required keys empty. For
  // edit: empty inputs (user only types the keys they want to replace).
  const [secretInputs, setSecretInputs] = useState<Record<string, string>>(
    () => initialSecretInputs(initialProvider),
  );
  // Tracks which keys the user explicitly chose to clear via "Clear".
  const [clearedKeys, setClearedKeys] = useState<Set<string>>(new Set());
  const [serverErrors, setServerErrors] = useState<ServerFieldErrors>({});

  const providerErr = validateProvider(provider);
  const modeErr = validateMode(paymentMode);
  const publicConfigErr = validatePublicConfigJSON(publicConfig);
  const localValid =
    providerErr === null && modeErr === null && publicConfigErr === null;

  // For edit, allow submission when at least one field changed.
  const dirty =
    !isEdit ||
    accountID.trim() !== initialAccountID ||
    publicConfig.trim() !== initialPublicConfig.trim() ||
    isActive !== initialIsActive ||
    Object.values(secretInputs).some((v) => v.trim() !== "") ||
    clearedKeys.size > 0;

  const requiredSecrets = PROVIDER_REQUIRED_SECRETS[provider] ?? [];
  const storedKeys = isEdit ? mode.config.secret_fields_set : [];
  // Union of required + already-stored keys so an operator can still
  // edit secrets the backend remembers even if our catalogue evolves.
  const allKnownKeys = useMemo(() => {
    const set = new Set<string>(requiredSecrets);
    for (const k of storedKeys) {
      set.add(k);
    }
    return [...set].sort();
  }, [requiredSecrets, storedKeys]);

  function setSecretField(key: string, value: string) {
    setSecretInputs((prev) => ({ ...prev, [key]: value }));
    if (clearedKeys.has(key)) {
      const next = new Set(clearedKeys);
      next.delete(key);
      setClearedKeys(next);
    }
    if (serverErrors.secrets !== undefined) {
      setServerErrors({ ...serverErrors, secrets: undefined });
    }
  }

  function toggleClear(key: string) {
    const next = new Set(clearedKeys);
    if (next.has(key)) {
      next.delete(key);
    } else {
      next.add(key);
      // Wipe any in-progress text so we don't accidentally send both.
      setSecretInputs((prev) => ({ ...prev, [key]: "" }));
    }
    setClearedKeys(next);
  }

  const mutation = useMutation<PaymentConfigEnvelope, ApiError, void>({
    mutationFn: () => {
      // Build the secrets patch map. Skip untouched keys so the backend
      // preserves the existing value.
      const secretsPatch: Record<string, string> = {};
      for (const [k, v] of Object.entries(secretInputs)) {
        const trimmed = v.trim();
        if (trimmed !== "") {
          secretsPatch[k] = trimmed;
        }
      }
      for (const k of clearedKeys) {
        secretsPatch[k] = "";
      }

      const publicConfigParsed =
        publicConfig.trim() === "" ? undefined : JSON.parse(publicConfig);

      if (isEdit) {
        const body: Record<string, unknown> = {};
        if (accountID.trim() !== initialAccountID) {
          body.provider_account_id = accountID.trim();
        }
        if (publicConfig.trim() !== initialPublicConfig.trim()) {
          body.public_config = publicConfigParsed ?? {};
        }
        if (Object.keys(secretsPatch).length > 0) {
          body.secrets = secretsPatch;
        }
        if (isActive !== initialIsActive) {
          body.is_active = isActive;
        }
        return authedFetch<PaymentConfigEnvelope>({
          method: "PATCH",
          path: `/v1/organizations/${orgID}/payment-configs/${mode.config.id}`,
          body,
        });
      }

      const body: Record<string, unknown> = {
        provider,
        mode: paymentMode,
        is_active: isActive,
      };
      if (accountID.trim() !== "") {
        body.provider_account_id = accountID.trim();
      }
      if (publicConfigParsed !== undefined) {
        body.public_config = publicConfigParsed;
      }
      if (Object.keys(secretsPatch).length > 0) {
        body.secrets = secretsPatch;
      }
      return authedFetch<PaymentConfigEnvelope>({
        method: "POST",
        path: `/v1/organizations/${orgID}/payment-configs`,
        body,
      });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["payment_configs"] });
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

  const title = isEdit
    ? `Edit ${mode.config.provider} (${mode.config.mode})`
    : "New payment config";
  const submitLabel = isEdit ? "Save changes" : "Create config";

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="payments-form-title"
      style={dialogBackdropStyle}
      data-testid="payments-form-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="payments-form-title" style={dialogTitleStyle}>
            {title}
          </h2>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="payments-form-close"
          >
            ×
          </button>
        </header>
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          <FieldRow
            label="Provider"
            htmlFor="payment-provider"
            error={serverErrors.provider ?? null}
            localError={providerErr}
            hint="Payment gateway slug. Required secrets per provider are shown below."
          >
            <select
              id="payment-provider"
              value={provider}
              onChange={(e) => {
                const next = e.target.value as PaymentProvider;
                setProvider(next);
                // Reset secret inputs to the new provider's required-key set.
                if (!isEdit) {
                  setSecretInputs(initialSecretInputs(next));
                  setClearedKeys(new Set());
                }
              }}
              style={inputStyle}
              disabled={isEdit}
              data-testid="payments-form-provider"
            >
              {PAYMENT_PROVIDERS.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </FieldRow>
          <FieldRow
            label="Mode"
            htmlFor="payment-mode"
            error={serverErrors.mode ?? null}
            localError={modeErr}
            hint="'test' uses sandbox credentials; 'live' charges real cards. One row per (provider, mode)."
          >
            <select
              id="payment-mode"
              value={paymentMode}
              onChange={(e) => setPaymentMode(e.target.value as PaymentMode)}
              style={inputStyle}
              disabled={isEdit}
              data-testid="payments-form-mode"
            >
              {PAYMENT_MODES.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
          </FieldRow>
          <FieldRow
            label="Provider account ID"
            htmlFor="payment-account-id"
            error={serverErrors.provider_account_id ?? null}
            localError={null}
            hint="Provider-side merchant identifier shown in dashboards (acct_…, shop id, etc.). Optional."
          >
            <input
              id="payment-account-id"
              type="text"
              value={accountID}
              onChange={(e) => setAccountID(e.target.value)}
              style={inputMonoStyle}
              maxLength={200}
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-testid="payments-form-account-id"
            />
          </FieldRow>
          <FieldRow
            label="Public config (JSON)"
            htmlFor="payment-public-config"
            error={serverErrors.public_config ?? null}
            localError={publicConfigErr}
            hint="Non-secret provider settings (webhook URL, statement descriptor, …). Optional. Must be a JSON object."
          >
            <textarea
              id="payment-public-config"
              value={publicConfig}
              onChange={(e) => setPublicConfig(e.target.value)}
              style={textareaStyle}
              rows={4}
              spellCheck={false}
              data-testid="payments-form-public-config"
            />
          </FieldRow>

          <fieldset style={secretsFieldsetStyle}>
            <legend style={secretsLegendStyle}>
              Secrets
              <span style={mutedHintStyle}>
                {" "}
                — values are masked on read; leave blank to keep existing
              </span>
            </legend>
            {allKnownKeys.length === 0 ? (
              <p style={fieldHintStyle}>
                The {provider} provider has no required secret fields.
              </p>
            ) : (
              allKnownKeys.map((key) => {
                const stored = storedKeys.includes(key);
                const cleared = clearedKeys.has(key);
                const value = secretInputs[key] ?? "";
                return (
                  <div key={key} style={secretRowStyle}>
                    <label
                      htmlFor={`payment-secret-${key}`}
                      style={secretLabelStyle}
                    >
                      {key}
                      {requiredSecrets.includes(key) ? (
                        <span style={requiredMarkStyle} title="Required">
                          {" "}
                          *
                        </span>
                      ) : null}
                    </label>
                    <input
                      id={`payment-secret-${key}`}
                      type="password"
                      value={value}
                      onChange={(e) => setSecretField(key, e.target.value)}
                      style={inputMonoStyle}
                      placeholder={
                        stored && !cleared
                          ? "••• stored (leave blank to keep)"
                          : "(not set)"
                      }
                      autoCapitalize="off"
                      autoCorrect="off"
                      spellCheck={false}
                      disabled={cleared}
                      data-testid={`payments-form-secret-${key}`}
                    />
                    {stored ? (
                      <button
                        type="button"
                        onClick={() => toggleClear(key)}
                        style={
                          cleared
                            ? secretClearActiveStyle
                            : secretClearButtonStyle
                        }
                        data-testid={`payments-form-clear-${key}`}
                      >
                        {cleared ? "Undo clear" : "Clear"}
                      </button>
                    ) : null}
                  </div>
                );
              })
            )}
            {serverErrors.secrets !== undefined ? (
              <div
                style={fieldErrorStyle}
                role="alert"
                data-testid="payments-form-secrets-error"
              >
                {serverErrors.secrets}
              </div>
            ) : null}
          </fieldset>

          <FieldRow
            label="Active"
            htmlFor="payment-is-active"
            error={null}
            localError={null}
            hint="Inactive configs are kept on file but skipped at checkout."
          >
            <label style={checkboxRowStyle}>
              <input
                id="payment-is-active"
                type="checkbox"
                checked={isActive}
                onChange={(e) => setIsActive(e.target.checked)}
                data-testid="payments-form-is-active"
              />
              <span>Enabled for checkout</span>
            </label>
          </FieldRow>

          {serverErrors.form !== undefined ? (
            <div
              style={formErrorStyle}
              role="alert"
              data-testid="payments-form-error"
            >
              {serverErrors.form}
            </div>
          ) : null}

          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="payments-form-cancel"
            >
              Cancel
            </button>
            <button
              type="submit"
              style={primaryButtonStyle}
              disabled={!localValid || !dirty || mutation.isPending}
              data-testid="payments-form-submit"
            >
              {mutation.isPending ? "Saving…" : submitLabel}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function initialSecretInputs(provider: PaymentProvider): Record<string, string> {
  const out: Record<string, string> = {};
  for (const key of PROVIDER_REQUIRED_SECRETS[provider] ?? []) {
    out[key] = "";
  }
  return out;
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
        <div
          style={fieldErrorStyle}
          role="alert"
          data-testid={`${htmlFor}-error`}
        >
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
  config,
  onClose,
}: {
  config: PaymentConfig;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [error, setError] = useState<ApiError | null>(null);

  const mutation = useMutation<unknown, ApiError, void>({
    mutationFn: () =>
      authedFetch<unknown>({
        method: "DELETE",
        path: `/v1/organizations/${config.org_id}/payment-configs/${config.id}`,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["payment_configs"] });
      onClose();
    },
    onError: (err) => setError(err),
  });

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="payments-delete-title"
      style={dialogBackdropStyle}
      data-testid="payments-delete-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="payments-delete-title" style={dialogTitleStyle}>
            Archive {config.provider} ({config.mode})?
          </h2>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="payments-delete-close"
          >
            ×
          </button>
        </header>
        <div style={archiveBodyStyle}>
          <p style={archiveParaStyle}>
            Archiving a payment config removes it from the active list
            and stops it being used at checkout. This is a{" "}
            <strong>soft-delete</strong>: the row is preserved with{" "}
            <code style={monoStyle}>deleted_at</code> set and a{" "}
            <code style={monoStyle}>v1.payment_config.delete</code>{" "}
            audit event is written atomically. The encrypted secrets
            remain in the row but are no longer reachable through the
            API.
          </p>
          {error !== null ? (
            <div
              style={formErrorStyle}
              role="alert"
              data-testid="payments-delete-error"
            >
              {error.message} (<code style={monoStyle}>{error.code}</code>)
            </div>
          ) : null}
          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="payments-delete-cancel"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => mutation.mutate()}
              style={dangerButtonStyle}
              disabled={mutation.isPending}
              data-testid="payments-delete-confirm"
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
 * Map an envelope from payment_configs*.go onto field-level errors.
 * The backend emits `details.field` for the most common validation
 * errors; the catch-all uses that field if present, otherwise lands
 * the message in the form-level error surface.
 */
export function mapServerError(err: ApiError): ServerFieldErrors {
  const out: ServerFieldErrors = {};
  const field =
    err.details !== undefined && typeof err.details.field === "string"
      ? err.details.field
      : undefined;
  switch (err.code) {
    case "payment_config.invalid_provider":
    case "payment_config.unsupported_provider":
      out.provider = err.message;
      return out;
    case "payment_config.invalid_mode":
      out.mode = err.message;
      return out;
    case "payment_config.invalid_public_config":
      out.public_config = err.message;
      return out;
    case "payment_config.invalid_secrets":
      out.secrets = err.message;
      return out;
    case "payment_config.duplicate":
      out.form = err.message;
      return out;
    case "payment_config.not_found":
      out.form = err.message;
      return out;
    case "payment_config.empty_body":
    case "payment_config.invalid_body":
    case "payment_config.invalid_json":
      out.form = err.message;
      return out;
    case "permissions.denied":
      out.form =
        "Your account is missing payment_config.write. Ask a platform administrator.";
      return out;
    default:
      if (field === "provider") {
        out.provider = err.message;
      } else if (field === "mode") {
        out.mode = err.message;
      } else if (field === "public_config") {
        out.public_config = err.message;
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
// Styles (mirror venues / channels)
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

const orgPickerStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  maxWidth: 480,
};

const badgeBaseStyle: CSSProperties = {
  display: "inline-block",
  padding: "2px 8px",
  fontSize: 11,
  fontWeight: 600,
  borderRadius: 999,
  border: "1px solid transparent",
  letterSpacing: 0.2,
};

const secretsSummaryStyle: CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  gap: 4,
  alignItems: "center",
};

const secretChipSetStyle: CSSProperties = {
  display: "inline-block",
  padding: "2px 6px",
  fontSize: 11,
  borderRadius: 4,
  border: "1px solid #cbd5e1",
  background: "#f8fafc",
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  color: "#334155",
};

const secretChipMissingStyle: CSSProperties = {
  display: "inline-block",
  padding: "2px 6px",
  fontSize: 11,
  borderRadius: 4,
  border: "1px solid #fca5a5",
  background: "#fef2f2",
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  color: "#7f1d1d",
};

const secretsFieldsetStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: 12,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#f8fafc",
};

const secretsLegendStyle: CSSProperties = {
  fontSize: 12,
  fontWeight: 600,
  color: "#334155",
  padding: "0 4px",
};

const secretRowStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "minmax(140px, max-content) 1fr auto",
  gap: 8,
  alignItems: "center",
};

const secretLabelStyle: CSSProperties = {
  fontSize: 12,
  fontWeight: 500,
  color: "#334155",
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
};

const requiredMarkStyle: CSSProperties = {
  color: "#b91c1c",
};

const secretClearButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 8px",
  background: "#ffffff",
  border: "1px solid #fca5a5",
  borderRadius: 4,
  cursor: "pointer",
  color: "#7f1d1d",
};

const secretClearActiveStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 8px",
  background: "#fee2e2",
  border: "1px solid #b91c1c",
  borderRadius: 4,
  cursor: "pointer",
  color: "#7f1d1d",
  fontWeight: 600,
};

const checkboxRowStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 8,
  fontSize: 13,
  color: "#0f172a",
};
