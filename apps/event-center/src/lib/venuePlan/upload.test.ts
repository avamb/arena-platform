import { describe, expect, it } from "vitest";
import {
  KIND_LIMIT_MB,
  addCandidates,
  MAX_FILES,
  MAX_TOTAL_MB,
  MB,
  attemptFor,
  classifyFile,
  detectKind,
  fitsSet,
  keepKeyAfter,
  problemName,
  problemText,
  uploadSignature,
} from "@/lib/venuePlan/upload";

const bytes = (...b: number[]) => Uint8Array.from(b);
const text = (s: string) => new TextEncoder().encode(s);
/** The start of a zip: a local header and the entry names. */
const zip = (...names: string[]) => bytes(0x50, 0x4b, 0x03, 0x04, 0x14, 0, ...Array.from(text(names.join("\0"))));

describe("detectKind reads the first bytes", () => {
  it("knows every signature of spec 64 section 2.1", () => {
    expect(detectKind(text("%PDF-1.7\n"), "a.bin")).toBe("pdf");
    expect(detectKind(bytes(0xff, 0xd8, 0xff, 0xe0), "a.bin")).toBe("jpeg");
    expect(detectKind(bytes(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0), "a.bin")).toBe("png");
    expect(detectKind(text("GIF89a...."), "a.bin")).toBe("gif");
    expect(detectKind(bytes(...Array.from(text("RIFF")), 1, 2, 3, 4, ...Array.from(text("WEBP"))), "a.bin")).toBe("webp");
    expect(detectKind(bytes(0x49, 0x49, 0x2a, 0x00, 8), "a.bin")).toBe("tiff");
    expect(detectKind(bytes(0x4d, 0x4d, 0x00, 0x2a), "a.bin")).toBe("tiff");
    expect(detectKind(bytes(0x42, 0x4d, 1, 2, 3, 4, 0, 0, 0, 0, 9), "a.bin")).toBe("bmp");
    expect(detectKind(bytes(0, 0, 0, 0x18, ...Array.from(text("ftypheic"))), "a.bin")).toBe("heic");
    expect(detectKind(bytes(0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1), "a.xls")).toBe("xls");
    expect(detectKind(zip("xl/workbook.xml"), "a.bin")).toBe("xlsx");
  });

  it("does not take a zip or an OLE file for a spreadsheet by its header alone", () => {
    // A .docx: a zip with word/ entries.
    expect(detectKind(zip("[Content_Types].xml", "word/document.xml"), "a.docx")).toBeNull();
    expect(classifyFile({ name: "a.docx", size: 100, head: zip("word/document.xml") })).toEqual({ problem: { kind: "unsupported", name: "a.docx" } });
    // A bare zip header.
    expect(detectKind(bytes(0x50, 0x4b, 0x03, 0x04, 0), "a.xlsx")).toBeNull();
    // An OLE file that is not named .xls (a .doc).
    expect(detectKind(bytes(0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1), "a.doc")).toBeNull();
  });

  it("finds xl/ in the central directory at the end of a long zip", () => {
    const head = zip("[Content_Types].xml", "docProps/app.xml");
    const tail = text("PK\u0001\u0002 xl/worksheets/sheet1.xml PK\u0005\u0006");
    expect(detectKind(head, "big.xlsx")).toBeNull();
    expect(detectKind(head, "big.xlsx", tail)).toBe("xlsx");
    expect(classifyFile({ name: "big.xlsx", size: 5 * MB, head, tail })).toEqual({ accepted: { name: "big.xlsx", size: 5 * MB, kind: "xlsx" } });
  });

  it("does not trust the name", () => {
    expect(detectKind(text("just words"), "plan.pdf")).toBeNull();
    expect(detectKind(text("%PDF-1.4"), "plan.txt")).toBe("pdf");
  });

  it("reads an SVG after a prolog and a comment", () => {
    expect(detectKind(text('<svg xmlns="http://www.w3.org/2000/svg"/>'), "a.svg")).toBe("svg");
    expect(detectKind(text('﻿<?xml version="1.0"?>\n<!-- made by hand -->\n<svg viewBox="0 0 1 1"/>'), "a.svg")).toBe("svg");
    expect(detectKind(text("<html><svg></svg></html>"), "a.svg")).toBeNull();
  });

  it("takes a CSV only with the .csv name and readable text", () => {
    expect(detectKind(text("row,seat\n1,2\n"), "seats.csv")).toBe("csv");
    expect(detectKind(text("row;seat\n1;2\n"), "SEATS.CSV")).toBe("csv");
    expect(detectKind(text("row,seat\n1,2\n"), "seats.txt")).toBeNull();
    expect(detectKind(bytes(0x61, 0x2c, 0x00, 0x62), "seats.csv")).toBeNull();
    expect(detectKind(bytes(0x61, 0x2c, 0xff, 0xfe, 0xfd), "seats.csv")).toBeNull();
    expect(detectKind(text("no delimiter at all"), "seats.csv")).toBeNull();
  });

  it("lets a text starting with BM pass as a CSV, not as a BMP", () => {
    expect(detectKind(text("BM,seat\n1,2\n"), "list.csv")).toBe("csv");
  });
});

describe("classifyFile", () => {
  const pdf = text("%PDF-1.7");

  it("accepts a file inside its limit", () => {
    expect(classifyFile({ name: "hall.pdf", size: 20 * MB, head: pdf })).toEqual({ accepted: { name: "hall.pdf", size: 20 * MB, kind: "pdf" } });
  });

  it("refuses a file over the limit of its kind", () => {
    expect(classifyFile({ name: "hall.pdf", size: 20 * MB + 1, head: pdf })).toEqual({ problem: { kind: "tooLarge", name: "hall.pdf", limitMb: 20 } });
    expect(classifyFile({ name: "a.svg", size: 3 * MB + 1, head: text("<svg/>") })).toEqual({ problem: { kind: "tooLarge", name: "a.svg", limitMb: 3 } });
    expect(classifyFile({ name: "a.csv", size: 5 * MB + 1, head: text("a,b") })).toEqual({ problem: { kind: "tooLarge", name: "a.csv", limitMb: 5 } });
  });

  it("refuses an empty file and an unknown kind", () => {
    expect(classifyFile({ name: "e.pdf", size: 0, head: new Uint8Array() })).toEqual({ problem: { kind: "empty", name: "e.pdf" } });
    expect(classifyFile({ name: "x.docx", size: 10, head: text("hello") })).toEqual({ problem: { kind: "unsupported", name: "x.docx" } });
  });

  it("has a limit for every kind", () => {
    expect(Object.keys(KIND_LIMIT_MB)).toHaveLength(12);
    expect(KIND_LIMIT_MB.xlsx).toBe(10);
    expect(KIND_LIMIT_MB.heic).toBe(20);
  });
});

describe("fitsSet", () => {
  it("allows up to ten files and 80 MB together", () => {
    expect(fitsSet([], 1)).toBeNull();
    expect(fitsSet(new Array(MAX_FILES - 1).fill(1), 1)).toBeNull();
    expect(fitsSet(new Array(MAX_FILES).fill(1), 1)).toEqual({ kind: "tooMany", max: 10 });
    expect(fitsSet([20 * MB, 20 * MB, 20 * MB], 20 * MB)).toBeNull();
    expect(fitsSet([20 * MB, 20 * MB, 20 * MB, 20 * MB], 1)).toEqual({ kind: "totalTooLarge", limitMb: MAX_TOTAL_MB });
  });
});

describe("the texts of the problems", () => {
  it("name the key, the limit and the file", () => {
    expect(problemText({ kind: "tooLarge", name: "a.pdf", limitMb: 20 })).toEqual({ key: "venuePlan.err.tooLarge", params: { limit: 20 } });
    expect(problemText({ kind: "unsupported", name: "a" }).key).toBe("venuePlan.err.format");
    expect(problemText({ kind: "tooMany", max: 10 })).toEqual({ key: "venuePlan.err.tooMany", params: { max: 10 } });
    expect(problemName({ kind: "empty", name: "e.pdf" })).toBe("e.pdf");
    expect(problemName({ kind: "tooMany", max: 10 })).toBeNull();
  });
});

describe("one Idempotency-Key per attempt", () => {
  const files = [{ name: "a.pdf", size: 5, lastModified: 1 }];
  const sig = uploadSignature("v1", files);
  let n = 0;
  const make = () => `key-${++n}`;

  it("keeps the key for the same files and the same venue", () => {
    const first = attemptFor(null, sig, make);
    expect(attemptFor(first, sig, make)).toBe(first);
  });

  it("makes a new key for other files or another venue", () => {
    const first = attemptFor(null, sig, make);
    const other = attemptFor(first, uploadSignature("v1", [...files, { name: "b.pdf", size: 6, lastModified: 2 }]), make);
    expect(other.key).not.toBe(first.key);
    expect(attemptFor(first, uploadSignature("v2", files), make).key).not.toBe(first.key);
  });

  it("is kept after a lost answer and ended by a clear one", () => {
    expect(keepKeyAfter(0)).toBe(true);
    expect(keepKeyAfter(502)).toBe(true);
    expect(keepKeyAfter(413)).toBe(false);
    expect(keepKeyAfter(415)).toBe(false);
    expect(keepKeyAfter(200)).toBe(false);
  });
});

describe("addCandidates", () => {
  const pdf = (name: string, size: number) => ({ name, size, head: text("%PDF-1.7"), ref: name });

  it("adds the good files and names each bad one", () => {
    const out = addCandidates([], [pdf("a.pdf", 5), { name: "b.doc", size: 5, head: text("hello"), ref: "b.doc" }, pdf("c.pdf", 21 * MB)]);
    expect(out.entries.map((e) => e.ref)).toEqual(["a.pdf"]);
    expect(out.problems).toEqual([
      { kind: "unsupported", name: "b.doc" },
      { kind: "tooLarge", name: "c.pdf", limitMb: 20 },
    ]);
  });

  it("names a problem of the whole list once", () => {
    const current = Array.from({ length: 9 }, (_, i) => ({ ref: `f${i}`, name: `f${i}.pdf`, size: 1, kind: "pdf" as const }));
    const out = addCandidates(current, [pdf("x.pdf", 1), pdf("y.pdf", 1), pdf("z.pdf", 1)]);
    expect(out.entries).toHaveLength(10);
    expect(out.problems).toEqual([{ kind: "tooMany", max: 10 }]);
  });

  it("keeps the list under 80 MB together", () => {
    const out = addCandidates([], [pdf("a.pdf", 20 * MB), pdf("b.pdf", 20 * MB), pdf("c.pdf", 20 * MB), pdf("d.pdf", 20 * MB), pdf("e.pdf", 20 * MB)]);
    expect(out.entries).toHaveLength(4);
    expect(out.problems).toEqual([{ kind: "totalTooLarge", limitMb: 80 }]);
  });
});
