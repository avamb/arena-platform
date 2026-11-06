// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import {
  DARK_BG,
  DARK_SURFACE,
  PLATFORM_THEME,
  VINO_THEME_EXAMPLE,
  a2hsDismissed,
  applyBranding,
  applyThemeMode,
  contrastRatio,
  darkPrimary,
  dismissA2hs,
  isThemeColor,
  readableOn,
  resetBranding,
  resolveTheme,
  setThemeMode,
  storedThemeMode,
  themeVariables,
} from "@/lib/theme";

const css = readFileSync(path.resolve(__dirname, "../styles.css"), "utf8");

afterEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.removeAttribute("style");
});

describe("organization theme", () => {
  it("falls back to the arenasoldout.com palette", () => {
    expect(resolveTheme(undefined)).toEqual(PLATFORM_THEME);
    expect(PLATFORM_THEME.primary).toBe("#001489");
    expect(PLATFORM_THEME.ink).toBe("#0b1020");
    expect(PLATFORM_THEME.bg).toBe("#f6f7fb");
    expect(PLATFORM_THEME.font).toBe("Onest");
  });

  it("takes the organization's valid tokens and ignores the rest", () => {
    const t = resolveTheme({ primary: "#822121", ink: "red", font: "Comic Sans", bg: "rgba(1, 2, 3, .5)" });
    expect(t.primary).toBe("#822121");
    expect(t.ink).toBe(PLATFORM_THEME.ink);
    expect(t.font).toBe(PLATFORM_THEME.font);
    expect(t.bg).toBe("rgba(1, 2, 3, .5)");
  });

  it("accepts only #rrggbb and rgba()", () => {
    expect(isThemeColor("#abcdef")).toBe(true);
    expect(isThemeColor("rgba(255,255,255,.72)")).toBe(true);
    expect(isThemeColor("rgba(256,0,0,1)")).toBe(false);
    expect(isThemeColor("#abc")).toBe(false);
    expect(isThemeColor("url(x)")).toBe(false);
  });

  it("writes the tokens into :root and the browser colour", () => {
    const meta = document.createElement("meta");
    meta.name = "theme-color";
    document.head.appendChild(meta);
    applyBranding(VINO_THEME_EXAMPLE);
    const root = document.documentElement.style;
    expect(root.getPropertyValue("--ec-primary")).toBe("#822121");
    expect(root.getPropertyValue("--ec-bg")).toBe("#f0e6dc");
    expect(root.getPropertyValue("--ec-font")).toContain('"Assistant"');
    expect(meta.getAttribute("content")).toBe("#822121");
    resetBranding();
    expect(root.getPropertyValue("--ec-primary")).toBe("#001489");
    meta.remove();
  });

  it("every :root token the stylesheet defaults is one applyBranding writes", () => {
    for (const name of Object.keys(themeVariables(null))) {
      expect(css, name).toContain(`${name}:`);
    }
    expect(css).toContain(`--ec-primary-dark: ${darkPrimary(PLATFORM_THEME.primary)};`);
  });
});

describe("contrast", () => {
  it("keeps text readable in both palettes", () => {
    for (const theme of [PLATFORM_THEME, VINO_THEME_EXAMPLE]) {
      expect(contrastRatio(theme.ink, theme.bg)).toBeGreaterThanOrEqual(4.5);
      expect(contrastRatio(theme.primary, "#ffffff")).toBeGreaterThanOrEqual(4.5);
      expect(contrastRatio(theme.primary, theme.bg)).toBeGreaterThanOrEqual(4.5);
      expect(contrastRatio(theme.on_primary, theme.primary)).toBeGreaterThanOrEqual(4.5);
      const dark = darkPrimary(theme.primary);
      expect(contrastRatio(dark, DARK_SURFACE)).toBeGreaterThanOrEqual(4.5);
      expect(contrastRatio(dark, DARK_BG)).toBeGreaterThanOrEqual(4.5);
    }
    // The muted text of the stylesheet on both light grounds and the dark one.
    expect(contrastRatio("#4f5262", VINO_THEME_EXAMPLE.bg)).toBeGreaterThanOrEqual(4.5);
    expect(contrastRatio("#4f5262", PLATFORM_THEME.bg)).toBeGreaterThanOrEqual(4.5);
    expect(contrastRatio("#b4b6c6", DARK_SURFACE)).toBeGreaterThanOrEqual(4.5);
  });

  it("lightens even a very dark brand colour until it reads on the dark theme", () => {
    expect(contrastRatio(darkPrimary("#000000"), DARK_SURFACE)).toBeGreaterThanOrEqual(4.5);
    expect(contrastRatio(darkPrimary("#2b0000"), DARK_SURFACE)).toBeGreaterThanOrEqual(4.5);
  });

  it("replaces an unreadable on-primary colour", () => {
    expect(readableOn("#f0e6dc", "#ffffff")).toBe("#000000");
    expect(readableOn("#822121", "#ffffff")).toBe("#ffffff");
  });
});

describe("light / dark switch", () => {
  it("follows the device until the person picks", () => {
    expect(storedThemeMode()).toBe("system");
    applyThemeMode("system");
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
  });

  it("keeps the choice in this browser", () => {
    setThemeMode("dark");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(storedThemeMode()).toBe("dark");
    setThemeMode("system");
    expect(storedThemeMode()).toBe("system");
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
  });

  it("styles both the chosen dark theme and the device's dark theme", () => {
    expect(css).toContain(':root[data-theme="dark"]');
    expect(css).toContain(':root:not([data-theme="light"])');
  });
});

describe("add to home screen", () => {
  it("is shown once", () => {
    expect(a2hsDismissed()).toBe(false);
    dismissA2hs();
    expect(a2hsDismissed()).toBe(true);
  });
});
