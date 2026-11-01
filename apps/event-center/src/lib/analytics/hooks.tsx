import { useEffect, useMemo } from "react";
import { useRouterState } from "@tanstack/react-router";
import { ApiError } from "@/lib/api/http";
import { trackFlow, trackScreen, trackUiError, type FlowName } from "@/lib/analytics/client";
import { orgIdOf, screenOf } from "@/lib/analytics/screens";

/** Reports the screen on every route change: the route template, and the organization as org_id. Renders nothing. */
export function ScreenTracking() {
  const screen = useRouterState({ select: (s) => screenOf(s.matches) });
  const orgId = useRouterState({ select: (s) => orgIdOf(s.matches) });
  useEffect(() => {
    trackScreen(screen, orgId);
  }, [screen, orgId]);
  return null;
}

/**
 * Marks the steps of one flow. A step is reported once per marker (`mark` twice with the
 * same step is one event). `reset` lets a panel that opens again count a new attempt.
 */
export function createFlowMarker(flow: FlowName, report: (flow: FlowName, step: string) => void = trackFlow) {
  const seen = new Set<string>();
  return {
    mark: (step: string) => {
      if (seen.has(step)) return;
      seen.add(step);
      report(flow, step);
    },
    reset: () => seen.clear(),
  };
}

/** One marker per mounted screen. */
export function useFlow(flow: FlowName): { mark: (step: string) => void; reset: () => void } {
  return useMemo(() => createFlowMarker(flow), [flow]);
}

/** The code of an error as analytics reports it: the API code, or a fixed word, never the message. */
export function uiErrorCode(error: unknown): string {
  return error instanceof ApiError ? error.code : "client.error";
}

/** Reports an error the person is looking at, once per error. */
export function useReportUiError(error: unknown): void {
  useEffect(() => {
    if (error !== null && error !== undefined) trackUiError(uiErrorCode(error));
  }, [error]);
}
