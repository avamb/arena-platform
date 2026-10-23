import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { ImportBil24SessionResponse } from "@/lib/api/types";
import type { EventBundle } from "@/lib/wizard/bundle";
import { clearDraftIfSaved, newDraft, reduceDraft, saveDraft, loadDraft, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";
import { classifySaveError, createSaver, saveBlockers } from "@/lib/wizard/save";

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
  it("lets a one-date draft with an existing venue through", () => {
    expect(saveBlockers(complete())).toEqual([]);
  });
  it("refuses two dates and a venue that is not chosen from the list", () => {
    expect(saveBlockers(complete({ type: "dateAdd", key: "d2", defaultName: "Admission" }))).toEqual(["oneDate"]);
    expect(saveBlockers(complete({ type: "venueMode", mode: "new" }))).toEqual(["existingVenue"]);
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
    expect(out).toEqual({ kind: "saved", eventId: ok.event_id, sessionId: ok.session_id, created: true });
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

  it("never posts a draft this release cannot save", async () => {
    const post = vi.fn(async () => ok);
    const out = await createSaver(post).save(complete({ type: "dateAdd", key: "d2", defaultName: "A" }), "org-1");
    expect(out.kind).toBe("rejected");
    expect(post).not.toHaveBeenCalled();
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
