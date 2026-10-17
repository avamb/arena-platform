/**
 * Event posters (task 7-1): which picture is accepted, how a chosen file is
 * checked and previewed, and who may upload.
 *
 * Pure of React. The browser pieces (decoding, object URLs) are passed in, so
 * every rule is tested with real synthetic JPEG/PNG bytes and without a DOM.
 *
 * The server does NOT look into the bytes: `POST /v1/media` stores whatever
 * `content_type` the client names, and a WebP is not verified anywhere. So the
 * web checks the picture itself and sends only what it has proven to be a
 * JPEG or a PNG. SVG, PDF and any other file are refused here.
 */

/** The largest poster the web sends: 5 MiB, like the poster reader (the server's own cap is 32 MiB). */
export const POSTER_MAX_BYTES = 5 * 1024 * 1024;
/** No side longer than this: a picture of 100 megapixels would freeze a phone while decoding. */
export const POSTER_MAX_SIDE = 10_000;
/** Limit decoded memory as well as file size: 25 MP is about 100 MB of RGBA pixels. */
export const POSTER_MAX_PIXELS = 25_000_000;
export const POSTER_TYPES = ["image/jpeg", "image/png"] as const;
export type PosterType = (typeof POSTER_TYPES)[number];
/** Value of the file input's `accept`. */
export const POSTER_ACCEPT = ".jpg,.jpeg,.png,image/jpeg,image/png";

export type PosterRejection = "empty" | "tooLarge" | "notImage" | "corrupt" | "tooBig";

export interface PosterInfo {
  type: PosterType;
  width: number;
  height: number;
}

function startsWith(bytes: Uint8Array, sig: readonly number[]): boolean {
  return sig.every((b, i) => bytes[i] === b);
}

/** The picture type by its first bytes (never by the name or the declared type), or null. */
export function sniffPosterType(head: Uint8Array): PosterType | null {
  if (startsWith(head, [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])) return "image/png";
  if (startsWith(head, [0xff, 0xd8, 0xff])) return "image/jpeg";
  return null;
}

function u32(b: Uint8Array, at: number): number {
  return ((b[at] << 24) | (b[at + 1] << 16) | (b[at + 2] << 8) | b[at + 3]) >>> 0;
}

/**
 * The size and the end of a picture, read from its structure: the PNG header
 * chunk and closing chunk, the JPEG frame header and end marker. A cut-off
 * upload fails here, before any decoder is asked.
 */
export function readPosterStructure(bytes: Uint8Array, type: PosterType): { width: number; height: number } | null {
  if (type === "image/png") {
    if (bytes.length < 33 || u32(bytes, 8) !== 13) return null;
    if (String.fromCharCode(bytes[12], bytes[13], bytes[14], bytes[15]) !== "IHDR") return null;
    const tail = bytes.subarray(bytes.length - 12);
    if (String.fromCharCode(tail[4], tail[5], tail[6], tail[7]) !== "IEND") return null;
    return { width: u32(bytes, 16), height: u32(bytes, 20) };
  }
  if (bytes.length < 4 || bytes[bytes.length - 2] !== 0xff || bytes[bytes.length - 1] !== 0xd9) return null;
  let i = 2;
  while (i + 9 < bytes.length) {
    if (bytes[i] !== 0xff) return null;
    const marker = bytes[i + 1];
    if (marker === 0xff) {
      i += 1;
      continue;
    }
    const len = (bytes[i + 2] << 8) | bytes[i + 3];
    // SOF0..SOF15 except DHT (C4), JPG (C8) and DAC (CC) carry the size.
    if (marker >= 0xc0 && marker <= 0xcf && marker !== 0xc4 && marker !== 0xc8 && marker !== 0xcc) {
      return { height: (bytes[i + 5] << 8) | bytes[i + 6], width: (bytes[i + 7] << 8) | bytes[i + 8] };
    }
    if (len < 2) return null;
    i += 2 + len;
  }
  return null;
}

export interface CheckDeps {
  /** Reads the whole file. */
  read: (file: Blob) => Promise<Uint8Array>;
  /** Decodes the picture for real (browser); rejects when it cannot be drawn. */
  decode?: (file: Blob) => Promise<{ width: number; height: number }>;
}

export const browserCheckDeps: CheckDeps = {
  read: async (file) => new Uint8Array(await file.arrayBuffer()),
  decode: async (file) => {
    if (typeof createImageBitmap !== "function") throw new Error("no decoder");
    const bmp = await createImageBitmap(file);
    const size = { width: bmp.width, height: bmp.height };
    bmp.close();
    return size;
  },
};

export type PosterCheck = { ok: true; info: PosterInfo } | { ok: false; reason: PosterRejection };

/** Is this file a poster the web may send? Size, then the bytes, then the structure, then a real decode. */
export async function checkPosterFile(file: Blob, deps: CheckDeps = browserCheckDeps): Promise<PosterCheck> {
  if (file.size === 0) return { ok: false, reason: "empty" };
  if (file.size > POSTER_MAX_BYTES) return { ok: false, reason: "tooLarge" };
  let bytes: Uint8Array;
  try {
    bytes = await deps.read(file);
  } catch {
    return { ok: false, reason: "corrupt" };
  }
  if (bytes.length === 0) return { ok: false, reason: "empty" };
  const type = sniffPosterType(bytes);
  if (type === null) return { ok: false, reason: "notImage" };
  const shape = readPosterStructure(bytes, type);
  if (shape === null || shape.width < 1 || shape.height < 1) return { ok: false, reason: "corrupt" };
  if (shape.width > POSTER_MAX_SIDE || shape.height > POSTER_MAX_SIDE || shape.width * shape.height > POSTER_MAX_PIXELS) {
    return { ok: false, reason: "tooBig" };
  }
  if (deps.decode !== undefined) {
    try {
      const real = await deps.decode(file);
      if (real.width < 1 || real.height < 1) return { ok: false, reason: "corrupt" };
      if (real.width > POSTER_MAX_SIDE || real.height > POSTER_MAX_SIDE || real.width * real.height > POSTER_MAX_PIXELS) {
        return { ok: false, reason: "tooBig" };
      }
    } catch {
      return { ok: false, reason: "corrupt" };
    }
  }
  return { ok: true, info: { type, width: shape.width, height: shape.height } };
}

// ─── the chosen file and its preview ─────────────────────────────────────────

export type PickerState =
  | { phase: "empty" }
  | { phase: "checking"; file: File }
  | { phase: "ready"; file: File; previewUrl: string; info: PosterInfo }
  | { phase: "rejected"; file: File; reason: PosterRejection };

export interface PickerDeps {
  check: (file: Blob) => Promise<PosterCheck>;
  createUrl: (file: Blob) => string;
  revokeUrl: (url: string) => void;
}

export const browserPickerDeps: PickerDeps = {
  check: (file) => checkPosterFile(file),
  createUrl: (file) => URL.createObjectURL(file),
  revokeUrl: (url) => URL.revokeObjectURL(url),
};

export interface Picker {
  state(): PickerState;
  subscribe(listener: (state: PickerState) => void): () => void;
  /** A newer choice always wins: the answer of an older check is dropped and never makes a preview. */
  select(file: File): Promise<void>;
  /** Drops the choice and revokes its preview. */
  clear(): void;
  /** The screen is gone (or another event/organization): revokes the preview and ignores every pending answer. */
  dispose(): void;
}

/**
 * ONE chosen file with at most ONE live object URL. The URL is created only
 * for a file that passed the check and is still the current choice, and it is
 * revoked when the choice is replaced, cleared or disposed.
 */
export function createPicker(deps: PickerDeps = browserPickerDeps): Picker {
  let current: PickerState = { phase: "empty" };
  let seq = 0;
  let disposed = false;
  const listeners = new Set<(s: PickerState) => void>();
  const set = (next: PickerState) => {
    if (current.phase === "ready" && (next.phase !== "ready" || next.previewUrl !== current.previewUrl)) {
      deps.revokeUrl(current.previewUrl);
    }
    current = next;
    for (const l of [...listeners]) l(next);
  };
  return {
    state: () => current,
    subscribe(l) {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    async select(file) {
      if (disposed) return;
      seq += 1;
      const mine = seq;
      set({ phase: "checking", file });
      let result: PosterCheck;
      try {
        result = await deps.check(file);
      } catch {
        result = { ok: false, reason: "corrupt" };
      }
      if (disposed || mine !== seq) return;
      set(result.ok ? { phase: "ready", file, previewUrl: deps.createUrl(file), info: result.info } : { phase: "rejected", file, reason: result.reason });
    },
    clear() {
      seq += 1;
      set({ phase: "empty" });
    },
    dispose() {
      disposed = true;
      seq += 1;
      if (current.phase === "ready") deps.revokeUrl(current.previewUrl);
      current = { phase: "empty" };
      listeners.clear();
    },
  };
}

// ─── who may do what ─────────────────────────────────────────────────────────

export interface PosterCapabilities {
  /** May see the picture (`media.read`). */
  canView: boolean;
  /** May upload and replace (`media.write` AND `event.update`: the upload and the PATCH are two permissions). */
  canChange: boolean;
}

export function posterCapabilities(can: (permission: string) => boolean): PosterCapabilities {
  return { canView: can("media.read"), canChange: can("media.write") && can("event.update") };
}

/**
 * Poster first: may a draft be created as far as the poster step is concerned? The person must
 * have uploaded a poster ("poster") or chosen "Continue without a poster" ("skipped"). Someone who
 * may not upload posters cannot do either, so the step never blocks them (they create without one).
 */
export function posterStepSettled(can: PosterCapabilities, decision: "" | "poster" | "skipped"): boolean {
  return !can.canChange || decision !== "";
}

/**
 * The signed address of a picture is used only when it points at the API the
 * page already trusts (`connect-src`), never at an address the answer made up.
 * A storage with its own origin (S3) is therefore refused, not guessed at: see README.
 */
export function trustedMediaUrl(signed: string, apiBaseUrl: string): URL | null {
  try {
    const api = new URL(apiBaseUrl);
    // A host-relative address (an API without a public URL) means "on the API itself".
    const url = new URL(signed, api);
    if (url.origin !== api.origin) return null;
    if (!url.pathname.startsWith("/v1/media-files/")) return null;
    return url;
  } catch {
    return null;
  }
}

/** Only these two types are drawn; anything else the server might name is not an image to us. */
export function isPosterType(value: unknown): value is PosterType {
  return typeof value === "string" && (POSTER_TYPES as readonly string[]).includes(value);
}
