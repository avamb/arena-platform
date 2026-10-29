/**
 * The poster on the event card (task 7-1): shows the poster the event has,
 * and, for a person who may, uploads or replaces it.
 *
 * What the screen asks of the server, and why:
 *  - the persisted poster id is `poster_media_id` of the event list item the
 *    card already has: no new read;
 *  - the picture is fetched with `media.read` only when the person has it;
 *  - the buttons need `media.write` AND `event.update` (an upload and a PATCH);
 *  - a replacement uploads, then attaches. The old poster stays shown until the
 *    server's answer proves the new id is on this event; a failure of either
 *    step leaves it as it was, and a retry of the second step never uploads
 *    again.
 *
 * The component is keyed by organization and event (EventScreen), so a
 * different event is a different instance: its picker, its request and its
 * notices cannot carry over, and the request of the old one is aborted.
 */
import { useCallback, useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { PosterPicker, usePosterPicker } from "@/components/PosterPicker";
import { attachEventPoster, fetchMediaObject, listEvents, uploadPosterMedia } from "@/lib/api/endpoints";
import { config } from "@/lib/config";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { loadPosterBlob, browserLoadDeps } from "@/lib/posterImage";
import { replacePoster, attachPoster, type Failure, type PosterApi } from "@/lib/posterFlow";
import type { PosterType } from "@/lib/posters";

/** The calls of the poster flow, bound to this organization. */
export function posterApi(orgId: string, locale: string): PosterApi {
  return {
    upload: (file, type, signal) => uploadPosterMedia(orgId, file, type, signal),
    attach: (eventId, mediaId, signal) => attachEventPoster(orgId, eventId, mediaId, signal),
    readPosterId: async (eventId, signal) => {
      void signal;
      const list = await listEvents(orgId, locale);
      const found = list.events.find((e) => e.id === eventId);
      return found === undefined ? undefined : (found.poster_media_id ?? null);
    },
  };
}

type PosterImageState =
  | { phase: "loading" }
  | { phase: "ok"; url: string; width: number | null; height: number | null }
  | { phase: "unavailable" }
  | { phase: "failed" };
type Tagged = PosterImageState & { id: string };

/**
 * The picture of a poster id as a blob address. The signed link of the API
 * answer is used once and dropped; the blob address is revoked when the id
 * changes or the screen goes away, and an answer that arrives after that is
 * ignored.
 */
export function usePosterImage(mediaId: string | null, enabled: boolean, width?: number) {
  const [tagged, setTagged] = useState<Tagged | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (mediaId === null || !enabled) return;
    const controller = new AbortController();
    let url: string | null = null;
    setTagged({ id: mediaId, phase: "loading" });
    void loadPosterBlob(browserLoadDeps(config.apiBaseUrl, fetchMediaObject), mediaId, controller.signal, width).then((res) => {
      if (controller.signal.aborted) return;
      if (res.kind === "ok") {
        url = URL.createObjectURL(res.blob);
        setTagged({ id: mediaId, phase: "ok", url, width: res.width, height: res.height });
      } else setTagged({ id: mediaId, phase: res.kind === "unavailable" ? "unavailable" : "failed" });
    });
    return () => {
      controller.abort();
      if (url !== null) URL.revokeObjectURL(url);
    };
  }, [mediaId, enabled, attempt, width]);
  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  // The state of ANOTHER poster id is never shown (and its address may already be revoked).
  const state: PosterImageState = tagged !== null && tagged.id === mediaId ? tagged : { phase: "loading" };
  return { state, reload };
}

type Work =
  | { phase: "idle" }
  | { phase: "uploading" }
  | { phase: "attaching" }
  | { phase: "saved" }
  | { phase: "uploadFailed"; failure: Failure }
  /** Stored, not attached: `mediaId` is kept so the retry only attaches. */
  | { phase: "attachFailed"; failure: Failure; mediaId: string };

function failureText(f: Failure, ifRejected: MessageKey, ifUnknown: MessageKey): MessageKey {
  if (f.kind === "denied") return "poster.fail.denied";
  if (f.kind === "rejected") return ifRejected;
  return ifUnknown;
}

export function EventPoster({
  orgId,
  eventId,
  eventName,
  posterMediaId,
  canView,
  canChange,
}: {
  orgId: string;
  eventId: string;
  eventName: string;
  posterMediaId: string | null;
  canView: boolean;
  canChange: boolean;
}) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const picker = usePosterPicker(`${orgId}:${eventId}`);
  const [work, setWork] = useState<Work>({ phase: "idle" });
  const image = usePosterImage(posterMediaId, canView);
  // The scope of the running request: a result of another scope is dropped.
  const live = useRef<AbortController | null>(null);
  const busyRef = useRef(false);
  useEffect(
    () => () => {
      live.current?.abort();
    },
    [],
  );

  const busy = work.phase === "uploading" || work.phase === "attaching";
  const ready = picker.state.phase === "ready" ? picker.state : null;

  const finish = async () => {
    setWork({ phase: "saved" });
    picker.clear();
    await queryClient.invalidateQueries({ queryKey: ["events", orgId] });
  };

  const save = async () => {
    // A double click, or a click while the request runs, never starts a second upload.
    if (ready === null || busyRef.current) return;
    busyRef.current = true;
    const controller = new AbortController();
    live.current = controller;
    const api = posterApi(orgId, locale);
    setWork({ phase: "uploading" });
    // The flow uploads, then attaches: `uploading` shows until the upload answered.
    const result = await replacePoster(api, { eventId, file: ready.file, type: ready.info.type as PosterType }, controller.signal);
    busyRef.current = false;
    if (controller.signal.aborted) return;
    if (result.kind === "done") await finish();
    else if (result.kind === "upload_failed") setWork({ phase: "uploadFailed", failure: result.failure });
    else setWork({ phase: "attachFailed", failure: result.failure, mediaId: result.mediaId });
  };

  const retryAttach = async () => {
    if (work.phase !== "attachFailed" || busyRef.current) return;
    busyRef.current = true;
    const mediaId = work.mediaId;
    const controller = new AbortController();
    live.current = controller;
    setWork({ phase: "attaching" });
    const res = await attachPoster(posterApi(orgId, locale), eventId, mediaId, controller.signal);
    busyRef.current = false;
    if (controller.signal.aborted) return;
    if (res.kind === "attached") await finish();
    else setWork({ phase: "attachFailed", failure: res.failure, mediaId });
  };

  const choose = (file: File) => {
    if (busyRef.current) return;
    // A new choice drops the stored-but-unattached picture of the old one (it is only forgotten here).
    setWork((w) => (w.phase === "saved" || w.phase === "idle" ? w : { phase: "idle" }));
    picker.select(file);
  };
  const clear = () => {
    if (busyRef.current) return;
    setWork({ phase: "idle" });
    picker.clear();
  };

  return (
    <section aria-labelledby="event-poster-title" data-testid="event-poster" data-busy={busy ? "1" : "0"}>
      <h2 id="event-poster-title">{t("poster.title")}</h2>

      {posterMediaId === null && <p data-testid="poster-none">{t("poster.none")}</p>}
      {posterMediaId !== null && !canView && (
        <p className="notice" data-testid="poster-no-view">
          {t("poster.noView")}
        </p>
      )}
      {posterMediaId !== null && canView && image.state.phase === "loading" && (
        <p role="status" data-testid="poster-loading">
          {t("poster.loading")}
        </p>
      )}
      {posterMediaId !== null && canView && image.state.phase === "ok" && (
        <img
          className="poster-img"
          src={image.state.url}
          width={image.state.width ?? undefined}
          height={image.state.height ?? undefined}
          alt={t("poster.alt", { name: eventName })}
          data-testid="poster-image"
          data-media-id={posterMediaId}
        />
      )}
      {posterMediaId !== null && canView && image.state.phase === "unavailable" && (
        <p className="notice" data-testid="poster-unavailable">
          {t("poster.unavailable")}
        </p>
      )}
      {posterMediaId !== null && canView && image.state.phase === "failed" && (
        <div className="notice notice-bad" role="alert" data-testid="poster-load-failed">
          <p>{t("poster.loadFailed")}</p>
          <button type="button" className="btn" onClick={image.reload}>
            {t("common.retry")}
          </button>
        </div>
      )}

      {!canChange && (
        <p className="field-hint" data-testid="poster-read-only">
          {t("poster.readOnly")}
        </p>
      )}

      {canChange && (
        <div data-testid="poster-editor">
          <PosterPicker
            state={picker.state}
            onSelect={choose}
            onClear={clear}
            busy={busy}
            testId="poster-picker"
            label={posterMediaId === null ? "poster.upload" : "poster.replace"}
          />
          <p className="field-hint">{t("poster.keepNote")}</p>

          {work.phase === "uploading" && (
            <p role="status" data-testid="poster-uploading">
              {t("poster.saving.upload")}
            </p>
          )}
          {work.phase === "attaching" && (
            <p role="status" data-testid="poster-attaching">
              {t("poster.saving.attach")}
            </p>
          )}
          {work.phase === "saved" && (
            <p className="notice notice-ok" role="status" data-testid="poster-saved">
              {t("poster.saved")}
            </p>
          )}
          {work.phase === "uploadFailed" && (
            <div className="notice notice-bad" role="alert" data-testid="poster-upload-failed" data-kind={work.failure.kind}>
              <p>{t(failureText(work.failure, "poster.fail.rejected", "poster.fail.upload"))}</p>
            </div>
          )}
          {work.phase === "attachFailed" && (
            <div className="notice notice-bad" role="alert" data-testid="poster-attach-failed" data-kind={work.failure.kind}>
              <p>{t("poster.fail.attach")}</p>
              <p>{t(failureText(work.failure, "poster.fail.attachRejected", "poster.fail.attachUnknown"))}</p>
            </div>
          )}

          <div className="actions">
            {work.phase === "attachFailed" && work.failure.kind !== "rejected" && work.failure.kind !== "denied" && (
              <button type="button" className="btn btn-primary" onClick={() => void retryAttach()} data-testid="poster-retry-attach">
                {t("poster.attach.retry")}
              </button>
            )}
            {ready !== null && work.phase !== "attachFailed" && (
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => void save()}
                disabled={busy}
                aria-busy={busy}
                data-testid="poster-save"
              >
                {busy ? t(work.phase === "attaching" ? "poster.saving.attach" : "poster.saving.upload") : t("poster.save")}
              </button>
            )}
          </div>
        </div>
      )}
    </section>
  );
}
