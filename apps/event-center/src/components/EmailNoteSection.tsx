/**
 * RT-11 (spec 44): "Text in the letter" on the event card. The organizer's own
 * text goes into the ticket letter and the letter about a change, under
 * "About the event". It is the web twin of the bot's screen (`tf:n|e|d|dy|t`,
 * eventbot/textfmt_note.go): the editor with the profile `email_note`, Save
 * (ONE PATCH of the event with `email_note` and `email_note_format: "art"`),
 * Delete (after a question) and "Test letter to myself" (the draft goes to the
 * caller's own address).
 *
 * The screen says plainly that the letter adds the greeting, the date and the
 * venue itself: Arena has no `{name}` or `{date}` substitutions (owner decision
 * 2026-10-24, spec 44 section 9.3) and nothing here may suggest them.
 *
 * The events list is the only route that carries the text (the single-event
 * route does not hydrate it), so after a write the list is read again.
 */
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { ErrorNotice } from "@/components/ErrorNotice";
import { RichTextField } from "@/components/RichTextField";
import { saveEmailNote, sendEmailNoteTest } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import { artOfStored, overMarkupLimit, overVisibleLimit } from "@/lib/richText";

export interface EmailNoteSectionProps {
  orgId: string;
  eventId: string;
  /** `email_note` and `email_note_format` of the event in the events list. */
  note: string | null | undefined;
  noteFormat: string | null | undefined;
  /** `event.update`: the one right the API asks for the save and the test letter. */
  canEdit: boolean;
  /** Called once after a Save went through and the list was read again (the Telegram mini app closes itself then). */
  onSaved?: () => void;
}

type Action = "save" | "test" | "delete";

export function EmailNoteSection({ orgId, eventId, note, noteFormat, canEdit, onSaved }: EmailNoteSectionProps) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const saved = artOfStored(note, noteFormat, "email_note");
  const [draft, setDraft] = useState(saved);
  // The saved text the draft was last in step with, and the draft that was last sent by Save. The draft
  // follows the saved text while the person has typed nothing since (the bot or another tab changed
  // it, so Save cannot overwrite their text unseen), and after a Save it takes the server's canonical
  // form, so the "not saved" mark goes even when the server wrote the ART a byte differently.
  const [synced, setSynced] = useState(saved);
  const [sent, setSent] = useState<string | null>(null);
  if (saved !== synced) {
    if (draft === synced || draft === sent) setDraft(saved);
    setSynced(saved);
  }
  const [busy, setBusy] = useState<Action | null>(null);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const [asking, setAsking] = useState(false);

  const dirty = draft !== saved;
  const tooLong = overVisibleLimit(draft, "email_note") || overMarkupLimit(draft, "email_note");
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["events", orgId] });

  const run = async (action: Action, work: () => Promise<string>) => {
    if (busy !== null) return;
    setBusy(action);
    setFailure(null);
    setStatus(null);
    try {
      setStatus(await work());
    } catch (error) {
      setFailure({ error });
    } finally {
      setBusy(null);
    }
  };

  const save = () =>
    run("save", async () => {
      await saveEmailNote(orgId, eventId, draft);
      setSent(draft);
      await refresh();
      onSaved?.();
      return t("emailNote.saved");
    });

  const test = () =>
    run("test", async () => {
      // The letter shows what is in the editor, always: an empty editor is an empty draft (the server
      // answers "no text to send"), never the saved text the person may have just cleared.
      const answer = await sendEmailNoteTest(orgId, eventId, { note: draft, note_format: "art", locale });
      return t("emailNote.test.sent", { email: answer.to });
    });

  const remove = async () => {
    // Busy while the PATCH runs: nothing can be typed that the delete would then wipe out. A failure
    // propagates to the dialog, which stays open and says what to do.
    if (busy !== null) return;
    setBusy("delete");
    try {
      await saveEmailNote(orgId, eventId, null);
      await refresh();
      setDraft("");
      setSynced("");
      setAsking(false);
      setStatus(t("emailNote.deleted"));
    } finally {
      setBusy(null);
    }
  };

  return (
    <section aria-labelledby="email-note-title" data-testid="email-note">
      <h2 id="email-note-title">{t("emailNote.title")}</h2>
      <p>{t("emailNote.intro")}</p>
      {saved === "" && !dirty && (
        <p className="muted" data-testid="email-note-none">
          {t("emailNote.none")}
        </p>
      )}
      <RichTextField
        profile="email_note"
        orgId={canEdit ? orgId : undefined}
        label={t("emailNote.label")}
        hint={canEdit ? t("emailNote.hint") : t("emailNote.readOnly")}
        error={tooLong ? t("emailNote.tooLong") : undefined}
        value={draft}
        onChange={(art) => {
          setDraft(art);
          setStatus(null);
        }}
        disabled={!canEdit || busy !== null}
        testId="email-note-field"
      />
      {canEdit && (
        <>
          {dirty && (
            <p className="field-hint" data-testid="email-note-unsaved">
              {t("emailNote.unsaved")}
            </p>
          )}
          <div className="actions">
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy !== null || !dirty || draft === "" || tooLong}
              onClick={() => void save()}
              data-testid="email-note-save"
            >
              {busy === "save" ? t("emailNote.saving") : t("emailNote.save")}
            </button>
            <button type="button" className="btn" disabled={busy !== null || tooLong} onClick={() => void test()} data-testid="email-note-test">
              {t("emailNote.test")}
            </button>
            {saved !== "" && (
              <button type="button" className="btn" disabled={busy !== null} onClick={() => setAsking(true)} data-testid="email-note-delete">
                {t("emailNote.delete")}
              </button>
            )}
          </div>
          <p className="field-hint">{t("emailNote.test.hint")}</p>
        </>
      )}
      {status !== null && (
        <p role="status" className="notice" data-testid="email-note-status">
          {status}
        </p>
      )}
      {failure !== null && <ErrorNotice error={failure.error} testId="email-note-error" />}
      {asking && (
        <ConfirmPressDialog
          title={t("emailNote.delete.title")}
          explain={t("emailNote.delete.explain")}
          confirmLabel={t("emailNote.delete.confirm")}
          testId="email-note-delete-dialog"
          onConfirm={remove}
          onCancel={() => setAsking(false)}
        />
      )}
    </section>
  );
}
