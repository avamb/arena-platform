import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { Series } from "@/lib/api/types";
import {
  changesOf,
  draftOf,
  draftProblems,
  eventSeriesErrorKey,
  filterSeries,
  hasChanges,
  linkableSeries,
  parsePosition,
  seriesErrorKey,
  validSeriesSlug,
  validVideoUrl,
} from "./series";

function tour(over: Partial<Series> = {}): Series {
  return {
    id: "s1",
    org_id: "o1",
    slug: "summer-tour",
    name: "Summer tour",
    description_html: null,
    poster_media_id: null,
    poster_url: null,
    hero_media_id: null,
    hero_url: null,
    hero_video_url: null,
    partners_text_html: null,
    hide_children_in_lists: false,
    position: 0,
    event_count: 0,
    archived_at: null,
    created_at: "2026-11-01T10:00:00Z",
    updated_at: "2026-11-01T10:00:00Z",
    translations: {},
    ...over,
  };
}

describe("page address of a tour", () => {
  it("takes 1-120 characters of a-z, digits and single hyphens", () => {
    for (const ok of ["a", "summer-tour", "tour-2027", "x".repeat(120)]) expect(validSeriesSlug(ok), ok).toBe(true);
    for (const bad of ["", "-a", "a-", "a--b", "a_b", "Tour", "x".repeat(121)]) expect(validSeriesSlug(bad), bad).toBe(false);
  });
});

describe("video link", () => {
  it("accepts https YouTube and Vimeo only", () => {
    expect(validVideoUrl("https://www.youtube.com/watch?v=abc")).toBe(true);
    expect(validVideoUrl("https://youtu.be/abc")).toBe(true);
    expect(validVideoUrl("https://vimeo.com/12345")).toBe(true);
    expect(validVideoUrl("https://player.vimeo.com/video/1")).toBe(true);
    for (const bad of ["", "http://youtu.be/abc", "https://example.com/v", "https://youtube.com.evil.test/x", "youtu.be/abc", "https://youtu.be/a b"]) {
      expect(validVideoUrl(bad), bad).toBe(false);
    }
  });
});

describe("draft of a tour", () => {
  it("starts equal to the tour: no change, no problem", () => {
    const s = tour({ description_html: "<p>x</p>", hero_video_url: "https://youtu.be/a", position: 3 });
    const d = draftOf(s);
    expect(hasChanges(s, d)).toBe(false);
    expect(changesOf(s, d)).toEqual({});
    expect(draftProblems(d)).toEqual({});
  });

  it("sends only what changed, and null for what was emptied", () => {
    const s = tour({ description_html: "<p>x</p>", poster_media_id: "m1", hero_video_url: "https://youtu.be/a" });
    const d = { ...draftOf(s), name: "  Winter   tour ", descriptionHtml: "", posterMediaId: null, videoUrl: "", hideChildren: true, position: "5" };
    expect(changesOf(s, d)).toEqual({
      name: "Winter tour",
      description_html: null,
      poster_media_id: null,
      hero_video_url: null,
      hide_children_in_lists: true,
      position: 5,
    });
  });

  it("reads a pasted link as the page address", () => {
    const s = tour();
    const d = { ...draftOf(s), slug: "https://tickets.example/Winter-Tour/" };
    expect(changesOf(s, d)).toEqual({ slug: "winter-tour" });
  });

  it("names the fields that cannot be saved", () => {
    const d = { ...draftOf(tour()), name: " ", slug: "Bad Slug", videoUrl: "http://x.test", position: "1.5" };
    expect(draftProblems(d)).toEqual({
      name: "series.err.nameEmpty",
      slug: "series.err.slugInvalid",
      videoUrl: "series.err.video",
      position: "series.err.position",
    });
    expect(draftProblems({ ...draftOf(tour()), name: "x".repeat(201) }).name).toBe("series.err.nameLong");
  });

  it("parses a position as a whole number", () => {
    expect(parsePosition("10")).toBe(10);
    expect(parsePosition(" -2 ")).toBe(-2);
    expect(parsePosition("")).toBeNull();
    expect(parsePosition("1e3")).toBeNull();
  });
});

describe("refusals in words", () => {
  it("maps the API's codes of a tour", () => {
    expect(seriesErrorKey(new ApiError(409, "series.slug_taken", ""))).toBe("series.err.slugTaken");
    expect(seriesErrorKey(new ApiError(404, "series.not_found", ""))).toBe("series.err.gone");
    expect(seriesErrorKey(new ApiError(422, "series.invalid", "", undefined, { field: "hero_video_url" }))).toBe("series.err.video");
    expect(seriesErrorKey(new ApiError(422, "series.invalid", "", undefined, { field: "slug" }))).toBe("series.err.slugInvalid");
    expect(seriesErrorKey(new ApiError(422, "series.invalid", "", undefined, { field: "hero_media_id" }))).toBe("series.err.media");
    expect(seriesErrorKey(new ApiError(422, "series.invalid", ""))).toBe("series.err.invalid");
    expect(seriesErrorKey(new ApiError(500, "internal", ""))).toBeNull();
    expect(seriesErrorKey(new Error("x"))).toBeNull();
  });

  it("says why an event cannot be linked", () => {
    expect(eventSeriesErrorKey(new ApiError(422, "event.invalid_series", ""))).toBe("series.err.eventInvalid");
    expect(eventSeriesErrorKey(new ApiError(404, "series.not_found", ""))).toBe("series.err.gone");
    expect(eventSeriesErrorKey(new ApiError(500, "internal", ""))).toBeNull();
  });
});

describe("lists", () => {
  const a = tour({ id: "a", name: "B tour", position: 2 });
  const b = tour({ id: "b", name: "A tour", position: 2 });
  const c = tour({ id: "c", name: "C tour", position: 1 });
  const z = tour({ id: "z", name: "Old tour", archived_at: "2026-11-02T10:00:00Z" });

  it("sorts active tours by position then name, and splits the archive", () => {
    expect(filterSeries([a, b, c, z], false).map((s) => s.id)).toEqual(["c", "b", "a"]);
    expect(filterSeries([a, b, c, z], true).map((s) => s.id)).toEqual(["z"]);
  });

  it("offers the active tours and the tour the event is already in", () => {
    expect(linkableSeries([a, z], null).map((s) => s.id)).toEqual(["a"]);
    expect(linkableSeries([a, z], "z").map((s) => s.id)).toEqual(["a", "z"]);
  });
});
