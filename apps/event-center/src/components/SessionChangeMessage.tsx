/**
 * 5-2: the text buyers read in the letter about a moved or canceled session.
 * It starts as the text the dry run suggests (in the person's language) and
 * the person may edit or clear it. The count follows the API's rule (code
 * points, at most MAX_NOTICE_CHARS); a longer text is refused here before it
 * is sent. The platform never promises a refund in this text: the hint says so.
 */
import { useId } from "react";
import { useI18n } from "@/lib/i18n";
import { MAX_NOTICE_CHARS, noticeLength, noticeTooLong, type MessageState } from "@/lib/sessionChange";

export interface SessionChangeMessageProps {
  state: MessageState;
  suggested: string;
  onType: (text: string) => void;
  onReset: () => void;
  disabled?: boolean;
}

export function SessionChangeMessage({ state, suggested, onType, onReset, disabled = false }: SessionChangeMessageProps) {
  const { t } = useI18n();
  const id = useId();
  const tooLong = noticeTooLong(state.text);
  return (
    <div className="session-change-message" data-testid="session-change-message-box">
      <label htmlFor={`${id}-text`}>{t("sessionChange.message.label")}</label>
      <textarea
        id={`${id}-text`}
        rows={5}
        value={state.text}
        disabled={disabled}
        aria-invalid={tooLong ? true : undefined}
        aria-describedby={`${id}-hint`}
        data-testid="session-change-message"
        onChange={(e) => onType(e.target.value)}
      />
      <p id={`${id}-hint`} className="field-hint">
        {t("sessionChange.message.hint", { count: noticeLength(state.text), max: MAX_NOTICE_CHARS })}
      </p>
      {tooLong && (
        <p className="field-error" role="alert" data-testid="session-change-message-error">
          {t("sessionChange.message.tooLong", { max: MAX_NOTICE_CHARS })}
        </p>
      )}
      {state.text !== suggested && suggested !== "" && (
        <p>
          <button type="button" className="btn" disabled={disabled} onClick={onReset} data-testid="session-change-message-reset">
            {t("sessionChange.message.reset")}
          </button>
        </p>
      )}
    </div>
  );
}
