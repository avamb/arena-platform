/**
 * The state machine behind DryRunConfirm: "see what will change, then one
 * press". A screen hands the component a dry run (a read-only API call, such as
 * a session change impact or an event delete impact) and the confirm action.
 *
 * Rules:
 * - the confirm button exists only when the dry run answered and did NOT block;
 * - the confirm action runs once per press and cannot be pressed twice while it works;
 * - a failure of the dry run or of the action keeps the screen and says what to do next.
 */

export interface DryRunFigure {
  label: string;
  value: string | number;
}

/** What a dry run reports. Texts are already in the person's language (the API writes them). */
export interface DryRunResult {
  /** One line about the whole change, for example "Moves 12 orders". */
  summary?: string;
  /** Numbers the person should see before the press. */
  figures?: DryRunFigure[];
  /** Things to know. They do not stop the action. */
  warnings?: string[];
  /** Set when the change cannot be done. The reason says what to do instead. */
  blocked?: string | null;
}

export type DryRunState =
  | { phase: "loading" }
  | { phase: "ready"; result: DryRunResult }
  | { phase: "failed"; error: unknown }
  | { phase: "working"; result: DryRunResult }
  | { phase: "done"; result: DryRunResult }
  | { phase: "actionFailed"; result: DryRunResult; error: unknown };

export type DryRunEvent =
  | { type: "load" }
  | { type: "loaded"; result: DryRunResult }
  | { type: "loadFailed"; error: unknown }
  | { type: "press" }
  | { type: "succeeded" }
  | { type: "actionFailed"; error: unknown };

export const initialDryRunState: DryRunState = { phase: "loading" };

export function isBlocked(result: DryRunResult): boolean {
  return result.blocked !== undefined && result.blocked !== null && result.blocked !== "";
}

/** True when the confirm button may be pressed now. */
export function canConfirm(state: DryRunState): boolean {
  switch (state.phase) {
    case "ready":
    case "actionFailed":
      return !isBlocked(state.result);
    default:
      return false;
  }
}

export function reduceDryRun(state: DryRunState, event: DryRunEvent): DryRunState {
  switch (event.type) {
    case "load":
      // Reloading while the action works would hide a change in progress.
      return state.phase === "working" ? state : { phase: "loading" };
    case "loaded":
      return state.phase === "loading" ? { phase: "ready", result: event.result } : state;
    case "loadFailed":
      return state.phase === "loading" ? { phase: "failed", error: event.error } : state;
    case "press":
      return canConfirm(state) && (state.phase === "ready" || state.phase === "actionFailed")
        ? { phase: "working", result: state.result }
        : state;
    case "succeeded":
      return state.phase === "working" ? { phase: "done", result: state.result } : state;
    case "actionFailed":
      return state.phase === "working" ? { phase: "actionFailed", result: state.result, error: event.error } : state;
  }
}
