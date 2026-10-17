/**
 * Build-time feature flags (plan of 2026-10-14, stage 6).
 *
 * A flag is a Vite variable read once at build time: `VITE_FEATURE_WIZARD=1`
 * turns the event wizard on. Production builds leave it out, so the wizard
 * has no menu entry there and its address shows the "not available" screen.
 * A staging build sets it.
 */

/** True only for the exact value "1". Anything else, an empty value included, is off. */
export function flagOn(raw: unknown): boolean {
  return raw === "1";
}

export function wizardEnabled(raw: unknown = import.meta.env.VITE_FEATURE_WIZARD): boolean {
  return flagOn(raw);
}
