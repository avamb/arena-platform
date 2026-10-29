/**
 * 4-4: "Invite guests". The web twin of the bot's invitation dialog
 * (eventbot/invite.go): event -> session -> general admission category ->
 * guest list -> a confirmation screen -> ONE issue call per guest (one place)
 * on the staff-invitation route (spec 46, INV-04b), so every invitation is its
 * own order and can be canceled on its own. Each guest has a request id minted
 * with the confirmation and kept while the list is unchanged, so a retry or a
 * double press replays what was issued, it never mails a guest twice.
 *
 * A date with a seat map is not issued here: the form links to the seat map of
 * that date, where seats are picked.
 *
 * The number of tickets is the number of guests. The server stays the
 * authority on free places and on the 50-ticket cap of one round; the screen
 * only refuses early what it can see (lib/invitations.ts sendBlock).
 *
 * A category that runs out half way keeps the guests issued before it: the
 * screen says how many went out and which guests are left, and offers to send
 * the rest. Guests' e-mails are shown here only to a person who may issue
 * (the route already requires it) and are never logged.
 */
import { useMemo, useRef, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { useEventSessions } from "@/components/staffInvitations/useEventSessions";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { issueStaffInvitation, listTiers } from "@/lib/api/endpoints";
import { filterEvents } from "@/lib/events";
import { formatDateTime } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  MAX_GUESTS,
  inviteSessionOk,
  inviteTierOk,
  isSeatedSession,
  parseGuests,
  overFreePlaces,
  sendBlock,
  type Guest,
  type SendBlock,
} from "@/lib/invitations";
import { RequestIds, gaIssueRequest } from "@/lib/orgInvitations";
import { STAFF_INVITATIONS_KEY, isDefinitiveRefusal } from "@/lib/staffInvitations";
import { useEventsList } from "@/screens/EventsScreen";

type Phase = "edit" | "confirm" | "sending" | "done" | "partial";

interface Progress {
  total: number;
  done: number;
  failure: unknown;
}

const BLOCK_KEY: Record<SendBlock, MessageKey> = {
  none: "invitations.block.none",
  problems: "invitations.problemsTitle",
  tooMany: "invitations.block.tooMany",
};

export function InviteForm({ orgId }: { orgId: string }) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const events = useEventsList(orgId);
  const [eventId, setEventId] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [tierId, setTierId] = useState("");
  const [text, setText] = useState("");
  const [phase, setPhase] = useState<Phase>("edit");
  // V1: the organizer's private VIP mark for every ticket of the operation.
  const [vip, setVip] = useState(false);
  const [progress, setProgress] = useState<Progress>({ total: 0, done: 0, failure: null });
  // One request id per guest: kept while the list, the category and the VIP mark stay, dropped when anything changes.
  const requestIds = useRef(new RequestIds());
  const sendLock = useRef(false);

  // The venue's zone for the dates comes from the event summary (needs order.read);
  // without it the dates are labelled UTC rather than guessed.
  const { sessions, sorted, zoneOf } = useEventSessions(orgId, eventId);
  const sessionList = useMemo(() => sorted.filter((s) => inviteSessionOk(s, new Date())), [sorted]);
  const session = sessionList.find((s) => s.id === sessionId);
  // A seated date is issued on its seat map, so its categories are not read here.
  const seated = session !== undefined && isSeatedSession(session);
  const tiers = useQuery({
    queryKey: ["tiers", orgId, eventId, sessionId],
    queryFn: () => listTiers(orgId, eventId, sessionId),
    enabled: eventId !== "" && sessionId !== "" && !seated,
  });

  const eventList = useMemo(() => filterEvents(events.data?.events ?? [], "run"), [events.data]);
  const tierList = useMemo(
    () => [...(tiers.data?.tiers ?? [])].filter(inviteTierOk).sort((a, b) => a.sort_order - b.sort_order),
    [tiers.data],
  );
  const tier = tierList.find((x) => x.id === tierId);
  const event = eventList.find((e) => e.id === eventId);
  const parsed = useMemo(() => parseGuests(text), [text]);
  const free = tier?.available ?? null;
  const block = sendBlock(parsed);
  const overFree = overFreePlaces(parsed, free);

  const changed = () => {
    requestIds.current.reset();
    setPhase("edit");
    setProgress({ total: 0, done: 0, failure: null });
  };

  const send = async (guests: Guest[], from: number) => {
    if (sendLock.current || session === undefined || tier === undefined) return;
    sendLock.current = true;
    const ids = requestIds.current;
    setPhase("sending");
    setProgress({ total: guests.length, done: from, failure: null });
    let done = from;
    try {
      for (let i = from; i < guests.length; i++) {
        await issueStaffInvitation(orgId, session.id, gaIssueRequest(guests[i], tier.id, locale, ids.at(i), vip));
        done = i + 1;
        setProgress({ total: guests.length, done, failure: null });
      }
      setPhase("done");
    } catch (failure) {
      // The server read the request and refused it: the next try of this guest is a new request.
      // A dropped connection or a 5xx may have been processed: the next try is the SAME request.
      if (isDefinitiveRefusal(failure)) ids.renew(done);
      setProgress({ total: guests.length, done, failure });
      setPhase("partial");
    } finally {
      sendLock.current = false;
      // The places, the list and the figures changed whatever happened.
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: [STAFF_INVITATIONS_KEY, orgId] }),
        queryClient.invalidateQueries({ queryKey: ["tiers", orgId, eventId, sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["sessionSummary", orgId, sessionId] }),
      ]);
    }
  };

  const reset = () => {
    requestIds.current.reset();
    setVip(false);
    setText("");
    setPhase("edit");
    setProgress({ total: 0, done: 0, failure: null });
  };

  if (events.isLoading) return <Loading />;
  if (events.error !== null) return <ErrorBox error={events.error} onRetry={() => void events.refetch()} />;

  const when = session !== undefined ? formatDateTime(session.start_at, zoneOf(session.id), locale) : "";
  const locked = phase === "confirm" || phase === "sending" || phase === "done" || phase === "partial";

  return (
    <section aria-labelledby="invite-title" data-testid="invite-form">
      <h2 id="invite-title">{t("invitations.issue.title")}</h2>
      {eventList.length === 0 ? (
        <Empty>{t("invitations.issue.noEvents")}</Empty>
      ) : (
        <>
          <label htmlFor="invite-event">{t("invitations.issue.event")}</label>
          <select
            id="invite-event"
            value={eventId}
            disabled={locked}
            data-testid="invite-event"
            onChange={(e) => {
              setEventId(e.target.value);
              setSessionId("");
              setTierId("");
              changed();
            }}
          >
            <option value="">{t("invitations.issue.eventPick")}</option>
            {eventList.map((e) => (
              <option key={e.id} value={e.id}>
                {e.name}
              </option>
            ))}
          </select>

          {eventId !== "" && sessions.isLoading && <Loading />}
          {eventId !== "" && sessions.error !== null && (
            <ErrorBox error={sessions.error} onRetry={() => void sessions.refetch()} />
          )}
          {eventId !== "" && sessions.data !== undefined && sessionList.length === 0 && (
            <Empty>{t("invitations.issue.noSessions")}</Empty>
          )}
          {sessionList.length > 0 && (
            <>
              <label htmlFor="invite-session">{t("invitations.issue.session")}</label>
              <select
                id="invite-session"
                value={sessionId}
                disabled={locked}
                data-testid="invite-session"
                onChange={(e) => {
                  setSessionId(e.target.value);
                  setTierId("");
                  changed();
                }}
              >
                <option value="">{t("invitations.issue.sessionPick")}</option>
                {sessionList.map((s) => (
                  <option key={s.id} value={s.id}>
                    {formatDateTime(s.start_at, zoneOf(s.id), locale)}
                  </option>
                ))}
              </select>
            </>
          )}

          {seated && session !== undefined && (
            <p className="notice" role="status" data-testid="invite-seated">
              {t("orgInv.issue.seated")}{" "}
              <Link
                to="/o/$orgId/events/$eventId/sessions/$sessionId/invitations"
                params={{ orgId, eventId, sessionId: session.id }}
                data-testid="invite-seated-link"
              >
                {t("orgInv.issue.seatedLink")}
              </Link>
            </p>
          )}

          {sessionId !== "" && !seated && tiers.isLoading && <Loading />}
          {sessionId !== "" && !seated && tiers.error !== null && <ErrorBox error={tiers.error} onRetry={() => void tiers.refetch()} />}
          {sessionId !== "" && !seated && tiers.data !== undefined && tierList.length === 0 && (
            <Empty>{t("invitations.issue.noCategories")}</Empty>
          )}
          {tierList.length > 0 && (
            <>
              <label htmlFor="invite-tier">{t("invitations.issue.category")}</label>
              <select
                id="invite-tier"
                value={tierId}
                disabled={locked}
                data-testid="invite-tier"
                onChange={(e) => {
                  setTierId(e.target.value);
                  changed();
                }}
              >
                <option value="">{t("invitations.issue.categoryPick")}</option>
                {tierList.map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.name} · {t("invitations.issue.free", { count: x.available ?? 0 })}
                  </option>
                ))}
              </select>
            </>
          )}

          {tier !== undefined && (
            <>
              <label htmlFor="invite-guests">{t("invitations.issue.guests")}</label>
              <textarea
                id="invite-guests"
                rows={6}
                value={text}
                disabled={locked}
                spellCheck={false}
                autoComplete="off"
                aria-describedby="invite-guests-hint"
                data-testid="invite-guests"
                onChange={(e) => {
                  setText(e.target.value);
                  changed();
                }}
              />
              <p id="invite-guests-hint" className="muted">
                {t("invitations.issue.guestsHint", { max: MAX_GUESTS })}
              </p>
              <p aria-live="polite" data-testid="invite-count">
                {t("invitations.issue.guestsCount", { count: parsed.guests.length })}
              </p>
              {parsed.problems.length > 0 && (
                <div className="notice notice-bad" role="alert" data-testid="invite-problems">
                  <p>
                    <strong>{t("invitations.problemsTitle")}</strong>
                  </p>
                  <ul>
                    {parsed.problems.map((p) => (
                      <li key={p.line}>
                        {t("invitations.problem", {
                          line: p.line,
                          reason: t(`invitations.reason.${p.reason}` as MessageKey),
                          text: p.text.length > 80 ? `${p.text.slice(0, 80)}…` : p.text,
                        })}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {block !== null && block !== "problems" && text.trim() !== "" && (
                <p className="notice" role="status" data-testid="invite-block">
                  {t(BLOCK_KEY[block], { max: MAX_GUESTS, free: free ?? 0 })}
                </p>
              )}
              {block === null && overFree && (
                <p className="notice" role="status" data-testid="invite-over-free">
                  {t("invitations.hint.overFree", { free: free ?? 0 })}
                </p>
              )}
              {phase === "edit" && (
                <p>
                  <button
                    type="button"
                    className="btn btn-primary"
                    disabled={block !== null}
                    onClick={() => {
                      requestIds.current.reset();
                      setPhase("confirm");
                    }}
                    data-testid="invite-review"
                  >
                    {t("invitations.issue.review")}
                  </button>
                </p>
              )}
            </>
          )}

          {phase === "confirm" && tier !== undefined && session !== undefined && (
            <div className="notice" role="region" aria-labelledby="invite-confirm-title" data-testid="invite-confirm">
              <h3 id="invite-confirm-title">{t("invitations.confirm.title")}</h3>
              <p>
                {t("invitations.confirm.what", { event: event?.name ?? "", when, category: tier.name })}
              </p>
              <p>
                <strong>{t("invitations.confirm.count", { count: parsed.guests.length })}</strong>
              </p>
              <p>{t("invitations.confirm.explain")}</p>
              <p>
                <label>
                  <input
                    type="checkbox"
                    checked={vip}
                    onChange={(e) => {
                      // A different request is a different operation: new request ids.
                      requestIds.current.reset();
                      setVip(e.target.checked);
                    }}
                    data-testid="invite-vip"
                  />{" "}
                  {t("invitations.confirm.vip")}
                </label>
              </p>
              <p className="muted">{t("vip.private")}</p>
              <div className="actions">
                <button
                  type="button"
                  className="btn btn-primary"
                  onClick={() => void send(parsed.guests, 0)}
                  data-testid="invite-send"
                >
                  {t("invitations.confirm.send")}
                </button>
                <button type="button" className="btn" onClick={changed} data-testid="invite-back">
                  {t("invitations.confirm.back")}
                </button>
              </div>
            </div>
          )}

          {phase === "sending" && (
            <p role="status" aria-live="polite" data-testid="invite-progress">
              {t("invitations.progress", { done: progress.done, total: progress.total })}
            </p>
          )}

          {phase === "done" && (
            <div className="notice notice-ok" role="status" data-testid="invite-done">
              <p>{t("invitations.done", { count: progress.total })}</p>
              <p>
                <button type="button" className="btn" onClick={reset} data-testid="invite-again">
                  {t("invitations.issue.title")}
                </button>
              </p>
            </div>
          )}

          {phase === "partial" && (
            <div data-testid="invite-partial">
              <ErrorNotice error={progress.failure} testId="invite-error" />
              <div className="notice" role="status">
                <p>
                  <strong>{t("invitations.partial.title", { done: progress.done, total: progress.total })}</strong>
                </p>
                <p data-testid="invite-left">
                  {t("invitations.partial.left", {
                    guests: parsed.guests
                      .slice(progress.done)
                      .map((g) => g.email)
                      .join(", "),
                  })}
                </p>
                <div className="actions">
                  <button
                    type="button"
                    className="btn btn-primary"
                    onClick={() => void send(parsed.guests, progress.done)}
                    data-testid="invite-retry"
                  >
                    {t("invitations.partial.retry")}
                  </button>
                  <button type="button" className="btn" onClick={reset} data-testid="invite-new">
                    {t("invitations.partial.new")}
                  </button>
                </div>
              </div>
            </div>
          )}
        </>
      )}
    </section>
  );
}
