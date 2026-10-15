/**
 * Pure rules of the promoter screens (task 4-6, spec 37 R3). They mirror the
 * bot's `eventbot/promoters.go`: the screen checks the SHAPE of what a person
 * typed so a typo is caught before a call, and the API decides the rest
 * (a taken name, a taken page address, a website that is not an address).
 */
import { ApiError } from "@/lib/api/http";
import type { Promoter } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n";

/** The fields of a promoter that a person edits one at a time. */
export type PromoterField = "name" | "address" | "legal_id" | "phone" | "email" | "website" | "slug";

export const PROMOTER_FIELDS: readonly PromoterField[] = ["name", "address", "legal_id", "phone", "email", "website", "slug"];

/** Optional fields can be cleared. The name cannot, and a page address is changed, never cleared. */
export function canClear(field: PromoterField): boolean {
  return field !== "name" && field !== "slug";
}

export const MAX_NAME = 120;
export const MAX_TEXT = 300; // address and website, as the API
export const MAX_TAX = 40;
export const MAX_EMAIL = 120;

export type ParseResult = { ok: true; value: string } | { ok: false; reason: MessageKey };

function runes(s: string): number {
  return [...s].length;
}

/** The page address alphabet: 2-64 characters of a-z, 0-9 and single inner hyphens. */
export function validSlug(s: string): boolean {
  if (s.length < 2 || s.length > 64) return false;
  let prevHyphen = true;
  for (let i = 0; i < s.length; i += 1) {
    const c = s[i];
    if ((c >= "a" && c <= "z") || (c >= "0" && c <= "9")) {
      prevHyphen = false;
    } else if (c === "-") {
      if (prevHyphen || i === s.length - 1) return false;
      prevHyphen = true;
    } else {
      return false;
    }
  }
  return true;
}

/** Lower case, and a whole pasted link is cut down to its last path segment. */
export function slugFromInput(raw: string): string {
  let s = raw.trim().toLowerCase();
  if (s.includes("/")) {
    const parts = s.replace(/\/+$/, "").split("/");
    s = parts[parts.length - 1] ?? "";
  }
  return s.trim();
}

function looksLikeEmail(s: string): boolean {
  if (/\s/.test(s)) return false;
  const at = s.indexOf("@");
  if (at < 1 || at !== s.lastIndexOf("@")) return false;
  const domain = s.slice(at + 1);
  return domain.includes(".") && !domain.startsWith(".") && !domain.endsWith(".");
}

/** Checks the shape of what was typed for a field. The text comes back folded the way it is sent. */
export function parseField(field: PromoterField, raw: string): ParseResult {
  switch (field) {
    case "name": {
      const s = raw.trim().split(/\s+/).filter(Boolean).join(" ");
      if (s === "") return { ok: false, reason: "promoters.err.nameEmpty" };
      if (runes(s) > MAX_NAME) return { ok: false, reason: "promoters.err.nameLong" };
      return { ok: true, value: s };
    }
    case "address": {
      const s = raw.trim().split(/\s+/).filter(Boolean).join(" ");
      if (s === "" || runes(s) > MAX_TEXT) return { ok: false, reason: "promoters.err.address" };
      return { ok: true, value: s };
    }
    case "legal_id": {
      const s = raw.trim();
      if (s === "" || runes(s) > MAX_TAX || /[\r\n]/.test(s)) return { ok: false, reason: "promoters.err.tax" };
      return { ok: true, value: s };
    }
    case "phone": {
      const s = raw.trim();
      let digits = 0;
      for (const ch of s) {
        if (/\d/.test(ch)) digits += 1;
        else if (!"+ ()-.".includes(ch)) return { ok: false, reason: "promoters.err.phone" };
      }
      if (digits < 6 || digits > 15) return { ok: false, reason: "promoters.err.phone" };
      return { ok: true, value: s };
    }
    case "email": {
      const s = raw.trim().toLowerCase();
      if (!looksLikeEmail(s) || runes(s) > MAX_EMAIL) return { ok: false, reason: "promoters.err.email" };
      return { ok: true, value: s };
    }
    case "website": {
      const s = raw.trim();
      if (s === "" || runes(s) > MAX_TEXT || /\s/.test(s) || !s.includes(".")) return { ok: false, reason: "promoters.err.website" };
      return { ok: true, value: s };
    }
    case "slug": {
      const s = slugFromInput(raw);
      if (!validSlug(s)) return { ok: false, reason: "promoters.err.slugInvalid" };
      return { ok: true, value: s };
    }
  }
}

/** What the promoter holds in a field now ("" = not set). */
export function fieldValue(p: Promoter, field: PromoterField): string {
  const v = p[field];
  return typeof v === "string" ? v : "";
}

/**
 * Does saving this page address need the "old -> new" question? Only when a
 * working address would be replaced by a different one: links already handed
 * out with the old address stop working.
 */
export function slugChangeNeedsConfirm(current: string | null | undefined, next: string): boolean {
  return current !== null && current !== undefined && current !== "" && current !== next;
}

/** The refusals of POST and PATCH of a promoter that have words of their own. */
export function promoterErrorKey(err: unknown): MessageKey | null {
  if (!(err instanceof ApiError)) return null;
  switch (err.code) {
    case "promoter.duplicate_name":
      return "promoters.err.nameTaken";
    case "promoter.duplicate_slug":
      return "promoters.err.slugTaken";
    case "promoter.invalid_slug":
      return "promoters.err.slugInvalid";
    case "promoter.invalid_name":
      return "promoters.err.nameEmpty";
    case "promoter.invalid_website":
      return "promoters.err.website";
    case "promoter.invalid_address":
      return "promoters.err.address";
  }
  return null;
}

export type PromoterFilter = "active" | "archived";

/** Active promoters by name, or the archived ones by name. */
export function filterPromoters(all: readonly Promoter[], filter: PromoterFilter): Promoter[] {
  return all
    .filter((p) => (filter === "archived" ? p.archived : !p.archived))
    .sort((a, b) => a.name.localeCompare(b.name));
}
