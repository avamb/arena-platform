/**
 * 4-4: "Invite guests". The web twin of the bot's invitation dialog
 * (eventbot/invite.go): event -> session -> general admission category ->
 * guest list -> a confirmation screen -> ONE issue call per guest (qty 1) with
 * the batch id `web-<operation>-<index>`, so every invitation can be annulled
 * on its own and a retry or a double press replays what was issued, it never
 * mails a guest twice.
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
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { fetchEventSummary, issueInvitation, listSessions, listTiers } from "@/lib/api/endpoints";
import { filterEvents } from "@/lib/events";
import { formatDateTime } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  MAX_GUESTS,
  batchId,
  inviteSessionOk,
  inviteTierOk,
  parseGuests,
  sendBlock,
  type Guest,
  type SendBlock,
} from "@/lib/invitations";
import { useEventsList } from "@/screens/EventsScreen";
import { useOrg } from "@/screens/OrgLayout";

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
  overFree: "invitations.block.overFree",
};

function newOperationId(): string {
  return globalThis.crypto.randomUUID();
}

export function InviteForm({ orgId }: { orgId: string }) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const { access } = useOrg();
  const events = useEventsList(orgId);
  const [eventId, setEventId] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [tierId, setTierId] = useState("");
  const [text, setText] = useState("");
  const [phase, setPhase] = useState<Phase>("edit");
  const [progress, setProgress] = useState<Progress>({ total: 0, done: 0, failure: null });
  // Minted when the confirmation is first shown, dropped when anything changes.
  const operation = useRef<string | null>(null);
  const sendLock = useRef(false);

  const sessions = useQuery({
    queryKey: ["sessions", orgId, eventId],
    queryFn: () => listSessions(orgId, eventId),
    enabled: eventId !== "",
  });
  // The venue's zone for the dates: the summary carries it (needs order.read);
  // without it the dates are labelled UTC rather than guessed.
  const summary = useQuery({
    queryKey: ["eventSummary", orgId, eventId],
    queryFn: () => fetchEventSummary(orgId, eventId),
    enabled: eventId !== "" && access.can("order.read"),
  });
  const zoneOf = (id: string): string | null => summary.data?.sessions.find((x) => x.id === id)?.venue_timezone ?? null;
  const tiers = useQuery({
    queryKey: ["tiers", orgId, eventId, sessionId],
    queryFn: () => listTiers(orgId, eventId, sessionId),
    enabled: eventId !== "" && sessionId !== "",
  });

  const eventList = useMemo(() => filterEvents(events.data?.events ?? [], "run"), [events.data]);
  const sessionList = useMemo(
    () =>
      [...(sessions.data?.sessions ?? [])]
        .filter((s) => inviteSessionOk(s, new Date()))
        .sort((a, b) => a.start_at.localeCompare(b.start_at)),
    [sessions.data],
  );
  const tierList = useMemo(
    () => [...(tiers.data?.tiers ?? [])].filter(inviteTierOk).sort((a, b) => a.sort_order - b.sort_order),
    [tiers.data],
  );
  const session = sessionList.find((s) => s.id === sessionId);
  const tier = tierList.find((x) => x.id === tierId);
  const event = eventList.find((e) => e.id === eventId);
  const parsed = useMemo(() => parseGuests(text), [text]);
  const free = tier?.available ?? null;
  const block = sendBlock(parsed, free);

  const changed = () => {
    operation.current = null;
    setPhase("edit");
    setProgress({ total: 0, done: 0, failure: null });
  };

  const send = async (guests: Guest[], from: number) => {
    if (sendLock.current || session === undefined || tier === undefined) return;
    sendLock.current = true;
    if (operation.current === null) operation.current = newOperationId();
    const op = operation.current;
    setPhase("sending");
    setProgress({ total: guests.length, done: from, failure: null });
    let done = from;
    try {
      for (let i = from; i < guests.length; i++) {
        await issueInvitation(orgId, {
          sessionId: session.id,
          tierId: tier.id,
          email: guests[i].email,
          name: guests[i].name,
          batchId: batchId(op, i),
        });
        done = i + 1;
        setProgress({ total: guests.length, done, failure: null });
      }
      setPhase("done");
    } catch (failure) {
      setProgress({ total: guests.length, done, failure });
      setPhase("partial");
    } finally {
      sendLock.current = false;
      // The places, the list and the figures changed whatever happened.
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["invitations", orgId] }),
        queryClient.invalidateQueries({ queryKey: ["tiers", orgId, eventId, sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["sessionSummary", orgId, sessionId] }),
      ]);
    }
  };

  const reset = () => {
    operation.current = null;
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

          {sessionId !== "" && tiers.isLoading && <Loading />}
          {sessionId !== "" && tiers.error !== null && <ErrorBox error={tiers.error} onRetry={() => void tiers.refetch()} />}
          {sessionId !== "" && tiers.data !== undefined && tierList.length === 0 && (
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
              {phase === "edit" && (
                <p>
                  <button
                    type="button"
                    className="btn btn-primary"
                    disabled={block !== null}
                    onClick={() => {
                      operation.current = newOperationId();
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
