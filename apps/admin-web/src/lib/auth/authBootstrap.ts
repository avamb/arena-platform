/**
 * authBootstrap.ts — pure async function for the session-restore path.
 *
 * Extracted from AuthProvider's useEffect so the logic can be unit-tested
 * without mounting a React component (AB-29).
 *
 * The function is intentionally NOT exported from the auth barrel; it is an
 * implementation detail of AuthProvider. Direct consumers should use
 * useAuth() or the AuthContext instead.
 */

export interface BootstrapCallbacks {
  /** Returns the stored refresh token or null when no session exists. */
  getRefreshToken: () => string | null;
  /**
   * Exchanges the stored refresh token for a new access token. Throws on failure.
   * The return value (if any) is intentionally ignored by runBootstrap.
   */
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  refresh: () => Promise<any>;
  /**
   * Fetches /v1/me and settles auth status to authenticated / me_failed / unauthenticated.
   * The return value (if any) is intentionally ignored by runBootstrap.
   */
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  loadMe: () => Promise<any>;
  /** Called when the bootstrap determines the user is not authenticated. */
  setUnauthenticated: () => void;
  /** Returns true if the calling useEffect has been cleaned up (cancelled). */
  isCancelled: () => boolean;
}

/**
 * runBootstrap drives the one-shot session-restore sequence.
 *
 * State transitions guaranteed:
 *  - No refresh token        → setUnauthenticated() (unless cancelled)
 *  - refresh() throws        → setUnauthenticated() (unless cancelled)
 *  - refresh() succeeds      → loadMe() always called (see AB-29 note below)
 *
 * AB-29: The `isCancelled()` check is intentionally omitted before `loadMe()`.
 * In React 18 StrictMode, the bootstrap effect fires, is cleaned up, then
 * fires again. The second invocation bails via bootstrappedRef, so only one
 * run ever calls refresh(). The cancelled flag is set by the StrictMode
 * cleanup before the async refresh() resolves; returning early here would
 * leave auth.status === "initializing" permanently. React 18 preserves
 * component state through the simulated unmount/remount, so setStatus() calls
 * from loadMe() still reach the mounted component — the early return was the
 * bug, not the solution.
 */
export async function runBootstrap(callbacks: BootstrapCallbacks): Promise<void> {
  const { getRefreshToken, refresh, loadMe, setUnauthenticated, isCancelled } = callbacks;

  if (getRefreshToken() === null) {
    if (!isCancelled()) setUnauthenticated();
    return;
  }

  try {
    await refresh();
  } catch {
    if (!isCancelled()) setUnauthenticated();
    return;
  }

  // AB-29: deliberately no isCancelled() check here.
  await loadMe();
}
