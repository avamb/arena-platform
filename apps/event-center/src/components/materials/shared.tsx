/**
 * Small parts shared by the sales page materials editors: the one-action-at-
 * a-time runner, its notice (an error is NEVER shown as a saved change), the
 * move buttons and the picture preview.
 */
import { useCallback, useRef, useState } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { usePosterImage } from "@/components/EventPoster";
import { useI18n } from "@/lib/i18n";
import { isLimitError } from "@/lib/materials";

export interface ActionState {
  busy: boolean;
  error: unknown;
  ok: boolean;
  run: (fn: () => Promise<void>) => Promise<boolean>;
  reset: () => void;
}

/** Runs one action at a time. `ok` is true only after the action finished without an error. */
export function useAction(): ActionState {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [ok, setOk] = useState(false);
  const running = useRef(false);
  const run = useCallback(async (fn: () => Promise<void>): Promise<boolean> => {
    if (running.current) return false;
    running.current = true;
    setBusy(true);
    setError(null);
    setOk(false);
    try {
      await fn();
      setOk(true);
      return true;
    } catch (err) {
      setError(err);
      return false;
    } finally {
      running.current = false;
      setBusy(false);
    }
  }, []);
  const reset = useCallback(() => {
    setError(null);
    setOk(false);
  }, []);
  return { busy, error, ok, run, reset };
}

export function ActionNotice({ action, max, testId }: { action: ActionState; max?: number; testId: string }) {
  const { t } = useI18n();
  if (action.busy) {
    return (
      <p className="field-hint" role="status" data-testid={`${testId}-busy`}>
        {t("mat.working")}
      </p>
    );
  }
  if (action.error !== null) {
    if (max !== undefined && isLimitError(action.error)) {
      return (
        <div className="notice notice-bad" role="alert" data-testid={`${testId}-error`}>
          <p>{t("mat.fail.limit", { max })}</p>
        </div>
      );
    }
    return <ErrorNotice error={action.error} testId={`${testId}-error`} />;
  }
  if (action.ok) {
    return (
      <p className="notice notice-ok" role="status" data-testid={`${testId}-ok`}>
        {t("mat.saved")}
      </p>
    );
  }
  return null;
}

export function MoveButtons({
  index,
  count,
  disabled,
  onMove,
  testId,
}: {
  index: number;
  count: number;
  disabled: boolean;
  onMove: (delta: -1 | 1) => void;
  testId: string;
}) {
  const { t } = useI18n();
  return (
    <span className="actions">
      <button type="button" className="btn" disabled={disabled || index === 0} onClick={() => onMove(-1)} data-testid={`${testId}-up`}>
        {t("mat.moveUp")}
      </button>
      <button
        type="button"
        className="btn"
        disabled={disabled || index === count - 1}
        onClick={() => onMove(1)}
        data-testid={`${testId}-down`}
      >
        {t("mat.moveDown")}
      </button>
    </span>
  );
}

/** The picture of a stored media id (needs `media.read`; the signed address is used once). */
export function PhotoThumb({ mediaId, canView, alt }: { mediaId: string; canView: boolean; alt: string }) {
  const { t } = useI18n();
  const image = usePosterImage(mediaId, canView);
  if (!canView) return <p className="field-hint">{t("mat.photo.thumbNoRight")}</p>;
  if (image.state.phase === "ok") {
    return <img className="mat-thumb" src={image.state.url} alt={alt} loading="lazy" />;
  }
  if (image.state.phase === "loading") return <span className="mat-thumb mat-thumb-empty" aria-hidden="true" />;
  return (
    <p className="field-hint" role="status">
      {t("mat.photo.thumbFailed")}
    </p>
  );
}
