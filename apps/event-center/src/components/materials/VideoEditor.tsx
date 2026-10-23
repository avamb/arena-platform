/**
 * Video links of an event (up to 4): add, view, remove, reorder. The API has
 * no PATCH for a video, so "replace" adds the new link first, then removes the
 * old one and restores the position: a rejected new link never costs the old
 * one. At the limit a link cannot be replaced in place (the add would be
 * refused), the person is told to remove it first.
 */
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { ActionNotice, MoveButtons, useAction } from "@/components/materials/shared";
import { addVideo, deleteVideo, listVideos, orderVideos, type VideoItem } from "@/lib/api/materialsApi";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { LIMITS, checkHttpsUrl, moved, type UrlProblem } from "@/lib/materials";

const PROVIDER_LABEL: Record<string, string> = { youtube: "YouTube", vimeo: "Vimeo", rutube: "Rutube" };

export const URL_PROBLEM_KEY: Record<UrlProblem, MessageKey> = {
  empty: "mat.url.empty",
  tooLong: "mat.url.tooLong",
  notHttps: "mat.url.notHttps",
  invalid: "mat.url.invalid",
};

export function VideoEditor({ orgId, eventId, canWrite }: { orgId: string; eventId: string; canWrite: boolean }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const queryKey = ["materials", "videos", orgId, eventId] as const;
  const list = useQuery({ queryKey, queryFn: ({ signal }) => listVideos(orgId, eventId, signal) });
  const action = useAction();
  const [url, setUrl] = useState("");
  const [problem, setProblem] = useState<UrlProblem | null>(null);
  const [removing, setRemoving] = useState<VideoItem | null>(null);
  const [replacing, setReplacing] = useState<{ id: string; url: string } | null>(null);
  const items = list.data?.items ?? [];
  const max = LIMITS.videos;
  const full = items.length >= max;
  const reload = () => queryClient.invalidateQueries({ queryKey });

  const add = async () => {
    const checked = checkHttpsUrl(url);
    if (!checked.ok) {
      setProblem(checked.problem);
      return;
    }
    setProblem(null);
    const done = await action.run(async () => {
      try {
        await addVideo(orgId, eventId, checked.url);
      } finally {
        await reload();
      }
    });
    if (done) setUrl("");
  };

  const replace = async (old: VideoItem, index: number) => {
    if (replacing === null) return;
    const checked = checkHttpsUrl(replacing.url);
    if (!checked.ok) {
      setProblem(checked.problem);
      return;
    }
    setProblem(null);
    const done = await action.run(async () => {
      try {
        const added = await addVideo(orgId, eventId, checked.url);
        try {
          await deleteVideo(orgId, eventId, old.id);
        } catch {
          throw new Error("replace_delete_failed");
        }
        // The new link was appended: put it where the old one was.
        const ids = items.filter((v) => v.id !== old.id).map((v) => v.id);
        ids.splice(index, 0, added.id);
        await orderVideos(orgId, eventId, ids);
      } finally {
        await reload();
      }
    });
    if (done) setReplacing(null);
  };

  return (
    <section className="mat-block" aria-labelledby="mat-videos-title" data-testid="mat-videos">
      <h3 id="mat-videos-title">
        {t("mat.video.title")} <span className="field-hint">({t("mat.count", { count: items.length, max })})</span>
      </h3>
      <p className="field-hint">{t("mat.video.hint", { max })}</p>
      {list.isLoading && <Loading />}
      {list.error !== null && <ErrorBox error={list.error} onRetry={() => void list.refetch()} />}
      {list.data !== undefined && items.length === 0 && <Empty>{t("mat.empty")}</Empty>}
      <ul className="cards">
        {items.map((v, index) => (
          <li key={v.id} className="card mat-row" data-testid="mat-video-item">
            <p className="mat-url">
              <a href={v.url} target="_blank" rel="noopener noreferrer">
                {v.url}
              </a>
            </p>
            <p className="field-hint">
              {PROVIDER_LABEL[v.provider] !== undefined ? t("mat.video.player", { provider: PROVIDER_LABEL[v.provider] }) : t("mat.video.plain")}
            </p>
            {canWrite && replacing?.id === v.id && (
              <div className="mat-form">
                <label className="field-label" htmlFor={`mat-video-replace-${v.id}`}>
                  {t("mat.video.url")}
                </label>
                <input
                  id={`mat-video-replace-${v.id}`}
                  className="input"
                  type="url"
                  value={replacing.url}
                  onChange={(e) => setReplacing({ id: v.id, url: e.target.value })}
                  data-testid="mat-video-replace-url"
                />
                <div className="actions">
                  <button type="button" className="btn btn-primary" disabled={action.busy} onClick={() => void replace(v, index)} data-testid="mat-video-replace-save">
                    {t("mat.save")}
                  </button>
                  <button type="button" className="btn" disabled={action.busy} onClick={() => setReplacing(null)}>
                    {t("mat.cancel")}
                  </button>
                </div>
              </div>
            )}
            {canWrite && (
              <div className="actions">
                <MoveButtons
                  index={index}
                  count={items.length}
                  disabled={action.busy}
                  onMove={(d) =>
                    void action.run(async () => {
                      queryClient.setQueryData(queryKey, await orderVideos(orgId, eventId, moved(items, index, d)));
                    })
                  }
                  testId={`mat-video-${index}`}
                />
                <button
                  type="button"
                  className="btn"
                  disabled={action.busy || full}
                  onClick={() => {
                    setProblem(null);
                    setReplacing({ id: v.id, url: v.url });
                  }}
                  data-testid={`mat-video-${index}-replace`}
                >
                  {t("mat.video.replace")}
                </button>
                <button type="button" className="btn" disabled={action.busy} onClick={() => setRemoving(v)} data-testid={`mat-video-${index}-remove`}>
                  {t("mat.remove")}
                </button>
              </div>
            )}
          </li>
        ))}
      </ul>
      {canWrite && full && <p className="field-hint">{t("mat.video.replaceFullHint")}</p>}
      {canWrite && list.data !== undefined && !full && (
        <div className="mat-form">
          <label className="field-label" htmlFor="mat-video-url">
            {t("mat.video.url")}
          </label>
          <input
            id="mat-video-url"
            className="input"
            type="url"
            inputMode="url"
            value={url}
            maxLength={LIMITS.url + 20}
            placeholder="https://"
            onChange={(e) => setUrl(e.target.value)}
            aria-invalid={problem !== null}
            data-testid="mat-video-url"
          />
          <div className="actions">
            <button type="button" className="btn btn-primary" disabled={action.busy} onClick={() => void add()} data-testid="mat-video-add">
              {t("mat.video.add")}
            </button>
          </div>
        </div>
      )}
      {problem !== null && (
        <p className="field-error" role="alert" data-testid="mat-video-problem">
          {t(URL_PROBLEM_KEY[problem], { max: LIMITS.url })}
        </p>
      )}
      {action.error instanceof Error && action.error.message === "replace_delete_failed" ? (
        <p className="notice notice-bad" role="alert" data-testid="mat-videos-replace-failed">
          {t("mat.video.replaceFailed")}
        </p>
      ) : (
        <ActionNotice action={action} max={max} testId="mat-videos" />
      )}
      {removing !== null && (
        <ConfirmPressDialog
          title={t("mat.confirm.title")}
          subject={removing.url}
          explain={t("mat.confirm.explain")}
          confirmLabel={t("mat.remove")}
          testId="mat-video-confirm"
          onCancel={() => setRemoving(null)}
          onConfirm={async () => {
            await deleteVideo(orgId, eventId, removing.id);
            await reload();
            setRemoving(null);
          }}
        />
      )}
    </section>
  );
}
