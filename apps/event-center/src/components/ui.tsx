/**
 * Small building blocks shared by the screens. Accessibility rules
 * (spec section 9): a state is never colour only (a badge carries a word),
 * loading and errors are announced, every action is a real button or link.
 */
import { createContext, useContext, useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { errorKey } from "@/lib/errors";
import type { DownloadedFile } from "@/lib/api/http";
import type { Tone } from "@/lib/orders";

export function Loading({ label }: { label?: string }) {
  const { t } = useI18n();
  return (
    <p className="muted" role="status" aria-live="polite" data-testid="loading">
      {label ?? t("common.loading")}
    </p>
  );
}

/**
 * The organization a screen works in, for the 403 texts ("no access in
 * {org}") and their way back (spec 5.6). OrgLayout provides it.
 */
export const ErrorScope = createContext<{ orgId: string; orgName: string } | null>(null);

export function ErrorBox({
  error,
  onRetry,
  params,
}: {
  error: unknown;
  onRetry?: () => void;
  params?: Record<string, string>;
}) {
  const { t } = useI18n();
  const scope = useContext(ErrorScope);
  const key = errorKey(error);
  const forbidden = key === "common.error.forbidden";
  return (
    <div className="notice notice-bad" role="alert" data-testid="error">
      <p>{t(forbidden && scope === null ? "common.error.forbiddenHere" : key, { org: scope?.orgName ?? "", ...params })}</p>
      {forbidden && scope !== null ? (
        <p>
          <Link to="/o/$orgId/events" params={{ orgId: scope.orgId }} data-testid="back-to-list">
            {t("common.backToList")}
          </Link>
        </p>
      ) : (
        onRetry !== undefined && (
          <button type="button" className="btn" onClick={onRetry}>
            {t("common.retry")}
          </button>
        )
      )}
    </div>
  );
}

export function Badge({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <span className={`badge badge-${tone}`}>{children}</span>;
}

export function Empty({ children }: { children: ReactNode }) {
  return (
    <p className="muted empty" role="status">
      {children}
    </p>
  );
}

/** Hands a downloaded file to the browser. */
export function saveFile(file: DownloadedFile): void {
  const url = URL.createObjectURL(file.blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = file.filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/**
 * A button that downloads a file and says what happened, out loud for a
 * screen reader too.
 */
export function DownloadButton({
  label,
  fetchFile,
  testId,
}: {
  label: MessageKey;
  fetchFile: () => Promise<DownloadedFile>;
  testId?: string;
}) {
  const { t } = useI18n();
  const scope = useContext(ErrorScope);
  const [state, setState] = useState<{ busy: boolean; message: string | null; bad: boolean }>({
    busy: false,
    message: null,
    bad: false,
  });
  const onClick = async () => {
    setState({ busy: true, message: t("common.download.working"), bad: false });
    try {
      const file = await fetchFile();
      saveFile(file);
      setState({ busy: false, message: t("common.download.done", { name: file.filename }), bad: false });
    } catch (err) {
      const key = errorKey(err);
      const shown = key === "common.error.generic" ? "common.download.failed" : key === "common.error.forbidden" && scope === null ? "common.error.forbiddenHere" : key;
      setState({
        busy: false,
        message: t(shown, { org: scope?.orgName ?? "" }),
        bad: true,
      });
    }
  };
  return (
    <span className="download">
      <button type="button" className="btn" onClick={onClick} disabled={state.busy} data-testid={testId}>
        {t(label)}
      </button>
      <span className={state.bad ? "bad-text" : "muted"} role={state.bad ? "alert" : "status"} aria-live="polite">
        {state.message ?? ""}
      </span>
    </span>
  );
}

export function Stat({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="stat">
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}
