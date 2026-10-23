/**
 * The gallery (up to 6) and the photo report (up to 30) of an event: upload
 * through the media API, caption, order, remove. The two lists differ only by
 * the owner type of the media, the limit and a note on when buyers see them.
 */
import { useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { ActionNotice, MoveButtons, PhotoThumb, useAction } from "@/components/materials/shared";
import {
  addPhoto,
  deletePhoto,
  listPhotos,
  orderPhotos,
  patchPhotoCaption,
  uploadEventPhoto,
  type ItemsResponse,
  type PhotoItem,
  type PhotoKind,
} from "@/lib/api/materialsApi";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { LIMITS, cleanText, moved } from "@/lib/materials";
import { POSTER_ACCEPT, POSTER_MAX_BYTES, checkPosterFile, type PosterRejection } from "@/lib/posters";

const REJECT_KEY: Record<PosterRejection, MessageKey> = {
  empty: "poster.reject.empty",
  tooLarge: "poster.reject.tooLarge",
  notImage: "poster.reject.notImage",
  corrupt: "poster.reject.corrupt",
  tooBig: "poster.reject.tooBig",
};

function PhotoRow({
  kind,
  item,
  index,
  count,
  canWrite,
  canView,
  busy,
  onCaption,
  onMove,
  onRemove,
}: {
  kind: PhotoKind;
  item: PhotoItem;
  index: number;
  count: number;
  canWrite: boolean;
  canView: boolean;
  busy: boolean;
  onCaption: (id: string, caption: string | null) => void;
  onMove: (index: number, delta: -1 | 1) => void;
  onRemove: (item: PhotoItem) => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState(item.caption ?? "");
  const changed = cleanText(draft) !== (item.caption ?? null);
  const tid = `mat-${kind}-${index}`;
  return (
    <li className="card mat-photo" data-testid={`mat-${kind}-item`}>
      <PhotoThumb mediaId={item.media_id} canView={canView} alt={item.caption ?? t("mat.photo.alt", { n: index + 1 })} />
      <div className="mat-photo-body">
        <label className="field-label" htmlFor={`${tid}-caption`}>
          {t("mat.photo.caption")}
        </label>
        <input
          id={`${tid}-caption`}
          className="input"
          type="text"
          value={draft}
          maxLength={LIMITS.caption}
          disabled={!canWrite || busy}
          onChange={(e) => setDraft(e.target.value)}
          aria-describedby={`${tid}-hint`}
          data-testid={`${tid}-caption`}
        />
        <p className="field-hint" id={`${tid}-hint`}>
          {t("mat.photo.captionHint", { max: LIMITS.caption })}
        </p>
        {canWrite && (
          <div className="actions">
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || !changed}
              onClick={() => onCaption(item.id, cleanText(draft))}
              data-testid={`${tid}-caption-save`}
            >
              {t("mat.photo.captionSave")}
            </button>
            <MoveButtons index={index} count={count} disabled={busy} onMove={(d) => onMove(index, d)} testId={tid} />
            <button type="button" className="btn" disabled={busy} onClick={() => onRemove(item)} data-testid={`${tid}-remove`}>
              {t("mat.remove")}
            </button>
          </div>
        )}
      </div>
    </li>
  );
}

export function PhotoListEditor({
  kind,
  orgId,
  eventId,
  canWrite,
  canUpload,
  canView,
  note,
}: {
  kind: PhotoKind;
  orgId: string;
  eventId: string;
  canWrite: boolean;
  /** media.write and event.update: the upload and the attach. */
  canUpload: boolean;
  canView: boolean;
  /** A line under the hint, for example when the photo report goes public. */
  note?: string;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const max = kind === "gallery" ? LIMITS.gallery : LIMITS.report;
  const queryKey = ["materials", kind, orgId, eventId] as const;
  const list = useQuery({ queryKey, queryFn: ({ signal }) => listPhotos(kind, orgId, eventId, signal) });
  const action = useAction();
  const [reject, setReject] = useState<PosterRejection | null>(null);
  const [removing, setRemoving] = useState<PhotoItem | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const items = list.data?.items ?? [];
  const title = t(kind === "gallery" ? "mat.gallery.title" : "mat.report.title");
  const hint = t(kind === "gallery" ? "mat.gallery.hint" : "mat.report.hint", { max, mb: POSTER_MAX_BYTES / (1024 * 1024) });
  const full = items.length >= max;

  const refresh = (data: ItemsResponse<PhotoItem>) => queryClient.setQueryData(queryKey, data);
  const reload = () => queryClient.invalidateQueries({ queryKey });

  const onFile = async (file: File | undefined) => {
    if (fileInput.current !== null) fileInput.current.value = "";
    if (file === undefined) return;
    setReject(null);
    const checked = await checkPosterFile(file);
    if (!checked.ok) {
      setReject(checked.reason);
      return;
    }
    await action.run(async () => {
      const uploaded = await uploadEventPhoto(kind, orgId, file, checked.info.type);
      try {
        await addPhoto(kind, orgId, eventId, uploaded.media_object.id, null);
      } finally {
        await reload();
      }
    });
  };

  const headingId = `mat-${kind}-title`;
  return (
    <section className="mat-block" aria-labelledby={headingId} data-testid={`mat-${kind}`}>
      <h3 id={headingId}>
        {title} <span className="field-hint">({t("mat.count", { count: items.length, max })})</span>
      </h3>
      <p className="field-hint">{hint}</p>
      {note !== undefined && (
        <p className="notice" data-testid={`mat-${kind}-note`}>
          {note}
        </p>
      )}
      {list.isLoading && <Loading />}
      {list.error !== null && <ErrorBox error={list.error} onRetry={() => void list.refetch()} />}
      {list.data !== undefined && items.length === 0 && <Empty>{t("mat.empty")}</Empty>}
      <ul className="cards mat-photos">
        {items.map((item, index) => (
          <PhotoRow
            key={`${item.id}:${item.caption ?? ""}`}
            kind={kind}
            item={item}
            index={index}
            count={items.length}
            canWrite={canWrite}
            canView={canView}
            busy={action.busy}
            onCaption={(id, caption) =>
              void action.run(async () => {
                await patchPhotoCaption(kind, orgId, eventId, id, caption);
                await reload();
              })
            }
            onMove={(i, delta) =>
              void action.run(async () => {
                refresh(await orderPhotos(kind, orgId, eventId, moved(items, i, delta)));
              })
            }
            onRemove={setRemoving}
          />
        ))}
      </ul>
      {canWrite && list.data !== undefined && (
        <div className="mat-add">
          {canUpload && !full && (
            <>
              <input
                ref={fileInput}
                id={`mat-${kind}-file`}
                className="visually-hidden"
                type="file"
                accept={POSTER_ACCEPT}
                disabled={action.busy}
                onChange={(e) => void onFile(e.target.files?.[0])}
                data-testid={`mat-${kind}-file`}
              />
              <label htmlFor={`mat-${kind}-file`} className={`btn btn-primary${action.busy ? " btn-disabled" : ""}`}>
                {action.busy ? t("mat.photo.uploading") : t("mat.photo.add")}
              </label>
            </>
          )}
          {full && <p className="field-hint">{t("mat.limit", { max })}</p>}
          {reject !== null && (
            <p className="field-error" role="alert" data-testid={`mat-${kind}-reject`}>
              {t(REJECT_KEY[reject], { mb: POSTER_MAX_BYTES / (1024 * 1024) })}
            </p>
          )}
        </div>
      )}
      <ActionNotice action={action} max={max} testId={`mat-${kind}`} />
      {removing !== null && (
        <ConfirmPressDialog
          title={t("mat.confirm.title")}
          explain={t("mat.confirm.explain")}
          confirmLabel={t("mat.remove")}
          testId={`mat-${kind}-confirm`}
          onCancel={() => setRemoving(null)}
          onConfirm={async () => {
            await deletePhoto(kind, orgId, eventId, removing.id);
            await reload();
            setRemoving(null);
          }}
        />
      )}
    </section>
  );
}
