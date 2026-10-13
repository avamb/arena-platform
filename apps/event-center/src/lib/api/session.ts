/**
 * Session tokens of the event center (phase R1).
 *
 * The access token lives in module memory only. The refresh token lives in
 * sessionStorage, so a reload keeps the person signed in and closing the tab
 * signs them out. This is the admin-web model. The target model of the spec
 * (08_architecture/37, section 4) moves both tokens into httpOnly cookies;
 * that needs server work (WEB-AUTH) and replaces this file.
 *
 * No token is ever written to a log, the URL or localStorage.
 */

const REFRESH_KEY = "arena.events.refresh_token";

interface State {
  accessToken: string | null;
  expiresAt: number | null;
  userId: string | null;
}

const state: State = { accessToken: null, expiresAt: null, userId: null };

type Listener = () => void;
const listeners = new Set<Listener>();

function notify(): void {
  for (const l of listeners) l();
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

export function getUserId(): string | null {
  return state.userId;
}

export function getRefreshToken(): string | null {
  try {
    return sessionStorage.getItem(REFRESH_KEY);
  } catch {
    return null;
  }
}

export function hasSession(): boolean {
  return state.accessToken !== null || getRefreshToken() !== null;
}

export interface SessionInput {
  accessToken: string;
  refreshToken?: string;
  expiresAt: string;
  userId: string;
}

export function setSession(input: SessionInput): void {
  state.accessToken = input.accessToken;
  state.expiresAt = Date.parse(input.expiresAt);
  state.userId = input.userId;
  if (typeof input.refreshToken === "string" && input.refreshToken.length > 0) {
    try {
      sessionStorage.setItem(REFRESH_KEY, input.refreshToken);
    } catch {
      // Without storage a reload signs the person out. Nothing else breaks.
    }
  }
  notify();
}

export function clearSession(): void {
  state.accessToken = null;
  state.expiresAt = null;
  state.userId = null;
  try {
    sessionStorage.removeItem(REFRESH_KEY);
  } catch {
    // Same as above.
  }
  notify();
}

/** Test helper. Production code never calls it. */
export function resetSessionForTests(): void {
  clearSession();
  listeners.clear();
}
