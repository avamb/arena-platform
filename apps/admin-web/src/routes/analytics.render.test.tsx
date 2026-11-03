/**
 * Render tests for the Analytics screens (spec 50, stage A8). The admin-web
 * Vitest environment is Node-only, so — following the sessionOverview
 * precedent — the router- and query-free views are rendered with
 * renderToStaticMarkup.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ErrorsView, kindLabel } from "@/routes/analyticsErrors";
import { FlowsView } from "@/routes/analyticsFlows";
import { TimelineView } from "@/routes/analyticsPerson";
import { FilterBar, matchingPreset } from "@/routes/analyticsUi";
import type { ErrorsResponse, FlowsResponse, PersonTimeline } from "@/lib/api/analytics";
import { ruCatalog, enCatalog, translate, type LocaleCode } from "@/lib/i18n/catalog";

const en = (key: string, params?: Readonly<Record<string, string | number>>) => translate("en", key, params);
const ru = (key: string, params?: Readonly<Record<string, string | number>>) => translate("ru", key, params);

const zero = { entered: 0, left_here: 0, drop_off_pct: null, errors: 0, median_seconds: null };

const flows: FlowsResponse = {
  surface: "",
  from: "2026-10-26",
  to: "2026-11-01",
  previous_from: "2026-10-19",
  previous_to: "2026-10-25",
  flows: [
    {
      surface: "bot",
      flow: "wizard",
      steps: [
        {
          step: "venue",
          entered: 40,
          left_here: 14,
          drop_off_pct: 35,
          errors: 3,
          median_seconds: 185,
          previous: { entered: 30, left_here: 9, drop_off_pct: 30, errors: 1, median_seconds: 60 },
        },
        {
          step: "price",
          entered: 20,
          left_here: 0,
          drop_off_pct: 0,
          errors: 0,
          median_seconds: null,
          previous: zero,
        },
      ],
    },
  ],
};

describe("FlowsView", () => {
  it("renders a row per step with the drop-off bar and the comparison", () => {
    const html = renderToStaticMarkup(<FlowsView data={flows} t={en} />);
    expect(html).toContain("bot / wizard");
    expect(html).toContain('data-step="venue"');
    expect(html).toContain("35%");
    expect(html).toContain('data-width="35"');
    expect(html).toContain("3 min 05 s");
    // previous period: 30 entered / 30%
    expect(html).toContain("30 / 30%");
    // exits 14 vs 9 -> +55.6%, worse
    expect(html).toContain("+55.6%");
    expect(html).toContain('data-direction="up"');
    expect(html).toContain("2026-10-19");
    expect(html).toContain("UTC");
  });

  it("says so when there are no steps", () => {
    const html = renderToStaticMarkup(<FlowsView data={{ ...flows, flows: [] }} t={en} />);
    expect(html).toContain("No flow steps in this period.");
  });

  it("renders in Russian too", () => {
    const html = renderToStaticMarkup(<FlowsView data={flows} t={ru} />);
    expect(html).toContain("Вошло");
    expect(html).toContain("Потери");
  });
});

const errors: ErrorsResponse = {
  surface: "",
  from: "2026-10-26",
  to: "2026-11-01",
  previous_from: "2026-10-19",
  previous_to: "2026-10-25",
  items: [
    { kind: "ui_error", surface: "event_center", key: "venue.not_found", place: "/events/:id", count: 12, previous_count: 8, change_pct: 50 },
    { kind: "rage_click", surface: "event_center", key: "/events/:id", place: "", count: 3, previous_count: 0, change_pct: null },
    { kind: "mystery", surface: "bot", key: "", place: "", count: 1, previous_count: 1, change_pct: 0 },
  ],
};

describe("ErrorsView", () => {
  it("renders the rows with their change", () => {
    const html = renderToStaticMarkup(<ErrorsView data={errors} t={en} />);
    expect(html).toContain("venue.not_found");
    expect(html).toContain("Screen error");
    expect(html).toContain("Rage click");
    expect(html).toContain("+50%");
    expect(html).toContain("new");
    expect(html.match(/data-testid="analytics-error-row"/g)?.length).toBe(3);
  });

  it("falls back to the raw kind when the catalog has none", () => {
    expect(kindLabel("mystery", en)).toBe("mystery");
    expect(kindLabel("ui_error", ru)).toBe("Ошибка экрана");
  });

  it("says so when there are no errors", () => {
    const html = renderToStaticMarkup(<ErrorsView data={{ ...errors, items: [] }} t={en} />);
    expect(html).toContain("No errors in this period.");
  });
});

const timeline: PersonTimeline = {
  person_id: "6f1c2c34-9b7d-4a76-8c52-5b5a3c3b2a10",
  twenty_person_id: "crm-77",
  user_id: null,
  first_touch: { source: "newsletter", medium: "email", campaign: "oct" },
  last_touch: { source: "google", medium: "organic" },
  created_at: "2026-10-20T08:00:00Z",
  profile: {
    stage: "applicant",
    last_seen_at: "2026-11-02T08:30:00Z",
    visits_30d: 4,
    engaged_minutes_30d: 12,
    articles: [{ path: "/blog/how-to-sell", at: "2026-10-21T09:00:00Z", seconds: 90 }],
    milestones: { application_started: "2026-10-22T09:00:00Z", gmv_30d: { EUR: 5000 } },
    activity_score: 17,
    score_explanation: "article x1 = 2.5 + visit x3 = 2.1 (half-life 14 days)",
    changed_at: "2026-11-02T08:00:00Z",
  },
  sessions: [
    {
      id: "s1",
      surface: "site",
      started_at: "2026-11-02T08:00:00Z",
      ended_at: "2026-11-02T08:10:00Z",
      pages: 3,
      engaged_seconds: 125,
      entry: { path: "/pricing", source: "newsletter" },
    },
  ],
  events: [
    { at: "2026-11-02T08:05:00Z", surface: "site", name: "page_view", props: { path: "/pricing" } },
    { at: "2026-11-02T08:00:00Z", surface: "site", name: "email_click", props: { utm_campaign: "oct" } },
  ],
  next_cursor: "1730534400000000.7",
};

const noop = () => undefined;

describe("TimelineView", () => {
  it("shows the profile, the score reason, milestones, sessions and events in Madrid time", () => {
    const html = renderToStaticMarkup(
      <TimelineView pages={[timeline]} hasMore={true} loadingMore={false} onLoadMore={noop} t={en} />,
    );
    expect(html).toContain("crm-77");
    expect(html).toContain("applicant");
    expect(html).toContain("17");
    expect(html).toContain("article x1 = 2.5");
    expect(html).toContain("application_started");
    expect(html).toContain("2026-10-22 11:00 CEST");
    expect(html).toContain("gmv_30d");
    expect(html).toContain("/blog/how-to-sell");
    expect(html).toContain("newsletter / email / oct");
    // 08:05 UTC on 2 Nov is 09:05 CET
    expect(html).toContain("2026-11-02 09:05 CET");
    expect(html).toContain("Europe/Madrid");
    expect(html).toContain("2 min 05 s");
    expect(html).toContain("page_view");
    expect(html).toContain('data-testid="analytics-person-more"');
  });

  it("hides the more button on the last page and explains a missing profile", () => {
    const last: PersonTimeline = { ...timeline, profile: null, next_cursor: null };
    const html = renderToStaticMarkup(
      <TimelineView pages={[last]} hasMore={false} loadingMore={false} onLoadMore={noop} t={en} />,
    );
    expect(html).not.toContain("analytics-person-more");
    expect(html).toContain("No profile yet");
  });

  it("joins the events of several pages in order", () => {
    const older: PersonTimeline = {
      ...timeline,
      profile: null,
      sessions: [],
      events: [{ at: "2026-11-01T08:00:00Z", surface: "site", name: "scroll", props: { depth: 50 } }],
      next_cursor: null,
    };
    const html = renderToStaticMarkup(
      <TimelineView pages={[timeline, older]} hasMore={false} loadingMore={false} onLoadMore={noop} t={en} />,
    );
    expect(html.match(/data-testid="analytics-person-event"/g)?.length).toBe(3);
    expect(html.indexOf("page_view")).toBeLessThan(html.indexOf("scroll"));
  });

  it("carries no e-mail or name field", () => {
    const html = renderToStaticMarkup(
      <TimelineView pages={[timeline]} hasMore={false} loadingMore={false} onLoadMore={noop} t={en} />,
    );
    expect(html).not.toMatch(/@/);
  });
});

describe("FilterBar", () => {
  const now = new Date("2026-11-02T10:00:00Z");

  it("recognises the preset the query equals", () => {
    expect(matchingPreset({ surface: "", from: "2026-10-27", to: "2026-11-02" }, now)).toBe(7);
    expect(matchingPreset({ surface: "", from: "2026-10-01", to: "2026-11-02" }, now)).toBeNull();
  });

  it("renders the surface and period controls with the applied values", () => {
    const html = renderToStaticMarkup(
      <FilterBar query={{ surface: "bot", from: "2026-10-27", to: "2026-11-02" }} onChange={noop} t={en} now={now} />,
    );
    expect(html).toContain('data-testid="analytics-surface"');
    expect(html).toContain("Event center");
    expect(html).toContain("Last 30 days");
    expect(html).toContain('value="2026-10-27"');
    expect(html).toContain('value="2026-11-02"');
  });
});

describe("analytics strings", () => {
  it("every analytics key exists in both locales", () => {
    const locales: Array<[LocaleCode, Record<string, string>]> = [
      ["ru", ruCatalog as Record<string, string>],
      ["en", enCatalog as Record<string, string>],
    ];
    const keys = Object.keys(enCatalog).filter((k) => k.startsWith("analytics.") || k.startsWith("nav.analytics_"));
    expect(keys.length).toBeGreaterThan(40);
    for (const [, cat] of locales) {
      for (const k of keys) expect(cat[k], k).toBeTruthy();
    }
  });
});
