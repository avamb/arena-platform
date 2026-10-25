import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { EmailNoteSection } from "@/components/EmailNoteSection";
import { TgDescriptionEditor } from "@/components/TgDescriptionEditor";
import { fetchMe, fetchOrgMe, listEvents } from "@/lib/api/endpoints";
import { organizationsOf, useAuth } from "@/lib/auth";
import { useI18n } from "@/lib/i18n";
import { bindBackButton, closeMiniApp, loadTelegramScript, tgEditTarget, type TgEditKind } from "@/lib/tgEdit";
import { openTgEdit, type TgEditOpened } from "@/lib/tgEditEvent";
import { LanguageSelect } from "@/screens/LanguageSelect";

export type TgEditState = { phase: "working" } | TgEditOpened;

/** The right the API asks for both texts (PATCH of the event). */
const EDIT_RIGHT = "event.update";

/**
 * The page itself: the state in, the close press out. The state "ready" mounts
 * the editor of the text, which needs the query client; everything else is
 * plain markup, so a test can render it on the server.
 */
export function TgEditView({ state, onClose }: { state: TgEditState; onClose: () => void }) {
  const { t } = useI18n();
  // The Telegram back button closes the page, the same as the button below. The handler is read through a ref:
  // a new function on every render must not take the button away and put it back.
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => bindBackButton(() => close.current()), []);

  return (
    <main id="main" className="tg-page">
      <div className="tg-card" data-testid="tg-edit" data-state={state.phase}>
        <p className="brand">{t("app.name")}</p>
        <h1>{t("tgEdit.title")}</h1>
        {state.phase === "working" && <p role="status">{t("tgEdit.working")}</p>}
        {state.phase === "ready" && <TgEditor kind={state.kind} orgId={state.orgId} eventId={state.eventId} onClose={onClose} />}
        {state.phase === "noAccess" && (
          <p className="notice notice-bad" role="alert" data-testid="tg-edit-no-access">
            {t("tgEdit.noAccess")}
          </p>
        )}
        {state.phase === "error" && (
          <p className="notice notice-bad" role="alert" data-testid="tg-edit-error">
            {t(state.key)}
          </p>
        )}
        {state.phase !== "ready" && <CloseButton onClose={onClose} />}
        <LanguageSelect />
      </div>
    </main>
  );
}

function CloseButton({ onClose }: { onClose: () => void }) {
  const { t } = useI18n();
  return (
    <p>
      <button type="button" className="btn" data-testid="tg-edit-close" onClick={onClose}>
        {t("tgEdit.close")}
      </button>
    </p>
  );
}

/**
 * The editor of one text of one event. The event is read through the events
 * list of its organization (the same list the web card reads) and the person's
 * right to edit through the organization's own /me: a person without
 * `event.update` is told there is no access, the same words as for a foreign
 * event. After a save the mini app closes itself; in a plain browser it says
 * so and offers the event in the web event center.
 */
function TgEditor({ kind, orgId, eventId, onClose }: { kind: TgEditKind; orgId: string; eventId: string; onClose: () => void }) {
  const { t, locale } = useI18n();
  const [saved, setSaved] = useState(false);
  const events = useQuery({ queryKey: ["events", orgId, locale], queryFn: () => listEvents(orgId, locale) });
  const access = useQuery({ queryKey: ["orgMe", orgId], queryFn: () => fetchOrgMe(orgId), staleTime: 60_000 });
  const done = useCallback(() => {
    // Telegram closes the window; outside Telegram the page stays and says it is saved.
    if (!closeMiniApp()) setSaved(true);
  }, []);

  if (events.isLoading || access.isLoading) return <p role="status">{t("tgEdit.working")}</p>;
  const event = events.data?.events.find((e) => e.id === eventId);
  if (event === undefined || access.data === undefined || !access.data.permissions.includes(EDIT_RIGHT)) {
    return (
      <>
        <p className="notice notice-bad" role="alert" data-testid="tg-edit-no-access">
          {t("tgEdit.noAccess")}
        </p>
        <CloseButton onClose={onClose} />
      </>
    );
  }

  return (
    <>
      <p className="tg-event" data-testid="tg-edit-event">
        {t("tgEdit.event", { name: event.name })}
      </p>
      {kind === "description" ? (
        <TgDescriptionEditor
          key={`${orgId}:${eventId}`}
          orgId={orgId}
          eventId={eventId}
          description={event.description}
          descriptionFormat={event.description_format}
          onSaved={done}
        />
      ) : (
        <EmailNoteSection
          key={`${orgId}:${eventId}`}
          orgId={orgId}
          eventId={eventId}
          note={event.email_note}
          noteFormat={event.email_note_format}
          canEdit
          onSaved={done}
        />
      )}
      {saved && (
        <div className="notice notice-ok" role="status" data-testid="tg-edit-saved">
          <p>
            <strong>{t("tgEdit.saved")}</strong> {t("tgEdit.saved.next")}
          </p>
          <p>
            <a href={`/o/${orgId}/events/${eventId}`} data-testid="tg-edit-open-event">
              {t("tgEdit.openEvent")}
            </a>
          </p>
        </div>
      )}
      <CloseButton onClose={onClose} />
    </>
  );
}

/**
 * /tg/edit, the Telegram mini app page (spec 44, RT-12). main.tsx has taken
 * the one-time code, the kind of text and the event out of the address; this
 * page exchanges the code for the 30-minute session, finds the event among the
 * person's own organizations and opens the editor of the text.
 */
export function TgEditScreen() {
  const { loginWithLink } = useAuth();
  const { locale } = useI18n();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [target] = useState(() => tgEditTarget());
  const [state, setState] = useState<TgEditState>({ phase: "working" });
  // One opening per page, however often the effect runs (StrictMode runs it twice, and a one-time code works once).
  const opening = useRef<Promise<TgEditOpened> | null>(null);

  useEffect(() => {
    let alive = true;
    opening.current ??= openTgEdit(target, {
      loadScript: loadTelegramScript,
      exchange: loginWithLink,
      organizations: async () => {
        const me = await queryClient.fetchQuery({ queryKey: ["me"], queryFn: fetchMe, staleTime: 60_000 });
        return organizationsOf(me.organization_memberships).map((m) => m.org_id);
      },
      // The list is kept in the query cache, so the editor reads it again without a second request.
      list: (orgId) => queryClient.fetchQuery({ queryKey: ["events", orgId, locale], queryFn: () => listEvents(orgId, locale), staleTime: 30_000 }),
    });
    void opening.current.then((out) => {
      if (alive) setState(out);
    });
    return () => {
      alive = false;
    };
    // Once per page: a language change after the opening must not look the event up again.
  }, []);

  const close = useCallback(() => {
    // Outside Telegram (a plain browser) there is nothing to close: go on to the app.
    if (!closeMiniApp()) void navigate({ to: "/orgs", replace: true });
  }, [navigate]);
  return <TgEditView state={state} onClose={close} />;
}
