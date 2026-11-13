/**
 * The checks of a seating plan upload BEFORE any request (spec 64 sections 2.1
 * and 2.5). Pure: no React, no fetch. The server checks the same limits again
 * and has the last word. These checks only save the person a long upload that
 * the server would refuse.
 *
 * The kind of a file comes from its first bytes, never from its name. The one
 * exception is CSV: it has no signature, so it needs the `.csv` name AND text
 * that reads as UTF-8.
 */
import type { MessageKey } from "@/lib/i18n/en";
import type { Params } from "@/lib/i18n";

export const MB = 1024 * 1024;
/** At most this many files in one upload. */
export const MAX_FILES = 10;
/** At most this many MB in one upload, all files together. */
export const MAX_TOTAL_MB = 80;
/** How many bytes from the start of a file `detectKind` reads. */
export const HEAD_BYTES = 64 * 1024;
/** How many bytes from the end of a file `detectKind` reads (the central directory of a zip). */
export const TAIL_BYTES = 64 * 1024;
/** The terms the note under the upload links to (the same page the hosted pages use). */
export const PLAN_TERMS_URL = "https://arenasoldout.com/legal/terms";

export type PlanFileKind = "pdf" | "xlsx" | "xls" | "csv" | "jpeg" | "png" | "webp" | "heic" | "tiff" | "bmp" | "gif" | "svg";

/** Limit of one file by its kind, in MB (spec 64 section 2.1). */
export const KIND_LIMIT_MB: Readonly<Record<PlanFileKind, number>> = {
  pdf: 20,
  xlsx: 10,
  xls: 10,
  csv: 5,
  jpeg: 20,
  png: 20,
  webp: 20,
  heic: 20,
  tiff: 20,
  bmp: 20,
  gif: 20,
  svg: 3,
};

/** The file picker's `accept` list: kinds we know, by extension and by type. */
export const ACCEPT = ".pdf,.xlsx,.xls,.csv,.jpg,.jpeg,.png,.webp,.heic,.heif,.tif,.tiff,.bmp,.gif,.svg,application/pdf,image/*";

function ascii(s: string): number[] {
  return Array.from(s, (c) => c.charCodeAt(0));
}

function at(head: Uint8Array, bytes: readonly number[], offset = 0): boolean {
  if (head.length < offset + bytes.length) return false;
  return bytes.every((b, i) => head[offset + i] === b);
}

const XL_ENTRY = ascii("xl/");

function hasBytes(buf: Uint8Array, needle: readonly number[]): boolean {
  for (let i = 0; i + needle.length <= buf.length; i++) {
    if (at(buf, needle, i)) return true;
  }
  return false;
}

const HEIC_BRANDS = ["heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1"];

function readSvgStart(head: Uint8Array): boolean {
  let text = new TextDecoder("utf-8").decode(head).replace(/^﻿/, "");
  // Skip what may stand before the root element: blanks, the xml prolog, comments, a doctype.
  for (;;) {
    const next = text
      .replace(/^\s+/, "")
      .replace(/^<\?[\s\S]*?\?>/, "")
      .replace(/^<!--[\s\S]*?-->/, "")
      .replace(/^<!DOCTYPE[^>]*>/i, "");
    if (next === text) break;
    text = next;
  }
  return /^<svg[\s>/]/i.test(text);
}

function readsAsCsv(head: Uint8Array, name: string): boolean {
  if (!/\.csv$/i.test(name)) return false;
  if (head.includes(0)) return false;
  let text: string;
  try {
    // `stream` lets a character cut at the end of the head pass.
    text = new TextDecoder("utf-8", { fatal: true }).decode(head, { stream: true });
  } catch {
    return false;
  }
  return /[,;\t]/.test(text);
}

/** The kind of a file from its first bytes, or null when it is none of the accepted kinds. */
export function detectKind(head: Uint8Array, name: string, tail?: Uint8Array): PlanFileKind | null {
  const pdf = ascii("%PDF-");
  for (let i = 0; i <= Math.min(1024, head.length - pdf.length); i++) {
    if (at(head, pdf, i)) return "pdf";
  }
  if (at(head, [0xff, 0xd8, 0xff])) return "jpeg";
  if (at(head, [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])) return "png";
  if (at(head, ascii("GIF87a")) || at(head, ascii("GIF89a"))) return "gif";
  if (at(head, ascii("RIFF")) && at(head, ascii("WEBP"), 8)) return "webp";
  if (at(head, [0x49, 0x49, 0x2a, 0x00]) || at(head, [0x4d, 0x4d, 0x00, 0x2a])) return "tiff";
  // The reserved fields of a BMP header are zero: a text that starts with "BM" is not a BMP.
  if (at(head, ascii("BM")) && at(head, [0, 0, 0, 0], 6)) return "bmp";
  if (at(head, ascii("ftyp"), 4) && HEIC_BRANDS.some((b) => at(head, ascii(b), 8))) return "heic";
  // An OLE header is also the header of .doc and .msg files: the name must say .xls.
  if (at(head, [0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1]) && /\.xls$/i.test(name)) return "xls";
  // A zip header alone is also a .docx, a .zip or a .jar. Heuristic: the browser does not unpack
  // the zip, so it accepts a zip as XLSX only when the entry name "xl/" shows in the bytes it read.
  // The local entry names are near the start (the head), the central directory with all entry
  // names is at the end (the tail). The server unpacks the file and has the last word.
  if (at(head, [0x50, 0x4b, 0x03, 0x04]) && (hasBytes(head, XL_ENTRY) || (tail !== undefined && hasBytes(tail, XL_ENTRY)))) return "xlsx";
  if (readSvgStart(head)) return "svg";
  if (readsAsCsv(head, name)) return "csv";
  return null;
}

/** What the checks know of a picked file. */
export interface Candidate {
  name: string;
  size: number;
  head: Uint8Array;
  /** The last bytes of the file (where a zip keeps its central directory), when the file is longer than the head. */
  tail?: Uint8Array;
}

export interface Accepted {
  name: string;
  size: number;
  kind: PlanFileKind;
}

export type UploadProblem =
  | { kind: "unsupported"; name: string }
  | { kind: "tooLarge"; name: string; limitMb: number }
  | { kind: "empty"; name: string }
  /** The browser could not read the file (it moved, or the disk failed). */
  | { kind: "unreadable"; name: string }
  | { kind: "tooMany"; max: number }
  | { kind: "totalTooLarge"; limitMb: number };

/** Checks ONE file: its kind and the limit of that kind. */
export function classifyFile(c: Candidate): { accepted: Accepted } | { problem: UploadProblem } {
  if (c.size <= 0) return { problem: { kind: "empty", name: c.name } };
  const kind = detectKind(c.head, c.name, c.tail);
  if (kind === null) return { problem: { kind: "unsupported", name: c.name } };
  const limitMb = KIND_LIMIT_MB[kind];
  if (c.size > limitMb * MB) return { problem: { kind: "tooLarge", name: c.name, limitMb } };
  return { accepted: { name: c.name, size: c.size, kind } };
}

/** Does one more file of `size` bytes fit the files already in the list? */
export function fitsSet(sizes: readonly number[], size: number): UploadProblem | null {
  if (sizes.length + 1 > MAX_FILES) return { kind: "tooMany", max: MAX_FILES };
  const total = sizes.reduce((a, b) => a + b, 0) + size;
  if (total > MAX_TOTAL_MB * MB) return { kind: "totalTooLarge", limitMb: MAX_TOTAL_MB };
  return null;
}

/** A file in the list: `ref` is what the screen holds (the File), the rest is what the checks found. */
export interface Entry<T> extends Accepted {
  ref: T;
}

/**
 * Adds picked files to the list. A file that fails a check is left out and named
 * in `problems`. A problem of the whole list (too many files, too large together)
 * is named ONCE, however many files it left out.
 */
export function addCandidates<T>(current: readonly Entry<T>[], picked: readonly (Candidate & { ref: T })[]): { entries: Entry<T>[]; problems: UploadProblem[] } {
  const entries = [...current];
  const problems: UploadProblem[] = [];
  const listProblems = new Set<string>();
  for (const c of picked) {
    const one = classifyFile(c);
    if ("problem" in one) {
      problems.push(one.problem);
      continue;
    }
    const fit = fitsSet(entries.map((e) => e.size), one.accepted.size);
    if (fit !== null) {
      if (!listProblems.has(fit.kind)) {
        listProblems.add(fit.kind);
        problems.push(fit);
      }
      continue;
    }
    entries.push({ ...one.accepted, ref: c.ref });
  }
  return { entries, problems };
}

/** The text of a problem: the key and its values. The file name is shown next to it by the screen. */
export function problemText(p: UploadProblem): { key: MessageKey; params: Params } {
  switch (p.kind) {
    case "unsupported":
      return { key: "venuePlan.err.format", params: {} };
    case "tooLarge":
      return { key: "venuePlan.err.tooLarge", params: { limit: p.limitMb } };
    case "empty":
      return { key: "venuePlan.err.empty", params: {} };
    case "unreadable":
      return { key: "venuePlan.err.read", params: {} };
    case "tooMany":
      return { key: "venuePlan.err.tooMany", params: { max: p.max } };
    case "totalTooLarge":
      return { key: "venuePlan.err.totalTooLarge", params: { limit: p.limitMb } };
  }
}

/** The file name a problem is about, or null for a problem of the whole list. */
export function problemName(p: UploadProblem): string | null {
  return "name" in p ? p.name : null;
}

// ─── One Idempotency-Key per attempt ─────────────────────────────────────────

export interface UploadAttempt {
  key: string;
  /** What the attempt sends: the venue and the files. */
  signature: string;
}

export function uploadSignature(venueId: string, files: readonly { name: string; size: number; lastModified: number }[]): string {
  return `${venueId}|${files.map((f) => `${f.name}:${f.size}:${f.lastModified}`).join("|")}`;
}

/**
 * The key of the next send. The same files to the same venue keep the key of the
 * last attempt, so a repeat after a lost answer is the same upload for the
 * server and makes no second intake. Other files get a new key.
 */
export function attemptFor(prev: UploadAttempt | null, signature: string, makeKey: () => string): UploadAttempt {
  if (prev !== null && prev.signature === signature) return prev;
  return { key: makeKey(), signature };
}

/**
 * Keep the key after this answer? Yes when the server may have stored the files
 * and we never saw it: no answer at all, a timeout, or a server failure. A clear
 * refusal (4xx) or a success ends the attempt.
 */
export function keepKeyAfter(status: number): boolean {
  return status === 0 || status >= 500;
}

/** Megabytes with one decimal, for a list of files. */
export function sizeLabel(bytes: number): string {
  const mb = bytes / MB;
  return mb >= 0.1 ? `${mb.toFixed(1)} MB` : `${Math.max(1, Math.round(bytes / 1024))} KB`;
}
