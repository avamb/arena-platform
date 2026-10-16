import { describe, expect, it } from "vitest";
import {
  createScheduler,
  failingFields,
  sectionsCorrect,
  splitErrors,
  targetOfPointer,
  visibleErrors,
  type CheckError,
  type FormField,
  type SchedulerState,
} from "@/lib/wizard/validation";

function err(field: string, code = "import.x"): CheckError {
  return { code, message: `msg ${code}`, field, status: 422 };
}

describe("targetOfPointer", () => {
  it("maps the pointers of the two sections to form fields", () => {
    const cases: Record<string, FormField> = {
      "/action/actionName": "name",
      "/action/description": "description",
      "/action/age": "age",
      "/action/promoterId": "promoter",
      "/venue/arenaVenueId": "venueExisting",
      "/venue/venueName": "venueName",
      "/venue/timezone": "venueTimezone",
      "/venue/cityName": "venueCity",
      "/venue/countryName": "venueCountry",
      "/actionEvent/currency": "currency",
    };
    for (const [pointer, field] of Object.entries(cases)) expect(targetOfPointer(pointer), pointer).toEqual({ kind: "field", field });
  });

  it("sends a date's own pointers to 'date' and anything unknown to 'general'", () => {
    for (const p of ["/actionEvent/day", "/actionEvent/time", "/categoryList", "/categoryList/0/price", "/externalRef"]) {
      expect(targetOfPointer(p), p).toEqual({ kind: "date" });
    }
    for (const p of ["", "/source", "/channelIds/0", "/action/arenaEventId"]) expect(targetOfPointer(p), p).toEqual({ kind: "general" });
  });
});

describe("splitErrors", () => {
  const errors = [
    err("/action/actionName", "import.action_name_required"),
    err("/actionEvent/day", "import.invalid_start_time"),
    err("/categoryList", "import.categories_required"),
    err("/venue/timezone", "venue.timezone_required"),
    err("", "organization.contact_missing"),
  ];

  it("keeps the section errors by field and sets the dates' ones aside", () => {
    const s = splitErrors(errors);
    expect(failingFields(s)).toEqual(["name", "venueTimezone"]);
    expect(s.dated.map((e) => e.code)).toEqual(["import.invalid_start_time", "import.categories_required"]);
    expect(s.general.map((e) => e.code)).toEqual(["organization.contact_missing"]);
  });

  it("calls the event and venue correct only when no field and no general error is left", () => {
    expect(sectionsCorrect(splitErrors([err("/actionEvent/day"), err("/categoryList")]))).toBe(true);
    expect(sectionsCorrect(splitErrors([err("/action/actionName")]))).toBe(false);
    expect(sectionsCorrect(splitErrors([err("")]))).toBe(false);
  });

  it("shows a field's error only after it was touched, or when everything is asked for", () => {
    const s = splitErrors(errors);
    expect(Object.keys(visibleErrors(s, new Set(), false))).toEqual([]);
    expect(Object.keys(visibleErrors(s, new Set<FormField>(["name"]), false))).toEqual(["name"]);
    expect(Object.keys(visibleErrors(s, new Set(), true))).toEqual(["name", "venueTimezone"]);
  });
});

// ─── the scheduler, on a clock we move by hand ───────────────────────────────

interface Pending {
  payload: string;
  resolve: (v: string) => void;
  reject: (e: unknown) => void;
}

function harness(opts: { quietMs?: number; minGapMs?: number } = {}) {
  let t = 0;
  const timers: { at: number; fn: () => void; id: number; live: boolean }[] = [];
  let nextId = 1;
  const asked: Pending[] = [];
  const states: SchedulerState<string>[] = [];
  const scheduler = createScheduler<string, string>({
    run: (payload) =>
      new Promise<string>((resolve, reject) => {
        asked.push({ payload, resolve, reject });
      }),
    onState: (s) => states.push(s),
    keyOf: (p) => p,
    quietMs: opts.quietMs ?? 700,
    minGapMs: opts.minGapMs ?? 1000,
    now: () => t,
    setTimer: (fn, ms) => {
      const entry = { at: t + ms, fn, id: nextId++, live: true };
      timers.push(entry);
      return entry.id;
    },
    clearTimer: (h) => {
      const e = timers.find((x) => x.id === h);
      if (e) e.live = false;
    },
  });
  /** Moves the clock and fires the timers that fall due, in order. */
  function advance(ms: number): void {
    const end = t + ms;
    for (;;) {
      const due = timers.filter((x) => x.live && x.at <= end).sort((a, b) => a.at - b.at)[0];
      if (!due) break;
      due.live = false;
      t = Math.max(t, due.at);
      due.fn();
    }
    t = end;
  }
  const flush = () => new Promise<void>((r) => setTimeout(r, 0));
  return { scheduler, advance, asked, states, flush, now: () => t };
}

describe("createScheduler", () => {
  it("waits for a pause in typing, then asks once", () => {
    const h = harness();
    h.scheduler.request("a");
    h.advance(300);
    h.scheduler.request("ab");
    h.advance(300);
    h.scheduler.request("abc");
    expect(h.asked).toHaveLength(0);
    expect(h.scheduler.state.status).toBe("pending");
    h.advance(699);
    expect(h.asked).toHaveLength(0);
    h.advance(1);
    expect(h.asked.map((a) => a.payload)).toEqual(["abc"]);
    expect(h.scheduler.state.status).toBe("checking");
  });

  it("keeps one request in flight and asks the newest payload after it, not sooner than the gap", async () => {
    const h = harness();
    h.scheduler.request("a");
    h.advance(700);
    expect(h.asked).toHaveLength(1);
    h.scheduler.request("b");
    h.advance(700); // pause is over, but "a" is still in flight
    expect(h.asked).toHaveLength(1);
    h.asked[0].resolve("answer-a");
    await h.flush();
    // The answer for "a" is stale: it writes nothing.
    expect(h.scheduler.state.last).toBeNull();
    expect(h.scheduler.state.status).toBe("pending");
    expect(h.asked).toHaveLength(1); // 700 ms since the start: the gap is not over
    h.advance(300);
    expect(h.asked).toHaveLength(2);
    expect(h.asked[1].payload).toBe("b");
  });

  it("never starts two requests closer than the gap", async () => {
    const h = harness();
    h.scheduler.request("a");
    h.advance(700);
    const startedAt = h.now();
    h.asked[0].resolve("A");
    await h.flush();
    h.scheduler.request("b");
    h.advance(700); // quiet over, gap not (only 700 ms since the start)
    expect(h.asked).toHaveLength(1);
    h.advance(300);
    expect(h.asked).toHaveLength(2);
    expect(h.now() - startedAt).toBeGreaterThanOrEqual(1000);
  });

  it("accepts the answer of the payload it asked about", async () => {
    const h = harness();
    h.scheduler.request("a");
    h.advance(700);
    h.asked[0].resolve("answer-a");
    await h.flush();
    expect(h.scheduler.state).toMatchObject({ status: "idle", last: { key: "a", result: "answer-a" } });
  });

  it("does not ask an unchanged payload twice, and returning to an answered one asks nothing", async () => {
    const h = harness();
    h.scheduler.request("a");
    h.advance(700);
    h.asked[0].resolve("A");
    await h.flush();
    h.scheduler.request("a");
    h.advance(5000);
    expect(h.asked).toHaveLength(1);
    h.scheduler.request("b");
    h.scheduler.request("a"); // back before the pause ended
    h.advance(5000);
    expect(h.asked).toHaveLength(1);
    expect(h.scheduler.state.status).toBe("idle");
  });

  it("reports a failure and asks again only on a change or a retry", async () => {
    const h = harness();
    h.scheduler.request("a");
    h.advance(700);
    h.asked[0].reject(new Error("boom"));
    await h.flush();
    expect(h.scheduler.state.error).toBeInstanceOf(Error);
    expect(h.scheduler.state.status).toBe("idle");
    h.advance(10_000);
    expect(h.asked).toHaveLength(1); // no loop
    h.scheduler.retry();
    h.advance(1000);
    expect(h.asked).toHaveLength(2);
    expect(h.scheduler.state.error).toBeNull();
  });

  it("writes nothing after it is disposed", async () => {
    const h = harness();
    h.scheduler.request("a");
    h.advance(700);
    h.scheduler.dispose();
    const before = h.states.length;
    h.asked[0].resolve("A");
    await h.flush();
    expect(h.states.length).toBe(before);
  });
});
