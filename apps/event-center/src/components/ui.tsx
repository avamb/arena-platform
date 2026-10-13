/**
 * Small building blocks shared by the screens. Accessibility rules
 * (spec section 9): a state is never colour only (a badge carries a word),
 * loading and errors are announced, every action is a real button or link.
 */
import { useState, type ReactNode } from "react";
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
  return (
    <div className="notice notice-bad" role="alert" data-testid="error">
      <p>{t(errorKey(error), params)}</p>
      {onRetry !== undefined && (
        <button type="button" className="btn" onClick={onRetry}>
          {t("common.retry")}
        </button>
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
      setState({
        busy: false,
        message: t(key === "common.error.generic" ? "common.download.failed" : key),
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
