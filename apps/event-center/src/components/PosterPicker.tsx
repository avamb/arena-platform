/**
 * The poster file picker shared by the wizard and the event card (task 7-1):
 * a button that opens the file dialog, the checked preview (never stretched:
 * the picture keeps its own proportions), a way to choose again or remove the
 * choice, and a spoken reason when a file is refused.
 *
 * The chosen file lives in a `Picker` (lib/posters.ts) whose preview address is
 * revoked when the choice changes or the screen goes away.
 */
import { useCallback, useEffect, useRef, useState } from "react";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import {
  POSTER_ACCEPT,
  POSTER_MAX_BYTES,
  createPicker,
  type Picker,
  type PickerDeps,
  type PickerState,
  type PosterRejection,
} from "@/lib/posters";

const EMPTY: PickerState = { phase: "empty" };

/**
 * One picker per `scope`: a new scope (another event, organization or draft)
 * disposes the old picker, so its preview is revoked and a slow check of the
 * old file can never fill the new scope.
 */
export function usePosterPicker(scope: string, deps?: PickerDeps) {
  const [state, setState] = useState<PickerState>(EMPTY);
  const ref = useRef<Picker | null>(null);
  useEffect(() => {
    const picker = createPicker(deps);
    ref.current = picker;
    setState(picker.state());
    const off = picker.subscribe(setState);
    return () => {
      off();
      picker.dispose();
      if (ref.current === picker) ref.current = null;
    };
    // `deps` is a test seam and constant for a screen.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope]);
  const select = useCallback((file: File) => void ref.current?.select(file), []);
  const clear = useCallback(() => ref.current?.clear(), []);
  return { state, select, clear };
}

export const POSTER_MAX_MB = POSTER_MAX_BYTES / (1024 * 1024);

const REJECT_KEY: Record<PosterRejection, MessageKey> = {
  empty: "poster.reject.empty",
  tooLarge: "poster.reject.tooLarge",
  notImage: "poster.reject.notImage",
  corrupt: "poster.reject.corrupt",
  tooBig: "poster.reject.tooBig",
};

export function PosterPicker({
  state,
  onSelect,
  onClear,
  busy,
  testId,
  label,
}: {
  state: PickerState;
  onSelect: (file: File) => void;
  onClear: () => void;
  /** True while the picture is being sent: the choice cannot change under the request. */
  busy: boolean;
  testId: string;
  /** The button's own text when nothing is chosen yet. */
  label: MessageKey;
}) {
  const { t } = useI18n();
  const input = useRef<HTMLInputElement>(null);
  const hintId = `${testId}-rules`;
  return (
    <div className="poster-picker" data-testid={testId} data-state={state.phase}>
      <input
        ref={input}
        type="file"
        hidden
        accept={POSTER_ACCEPT}
        data-testid={`${testId}-input`}
        onChange={(e) => {
          const file = e.currentTarget.files?.[0];
          // The same file can be chosen again after a refusal.
          e.currentTarget.value = "";
          if (file !== undefined) onSelect(file);
        }}
      />
      <div className="actions">
        <button
          type="button"
          className="btn"
          disabled={busy}
          aria-describedby={hintId}
          onClick={() => input.current?.click()}
          data-testid={`${testId}-choose`}
        >
          {t(state.phase === "empty" ? label : "poster.chooseAnother")}
        </button>
        {state.phase !== "empty" && (
          <button type="button" className="btn" disabled={busy} onClick={onClear} data-testid={`${testId}-remove`}>
            {t("poster.remove")}
          </button>
        )}
      </div>
      <p className="field-hint" id={hintId}>
        {t("poster.rules", { mb: POSTER_MAX_MB })}
      </p>
      {state.phase === "checking" && (
        <p role="status" data-testid={`${testId}-checking`}>
          {t("poster.checking")}
        </p>
      )}
      {state.phase === "rejected" && (
        <div className="notice notice-bad" role="alert" data-testid={`${testId}-rejected`} data-reason={state.reason}>
          <p>{t(REJECT_KEY[state.reason], { mb: POSTER_MAX_MB })}</p>
        </div>
      )}
      {state.phase === "ready" && (
        <img
          className="poster-img"
          src={state.previewUrl}
          width={state.info.width}
          height={state.info.height}
          alt={t("poster.preview.alt")}
          data-testid={`${testId}-preview`}
        />
      )}
    </div>
  );
}
