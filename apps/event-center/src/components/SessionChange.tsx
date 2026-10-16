/**
 * 5-2: the "Move or cancel" block of a session card. The buttons come from
 * what the SERVER says the person may do (`changeButtons` over
 * `useOrgAccess().can`): an agent sees no block at all. One panel is open at a
 * time. After a save the block says what happened (the session moved, or was
 * canceled) and how many letters were queued, and the card reloads.
 */
import { useState } from "react";
import { SessionCancelPanel } from "@/components/SessionCancelPanel";
import { SessionMovePanel } from "@/components/SessionMovePanel";
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { changeButtons } from "@/lib/sessionChange";
import { useOrg } from "@/screens/OrgLayout";

export interface SessionChangeProps {
  orgId: string;
  eventId: string;
  sessionId: string;
  status: string | null | undefined;
  startAt: string;
  venueId: string | null;
  timezone: string | null;
}

type Panel = "move" | "cancel" | null;
type Outcome = { kind: "move"; startAt: string; queued: number } | { kind: "cancel"; queued: number } | null;

export function SessionChange({ orgId, eventId, sessionId, status, startAt, venueId, timezone }: SessionChangeProps) {
  const { t, locale } = useI18n();
  const { access } = useOrg();
  const [panel, setPanel] = useState<Panel>(null);
  const [outcome, setOutcome] = useState<Outcome>(null);
  const buttons = changeButtons(status, access.can);

  if (!buttons.move && !buttons.cancel && outcome === null) return null;

  return (
    <section className="session-change" aria-labelledby="session-change-title" data-testid="session-change">
      <h2 id="session-change-title">{t("sessionChange.title")}</h2>

      {outcome !== null && (
        <p className="notice notice-ok" role="status" data-testid="session-change-done">
          {outcome.kind === "move"
            ? t("sessionChange.move.done", { time: formatDateTime(outcome.startAt, timezone, locale) })
            : t("sessionChange.cancel.done")}{" "}
          {t("sessionChange.letters", { count: outcome.queued })}
        </p>
      )}

      {panel === null && (buttons.move || buttons.cancel) && (
        <>
          <p>{t("sessionChange.intro")}</p>
          <div className="actions">
            {buttons.move && (
              <button
                type="button"
                className="btn"
                data-testid="session-move-open"
                onClick={() => {
                  setOutcome(null);
                  setPanel("move");
                }}
              >
                {t("sessionChange.move.button")}
              </button>
            )}
            {buttons.cancel && (
              <button
                type="button"
                className="btn"
                data-testid="session-cancel-open"
                onClick={() => {
                  setOutcome(null);
                  setPanel("cancel");
                }}
              >
                {t("sessionChange.cancel.button")}
              </button>
            )}
          </div>
        </>
      )}

      {panel === "move" && (
        <SessionMovePanel
          orgId={orgId}
          eventId={eventId}
          sessionId={sessionId}
          startAt={startAt}
          venueId={venueId}
          timezone={timezone}
          onClose={() => setPanel(null)}
          onDone={(done) => {
            setPanel(null);
            setOutcome({ kind: "move", ...done });
          }}
        />
      )}
      {panel === "cancel" && (
        <SessionCancelPanel
          orgId={orgId}
          eventId={eventId}
          sessionId={sessionId}
          startAt={startAt}
          timezone={timezone}
          onClose={() => setPanel(null)}
          onDone={(done) => {
            setPanel(null);
            setOutcome({ kind: "cancel", ...done });
          }}
        />
      )}
    </section>
  );
}
