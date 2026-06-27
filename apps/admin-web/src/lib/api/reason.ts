/**
 * Cross-tenant audit-reason context (SAUI-04).
 *
 * The backend requires an `X-Admin-Reason` header on all superadmin
 * cross-tenant reads:
 *
 *     GET /v1/admin/organizations
 *     GET /v1/admin/orders
 *     GET /v1/admin/tickets
 *     GET /v1/admin/refunds
 *     POST /v1/admin/impersonate
 *
 * Missing or empty headers come back as 400 with
 * { error: { code: "superadmin.missing_reason" } }.
 *
 * The admin UI prompts the operator the first time a cross-tenant request
 * is about to fire in a session, persists the chosen reason in
 * sessionStorage so it survives an in-tab reload, and re-prompts when the
 * backend rejects the request. This module owns:
 *
 *   - the path predicate (`requiresAdminReason`)
 *   - the sessionStorage key + helpers (read/write/clear)
 *   - a pub-sub for the in-shell reason badge
 *   - a "reason resolver" registration slot used by the API client so the
 *     React layer can plug a modal-driven prompt into raw fetch without
 *     leaking React types into the API client.
 *
 * Design notes:
 *
 *   - We deliberately do NOT default to a generic reason like
 *     "Operator browsing" -- the spec calls that out as a regression
 *     vector ("never silently send generic reason").
 *   - We deliberately do NOT cache the reason in localStorage -- closing
 *     the tab MUST drop the reason so the next session re-prompts.
 *   - The resolver is async because the prompt is interactive; the API
 *     client awaits it before adding the header.
 */

/** sessionStorage key holding the active reason for this tab. */
const REASON_STORAGE_KEY = "arena.admin.adminReason";

/** Set of API path prefixes that require X-Admin-Reason. */
const REASON_REQUIRED_PREFIXES: readonly string[] = [
  "/v1/admin/organizations",
  "/v1/admin/orders",
  "/v1/admin/tickets",
  "/v1/admin/refunds",
  "/v1/admin/impersonate",
];

/**
 * True when the given path's request must carry X-Admin-Reason.
 *
 * Uses prefix matching so `/v1/admin/orders/{id}` and
 * `/v1/admin/orders?state=completed` both qualify, while `/v1/admin/geo`
 * (catalog admin, no cross-tenant data) does NOT.
 */
export function requiresAdminReason(path: string): boolean {
  // Strip query string before matching so prefixes stay unambiguous.
  const qIndex = path.indexOf("?");
  const bare = qIndex === -1 ? path : path.slice(0, qIndex);
  for (const prefix of REASON_REQUIRED_PREFIXES) {
    if (bare === prefix || bare.startsWith(`${prefix}/`)) {
      return true;
    }
  }
  return false;
}

/**
 * Backend error code returned when the X-Admin-Reason header is missing
 * or empty. Exposed for the client's retry logic and the modal copy.
 */
export const MISSING_REASON_CODE = "superadmin.missing_reason";

// ---------------------------------------------------------------------------
// Active-reason store
// ---------------------------------------------------------------------------

let cachedReason: string | null = null;
let initialised = false;

type ReasonListener = (reason: string | null) => void;
const listeners = new Set<ReasonListener>();

function readPersisted(): string | null {
  try {
    const raw = sessionStorage.getItem(REASON_STORAGE_KEY);
    if (raw === null) {
      return null;
    }
    const trimmed = raw.trim();
    return trimmed === "" ? null : trimmed;
  } catch {
    return null;
  }
}

function writePersisted(reason: string | null): void {
  try {
    if (reason === null) {
      sessionStorage.removeItem(REASON_STORAGE_KEY);
    } else {
      sessionStorage.setItem(REASON_STORAGE_KEY, reason);
    }
  } catch {
    // sessionStorage unavailable (private mode / SSR shim); the reason
    // still lives in module memory for the current page lifetime.
  }
}

function ensureInit(): void {
  if (initialised) {
    return;
  }
  initialised = true;
  cachedReason = readPersisted();
}

function notify(): void {
  for (const fn of listeners) {
    fn(cachedReason);
  }
}

/** Current reason; null when no reason has been captured this session. */
export function getActiveReason(): string | null {
  ensureInit();
  return cachedReason;
}

/**
 * Persist a new active reason. Empty / whitespace input is rejected by
 * collapsing to null (and clearing storage) so the API client cannot
 * silently send an empty header.
 */
export function setActiveReason(next: string | null): void {
  ensureInit();
  const normalised =
    next === null ? null : next.trim() === "" ? null : next.trim();
  if (normalised === cachedReason) {
    return;
  }
  cachedReason = normalised;
  writePersisted(normalised);
  notify();
}

/** Wipe the stored reason. Convenience over setActiveReason(null). */
export function clearActiveReason(): void {
  setActiveReason(null);
}

/** Subscribe to reason changes. Returns the unsubscribe function. */
export function subscribeReason(fn: ReasonListener): () => void {
  ensureInit();
  listeners.add(fn);
  // Fire once on subscribe so consumers can hydrate without an extra read.
  fn(cachedReason);
  return () => {
    listeners.delete(fn);
  };
}

// ---------------------------------------------------------------------------
// Resolver registration (consumed by lib/api/client.ts)
// ---------------------------------------------------------------------------

/**
 * Function the API client calls when it needs a reason for a cross-tenant
 * request. Implementations:
 *
 *   - return the active reason if one is set;
 *   - otherwise open the prompt modal and resolve when the operator submits.
 *
 * The returned reason MUST be non-empty after trimming. Implementations
 * reject the promise if the operator cancels the prompt so the API call
 * fails fast instead of going out with no header.
 */
export type ReasonResolver = (path: string) => Promise<string>;

let resolver: ReasonResolver | null = null;

/**
 * Wire the React layer's interactive resolver into the API client. The
 * <ReasonProvider /> calls this on mount and clears it on unmount.
 */
export function setReasonResolver(fn: ReasonResolver | null): void {
  resolver = fn;
}

/**
 * Resolve a reason for the given path, prompting the operator if needed.
 * If no resolver has been registered (e.g. tests calling the API client
 * directly), falls back to the persisted reason or rejects if none.
 */
export async function resolveReasonFor(path: string): Promise<string> {
  if (resolver !== null) {
    const reason = await resolver(path);
    const trimmed = reason.trim();
    if (trimmed === "") {
      throw new Error("reason resolver returned an empty reason");
    }
    return trimmed;
  }
  // Resolver-less path: still honour an already-persisted reason. This
  // keeps the API client usable from unit tests that pre-seed a reason.
  const persisted = getActiveReason();
  if (persisted !== null) {
    return persisted;
  }
  throw new Error(
    "no admin reason resolver registered and no reason cached; mount <ReasonProvider /> or pre-seed via setActiveReason()",
  );
}

// ---------------------------------------------------------------------------
// Test-only escape hatch
// ---------------------------------------------------------------------------

/**
 * Resets module state. Tests call this in beforeEach so reason state
 * never leaks between cases.
 */
export function __TEST_ONLY_resetReason(): void {
  cachedReason = null;
  initialised = false;
  resolver = null;
  listeners.clear();
  try {
    sessionStorage.removeItem(REASON_STORAGE_KEY);
  } catch {
    // ignore in environments without sessionStorage
  }
}
