/**
 * The password question before a money action (spec 37 section 4.6).
 * StepUpProvider mounts it once; a screen calls `useStepUp().ask()`, which
 * opens the question and resolves true when the password is confirmed and
 * false when the person cancels. Money calls go through `withStepUp`.
 */
import { createContext, useCallback, useContext, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { confirmPassword, stepUpErrorKey } from "@/lib/stepUp";
import { useI18n, type MessageKey } from "@/lib/i18n";

interface StepUpValue {
  ask: () => Promise<boolean>;
}

const Ctx = createContext<StepUpValue | null>(null);

export function StepUpProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<((ok: boolean) => void) | null>(null);

  const ask = useCallback(
    () =>
      new Promise<boolean>((resolve) => {
        setPending(() => resolve);
      }),
    [],
  );

  const close = (ok: boolean) => {
    pending?.(ok);
    setPending(null);
  };

  return (
    <Ctx.Provider value={{ ask }}>
      {children}
      {pending !== null && <StepUpDialog onDone={close} />}
    </Ctx.Provider>
  );
}

export function useStepUp(): StepUpValue {
  const v = useContext(Ctx);
  if (v === null) throw new Error("useStepUp needs StepUpProvider");
  return v;
}

function StepUpDialog({ onDone }: { onDone: (ok: boolean) => void }) {
  const { t } = useI18n();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<MessageKey | null>(null);
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    input.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onDone(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onDone]);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (password.length === 0) {
      setError("stepUp.error.empty");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await confirmPassword(password);
      setPassword("");
      onDone(true);
    } catch (err) {
      setError(stepUpErrorKey(err));
      setBusy(false);
    }
  };

  return (
    <div className="dialog-backdrop">
      <div className="dialog" role="dialog" aria-modal="true" aria-labelledby="step-up-title" data-testid="step-up">
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
