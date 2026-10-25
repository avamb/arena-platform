/**
 * Which event the Telegram mini app opens, and whether the person may touch it
 * (spec 44, RT-12b).
 *
 * The link names an event id and nothing else, and the id comes from the
 * address, so it is untrusted: the page never believes it. It looks the event
 * up through the normal, organization-scoped events list of every organization
 * the person belongs to. An organization that refuses the list (403, 404) is
 * not theirs for this purpose, an event no list holds does not exist for them.
 * The API stays the judge: a save of a foreign event is refused there too.
 */
import { ApiError } from "@/lib/api/http";
import type { EventItem } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n";
import { enterMiniApp, type TgEditDeps, type TgEditKind, type TgEditTarget } from "@/lib/tgEdit";

export interface FoundEvent {
  orgId: string;
  event: EventItem;
}

/**
 * The event and its organization, or null when no organization of the person
 * holds it. A failure that is not a refusal (the network, a 500) is thrown
 * when nothing was found, so it is not mistaken for "no access".
 */
export async function findEvent(
  eventId: string,
  orgIds: readonly string[],
  list: (orgId: string) => Promise<{ events: readonly EventItem[] }>,
): Promise<FoundEvent | null> {
  const answers = await Promise.allSettled(orgIds.map((orgId) => list(orgId)));
  let failure: unknown = null;
  for (let i = 0; i < answers.length; i++) {
    const a = answers[i];
    if (a.status === "fulfilled") {
      const event = a.value.events.find((e) => e.id === eventId);
      if (event !== undefined) return { orgId: orgIds[i], event };
    } else if (!isRefusal(a.reason)) {
      failure ??= a.reason;
    }
  }
  if (failure !== null) throw failure;
  return null;
}

/** A 403 or 404: the organization does not show this person its events. */
export function isRefusal(err: unknown): boolean {
  return err instanceof ApiError && (err.status === 403 || err.status === 404);
}

/** What the page shows once the code is exchanged and the event looked up. */
export type TgEditOpened =
  | { phase: "ready"; kind: TgEditKind; orgId: string; eventId: string }
  | { phase: "noAccess" }
  | { phase: "error"; key: MessageKey };

export interface OpenTgEditDeps extends TgEditDeps {
  /** The organizations the person belongs to (platform roles left out). */
  organizations: () => Promise<readonly string[]>;
  /** The events list of one organization. */
  list: (orgId: string) => Promise<{ events: readonly EventItem[] }>;
}

function failureKey(err: unknown): MessageKey {
  return err instanceof ApiError && err.status === 0 ? "common.error.network" : "common.error.generic";
}

/**
 * Opens the page: the Telegram script, the one-time code, then the event of
 * the link among the person's own organizations. A link without a kind or an
 * event, or an event the person cannot see, ends in a calm message, never in a
 * half-open editor.
 */
export async function openTgEdit(target: TgEditTarget | null, deps: OpenTgEditDeps): Promise<TgEditOpened> {
  const entered = await enterMiniApp(target, deps);
  if (!entered.ok) return { phase: "error", key: entered.key };
  const kind = target?.kind ?? null;
  const eventId = target?.eventId ?? null;
  if (kind === null || eventId === null) return { phase: "error", key: "tgEdit.error.missing" };
  try {
    const found = await findEvent(eventId, await deps.organizations(), deps.list);
    return found === null ? { phase: "noAccess" } : { phase: "ready", kind, orgId: found.orgId, eventId };
  } catch (err) {
    return { phase: "error", key: failureKey(err) };
  }
}

/**
 * The CANONICAL description of an event: the text stored on the event itself, which is what the editor reads
 * and writes back. `description` of the events list is resolved for the request's language and may be a
 * translation; saving that over the event would lose the original. A server that does not send
 * `description_base` yet has no translation to mix up, so `description` is then the only text.
 */
export function descriptionBaseOf(event: Pick<EventItem, "description" | "description_base" | "description_translated">): string | null {
  if (event.description_base !== undefined) return event.description_base;
  if (event.description_translated === true) return null;
  return event.description ?? null;
}
