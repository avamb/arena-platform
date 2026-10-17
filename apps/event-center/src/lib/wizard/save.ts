/**
 * Saving the wizard draft (task 6-1c): ONE explicit POST of the event bundle
 * to `POST /v1/organizations/{org}/imports/event-bundle` (no `dry_run`).
 *
 * What the server guarantees, and this file relies on (himports/import_arena.go):
 *  - `publish: false` (always sent by `buildBundle`) makes a DRAFT event and a
 *    DRAFT session: nothing is on sale, nothing is published into a channel;
 *  - `externalRef` is the idempotency key. A repeat call with the same key
 *    EDITS the session it created (200, `created: false`) and never makes a
 *    second event, so a retry after an unknown result is safe as long as it
 *    carries the draft's own key (`web:<org>:<draft id>:1`, `buildBundle`).
 *
 * This release creates ONE date per event: the import is atomic per session
 * only, and a second date needs the first answer's event id. A draft with more
 * dates is refused here (`saveBlockers`), never split into separate events.
 */
import { ApiError } from "@/lib/api/http";
import type { ImportBil24SessionResponse } from "@/lib/api/types";
import { buildBundle, type EventBundle } from "@/lib/wizard/bundle";
import type { WizardDraft } from "@/lib/wizard/draft";

export type SaveBlocker = "oneDate" | "existingVenue";

/** What stops THIS release from saving a draft that is otherwise correct. */
export function saveBlockers(draft: WizardDraft): SaveBlocker[] {
  const out: SaveBlocker[] = [];
  if (draft.dates.length !== 1) out.push("oneDate");
  if (draft.venue.mode !== "existing" || draft.venue.existingId === "") out.push("existingVenue");
  return out;
}

export type SaveOutcome =
  | { kind: "saved"; eventId: string; sessionId: string; created: boolean }
  /** The server looked at the bundle and said no (400, 409, 422): nothing was written, fix and send again. */
  | { kind: "rejected"; status: number; code: string; message: string; requestId?: string }
  /** 401/403: the person may not write here. */
  | { kind: "denied"; status: number; code: string }
  /** No answer, a 5xx or an unreadable answer: the event MAY exist. Retry with the same key, never start a new draft. */
  | { kind: "unknown"; status: number; code: string };

export function classifySaveError(err: unknown): Exclude<SaveOutcome, { kind: "saved" }> {
  if (err instanceof ApiError) {
    const { status, code, message, requestId } = err;
    if (status === 401 || status === 403) return { kind: "denied", status, code };
    if (status >= 400 && status < 500 && status !== 408 && status !== 429) {
      return { kind: "rejected", status, code, message, requestId };
    }
    return { kind: "unknown", status, code };
  }
  return { kind: "unknown", status: 0, code: "network.failure" };
}

export type PostBundle = (bundle: EventBundle) => Promise<ImportBil24SessionResponse>;

export interface Saver {
  /**
   * Sends the draft once. While a request is in flight every further call
   * returns THAT request's promise: a double click cannot make a second POST.
   */
  save(draft: WizardDraft, orgId: string): Promise<SaveOutcome>;
  inFlight(): boolean;
}

export function createSaver(post: PostBundle): Saver {
  let current: Promise<SaveOutcome> | null = null;
  return {
    inFlight: () => current !== null,
    save(draft, orgId) {
      if (current !== null) return current;
      const run = async (): Promise<SaveOutcome> => {
        if (saveBlockers(draft).length > 0) return { kind: "rejected", status: 0, code: "wizard.not_supported", message: "" };
        try {
          const res = await post(buildBundle(draft, { orgId }, 0));
          if (typeof res?.event_id !== "string" || typeof res?.session_id !== "string") {
            return { kind: "unknown", status: 200, code: "http.unreadable_answer" };
          }
          return { kind: "saved", eventId: res.event_id, sessionId: res.session_id, created: res.created !== false };
        } catch (err) {
          return classifySaveError(err);
        }
      };
      current = run().finally(() => {
        current = null;
      });
      return current;
    },
  };
}
