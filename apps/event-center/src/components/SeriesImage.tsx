/**
 * One picture of a tour (spec 63, L-2): the portrait poster 4:5 or the wide image. The file is checked in the
 * browser, uploaded as media of owner type `event_series` and its id goes to the form's draft; the tour's own Save
 * sends it with the rest (one PATCH). The picture is fetched as a blob with `media.read`, like the event poster.
 */
import { useRef, useState } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { PhotoThumb, useAction } from "@/components/materials/shared";
import { uploadSeriesImage } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { POSTER_ACCEPT, POSTER_MAX_BYTES, checkPosterFile, type PosterRejection } from "@/lib/posters";

const REJECT_KEY: Record<PosterRejection, MessageKey> = {
  empty: "poster.reject.empty",
  tooLarge: "poster.reject.tooLarge",
  notImage: "poster.reject.notImage",
  corrupt: "poster.reject.corrupt",
  tooBig: "poster.reject.tooBig",
};

export interface SeriesImageProps {
  orgId: string;
  label: string;
  hint: string;
  mediaId: string | null;
  onChange: (mediaId: string | null) => void;
  /** `media.read`: the picture can be shown. */
  canView: boolean;
  /** `media.write`: a picture can be uploaded. */
  canUpload: boolean;
  disabled?: boolean;
  testId: string;
}

export function SeriesImage({ orgId, label, hint, mediaId, onChange, canView, canUpload, disabled = false, testId }: SeriesImageProps) {
  const { t } = useI18n();
  const action = useAction();
  const [reject, setReject] = useState<PosterRejection | null>(null);
  const input = useRef<HTMLInputElement>(null);

  const onFile = async (file: File | undefined) => {
    if (input.current !== null) input.current.value = "";
    if (file === undefined) return;
    setReject(null);
    const checked = await checkPosterFile(file);
    if (!checked.ok) {
      setReject(checked.reason);
      return;
    }
    await action.run(async () => {
      const uploaded = await uploadSeriesImage(orgId, file, checked.info.type);
      onChange(uploaded.media_object.id);
    });
  };

  return (
    <section className="mat-block" aria-labelledby={`${testId}-label`} data-testid={testId}>
      <h3 id={`${testId}-label`}>{label}</h3>
      <p className="field-hint">{hint}</p>
      {mediaId !== null ? (
        <PhotoThumb mediaId={mediaId} canView={canView} alt={label} width={480} />
      ) : (
        <p className="muted" data-testid={`${testId}-none`}>
          {t("series.none")}
        </p>
      )}
      {canUpload ? (
        <div className="actions">
          <input
            ref={input}
            id={`${testId}-file`}
            className="visually-hidden"
            type="file"
            accept={POSTER_ACCEPT}
            disabled={disabled || action.busy}
            onChange={(e) => void onFile(e.target.files?.[0])}
            data-testid={`${testId}-file`}
          />
          <label htmlFor={`${testId}-file`} className="btn">
            {action.busy ? t("series.image.uploading") : t(mediaId === null ? "series.image.choose" : "series.image.replace")}
          </label>
          {mediaId !== null && (
            <button type="button" className="btn" disabled={disabled || action.busy} onClick={() => onChange(null)} data-testid={`${testId}-remove`}>
              {t("series.image.remove")}
            </button>
          )}
        </div>
      ) : (
        <p className="field-hint">{t("series.image.noRight")}</p>
      )}
      {reject !== null && (
        <p className="bad-text" role="alert" data-testid={`${testId}-rejected`}>
          {t(REJECT_KEY[reject], { mb: POSTER_MAX_BYTES / (1024 * 1024) })}
        </p>
      )}
      {action.error !== null && <ErrorNotice error={action.error} testId={`${testId}-error`} />}
    </section>
  );
}
