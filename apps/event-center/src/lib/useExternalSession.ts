/**
 * Spec 63, L-3: is this date sold by a partner box office? The seat, guest and
 * invitation screens of such a date have nothing to show, so they ask first.
 * It reads the sessions list the event card already holds in the query cache.
 */
import { useQuery } from "@tanstack/react-query";
import { listSessions } from "@/lib/api/endpoints";
import { isExternalSession } from "@/lib/externalSale";

export function useIsExternalSession(orgId: string, eventId: string, sessionId: string, enabled: boolean): boolean {
  const sessions = useQuery({
    queryKey: ["sessions", orgId, eventId],
    queryFn: () => listSessions(orgId, eventId),
    enabled,
  });
  return isExternalSession(sessions.data?.sessions.find((s) => s.id === sessionId));
}
