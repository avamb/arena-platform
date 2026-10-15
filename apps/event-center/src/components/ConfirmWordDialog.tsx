/**
 * "Type the word to confirm": the last step before an action that cannot be
 * undone (spec 37, the bot's typed words). The word is the bot's own word for
 * that action (src/lib/confirmWords.ts), so a person who knows one channel
 * knows the other. A wrong or empty text changes nothing and says so.
 *
 * The caller closes the dialog: `onConfirm` resolves, the caller unmounts it.
 * When `onConfirm` throws, the dialog stays and shows what to do next.
 */
import { useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { useDialogBehaviour } from "@/components/useDialogBehaviour";
import { attemptConfirm, confirmWord, type ConfirmKind } from "@/lib/confirmWords";
import { useI18n, type MessageKey } from "@/lib/i18n";

export interface ConfirmWordDialogProps {
  kind: ConfirmKind;
  /** What the action is about: the order number, the session date, the event name. */
  subject?: ReactNode;
  onConfirm: () => void | Promise<void>;
  onCancel: () => void;
}

export function ConfirmWordDialog({ kind, subject, onConfirm, onCancel }: ConfirmWordDialogProps) {
  const { t, locale } = useI18n();
  const ids = useId();
  const [typed, setTyped] = useState("");
  const [note, setNote] = useState<MessageKey | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const pressed = useRef(false);
  const box = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const { onKeyDown } = useDialogBehaviour(box, input, () => {
    if (!pressed.current) onCancel();
  });

  const word = confirmWord(kind, locale);
  const button = t(`confirmWord.${kind}.button` as MessageKey);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (pressed.current) return; // one press at a time
    const attempt = attemptConfirm(kind, locale, typed);
    if (attempt !== "ok") {
      setNote(attempt === "empty" ? "confirmWord.empty" : "confirmWord.mismatch");
      return;
    }
    pressed.current = true;
    setNote(null);
    setFailure(null);
    setBusy(true);
    try {
      await onConfirm();
    } catch (error) {
      setFailure({ error });
    } finally {
      pressed.current = false;
      setBusy(false);
    }
  };

  return (
    <div className="dialog-backdrop">
      <div
        ref={box}
        className="dialog"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={`${ids}-title`}
        aria-describedby={`${ids}-explain`}
        data-testid="confirm-word"
        onKeyDown={onKeyDown}
      >
        <h2 id={`${ids}-title`}>{t(`confirmWord.${kind}.title` as MessageKey)}</h2>
        {subject !== undefined && (
          <p>
            <strong data-testid="confirm-word-subject">{subject}</strong>
          </p>
        )}
        <p id={`${ids}-explain`}>{t(`confirmWord.${kind}.explain` as MessageKey)}</p>
        <form onSubmit={onSubmit} noValidate>
          <p>{t("confirmWord.prompt", { word, button })}</p>
          <label htmlFor={`${ids}-input`}>{t("confirmWord.type")}</label>
          <input
            ref={input}
            id={`${ids}-input`}
            type="text"
            autoComplete="off"
            autoCapitalize="characters"
            spellCheck={false}
            value={typed}
            disabled={busy}
            onChange={(e) => setTyped(e.target.value)}
            aria-describedby={note !== null ? `${ids}-note` : undefined}
          />
          {note !== null && (
            <p id={`${ids}-note`} className="notice notice-bad" role="alert" data-testid="confirm-word-note">
              {t(note, { word })}
            </p>
          )}
          {failure !== null && <ErrorNotice error={failure.error} />}
          <div className="dialog-actions">
            <button type="submit" className="btn btn-primary" disabled={busy} data-testid="confirm-word-submit">
              {busy ? t("confirmWord.working") : button}
            </button>
            <button type="button" className="btn" disabled={busy} onClick={onCancel} data-testid="confirm-word-cancel">
              {t("confirmWord.cancel")}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
