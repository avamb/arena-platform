/**
 * Poster-first creation: the stored decision ("poster" / "continue without") of
 * the org-scoped draft, and the safe upgrade of version-2 and version-3 drafts.
 */
import { describe, expect, it } from "vitest";
import {
  DRAFT_VERSION,
  draftStorageKey,
  hasPosterRefs,
  loadDraft,
  newDraft,
  parseDraft,
  recordPosterProgress,
  reduceDraft,
  saveDraft,
  type DraftAction,
  type WizardDraft,
} from "@/lib/wizard/draft";

const NOW = new Date("2026-10-17T10:00:00Z");
const MEDIA = "0199aaaa-0000-7000-8000-000000000001";
const MEDIA2 = "0199aaaa-0000-7000-8000-000000000003";
const EVENT = "0199bbbb-0000-7000-8000-000000000002";

function memoryStorage() {
  const data = new Map<string, string>();
  return {
    data,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
  };
}

const typed: WizardDraft = (
  [
    { type: "event", field: "name", value: "Swan Lake" },
    { type: "event", field: "description", value: "Ballet in two acts" },
    { type: "dateDay", key: "d1", value: "2027-03-05" },
    { type: "dateField", key: "d1", field: "time", value: "19:30" },
  ] as DraftAction[]
).reduce((d, a) => reduceDraft(d, a, NOW), newDraft("d1", NOW));

/** A draft as the previous releases stored it (no `posterDecision`). */
function stored(version: 2 | 3, poster?: { mediaId: string; eventId: string }): Record<string, unknown> {
  const copy: Record<string, unknown> = { ...typed, version };
  delete copy.posterDecision;
  if (version === 2) delete copy.poster;
  else copy.poster = poster ?? { mediaId: "", eventId: "" };
  return copy;
}

describe("the decision in the reducer", () => {
  it("starts undecided", () => {
    expect(newDraft("x", NOW).posterDecision).toBe("");
  });
  it("an uploaded picture is the decision to give a poster", () => {
    const d = reduceDraft(newDraft("x", NOW), { type: "posterUploaded", mediaId: MEDIA }, NOW);
    expect(d.posterDecision).toBe("poster");
    expect(d.poster.mediaId).toBe(MEDIA);
  });
  it("skipping is a conscious decision and drops an uploaded id", () => {
    const up = reduceDraft(newDraft("x", NOW), { type: "posterUploaded", mediaId: MEDIA }, NOW);
    const d = reduceDraft(up, { type: "posterSkipped" }, NOW);
    expect(d.posterDecision).toBe("skipped");
    expect(d.poster.mediaId).toBe("");
  });
  it("removing or changing the picture goes back to undecided, never to 'skipped'", () => {
    const up = reduceDraft(newDraft("x", NOW), { type: "posterUploaded", mediaId: MEDIA }, NOW);
    expect(reduceDraft(up, { type: "posterCleared" }, NOW).posterDecision).toBe("");
    const skipped = reduceDraft(newDraft("x", NOW), { type: "posterSkipped" }, NOW);
    // Choosing a file after skipping clears the skip until the upload finishes.
    expect(reduceDraft(skipped, { type: "posterCleared" }, NOW).posterDecision).toBe("");
  });
  it("typed work is untouched by any poster decision", () => {
    const skipped = reduceDraft(typed, { type: "posterSkipped" }, NOW);
    expect(skipped.event).toEqual(typed.event);
    expect(skipped.dates).toEqual(typed.dates);
  });
});

describe("storage of the decision", () => {
  it("round-trips 'skipped' (and a skip alone is worth keeping)", () => {
    const store = memoryStorage();
    const d = reduceDraft(newDraft("x", NOW), { type: "posterSkipped" }, NOW);
    expect(hasPosterRefs(d)).toBe(true);
    saveDraft("org1", d, store);
    expect(store.data.has(draftStorageKey("org1"))).toBe(true);
    expect(loadDraft("org1", store)?.posterDecision).toBe("skipped");
  });
  it("is kept per organization", () => {
    const store = memoryStorage();
    saveDraft("orgA", reduceDraft(typed, { type: "posterSkipped" }, NOW), store);
    saveDraft("orgB", reduceDraft(typed, { type: "posterUploaded", mediaId: MEDIA }, NOW), store);
    expect(loadDraft("orgA", store)?.posterDecision).toBe("skipped");
    expect(loadDraft("orgB", store)?.posterDecision).toBe("poster");
    expect(loadDraft("orgB", store)?.poster.mediaId).toBe(MEDIA);
  });
  it("stores the ids and the decision only: no file, blob address, link or model text", () => {
    const store = memoryStorage();
    saveDraft("o", reduceDraft(typed, { type: "posterUploaded", mediaId: MEDIA }, NOW), store);
    const raw = store.data.get(draftStorageKey("o")) as string;
    expect(raw).not.toMatch(/blob:|https?:|token|facts|suggest/i);
    expect(Object.keys(JSON.parse(raw).poster).sort()).toEqual(["eventId", "mediaId"]);
  });
  it("rejects an unknown decision and a newer version", () => {
    expect(parseDraft({ ...typed, posterDecision: "later" })).toBeNull();
    expect(parseDraft({ ...typed, version: DRAFT_VERSION + 1 })).toBeNull();
  });
  it("'poster' without any picture (a reload cannot bring the file back) asks again", () => {
    expect(parseDraft({ ...typed, posterDecision: "poster" })?.posterDecision).toBe("");
  });
  it("a stored picture beats a stale 'skipped'", () => {
    const d = parseDraft({ ...typed, posterDecision: "skipped", poster: { mediaId: MEDIA, eventId: "" } });
    expect(d?.posterDecision).toBe("poster");
  });
  it("recordPosterProgress of an upload that finished off screen is read back as a chosen poster", () => {
    const store = memoryStorage();
    saveDraft("o", reduceDraft(typed, { type: "event", field: "name", value: "Kept" }, NOW), store);
    recordPosterProgress("o", typed.id, { mediaId: MEDIA2 }, store);
    const read = loadDraft("o", store);
    expect(read?.poster.mediaId).toBe(MEDIA2);
    expect(read?.posterDecision).toBe("poster");
    expect(read?.event.name).toBe("Kept");
  });
});

describe("an older stored draft keeps all its work", () => {
  it("version 2 (no poster): fields are intact, the decision is asked once", () => {
    const read = parseDraft(stored(2));
    expect(read).not.toBeNull();
    expect(read?.version).toBe(DRAFT_VERSION);
    expect(read?.event.name).toBe("Swan Lake");
    expect(read?.event.description).toBe("Ballet in two acts");
    expect(read?.dates[0].day).toBe("2027-03-05");
    expect(read?.dates[0].time).toBe("19:30");
    expect(read?.poster).toEqual({ mediaId: "", eventId: "" });
    expect(read?.posterDecision).toBe("");
  });
  it("version 3 without a poster: fields intact, decision asked once", () => {
    const read = parseDraft(stored(3));
    expect(read?.event.name).toBe("Swan Lake");
    expect(read?.posterDecision).toBe("");
  });
  it("version 3 with an uploaded picture: the poster stays chosen and is NOT uploaded again", () => {
    const read = parseDraft(stored(3, { mediaId: MEDIA, eventId: "" }));
    expect(read?.posterDecision).toBe("poster");
    expect(read?.poster.mediaId).toBe(MEDIA);
  });
  it("version 3 whose event exists (attach pending): selected, ids intact", () => {
    const read = parseDraft(stored(3, { mediaId: MEDIA, eventId: EVENT }));
    expect(read?.posterDecision).toBe("poster");
    expect(read?.poster).toEqual({ mediaId: MEDIA, eventId: EVENT });
  });
  it("a version-1 draft is still dropped", () => {
    expect(parseDraft({ ...stored(3), version: 1 })).toBeNull();
  });
});
