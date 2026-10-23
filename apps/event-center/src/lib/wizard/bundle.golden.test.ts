/**
 * The web wizard's bundles against the bot's (task 6-1b): the Go test
 * `TestWizardGolden_BundlesMatchTheFiles` (eventbot/wizard_golden_test.go)
 * writes one file per set of typed answers with the bundles the bot's
 * `BuildBundle` makes; this test builds a draft from the SAME answers and
 * compares what `buildBundle` makes. One truth, two readers.
 *
 * The bot's request is the whole import wire (poster, seating, compat ids...);
 * the web sends only what it has. So both sides are first reduced to what the
 * server would read: `null`, "", 0 and false are the same as an absent field,
 * EXCEPT on the keys where the server tells "absent" (keep the stored value)
 * from "zero" (clear it).
 */
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { buildBundle } from "@/lib/wizard/bundle";
import { newDraft, reduceDraft, type WizardDraft } from "@/lib/wizard/draft";
import type { CategoryEntry, DateEntry, PriceStepEntry, TicketMode } from "@/lib/wizard/datesDraft";

const DIR = new URL("../../../../backend/internal/platform/eventbot/testdata/wizard_bundles/", import.meta.url);

/** Keys where 0, "" or [] means something other than absent (pointers on the wire). */
const SIGNIFICANT_ZERO = new Set(["nextCategoryIndex", "sellLimit", "priceSchedule", "doorsOpenTime", "promoterId"]);

export function reduce(value: unknown, key = ""): unknown {
  if (Array.isArray(value)) return value.map((v) => reduce(v));
  if (value !== null && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      const r = reduce(v, k);
      if (r === undefined) continue;
      out[k] = r;
    }
    return out;
  }
  if (value === null || value === undefined) return undefined;
  if ((value === "" || value === 0 || value === false) && !SIGNIFICANT_ZERO.has(key)) return undefined;
  return value;
}

interface GoldenInput {
  event: { name: string; description: string; age: string; promoterId: string };
  currency: string;
  venue: { id: string; name: string; timezone: string; capacity: number };
  externalRefPrefix: string;
  dates: {
    day: string;
    time: string;
    salesEnd: string;
    doors: string;
    capacity: string;
    tickets: {
      mode: TicketMode;
      categories: { name: string; price: string; places: string; until: string; limit: string }[];
      schedule: { from: string; price: string }[];
    };
  }[];
}

interface GoldenFile {
  description: string;
  input: GoldenInput;
  bundles: Record<string, unknown>[];
}

/** The draft the web wizard would hold after these typed answers. */
export function draftFromGolden(input: GoldenInput): WizardDraft {
  const NOW = new Date("2026-10-16T10:00:00Z");
  let d = newDraft("draft-golden", NOW, "");
  d = reduceDraft(d, { type: "event", field: "name", value: input.event.name }, NOW);
  d = reduceDraft(d, { type: "event", field: "description", value: input.event.description }, NOW);
  if (input.event.age !== "") d = reduceDraft(d, { type: "age", value: input.event.age as "6+" }, NOW);
  if (input.event.promoterId !== "") d = reduceDraft(d, { type: "promoter", id: input.event.promoterId, name: "P" }, NOW);
  d = reduceDraft(
    d,
    { type: "venueExisting", venue: { id: input.venue.id, name: input.venue.name, timezone: input.venue.timezone, capacity: input.venue.capacity, country: null } },
    NOW,
  );
  d = reduceDraft(d, { type: "currency", value: input.currency, source: "typed" }, NOW);
  const dates: DateEntry[] = input.dates.map((dt, i) => ({
    key: `d${i + 1}`,
    day: dt.day,
    time: dt.time,
    endTime: "",
    salesEnd: dt.salesEnd,
    doors: dt.doors,
    capacity: dt.capacity,
    sameTickets: false,
    tickets: {
      mode: dt.tickets.mode,
      categories: dt.tickets.categories.map((c, j): CategoryEntry => ({ key: `d${i + 1}-c${j + 1}`, ...c })),
      schedule: dt.tickets.schedule.map((s, j): PriceStepEntry => ({ key: `d${i + 1}-s${j + 1}`, ...s })),
    },
  }));
  return { ...d, dates };
}

const files = readdirSync(DIR)
  .filter((f) => f.endsWith(".json"))
  .sort();

describe("golden bundles of the bot", () => {
  it("has the golden files", () => {
    expect(files.length).toBeGreaterThanOrEqual(6);
  });

  for (const file of files) {
    const g = JSON.parse(readFileSync(fileURLToPath(new URL(file, DIR)), "utf8")) as GoldenFile;
    describe(file, () => {
      const draft = draftFromGolden(g.input);

      it("has one bundle per date", () => {
        expect(g.bundles).toHaveLength(g.input.dates.length);
      });

      g.input.dates.forEach((_, i) => {
        it(`date ${i + 1}: the web bundle is the bot's bundle`, () => {
          const web = buildBundle(draft, { orgId: "org-1", externalRef: `${g.input.externalRefPrefix}:${i + 1}` }, i) as unknown as Record<string, unknown>;
          const bot = g.bundles[i];
          const { venue: webVenue, ...webRest } = web;
          const { venue: botVenue, ...botRest } = bot;
          expect(reduce(webRest)).toEqual(reduce(botRest));
          // The venue block is 6-1a's own; an existing venue is addressed the same way.
          expect((webVenue as { arenaVenueId?: string }).arenaVenueId).toBe((botVenue as { arenaVenueId: string }).arenaVenueId);
        });
      });
    });
  }
});
