/**
 * Showing the poster an event already has (task 7-1).
 *
 * `GET /v1/media/{id}` answers a signed address that lives about seven
 * minutes. It is NEVER kept as the picture's identity or put into the page:
 * the bytes are fetched at once into a Blob (the page shows a blob address,
 * which is revoked when the screen goes away) and the next visit asks again.
 * The persisted identity of a poster is its media id, nothing else.
 */
import type { MediaObject } from "@/lib/api/types";
import { trustedMediaUrl } from "@/lib/posters";

/** Pictures the page draws. Only these: an SVG could carry script and is never drawn. */
export const DISPLAY_TYPES = ["image/jpeg", "image/png", "image/webp", "image/gif"] as const;
/** The server accepts up to 32 MiB; a bigger answer is not a poster. */
export const DISPLAY_MAX_BYTES = 32 * 1024 * 1024;

export type PosterLoad =
  | { kind: "ok"; blob: Blob; width: number | null; height: number | null }
  /** No `media.read`, or the object belongs to another organization (403/404). */
  | { kind: "unavailable"; why: "denied" | "missing" }
  /** The picture could not be fetched now: try again. */
  | { kind: "failed"; why: "network" | "address" | "type" | "size" };

export interface LoadDeps {
  getMedia: (mediaId: string, signal?: AbortSignal) => Promise<{ media_object: MediaObject }>;
  /** A plain GET of the signed address: no token, no cookie, no custom header. */
  fetchBytes: (url: string, signal?: AbortSignal) => Promise<Response>;
  apiBaseUrl: string;
}

export const browserLoadDeps = (apiBaseUrl: string, getMedia: LoadDeps["getMedia"]): LoadDeps => ({
  getMedia,
  fetchBytes: (url, signal) => fetch(url, { method: "GET", credentials: "omit", cache: "no-store", signal }),
  apiBaseUrl,
});

function statusOf(err: unknown): number | null {
  return err !== null && typeof err === "object" && "status" in err && typeof (err as { status: unknown }).status === "number"
    ? (err as { status: number }).status
    : null;
}

/**
 * `width` asks the media service for a picture resized to that many pixels (the query parameter `w`);
 * an older server ignores it and sends the original, which the page then scales down in the browser.
 */
export async function loadPosterBlob(deps: LoadDeps, mediaId: string, signal?: AbortSignal, width?: number): Promise<PosterLoad> {
  let media: MediaObject;
  try {
    media = (await deps.getMedia(mediaId, signal)).media_object;
  } catch (err) {
    const status = statusOf(err);
    if (status === 401 || status === 403) return { kind: "unavailable", why: "denied" };
    if (status === 404) return { kind: "unavailable", why: "missing" };
    return { kind: "failed", why: "network" };
  }
  const type = typeof media?.content_type === "string" ? media.content_type.toLowerCase() : "";
  if (!(DISPLAY_TYPES as readonly string[]).includes(type)) return { kind: "failed", why: "type" };
  const url = typeof media.signed_url === "string" ? trustedMediaUrl(media.signed_url, deps.apiBaseUrl) : null;
  if (url === null) return { kind: "failed", why: "address" };
  if (width !== undefined && Number.isInteger(width) && width > 0) url.searchParams.set("w", String(width));
  try {
    const res = await deps.fetchBytes(url.toString(), signal);
    if (!res.ok) return { kind: "failed", why: "network" };
    const bytes = await res.blob();
    if (bytes.size === 0 || bytes.size > DISPLAY_MAX_BYTES) return { kind: "failed", why: "size" };
    // The stored type names the picture, not whatever the response header says.
    return {
      kind: "ok",
      blob: new Blob([bytes], { type }),
      width: typeof media.width === "number" ? media.width : null,
      height: typeof media.height === "number" ? media.height : null,
    };
  } catch {
    return { kind: "failed", why: "network" };
  }
}
