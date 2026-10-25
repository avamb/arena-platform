/**
 * RT-12b (spec 44): the description of an event in the Telegram mini app. The
 * bot's "Edit in the editor" button under the description preview opens it.
 *
 * It saves with ONE PATCH of the event that carries `description` and
 * `description_format: "art"` and nothing else (saveDescription), so the name,
 * the poster, the dates, the categories and the translations are not touched.
 * The event-bundle the wizard edit uses would send the whole event back.
 *
 * The text is the CANONICAL one (`description_base` of the events list): the
 * `description` of that list is resolved for a language and may be a
 * translation, and a translation saved over the event would be the original
 * lost. A description kept in the old notation is converted the way every
 * reader converts it (`artOfStored`), never shown with its marks. One that a
 * website wrote with literal HTML tags is NOT opened: the converter would show
 * the tags as words and a save would store them (`looksLikeSiteHtml`).
 */
import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { RichTextField } from "@/components/RichTextField";
import { ApiError } from "@/lib/api/http";
import { saveDescription } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import { artOfStored, looksLikeSiteHtml, overMarkupLimit, overVisibleLimit } from "@/lib/richText";

/** What the page needs to know to keep the window from being closed over unsaved or half-sent text. */
export interface TgWork {
  dirty: boolean;
  busy: boolean;
}

export interface TgDescriptionEditorProps {
  orgId: string;
  eventId: string;
  /** The canonical description of the event (`description_base` of the events list). */
  description: string | null | undefined;
  /** `description_format` of the event. */
  descriptionFormat: string | null | undefined;
  /** Called once after a Save went through and the list was read again. */
  onSaved: () => void;
  /** Called whenever the text is unsaved or a save is on its way. */
  onWork?: (work: TgWork) => void;
}

/** The language of a translation the server could not convert, when that is why a save was refused. */
export function refusedTranslationLocale(error: unknown): string | null {
  if (!(error instanceof ApiError) || error.code !== "event.invalid_description") return null;
  const locale = error.details?.locale;
  return typeof locale === "string" && locale !== "" ? locale : null;
}

export function TgDescriptionEditor({ orgId, eventId, description, descriptionFormat, onSaved, onWork }: TgDescriptionEditorProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const siteHtml = looksLikeSiteHtml(description, descriptionFormat);
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

  useEffect(() => {
    onWork?.({ dirty: !siteHtml && dirty, busy });
  }, [onWork, siteHtml, dirty, busy]);
  useEffect(() => () => onWork?.({ dirty: false, busy: false }), [onWork]);

  const save = async () => {
    if (busy || refused || !dirty || siteHtml) return;
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

  const translation = failure === null ? null : refusedTranslationLocale(failure.error);

  return (
    <section aria-labelledby="tg-description-title" data-testid="tg-description">
      <h2 id="tg-description-title">{t("tgEdit.description.title")}</h2>
      {siteHtml ? (
        <p className="notice" role="status" data-testid="tg-description-site-html">
          {t("tgEdit.description.siteHtml")}
        </p>
      ) : (
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
      )}
      {dirty && !siteHtml && (
        <p className="field-hint" data-testid="tg-description-unsaved">
          {t("tgEdit.unsaved")}
        </p>
      )}
      <div className="actions">
        <button
          type="button"
          className="btn btn-primary"
          disabled={busy || !dirty || refused || siteHtml}
          onClick={() => void save()}
          data-testid="tg-description-save"
        >
          {busy ? t("tgEdit.saving") : t("tgEdit.save")}
        </button>
      </div>
      {failure !== null &&
        (translation !== null ? (
          <div className="notice notice-bad" role="alert" data-testid="tg-description-error">
            <p>
              <strong>{t("tgEdit.description.translationRefused.title", { locale: translation })}</strong>
            </p>
            <p>{t("tgEdit.description.translationRefused.next", { locale: translation })}</p>
          </div>
        ) : (
          <ErrorNotice error={failure.error} testId="tg-description-error" />
        ))}
    </section>
  );
}
