import { useEffect, useRef, useState } from "react";
import { useI18n } from "@/lib/i18n";
import { CHECK_EVERY_MS, MIN_GAP_MS, RUNNING_BUILD, fetchDeployedBuild, isNewBuild } from "@/lib/appVersion";

type Props = {
  /** Off outside a production build: a dev server has no /version.json. */
  enabled?: boolean;
  /** The build this window runs; tests pass their own. */
  running?: string;
  /** Reads the deployed build; tests pass a fake. */
  fetchDeployed?: () => Promise<string | null>;
  /** Reloads the window; tests pass a spy. */
  reload?: () => void;
};

/**
 * "A new version of the event center is available": shown when the server
 * names another build than the one this window runs. Asked on start, every
 * five minutes and whenever the window comes back to the front. The person
 * reloads with one press; "Later" hides it until the next deploy.
 */
export function NewVersionBanner({
  enabled = import.meta.env.PROD,
  running = RUNNING_BUILD,
  fetchDeployed = () => fetchDeployedBuild(),
  reload = () => window.location.reload(),
}: Props) {
  const { t } = useI18n();
  const [deployed, setDeployed] = useState<string | null>(null);
  const [hiddenFor, setHiddenFor] = useState<string | null>(null);
  const lastAsk = useRef(0);
  const fetchRef = useRef(fetchDeployed);
  fetchRef.current = fetchDeployed;

  useEffect(() => {
    if (!enabled) return;
    let alive = true;
    const ask = () => {
      const now = Date.now();
      if (lastAsk.current !== 0 && now - lastAsk.current < MIN_GAP_MS) return;
      lastAsk.current = now;
      void fetchRef.current().then((b) => {
        if (alive && b !== null) setDeployed(b);
      });
    };
    const onFront = () => {
      if (document.visibilityState === "visible") ask();
    };
    ask();
    const timer = window.setInterval(ask, CHECK_EVERY_MS);
    document.addEventListener("visibilitychange", onFront);
    window.addEventListener("focus", onFront);
    return () => {
      alive = false;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onFront);
      window.removeEventListener("focus", onFront);
    };
  }, [enabled]);

  if (!isNewBuild(running, deployed) || hiddenFor === deployed) return null;
  return (
    <aside className="new-version notice" role="status" aria-live="polite" data-testid="new-version">
      <p className="new-version-text">{t("update.available")}</p>
      <div className="new-version-actions">
        <button type="button" className="btn btn-primary" onClick={reload} data-testid="new-version-reload">
          {t("update.reload")}
        </button>
        <button type="button" className="btn" onClick={() => setHiddenFor(deployed)} data-testid="new-version-later">
          {t("update.later")}
        </button>
      </div>
    </aside>
  );
}
