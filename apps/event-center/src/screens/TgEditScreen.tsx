import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { EmailNoteSection } from "@/components/EmailNoteSection";
import { TgDescriptionEditor, type TgWork } from "@/components/TgDescriptionEditor";
import { fetchMe, fetchOrgMe, listEvents } from "@/lib/api/endpoints";
import { organizationsOf, useAuth } from "@/lib/auth";
import { useI18n } from "@/lib/i18n";
import {
  closeMiniApp,
  insideTelegram,
  loadTelegramScript,
  setClosingConfirmation,
  tgEditTarget,
  type TgEditKind,
} from "@/lib/tgEdit";
import { useTelegramBack } from "@/lib/useTelegramBack";
import { descriptionBaseOf, openTgEdit, type TgEditOpened } from "@/lib/tgEditEvent";
import { LanguageSelect } from "@/screens/LanguageSelect";

export type TgEditState = { phase: "working" } | TgEditOpened;

/** The right the API asks for both texts (PATCH of the event). */
const EDIT_RIGHT = "event.update";

/** The events list of the mini app has its own cache entry (no language), under the prefix the saves refresh. */
const TG_LIST = "tg-edit";

/**
 * The page itself: the state in, the close press out. The state "ready" mounts
 * the editor of the text, which needs the query client; everything else is
 * plain markup, so a test can render it on the server.
 */
export function TgEditView({ state, onClose }: { state: TgEditState; onClose: () => void }) {
  const { t } = useI18n();
  // What the editor on the page is doing: unsaved text asks Telegram to confirm a close, a save on its way
  // is never cut off by the back button or the close button.
  const [work, setWork] = useState<TgWork>({ dirty: false, busy: false });
  const busy = useRef(false);
  busy.current = work.busy;
  // The Telegram back button closes the page, the same as the button below.
  const close = useRef(onClose);
  close.current = onClose;
  const closeIfIdle = useCallback(() => {
    if (!busy.current) close.current();
  }, []);
  useTelegramBack(closeIfIdle);
  const dirty = work.dirty;
  useEffect(() => {
    let alive = true;
    void loadTelegramScript().then(() => {
      if (alive) setClosingConfirmation(dirty);
    });
    return () => {
      alive = false;
    };
  }, [dirty]);
  useEffect(() => () => setClosingConfirmation(false), []);

  return (
    <main id="main" className="tg-page">
      <div className="tg-card" data-testid="tg-edit" data-state={state.phase}>
        <p className="brand">{t("app.name")}</p>
        <h1>{t("tgEdit.title")}</h1>
        {state.phase === "working" && <p role="status">{t("tgEdit.working")}</p>}
        {state.phase === "ready" && (
          <TgEditor kind={state.kind} orgId={state.orgId} eventId={state.eventId} busy={work.busy} onWork={setWork} onClose={closeIfIdle} />
        )}
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

function CloseButton({ onClose, disabled = false }: { onClose: () => void; disabled?: boolean }) {
  const { t } = useI18n();
  return (
    <p>
      <button type="button" className="btn" data-testid="tg-edit-close" onClick={onClose} disabled={disabled}>
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
function TgEditor({
  kind,
  orgId,
  eventId,
  busy,
  onWork,
  onClose,
}: {
  kind: TgEditKind;
  orgId: string;
  eventId: string;
  busy: boolean;
  onWork: (work: TgWork) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [saved, setSaved] = useState(false);
  // No language in the key or the call: the editor works on the canonical text, and switching the language of the
  // page must neither read the list again nor put another text under the draft.
  const events = useQuery({ queryKey: ["events", orgId, TG_LIST], queryFn: () => listEvents(orgId) });
  const access = useQuery({ queryKey: ["orgMe", orgId], queryFn: () => fetchOrgMe(orgId), staleTime: 60_000 });
  const done = useCallback(() => {
    // Telegram closes the window; outside Telegram the page stays and says it is saved.
    // The text is saved: nothing is left to confirm. Telegram closes the window; outside Telegram the page stays and says so.
    setClosingConfirmation(false);
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
          description={descriptionBaseOf(event)}
          descriptionFormat={event.description_format}
          onWork={onWork}
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
          onWork={onWork}
          onSaved={done}
        />
      )}
      {saved && (
        <div className="notice notice-ok" role="status" data-testid="tg-edit-saved">
          <p>
            <strong>{t("tgEdit.saved")}</strong> {t("tgEdit.saved.next")}
          </p>
          {!insideTelegram() && (
            <p>
              <a href={`/o/${orgId}/events/${eventId}`} data-testid="tg-edit-open-event">
                {t("tgEdit.openEvent")}
              </a>
            </p>
          )}
        </div>
      )}
      <CloseButton onClose={onClose} disabled={busy} />
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
      list: (orgId) => queryClient.fetchQuery({ queryKey: ["events", orgId, TG_LIST], queryFn: () => listEvents(orgId), staleTime: 30_000 }),
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
