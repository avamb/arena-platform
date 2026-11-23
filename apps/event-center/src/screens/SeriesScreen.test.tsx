/**
 * The tour screens rendered to static markup: the list row, the fields of the form and the events of a tour.
 * (Saving and the live behavior are proven by the live twin, tests/smoke/live/series.spec.ts.)
 */
import { describe, expect, it } from "vitest";
import type { Series, SeriesEventCard } from "@/lib/api/types";
import { draftOf } from "@/lib/series";
import { renderInRouter } from "@/test-utils/renderInRouter";
import { SeriesFields, SeriesEvents } from "@/screens/SeriesDetailScreen";
import { SeriesRow } from "@/screens/SeriesScreen";

const tour: Series = {
  id: "s1",
  org_id: "o1",
  slug: "summer-tour",
  name: "Summer tour",
  description_html: null,
  poster_media_id: null,
  poster_url: null,
  hero_media_id: null,
  hero_url: null,
  hero_video_url: "https://youtu.be/abc",
  partners_text_html: null,
  hide_children_in_lists: true,
  position: 4,
  event_count: 3,
  archived_at: null,
  created_at: "2026-11-01T10:00:00Z",
  updated_at: "2026-11-01T10:00:00Z",
  translations: {},
};

describe("SeriesRow", () => {
  it("shows the name, the page address and the number of events with a link to the tour", async () => {
    const html = await renderInRouter(<SeriesRow orgId="o1" series={tour} />);
    expect(html).toContain("Summer tour");
    expect(html).toContain("Page: summer-tour");
    expect(html).toContain("Events: 3");
    expect(html).toContain("/o/o1/series/s1");
    expect(html).not.toContain("Archived");
  });

  it("marks an archived tour", async () => {
    const html = await renderInRouter(<SeriesRow orgId="o1" series={{ ...tour, archived_at: "2026-11-02T10:00:00Z" }} />);
    expect(html).toContain("Archived");
  });

  it("speaks Russian with the glossary word", async () => {
    const html = await renderInRouter(<SeriesRow orgId="o1" series={tour} />, "ru");
    expect(html).toContain("Мероприятий: 3");
  });
});

describe("SeriesFields", () => {
  const props = {
    orgId: "o1",
    draft: draftOf(tour),
    onDraft: () => undefined,
    problems: {},
    textProblems: {},
    canEdit: true,
    canView: true,
    canUpload: true,
    busy: false,
  };

  it("has every field of the tour", async () => {
    const html = await renderInRouter(<SeriesFields {...props} />);
    for (const id of ["series-name", "series-slug-input", "series-description", "series-partners", "series-poster", "series-hero", "series-video", "series-hide-children", "series-position"]) {
      expect(html, id).toContain(`data-testid="${id}"`);
    }
    expect(html).toContain('value="Summer tour"');
    expect(html).toContain('value="https://youtu.be/abc"');
    expect(html).toContain('value="4"');
    expect(html).toMatch(/data-testid="series-hide-children"/);
    expect(html).toContain("Hide the cities in the feed");
    expect(html).toContain("Poster (portrait 4:5)");
  });

  it("shows the reasons of the fields in words", async () => {
    const html = await renderInRouter(<SeriesFields {...props} problems={{ videoUrl: "series.err.video", slug: "series.err.slugInvalid" }} />);
    expect(html).toContain("Enter a YouTube or Vimeo link that starts with https://.");
    expect(html).toContain("single hyphens only");
    expect(html).toContain('aria-invalid="true"');
  });

  it("offers no upload to a person without the right, and none on a tour that cannot be edited", async () => {
    const noRight = await renderInRouter(<SeriesFields {...props} canUpload={false} />);
    expect(noRight).toContain("You have no right to upload pictures.");
    expect(noRight).not.toContain('type="file"');
    const archived = await renderInRouter(<SeriesFields {...props} canEdit={false} />);
    expect(archived).not.toContain('type="file"');
    expect(archived).toMatch(/data-testid="series-hide-children"[^>]*disabled|disabled[^>]*data-testid="series-hide-children"/);
  });
});

describe("SeriesEvents", () => {
  const events: SeriesEventCard[] = [
    { id: "e1", name: "Prague", slug: "prague", status: "published", first_session_at: "2027-06-01T18:00:00Z" },
    { id: "e2", name: "Brno", slug: null, status: "draft", first_session_at: null },
  ];

  it("lists the events with a link to each", async () => {
    const html = await renderInRouter(<SeriesEvents orgId="o1" events={events} canOpen />);
    expect(html).toContain("Prague");
    expect(html).toContain("/o/o1/events/e1");
    expect(html).toContain("/o/o1/events/e2");
    expect(html).toContain("No date");
    expect(html).toContain("Draft");
  });

  it("says so when the tour has no event", async () => {
    const html = await renderInRouter(<SeriesEvents orgId="o1" events={[]} canOpen />);
    expect(html).toContain("No event is in this tour yet.");
  });
});
