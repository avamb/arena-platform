/**
 * The organization's theme (spec 62 §12.1, decision R-24) and the light/dark
 * switch of the event center.
 *
 * The organization stores its tokens in `organizations.branding` (migration
 * 0203); `GET /v1/organizations/{org_id}/me` answers them. OrgLayout calls
 * applyBranding() when the organization loads: the tokens go into `:root`
 * as `--ec-*` custom properties through the CSSOM (allowed by the page's
 * `style-src 'self'`, unlike a style attribute), and styles.css maps them
 * onto the tokens every screen already uses (--accent, --text, --bg...).
 * Without branding the platform palette of arenasoldout.com applies.
 *
 * The dark theme does not use the organization's light colours as they are:
 * the brand colour is lightened towards white until it reads on the dark
 * background (contrast >= 4.5:1), see darkPrimary().
 */

/** The tokens an organization may set; every key is optional. */
export interface Branding {
  primary?: string;
  on_primary?: string;
  on_primary_muted?: string;
  ink?: string;
  accent?: string;
  bg?: string;
  font?: string;
}

/** The arenasoldout.com palette: every organization without its own theme. */
export const PLATFORM_THEME: Required<Branding> = {
  primary: "#001489",
  on_primary: "#ffffff",
  on_primary_muted: "rgba(255, 255, 255, .72)",
  ink: "#0b1020",
  accent: "#001489",
  bg: "#f6f7fb",
  font: "Onest",
};

/**
 * Vino&Co's theme, an example of a full branding object (spec 62 §12.1). The
 * owner sets it on the organization with
 * `PATCH /v1/organizations/{id}` `{"branding": VINO_THEME_EXAMPLE}`.
 */
export const VINO_THEME_EXAMPLE: Required<Branding> = {
  primary: "#822121",
  on_primary: "#ffffff",
  on_primary_muted: "rgba(255, 255, 255, .72)",
  ink: "#2c2c2c",
  accent: "#d9a441",
  bg: "#f0e6dc",
  font: "Assistant",
};

/** The font allow-list (the same as the API's). */
export const THEME_FONTS = ["Assistant", "Onest", "Golos Text"] as const;

const FONT_FALLBACK = 'system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';

const HEX = /^#[0-9a-fA-F]{6}$/;
const RGBA = /^rgba\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(0|1|0?\.\d{1,3}|1\.0{1,3})\s*\)$/;

/** True for "#rrggbb" or "rgba(r, g, b, a)" — the only colour forms the API stores. */
export function isThemeColor(v: unknown): v is string {
  if (typeof v !== "string") return false;
  if (HEX.test(v)) return true;
  const m = RGBA.exec(v);
  return m !== null && [m[1], m[2], m[3]].every((x) => Number(x) <= 255);
}

/** The organization's branding over the platform palette; unknown or invalid values are ignored. */
export function resolveTheme(branding: Branding | null | undefined): Required<Branding> {
  const out: Required<Branding> = { ...PLATFORM_THEME };
  if (branding === null || branding === undefined) return out;
  for (const key of ["primary", "on_primary", "on_primary_muted", "ink", "accent", "bg"] as const) {
    const v = branding[key];
    if (isThemeColor(v)) out[key] = v;
  }
  if (typeof branding.font === "string" && (THEME_FONTS as readonly string[]).includes(branding.font)) {
    out.font = branding.font;
  }
  return out;
}

type RGB = [number, number, number];

function parseHex(hex: string): RGB | null {
  if (!HEX.test(hex)) return null;
  return [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)) as RGB;
}

function toHex(rgb: RGB): string {
  return "#" + rgb.map((c) => Math.round(Math.min(255, Math.max(0, c))).toString(16).padStart(2, "0")).join("");
}

function luminance([r, g, b]: RGB): number {
  const lin = (c: number) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

/** WCAG contrast ratio of two "#rrggbb" colours; 0 when either is not hex. */
export function contrastRatio(a: string, b: string): number {
  const x = parseHex(a);
  const y = parseHex(b);
  if (x === null || y === null) return 0;
  const [hi, lo] = [luminance(x), luminance(y)].sort((p, q) => q - p);
  return (hi + 0.05) / (lo + 0.05);
}

/** The dark theme's page background and surface (styles.css keeps the same values). */
export const DARK_BG = "#121219";
export const DARK_SURFACE = "#1c1c26";

/**
 * The brand colour for the dark theme: the light primary mixed with white
 * until it reaches 4.5:1 against the dark surface (links, the active tab and
 * the primary button with dark text all use it).
 */
export function darkPrimary(primary: string): string {
  const base = parseHex(primary) ?? parseHex(PLATFORM_THEME.primary)!;
  for (let w = 0.3; w <= 1.0001; w += 0.05) {
    const mixed: RGB = [0, 1, 2].map((i) => base[i] + (255 - base[i]) * w) as RGB;
    const hex = toHex(mixed);
    if (contrastRatio(hex, DARK_SURFACE) >= 4.5) return hex;
  }
  return "#ffffff";
}

/**
 * Text colour on the brand colour in the light theme: the organization's
 * on_primary when it reads (>= 4.5:1), else black or white, whichever reads
 * better. A non-hex value (rgba) is trusted as given.
 */
export function readableOn(bg: string, preferred: string): string {
  if (!HEX.test(bg) || !HEX.test(preferred)) return preferred;
  if (contrastRatio(bg, preferred) >= 4.5) return preferred;
  return contrastRatio(bg, "#ffffff") >= contrastRatio(bg, "#000000") ? "#ffffff" : "#000000";
}

/** The CSS custom properties applyBranding writes into :root. */
export function themeVariables(branding: Branding | null | undefined): Record<string, string> {
  const t = resolveTheme(branding);
  return {
    "--ec-primary": t.primary,
    "--ec-on-primary": readableOn(t.primary, t.on_primary),
    "--ec-on-primary-muted": t.on_primary_muted,
    "--ec-ink": t.ink,
    "--ec-accent": t.accent,
    "--ec-bg": t.bg,
    "--ec-font": `"${t.font}", ${FONT_FALLBACK}`,
    "--ec-primary-dark": darkPrimary(t.primary),
  };
}

/** Writes the organization's theme into :root and the browser's theme colour. */
export function applyBranding(branding: Branding | null | undefined, doc: Document = document): void {
  const root = doc.documentElement;
  for (const [name, value] of Object.entries(themeVariables(branding))) {
    root.style.setProperty(name, value);
  }
  const meta = doc.querySelector('meta[name="theme-color"]');
  if (meta !== null) meta.setAttribute("content", resolveTheme(branding).primary);
}

/** Back to the platform palette (leaving an organization, signing out). */
export function resetBranding(doc: Document = document): void {
  applyBranding(null, doc);
}

// ── Light / dark ───────────────────────────────────────────────────────

export type ThemeMode = "system" | "light" | "dark";
export const THEME_MODES: readonly ThemeMode[] = ["system", "light", "dark"];
const THEME_KEY = "arena.events.theme";

/** The stored choice; "system" (follow the device) when nothing or something unknown is stored. */
export function storedThemeMode(): ThemeMode {
  try {
    const v = localStorage.getItem(THEME_KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

/** Applies the choice: data-theme on <html> for light or dark, none for system. */
export function applyThemeMode(mode: ThemeMode, doc: Document = document): void {
  const root = doc.documentElement;
  if (mode === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", mode);
}

/** Stores and applies the choice. */
export function setThemeMode(mode: ThemeMode): void {
  try {
    if (mode === "system") localStorage.removeItem(THEME_KEY);
    else localStorage.setItem(THEME_KEY, mode);
  } catch {
    // A convenience only: the choice still applies to this page.
  }
  applyThemeMode(mode);
}

// ── "Add to home screen" ───────────────────────────────────────────────

const A2HS_KEY = "arena.events.a2hs_dismissed";

/** True once the person closed the "Add to home screen" note; it is shown once. */
export function a2hsDismissed(): boolean {
  try {
    return localStorage.getItem(A2HS_KEY) === "1";
  } catch {
    return true;
  }
}

export function dismissA2hs(): void {
  try {
    localStorage.setItem(A2HS_KEY, "1");
  } catch {
    // A convenience only.
  }
}

/** True when the app already runs from the home screen. */
export function runsStandalone(): boolean {
  try {
    return (
      window.matchMedia?.("(display-mode: standalone)").matches === true ||
      (navigator as Navigator & { standalone?: boolean }).standalone === true
    );
  } catch {
    return false;
  }
}
