/**
 * Session token storage for the admin shell.
 *
 * Policy choices (SAUI-02):
 *   - Access token lives in module-scoped memory only. It is NEVER written
 *     to localStorage, sessionStorage, IndexedDB, cookies, or the URL. A
 *     hard reload therefore discards the access token and forces a refresh
 *     via the long-lived refresh token below. This matches the platform
 *     security note: short-lived bearer tokens MUST NOT survive a refresh
 *     in attacker-readable storage.
 *   - Refresh token lives in sessionStorage (per-tab, cleared on tab close)
 *     so that a casual page reload does not log the operator out, but
 *     closing the tab does. Storing in localStorage would persist across
 *     tab close, which the platform spec explicitly forbids for operator
 *     workstations.
 *   - The expires_at value is held alongside the access token in memory so
 *     callers can decide to proactively refresh before the access token
 *     hits 0 TTL on the server side.
 *
 * No token field is ever stringified into a log line. The diagnostic panel
 * (DevDiagnosticsPanel) renders only a redacted prefix.
 */

const REFRESH_STORAGE_KEY = "arena.admin.refresh_token";

interface SessionState {
  accessToken: string | null;
  expiresAt: number | null; // Unix ms
  userId: string | null;
}

const state: SessionState = {
  accessToken: null,
  expiresAt: null,
  userId: null,
};

type Listener = () => void;
const listeners = new Set<Listener>();

function notify(): void {
  for (const l of listeners) {
    l();
  }
}

export function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function getAccessToken(): string | null {
  return state.accessToken;
}

export function getAccessExpiresAt(): number | null {
  return state.expiresAt;
}

export function getUserId(): string | null {
  return state.userId;
}

export function getRefreshToken(): string | null {
  try {
    return sessionStorage.getItem(REFRESH_STORAGE_KEY);
  } catch {
    // sessionStorage can throw in privacy modes / file:// — degrade gracefully.
    return null;
  }
}

export function hasSession(): boolean {
  return state.accessToken !== null || getRefreshToken() !== null;
}

export interface SetSessionInput {
  accessToken: string;
  refreshToken?: string; // refresh endpoint only returns a new access token
  expiresAt: string; // RFC3339
  userId: string;
}

export function setSession(input: SetSessionInput): void {
  state.accessToken = input.accessToken;
  state.expiresAt = Date.parse(input.expiresAt);
  state.userId = input.userId;
  if (typeof input.refreshToken === "string" && input.refreshToken.length > 0) {
    try {
      sessionStorage.setItem(REFRESH_STORAGE_KEY, input.refreshToken);
    } catch {
      // Swallow: refresh-on-reload will simply not work without storage.
    }
  }
  notify();
}

export function clearSession(): void {
  state.accessToken = null;
  state.expiresAt = null;
  state.userId = null;
  try {
    sessionStorage.removeItem(REFRESH_STORAGE_KEY);
  } catch {
    // Same fallthrough as above.
  }
  notify();
}

/**
 * Test-only helper: forcibly seed module state. NOT exported from
 * any production entry point; vitest imports it directly via the file
 * path.
 */
export function __TEST_ONLY_resetTokenStore(): void {
  state.accessToken = null;
  state.expiresAt = null;
  state.userId = null;
  try {
    sessionStorage.removeItem(REFRESH_STORAGE_KEY);
  } catch {
    /* noop */
  }
  listeners.clear();
}
