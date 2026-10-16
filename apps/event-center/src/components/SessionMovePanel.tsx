/**
 * 5-2: "Move the session". The web twin of the bot's `ses:mv` ... `ses:go`
 * (eventbot/sessions_dialog.go).
 *
 * The person picks a new day (DateField: a typed date is read back and
 * confirmed, never taken directly), a start time in the VENUE's zone and,
 * when they may read venues, another venue. Nothing is written yet: the dry
 * run (GET .../change-impact) says how many orders get a letter and whether
 * the change is blocked, and ONE press (DryRunConfirm) moves the EXISTING
 * session with a PATCH. A move is not a cancellation, so it needs no typed
 * word.
 *
 * The change is blocked in two honest ways (the server's words, shown as they
 * are): `contact_missing` shows the contact form right here, `site_route_unsupported`
 * says that the sites write their own letter and the move is not available yet.
 */
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { DateField } from "@/components/DateField";
import { DryRunConfirm } from "@/components/DryRunConfirm";
import { SessionChangeMessage } from "@/components/SessionChangeMessage";
import { SessionContactForm } from "@/components/SessionContactForm";
import { fetchChangeImpact, listVenues, patchSession } from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import type { SessionChangeImpact } from "@/lib/api/types";
import { formatDateTime, zoneLabel } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import {
  EVENT_UPDATE_PERMISSION,
  emptyMessage,
  impactQuery,
  impactToDryRun,
  instantToWall,
  isContactBlocked,
  moveBody,
  noticeToSend,
  noticeTooLong,
  proposeMove,
  resetToSuggested,
  typeMessage,
  withSuggested,
  type MessageState,
} from "@/lib/sessionChange";
import { useOrg } from "@/screens/OrgLayout";

export interface SessionMoveDone {
  startAt: string;
  queued: number;
}

export interface SessionMovePanelProps {
  orgId: string;
  eventId: string;
  sessionId: string;
  /** The session now. */
  startAt: string;
  venueId: string | null;
  /** The zone of the current venue; the day and time are shown in it. */
  timezone: string | null;
  onDone: (done: SessionMoveDone) => void;
  onClose: () => void;
}

export function SessionMovePanel({ orgId, eventId, sessionId, startAt, venueId, timezone, onDone, onClose }: SessionMovePanelProps) {
  const { t, locale } = useI18n();
  const { access } = useOrg();
  const queryClient = useQueryClient();

  const venues = useQuery({ queryKey: ["venues", orgId], queryFn: () => listVenues(orgId), enabled: access.can("venue.read") });
  const venueList = venues.data?.venues ?? [];

  const currentZone = timezone ?? "UTC";
  const initial = instantToWall(startAt, currentZone);
  const [day, setDay] = useState(initial.day);
  const [time, setTime] = useState(initial.time);
  const [touched, setTouched] = useState(false);
  const [venueChoice, setVenueChoice] = useState("");
  const [message, setMessage] = useState<MessageState>(emptyMessage);
  const [impact, setImpact] = useState<SessionChangeImpact | null>(null);
  const [refresh, setRefresh] = useState(0);

  const chosen = venueList.find((v) => v.id === (venueChoice !== "" ? venueChoice : venueId));
  const zone = chosen?.timezone ?? currentZone;
  const proposal = proposeMove({
    currentStartAt: startAt,
    currentVenueId: venueId,
    day,
    time,
    scheduleTouched: touched,
    venueId: venueChoice,
    zone,
  });

  const reload = () => {
    setImpact(null);
    setRefresh((n) => n + 1);
  };

  const dryRun = async () => {
    const got = await fetchChangeImpact(
      orgId,
      sessionId,
      impactQuery({ startAt: proposal.startAt, venueId: proposal.venueId, locale }),
    );
    setImpact(got);
    setMessage((prev) => withSuggested(prev, got.default_message));
    return impactToDryRun(got, "move", t);
  };

  const onConfirm = async () => {
    if (noticeTooLong(message.text)) {
      throw new ApiError(422, "session.change_message_too_long", "the message to buyers is too long");
    }
    try {
      const answer = await patchSession(orgId, eventId, sessionId, moveBody(proposal, noticeToSend(impact, message.text)));
      await queryClient.invalidateQueries();
      onDone({ startAt: answer.session.start_at, queued: answer.change?.queued ?? 0 });
    } catch (error) {
      // The state changed under the person (a contact removed, a site order
      // arrived): ask the dry run again so the screen says what is true now.
      if (error instanceof ApiError && (error.code === "organization.contact_missing" || error.code === "session.change_site_unsupported")) {
        reload();
      }
      throw error;
    }
  };

  return (
    <section className="panel session-change-panel" aria-labelledby="session-move-title" data-testid="session-move">
      <h2 id="session-move-title">{t("sessionChange.move.title")}</h2>
      <p className="muted">{t("sessionChange.move.now", { time: formatDateTime(startAt, currentZone, locale) })}</p>

      <DateField
        label={t("sessionChange.move.dateLegend")}
        value={day === "" ? null : day}
        timezone={zone}
        onChange={(iso) => {
          setDay(iso);
          setTouched(true);
        }}
      />

      <label htmlFor="session-move-time">{t("sessionChange.move.time")}</label>
      <input
        id="session-move-time"
        type="time"
        value={time}
        step={60}
        aria-describedby="session-move-zone"
        aria-invalid={proposal.problem !== "none" ? true : undefined}
        data-testid="session-move-time"
        onChange={(e) => {
          setTime(e.target.value);
          setTouched(true);
        }}
      />
      <p id="session-move-zone" className="field-hint">
        {t("sessionChange.move.zone", { zone: zoneLabel(zone) !== "" ? `${zoneLabel(zone)} (${zone})` : zone })}
      </p>
      {proposal.problem === "invalidTime" && (
        <p className="field-error" role="alert" data-testid="session-move-time-error">
          {t("sessionChange.move.invalidTime")}
        </p>
      )}
      {proposal.problem === "gapTime" && (
        <p className="field-error" role="alert" data-testid="session-move-time-error">
          {t("sessionChange.move.gapTime")}
        </p>
      )}

      {venueList.length > 1 && (
        <>
          <label htmlFor="session-move-venue">{t("sessionChange.move.venue")}</label>
          <select
            id="session-move-venue"
            value={venueChoice !== "" ? venueChoice : (venueId ?? "")}
            data-testid="session-move-venue"
            onChange={(e) => setVenueChoice(e.target.value === venueId ? "" : e.target.value)}
          >
            {venueList.map((v) => (
              <option key={v.id} value={v.id}>
                {v.name}
              </option>
            ))}
          </select>
        </>
      )}

      {!proposal.changed && proposal.problem === "none" && (
        <p className="muted" data-testid="session-move-unchanged">
          {t("sessionChange.move.unchanged")}
        </p>
      )}

      {proposal.changed && (
        <>
          {impact !== null && impact.orders > 0 && (
            <SessionChangeMessage
              state={message}
              suggested={impact.default_message}
              onType={(text) => setMessage(typeMessage(text))}
              onReset={() => setMessage(resetToSuggested(impact.default_message))}
            />
          )}
          <DryRunConfirm
            title={t("sessionChange.move.check")}
            dryRun={dryRun}
            onConfirm={onConfirm}
            confirmLabel={t("sessionChange.move.confirm")}
            reloadKey={`${proposal.startAt ?? ""}|${proposal.venueId ?? ""}|${refresh}`}
          />
          {isContactBlocked(impact) && impact !== null && (
            <SessionContactForm
              orgId={orgId}
              eventId={eventId}
              contact={impact.contact}
              canEdit={access.can(EVENT_UPDATE_PERMISSION)}
              onSaved={reload}
            />
          )}
        </>
      )}

      <p>
        <button type="button" className="btn" onClick={onClose} data-testid="session-change-close">
          {t("sessionChange.close")}
        </button>
      </p>
    </section>
  );
}
