/**
 * Uploading a poster and making it the event's poster (task 7-1).
 *
 * Two server writes that are NEVER reported as one:
 *  1. UPLOAD  (`POST /v1/media`)   - stores the picture, answers its id;
 *  2. ATTACH  (`PATCH .../events/{id}` with `poster_media_id`) - points the
 *     event at it.
 * "Saved" is said only after the server's own answer (or a fresh read of the
 * event) shows that exact id on that exact event.
 *
 * A failed attach keeps the media id and the event id, and a retry repeats the
 * ATTACH only: no second upload, no second event, never the whole bundle.
 * A picture uploaded but never attached stays in storage: there is no cleanup
 * call for it in this release (documented limit, README), and a picture that
 * is already an event's poster is NEVER deleted by a replacement or a cancel.
 *
 * Free of React and of `fetch`: the calls are passed in, so every transition is
 * tested with fakes.
 */
import { ApiError } from "@/lib/api/http";
import type { EventEnvelope, MediaObject } from "@/lib/api/types";
import type { PosterType } from "@/lib/posters";
import type { SaveOutcome } from "@/lib/wizard/save";

export type FailureKind =
  /** 401/403: the person may not do this. */
  | "denied"
  /** The server looked and said no (400, 404, 413, 422...): nothing changed, a retry with the same input gives the same answer. */
  | "rejected"
  /** No answer, a timeout or a 5xx: the write MAY have happened. */
  | "unknown"
  /** The person left or chose again: not an error to show. */
  | "aborted";

export interface Failure {
  kind: FailureKind;
  status: number;
  code: string;
  requestId?: string;
}

export function classifyFailure(err: unknown): Failure {
  if (err instanceof ApiError) {
    if (err.code === "network.aborted") return { kind: "aborted", status: 0, code: err.code };
    const base = { status: err.status, code: err.code, requestId: err.requestId };
    if (err.status === 401 || err.status === 403) return { kind: "denied", ...base };
    if (err.status >= 400 && err.status < 500 && err.status !== 408 && err.status !== 429) {
      return { kind: "rejected", ...base };
    }
    return { kind: "unknown", ...base };
  }
  return { kind: "unknown", status: 0, code: "network.failure" };
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface PosterApi {
  upload: (file: Blob, type: PosterType, signal?: AbortSignal) => Promise<{ media_object: MediaObject }>;
  attach: (eventId: string, mediaId: string, signal?: AbortSignal) => Promise<EventEnvelope>;
  /** Reads the event's CURRENT poster id from the server (null: none; undefined: the event is not found). */
  readPosterId: (eventId: string, signal?: AbortSignal) => Promise<string | null | undefined>;
}

export type UploadResult = { kind: "uploaded"; mediaId: string } | { kind: "failed"; failure: Failure };

export async function uploadPoster(api: Pick<PosterApi, "upload">, file: Blob, type: PosterType, signal?: AbortSignal): Promise<UploadResult> {
  try {
    const res = await api.upload(file, type, signal);
    const id = res?.media_object?.id;
    // The id is the only thing the next step needs: an answer without it is a lost response, not a success.
    if (typeof id !== "string" || !UUID.test(id)) {
      return { kind: "failed", failure: { kind: "unknown", status: 201, code: "http.unreadable_answer" } };
    }
    return { kind: "uploaded", mediaId: id };
  } catch (err) {
    return { kind: "failed", failure: classifyFailure(err) };
  }
}

export type AttachResult = { kind: "attached" } | { kind: "failed"; failure: Failure };

/**
 * ONE PATCH, then proof. The answer must name this event and this media id. A
 * lost answer (timeout, 5xx) is checked by reading the event back: only then
 * is the poster known to be attached, or known not to be.
 */
export async function attachPoster(api: PosterApi, eventId: string, mediaId: string, signal?: AbortSignal): Promise<AttachResult> {
  try {
    const res = await api.attach(eventId, mediaId, signal);
    if (res?.event?.id === eventId && res.event.poster_media_id === mediaId) return { kind: "attached" };
    return { kind: "failed", failure: { kind: "unknown", status: 200, code: "http.unverified_answer" } };
  } catch (err) {
    const failure = classifyFailure(err);
    if (failure.kind !== "unknown") return { kind: "failed", failure };
    try {
      if ((await api.readPosterId(eventId, signal)) === mediaId) return { kind: "attached" };
    } catch {
      // Still not known: the retry is safe, the PATCH is idempotent.
    }
    return { kind: "failed", failure };
  }
}

// ─── replacing the poster of an existing event ───────────────────────────────

export type ReplaceResult =
  | { kind: "done"; mediaId: string }
  /** Nothing changed: the old poster is still the poster. */
  | { kind: "upload_failed"; failure: Failure }
  /** The picture is stored (`mediaId`) but the event still shows its old poster. Retry attaches only. */
  | { kind: "attach_failed"; mediaId: string; failure: Failure };

export async function replacePoster(
  api: PosterApi,
  args: { eventId: string; file: Blob; type: PosterType; uploadedMediaId?: string },
  signal?: AbortSignal,
): Promise<ReplaceResult> {
  let mediaId = args.uploadedMediaId ?? "";
  if (mediaId === "") {
    const up = await uploadPoster(api, args.file, args.type, signal);
    if (up.kind === "failed") return { kind: "upload_failed", failure: up.failure };
    mediaId = up.mediaId;
  }
  const att = await attachPoster(api, args.eventId, mediaId, signal);
  if (att.kind === "failed") return { kind: "attach_failed", mediaId, failure: att.failure };
  return { kind: "done", mediaId };
}

// ─── creating an event with a poster ─────────────────────────────────────────

export type CreateWithPosterResult =
  /** The event exists; `poster` says what happened to the picture. */
  | { kind: "done"; eventId: string; poster: "none" | "attached" }
  /** Nothing was created: the upload failed. */
  | { kind: "upload_failed"; failure: Failure }
  /** The event was not (surely) created. `mediaId` is kept for the retry. */
  | { kind: "save_failed"; outcome: Exclude<SaveOutcome, { kind: "saved" }>; mediaId: string }
  /** The event EXISTS; the picture is stored; the poster is NOT attached. */
  | { kind: "attach_failed"; eventId: string; mediaId: string; failure: Failure };

export interface CreateArgs {
  /** The chosen file, still in memory (null after a reload or when none was chosen). */
  file: Blob | null;
  type: PosterType | null;
  /** From the stored draft: the picture is already uploaded. */
  mediaId: string;
  /** From the stored draft: the event already exists. */
  eventId: string;
  save: () => Promise<SaveOutcome>;
  /** Persists progress at once (a later failure must not lose an id). */
  onProgress: (patch: { mediaId?: string; eventId?: string }) => void;
}

/**
 * upload -> create the draft with the draft's own stable key -> attach.
 * A step already done (an id in the draft) is never repeated.
 */
export async function createWithPoster(api: PosterApi, a: CreateArgs, signal?: AbortSignal): Promise<CreateWithPosterResult> {
  let mediaId = a.mediaId;
  let eventId = a.eventId;
  if (eventId === "") {
    if (mediaId === "" && a.file !== null && a.type !== null) {
      const up = await uploadPoster(api, a.file, a.type, signal);
      if (up.kind === "failed") return { kind: "upload_failed", failure: up.failure };
      mediaId = up.mediaId;
      a.onProgress({ mediaId });
    }
    const saved = await a.save();
    if (saved.kind !== "saved") return { kind: "save_failed", outcome: saved, mediaId };
    eventId = saved.eventId;
    // Only a poster that is still waiting needs the event id kept: without one, "done" is all there is.
    if (mediaId !== "") a.onProgress({ eventId });
  }
  if (mediaId === "") return { kind: "done", eventId, poster: "none" };
  const att = await attachPoster(api, eventId, mediaId, signal);
  if (att.kind === "failed") return { kind: "attach_failed", eventId, mediaId, failure: att.failure };
  return { kind: "done", eventId, poster: "attached" };
}
