/**
 * The poster step of the wizard, "Poster" (poster-first creation). It comes first
 * and offers two explicit ways on: UPLOAD a poster (we then offer to fill in the
 * event details from it), or "Continue without a poster", chosen on purpose. The
 * wizard cannot create an event until one of them is chosen (a person who may not
 * upload posters is never blocked). Skipping is allowed: the poster can be added
 * later in the event card, and a reminder says so.
 *
 * The picture is uploaded as soon as it is chosen. Reading it is a separate,
 * explicit step: the person is told that the picture is sent for automatic text
 * recognition BEFORE pressing the button, and the answer is only a set of
 * suggestions to review (WizardPosterReview). A poster that cannot be read never
 * blocks the form and never loses the uploaded picture.
 */
import { useId } from "react";
import { PosterPicker } from "@/components/PosterPicker";
import { usePosterImage } from "@/components/EventPoster";
import { FIELD_LABEL, WizardPosterReview } from "@/components/WizardPosterReview";
import type { WizardPoster } from "@/components/useWizardPoster";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import type { PickerState } from "@/lib/posters";
import type { PosterDecision, WizardDraft, DraftAction } from "@/lib/wizard/draft";

export function WizardPosterSection({
  state,
  poster,
  decision,
  mediaId,
  canChange,
  canView,
  busy,
  draft,
  dispatch,
  defaultCategoryName,
}: {
  state: PickerState;
  poster: WizardPoster;
  decision: PosterDecision;
  /** The id of the uploaded picture kept in the draft ("" = none). */
  mediaId: string;
  canChange: boolean;
  canView: boolean;
  busy: boolean;
  draft: WizardDraft;
  dispatch: (action: DraftAction) => void;
  defaultCategoryName: string;
}) {
  const { t } = useI18n();
  const ids = useId();
  const { view } = poster;
  const uploaded = view.upload.phase === "uploaded";
  // After a reload the file is gone: show the stored picture, when this person may see it.
  const restored = uploaded && state.phase === "empty" && mediaId !== "";
  const image = usePosterImage(mediaId, restored && canView);

  return (
    <fieldset className="org-form" data-testid="wizard-poster" data-decision={decision}>
      <legend>{t("wizard.poster.title")}</legend>
      {!canChange ? (
        <p className="field-hint" data-testid="wizard-poster-no-permission">
          {t("wizard.poster.noPermission")}
        </p>
      ) : (
        <>
          <p className="field-hint" id={`${ids}-hint`}>
            {t("wizard.poster.hint")}
          </p>
          <PosterPicker
            state={state}
            onSelect={poster.choose}
            onClear={poster.remove}
            busy={busy}
            testId="wizard-poster-picker"
            label="wizard.poster.upload"
          />

          {restored && (
            <div data-testid="wizard-poster-uploaded">
              <p className="notice">{t("wizard.poster.uploaded")}</p>
              {canView && image.state.phase === "ok" && (
                <img
                  className="poster-img"
                  src={image.state.url}
                  width={image.state.width ?? undefined}
                  height={image.state.height ?? undefined}
                  alt={t("poster.preview.alt")}
                  data-testid="wizard-poster-restored"
                />
              )}
            </div>
          )}

          {view.upload.phase === "uploading" && (
            <p role="status" data-testid="wizard-poster-uploading">
              {t("wizard.poster.uploading")}
            </p>
          )}
          {view.upload.phase === "failed" && (
            <div className="notice notice-bad" role="alert" data-testid="wizard-poster-upload-failed" data-kind={view.upload.failure.kind}>
              <p>
                {t(
                  (view.upload.failure.kind === "rejected"
                    ? "wizard.poster.uploadRejected"
                    : view.upload.failure.kind === "denied"
                      ? "wizard.poster.uploadDenied"
                      : "wizard.poster.uploadFailed") satisfies MessageKey,
                )}
              </p>
              {state.phase === "ready" && view.upload.failure.kind !== "rejected" && view.upload.failure.kind !== "denied" && (
                <button type="button" className="btn" disabled={busy} onClick={poster.retryUpload} data-testid="wizard-poster-upload-retry">
                  {t("wizard.poster.uploadRetry")}
                </button>
              )}
            </div>
          )}
          {uploaded && view.read.phase !== "ready" && decision === "poster" && (
            <p role="status" className="notice notice-ok" data-testid="wizard-poster-ok">
              {t("wizard.poster.uploadedOk")}
            </p>
          )}

          {decision !== "poster" && state.phase !== "checking" && view.upload.phase !== "uploading" && (
            <div className="poster-skip">
              {decision === "" && <p className="field-hint">{t("wizard.poster.skipHint")}</p>}
              {decision === "skipped" && (
                <>
                  <p className="notice" role="status" data-testid="wizard-poster-skipped">
                    {t("wizard.poster.skipped")}
                  </p>
                  <p className="field-hint">{t("wizard.poster.skippedChange")}</p>
                </>
              )}
              {decision === "" && (
                <div className="actions">
                  <button type="button" className="btn" disabled={busy} onClick={poster.skip} data-testid="wizard-poster-skip">
                    {t("wizard.poster.skip")}
                  </button>
                </div>
              )}
            </div>
          )}

          {uploaded && decision === "poster" && (
            <div className="poster-read" data-testid="wizard-poster-read" data-state={view.read.phase}>
              {(view.read.phase === "idle" || view.read.phase === "reading") && (
                <>
                  <p className="field-hint">{t("wizard.poster.read.note")}</p>
                  <div className="actions">
                    <button
                      type="button"
                      className="btn"
                      onClick={poster.read}
                      disabled={busy || view.read.phase === "reading"}
                      aria-busy={view.read.phase === "reading"}
                      data-testid="wizard-poster-read-button"
                    >
                      {t("wizard.poster.read.button")}
                    </button>
                  </div>
                  {view.read.phase === "reading" && (
                    <p role="status" data-testid="wizard-poster-reading">
                      {t("wizard.poster.read.reading")}
                    </p>
                  )}
                </>
              )}
              {view.read.phase === "ready" && view.read.mediaId === mediaId && (
                <WizardPosterReview
                  key={view.read.mediaId}
                  facts={view.read.facts}
                  draft={draft}
                  defaultCategoryName={defaultCategoryName}
                  dispatch={dispatch}
                  onDone={poster.finishRead}
                />
              )}
              {view.read.phase === "nothing" && (
                <>
                  <p className="notice" role="status" data-testid="wizard-poster-read-nothing">
                    {t("wizard.poster.read.nothing")}
                  </p>
                  <div className="actions">
                    <button type="button" className="btn" disabled={busy} onClick={poster.read} data-testid="wizard-poster-read-again">
                      {t("wizard.poster.read.again")}
                    </button>
                  </div>
                  <p className="field-hint">{t("wizard.poster.read.note")}</p>
                </>
              )}
              {view.read.phase === "failed" && (
                <div className="notice notice-bad" role="alert" data-testid="wizard-poster-read-failed" data-reason={view.read.reason}>
                  <p>{t(`wizard.poster.read.fail.${view.read.reason}` satisfies MessageKey)}</p>
                  <div className="actions">
                    {view.read.canRetry && (
                      <button type="button" className="btn" disabled={busy} onClick={poster.read} data-testid="wizard-poster-read-again">
                        {t("wizard.poster.read.again")}
                      </button>
                    )}
                    <button type="button" className="btn" onClick={() => poster.finishRead([])} data-testid="wizard-poster-read-manual">
                      {t("wizard.poster.read.manual")}
                    </button>
                  </div>
                  {view.read.canRetry && <p className="field-hint">{t("wizard.poster.read.note")}</p>}
                </div>
              )}
              {view.read.phase === "done" && (
                <div data-testid="wizard-poster-read-done" role="status">
                  <p className="notice">
                    {view.read.applied.length === 0
                      ? t("wizard.poster.read.doneNone")
                      : t("wizard.poster.read.doneSome", {
                          fields: view.read.applied.map((k) => t(FIELD_LABEL[k] ?? "wizard.poster.fld.name")).join(", "),
                        })}
                  </p>
                  <div className="actions">
                    <button type="button" className="btn" disabled={busy} onClick={poster.read} data-testid="wizard-poster-read-again">
                      {t("wizard.poster.read.again")}
                    </button>
                  </div>
                  <p className="field-hint">{t("wizard.poster.read.note")}</p>
                </div>
              )}
            </div>
          )}
        </>
      )}
    </fieldset>
  );
}
