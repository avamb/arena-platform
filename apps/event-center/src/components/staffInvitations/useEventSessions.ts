/**
 * The dates of one event with the venue's time zone of each, for the pickers of
 * the organization-wide invitations screen (the issue form and the list
 * filter). The zone comes from the event summary (needs `order.read`); without
 * it the dates are labelled UTC rather than guessed.
 */
import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchEventSummary, listSessions } from "@/lib/api/endpoints";
import type { SessionItem } from "@/lib/api/types";
import { useOrg } from "@/screens/OrgLayout";

export function useEventSessions(orgId: string, eventId: string) {
  const { access } = useOrg();
  const sessions = useQuery({
    queryKey: ["sessions", orgId, eventId],
    queryFn: () => listSessions(orgId, eventId),
    enabled: eventId !== "",
  });
  const summary = useQuery({
    queryKey: ["eventSummary", orgId, eventId],
    queryFn: () => fetchEventSummary(orgId, eventId),
    enabled: eventId !== "" && access.can("order.read"),
  });
  const sorted = useMemo<SessionItem[]>(
    () => [...(sessions.data?.sessions ?? [])].sort((a, b) => a.start_at.localeCompare(b.start_at)),
    [sessions.data],
  );
  const zoneOf = (sessionId: string): string | null =>
    summary.data?.sessions.find((x) => x.id === sessionId)?.venue_timezone ?? null;
  return { sessions, sorted, zoneOf };
}
