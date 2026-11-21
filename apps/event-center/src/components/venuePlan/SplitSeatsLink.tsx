/**
 * "Split seats by price" (spec 64 section 2.4): a link to the seat manager of a
 * date, opened on its layout tab (`splitSeatsByPricePath`). A plain click moves
 * inside the app without a reload. A click with a modifier key or the middle
 * button keeps the browser's own behaviour (a new tab).
 */
import { useRouter } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { splitSeatsByPricePath } from "@/lib/seatManager/embed";

export function SplitSeatsLink({ orgId, eventId, sessionId, children, testId = "intake-split" }: { orgId: string; eventId: string; sessionId: string; children: ReactNode; testId?: string }) {
  const router = useRouter();
  const href = splitSeatsByPricePath(orgId, eventId, sessionId);
  return (
    <a
      href={href}
      className="btn btn-primary"
      data-testid={testId}
      onClick={(e) => {
        if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        router.history.push(href);
      }}
    >
      {children}
    </a>
  );
}
