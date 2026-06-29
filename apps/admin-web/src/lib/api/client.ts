/**
 * Typed admin API client.
 *
 * Wraps fetch with: bearer-token injection, JSON envelope handling,
 * the platform's ErrorEnvelope contract, and silent access-token refresh
 * on a single 401 retry. The client is deliberately small -- only the
 * subset the admin shell currently needs (auth + /v1/me). Additional
 * endpoints get thin helpers in sibling files as they are wired up.
 *
 * Error contract:
 *   - Network or non-JSON failures -> ApiError(code='network.failure').
 *   - Hung requests are aborted after a fixed timeout and reported as
 *     ApiError(code='network.timeout').
 *   - HTTP >= 400 with ErrorEnvelope body -> ApiError carrying the parsed
 *     envelope code/message/request_id/trace_id.
 *   - 401 triggers exactly ONE refresh attempt; on refresh failure the
 *     session is cleared and the original 401 propagates so the caller
 *     (AuthProvider) can redirect to /login.
 *   - Refresh + /v1/auth/login + /v1/auth/logout are explicitly never
 *     retried (would loop).
 */
import { config } from "@/lib/config";
import {
  clearSession,
  getAccessToken,
  getRefreshToken,
  setSession,
} from "@/lib/api/tokenStore";
import {
  MISSING_REASON_CODE,
  clearActiveReason,
  requiresAdminReason,
  resolveReasonFor,
} from "@/lib/api/reason";
import type {
  AdminCreateUserRequest,
  AdminCreateUserResponse,
  AuthLoginRequest,
  AuthLoginResponse,
  AuthLogoutRequest,
  AuthRefreshRequest,
  AuthRefreshResponse,
  ErrorEnvelope,
  MeResponse,
} from "@/lib/api/types";

const REQUEST_TIMEOUT_MS = 30_000;

export interface ApiErrorBody {
  code: string;
  message: string;
  requestId?: string;
  traceId?: string;
  details?: Record<string, unknown>;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string | undefined;
  readonly traceId: string | undefined;
  readonly details: Record<string, unknown> | undefined;

  constructor(status: number, body: ApiErrorBody) {
    super(body.message);
    this.name = "ApiError";
    this.status = status;
    this.code = body.code;
    this.requestId = body.requestId;
    this.traceId = body.traceId;
    this.details = body.details;
  }
}

function parseErrorEnvelope(status: number, raw: unknown): ApiError {
  if (
    raw !== null &&
    typeof raw === "object" &&
    "error" in raw &&
    typeof (raw as { error: unknown }).error === "object"
  ) {
    const env = raw as ErrorEnvelope;
    return new ApiError(status, {
      code: env.error.code,
      message: env.error.message,
      requestId: env.error.request_id,
      traceId: env.error.trace_id,
      details: env.error.details,
    });
  }
  return new ApiError(status, {
    code: `http.${status}`,
    message: `HTTP ${status}`,
  });
}

interface RawFetchOptions {
  method: "GET" | "POST" | "PATCH" | "DELETE";
  path: string;
  body?: unknown;
  authenticated: boolean;
  /** When true, a 401 does NOT trigger a refresh attempt. */
  noRefresh?: boolean;
  /**
   * Explicit reason override. When set, this value is sent as
   * X-Admin-Reason regardless of path. Used by the missing-reason retry
   * path to inject the freshly-prompted reason without consulting the
   * resolver again.
   */
  adminReason?: string;
}

async function rawFetch<T>(opts: RawFetchOptions): Promise<T> {
  const headers: Record<string, string> = {
    Accept: "application/json",
  };
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  if (opts.authenticated) {
    const token = getAccessToken();
    if (token !== null) {
      headers.Authorization = `Bearer ${token}`;
    }
  }
  if (opts.adminReason !== undefined) {
    headers["X-Admin-Reason"] = opts.adminReason;
  } else if (
    opts.authenticated &&
    requiresAdminReason(opts.path, opts.method)
  ) {
    // Cross-tenant superadmin reads MUST carry a non-empty reason; the
    // resolver is async because it may prompt the operator. If the
    // resolver rejects (operator cancelled, or no resolver registered),
    // we surface a synthetic ApiError instead of letting the request fly
    // out without a header (which would 400 with superadmin.missing_reason
    // anyway, but with a less helpful UX).
    let reason: string;
    try {
      reason = await resolveReasonFor(opts.path);
    } catch (cause) {
      throw new ApiError(0, {
        code: "superadmin.reason_required",
        message:
          cause instanceof Error
            ? cause.message
            : "An audit reason is required for cross-tenant requests.",
      });
    }
    headers["X-Admin-Reason"] = reason;
  }

  let response: Response;
  const controller = new AbortController();
  let timedOut = false;
  const timeout = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, REQUEST_TIMEOUT_MS);
  try {
    response = await fetch(`${config.apiBaseUrl}${opts.path}`, {
      method: opts.method,
      headers,
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
      signal: controller.signal,
      // Tokens are passed in the Authorization header, never via cookies,
      // so we deliberately omit credentials to avoid CORS preflight noise.
      credentials: "omit",
    });
  } catch (cause) {
    if (timedOut || (cause instanceof Error && cause.name === "AbortError")) {
      throw new ApiError(0, {
        code: "network.timeout",
        message: `Request timed out after ${REQUEST_TIMEOUT_MS / 1000}s`,
      });
    }
    throw new ApiError(0, {
      code: "network.failure",
      message: cause instanceof Error ? cause.message : "Network request failed",
    });
  } finally {
    clearTimeout(timeout);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  let parsed: unknown = null;
  const text = await response.text();
  if (text.length > 0) {
    try {
      parsed = JSON.parse(text);
    } catch {
      throw new ApiError(response.status, {
        code: "http.invalid_json",
        message: "Server returned a non-JSON response",
      });
    }
  }

  if (!response.ok) {
    throw parseErrorEnvelope(response.status, parsed);
  }
  return parsed as T;
}

// -- Auth wrappers ----------------------------------------------------------

export async function login(req: AuthLoginRequest): Promise<AuthLoginResponse> {
  const res = await rawFetch<AuthLoginResponse>({
    method: "POST",
    path: "/v1/auth/login",
    body: req,
    authenticated: false,
    noRefresh: true,
  });
  setSession({
    accessToken: res.access_token,
    refreshToken: res.refresh_token,
    expiresAt: res.expires_at,
    userId: res.user_id,
  });
  return res;
}

let inFlightRefresh: Promise<AuthRefreshResponse> | null = null;

export async function refresh(): Promise<AuthRefreshResponse> {
  if (inFlightRefresh !== null) {
    return inFlightRefresh;
  }
  const refreshToken = getRefreshToken();
  if (refreshToken === null) {
    throw new ApiError(401, {
      code: "auth.no_refresh_token",
      message: "No refresh token available",
    });
  }
  const body: AuthRefreshRequest = { refresh_token: refreshToken };
  inFlightRefresh = rawFetch<AuthRefreshResponse>({
    method: "POST",
    path: "/v1/auth/refresh",
    body,
    authenticated: false,
    noRefresh: true,
  })
    .then((res) => {
      setSession({
        accessToken: res.access_token,
        expiresAt: res.expires_at,
        userId: res.user_id,
      });
      return res;
    })
    .finally(() => {
      inFlightRefresh = null;
    });
  return inFlightRefresh;
}

export async function logout(): Promise<void> {
  const refreshToken = getRefreshToken();
  try {
    if (refreshToken !== null) {
      const body: AuthLogoutRequest = { refresh_token: refreshToken };
      await rawFetch<void>({
        method: "POST",
        path: "/v1/auth/logout",
        body,
        authenticated: true,
        noRefresh: true,
      });
    }
  } catch {
    // Server-side logout failures are non-fatal; the local session is
    // always cleared so the operator is never stuck in a half-state.
  } finally {
    clearSession();
  }
}

export async function fetchMe(): Promise<MeResponse> {
  return authedFetch<MeResponse>({ method: "GET", path: "/v1/me" });
}

export async function createAdminUser(
  req: AdminCreateUserRequest,
): Promise<AdminCreateUserResponse> {
  return authedFetch<AdminCreateUserResponse>({
    method: "POST",
    path: "/v1/admin/users",
    body: req,
  });
}

interface AuthedRequest {
  method: "GET" | "POST" | "PATCH" | "DELETE";
  path: string;
  body?: unknown;
}

/**
 * Authenticated fetch with two silent retry policies:
 *
 *   1. 401 -> single refresh-and-retry. If refresh fails the session is
 *      cleared and the original 401 propagates so AuthProvider can
 *      redirect to /login.
 *   2. 400 with code `superadmin.missing_reason` -> clear the cached
 *      reason (so the resolver re-prompts), resolve a fresh reason for
 *      this path, retry exactly once with the new reason injected. This
 *      covers the case where the operator's persisted reason was
 *      invalidated server-side mid-session.
 *
 * Each retry policy fires at most once; a second failure of the same
 * kind propagates to the caller. The two policies are independent --
 * the missing_reason retry path does NOT itself trigger a 401 refresh,
 * because a 401 after a fresh reason is a real auth failure.
 */
export async function authedFetch<T>(req: AuthedRequest): Promise<T> {
  try {
    return await rawFetch<T>({ ...req, authenticated: true });
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      try {
        await refresh();
      } catch {
        clearSession();
        throw err;
      }
      return rawFetch<T>({ ...req, authenticated: true, noRefresh: true });
    }
    if (
      err instanceof ApiError &&
      err.code === MISSING_REASON_CODE &&
      requiresAdminReason(req.path, req.method)
    ) {
      // Server rejected our (possibly stale) reason. Drop the cached
      // reason, prompt the operator again, retry once with the new value.
      clearActiveReason();
      let fresh: string;
      try {
        fresh = await resolveReasonFor(req.path);
      } catch (cause) {
        throw new ApiError(err.status, {
          code: "superadmin.reason_required",
          message:
            cause instanceof Error
              ? cause.message
              : "An audit reason is required for cross-tenant requests.",
        });
      }
      return rawFetch<T>({
        ...req,
        authenticated: true,
        adminReason: fresh,
      });
    }
    throw err;
  }
}
