/**
 * Runtime configuration sourced from Vite env vars.
 *
 * Vite inlines `import.meta.env.VITE_*` at build time. Missing values fall
 * back to safe defaults so the admin shell never silently targets the wrong
 * backend; instead a visible error surfaces during startup.
 */

export interface AdminConfig {
  /** Backend HTTP API base URL, no trailing slash. */
  readonly apiBaseUrl: string;
  /** True when running `vite dev` / `vite preview` in dev mode. */
  readonly isDevelopment: boolean;
}

function readApiBaseUrl(): string {
  const raw = import.meta.env.VITE_API_BASE_URL;
  if (typeof raw !== "string" || raw.length === 0) {
    // Surface a loud, structured failure on startup rather than silently
    // pointing the admin UI at the wrong host. The error is caught by the
    // root <ErrorBoundary /> and rendered as a recovery screen.
    throw new Error(
      "VITE_API_BASE_URL is not set. Copy apps/admin-web/.env.example to .env.local and set the backend URL.",
    );
  }
  return raw.replace(/\/+$/, "");
}

export const config: AdminConfig = {
  apiBaseUrl: readApiBaseUrl(),
  isDevelopment: import.meta.env.DEV === true,
};
