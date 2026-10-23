/** Task 7-1: the poster part of the stored draft, and the upgrade from version 2. */
import { describe, expect, it } from "vitest";
import {
  DRAFT_VERSION,
  draftStorageKey,
  hasPosterRefs,
  isBlank,
  loadDraft,
  newDraft,
  parseDraft,
  recordPosterProgress,
  reduceDraft,
  saveDraft,
  type WizardDraft,
} from "@/lib/wizard/draft";

const NOW = new Date("2026-10-17T10:00:00Z");
const MEDIA = "0199aaaa-0000-7000-8000-000000000001";
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

const typed: WizardDraft = reduceDraft(newDraft("d1", NOW), { type: "event", field: "name", value: "Swan Lake" }, NOW);

/** A draft exactly as the previous release stored it: version 2, no poster. */
function storedV2(): Record<string, unknown> {
  const copy: Record<string, unknown> = { ...typed };
  delete copy.poster;
  return { ...copy, version: 2 };
}

describe("a version-2 draft survives this upgrade", () => {
  it("is read whole, with an empty poster, and its typed work is intact", () => {
    const old = {
      ...storedV2(),
      event: { ...typed.event, description: "Ballet in two acts", age: "12+" },
      currency: "EUR",
      currencySource: "typed",
    };
    const read = parseDraft(old);
    expect(read).not.toBeNull();
    expect(read?.version).toBe(DRAFT_VERSION);
    expect(read?.poster).toEqual({ mediaId: "", eventId: "" });
    expect(read?.event).toMatchObject({ name: "Swan Lake", description: "Ballet in two acts", age: "12+" });
    expect(read?.dates).toEqual(typed.dates);
    expect(read?.currency).toBe("EUR");
  });

  it("loads from storage and is stored again as the new version", () => {
    const store = memoryStorage();
    store.setItem(draftStorageKey("o"), JSON.stringify(storedV2()));
    const read = loadDraft("o", store);
    expect(read?.event.name).toBe("Swan Lake");
    saveDraft("o", read as WizardDraft, store);
    expect(JSON.parse(store.data.get(draftStorageKey("o")) as string).version).toBe(DRAFT_VERSION);
  });

  it("still drops an unknown future version, a version 1 and a new version without a poster", () => {
    expect(parseDraft({ ...typed, version: DRAFT_VERSION + 1 })).toBeNull();
    expect(parseDraft({ ...storedV2(), version: 1 })).toBeNull();
    const noPoster: Record<string, unknown> = { ...typed };
    delete noPoster.poster;
    expect(parseDraft(noPoster)).toBeNull();
    expect(parseDraft({ ...typed, poster: { mediaId: 5, eventId: "" } })).toBeNull();
  });
});

describe("the poster of a draft", () => {
  it("holds two stable ids and nothing else: no file, blob address, signed link or token", () => {
    let d = reduceDraft(typed, { type: "posterUploaded", mediaId: MEDIA }, NOW);
    d = reduceDraft(d, { type: "posterCreated", eventId: EVENT }, NOW);
    const store = memoryStorage();
    saveDraft("o", d, store);
    const raw = store.data.get(draftStorageKey("o")) as string;
    expect(JSON.parse(raw).poster).toEqual({ mediaId: MEDIA, eventId: EVENT });
    for (const bad of ["blob:", "http", "sig=", "expires", "Bearer", "token", "data:image"]) expect(raw).not.toContain(bad);
    expect(loadDraft("o", store)?.poster).toEqual({ mediaId: MEDIA, eventId: EVENT });
  });

  it("a draft with only an uploaded picture is worth keeping", () => {
    const d = reduceDraft(newDraft("d2", NOW), { type: "posterUploaded", mediaId: MEDIA }, NOW);
    expect(isBlank(d)).toBe(true);
    expect(hasPosterRefs(d)).toBe(true);
    const store = memoryStorage();
    saveDraft("o", d, store);
    expect(store.data.size).toBe(1);
  });

  it("choosing another picture forgets the uploaded one, never the created event", () => {
    let d = reduceDraft(typed, { type: "posterUploaded", mediaId: MEDIA }, NOW);
    d = reduceDraft(d, { type: "posterCreated", eventId: EVENT }, NOW);
    d = reduceDraft(d, { type: "posterCleared" }, NOW);
    expect(d.poster).toEqual({ mediaId: "", eventId: EVENT });
    expect(reduceDraft(d, { type: "posterCleared" }, NOW)).toBe(d);
  });

  it("a poster change never touches the typed answers, and reset clears the poster", () => {
    const d = reduceDraft(typed, { type: "posterUploaded", mediaId: MEDIA }, NOW);
    expect(d.event).toEqual(typed.event);
    expect(d.dates).toEqual(typed.dates);
    expect(reduceDraft(d, { type: "reset", id: "d9", defaultName: "A" }, NOW).poster).toEqual({ mediaId: "", eventId: "" });
  });
});

describe("recordPosterProgress", () => {
  it("writes into the stored draft with the SAME id only", () => {
    const store = memoryStorage();
    saveDraft("o", typed, store);
    recordPosterProgress("o", "d1", { mediaId: MEDIA }, store);
    expect(loadDraft("o", store)?.poster.mediaId).toBe(MEDIA);
    recordPosterProgress("o", "somebody-else", { eventId: EVENT }, store);
    expect(loadDraft("o", store)?.poster.eventId).toBe("");
  });

  it("does nothing for another organization or an empty store", () => {
    const store = memoryStorage();
    recordPosterProgress("o", "d1", { mediaId: MEDIA }, store);
    expect(store.data.size).toBe(0);
    saveDraft("o", typed, store);
    recordPosterProgress("other-org", "d1", { mediaId: MEDIA }, store);
    expect(loadDraft("o", store)?.poster.mediaId).toBe("");
  });
});
