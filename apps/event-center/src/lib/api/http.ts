/**
 * Small fetch wrapper for arena-api.
 *
 * - Adds `Authorization: Bearer` and `X-Client-Channel: event_center_web`.
 *   The API ignores an unknown channel value today (allow-list
 *   telegram_bot, admin_web, site_plugin); the spec asks to add this one.
 * - Parses the platform error envelope into ApiError.
 * - On a 401 it refreshes the session ONCE and repeats the request.
 * - Aborts a request after 30 s.
 */
import { config } from "@/lib/config";
import { clearSession, getAccessToken, getRefreshToken, setSession } from "@/lib/api/session";
import { API, buildPath, type ApiRoute } from "@/lib/api/routes";
import type { AuthLoginResponse, AuthRefreshResponse } from "@/lib/api/types";

const TIMEOUT_MS = 30_000;
export const CLIENT_CHANNEL = "event_center_web";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string | undefined;
  readonly details: Record<string, unknown> | undefined;

  constructor(
    status: number,
    code: string,
    message: string,
    requestId?: string,
    details?: Record<string, unknown>,
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.details = details;
  }
}

function toApiError(status: number, raw: unknown): ApiError {
  if (raw !== null && typeof raw === "object" && "error" in raw) {
    const err = (raw as { error: unknown }).error;
    if (err !== null && typeof err === "object") {
      const e = err as {
        code?: unknown;
        message?: unknown;
        request_id?: unknown;
        details?: unknown;
      };
      return new ApiError(
        status,
        typeof e.code === "string" ? e.code : `http.${status}`,
        typeof e.message === "string" ? e.message : `HTTP ${status}`,
        typeof e.request_id === "string" ? e.request_id : undefined,
        e.details !== null && typeof e.details === "object"
          ? (e.details as Record<string, unknown>)
          : undefined,
      );
    }
  }
  return new ApiError(status, `http.${status}`, `HTTP ${status}`);
}

interface SendOptions {
  route: ApiRoute;
  params?: Record<string, string>;
  query?: Record<string, string | number | undefined>;
  body?: unknown;
  authenticated: boolean;
  accept?: string;
}

async function send(opts: SendOptions): Promise<Response> {
  const headers: Record<string, string> = {
    Accept: opts.accept ?? "application/json",
    "X-Client-Channel": CLIENT_CHANNEL,
  };
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (opts.authenticated) {
    const token = getAccessToken();
    if (token !== null) headers.Authorization = `Bearer ${token}`;
  }
  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, TIMEOUT_MS);
  try {
    return await fetch(`${config.apiBaseUrl}${buildPath(opts.route, opts.params, opts.query)}`, {
      method: opts.route.method,
      headers,
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
      signal: controller.signal,
      credentials: "omit",
    });
  } catch (cause) {
    if (timedOut) throw new ApiError(0, "network.timeout", "The request took too long.");
    throw new ApiError(
      0,
      "network.failure",
      cause instanceof Error ? cause.message : "The network request failed.",
    );
  } finally {
    clearTimeout(timer);
  }
}

async function readJson<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let parsed: unknown = null;
  if (text.length > 0) {
    try {
      parsed = JSON.parse(text);
    } catch {
      throw new ApiError(res.status, "http.invalid_json", "The server answered with something that is not JSON.");
    }
  }
  if (!res.ok) throw toApiError(res.status, parsed);
  return parsed as T;
}

let refreshing: Promise<void> | null = null;

/** Rotates the session. The server issues a NEW refresh token every time. */
export function refreshSession(): Promise<void> {
  if (refreshing !== null) return refreshing;
  const token = getRefreshToken();
  if (token === null) {
    return Promise.reject(new ApiError(401, "auth.no_refresh_token", "You are signed out."));
  }
  refreshing = send({ route: API.refresh, body: { refresh_token: token }, authenticated: false })
    .then((res) => readJson<AuthRefreshResponse>(res))
    .then((res) => {
      setSession({
        accessToken: res.access_token,
        refreshToken: res.refresh_token,
        expiresAt: res.expires_at,
        userId: res.user_id,
      });
    })
    .catch((err: unknown) => {
      clearSession();
      throw err;
    })
    .finally(() => {
      refreshing = null;
    });
  return refreshing;
}

export async function login(email: string, password: string): Promise<void> {
  const res = await readJson<AuthLoginResponse>(
    await send({ route: API.login, body: { email, password }, authenticated: false }),
  );
  setSession({
    accessToken: res.access_token,
    refreshToken: res.refresh_token,
    expiresAt: res.expires_at,
    userId: res.user_id,
  });
}

export async function logout(): Promise<void> {
  const token = getRefreshToken();
  try {
    if (token !== null) {
      await send({ route: API.logout, body: { refresh_token: token }, authenticated: true });
    }
  } catch {
    // The local session is cleared anyway.
  } finally {
    clearSession();
  }
}

export async function requestPasswordReset(email: string): Promise<void> {
  await readJson<unknown>(
    await send({ route: API.passwordResetRequest, body: { email }, authenticated: false }),
  );
}

type CallOptions = Omit<SendOptions, "authenticated">;

async function sendAuthed(opts: CallOptions): Promise<Response> {
  if (getAccessToken() === null && getRefreshToken() !== null) await refreshSession();
  let res = await send({ ...opts, authenticated: true });
  if (res.status === 401 && getRefreshToken() !== null) {
    await refreshSession();
    res = await send({ ...opts, authenticated: true });
  }
  return res;
}

/** Authenticated JSON call. */
export async function call<T>(opts: CallOptions): Promise<T> {
  return readJson<T>(await sendAuthed(opts));
}

export interface DownloadedFile {
  blob: Blob;
  filename: string;
}

/** Authenticated file download (CSV, PDF). */
export async function download(opts: CallOptions & { fallbackName: string }): Promise<DownloadedFile> {
  const res = await sendAuthed(opts);
  if (!res.ok) {
    const text = await res.text();
    let parsed: unknown = null;
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = null;
    }
    throw toApiError(res.status, parsed);
  }
  return { blob: await res.blob(), filename: filenameFrom(res.headers.get("Content-Disposition"), opts.fallbackName) };
}

/** Reads the file name from Content-Disposition, or uses the fallback. */
export function filenameFrom(header: string | null, fallback: string): string {
  if (header === null) return fallback;
  const star = /filename\*=UTF-8''([^;]+)/i.exec(header);
  if (star !== null) {
    try {
      return decodeURIComponent(star[1].trim());
    } catch {
      return fallback;
    }
  }
  const plain = /filename="?([^";]+)"?/i.exec(header);
  return plain !== null ? plain[1].trim() : fallback;
}
