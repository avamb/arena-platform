import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { ImportBil24SessionResponse } from "@/lib/api/types";
import type { EventBundle } from "@/lib/wizard/bundle";
import { clearDraftIfSaved, newDraft, reduceDraft, saveDraft, loadDraft, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";
import { classifySaveError, createSaver, newVenueBody, saveBlockers, type VenueApi } from "@/lib/wizard/save";

const NOW = new Date("2026-10-16T10:00:00Z");
const VENUE = { id: "v-1", name: "E2E Hall", timezone: "Europe/Madrid", capacity: 200, country: "ES" };

function complete(...extra: DraftAction[]): WizardDraft {
  const actions: DraftAction[] = [
    { type: "event", field: "name", value: "Swan Lake" },
    { type: "venueMode", mode: "existing" },
    { type: "venueExisting", venue: VENUE },
    { type: "currency", value: "eur", source: "typed" },
    { type: "dateDay", key: "d1", value: "2026-12-05" },
    { type: "dateField", key: "d1", field: "time", value: "19:00" },
    { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "18,90" },
    ...extra,
  ];
  return actions.reduce((d, a) => reduceDraft(d, a, NOW), newDraft("draft-1", NOW, "Admission"));
}

const ok: ImportBil24SessionResponse = {
  event_id: "11111111-1111-4111-8111-111111111111",
  session_id: "22222222-2222-4222-8222-222222222222",
  created: true,
} as ImportBil24SessionResponse;

describe("saveBlockers", () => {
  it("lets several dates and a new venue through (F-004)", () => {
    expect(saveBlockers(complete(), false)).toEqual([]);
    expect(saveBlockers(complete({ type: "dateAdd", key: "d2", defaultName: "Admission" }), false)).toEqual([]);
    expect(saveBlockers(complete({ type: "venueMode", mode: "new" }), true)).toEqual([]);
  });
  it("stops a new venue for a role that may not create one", () => {
    expect(saveBlockers(complete({ type: "venueMode", mode: "new" }), false)).toEqual(["venueCreate"]);
  });
});

describe("classifySaveError", () => {
  it("tells a refusal, a denial and an unknown result apart", () => {
    expect(classifySaveError(new ApiError(422, "import.invalid_category", "bad")).kind).toBe("rejected");
    expect(classifySaveError(new ApiError(409, "import.external_ref_conflict", "x")).kind).toBe("rejected");
    expect(classifySaveError(new ApiError(403, "auth.forbidden", "x")).kind).toBe("denied");
    expect(classifySaveError(new ApiError(401, "auth.unauthorized", "x")).kind).toBe("denied");
    for (const status of [0, 408, 429, 500, 502, 503, 504]) {
      expect(classifySaveError(new ApiError(status, "x", "x")).kind, String(status)).toBe("unknown");
    }
    expect(classifySaveError(new TypeError("boom")).kind).toBe("unknown");
  });
});

describe("createSaver", () => {
  it("creates once and reports the saved ids; the bundle is an unpublished arena bundle with the draft's key", async () => {
    const post = vi.fn(async (_b: EventBundle) => ok);
    const out = await createSaver(post).save(complete(), "org-1");
    expect(out).toEqual({ kind: "saved", eventId: ok.event_id, sessionId: ok.session_id, sessionIds: [ok.session_id], created: true });
    expect(post).toHaveBeenCalledTimes(1);
    const b = post.mock.calls[0][0];
    expect(b.publish).toBe(false);
    expect(b.source).toBe("arena");
    expect(b.externalRef).toBe("web:org-1:draft-1:1");
    expect(b.actionEvent.currency).toBe("EUR");
    expect(b.categoryList).toHaveLength(1);
    expect(b.categoryList[0]).toMatchObject({ categoryPriceName: "Admission", price: 18.9, availability: 200 });
  });

  it("sends ONE request for a double click: the second call gets the first call's answer", async () => {
    let release: (r: ImportBil24SessionResponse) => void = () => undefined;
    const post = vi.fn(() => new Promise<ImportBil24SessionResponse>((r) => (release = r)));
    const saver = createSaver(post);
    const first = saver.save(complete(), "org-1");
    const second = saver.save(complete(), "org-1");
    expect(saver.inFlight()).toBe(true);
    release(ok);
    expect(await first).toEqual(await second);
    expect(post).toHaveBeenCalledTimes(1);
    expect(saver.inFlight()).toBe(false);
  });

  it("a refusal and an unknown result both leave the saver free, and a retry carries the SAME key", async () => {
    const post = vi
      .fn<(b: EventBundle) => Promise<ImportBil24SessionResponse>>()
      .mockRejectedValueOnce(new ApiError(0, "network.timeout", "slow"))
      .mockRejectedValueOnce(new ApiError(422, "import.invalid_category", "bad"))
      .mockResolvedValueOnce({ ...ok, created: false });
    const saver = createSaver(post);
    const draft = complete();
    expect((await saver.save(draft, "org-1")).kind).toBe("unknown");
    expect((await saver.save(draft, "org-1")).kind).toBe("rejected");
    const last = await saver.save(draft, "org-1");
    expect(last).toMatchObject({ kind: "saved", created: false });
    const keys = post.mock.calls.map((c) => c[0].externalRef);
    expect(new Set(keys).size).toBe(1);
  });

  it("an answer without ids is an unknown result, not a success", async () => {
    const out = await createSaver(async () => ({}) as ImportBil24SessionResponse).save(complete(), "org-1");
    expect(out.kind).toBe("unknown");
  });

  it("never posts a new venue without the venue API", async () => {
    const post = vi.fn(async () => ok);
    const out = await createSaver(post).save(newVenueDraft(), "org-1");
    expect(out.kind).toBe("rejected");
    expect(post).not.toHaveBeenCalled();
  });
});

const SECOND: DraftAction[] = [
  { type: "dateAdd", key: "d2", defaultName: "Admission" },
  { type: "dateDay", key: "d2", value: "2026-12-06" },
  { type: "dateField", key: "d2", field: "time", value: "18:00" },
];

function answer(n: number): ImportBil24SessionResponse {
  return { event_id: ok.event_id, session_id: `33333333-3333-4333-8333-33333333333${n}`, created: n === 1 } as ImportBil24SessionResponse;
}

describe("several dates (F-004)", () => {
  it("posts every date in order with its own key; later dates join the first date's event", async () => {
    let n = 0;
    const post = vi.fn(async (_b: EventBundle) => answer(++n));
    const out = await createSaver(post).save(complete(...SECOND), "org-1");
    expect(post).toHaveBeenCalledTimes(2);
    const [first, second] = post.mock.calls.map((c) => c[0]);
    expect(first.externalRef).toBe("web:org-1:draft-1:1");
    expect(second.externalRef).toBe("web:org-1:draft-1:2");
    expect(first.action.arenaEventId).toBeUndefined();
    expect(second.action.arenaEventId).toBe(ok.event_id);
    expect(out).toMatchObject({ kind: "saved", eventId: ok.event_id, sessionIds: [answer(1).session_id, answer(2).session_id], created: true });
  });

  it("says how far a save got, and a retry repeats every date with the SAME keys", async () => {
    const post = vi
      .fn<(b: EventBundle) => Promise<ImportBil24SessionResponse>>()
      .mockResolvedValueOnce(answer(1))
      .mockRejectedValueOnce(new ApiError(422, "import.invalid_category", "bad"))
      .mockResolvedValueOnce({ ...answer(1), created: false })
      .mockResolvedValueOnce(answer(2));
    const saver = createSaver(post);
    const draft = complete(...SECOND);
    const failed = await saver.save(draft, "org-1");
    expect(failed).toMatchObject({ kind: "rejected", progress: { step: "date", dateIndex: 1, savedDates: 1, eventId: ok.event_id } });
    const again = await saver.save(draft, "org-1");
    expect(again.kind).toBe("saved");
    expect(post.mock.calls.map((c) => c[0].externalRef)).toEqual([
      "web:org-1:draft-1:1",
      "web:org-1:draft-1:2",
      "web:org-1:draft-1:1",
      "web:org-1:draft-1:2",
    ]);
  });

  it("an answer that names another event is an unknown result", async () => {
    const post = vi
      .fn<(b: EventBundle) => Promise<ImportBil24SessionResponse>>()
      .mockResolvedValueOnce(answer(1))
      .mockResolvedValueOnce({ ...answer(2), event_id: "44444444-4444-4444-8444-444444444444" });
    const out = await createSaver(post).save(complete(...SECOND), "org-1");
    expect(out).toMatchObject({ kind: "unknown", progress: { dateIndex: 1, savedDates: 1 } });
  });
});

function newVenueDraft(): WizardDraft {
  return complete(
    { type: "venueMode", mode: "new" },
    { type: "venueNew", field: "newName", value: "  Sala Apolo " },
    { type: "country", iso2: "ES" },
    { type: "venueNew", field: "cityName", value: "Barcelona" },
    { type: "timezone", value: "Europe/Madrid" },
    { type: "venueNew", field: "capacity", value: "300" },
    { type: "venueNew", field: "address", value: "Carrer Nou de la Rambla 113" },
  );
}

const CREATED = { id: "v-new", name: "Sala Apolo", timezone: "Europe/Madrid", capacity: 300, country: "ES" };
type Choice = typeof CREATED;

function fakeVenues(over: Partial<VenueApi> = {}) {
  return {
    listVenues: vi.fn(async (): Promise<Choice[]> => []),
    countryId: vi.fn(async (iso2: string) => (iso2 === "ES" ? "country-es" : null)),
    createCity: vi.fn(async () => "city-bcn"),
    createVenue: vi.fn(async () => CREATED),
    ...over,
  };
}

describe("a new venue (F-004)", () => {
  it("creates the city and the venue with the bot's fields, reports it once, then sends the dates by its id", async () => {
    const venues = fakeVenues();
    const post = vi.fn(async (_b: EventBundle) => ok);
    const onVenue = vi.fn();
    const out = await createSaver({ post, venues }).save(newVenueDraft(), "org-1", { onVenue });
    expect(out.kind).toBe("saved");
    expect(venues.createCity).toHaveBeenCalledWith("org-1", "country-es", "Barcelona");
    expect(venues.createVenue).toHaveBeenCalledWith("org-1", {
      name: "Sala Apolo",
      city_id: "city-bcn",
      country: "ES",
      timezone: "Europe/Madrid",
      status: "active",
      address_line1: "Carrer Nou de la Rambla 113",
      capacity_default: 300,
    });
    expect(onVenue).toHaveBeenCalledTimes(1);
    expect(onVenue).toHaveBeenCalledWith(CREATED);
    const b = post.mock.calls[0][0];
    expect(b.venue).toEqual({ arenaVenueId: "v-new", timezone: "Europe/Madrid" });
    expect(b.categoryList[0].availability).toBe(300);
  });

  it("reuses a venue of the same name and creates nothing", async () => {
    const venues = fakeVenues({ listVenues: vi.fn(async () => [{ ...CREATED, name: "sala apolo" }]) });
    const out = await createSaver({ post: async () => ok, venues }).save(newVenueDraft(), "org-1");
    expect(out.kind).toBe("saved");
    expect(venues.createCity).not.toHaveBeenCalled();
    expect(venues.createVenue).not.toHaveBeenCalled();
  });

  it("takes the venue another request made meanwhile (409 venue.duplicate)", async () => {
    const venues = fakeVenues({
      listVenues: vi.fn<() => Promise<Choice[]>>().mockResolvedValueOnce([]).mockResolvedValueOnce([CREATED]),
      createVenue: vi.fn(async (): Promise<Choice> => {
        throw new ApiError(409, "venue.duplicate", "dup");
      }),
    });
    const onVenue = vi.fn();
    const out = await createSaver({ post: async () => ok, venues }).save(newVenueDraft(), "org-1", { onVenue });
    expect(out.kind).toBe("saved");
    expect(onVenue).toHaveBeenCalledWith(CREATED);
  });

  it("sends a country outside the reference with the venue (country_iso2 + city_name), with no city call and no city_id", async () => {
    const venues = fakeVenues({ countryId: vi.fn(async () => null) });
    const out = await createSaver({ post: async () => ok, venues }).save(newVenueDraft(), "org-1");
    expect(out.kind).toBe("saved");
    expect(venues.createCity).not.toHaveBeenCalled();
    expect(venues.createVenue).toHaveBeenCalledWith("org-1", {
      name: "Sala Apolo",
      country_iso2: "ES",
      city_name: "Barcelona",
      country: "ES",
      timezone: "Europe/Madrid",
      status: "active",
      address_line1: "Carrer Nou de la Rambla 113",
      capacity_default: 300,
    });
  });

  it("stops before any date when a venue call fails", async () => {
    const post = vi.fn(async () => ok);
    const down = await createSaver({
      post,
      venues: fakeVenues({
        createVenue: vi.fn(async (): Promise<Choice> => {
          throw new ApiError(503, "dependency.database_unavailable", "down");
        }),
      }),
    }).save(newVenueDraft(), "org-1");
    expect(down).toMatchObject({ kind: "unknown", progress: { step: "venue" } });
    const denied = await createSaver({
      post,
      venues: fakeVenues({
        createCity: vi.fn(async (): Promise<string> => {
          throw new ApiError(403, "permissions.denied", "no");
        }),
      }),
    }).save(newVenueDraft(), "org-1");
    expect(denied).toMatchObject({ kind: "denied", progress: { step: "venue" } });
    expect(post).not.toHaveBeenCalled();
  });

  it("leaves the address and the seats out when they are empty", () => {
    const d = reduceDraft(reduceDraft(newVenueDraft(), { type: "venueNew", field: "address", value: " " }), {
      type: "venueNew",
      field: "capacity",
      value: "",
    });
    const body = newVenueBody(d, "city-1");
    expect(body.address_line1).toBeUndefined();
    expect(body.capacity_default).toBeUndefined();
  });
});

describe("draft retention", () => {
  function store() {
    const m = new Map<string, string>();
    return {
      getItem: (k: string) => m.get(k) ?? null,
      setItem: (k: string, v: string) => void m.set(k, v),
      removeItem: (k: string) => void m.delete(k),
      size: () => m.size,
    };
  }
  it("keeps the draft after a failed save and removes only the saved one after a success", () => {
    const s = store();
    saveDraft("org-1", complete(), s);
    expect(loadDraft("org-1", s)?.id).toBe("draft-1");
    // A failed save never calls the clear: the draft is still there.
    expect(loadDraft("org-1", s)?.event.name).toBe("Swan Lake");
    // Another tab's newer draft (another id) is not the saved one.
    expect(clearDraftIfSaved("org-1", "some-other-draft", s)).toBe(false);
    expect(s.size()).toBe(1);
    expect(clearDraftIfSaved("org-1", "draft-1", s)).toBe(true);
    expect(s.size()).toBe(0);
  });
});
