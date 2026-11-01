/**
 * The tracker of this tab and the few calls screens make (spec 50, 4.1).
 * Everything is a no-op unless the build has VITE_ANALYTICS=1 and the person
 * has not opted out. A screen names a step; it never passes a value.
 */
import type { Tracker } from "@arena-track";
import { config } from "@/lib/config";
import { CLIENT_CHANNEL, session } from "@/lib/api/http";
import { analyticsFlagOn, makeTracker } from "@/lib/analytics/factory";
import { isOptedOut } from "@/lib/analytics/optOut";

/**
 * First-wave flows of spec 50, section 5. `payment_connect` and `onboarding_profile`
 * have no screen in the event center yet.
 */
export type FlowName = "event_create" | "event_edit" | "refund" | "member_invite" | "sales_link_create";

let tracker: Tracker | null = null;
let currentOrg: string | undefined;
let currentScreen = "";

/** True when this build collects anything at all (the toggle is shown only then). */
export function analyticsAvailable(): boolean {
  return analyticsFlagOn(import.meta.env.VITE_ANALYTICS);
}

function getTracker(): Tracker | null {
  if (tracker === null && typeof window !== "undefined") {
    tracker = makeTracker({
      flag: import.meta.env.VITE_ANALYTICS,
      apiBaseUrl: config.apiBaseUrl,
      clientChannel: CLIENT_CHANNEL,
      accessToken: () => session.getAccessToken(),
      optedOut: isOptedOut,
      orgId: () => currentOrg,
    });
  }
  return tracker;
}

/** A screen was opened. The organization changes first so the batch of the previous screen keeps its own. */
export function trackScreen(screen: string, orgId: string | undefined): void {
  const t = getTracker();
  if (t === null) return;
  if (orgId !== currentOrg) {
    t.flush();
    currentOrg = orgId;
  }
  currentScreen = screen;
  t.view(screen);
}

export function trackFlow(flow: FlowName, step: string): void {
  getTracker()?.flowStep(flow, step);
}

/** An error was shown to the person. `code` is the API error code, never its message. */
export function trackUiError(code: string): void {
  if (currentScreen !== "") getTracker()?.error(currentScreen, code);
}

/** Test seam: forget the tracker of the module. */
export function resetAnalyticsForTests(): void {
  tracker?.destroy();
  tracker = null;
  currentOrg = undefined;
  currentScreen = "";
}
