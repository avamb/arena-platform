/**
 * 5-2: the text buyers read in the letter about a moved or canceled session.
 * It starts as the text the dry run suggests (in the person's language) and
 * the person may edit or clear it. RT-11 (spec 44): the field is the editor of
 * the library with the profile `session_message` (bold, italic, links to
 * https, lists; 1000 visible characters, 3 links), and the text goes to the API
 * as ART with `message_format: "art"`. The count follows the API's rule (the
 * visible text, not the HTML); a longer text is refused here before it is sent.
 * The platform never promises a refund in this text: the hint says so.
 */
import { RichTextField } from "@/components/RichTextField";
import { useI18n } from "@/lib/i18n";
import { MAX_NOTICE_CHARS, noticeTooLong, suggestedArt, type MessageState } from "@/lib/sessionChange";

export interface SessionChangeMessageProps {
  /** The organization: turns the editor's "Format" button on. */
  orgId?: string;
  state: MessageState;
  /** The text the dry run suggests, plain text (`default_message`). */
  suggested: string;
  /** Called with the new text as ART. */
  onType: (art: string) => void;
  onReset: () => void;
  disabled?: boolean;
}

export function SessionChangeMessage({ orgId, state, suggested, onType, onReset, disabled = false }: SessionChangeMessageProps) {
  const { t } = useI18n();
  const tooLong = noticeTooLong(state.text);
  const suggestion = suggestedArt(suggested);
  return (
    <div className="session-change-message" data-testid="session-change-message-box">
      <RichTextField
        profile="session_message"
        orgId={orgId}
        label={t("sessionChange.message.label")}
        hint={t("sessionChange.message.hint")}
        error={tooLong ? t("sessionChange.message.tooLong", { max: MAX_NOTICE_CHARS }) : undefined}
        value={state.text}
        onChange={onType}
        disabled={disabled}
        testId="session-change-message"
      />
      {state.text !== suggestion && suggestion !== "" && (
        <p>
          <button type="button" className="btn" disabled={disabled} onClick={onReset} data-testid="session-change-message-reset">
            {t("sessionChange.message.reset")}
          </button>
        </p>
      )}
    </div>
  );
}
