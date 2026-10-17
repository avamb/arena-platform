/**
 * Build-time feature flags (plan of 2026-10-14, stage 6).
 *
 * A flag is a Vite variable read once at build time. The event wizard
 * (create event, 6-1c) ships ON in the release image: `Dockerfile` defaults
 * `VITE_FEATURE_WIZARD` to `1`, and the build argument `VITE_FEATURE_WIZARD=0`
 * is the rollback (no menu entry, no button, the address shows the "not
 * available" screen). A bare `vite`/`vitest` run without the variable keeps it
 * off, so a build nobody configured never offers a write by accident.
 * `src/lib/features.test.ts` pins both the rule and the Dockerfile default.
 */

/** True only for the exact value "1". Anything else, an empty value included, is off. */
export function flagOn(raw: unknown): boolean {
  return raw === "1";
}

export function wizardEnabled(raw: unknown = import.meta.env.VITE_FEATURE_WIZARD): boolean {
  return flagOn(raw);
}

/** The permission the import route itself demands (`imports_shims.go`); no new permission model. */
export const CREATE_EVENT_PERMISSION = "import.bil24_session";

export type WizardGate = "off" | "noAccess" | "ok";

/**
 * Where a person lands: the build has no wizard ("off": "not found"), the
 * person may not write ("noAccess"), or the form is theirs ("ok"). The menu
 * entry, the list button and the screen all ask this one function.
 */
export function wizardGate(can: (permission: string) => boolean, flag: unknown = import.meta.env.VITE_FEATURE_WIZARD): WizardGate {
  if (!wizardEnabled(flag)) return "off";
  return can(CREATE_EVENT_PERMISSION) ? "ok" : "noAccess";
}
