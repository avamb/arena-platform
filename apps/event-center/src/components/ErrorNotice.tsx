/**
 * An API failure as a title and the next step (describeApiError). Announced
 * to a screen reader; the title is bold so the state is not colour only.
 */
import { describeApiError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

export function ErrorNotice({ error, testId = "error-notice" }: { error: unknown; testId?: string }) {
  const { t } = useI18n();
  const text = describeApiError(error, t);
  return (
    <div className="notice notice-bad" role="alert" data-testid={testId}>
      <p>
        <strong>{text.title}</strong>
      </p>
      <p>{text.next}</p>
    </div>
  );
}
