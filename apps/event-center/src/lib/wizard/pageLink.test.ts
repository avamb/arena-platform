import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import { classifyPageFailure, pageUrlOf, parsePageReading } from "@/lib/wizard/pageLink";

const answer = {
  facts: {
    name: "Вишнёвый сад",
    date: "2031-11-20",
    time: "19:00",
    venue_name: "Teatro Kolybel",
    city: "Madrid",
    country: "ES",
    age: "12+",
    description: "Комедия Чехова.",
    dates: [
      { date: "2031-11-20", time: "19:00", utc_offset: "+01:00" },
      { date: "2031-11-21", time: "17:00" },
      { date: "not a day" },
      { date: "2031-11-22", time: "25:00" },
    ],
    categories: [{ name: "Партер", price_minor: 2500, currency: "EUR" }],
    image: { media_id: "0190a8a1-4c2e-7a10-9d3f-1b2c3d4e5f60", width: 1080, height: 1350, fits_poster: true },
  },
  sources: { name: "json_ld", dates: "json_ld", categories: "model" },
  final_url: "https://teatro.example.org/cerezos",
  language: "ru",
  warnings: [{ code: "event_page.past_dates_skipped", count: 2 }, { code: "event_page.something_new" }, { code: "event_page.past_dates_skipped" }],
};

describe("pageUrlOf", () => {
  it("takes only a full http or https address", () => {
    expect(pageUrlOf("  https://teatro.example.org/obra  ")).toBe("https://teatro.example.org/obra");
    expect(pageUrlOf("HTTP://x.org")).toBe("HTTP://x.org");
    for (const bad of ["", "www.teatro.es", "teatro.es/obra", "ftp://x.org", "javascript:alert(1)", "https://", "https://a b.org", "x".repeat(2049)]) {
      expect(pageUrlOf(bad), bad).toBeNull();
    }
  });
});

describe("classifyPageFailure", () => {
  it("names each refusal of the server", () => {
    const cases: [number, string, string][] = [
      [422, "event_page.ticketing_platform", "ticketing"],
      [400, "event_page.invalid_url", "invalid"],
      [422, "event_page.destination_not_allowed", "notPublic"],
      [422, "event_page.not_html", "notPage"],
      [422, "event_page.too_large", "notPage"],
      [422, "event_page.nothing_found", "nothing"],
      [503, "event_page.fetch_failed", "fetch"],
      [429, "event_page.rate_limited", "rate"],
      [403, "permissions.denied", "denied"],
      [500, "internal", "unavailable"],
      [0, "network.timeout", "unknown"],
    ];
    for (const [status, code, want] of cases) {
      expect(classifyPageFailure(new ApiError(status, code, "x")), code).toBe(want);
    }
    expect(classifyPageFailure(new ApiError(0, "network.aborted", "x"))).toBe("aborted");
    expect(classifyPageFailure(new Error("boom"))).toBe("unknown");
  });
});

describe("parsePageReading", () => {
  it("keeps the facts, the other real dates, the picture and the known warnings", () => {
    const r = parsePageReading(answer);
    expect(r).not.toBeNull();
    if (r === null) return;
    expect(r.facts.name).toBe("Вишнёвый сад");
    expect(r.facts.date).toBe("2031-11-20");
    expect(r.facts.venueName).toBe("Teatro Kolybel");
    expect(r.facts.categories).toEqual([{ name: "Партер", priceMinor: 2500, currency: "EUR" }]);
    expect(r.otherDates).toEqual([
      { date: "2031-11-21", time: "17:00" },
      { date: "2031-11-22", time: "" },
    ]);
    expect(r.image).toEqual({ mediaId: "0190a8a1-4c2e-7a10-9d3f-1b2c3d4e5f60", width: 1080, height: 1350, fitsPoster: true });
    expect(r.warnings).toEqual(["event_page.past_dates_skipped"]);
    expect(r.byModel).toBe(true);
  });

  it("drops a picture that is not an id with two sizes", () => {
    for (const image of [null, { media_id: "../x", width: 1, height: 1 }, { media_id: "0190a8a1-4c2e-7a10-9d3f-1b2c3d4e5f60", width: 0, height: 10 }]) {
      const r = parsePageReading({ ...answer, facts: { ...answer.facts, image } });
      expect(r?.image).toBeNull();
    }
  });

  it("refuses what is not an answer", () => {
    expect(parsePageReading(null)).toBeNull();
    expect(parsePageReading({})).toBeNull();
    expect(parsePageReading({ facts: "x" })).toBeNull();
  });
});
