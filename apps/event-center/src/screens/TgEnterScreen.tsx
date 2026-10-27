import { useEffect, useRef, useState } from "react";
import { useRouter } from "@tanstack/react-router";
import { useAuth } from "@/lib/auth";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { launchData, loadTelegramScript, closeMiniApp } from "@/lib/tgEdit";
import { enterTelegram, markEnteredThroughTelegram, type TgEnterSearch } from "@/lib/tgEnter";
import { LanguageSelect } from "@/screens/LanguageSelect";

/**
 * The page markup: the state in, the press out. Plain markup, so a test can
 * render it on the server.
 */
export function TgEnterView({ error, onClose }: { error: MessageKey | null; onClose: () => void }) {
  const { t } = useI18n();
  return (
    <main id="main" className="tg-page">
      <div className="tg-card" data-testid="tg-enter" data-state={error === null ? "working" : "error"}>
        <p className="brand">{t("app.name")}</p>
        <h1>{t("tgEnter.title")}</h1>
        {error === null ? (
          <p role="status">{t("tgEnter.working")}</p>
        ) : (
          <>
            <p className="notice notice-bad" role="alert" data-testid="tg-enter-error">
              {t(error)}
            </p>
            <p>
              <button type="button" className="btn" data-testid="tg-enter-close" onClick={onClose}>
                {t("tgEdit.close")}
              </button>
            </p>
            <p>
              <a href="/login">{t("linkLogin.toLogin")}</a>
            </p>
          </>
        )}
        <LanguageSelect />
      </div>
    </main>
  );
}

/**
 * /tg — the entry of the Telegram mini app. The bot's web_app button opens
 * /tg?next=<path>; the page signs the person in with Telegram's launch data
 * and opens the screen the bot named.
 */
export function TgEnterScreen({ next }: { next: TgEnterSearch }) {
  const { status, loginWithTelegram } = useAuth();
  const router = useRouter();
  const [error, setError] = useState<MessageKey | null>(null);
  const [to, setTo] = useState<string | null>(null);
  // StrictMode runs the effect twice; the exchange itself is also once per launch data.
  const started = useRef(false);

  useEffect(() => {
    if (started.current) return;
    started.current = true;
    let alive = true;
    void enterTelegram(next.next, {
      loadScript: () => loadTelegramScript(),
      initData: () => launchData(),
      exchange: loginWithTelegram,
    }).then((out) => {
      if (!alive) return;
      if (!out.ok) {
        setError(out.key);
        return;
      }
      markEnteredThroughTelegram();
      setTo(out.to);
    });
    return () => {
      alive = false;
      // StrictMode's first pass is undone and the second does not run the exchange again, so keep the flag.
      started.current = false;
    };
  }, []);

  // Leave only once the app knows it is signed in: a screen of the organization that still saw "signed out" would send
  // the person to the sign-in page and back, and the history would have a step the back button must not walk.
  // replace: this loading page is not a place to come back to.
  useEffect(() => {
    if (to !== null && status === "signedIn") router.history.replace(to);
  }, [to, status, router]);

  return <TgEnterView error={error} onClose={() => void closeMiniApp()} />;
}
