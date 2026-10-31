/**
 * The ticket letter as a scale of three points, sent, delivered, opened, with
 * the time under each point (owner decision 2026-10-30, the WordPress ops
 * center's stepper). The scale is a picture of the status sentence: the
 * sentence itself stays in OrderLetters for screen readers and for every state
 * that needs words.
 */
import type { LetterStatus, LetterStep } from "@/lib/letters";
import { formatShortDateTime } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";

export function LetterSteps({ steps, status, tz }: { steps: readonly LetterStep[]; status: LetterStatus; tz: string }) {
  const { t, locale } = useI18n();
  const reached = steps.filter((s) => s.mark === "done").length;
  const partial = status.state === "partial" || status.state === "partial_queued";
  return (
    <div className="steps" aria-hidden="true" data-testid="letter-steps" data-reached={reached}>
      <span className="steps-line" />
      <span className={`steps-line steps-line-done steps-done-${reached}`} />
      <ol className="steps-list">
        {steps.map((s, i) => (
          <li key={s.key} className={`step step-${s.mark}`}>
            <span className="step-dot" />
            <span className="step-label">{t(`letter.step.${s.key}` as MessageKey)}</span>
            <span className="step-time">
              {i === 0 && partial
                ? t("letter.step.partial", { done: status.done, total: status.total })
                : formatShortDateTime(s.at, tz, locale)}
            </span>
          </li>
        ))}
      </ol>
    </div>
  );
}
