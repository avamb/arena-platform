/**
 * Names of the flow steps the event center reports (spec 50, section 5).
 * A step name is a fixed word chosen here, never something the person typed.
 */

/** The wizard is one form with sections: a step is "the person started working in this section". */
export function wizardStepOfField(field: string): "event" | "venue" | "seating" | "tickets" | "dates" {
  if (field === "name" || field === "description" || field === "age" || field === "promoter" || field === "currency") return "event";
  if (field.startsWith("venue")) return "venue";
  if (field.startsWith("seating.")) return "seating";
  // A date's key is "<dateKey>.<suffix>": categories (c:) and price changes (s:) are tickets, the rest the date itself.
  if (field.includes(".c:") || field.includes(".s:")) return "tickets";
  return "dates";
}
