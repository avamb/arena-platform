/**
 * Every request the admin console makes names itself with
 * `X-Client-Channel: admin_web`. The API stamps it on the audit trail and, on
 * sign-in and token refresh, refuses a user who is not Arena platform staff
 * (403 `auth.console_staff_only`): the console is for staff only, organizers
 * work in the web event center.
 */
export const CLIENT_CHANNEL_HEADER = "X-Client-Channel";
export const CLIENT_CHANNEL = "admin_web";

/** The header as a record, for spreading into a `headers` object. */
export const clientChannelHeaders: Readonly<Record<string, string>> = {
  [CLIENT_CHANNEL_HEADER]: CLIENT_CHANNEL,
};

/** The web event center organizers use instead of this console. */
export function eventCenterUrl(): string {
  const raw: unknown = import.meta.env.VITE_EVENT_CENTER_URL;
  return typeof raw === "string" && raw.trim().length > 0
    ? raw.trim().replace(/\/+$/, "")
    : "https://events.arenasoldout.com";
}

/** Error code of the server's refusal of a non-staff console sign-in. */
export const CONSOLE_STAFF_ONLY_CODE = "auth.console_staff_only";

/** What a non-staff user is told, on the login page and on the gate screen. */
export const CONSOLE_STAFF_ONLY_MESSAGE =
  "This console is for Arena staff. Organizers work in the event center.";
