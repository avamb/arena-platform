import { useEffect, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useAuth } from "@/lib/auth";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { closeMiniApp, enterMiniApp, loadTelegramScript, tgEditTarget } from "@/lib/tgEdit";

export type TgEditState = { phase: "working" } | { phase: "ready" } | { phase: "error"; key: MessageKey };

/** The page itself: the state in, the close press out (no hooks of the router, so a test can render it). */
export function TgEditView({ state, onClose }: { state: TgEditState; onClose: () => void }) {
  const { t } = useI18n();
  return (
    <main id="main" className="auth-page">
      <div className="auth-card" data-testid="tg-edit" data-state={state.phase}>
        <p className="brand">{t("app.name")}</p>
        <h1>{t("tgEdit.title")}</h1>
        {state.phase === "working" && <p role="status">{t("tgEdit.working")}</p>}
        {state.phase === "ready" && <p data-testid="tg-edit-placeholder">{t("tgEdit.placeholder")}</p>}
        {state.phase === "error" && (
          <p className="notice notice-bad" role="alert" data-testid="tg-edit-error">
            {t(state.key)}
          </p>
        )}
        <p>
          <button type="button" data-testid="tg-edit-close" onClick={onClose}>
            {t("tgEdit.close")}
          </button>
        </p>
      </div>
    </main>
  );
}

/**
 * /tg/edit, the Telegram mini app page (spec 44, RT-12). main.tsx has taken
 * the one-time code out of the address; this page exchanges it for the
 * 30-minute session. The editor itself comes in a later update (RT-12b): for
 * now the page says so and offers to close.
 */
export function TgEditScreen() {
  const { loginWithLink } = useAuth();
  const navigate = useNavigate();
  const [target] = useState(() => tgEditTarget());
  const [state, setState] = useState<TgEditState>({ phase: "working" });

  useEffect(() => {
    let alive = true;
    void enterMiniApp(target, { loadScript: loadTelegramScript, exchange: loginWithLink }).then((out) => {
      if (alive) setState(out.ok ? { phase: "ready" } : { phase: "error", key: out.key });
    });
    return () => {
      alive = false;
    };
  }, [target, loginWithLink]);

  const close = () => {
    // Outside Telegram (a plain browser) there is nothing to close: go on to the app.
    if (!closeMiniApp()) void navigate({ to: "/orgs", replace: true });
  };
  return <TgEditView state={state} onClose={close} />;
}
