/**
 * What the wizard does with the answer of the server's check
 * (`POST .../imports/event-bundle?dry_run=true`, task 3-1).
 *
 * The check lists EVERY error of a bundle with the JSON pointer of the field
 * it is about. This file turns a pointer into a field of the form, hides the
 * errors that belong to a part of the bundle the wizard does not have yet (the
 * stub date and category, see bundle.ts) and keeps the rest for the screen.
 *
 * Pure: no React, no network.
 */

/** A field of the sections on screen. */
export type FormField =
  | "name"
  | "description"
  | "age"
  | "promoter"
  | "venueExisting"
  | "venueName"
  | "venueCountry"
  | "venueCity"
  | "venueTimezone"
  | "venueCapacity"
  | "currency";

export const FORM_FIELDS: readonly FormField[] = [
  "name",
  "description",
  "age",
  "promoter",
  "venueExisting",
  "venueName",
  "venueCountry",
  "venueCity",
  "venueTimezone",
  "venueCapacity",
  "currency",
];

/** One error of the check, as the API answers it (`ImportDryRunError`). */
export interface CheckError {
  code: string;
  message: string;
  field: string;
  status: number;
}

export interface CheckWarning {
  code: string;
  message: string;
}

/** The part of the answer the wizard reads. */
export interface CheckAnswer {
  valid: boolean;
  errors: CheckError[];
  warnings: CheckWarning[];
}

const POINTER_TO_FIELD: Record<string, FormField> = {
  "/action/actionName": "name",
  "/action/fullActionName": "name",
  "/action/description": "description",
  "/action/age": "age",
  "/action/promoterId": "promoter",
  "/venue/arenaVenueId": "venueExisting",
  "/venue/venueName": "venueName",
  "/venue/countryName": "venueCountry",
  "/venue/cityName": "venueCity",
  "/venue/timezone": "venueTimezone",
  "/actionEvent/currency": "currency",
};

/**
 * Pointers of the bundle that the sections on screen do not fill. In 6-1a
 * these hold a stub, so an error about them says "fill in the dates", not
 * "this section is wrong".
 */
const LATER_PREFIXES = ["/actionEvent", "/categoryList", "/externalRef", "/seatList", "/svg"];

export type PointerTarget = { kind: "field"; field: FormField } | { kind: "later" } | { kind: "general" };

/** Where an error with this JSON pointer belongs. */
export function targetOfPointer(pointer: string): PointerTarget {
  const known = POINTER_TO_FIELD[pointer];
  if (known !== undefined) return { kind: "field", field: known };
  for (const prefix of LATER_PREFIXES) {
    if (pointer === prefix || pointer.startsWith(`${prefix}/`)) return { kind: "later" };
  }
  return { kind: "general" };
}

export interface SplitErrors {
  /** Errors by form field, in the order found. */
  byField: Partial<Record<FormField, CheckError[]>>;
  /** Errors about the whole bundle or a field the form does not know. */
  general: CheckError[];
  /** Errors about the stub parts: not shown as errors. */
  later: CheckError[];
}

export function splitErrors(errors: readonly CheckError[]): SplitErrors {
  const out: SplitErrors = { byField: {}, general: [], later: [] };
  for (const err of errors) {
    const target = targetOfPointer(err.field);
    if (target.kind === "field") {
      (out.byField[target.field] ??= []).push(err);
    } else if (target.kind === "later") {
      out.later.push(err);
    } else {
      out.general.push(err);
    }
  }
  return out;
}

/** The fields that have at least one error. */
export function failingFields(split: SplitErrors): FormField[] {
  return FORM_FIELDS.filter((f) => (split.byField[f]?.length ?? 0) > 0);
}

/**
 * The sections are correct when the check found nothing about them. The stub
 * errors are not the sections' business.
 */
export function sectionsCorrect(split: SplitErrors): boolean {
  return failingFields(split).length === 0 && split.general.length === 0;
}

/**
 * A field shows its error only once the person has touched it, or pressed
 * "Check": an untouched empty name is "required" to the server from the first
 * second, and an error under a field nobody has reached yet is noise.
 */
export function visibleErrors(
  split: SplitErrors,
  touched: ReadonlySet<FormField>,
  all: boolean,
): Partial<Record<FormField, CheckError[]>> {
  const out: Partial<Record<FormField, CheckError[]>> = {};
  for (const f of failingFields(split)) {
    if (all || touched.has(f)) out[f] = split.byField[f];
  }
  return out;
}

// ─── the scheduler ───────────────────────────────────────────────────────────

/**
 * The check holds a database transaction and, for an event that exists, a
 * lock on its sessions, exactly like an import (backlog of task 3-1). So the
 * wizard never fires it on every keystroke:
 *
 *  - it waits for a pause in typing (`quietMs`);
 *  - it starts at most one request per `minGapMs`;
 *  - one request is in flight at a time, and an older answer never writes
 *    over a newer state: an answer counts only for the payload it was asked
 *    about, otherwise the newest payload is asked again;
 *  - an unchanged payload is not asked twice.
 *
 * The same shape as the widget's promo controller (lib/promoController.ts),
 * with time passed in so the rules are tested without a clock.
 */
export interface SchedulerState<R> {
  /** "pending": waiting for a pause or for the gap to pass. "checking": a request is in flight. */
  status: "idle" | "pending" | "checking";
  /** The newest accepted answer, with the payload it belongs to. */
  last: { key: string; result: R } | null;
  /** The last request failed; the next change (or `retry`) asks again. */
  error: unknown;
}

export interface SchedulerDeps<P, R> {
  run: (payload: P) => Promise<R>;
  onState: (state: SchedulerState<R>) => void;
  /** Identity of a payload; equal keys are the same question. */
  keyOf: (payload: P) => string;
  quietMs?: number;
  minGapMs?: number;
  now?: () => number;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (handle: unknown) => void;
}

export const DEFAULT_QUIET_MS = 700;
export const DEFAULT_MIN_GAP_MS = 1000;

export interface Scheduler<P, R> {
  /** The newest payload. Asks after a pause. */
  request(payload: P): void;
  /** Asks the newest payload again now (a failed check, the "Check" button). */
  retry(): void;
  /** Forgets everything in flight; no more state changes. */
  dispose(): void;
  readonly state: SchedulerState<R>;
}

export function createScheduler<P, R>(deps: SchedulerDeps<P, R>): Scheduler<P, R> {
  const quietMs = deps.quietMs ?? DEFAULT_QUIET_MS;
  const minGapMs = deps.minGapMs ?? DEFAULT_MIN_GAP_MS;
  const now = deps.now ?? (() => Date.now());
  const setTimer = deps.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
  const clearTimer = deps.clearTimer ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>));

  let state: SchedulerState<R> = { status: "idle", last: null, error: null };
  let latest: { payload: P; key: string } | null = null;
  let inFlightKey: string | null = null;
  let lastStart = Number.NEGATIVE_INFINITY;
  let timer: unknown = null;
  let disposed = false;

  function set(patch: Partial<SchedulerState<R>>): void {
    if (disposed) return;
    state = { ...state, ...patch };
    deps.onState(state);
  }

  function cancelTimer(): void {
    if (timer !== null) {
      clearTimer(timer);
      timer = null;
    }
  }

  /** True when `latest` still needs an answer. */
  function needsAnswer(): boolean {
    if (latest === null) return false;
    if (inFlightKey === latest.key) return false;
    if (state.error === null && state.last !== null && state.last.key === latest.key) return false;
    return true;
  }

  function schedule(delay: number): void {
    cancelTimer();
    timer = setTimer(() => {
      timer = null;
      tryStart();
    }, delay);
  }

  function tryStart(): void {
    if (disposed || !needsAnswer() || latest === null) {
      if (!disposed && inFlightKey === null && state.status !== "idle") set({ status: "idle" });
      return;
    }
    if (inFlightKey !== null) return; // the finishing request asks again
    const wait = lastStart + minGapMs - now();
    if (wait > 0) {
      schedule(wait);
      return;
    }
    const asked = latest;
    inFlightKey = asked.key;
    lastStart = now();
    set({ status: "checking" });
    deps.run(asked.payload).then(
      (result) => finish(asked.key, { last: { key: asked.key, result }, error: null }),
      (error: unknown) => finish(asked.key, { error }),
    );
  }

  function finish(key: string, patch: Partial<SchedulerState<R>>): void {
    inFlightKey = null;
    if (disposed) return;
    const stale = latest === null || latest.key !== key;
    if (stale) {
      // The answer belongs to an older payload: it writes nothing, and the
      // newest payload is asked next, after the gap.
      if (patch.error !== undefined && patch.error !== null) set({ error: patch.error });
      set({ status: "pending" });
      schedule(Math.max(0, lastStart + minGapMs - now()));
      return;
    }
    set({ ...patch, status: "idle" });
  }

  return {
    request(payload: P): void {
      if (disposed) return;
      const key = deps.keyOf(payload);
      if (latest !== null && latest.key === key) return;
      latest = { payload, key };
      if (!needsAnswer()) {
        // Back to a payload that is already answered (or in flight).
        cancelTimer();
        if (inFlightKey === null) set({ status: "idle", error: null });
        return;
      }
      set({ status: inFlightKey !== null ? "checking" : "pending", error: null });
      schedule(quietMs);
    },
    retry(): void {
      if (disposed || latest === null) return;
      set({ error: null });
      cancelTimer();
      if (inFlightKey !== null) return;
      // An explicit retry still respects the gap.
      schedule(Math.max(0, lastStart + minGapMs - now()));
      set({ status: "pending" });
    },
    dispose(): void {
      disposed = true;
      cancelTimer();
    },
    get state(): SchedulerState<R> {
      return state;
    },
  };
}
