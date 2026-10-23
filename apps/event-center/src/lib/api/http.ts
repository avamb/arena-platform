/**
 * Small fetch wrapper for arena-api.
 *
 * - Adds `Authorization: Bearer` and `X-Client-Channel: event_center_web`
 *   (an allow-listed channel since A1: audit rows of the web carry
 *   metadata.via = event_center_web).
 * - Parses the platform error envelope into ApiError.
 * - On a 401 it refreshes the session ONCE and repeats the request.
 * - Aborts a request after 30 s (a file upload gets 120 s, see `UPLOAD_TIMEOUT_MS`).
 * - A call may carry a `form` (multipart/form-data) instead of a JSON `body`:
 *   same transport, same token refresh, same channel header; the browser sets
 *   the multipart Content-Type with its boundary, so it is never set here.
 *
 * The session itself is `session` (tabSession.ts): one tab of a session
 * rotates the refresh token, the others ask it (review finding M3).
 */
import { config } from "@/lib/config";
import { API, buildPath, type ApiRoute } from "@/lib/api/routes";
import { TabSession, browserBus, browserLocks, browserStore, type Deps, type Tokens } from "@/lib/api/tabSession";
import type { AuthLoginResponse, AuthRefreshResponse } from "@/lib/api/types";

const TIMEOUT_MS = 30_000;
/** A poster of a few megabytes over a slow phone connection needs more than 30 s. */
export const UPLOAD_TIMEOUT_MS = 120_000;
/**
 * Reading a poster with the vision model: the server allows the model 45 s and
 * 10 s more to write the answer, so the browser waits a little longer than that
 * (and never repeats the paid request by itself).
 */
export const POSTER_READ_TIMEOUT_MS = 60_000;
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
  /** A multipart body. Never together with `body`. */
  form?: FormData;
  /** The caller's own cancel (a screen that left); a cancel is reported as `network.aborted`. */
  signal?: AbortSignal;
  /** Overrides the default 30 s. */
  timeoutMs?: number;
  authenticated: boolean;
  accept?: string;
  /** Extra headers, for example X-Step-Up-Token on a money request. */
  headers?: Record<string, string>;
}

async function send(opts: SendOptions): Promise<Response> {
  const headers: Record<string, string> = {
    Accept: opts.accept ?? "application/json",
    ...opts.headers,
    "X-Client-Channel": CLIENT_CHANNEL,
  };
  if (opts.body !== undefined && opts.form !== undefined) throw new Error("send: a call has a body or a form, not both");
  // A form is NOT given a Content-Type: fetch adds multipart/form-data with the boundary.
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (opts.authenticated) {
    const token = session.getAccessToken();
    if (token !== null) headers.Authorization = `Bearer ${token}`;
  }
  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, opts.timeoutMs ?? TIMEOUT_MS);
  const onCallerAbort = () => controller.abort();
  if (opts.signal?.aborted === true) controller.abort();
  else opts.signal?.addEventListener("abort", onCallerAbort, { once: true });
  try {
    return await fetch(`${config.apiBaseUrl}${buildPath(opts.route, opts.params, opts.query)}`, {
      method: opts.route.method,
      headers,
      body: opts.form !== undefined ? opts.form : opts.body === undefined ? undefined : JSON.stringify(opts.body),
      signal: controller.signal,
      credentials: "omit",
    });
  } catch (cause) {
    if (timedOut) throw new ApiError(0, "network.timeout", "The request took too long.");
    if (opts.signal?.aborted === true) throw new ApiError(0, "network.aborted", "The request was cancelled.");
    throw new ApiError(
      0,
      "network.failure",
      cause instanceof Error ? cause.message : "The network request failed.",
    );
  } finally {
    clearTimeout(timer);
    opts.signal?.removeEventListener("abort", onCallerAbort);
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

/** POST /v1/auth/refresh. The server issues a NEW refresh token every time. */
async function rotateRefreshToken(refreshToken: string): Promise<Tokens> {
  const res = await readJson<AuthRefreshResponse>(
    await send({ route: API.refresh, body: { refresh_token: refreshToken }, authenticated: false }),
  );
  return { accessToken: res.access_token, refreshToken: res.refresh_token, expiresAt: res.expires_at, userId: res.user_id };
}

/** Builds the session of this tab. Tests pass their own store, bus and locks. */
export function createSession(over: Partial<Deps> = {}): TabSession {
  return new TabSession({
    store: over.store ?? browserStore(),
    bus: over.bus !== undefined ? over.bus : browserBus(),
    locks: over.locks !== undefined ? over.locks : browserLocks(),
    refresh: over.refresh ?? rotateRefreshToken,
    makeError: over.makeError ?? ((status, code, message) => new ApiError(status, code, message)),
    now: over.now,
    newId: over.newId,
    askTimeoutMs: over.askTimeoutMs,
  });
}

export let session: TabSession = createSession();

/** Test helper: replaces the session of the module. */
export function useSessionForTests(next: TabSession): void {
  session = next;
}

/**
 * Gets a fresh access token. Only a refusal by the server (400/401) signs
 * the person out. A network error keeps the session (review finding L7).
 */
export function refreshSession(): Promise<void> {
  return session.refresh();
}

export async function login(email: string, password: string): Promise<void> {
  const res = await readJson<AuthLoginResponse>(
    await send({ route: API.login, body: { email, password }, authenticated: false }),
  );
  await session.signedIn({
    accessToken: res.access_token,
    refreshToken: res.refresh_token,
    expiresAt: res.expires_at,
    userId: res.user_id,
  });
}

/**
 * Signs in with the one-time code of a bot link (spec 37 section 4.3). One
 * exchange per code: React may run the page's effect twice, and a second
 * POST of the same code would be refused as already used.
 */
let linkExchange: { code: string; done: Promise<void> } | null = null;

export function loginWithLink(code: string): Promise<void> {
  if (linkExchange?.code === code) return linkExchange.done;
  const done = (async () => {
    const res = await readJson<AuthLoginResponse>(
      await send({ route: API.webLink, body: { code }, authenticated: false }),
    );
    await session.signedIn({
      accessToken: res.access_token,
      refreshToken: res.refresh_token,
      expiresAt: res.expires_at,
      userId: res.user_id,
    });
  })();
  linkExchange = { code, done };
  return done;
}

/**
 * Signs out and revokes the refresh token on the server (review finding
 * M1). POST /v1/auth/logout needs a VALID access token, so an expired one
 * is refreshed first, and the token sent is the latest one: a rotated-away
 * token is already revoked and logging it out would leave the session alive.
 * Every tab of the session is signed out, whatever the server answers.
 */
export async function logout(): Promise<void> {
  try {
    await session.ensureFresh();
    let res = await sendLogout();
    if (res?.status === 401) {
      await session.refresh();
      res = await sendLogout();
    }
  } catch {
    // The person asked to leave: the local session is cleared anyway.
  } finally {
    session.signOut(true);
  }
}

function sendLogout(): Promise<Response> | null {
  const token = session.getRefreshToken();
  if (token === null) return null;
  return send({ route: API.logout, body: { refresh_token: token }, authenticated: true });
}

export async function requestPasswordReset(email: string): Promise<void> {
  await readJson<unknown>(
    await send({ route: API.passwordResetRequest, body: { email }, authenticated: false }),
  );
}

/**
 * Sets the password from a link of a password letter (reset, account setup
 * or invitation: all three redeem the same one-time token).
 */
export async function confirmPasswordReset(token: string, newPassword: string): Promise<void> {
  await readJson<unknown>(
    await send({
      route: API.passwordResetConfirm,
      body: { token, new_password: newPassword },
      authenticated: false,
    }),
  );
}

type CallOptions = Omit<SendOptions, "authenticated">;

async function sendAuthed(opts: CallOptions): Promise<Response> {
  if (session.getAccessToken() === null && session.hasSession()) await session.refresh();
  let res = await send({ ...opts, authenticated: true });
  if (res.status === 401 && session.hasSession()) {
    await session.refresh();
    res = await send({ ...opts, authenticated: true });
  }
  return res;
}

/** Authenticated call: JSON by default, multipart when the options carry a `form`. */
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
