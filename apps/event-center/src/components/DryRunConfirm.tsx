/**
 * "See what will change, then one press" (spec 37 and the bot's dry runs:
 * session change impact, event delete impact). The screen passes a dry run (a
 * read-only API call that returns numbers, warnings and a `blocked` reason) and
 * the action. The component asks the dry run on mount, shows the result and
 * offers ONE confirm button, only when the change is not blocked.
 *
 * It is not a modal: a screen places it where the question belongs. For a
 * change that needs a typed word too (a session cancel), the screen shows
 * ConfirmWordDialog after this press.
 */
import { useCallback, useEffect, useReducer, useRef, type ReactNode } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { canConfirm, initialDryRunState, isBlocked, reduceDryRun, type DryRunResult } from "@/lib/dryRun";
import { useI18n } from "@/lib/i18n";

export interface DryRunConfirmProps {
  /** A read-only call. It must not change anything. */
  dryRun: () => Promise<DryRunResult>;
  /** The change itself. Runs once per press. */
  onConfirm: () => void | Promise<void>;
  /** The text of the confirm button, a verb: «Move the session». */
  confirmLabel: string;
  /** A heading for the block. */
  title?: ReactNode;
  /** Change this to ask the dry run again (for example when the form changed). */
  reloadKey?: string | number;
}

export function DryRunConfirm({ dryRun, onConfirm, confirmLabel, title, reloadKey }: DryRunConfirmProps) {
  const { t } = useI18n();
  const [state, dispatch] = useReducer(reduceDryRun, initialDryRunState);
  const latest = useRef(0);
  const working = useRef(false);
  const dryRunRef = useRef(dryRun);
  dryRunRef.current = dryRun;

  const load = useCallback(() => {
    const mine = ++latest.current;
    dispatch({ type: "load" });
    dryRunRef.current().then(
      (result) => {
        if (mine === latest.current) dispatch({ type: "loaded", result });
      },
      (error: unknown) => {
        if (mine === latest.current) dispatch({ type: "loadFailed", error });
      },
    );
  }, []);

  useEffect(() => {
    load();
    return () => {
      // An answer that arrives after the screen left is dropped.
      latest.current++;
    };
  }, [load, reloadKey]);

  const press = async () => {
    if (working.current || !canConfirm(state)) return; // one press at a time
    working.current = true;
    dispatch({ type: "press" });
    try {
      await onConfirm();
      dispatch({ type: "succeeded" });
    } catch (error) {
      dispatch({ type: "actionFailed", error });
    } finally {
      working.current = false;
    }
  };

  return (
    <section className="dry-run" aria-busy={state.phase === "loading" || state.phase === "working"} data-testid="dry-run">
      {title !== undefined && <h3>{title}</h3>}

      {state.phase === "loading" && (
        <p className="muted" role="status" aria-live="polite" data-testid="dry-run-loading">
          {t("dryRun.loading")}
        </p>
      )}

      {state.phase === "failed" && (
        <>
          <ErrorNotice error={state.error} />
          <p>
            <button type="button" className="btn" onClick={load} data-testid="dry-run-retry">
              {t("dryRun.retry")}
            </button>
          </p>
        </>
      )}

      {(state.phase === "ready" || state.phase === "working" || state.phase === "done" || state.phase === "actionFailed") && (
        <DryRunBody result={state.result} />
      )}

      {state.phase === "actionFailed" && <ErrorNotice error={state.error} testId="dry-run-action-error" />}

      {state.phase === "done" && (
        <p className="notice notice-ok" role="status" data-testid="dry-run-done">
          {t("dryRun.done")}
        </p>
      )}

      {(state.phase === "ready" || state.phase === "working" || state.phase === "actionFailed") && !isBlocked(state.result) && (
        <p>
          <button
            type="button"
            className="btn btn-primary"
            disabled={state.phase === "working"}
            onClick={() => void press()}
            data-testid="dry-run-confirm"
          >
            {state.phase === "working" ? t("dryRun.working") : state.phase === "actionFailed" ? t("dryRun.tryAgain") : confirmLabel}
          </button>
        </p>
      )}
    </section>
  );
}

export function DryRunBody({ result }: { result: DryRunResult }) {
  const { t } = useI18n();
  const figures = result.figures ?? [];
  const warnings = result.warnings ?? [];
  const blocked = isBlocked(result);
  const empty = !blocked && result.summary === undefined && figures.length === 0 && warnings.length === 0;
  return (
    <div data-testid="dry-run-result">
      {blocked && (
        <div className="notice notice-bad" role="alert" data-testid="dry-run-blocked">
          <p>
            <strong>{t("dryRun.blockedTitle")}</strong>
          </p>
          <p>{result.blocked}</p>
        </div>
      )}
      {result.summary !== undefined && <p data-testid="dry-run-summary">{result.summary}</p>}
      {figures.length > 0 && (
        <dl className="dry-run-figures">
          {figures.map((f) => (
            <div key={f.label}>
              <dt>{f.label}</dt>
              <dd>{f.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {warnings.length > 0 && (
        <div className="notice" data-testid="dry-run-warnings">
          <p>
            <strong>{t("dryRun.warnings")}</strong>
          </p>
          <ul>
            {warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </div>
      )}
      {empty && <p className="muted">{t("dryRun.nothing")}</p>}
    </div>
  );
}
