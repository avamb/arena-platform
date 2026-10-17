/**
 * The optional poster of the wizard (task 7-1): choose, preview, replace or
 * remove a picture BEFORE anything is sent. Nothing is uploaded here: the
 * picture travels when "Create event" is pressed (EventWizardScreen), and the
 * chosen file stays in memory (a reload forgets it, the draft keeps only the
 * id of an upload that already happened).
 */
import { PosterPicker } from "@/components/PosterPicker";
import { useI18n } from "@/lib/i18n";
import type { PickerState } from "@/lib/posters";

export function WizardPosterSection({
  state,
  onSelect,
  onClear,
  uploadedBefore,
  canChange,
  busy,
}: {
  state: PickerState;
  onSelect: (file: File) => void;
  onClear: () => void;
  /** The draft holds the id of a picture uploaded earlier and no file is chosen now. */
  uploadedBefore: boolean;
  canChange: boolean;
  busy: boolean;
}) {
  const { t } = useI18n();
  return (
    <fieldset className="org-form" data-testid="wizard-poster">
      <legend>{t("wizard.poster.title")}</legend>
      {!canChange ? (
        <p className="field-hint" data-testid="wizard-poster-no-permission">
          {t("wizard.poster.noPermission")}
        </p>
      ) : (
        <>
          <p className="field-hint">{t("wizard.poster.hint")}</p>
          {uploadedBefore && state.phase === "empty" && (
            <p className="notice" data-testid="wizard-poster-uploaded">
              {t("wizard.poster.uploaded")}
            </p>
          )}
          <PosterPicker state={state} onSelect={onSelect} onClear={onClear} busy={busy} testId="wizard-poster-picker" label="poster.choose" />
        </>
      )}
    </fieldset>
  );
}
