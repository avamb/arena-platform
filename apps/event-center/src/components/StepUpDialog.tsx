/**
 * The password question before a money action (spec 37 section 4.6).
 * StepUpProvider mounts it once; a screen calls `useStepUp().ask(orgId)`,
 * which opens the question and resolves true when the password is confirmed
 * for that organization and false when the person cancels. Money calls go
 * through `withStepUp`.
 *
 * Two asks while the question is open share it (StepUpQuestion): nobody is
 * left waiting. The dialog keeps the keyboard inside itself (Tab and
 * Shift+Tab go round its controls) and gives the focus back to the control
 * that was focused before it opened.
 */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import { StepUpQuestion, confirmPassword, stepUpErrorKey, trapFocusIndex } from "@/lib/stepUp";
import { useI18n, type MessageKey } from "@/lib/i18n";

interface StepUpValue {
  ask: (orgId: string) => Promise<boolean>;
}

const Ctx = createContext<StepUpValue | null>(null);

export function StepUpProvider({ children }: { children: ReactNode }) {
  const [openOrg, setOpenOrg] = useState<string | null>(null);
  const question = useMemo(() => new StepUpQuestion(setOpenOrg), []);
  const ask = useCallback((orgId: string) => question.ask(orgId), [question]);
  const value = useMemo(() => ({ ask }), [ask]);

  return (
    <Ctx.Provider value={value}>
      {children}
      {openOrg !== null && <StepUpDialog orgId={openOrg} onDone={(ok) => question.answer(ok)} />}
    </Ctx.Provider>
  );
}

export function useStepUp(): StepUpValue {
  const v = useContext(Ctx);
  if (v === null) throw new Error("useStepUp needs StepUpProvider");
  return v;
}

const FOCUSABLE = 'button:not([disabled]), input:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])';

function StepUpDialog({ orgId, onDone }: { orgId: string; onDone: (ok: boolean) => void }) {
  const { t } = useI18n();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<MessageKey | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const box = useRef<HTMLDivElement>(null);
  const done = useRef(onDone);
  done.current = onDone;

  useEffect(() => {
    // The control that opened the question gets the focus back afterwards.
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    input.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") done.current(false);
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      if (before !== null && before.isConnected) before.focus();
    };
  }, []);

  const keepFocusInside = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "Tab" || box.current === null) return;
    const items = Array.from(box.current.querySelectorAll<HTMLElement>(FOCUSABLE));
    const next = trapFocusIndex(items.length, items.indexOf(document.activeElement as HTMLElement), e.shiftKey);
    if (next < 0) return;
    e.preventDefault();
    items[next].focus();
  };

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (password.length === 0) {
      setError("stepUp.error.empty");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await confirmPassword(password, orgId);
      setPassword("");
      onDone(true);
    } catch (err) {
      setError(stepUpErrorKey(err));
      setBusy(false);
    }
  };

  return (
    <div className="dialog-backdrop">
      <div
        ref={box}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="step-up-title"
        data-testid="step-up"
        onKeyDown={keepFocusInside}
      >
        <h2 id="step-up-title">{t("stepUp.title")}</h2>
        <p>{t("stepUp.intro")}</p>
        <form onSubmit={onSubmit} noValidate>
          <label htmlFor="step-up-password">{t("stepUp.password")}</label>
          <input
            ref={input}
            id="step-up-password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            aria-describedby={error !== null ? "step-up-hint step-up-error" : "step-up-hint"}
          />
          <p id="step-up-hint" className="muted">
            {t("stepUp.hint")}
          </p>
          {error !== null && (
            <p id="step-up-error" className="notice notice-bad" role="alert">
              {t(error)}
            </p>
          )}
          <div className="dialog-actions">
            <button type="submit" className="btn btn-primary" disabled={busy}>
              {busy ? t("stepUp.working") : t("stepUp.submit")}
            </button>
            <button type="button" className="btn" onClick={() => onDone(false)}>
              {t("stepUp.cancel")}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
