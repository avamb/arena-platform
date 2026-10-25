/**
 * RT-12b (spec 44): the description of an event in the Telegram mini app. The
 * bot's "Edit in the editor" button under the description preview opens it.
 *
 * It saves with ONE PATCH of the event that carries `description` and
 * `description_format: "art"` and nothing else (saveDescription), so the name,
 * the poster, the dates, the categories and the translations are not touched.
 * The event-bundle the wizard edit uses would send the whole event back.
 *
 * The text comes from the events list, the route the letter text screen reads
 * too. A description kept in the old notation is converted the way every reader
 * converts it (`artOfStored`), never shown with its marks.
 */
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { RichTextField } from "@/components/RichTextField";
import { saveDescription } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import { artOfStored, overMarkupLimit, overVisibleLimit } from "@/lib/richText";

export interface TgDescriptionEditorProps {
  orgId: string;
  eventId: string;
  /** `description` and `description_format` of the event in the events list. */
  description: string | null | undefined;
  descriptionFormat: string | null | undefined;
  /** Called once after a Save went through and the list was read again. */
  onSaved: () => void;
}

export function TgDescriptionEditor({ orgId, eventId, description, descriptionFormat, onSaved }: TgDescriptionEditorProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const saved = artOfStored(description, descriptionFormat, "description");
  const [draft, setDraft] = useState(saved);
  // The stored text the draft was last in step with, and the draft Save last sent: the draft follows the stored
  // text while nothing was typed since (the list was read again), and after a Save takes the server's own form.
  const [synced, setSynced] = useState(saved);
  const [sent, setSent] = useState<string | null>(null);
  if (saved !== synced) {
    if (draft === synced || draft === sent) setDraft(saved);
    setSynced(saved);
  }
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);

  const dirty = draft !== saved;
  const tooLong = overVisibleLimit(draft, "description");
  const tooMuchMarkup = !tooLong && overMarkupLimit(draft, "description");
  const refused = tooLong || tooMuchMarkup;
  const problem = tooLong ? t("wizard.err.descriptionLong") : tooMuchMarkup ? t("wizard.err.descriptionMarkup") : undefined;

  const save = async () => {
    if (busy || refused || !dirty) return;
    setBusy(true);
    setFailure(null);
    try {
      await saveDescription(orgId, eventId, draft);
      setSent(draft);
      await queryClient.invalidateQueries({ queryKey: ["events", orgId] });
      onSaved();
    } catch (error) {
      setFailure({ error });
    } finally {
      setBusy(false);
    }
  };

  return (
    <section aria-labelledby="tg-description-title" data-testid="tg-description">
      <h2 id="tg-description-title">{t("tgEdit.description.title")}</h2>
      <RichTextField
        profile="description"
        orgId={orgId}
        label={t("wizard.f.description")}
        hint={t("wizard.f.descriptionHint")}
        error={problem}
        value={draft}
        onChange={setDraft}
        disabled={busy}
        testId="tg-description-field"
      />
      {dirty && (
        <p className="field-hint" data-testid="tg-description-unsaved">
          {t("tgEdit.unsaved")}
        </p>
      )}
      <div className="actions">
        <button type="button" className="btn btn-primary" disabled={busy || !dirty || refused} onClick={() => void save()} data-testid="tg-description-save">
          {busy ? t("tgEdit.saving") : t("tgEdit.save")}
        </button>
      </div>
      {failure !== null && <ErrorNotice error={failure.error} testId="tg-description-error" />}
    </section>
  );
}
