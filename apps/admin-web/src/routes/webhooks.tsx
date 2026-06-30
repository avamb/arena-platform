/**
 * SuperAdmin Webhook Subscribers CRUD module (Feature #294 — S-3).
 *
 * Wires the existing webhook_subscribers backend surface (originally
 * shipped as Feature #156 for WordPress webhook registration) to a
 * SuperAdmin-managed admin route. Backed by:
 *
 *   GET    /v1/webhooks/subscribers
 *   POST   /v1/webhooks/subscribers
 *   GET    /v1/webhooks/subscribers/{id}
 *   PATCH  /v1/webhooks/subscribers/{id}
 *   DELETE /v1/webhooks/subscribers/{id}
 *   GET    /v1/webhooks/subscribers/{id}/recent-deliveries
 *
 * RBAC: `webhook.subscriber.manage` (same permission the create/list
 * endpoints already enforce).
 *
 * UI shape (matches the master spec for this feature):
 *
 *   - List table — site_url / callback_url / event_types / active /
 *     created_at, plus inline "Edit" / "Delete" actions.
 *   - Create form — callback_url, site_url, event_types multi-select.
 *     The generated `signing_secret` is shown EXACTLY ONCE in a
 *     dismissable banner immediately after creation. Subsequent reads
 *     never return it (backend invariant).
 *   - Edit form — event_types multi-select + active toggle. The
 *     signing_secret field is intentionally not editable; rotating
 *     requires a deactivate + re-register cycle (matches the
 *     write-only contract in wp_webhooks.go).
 *   - Recent deliveries panel — best-effort listing of the most recent
 *     outbox rows whose event_type would be dispatched to this
 *     subscriber. The dispatcher does NOT persist per-subscriber
 *     delivery rows today, so the attempts / dispatched_at columns
 *     reflect the source-event aggregate. The panel labels this so
 *     operators do not misread the data during an incident.
 *
 * Mock data: NONE. Every screen hits the live backend through
 * authedFetch — no globalThis / devStore / mockDb / fakeData.
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
import { NAV_BY_PATH } from "@/lib/auth/navConfig";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/webhooks",
  component: WebhooksRoute,
});

const WEBHOOKS_NAV_ENTRY = NAV_BY_PATH["/webhooks"];
if (WEBHOOKS_NAV_ENTRY === undefined) {
  throw new Error("webhooks route: NAV_BY_PATH['/webhooks'] missing");
}

// ---------------------------------------------------------------------------
// Backend response shapes (hand-typed; we deliberately do not import the
// generated OpenAPI types at runtime to keep the route lazy-load small).
// ---------------------------------------------------------------------------

/** Stable set of event_type strings the dispatcher knows how to emit today. */
export const KNOWN_EVENT_TYPES: readonly string[] = [
  "order_paid",
  "ticket_issued",
  "refund_succeeded",
  "v1.ticket.refunded",
  "v1.ticket.revoked",
  "v1.ticket.scanned",
  "v1.session.cancelled",
] as const;

export interface WebhookSubscriberSummary {
  readonly subscriber_id: string;
  readonly site_url: string;
  readonly callback_url: string;
  readonly event_types: readonly string[];
  readonly active: boolean;
  readonly created_at: string;
}

export interface RegisterSubscriberResponse {
  readonly subscriber_id: string;
  readonly site_url: string;
  readonly callback_url: string;
  readonly event_types: readonly string[];
  readonly active: boolean;
  /**
   * The freshly-generated HMAC-SHA256 signing key. Returned ONCE at
   * registration time; never retrievable afterwards. The UI displays it
   * in a dismissable banner so the operator can copy it into the
   * subscribing system (e.g. the WordPress plugin's "Webhook Secret"
   * settings field).
   */
  readonly signing_secret: string;
}

export interface RecentDeliveryAttempt {
  readonly event_id: string;
  readonly event_type: string;
  readonly occurred_at: string;
  /**
   * Absent when the source outbox row has not yet been dispatched.
   * The platform's openapi dialect avoids `nullable` (OAS 3.0) and the
   * `type: [string, "null"]` 2020-12 idiom oapi-codegen does not yet
   * support; the field is simply omitted instead of being `null`.
   */
  readonly dispatched_at?: string;
  readonly attempts: number;
}

export interface RecentDeliveriesResponse {
  readonly subscriber_id: string;
  readonly wildcard: boolean;
  readonly event_types: readonly string[];
  readonly attempts: readonly RecentDeliveryAttempt[];
  readonly total: number;
}

interface ListEnvelope {
  readonly subscribers: readonly WebhookSubscriberSummary[];
  readonly total: number;
}

// ---------------------------------------------------------------------------
// Validators
// ---------------------------------------------------------------------------

/**
 * Light-touch callback URL validation that mirrors what the backend
 * accepts: a non-empty https:// (or http:// for local dev) URL. The
 * server is the authority — we just protect against the obvious typo
 * before submitting.
 */
export function validateCallbackUrl(raw: string): string | null {
  const value = raw.trim();
  if (value === "") {
    return "Callback URL is required.";
  }
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return "Enter a valid absolute URL (https://…).";
  }
  if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
    return "Callback URL must use http or https.";
  }
  return null;
}

/** Site URL is optional; backend defaults it to callback_url on create. */
export function validateSiteUrl(raw: string): string | null {
  const value = raw.trim();
  if (value === "") {
    return null;
  }
  try {
    const parsed = new URL(value);
    if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
      return "Site URL must use http or https.";
    }
  } catch {
    return "Enter a valid absolute URL (https://…).";
  }
  return null;
}

// ---------------------------------------------------------------------------
// API helpers
// ---------------------------------------------------------------------------

const SUBSCRIBERS_PATH = "/v1/webhooks/subscribers";

async function listSubscribers(): Promise<readonly WebhookSubscriberSummary[]> {
  const env = await authedFetch<ListEnvelope>({
    method: "GET",
    path: SUBSCRIBERS_PATH,
  });
  return env.subscribers;
}

async function registerSubscriber(req: {
  callback_url: string;
  site_url: string;
  event_types: readonly string[];
}): Promise<RegisterSubscriberResponse> {
  return authedFetch<RegisterSubscriberResponse>({
    method: "POST",
    path: SUBSCRIBERS_PATH,
    body: req,
  });
}

async function patchSubscriber(
  id: string,
  body: { event_types?: readonly string[]; active?: boolean },
): Promise<WebhookSubscriberSummary> {
  return authedFetch<WebhookSubscriberSummary>({
    method: "PATCH",
    path: `${SUBSCRIBERS_PATH}/${id}`,
    body,
  });
}

async function deleteSubscriber(id: string): Promise<void> {
  await authedFetch<unknown>({
    method: "DELETE",
    path: `${SUBSCRIBERS_PATH}/${id}`,
  });
}

async function fetchRecentDeliveries(
  id: string,
): Promise<RecentDeliveriesResponse> {
  return authedFetch<RecentDeliveriesResponse>({
    method: "GET",
    path: `${SUBSCRIBERS_PATH}/${id}/recent-deliveries`,
  });
}

// ---------------------------------------------------------------------------
// Route
// ---------------------------------------------------------------------------

function WebhooksRoute() {
  return (
    <RequirePermission entry={WEBHOOKS_NAV_ENTRY}>
      <WebhooksAdmin />
    </RequirePermission>
  );
}

function WebhooksAdmin() {
  const queryClient = useQueryClient();
  const listQuery = useQuery({
    queryKey: ["webhooks", "subscribers"],
    queryFn: listSubscribers,
    staleTime: 5_000,
  });

  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [secretBanner, setSecretBanner] =
    useState<RegisterSubscriberResponse | null>(null);

  const subscribers = listQuery.data ?? [];
  const selected = useMemo(
    () => subscribers.find((s) => s.subscriber_id === selectedId) ?? null,
    [subscribers, selectedId],
  );

  function invalidate() {
    void queryClient.invalidateQueries({ queryKey: ["webhooks", "subscribers"] });
  }

  return (
    <section style={pageStyle} aria-labelledby="webhooks-heading">
      <header style={headerStyle}>
        <div>
          <h1 id="webhooks-heading" style={headingStyle}>
            Webhook Subscribers
          </h1>
          <p style={subheadingStyle}>
            Manage HTTP endpoints that receive signed outbox events from
            Arena. Each subscriber stores a callback URL, an event-type
            filter (empty = wildcard), and an HMAC-SHA256 signing
            secret used by the dispatcher to sign every delivery body.
          </p>
        </div>
        <div>
          <button
            type="button"
            style={primaryButtonStyle}
            onClick={() => setShowCreate((v) => !v)}
            data-testid="webhooks-toggle-create"
          >
            {showCreate ? "Cancel" : "Register subscriber"}
          </button>
        </div>
      </header>

      {secretBanner !== null ? (
        <SecretBanner
          response={secretBanner}
          onDismiss={() => setSecretBanner(null)}
        />
      ) : null}

      {showCreate ? (
        <CreateSubscriberForm
          onCreated={(resp) => {
            setSecretBanner(resp);
            setShowCreate(false);
            invalidate();
          }}
        />
      ) : null}

      {listQuery.isLoading ? (
        <div style={statusBoxStyle} role="status">
          Loading subscribers…
        </div>
      ) : null}
      {listQuery.isError ? (
        <div style={errorBoxStyle} role="alert" data-testid="webhooks-list-error">
          {(listQuery.error as ApiError).message ?? "Failed to load subscribers."}
        </div>
      ) : null}

      {!listQuery.isLoading && subscribers.length === 0 ? (
        <div style={statusBoxStyle} role="status" data-testid="webhooks-empty">
          No webhook subscribers registered yet. The dispatcher will not
          fan out events until at least one row is active.
        </div>
      ) : null}

      {subscribers.length > 0 ? (
        <SubscribersTable
          subscribers={subscribers}
          selectedId={selectedId}
          onSelect={(id) => setSelectedId(id === selectedId ? null : id)}
          onDelete={(id) => {
            if (typeof window !== "undefined" && window.confirm) {
              if (!window.confirm("Deactivate this subscriber? The row is retained for audit.")) {
                return;
              }
            }
            void deleteSubscriber(id).then(invalidate);
          }}
        />
      ) : null}

      {selected !== null ? (
        <SubscriberDrawer
          subscriber={selected}
          onUpdated={invalidate}
          onClose={() => setSelectedId(null)}
        />
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Subscribers table
// ---------------------------------------------------------------------------

function SubscribersTable({
  subscribers,
  selectedId,
  onSelect,
  onDelete,
}: {
  readonly subscribers: readonly WebhookSubscriberSummary[];
  readonly selectedId: string | null;
  readonly onSelect: (id: string) => void;
  readonly onDelete: (id: string) => void;
}) {
  return (
    <table style={tableStyle} data-testid="webhooks-table">
      <thead>
        <tr>
          <th style={thStyle}>Site</th>
          <th style={thStyle}>Callback URL</th>
          <th style={thStyle}>Event types</th>
          <th style={thStyle}>Active</th>
          <th style={thStyle}>Created</th>
          <th style={thStyle} />
        </tr>
      </thead>
      <tbody>
        {subscribers.map((s) => {
          const isSelected = s.subscriber_id === selectedId;
          return (
            <tr
              key={s.subscriber_id}
              style={isSelected ? trSelectedStyle : trStyle}
              data-testid={`webhooks-row-${s.subscriber_id}`}
            >
              <td style={tdStyle}>{s.site_url}</td>
              <td style={tdMonoStyle}>{s.callback_url}</td>
              <td style={tdStyle}>
                {s.event_types.length === 0
                  ? <em style={{ color: "#64748b" }}>wildcard</em>
                  : s.event_types.join(", ")}
              </td>
              <td style={tdStyle}>
                <span style={s.active ? activeBadgeStyle : inactiveBadgeStyle}>
                  {s.active ? "active" : "inactive"}
                </span>
              </td>
              <td style={tdStyle}>{formatDateTime(s.created_at)}</td>
              <td style={tdActionsStyle}>
                <button
                  type="button"
                  style={linkButtonStyle}
                  onClick={() => onSelect(s.subscriber_id)}
                  data-testid={`webhooks-edit-${s.subscriber_id}`}
                >
                  {isSelected ? "Close" : "Edit"}
                </button>
                <button
                  type="button"
                  style={dangerLinkButtonStyle}
                  onClick={() => onDelete(s.subscriber_id)}
                  data-testid={`webhooks-delete-${s.subscriber_id}`}
                >
                  Delete
                </button>
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

// ---------------------------------------------------------------------------
// Create form
// ---------------------------------------------------------------------------

interface CreateFormErrors {
  callbackUrl?: string;
  siteUrl?: string;
  form?: string;
}

function CreateSubscriberForm({
  onCreated,
}: {
  readonly onCreated: (resp: RegisterSubscriberResponse) => void;
}) {
  const [callbackUrl, setCallbackUrl] = useState("");
  const [siteUrl, setSiteUrl] = useState("");
  const [eventTypes, setEventTypes] = useState<readonly string[]>([]);
  const [errors, setErrors] = useState<CreateFormErrors>({});

  const mutation = useMutation<
    RegisterSubscriberResponse,
    ApiError,
    { callback_url: string; site_url: string; event_types: readonly string[] }
  >({
    mutationFn: registerSubscriber,
    onSuccess: (resp) => {
      setCallbackUrl("");
      setSiteUrl("");
      setEventTypes([]);
      setErrors({});
      onCreated(resp);
    },
    onError: (err) => {
      setErrors(mapCreateServerError(err));
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const local: CreateFormErrors = {};
    const cbErr = validateCallbackUrl(callbackUrl);
    if (cbErr !== null) {
      local.callbackUrl = cbErr;
    }
    const siteErr = validateSiteUrl(siteUrl);
    if (siteErr !== null) {
      local.siteUrl = siteErr;
    }
    setErrors(local);
    if (Object.keys(local).length > 0) {
      return;
    }
    mutation.mutate({
      callback_url: callbackUrl.trim(),
      site_url: siteUrl.trim() === "" ? callbackUrl.trim() : siteUrl.trim(),
      event_types: eventTypes,
    });
  }

  return (
    <form style={formStyle} onSubmit={onSubmit} noValidate>
      <Field label="Callback URL" htmlFor="wh-callback" error={errors.callbackUrl}
        hint="The URL Arena POSTs signed event payloads to.">
        <input
          id="wh-callback"
          type="url"
          value={callbackUrl}
          onChange={(e) => setCallbackUrl(e.target.value)}
          style={inputStyle}
          data-testid="webhooks-create-callback"
          required
        />
      </Field>
      <Field label="Site URL (optional)" htmlFor="wh-site" error={errors.siteUrl}
        hint="Human-readable URL; defaults to the callback URL.">
        <input
          id="wh-site"
          type="url"
          value={siteUrl}
          onChange={(e) => setSiteUrl(e.target.value)}
          style={inputStyle}
          data-testid="webhooks-create-site"
        />
      </Field>
      <Field label="Event types" htmlFor="wh-events" error={undefined}
        hint="Leave empty to receive every event type (wildcard).">
        <EventTypeMultiSelect
          id="wh-events"
          value={eventTypes}
          onChange={setEventTypes}
        />
      </Field>
      {errors.form !== undefined ? (
        <div style={formErrorStyle} role="alert" data-testid="webhooks-create-error">
          {errors.form}
        </div>
      ) : null}
      <div style={formActionsStyle}>
        <button
          type="submit"
          style={primaryButtonStyle}
          disabled={mutation.isPending}
          data-testid="webhooks-create-submit"
        >
          {mutation.isPending ? "Registering…" : "Register subscriber"}
        </button>
      </div>
    </form>
  );
}

// ---------------------------------------------------------------------------
// Edit drawer (event_types + active toggle) + recent deliveries panel
// ---------------------------------------------------------------------------

function SubscriberDrawer({
  subscriber,
  onUpdated,
  onClose,
}: {
  readonly subscriber: WebhookSubscriberSummary;
  readonly onUpdated: () => void;
  readonly onClose: () => void;
}) {
  const [eventTypes, setEventTypes] = useState<readonly string[]>(
    subscriber.event_types,
  );
  const [active, setActive] = useState<boolean>(subscriber.active);
  const [savedAt, setSavedAt] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const dirty =
    !arrayEq(eventTypes, subscriber.event_types) || active !== subscriber.active;

  const mutation = useMutation<
    WebhookSubscriberSummary,
    ApiError,
    { event_types?: readonly string[]; active?: boolean }
  >({
    mutationFn: (body) => patchSubscriber(subscriber.subscriber_id, body),
    onSuccess: () => {
      setSavedAt(new Date().toISOString());
      setError(null);
      onUpdated();
    },
    onError: (err) => {
      setError(`${err.message} (${err.code})`);
    },
  });

  const deliveriesQuery = useQuery({
    queryKey: ["webhooks", "deliveries", subscriber.subscriber_id],
    queryFn: () => fetchRecentDeliveries(subscriber.subscriber_id),
    staleTime: 10_000,
  });

  function onSave() {
    const body: { event_types?: readonly string[]; active?: boolean } = {};
    if (!arrayEq(eventTypes, subscriber.event_types)) {
      body.event_types = eventTypes;
    }
    if (active !== subscriber.active) {
      body.active = active;
    }
    if (Object.keys(body).length === 0) {
      return;
    }
    mutation.mutate(body);
  }

  return (
    <aside style={drawerStyle} data-testid={`webhooks-drawer-${subscriber.subscriber_id}`}>
      <div style={drawerHeaderStyle}>
        <h2 style={drawerTitleStyle}>Edit subscriber</h2>
        <button
          type="button"
          style={linkButtonStyle}
          onClick={onClose}
          data-testid="webhooks-drawer-close"
        >
          Close
        </button>
      </div>
      <div style={drawerMetaStyle}>
        <div><strong>ID:</strong> {subscriber.subscriber_id}</div>
        <div><strong>Callback:</strong> <code>{subscriber.callback_url}</code></div>
        <div><strong>Site:</strong> {subscriber.site_url}</div>
        <div>
          <strong>Created:</strong> {formatDateTime(subscriber.created_at)}
        </div>
      </div>

      <div style={drawerSectionStyle}>
        <h3 style={drawerSectionTitleStyle}>Event-type filter</h3>
        <EventTypeMultiSelect
          id={`wh-edit-events-${subscriber.subscriber_id}`}
          value={eventTypes}
          onChange={setEventTypes}
        />
        <p style={hintStyle}>
          Empty list = wildcard (receives every event_type).
        </p>
      </div>

      <div style={drawerSectionStyle}>
        <h3 style={drawerSectionTitleStyle}>Active</h3>
        <label style={toggleRowStyle}>
          <input
            type="checkbox"
            checked={active}
            onChange={(e) => setActive(e.target.checked)}
            data-testid={`webhooks-edit-active-${subscriber.subscriber_id}`}
          />
          <span>
            {active
              ? "Active — dispatcher delivers matching events"
              : "Inactive — events are not delivered to this subscriber"}
          </span>
        </label>
      </div>

      <div style={drawerSectionStyle}>
        <h3 style={drawerSectionTitleStyle}>Signing secret</h3>
        <p style={hintStyle}>
          Write-only after creation. To rotate the secret, deactivate
          this subscriber and register the callback URL again.
        </p>
      </div>

      {error !== null ? (
        <div style={formErrorStyle} role="alert" data-testid="webhooks-edit-error">
          {error}
        </div>
      ) : null}
      {savedAt !== null && !dirty ? (
        <div style={savedBadgeStyle} role="status" data-testid="webhooks-edit-saved">
          Saved at {formatDateTime(savedAt)}
        </div>
      ) : null}

      <div style={formActionsStyle}>
        <button
          type="button"
          style={dirty ? primaryButtonStyle : disabledButtonStyle}
          onClick={onSave}
          disabled={!dirty || mutation.isPending}
          data-testid={`webhooks-edit-save-${subscriber.subscriber_id}`}
        >
          {mutation.isPending ? "Saving…" : "Save changes"}
        </button>
      </div>

      <div style={drawerSectionStyle}>
        <h3 style={drawerSectionTitleStyle}>Recent deliveries</h3>
        <p style={hintStyle}>
          Best-effort: per-subscriber delivery rows are not persisted.
          Attempts / dispatched_at reflect the source outbox event row
          (see dispatcher logs for per-subscriber retry detail).
        </p>
        {deliveriesQuery.isLoading ? (
          <div style={statusBoxStyle}>Loading…</div>
        ) : null}
        {deliveriesQuery.isError ? (
          <div style={errorBoxStyle} role="alert">
            {(deliveriesQuery.error as ApiError).message}
          </div>
        ) : null}
        {deliveriesQuery.data !== undefined ? (
          <DeliveriesTable attempts={deliveriesQuery.data.attempts} />
        ) : null}
      </div>
    </aside>
  );
}

function DeliveriesTable({
  attempts,
}: {
  readonly attempts: readonly RecentDeliveryAttempt[];
}) {
  if (attempts.length === 0) {
    return (
      <div style={statusBoxStyle} data-testid="webhooks-deliveries-empty">
        No matching outbox rows yet.
      </div>
    );
  }
  return (
    <table style={smallTableStyle} data-testid="webhooks-deliveries-table">
      <thead>
        <tr>
          <th style={thStyle}>Event type</th>
          <th style={thStyle}>Occurred</th>
          <th style={thStyle}>Dispatched</th>
          <th style={thStyle}>Attempts</th>
        </tr>
      </thead>
      <tbody>
        {attempts.map((a) => (
          <tr key={a.event_id} style={trStyle}>
            <td style={tdMonoStyle}>{a.event_type}</td>
            <td style={tdStyle}>{formatDateTime(a.occurred_at)}</td>
            <td style={tdStyle}>
              {a.dispatched_at === undefined
                ? <em style={{ color: "#b91c1c" }}>pending</em>
                : formatDateTime(a.dispatched_at)}
            </td>
            <td style={tdStyle}>{a.attempts}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// ---------------------------------------------------------------------------
// Secret banner — shown ONCE after a successful create
// ---------------------------------------------------------------------------

function SecretBanner({
  response,
  onDismiss,
}: {
  readonly response: RegisterSubscriberResponse;
  readonly onDismiss: () => void;
}) {
  return (
    <div style={secretBannerStyle} role="status" data-testid="webhooks-secret-banner">
      <div style={secretBannerTitleStyle}>
        Copy this signing secret now — it is not retrievable later.
      </div>
      <div style={secretBannerMetaStyle}>
        Subscriber <code>{response.subscriber_id}</code>
      </div>
      <code style={secretCodeStyle} data-testid="webhooks-secret-value">
        {response.signing_secret}
      </code>
      <div style={formActionsStyle}>
        <button
          type="button"
          style={linkButtonStyle}
          onClick={onDismiss}
          data-testid="webhooks-secret-dismiss"
        >
          I have copied it — dismiss
        </button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Event type multi-select
// ---------------------------------------------------------------------------

function EventTypeMultiSelect({
  id,
  value,
  onChange,
}: {
  readonly id: string;
  readonly value: readonly string[];
  readonly onChange: (next: readonly string[]) => void;
}) {
  const set = new Set(value);
  function toggle(t: string) {
    const next = new Set(set);
    if (next.has(t)) {
      next.delete(t);
    } else {
      next.add(t);
    }
    onChange(Array.from(next));
  }
  return (
    <div id={id} style={multiSelectStyle} role="group" aria-label="Event types">
      {KNOWN_EVENT_TYPES.map((t) => (
        <label key={t} style={multiSelectChipStyle(set.has(t))}>
          <input
            type="checkbox"
            checked={set.has(t)}
            onChange={() => toggle(t)}
            style={{ marginRight: 6 }}
            data-testid={`webhooks-eventtype-${t}`}
          />
          <span>{t}</span>
        </label>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Field + helpers
// ---------------------------------------------------------------------------

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

/** Map a backend error envelope to a UI-shaped error map. Exported for tests. */
export function mapCreateServerError(err: ApiError): CreateFormErrors {
  switch (err.code) {
    case "bad_request":
      return { callbackUrl: err.message };
    case "validation_error":
      if (/callback_url/i.test(err.message)) {
        return { callbackUrl: err.message };
      }
      return { form: err.message };
    case "conflict":
      return { callbackUrl: "A subscriber with this callback URL already exists." };
    case "permissions.denied":
      return { form: "Your account is missing webhook.subscriber.manage." };
    case "service_unavailable":
      return { form: "Webhook service is not available. Retry shortly." };
    default:
      return { form: `${err.message} (${err.code})` };
  }
}

/** Shallow array equality. Exported for tests. */
export function arrayEq<T>(a: readonly T[], b: readonly T[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  return `${d.toISOString().slice(0, 16).replace("T", " ")}Z`;
}

// ---------------------------------------------------------------------------
// Styles (mirrors the conventions in users.tsx / channels.tsx)
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
};

const subheadingStyle: CSSProperties = {
  margin: "4px 0 0 0",
  fontSize: 13,
  color: "#475569",
  maxWidth: 720,
  lineHeight: 1.45,
};

const statusBoxStyle: CSSProperties = {
  padding: 12,
  background: "#f1f5f9",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  fontSize: 13,
  color: "#334155",
};

const errorBoxStyle: CSSProperties = {
  padding: 12,
  background: "#fef2f2",
  border: "1px solid #fca5a5",
  color: "#7f1d1d",
  borderRadius: 4,
  fontSize: 13,
};

const tableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 13,
  background: "#ffffff",
  border: "1px solid #e2e8f0",
  borderRadius: 4,
};

const smallTableStyle: CSSProperties = {
  ...tableStyle,
  marginTop: 6,
};

const thStyle: CSSProperties = {
  textAlign: "left",
  padding: "8px 10px",
  borderBottom: "1px solid #e2e8f0",
  background: "#f8fafc",
  fontWeight: 600,
  color: "#475569",
  fontSize: 12,
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const trStyle: CSSProperties = {
  borderBottom: "1px solid #f1f5f9",
};

const trSelectedStyle: CSSProperties = {
  ...trStyle,
  background: "#eff6ff",
};

const tdStyle: CSSProperties = {
  padding: "8px 10px",
  verticalAlign: "top",
};

const tdMonoStyle: CSSProperties = {
  ...tdStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};

const tdActionsStyle: CSSProperties = {
  ...tdStyle,
  whiteSpace: "nowrap",
  textAlign: "right",
};

const activeBadgeStyle: CSSProperties = {
  display: "inline-block",
  padding: "2px 8px",
  fontSize: 11,
  borderRadius: 999,
  background: "#dcfce7",
  color: "#166534",
};

const inactiveBadgeStyle: CSSProperties = {
  ...activeBadgeStyle,
  background: "#fee2e2",
  color: "#991b1b",
};

const linkButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "4px 8px",
  background: "transparent",
  border: "none",
  color: "#1d4ed8",
  cursor: "pointer",
  textDecoration: "underline",
};

const dangerLinkButtonStyle: CSSProperties = {
  ...linkButtonStyle,
  color: "#b91c1c",
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

const disabledButtonStyle: CSSProperties = {
  ...primaryButtonStyle,
  background: "#94a3b8",
  border: "1px solid #94a3b8",
  cursor: "not-allowed",
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

const drawerStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
  padding: 16,
  border: "1px solid #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
};

const drawerHeaderStyle: CSSProperties = {
  display: "flex",
  justifyContent: "space-between",
  alignItems: "center",
};

const drawerTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 16,
  fontWeight: 600,
};

const drawerMetaStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  fontSize: 12,
  color: "#475569",
};

const drawerSectionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 6,
  padding: "10px 0",
  borderTop: "1px solid #e2e8f0",
};

const drawerSectionTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  fontWeight: 600,
  textTransform: "uppercase",
  color: "#475569",
  letterSpacing: 0.4,
};

const toggleRowStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 8,
  fontSize: 13,
};

const multiSelectStyle: CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  gap: 6,
};

function multiSelectChipStyle(active: boolean): CSSProperties {
  return {
    display: "inline-flex",
    alignItems: "center",
    padding: "4px 10px",
    borderRadius: 999,
    border: active ? "1px solid #0369a1" : "1px solid #cbd5e1",
    background: active ? "#e0f2fe" : "#ffffff",
    color: active ? "#0c4a6e" : "#334155",
    fontSize: 12,
    cursor: "pointer",
  };
}

const secretBannerStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: 14,
  border: "1px solid #f59e0b",
  background: "#fffbeb",
  borderRadius: 6,
};

const secretBannerTitleStyle: CSSProperties = {
  fontWeight: 600,
  color: "#78350f",
};

const secretBannerMetaStyle: CSSProperties = {
  fontSize: 12,
  color: "#78350f",
};

const secretCodeStyle: CSSProperties = {
  padding: 10,
  background: "#ffffff",
  border: "1px solid #fbbf24",
  borderRadius: 4,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
  wordBreak: "break-all",
};

const savedBadgeStyle: CSSProperties = {
  fontSize: 12,
  color: "#166534",
};
