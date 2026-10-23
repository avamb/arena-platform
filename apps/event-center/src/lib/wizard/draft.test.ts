import { describe, expect, it } from "vitest";
import {
  AGE_OPTIONS,
  DRAFT_VERSION,
  draftStorageKey,
  isBlank,
  loadDraft,
  newDraft,
  parseCapacity,
  parseDraft,
  reduceDraft,
  saveDraft,
  validCurrency,
  venueCapacity,
  venueTimezone,
  type WizardDraft,
} from "@/lib/wizard/draft";

const NOW = new Date("2026-10-16T10:00:00Z");

function memoryStorage() {
  const data = new Map<string, string>();
  return {
    data,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
  };
}

describe("age ratings", () => {
  it("are exactly the ones the database accepts (events_age_rating_check)", () => {
    expect([...AGE_OPTIONS]).toEqual(["0+", "6+", "12+", "16+", "18+", "NR"]);
  });
});

describe("reduceDraft", () => {
  const base = newDraft("d1", NOW);

  it("keeps the event answers", () => {
    let d = reduceDraft(base, { type: "event", field: "name", value: "Swan Lake" }, NOW);
    d = reduceDraft(d, { type: "age", value: "6+" }, NOW);
    d = reduceDraft(d, { type: "promoter", id: "p1", name: "Partner" }, NOW);
    expect(d.event).toMatchObject({ name: "Swan Lake", age: "6+", promoterId: "p1", promoterName: "Partner" });
  });

  it("chooses an existing venue and remembers its facts", () => {
    const d = reduceDraft(
      base,
      { type: "venueExisting", venue: { id: "v1", name: "Hall", timezone: "Europe/Madrid", capacity: 300, country: "ES" } },
      NOW,
    );
    expect(d.venue).toMatchObject({ mode: "existing", existingId: "v1", existingTimezone: "Europe/Madrid", existingCapacity: 300 });
    expect(venueTimezone(d.venue)).toBe("Europe/Madrid");
    expect(venueCapacity(d.venue)).toBe(300);
  });

  it("keeps what was typed for a new venue when the person switches mode and back", () => {
    let d = reduceDraft(base, { type: "venueMode", mode: "new" }, NOW);
    d = reduceDraft(d, { type: "venueNew", field: "newName", value: "Palau" }, NOW);
    d = reduceDraft(d, { type: "venueMode", mode: "existing" }, NOW);
    d = reduceDraft(d, { type: "venueMode", mode: "new" }, NOW);
    expect(d.venue.newName).toBe("Palau");
  });

  it("clears the city and the zone when the country changes, and only then", () => {
    let d = reduceDraft(base, { type: "country", iso2: "es" }, NOW);
    d = reduceDraft(d, { type: "venueNew", field: "cityName", value: "Barcelona" }, NOW);
    d = reduceDraft(d, { type: "timezone", value: "Europe/Madrid" }, NOW);
    const same = reduceDraft(d, { type: "country", iso2: "ES" }, NOW);
    expect(same).toBe(d);
    const other = reduceDraft(d, { type: "country", iso2: "CZ" }, NOW);
    expect(other.venue).toMatchObject({ countryIso2: "CZ", cityName: "", timezone: "" });
  });

  it("lets a country fill the currency but never overwrites a typed one", () => {
    let d = reduceDraft(base, { type: "currency", value: "eur", source: "auto" }, NOW);
    expect(d.currency).toBe("EUR");
    d = reduceDraft(d, { type: "currency", value: "czk", source: "typed" }, NOW);
    expect(d).toMatchObject({ currency: "CZK", currencySource: "typed" });
    const kept = reduceDraft(d, { type: "currency", value: "EUR", source: "auto" }, NOW);
    expect(kept.currency).toBe("CZK");
  });

  it("stamps the change time and leaves an unchanged draft as it is", () => {
    const later = new Date("2026-10-16T11:00:00Z");
    const d = reduceDraft(base, { type: "event", field: "name", value: "A" }, later);
    expect(d.updatedAt).toBe(later.toISOString());
    expect(reduceDraft(d, { type: "venueMode", mode: "" }, later)).toBe(d);
  });
});

describe("isBlank", () => {
  it("is true for a fresh draft and false once anything is typed", () => {
    const d = newDraft("d1", NOW);
    expect(isBlank(d)).toBe(true);
    expect(isBlank(reduceDraft(d, { type: "event", field: "description", value: "x" }, NOW))).toBe(false);
  });
});

describe("parseCapacity", () => {
  it("reads whole places with spaces and refuses everything else", () => {
    expect(parseCapacity("300")).toBe(300);
    expect(parseCapacity("1 200")).toBe(1200);
    expect(parseCapacity("1 200")).toBe(1200);
    for (const bad of ["", "0", "-5", "12.5", "abc", "1000001", "99999999"]) expect(parseCapacity(bad), bad).toBeNull();
    expect(parseCapacity("1000000")).toBe(1_000_000);
  });
});

describe("validCurrency", () => {
  it("accepts a three-letter code in any case", () => {
    expect(validCurrency(" eur ")).toBe("EUR");
    expect(validCurrency("EURO")).toBe("");
    expect(validCurrency("E1R")).toBe("");
  });
});

describe("storage", () => {
  const filled: WizardDraft = reduceDraft(newDraft("d1", NOW), { type: "event", field: "name", value: "Swan Lake" }, NOW);

  it("keeps one draft per organization and gives it back", () => {
    const store = memoryStorage();
    saveDraft("org-a", filled, store);
    expect(store.data.has(draftStorageKey("org-a"))).toBe(true);
    expect(loadDraft("org-a", store)).toEqual(filled);
    expect(loadDraft("org-b", store)).toBeNull();
  });

  it("does not keep an untouched draft", () => {
    const store = memoryStorage();
    saveDraft("org-a", filled, store);
    saveDraft("org-a", newDraft("d2", NOW), store);
    expect(store.data.size).toBe(0);
  });

  it("drops a value of another version, a broken one and a wrong shape", () => {
    const store = memoryStorage();
    store.setItem(draftStorageKey("o"), JSON.stringify({ ...filled, version: DRAFT_VERSION + 1 }));
    expect(loadDraft("o", store)).toBeNull();
    store.setItem(draftStorageKey("o"), "{not json");
    expect(loadDraft("o", store)).toBeNull();
    expect(parseDraft({ ...filled, event: { ...filled.event, age: "4+" } })).toBeNull();
    expect(parseDraft({ ...filled, venue: { ...filled.venue, mode: "other" } })).toBeNull();
    expect(parseDraft({ ...filled, currencySource: "x" })).toBeNull();
    expect(parseDraft(null)).toBeNull();
  });

  it("survives a storage that throws", () => {
    const broken = {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
      removeItem: () => {
        throw new Error("blocked");
      },
    };
    expect(loadDraft("o", broken)).toBeNull();
    expect(() => saveDraft("o", filled, broken)).not.toThrow();
  });
});
