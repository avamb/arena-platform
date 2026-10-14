/**
 * The state machine behind DateField (the bot's date question, wizard_dates.go,
 * in the browser). A pressed day inside the range is the answer at once. A typed
 * date is NEVER the answer: it becomes a list of readings (`pending`) and the
 * person confirms one of them.
 */
import { dateCandidates, inBounds, monthOf, boundsEmpty, clampMonth, type DateBounds } from "@/lib/dates";
import type { MessageKey } from "@/lib/i18n";

export interface DateFieldState {
  /** The month the grid shows, "YYYY-MM". */
  month: string;
  /** What the person typed. */
  typed: string;
  /** Readings of the typed text inside the range, waiting for a choice. */
  pending: string[];
  /** The text the readings were made from, shown in the question. */
  raw: string;
  /** A note under the field: why a date was not taken. */
  note: MessageKey | null;
}

export type DateFieldAction =
  | { type: "month"; month: string }
  | { type: "pick"; iso: string }
  | { type: "type"; text: string }
  | { type: "submit" }
  | { type: "confirm"; iso: string }
  | { type: "retry" };

export interface DateFieldContext {
  today: string;
  bounds: DateBounds;
}

export function initialDateFieldState(value: string | null, ctx: DateFieldContext): DateFieldState {
  const preferred = value !== null && value !== "" ? monthOf(value) : monthOf(ctx.today);
  return { month: clampMonth(preferred, ctx.bounds), typed: "", pending: [], raw: "", note: null };
}

/** Why a date outside the range was refused. */
export function outOfRangeNote(iso: string, today: string, bounds: DateBounds): MessageKey {
  if (iso < today) return "dateField.error.past";
  if (iso < bounds.min) return "dateField.error.early";
  return "dateField.error.late";
}

export interface DateFieldResult {
  state: DateFieldState;
  /** Set when the person has chosen a day: the caller stores it. */
  picked: string | null;
}

export function reduceDateField(state: DateFieldState, action: DateFieldAction, ctx: DateFieldContext): DateFieldResult {
  const keep = (patch: Partial<DateFieldState>): DateFieldResult => ({ state: { ...state, ...patch }, picked: null });
  switch (action.type) {
    case "month":
      return keep({ month: clampMonth(action.month, ctx.bounds) });

    case "pick":
      if (!inBounds(action.iso, ctx.bounds)) return keep({ note: outOfRangeNote(action.iso, ctx.today, ctx.bounds) });
      return {
        state: { ...state, month: monthOf(action.iso), typed: "", pending: [], raw: "", note: null },
        picked: action.iso,
      };

    case "type":
      // Typing again drops readings made from older text.
      return keep({ typed: action.text, pending: [], raw: "", note: null });

    case "submit": {
      const text = state.typed.trim();
      if (text === "") return keep({ note: "dateField.error.empty" });
      const readings = dateCandidates(text, ctx.today);
      if (readings.length === 0) return keep({ pending: [], raw: "", note: "dateField.error.notDate" });
      const ok = readings.filter((r) => inBounds(r, ctx.bounds));
      if (ok.length === 0) return keep({ pending: [], raw: "", note: outOfRangeNote(readings[0], ctx.today, ctx.bounds) });
      return keep({ pending: ok, raw: text, note: null });
    }

    case "confirm":
      // Only a reading the person was offered counts, and it is checked again.
      if (!state.pending.includes(action.iso) || !inBounds(action.iso, ctx.bounds)) return keep({});
      return {
        state: { ...state, month: monthOf(action.iso), typed: "", pending: [], raw: "", note: null },
        picked: action.iso,
      };

    case "retry":
      return keep({ pending: [], raw: "", typed: "", note: null });
  }
}

/** True when the range holds no day at all (the field says so instead of drawing a grid). */
export function noDaysToPick(ctx: DateFieldContext): boolean {
  return boundsEmpty(ctx.bounds);
}
