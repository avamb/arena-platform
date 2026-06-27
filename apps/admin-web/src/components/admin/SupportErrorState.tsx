/**
 * Shared error-state surface for SuperAdmin support consoles (SAUI-10).
 *
 * The three support consoles (/orders, /tickets, /refunds) handle the
 * same four classes of failure from /v1/admin/{entity}:
 *
 *   - 400 + `superadmin.reason_required` / `superadmin.missing_reason`
 *     -> re-prompt for X-Admin-Reason, allow retry once the modal is
 *        re-submitted;
 *   - 403 or `permissions.denied` -> caller lacks superadmin.read,
 *     advise asking a platform admin;
 *   - 401 -> session expired, re-login flow handled by AuthProvider;
 *   - everything else -> generic error with code+message+retry button.
 *
 * Render the same surface as the SAUI-06 organizations explorer so
 * operators see one consistent recovery affordance across the entire
 * SuperAdmin shell.
 */
import { ApiError } from "@/lib/api/client";
import {
  errorBoxStyle,
  errorCodeStyle,
  errorParaStyle,
  errorRetryStyle,
} from "@/lib/admin/supportStyles";

export interface SupportErrorStateProps {
  /** A `data-testid` prefix so each consumer's tests can target it. */
  readonly testIdPrefix: string;
  readonly error: ApiError | Error | null;
  readonly onRetry: () => void;
}

export function SupportErrorState({
  testIdPrefix,
  error,
  onRetry,
}: SupportErrorStateProps): JSX.Element {
  if (
    error instanceof ApiError &&
    (error.code === "superadmin.reason_required" ||
      error.code === "superadmin.missing_reason")
  ) {
    return (
      <div
        style={errorBoxStyle}
        role="status"
        data-testid={`${testIdPrefix}-reason-required`}
      >
        <strong>Audit reason required.</strong>
        <p style={errorParaStyle}>
          Cross-tenant reads require an <code>X-Admin-Reason</code>. Submit
          a reason in the prompt at the top of the screen and then retry.
        </p>
        <button type="button" style={errorRetryStyle} onClick={onRetry}>
          Retry
        </button>
      </div>
    );
  }
  if (
    error instanceof ApiError &&
    (error.status === 403 || error.code === "permissions.denied")
  ) {
    return (
      <div
        style={errorBoxStyle}
        role="alert"
        data-testid={`${testIdPrefix}-forbidden`}
      >
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code>superadmin.read</code>. Ask a
          platform administrator to grant the permission.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div
        style={errorBoxStyle}
        role="status"
        data-testid={`${testIdPrefix}-session-expired`}
      >
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload the data.</p>
      </div>
    );
  }
  const code =
    error instanceof ApiError ? error.code : error?.name ?? "unknown.error";
  const message = error?.message ?? "";
  return (
    <div style={errorBoxStyle} role="alert" data-testid={`${testIdPrefix}-error`}>
      <strong>Failed to load.</strong>
      <div style={errorCodeStyle}>{code}</div>
      {message !== "" ? <div style={errorParaStyle}>{message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}
