/**
 * Builds the event center's tracker from its parts. No import of the API
 * client here: the wiring in `client.ts` passes the session in, so this file
 * and its tests stay free of the app's runtime configuration.
 */
import { createTracker, type Tracker } from "@arena-track";

export const COLLECTOR_PATH = "/v1/me/t";

/** Only the exact value "1" switches the build on (same rule as the other build flags). */
export function analyticsFlagOn(raw: unknown): boolean {
  return raw === "1";
}

export interface FactoryParts {
  /** The VITE_ANALYTICS build flag. */
  flag: unknown;
  apiBaseUrl: string;
  clientChannel: string;
  /** The access token, or null when there is none or it has expired (the batch then waits for a refresh). */
  accessToken: () => string | null;
  /** The signed-in user, so events of a user who signed out are never sent under the next one. */
  userId: () => string | null;
  optedOut: () => boolean;
  orgId: () => string | undefined;
  win?: Window;
}

/** With the flag off the endpoint is empty and the tracker does nothing. */
export function makeTracker(p: FactoryParts): Tracker {
  return createTracker({
    endpoint: analyticsFlagOn(p.flag) ? `${p.apiBaseUrl.replace(/\/+$/, "")}${COLLECTOR_PATH}` : "",
    surface: "event_center",
    enabled: () => !p.optedOut(),
    getIdentity: p.userId,
    getHeaders: () => {
      const token = p.accessToken();
      return token === null ? null : { Authorization: `Bearer ${token}`, "X-Client-Channel": p.clientChannel };
    },
    getContext: () => {
      const org = p.orgId();
      return org === undefined ? {} : { org_id: org };
    },
    win: p.win,
  });
}
