/**
 * Where a sales link leads (spec 43, QL-12): the bot's first question of the
 * link dialog as a form. An event, one date of an event, the page with all
 * events of the organization, a promoter page (the promoters the events
 * name) or any http(s) address. The parent gets the target and a sentence
 * that names it, for the card and the retarget question.
 */
import { useEffect, useId, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchEventSummary, listSessions } from "@/lib/api/endpoints";
import type { EventItem, SalesLinkTarget } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { parseUrl, type TargetKind } from "@/lib/salesLinks";
import { Loading } from "@/components/ui";
import { useEventsList } from "@/screens/EventsScreen";

export interface TargetChoice {
  target: SalesLinkTarget | null;
  /** The target in words: "Event: Swan Lake", "Date of Swan Lake: Fri 12 Dec 19:00 (Madrid)". */
  text: string;
}

interface Props {
  orgId: string;
  orgName: string;
  /** The target to start from (a card's link, or the event/session the person came from). */
  initial: SalesLinkTarget | null;
  onChange: (choice: TargetChoice) => void;
  canSales: boolean;
  disabled?: boolean;
  /** Shown under the address field when the typed address does not fit. */
  urlError?: string;
}

/** The events a link may lead to: not archived and not canceled (a draft stays: it leads to the general page until published). */
export function linkableEvents(events: readonly EventItem[]): EventItem[] {
  return events.filter((e) => e.status === "draft" || e.status === "published");
}

interface PromoterRef {
  id: string;
  name: string;
}

/** The promoters the events name, each once. */
export function promotersOf(events: readonly EventItem[]): PromoterRef[] {
  const seen = new Map<string, string>();
  for (const e of events) {
    if (typeof e.promoter_id === "string" && e.promoter_id !== "" && !seen.has(e.promoter_id)) {
      seen.set(e.promoter_id, e.promoter_name ?? "");
    }
  }
  return [...seen.entries()].map(([id, name]) => ({ id, name }));
}

export function SalesLinkTargetPicker({ orgId, orgName, initial, onChange, canSales, disabled = false, urlError }: Props) {
  const { t, locale } = useI18n();
  const ids = useId();
  const [kind, setKind] = useState<TargetKind>(initial?.kind as TargetKind | undefined ?? "event");
  const [eventId, setEventId] = useState<string>(initial?.event_id ?? "");
  const [sessionId, setSessionId] = useState<string>(initial?.session_id ?? "");
  const [promoterId, setPromoterId] = useState<string>(initial?.promoter_id ?? "");
  const [url, setUrl] = useState<string>(initial?.url ?? "");

  const events = useEventsList(orgId);
  const all = useMemo(() => linkableEvents(events.data?.events ?? []), [events.data]);
  const promoters = useMemo(() => promotersOf(events.data?.events ?? []), [events.data]);
  const needSessions = kind === "session" && eventId !== "";
  const sessions = useQuery({
    queryKey: ["sessions", orgId, eventId],
    queryFn: () => listSessions(orgId, eventId),
    enabled: needSessions,
  });
  // The dates are shown in the venue's zone, which only the summary carries (order.read).
  const summary = useQuery({
    queryKey: ["eventSummary", orgId, eventId],
    queryFn: () => fetchEventSummary(orgId, eventId),
    enabled: needSessions && canSales,
  });
  const liveSessions = useMemo(
    () => (sessions.data?.sessions ?? []).filter((s) => s.status !== "cancelled").sort((a, b) => a.start_at.localeCompare(b.start_at)),
    [sessions.data],
  );
  const zoneOf = (sid: string): string | null => summary.data?.sessions.find((s) => s.id === sid)?.venue_timezone ?? null;
  const eventName = (id: string) => all.find((e) => e.id === id)?.name ?? events.data?.events.find((e) => e.id === id)?.name ?? "";

  // A session that is not of the chosen event is dropped when the event changes.
  useEffect(() => {
    if (sessions.data === undefined || sessionId === "") return;
    if (!sessions.data.sessions.some((s) => s.id === sessionId)) setSessionId("");
  }, [sessions.data, sessionId]);

  useEffect(() => {
    let target: SalesLinkTarget | null = null;
    let text = "";
    switch (kind) {
      case "event":
        if (eventId !== "") {
          target = { kind: "event", event_id: eventId };
          text = t("links.where.event", { name: eventName(eventId) });
        }
        break;
      case "session":
        if (eventId !== "" && sessionId !== "") {
          target = { kind: "session", session_id: sessionId, event_id: eventId };
          const s = liveSessions.find((x) => x.id === sessionId);
          text = t("links.where.session", { name: eventName(eventId), when: s !== undefined ? formatDateTime(s.start_at, zoneOf(s.id), locale) : "" });
        }
        break;
      case "org_page":
        target = { kind: "org_page" };
        text = t("links.t.org", { org: orgName });
        break;
      case "promoter_page":
        if (promoterId !== "") {
          target = { kind: "promoter_page", promoter_id: promoterId };
          text = `${t("links.where.promoter")}: ${promoters.find((p) => p.id === promoterId)?.name ?? ""}`;
        }
        break;
      case "url": {
        const parsed = parseUrl(url);
        if (parsed.ok) {
          target = { kind: "url", url: parsed.value };
          text = t("links.where.url", { url: parsed.value });
        }
        break;
      }
    }
    onChange({ target, text });
    // The parent's onChange is a stable setter; the names depend on the loaded lists.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, eventId, sessionId, promoterId, url, all, liveSessions, summary.data, promoters, locale, orgName]);

  const kinds: TargetKind[] = ["event", "session", "org_page", ...(promoters.length > 0 ? (["promoter_page"] as TargetKind[]) : []), "url"];
  const kindLabel = (k: TargetKind): string => {
    switch (k) {
      case "event":
        return t("links.t.event");
      case "session":
        return t("links.t.session");
      case "org_page":
        return t("links.t.org", { org: orgName });
      case "promoter_page":
        return t("links.t.promoter");
      default:
        return t("links.t.url");
    }
  };

  const eventSelect = (
    <>
      <label htmlFor={`${ids}-event`}>{t("links.t.pickEvent")}</label>
      {events.isLoading && <Loading />}
      {events.data !== undefined && all.length === 0 && <p className="muted">{t("links.t.noEvents")}</p>}
      <select id={`${ids}-event`} value={eventId} onChange={(e) => setEventId(e.target.value)} disabled={disabled} data-testid="link-target-event">
        <option value="">{t("links.t.choose")}</option>
        {all.map((e) => (
          <option key={e.id} value={e.id}>
            {e.name}
          </option>
        ))}
      </select>
    </>
  );

  return (
    <fieldset className="link-target" data-testid="link-target">
      <legend>{t("links.t.title")}</legend>
      {kinds.map((k) => (
        <label key={k} className="check">
          <input type="radio" name={`${ids}-kind`} checked={kind === k} onChange={() => setKind(k)} disabled={disabled} data-testid={`link-kind-${k}`} />
          <span>{kindLabel(k)}</span>
        </label>
      ))}
      {kind === "event" && eventSelect}
      {kind === "session" && (
        <>
          {eventSelect}
          {eventId !== "" && (
            <>
              <label htmlFor={`${ids}-session`}>{t("links.t.pickSession")}</label>
              {sessions.isLoading && <Loading />}
              {sessions.data !== undefined && liveSessions.length === 0 && <p className="muted">{t("links.t.noSessions")}</p>}
              <select id={`${ids}-session`} value={sessionId} onChange={(e) => setSessionId(e.target.value)} disabled={disabled} data-testid="link-target-session">
                <option value="">{t("links.t.choose")}</option>
                {liveSessions.map((s) => (
                  <option key={s.id} value={s.id}>
                    {formatDateTime(s.start_at, zoneOf(s.id), locale)}
                  </option>
                ))}
              </select>
            </>
          )}
        </>
      )}
      {kind === "promoter_page" && (
        <>
          <label htmlFor={`${ids}-promoter`}>{t("links.t.pickPromoter")}</label>
          <select id={`${ids}-promoter`} value={promoterId} onChange={(e) => setPromoterId(e.target.value)} disabled={disabled} data-testid="link-target-promoter">
            <option value="">{t("links.t.choose")}</option>
            {promoters.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </>
      )}
      {kind === "url" && (
        <>
          <label htmlFor={`${ids}-url`}>{t("links.t.pickUrl")}</label>
          <input
            id={`${ids}-url`}
            type="url"
            inputMode="url"
            autoComplete="off"
            spellCheck={false}
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            disabled={disabled}
            aria-describedby={`${ids}-url-hint`}
            aria-invalid={urlError !== undefined}
            data-testid="link-target-url"
          />
          <p id={`${ids}-url-hint`} className="muted">
            {t("links.t.urlHint")}
          </p>
          {urlError !== undefined && (
            <p className="field-error" role="alert">
              {urlError}
            </p>
          )}
        </>
      )}
    </fieldset>
  );
}
