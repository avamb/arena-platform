/**
 * An API failure of the "Organization data" screen (4-7). It is the shared
 * ErrorNotice, plus the list of what is missing when the API answers
 * 422 `seller.incomplete` (`error.details.missing`, names such as
 * `tax_id` or `public_email`).
 */
import { ErrorNotice } from "@/components/ErrorNotice";
import { ApiError } from "@/lib/api/http";
import { describeApiError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { missingKey } from "@/lib/orgData";

/** The names of `details.missing` the web knows, as message keys. */
export function missingFromError(err: unknown): string[] {
  if (!(err instanceof ApiError) || err.code !== "seller.incomplete") return [];
  const raw = err.details?.missing;
  return Array.isArray(raw) ? raw.filter((x): x is string => typeof x === "string") : [];
}

export function SellerErrorNotice({ error, testId = "error-notice" }: { error: unknown; testId?: string }) {
  const { t } = useI18n();
  const missing = missingFromError(error)
    .map((name) => missingKey(name))
    .filter((k): k is NonNullable<ReturnType<typeof missingKey>> => k !== null);
  if (missing.length === 0) return <ErrorNotice error={error} testId={testId} />;
  const text = describeApiError(error, t);
  return (
    <div className="notice notice-bad" role="alert" data-testid={testId}>
      <p>
        <strong>{text.title}</strong>
      </p>
      <p>{text.next}</p>
      <ul className="plain-list" data-testid={`${testId}-missing`}>
        {missing.map((k) => (
          <li key={k}>{t(k)}</li>
        ))}
      </ul>
    </div>
  );
}
