/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Backend API base URL, no trailing slash. Empty = same origin. */
  readonly VITE_API_BASE_URL?: string;
}
