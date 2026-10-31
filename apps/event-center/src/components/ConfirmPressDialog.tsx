/**
 * A question with ONE confirm button and no typed word, for an action that is
 * serious but can be repaired (4-5: remove a team member, revoke an
 * invitation: the bot asks the same way, `team:rm:<id>:yes`). Actions that
 * cannot be undone use ConfirmWordDialog instead.
 *
 * The caller closes the dialog: `onConfirm` resolves, the caller unmounts it.
 * When `onConfirm` throws, the dialog stays and shows what to do next.
 */
import { useId, useRef, useState, type ReactNode } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { useDialogBehaviour } from "@/components/useDialogBehaviour";
import { useI18n } from "@/lib/i18n";

export interface ConfirmPressDialogProps {
  title: string;
  /** Who or what the action is about: an e-mail address. */
  subject?: ReactNode;
  /** What happens, in plain words. */
  explain: string;
  confirmLabel: string;
  testId?: string;
  /** Extra controls between the explanation and the buttons (a checkbox). */
  children?: ReactNode;
  onConfirm: () => void | Promise<void>;
  onCancel: () => void;
}

export function ConfirmPressDialog({
  title,
  subject,
  explain,
  confirmLabel,
  testId = "confirm-press",
  children,
  onConfirm,
  onCancel,
}: ConfirmPressDialogProps) {
  const { t } = useI18n();
  const ids = useId();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const pressed = useRef(false);
  const box = useRef<HTMLDivElement>(null);
  const cancelButton = useRef<HTMLButtonElement>(null);
  // The focus starts on "Cancel": Enter must never remove somebody by habit.
  const { onKeyDown } = useDialogBehaviour(box, cancelButton, () => {
    if (!pressed.current) onCancel();
  });

  const press = async () => {
    if (pressed.current) return; // one press at a time
    pressed.current = true;
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
        data-testid={testId}
        onKeyDown={onKeyDown}
      >
        <h2 id={`${ids}-title`}>{title}</h2>
        {subject !== undefined && (
          <p>
            <strong data-testid={`${testId}-subject`}>{subject}</strong>
          </p>
        )}
        <p id={`${ids}-explain`}>{explain}</p>
        {children}
        {failure !== null && <ErrorNotice error={failure.error} />}
        <div className="dialog-actions">
          <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void press()} data-testid={`${testId}-submit`}>
            {busy ? t("confirmWord.working") : confirmLabel}
          </button>
          <button ref={cancelButton} type="button" className="btn" disabled={busy} onClick={onCancel} data-testid={`${testId}-cancel`}>
            {t("confirmWord.cancel")}
          </button>
        </div>
      </div>
    </div>
  );
}
