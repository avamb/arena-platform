import { deflateSync } from "node:zlib";
import { describe, expect, it, vi } from "vitest";
import {
  POSTER_MAX_BYTES,
  POSTER_MAX_SIDE,
  checkPosterFile,
  createPicker,
  posterCapabilities,
  readPosterStructure,
  sniffPosterType,
  trustedMediaUrl,
  type CheckDeps,
  type PosterCheck,
} from "@/lib/posters";

// ─── real synthetic images ───────────────────────────────────────────────────

function crc32(buf: Uint8Array): number {
  let c = ~0;
  for (const b of buf) {
    c ^= b;
    for (let k = 0; k < 8; k++) c = c & 1 ? (c >>> 1) ^ 0xedb88320 : c >>> 1;
  }
  return ~c >>> 0;
}

function chunk(type: string, data: Uint8Array): Uint8Array {
  const out = new Uint8Array(12 + data.length);
  const dv = new DataView(out.buffer);
  dv.setUint32(0, data.length);
  out.set(new TextEncoder().encode(type), 4);
  out.set(data, 8);
  dv.setUint32(8 + data.length, crc32(out.subarray(4, 8 + data.length)));
  return out;
}

/** A complete, decodable 8-bit RGB PNG of one colour (zlib-compressed scanlines, real CRCs). */
export function makePng(width: number, height: number, rgb: [number, number, number] = [200, 30, 60]): Uint8Array {
  const ihdr = new Uint8Array(13);
  const dv = new DataView(ihdr.buffer);
  dv.setUint32(0, width);
  dv.setUint32(4, height);
  ihdr[8] = 8;
  ihdr[9] = 2;
  const row = new Uint8Array(1 + width * 3);
  for (let x = 0; x < width; x++) row.set(rgb, 1 + x * 3);
  const raw = new Uint8Array(row.length * height);
  for (let y = 0; y < height; y++) raw.set(row, y * row.length);
  const parts = [
    new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", new Uint8Array(deflateSync(raw))),
    chunk("IEND", new Uint8Array(0)),
  ];
  return concat(parts);
}

function concat(parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}

/**
 * A JPEG with the real segment structure (SOI, JFIF, SOF0 with the size, SOS,
 * scan bytes, EOI). Its pixels are not meant for a decoder: the browser decode
 * of a real JPEG is proven in the live Playwright test.
 */
export function makeJpegShape(width: number, height: number): Uint8Array {
  return new Uint8Array([
    0xff, 0xd8,
    0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
    0xff, 0xc0, 0x00, 0x0b, 0x08, height >> 8, height & 255, width >> 8, width & 255, 0x01, 0x01, 0x11, 0x00,
    0xff, 0xda, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3f, 0x00,
    0x12, 0x34, 0x56,
    0xff, 0xd9,
  ]);
}

const blob = (bytes: Uint8Array, type = "") => new Blob([bytes as BlobPart], { type });
const bytesDeps: CheckDeps = { read: async (f) => new Uint8Array(await f.arrayBuffer()) };

describe("sniffPosterType", () => {
  it("names a PNG and a JPEG by their first bytes, never by a name", () => {
    expect(sniffPosterType(makePng(2, 2))).toBe("image/png");
    expect(sniffPosterType(makeJpegShape(2, 2))).toBe("image/jpeg");
  });
  it("refuses SVG, PDF, WebP, GIF and plain text", () => {
    const enc = (s: string) => new TextEncoder().encode(s);
    expect(sniffPosterType(enc('<svg xmlns="http://www.w3.org/2000/svg"/>'))).toBeNull();
    expect(sniffPosterType(enc("%PDF-1.7\n"))).toBeNull();
    expect(sniffPosterType(enc("RIFF\0\0\0\0WEBPVP8 "))).toBeNull();
    expect(sniffPosterType(enc("GIF89a"))).toBeNull();
    expect(sniffPosterType(enc("hello"))).toBeNull();
  });
});

describe("readPosterStructure", () => {
  it("reads the size of a PNG and of a JPEG", () => {
    expect(readPosterStructure(makePng(30, 40), "image/png")).toEqual({ width: 30, height: 40 });
    expect(readPosterStructure(makeJpegShape(300, 400), "image/jpeg")).toEqual({ width: 300, height: 400 });
  });
  it("refuses a PNG without its closing chunk and a JPEG without its end marker", () => {
    const png = makePng(8, 8);
    expect(readPosterStructure(png.subarray(0, png.length - 12), "image/png")).toBeNull();
    const jpg = makeJpegShape(8, 8);
    expect(readPosterStructure(jpg.subarray(0, jpg.length - 2), "image/jpeg")).toBeNull();
  });
});

describe("checkPosterFile", () => {
  it("accepts a normal PNG and a JPEG and reports their own proportions", async () => {
    const png = await checkPosterFile(blob(makePng(60, 90), "image/png"), bytesDeps);
    expect(png).toEqual({ ok: true, info: { type: "image/png", width: 60, height: 90 } });
    const jpg = await checkPosterFile(blob(makeJpegShape(120, 80), "image/jpeg"), bytesDeps);
    expect(jpg).toEqual({ ok: true, info: { type: "image/jpeg", width: 120, height: 80 } });
  });

  it("refuses an empty file", async () => {
    expect(await checkPosterFile(blob(new Uint8Array(0)), bytesDeps)).toEqual({ ok: false, reason: "empty" });
  });

  it("refuses a file over the limit before reading it", async () => {
    const read = vi.fn();
    const big = { size: POSTER_MAX_BYTES + 1 } as Blob;
    expect(await checkPosterFile(big, { read })).toEqual({ ok: false, reason: "tooLarge" });
    expect(read).not.toHaveBeenCalled();
  });

  it("refuses SVG and PDF even when they claim to be a picture", async () => {
    const svg = blob(new TextEncoder().encode('<svg xmlns="http://www.w3.org/2000/svg" width="9" height="9"/>'), "image/png");
    expect(await checkPosterFile(svg, bytesDeps)).toEqual({ ok: false, reason: "notImage" });
    const pdf = blob(new TextEncoder().encode("%PDF-1.4 poster"), "image/jpeg");
    expect(await checkPosterFile(pdf, bytesDeps)).toEqual({ ok: false, reason: "notImage" });
  });

  it("refuses a cut-off or damaged picture", async () => {
    const png = makePng(16, 16);
    expect(await checkPosterFile(blob(png.subarray(0, png.length - 20), "image/png"), bytesDeps)).toEqual({ ok: false, reason: "corrupt" });
    const justSignature = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0]);
    expect(await checkPosterFile(blob(justSignature), bytesDeps)).toEqual({ ok: false, reason: "corrupt" });
  });

  it("refuses a picture the browser cannot decode, and one with absurd sides", async () => {
    const png = blob(makePng(10, 10), "image/png");
    const decode = vi.fn().mockRejectedValue(new Error("bad"));
    expect(await checkPosterFile(png, { ...bytesDeps, decode })).toEqual({ ok: false, reason: "corrupt" });
    const wide = blob(makeJpegShape(POSTER_MAX_SIDE + 1, 10), "image/jpeg");
    expect(await checkPosterFile(wide, bytesDeps)).toEqual({ ok: false, reason: "tooBig" });
  });

  it("answers corrupt when the file cannot be read at all", async () => {
    const png = blob(makePng(10, 10));
    expect(await checkPosterFile(png, { read: async () => Promise.reject(new Error("io")) })).toEqual({ ok: false, reason: "corrupt" });
  });

  it("rejects a compact image declaring 64 megapixels before allocating a bitmap", async () => {
    const decode = vi.fn(async () => ({ width: 8000, height: 8000 }));
    const compact = blob(makeJpegShape(8000, 8000), "image/jpeg");
    expect(await checkPosterFile(compact, { ...bytesDeps, decode })).toEqual({ ok: false, reason: "tooBig" });
    expect(decode).not.toHaveBeenCalled();
  });
});

describe("createPicker (preview lifecycle)", () => {
  const file = (name: string) => new File([new Uint8Array([1])], name);
  const ok: PosterCheck = { ok: true, info: { type: "image/png", width: 3, height: 2 } };

  function harness(check: (f: Blob) => Promise<PosterCheck>) {
    let n = 0;
    const live = new Set<string>();
    const picker = createPicker({
      check,
      createUrl: () => {
        const u = `blob:test/${++n}`;
        live.add(u);
        return u;
      },
      revokeUrl: (u) => {
        live.delete(u);
      },
    });
    return { picker, live };
  }

  it("makes ONE preview address for the current file and revokes it on replace, clear and dispose", async () => {
    const { picker, live } = harness(async () => ok);
    await picker.select(file("a"));
    expect(picker.state().phase).toBe("ready");
    expect(live.size).toBe(1);
    await picker.select(file("b"));
    expect(live.size).toBe(1);
    picker.clear();
    expect(live.size).toBe(0);
    await picker.select(file("c"));
    expect(live.size).toBe(1);
    picker.dispose();
    expect(live.size).toBe(0);
  });

  it("never makes an address for a refused file", async () => {
    const { picker, live } = harness(async () => ({ ok: false, reason: "notImage" }));
    await picker.select(file("x"));
    expect(picker.state()).toMatchObject({ phase: "rejected", reason: "notImage" });
    expect(live.size).toBe(0);
  });

  it("ignores the slow answer of an older choice (stale selection)", async () => {
    const pending: Array<(c: PosterCheck) => void> = [];
    const { picker, live } = harness(() => new Promise<PosterCheck>((res) => pending.push(res)));
    const first = picker.select(file("slow"));
    const second = picker.select(file("fast"));
    pending[1](ok);
    await second;
    expect(picker.state()).toMatchObject({ phase: "ready" });
    pending[0]({ ok: true, info: { type: "image/jpeg", width: 9, height: 9 } });
    await first;
    const s = picker.state();
    expect(s.phase === "ready" && s.info.width).toBe(3);
    expect(live.size).toBe(1);
  });

  it("makes nothing after dispose, even when the check answers later", async () => {
    let release: (c: PosterCheck) => void = () => {};
    const { picker, live } = harness(() => new Promise<PosterCheck>((res) => (release = res)));
    const p = picker.select(file("late"));
    picker.dispose();
    release(ok);
    await p;
    expect(live.size).toBe(0);
    expect(picker.state().phase).toBe("empty");
  });

  it("a clear while checking drops the answer", async () => {
    let release: (c: PosterCheck) => void = () => {};
    const { picker, live } = harness(() => new Promise<PosterCheck>((res) => (release = res)));
    const p = picker.select(file("a"));
    picker.clear();
    release(ok);
    await p;
    expect(picker.state().phase).toBe("empty");
    expect(live.size).toBe(0);
  });
});

describe("posterCapabilities", () => {
  const can = (...granted: string[]) => (p: string) => granted.includes(p);
  it("changes need media.write AND event.update", () => {
    expect(posterCapabilities(can("media.read", "media.write", "event.update"))).toEqual({ canView: true, canChange: true });
    expect(posterCapabilities(can("media.read", "media.write"))).toEqual({ canView: true, canChange: false });
    expect(posterCapabilities(can("media.read", "event.update"))).toEqual({ canView: true, canChange: false });
  });
  it("the agent sees but never changes; no media.read means no picture", () => {
    expect(posterCapabilities(can("event.read", "media.read"))).toEqual({ canView: true, canChange: false });
    expect(posterCapabilities(can("media.write", "event.update"))).toEqual({ canView: false, canChange: true });
  });
});

describe("trustedMediaUrl", () => {
  const api = "http://127.0.0.1:18086";
  it("takes only a media-file address on the API origin", () => {
    expect(trustedMediaUrl(`${api}/v1/media-files/abc?expires=1&sig=2`, api)?.pathname).toBe("/v1/media-files/abc");
    expect(trustedMediaUrl("https://evil.example/v1/media-files/abc", api)).toBeNull();
    expect(trustedMediaUrl(`${api}/other/path`, api)).toBeNull();
    expect(trustedMediaUrl("javascript:alert(1)", api)).toBeNull();
    // A host-relative address means "on the API itself"; a protocol-relative one names another host.
    expect(trustedMediaUrl("/v1/media-files/abc?expires=1", api)?.origin).toBe(api);
    expect(trustedMediaUrl("//evil.example/v1/media-files/abc", api)).toBeNull();
  });
});
