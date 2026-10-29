import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { SessionInvitations } from "@/components/staffInvitations/SessionInvitations";
import { fetchMe, fetchOrgMe, fetchSessionSummary, listEvents, listSessions } from "@/lib/api/endpoints";
import { session as apiSession } from "@/lib/api/http";
import { organizationsOf, useAuth } from "@/lib/auth";
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { closeMiniApp, launchData, loadTelegramScript, openExternalLink } from "@/lib/tgEdit";
import { openTgInvitations, tgInvitationsTarget, webInvitationsPath, type TgInvitationsOpened } from "@/lib/tgInvitations";
import { useTelegramBack } from "@/lib/useTelegramBack";
import { LanguageSelect } from "@/screens/LanguageSelect";

export type TgInvitationsState = { phase: "working" } | TgInvitationsOpened;

/** The events list of the mini app has its own cache entry (no language). */
const TG_LIST = "tg-invitations";

function CloseButton({ onClose }: { onClose: () => void }) {
  const { t } = useI18n();
  return (
    <p>
      <button type="button" className="btn" data-testid="tg-inv-close" onClick={onClose}>
        {t("tgEdit.close")}
      </button>
    </p>
  );
}

/**
 * The page itself: the state in, the close press out. The state "ready" mounts
 * the working part, which needs the query client; everything else is plain
 * markup, so a test can render it on the server.
 */
export function TgInvitationsView({ state, onClose }: { state: TgInvitationsState; onClose: () => void }) {
  const { t } = useI18n();
  // The Telegram back button closes the page, the same as the button below.
  useTelegramBack(onClose);
  return (
    <main id="main" className="tg-page">
      <div className="tg-card tg-inv" data-testid="tg-inv" data-state={state.phase}>
        <p className="brand">{t("app.name")}</p>
        <h1>{t("tgInv.title")}</h1>
        {state.phase === "working" && <p role="status">{t("tgInv.working")}</p>}
        {state.phase === "ready" && <TgInvitations opened={state} />}
        {state.phase === "noAccess" && (
          <p className="notice notice-bad" role="alert" data-testid="tg-inv-no-access">
            {t("tgInv.noAccess")}
          </p>
        )}
        {state.phase === "error" && (
          <p className="notice notice-bad" role="alert" data-testid="tg-inv-error">
            {t(state.key)}
          </p>
        )}
        <CloseButton onClose={onClose} />
        <LanguageSelect />
      </div>
    </main>
  );
}

/**
 * The invitations of one session in the phone layout: the same working part as
 * the web screen. What the phone does badly stays in the browser: the CSV
 * upload is only a link to the web screen of the same session. The person's
 * rights come from the organization's own /me, never from the link.
 */
function TgInvitations({ opened }: { opened: Extract<TgInvitationsOpened, { phase: "ready" }> }) {
  const { t, locale } = useI18n();
  const { orgId, eventId, sessionId, event, session } = opened;
  const access = useQuery({ queryKey: ["orgMe", orgId], queryFn: () => fetchOrgMe(orgId), staleTime: 60_000 });
  const canSales = access.data?.permissions.includes("order.read") === true;
  // The venue's own time zone, when the person may read the summary; otherwise the date is shown in UTC with its label.
  const summary = useQuery({
    queryKey: ["sessionSummary", orgId, sessionId],
    queryFn: () => fetchSessionSummary(orgId, sessionId),
    enabled: canSales,
    retry: false,
  });
  if (access.isLoading) return <p role="status">{t("tgInv.working")}</p>;
  if (access.data === undefined) {
    return (
      <p className="notice notice-bad" role="alert" data-testid="tg-inv-no-access">
        {t("tgInv.noAccess")}
      </p>
    );
  }
  const permissions = new Set(access.data.permissions);
  const can = (p: string) => permissions.has(p);
  const tz = summary.data?.session.venue_timezone ?? null;
  const when = formatDateTime(summary.data?.session.start_at ?? session.start_at, tz, locale);
  const webUrl = `${window.location.origin}${webInvitationsPath(orgId, eventId, sessionId)}`;

  return (
    <>
      <p className="tg-event" data-testid="tg-inv-event">
        {t("tgEdit.event", { name: event.name })}
        {when !== "" && (
          <>
            <br />
            {t("tgInv.date", { when })}
          </>
        )}
      </p>
      <SessionInvitations orgId={orgId} eventId={eventId} sessionId={sessionId} can={can} />
      <p className="muted" data-testid="tg-inv-browser">
        {t("tgInv.csvBrowser")}{" "}
        <a
          href={webUrl}
          target="_blank"
          rel="noopener noreferrer"
          data-testid="tg-inv-open-browser"
          onClick={(e) => {
            // Inside Telegram the phone's own browser opens it; elsewhere the link works by itself.
            if (openExternalLink(webUrl)) e.preventDefault();
          }}
        >
          {t("tgInv.openBrowser")}
        </a>
      </p>
    </>
  );
}

/**
 * /tg/invitations, the Telegram mini app page of the staff invitations (spec
 * 46, 4.2). main.tsx has taken the one-time code, the session and the event out
 * of the address; this page signs in, finds the session among the person's own
 * organizations and opens the invitations of that session.
 */
export function TgInvitationsScreen() {
  const { loginWithLink, loginWithTelegram } = useAuth();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [target] = useState(() => tgInvitationsTarget());
  const [state, setState] = useState<TgInvitationsState>({ phase: "working" });
  // One opening per page, however often the effect runs (StrictMode runs it twice, and a one-time code works once).
  const opening = useRef<Promise<TgInvitationsOpened> | null>(null);

  useEffect(() => {
    let alive = true;
    opening.current ??= openTgInvitations(target, {
      loadScript: loadTelegramScript,
      initData: () => launchData(),
      hasSession: () => apiSession.hasSession(),
      exchangeCode: loginWithLink,
      exchangeTelegram: loginWithTelegram,
      organizations: async () => {
        const me = await queryClient.fetchQuery({ queryKey: ["me"], queryFn: fetchMe, staleTime: 60_000 });
        return organizationsOf(me.organization_memberships).map((m) => m.org_id);
      },
      list: (orgId) => queryClient.fetchQuery({ queryKey: ["events", orgId, TG_LIST], queryFn: () => listEvents(orgId), staleTime: 30_000 }),
      sessions: (orgId, eventId) =>
        queryClient.fetchQuery({ queryKey: ["sessions", orgId, eventId], queryFn: () => listSessions(orgId, eventId), staleTime: 30_000 }),
    });
    void opening.current.then((out) => {
      if (alive) setState(out);
    });
    return () => {
      alive = false;
    };
    // Once per page: a language change after the opening must not look the session up again.
  }, []);

  const close = useCallback(() => {
    // Outside Telegram (a plain browser) there is nothing to close: go on to the app.
    if (!closeMiniApp()) void navigate({ to: "/orgs", replace: true });
  }, [navigate]);
  return <TgInvitationsView state={state} onClose={close} />;
}
