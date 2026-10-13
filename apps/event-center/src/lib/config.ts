/**
 * Build-time configuration. Vite inlines `import.meta.env.VITE_*`.
 *
 * VITE_API_BASE_URL is required: a missing value throws on startup and
 * the root error screen shows it, so the app never talks to a wrong host.
 */

export interface EventCenterConfig {
  /** arena-api base URL without a trailing slash. */
  readonly apiBaseUrl: string;
  readonly isDevelopment: boolean;
}

function readApiBaseUrl(): string {
  const raw = import.meta.env.VITE_API_BASE_URL;
  if (typeof raw !== "string" || raw.length === 0) {
    throw new Error(
      "VITE_API_BASE_URL is not set. Copy apps/event-center/.env.example to .env.local and set the API address.",
    );
  }
  return raw.replace(/\/+$/, "");
}

export const config: EventCenterConfig = {
  apiBaseUrl: readApiBaseUrl(),
  isDevelopment: import.meta.env.DEV === true,
};
