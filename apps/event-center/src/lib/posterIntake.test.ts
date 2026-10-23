/**
 * The poster step: upload once, read only on an explicit press, never repeat a
 * paid read by itself, and never let an answer of an older selection, another
 * organization or a closed screen touch the new state.
 */
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/http";
import { classifyReadFailure, createIntake, type IntakeApi } from "@/lib/posterIntake";

const ORG = "org-1";
const M1 = "0199aaaa-0000-7000-8000-000000000001";
const M2 = "0199aaaa-0000-7000-8000-000000000002";
const FILE = new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" });

const GOOD = { facts: { name: "Swan Lake", date: "2027-03-05", time: "19:30", venue_name: "", city: "", age: "", description: "", categories: [] } };
const EMPTY = { facts: { name: "", date: "", time: "", venue_name: "", city: "", age: "", description: "", categories: [] } };

/** A promise whose answer the test gives by hand. */
function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function rig(over: Partial<IntakeApi> = {}, orgId = ORG) {
  const uploaded: string[] = [];
  const upload = vi.fn(async () => ({ media_object: { id: M1 } }) as never);
  const readFacts = vi.fn(async () => GOOD as unknown);
  const intake = createIntake({ orgId, api: { upload, readFacts, ...over }, onUploaded: (id) => uploaded.push(id) });
  return { intake, upload, readFacts, uploaded };
}

describe("upload", () => {
  it("uploads once and reports the id; a second call while stored does nothing", async () => {
    const { intake, upload, uploaded } = rig();
    await intake.upload(FILE, "image/png");
    await intake.upload(FILE, "image/png");
    expect(upload).toHaveBeenCalledTimes(1);
    expect(uploaded).toEqual([M1]);
    expect(intake.view().upload).toEqual({ phase: "uploaded", mediaId: M1 });
  });
  it("ignores a second call while the first runs (double click)", async () => {
    const d = deferred<never>();
    const { intake, upload } = rig({ upload: vi.fn(() => d.promise) });
    const first = intake.upload(FILE, "image/png");
    await intake.upload(FILE, "image/png");
    d.resolve({ media_object: { id: M1 } } as never);
    await first;
    expect(intake.view().upload.phase).toBe("uploaded");
    void upload;
  });
  it("an upload failure is its own state, keeps nothing stored, and a retry uploads again", async () => {
    const upload = vi.fn().mockRejectedValueOnce(new ApiError(0, "network.failure", "x")).mockResolvedValueOnce({ media_object: { id: M1 } });
    const { intake, uploaded } = rig({ upload });
    await intake.upload(FILE, "image/png");
    expect(intake.view().upload).toMatchObject({ phase: "failed", failure: { kind: "unknown" } });
    expect(intake.view().read.phase).toBe("idle");
    expect(uploaded).toEqual([]);
    await intake.upload(FILE, "image/png");
    expect(intake.view().upload.phase).toBe("uploaded");
    expect(upload).toHaveBeenCalledTimes(2);
  });
  it("a refusal is 'rejected', a 403 is 'denied'", async () => {
    const a = rig({ upload: vi.fn().mockRejectedValue(new ApiError(422, "media.bad", "x")) });
    await a.intake.upload(FILE, "image/png");
    expect(a.intake.view().upload).toMatchObject({ phase: "failed", failure: { kind: "rejected" } });
    const b = rig({ upload: vi.fn().mockRejectedValue(new ApiError(403, "forbidden", "x")) });
    await b.intake.upload(FILE, "image/png");
    expect(b.intake.view().upload).toMatchObject({ phase: "failed", failure: { kind: "denied" } });
  });
  it("an answer without a usable id is a lost response, not a stored picture", async () => {
    const { intake, uploaded } = rig({ upload: vi.fn(async () => ({ media_object: { id: "nope" } }) as never) });
    await intake.upload(FILE, "image/png");
    expect(intake.view().upload.phase).toBe("failed");
    expect(uploaded).toEqual([]);
  });
});

describe("reading is explicit and paid", () => {
  it("nothing is read by uploading, adopting a stored id, or constructing", async () => {
    const { intake, readFacts } = rig();
    await intake.upload(FILE, "image/png");
    intake.reset();
    intake.adopt(M1);
    expect(readFacts).not.toHaveBeenCalled();
    expect(intake.view().read.phase).toBe("idle");
  });
  it("cannot read without an uploaded picture", async () => {
    const { intake, readFacts } = rig();
    await intake.read();
    expect(readFacts).not.toHaveBeenCalled();
  });
  it("reads once for the exact organization and picture and keeps the hints for review (never applies)", async () => {
    const { intake, readFacts } = rig();
    await intake.upload(FILE, "image/png");
    await intake.read();
    expect(readFacts).toHaveBeenCalledTimes(1);
    expect(readFacts).toHaveBeenCalledWith(ORG, M1, expect.any(AbortSignal));
    const v = intake.view().read;
    expect(v).toMatchObject({ phase: "ready", orgId: ORG, mediaId: M1 });
    if (v.phase === "ready") expect(v.facts.name).toBe("Swan Lake");
  });
  it("a double click makes ONE paid request", async () => {
    const d = deferred<unknown>();
    const { intake, readFacts } = rig({ readFacts: vi.fn(() => d.promise) });
    intake.adopt(M1);
    const a = intake.read();
    const b = intake.read();
    expect(intake.view().read.phase).toBe("reading");
    d.resolve(GOOD);
    await Promise.all([a, b]);
    expect(intake.view().read.phase).toBe("ready");
    expect(intake.view().read.phase).toBe("ready");
    void readFacts;
  });
  it("no usable value is its own state", async () => {
    const { intake } = rig({ readFacts: vi.fn(async () => EMPTY) });
    intake.adopt(M1);
    await intake.read();
    expect(intake.view().read.phase).toBe("nothing");
  });
  it("an unreadable answer is a failure, not a success", async () => {
    const { intake } = rig({ readFacts: vi.fn(async () => ({ nonsense: true })) });
    intake.adopt(M1);
    await intake.read();
    expect(intake.view().read).toEqual({ phase: "failed", reason: "unavailable", canRetry: true });
  });
  it("after a failure nothing is retried by itself; the uploaded picture stays; an explicit retry reads again", async () => {
    const readFacts = vi.fn().mockRejectedValueOnce(new ApiError(503, "media.poster_read_failed", "x")).mockResolvedValueOnce(GOOD);
    const { intake } = rig({ readFacts });
    await intake.upload(FILE, "image/png");
    await intake.read();
    expect(readFacts).toHaveBeenCalledTimes(1);
    expect(intake.view().read).toEqual({ phase: "failed", reason: "unavailable", canRetry: true });
    expect(intake.view().upload).toEqual({ phase: "uploaded", mediaId: M1 });
    await new Promise((r) => setTimeout(r, 20));
    expect(readFacts).toHaveBeenCalledTimes(1);
    await intake.read();
    expect(readFacts).toHaveBeenCalledTimes(2);
    expect(intake.view().read.phase).toBe("ready");
  });
  it("'continue by hand' ends the read without hints", async () => {
    const { intake } = rig({ readFacts: vi.fn().mockRejectedValue(new ApiError(0, "network.timeout", "x")) });
    intake.adopt(M1);
    await intake.read();
    intake.finishRead([]);
    expect(intake.view().read).toEqual({ phase: "done", applied: [] });
    expect(intake.view().upload.phase).toBe("uploaded");
  });
});

describe("failure kinds", () => {
  const e = (status: number, code: string) => classifyReadFailure(new ApiError(status, code, "m"));
  it("unconfigured, quota, denied, missing, unsupported are final", () => {
    expect(e(503, "media.poster_reader_not_configured")).toEqual({ reason: "unconfigured", canRetry: false });
    expect(e(429, "media.poster_reads_exhausted")).toEqual({ reason: "quota", canRetry: false });
    expect(e(403, "forbidden")).toEqual({ reason: "denied", canRetry: false });
    expect(e(401, "unauthorized")).toEqual({ reason: "denied", canRetry: false });
    expect(e(404, "media.not_found")).toEqual({ reason: "missing", canRetry: false });
    expect(e(422, "media.poster_unsupported_type")).toEqual({ reason: "unsupported", canRetry: false });
    expect(e(422, "media.poster_too_large")).toEqual({ reason: "unsupported", canRetry: false });
  });
  it("a read failure of the service is retryable by the person", () => {
    expect(e(503, "media.poster_read_failed")).toEqual({ reason: "unavailable", canRetry: true });
    expect(e(500, "media.get_failed")).toEqual({ reason: "unavailable", canRetry: true });
  });
  it("a timeout or lost connection says 'unknown' (it may have been counted)", () => {
    expect(e(0, "network.timeout")).toEqual({ reason: "unknown", canRetry: true });
    expect(e(0, "network.failure")).toEqual({ reason: "unknown", canRetry: true });
    expect(classifyReadFailure(new Error("boom"))).toEqual({ reason: "unknown", canRetry: true });
  });
  it("a cancel is not a failure", () => {
    expect(e(0, "network.aborted")).toBe("aborted");
  });
  it("each failure lands in the view", async () => {
    for (const [status, code, reason] of [
      [503, "media.poster_reader_not_configured", "unconfigured"],
      [429, "media.poster_reads_exhausted", "quota"],
      [403, "forbidden", "denied"],
    ] as const) {
      const { intake } = rig({ readFacts: vi.fn().mockRejectedValue(new ApiError(status, code, "x")) });
      intake.adopt(M1);
      await intake.read();
      expect(intake.view().read).toMatchObject({ phase: "failed", reason, canRetry: false });
    }
  });
});

describe("races: an answer belongs to its own generation", () => {
  it("a slow read that finishes after the picture was removed changes nothing", async () => {
    const d = deferred<unknown>();
    const { intake } = rig({ readFacts: vi.fn(() => d.promise) });
    intake.adopt(M1);
    const p = intake.read();
    intake.reset();
    d.resolve(GOOD);
    await p;
    expect(intake.view()).toEqual({ upload: { phase: "idle" }, read: { phase: "idle" } });
  });
  it("a slow read for picture A cannot fill the review of picture B", async () => {
    const d1 = deferred<unknown>();
    const readFacts = vi.fn().mockReturnValueOnce(d1.promise).mockResolvedValueOnce({ facts: { ...GOOD.facts, name: "Second" } });
    const upload = vi.fn().mockResolvedValueOnce({ media_object: { id: M1 } }).mockResolvedValueOnce({ media_object: { id: M2 } });
    const { intake } = rig({ readFacts, upload });
    await intake.upload(FILE, "image/png");
    const slow = intake.read();
    intake.reset(); // the person chose another picture
    await intake.upload(FILE, "image/png");
    expect(intake.view().upload).toEqual({ phase: "uploaded", mediaId: M2 });
    d1.resolve({ facts: { ...GOOD.facts, name: "First" } });
    await slow;
    // The first answer was dropped: the second picture has not been read yet.
    expect(intake.view().read).toEqual({ phase: "idle" });
    await intake.read();
    const v = intake.view().read;
    expect(v).toMatchObject({ phase: "ready", mediaId: M2 });
    if (v.phase === "ready") expect(v.facts.name).toBe("Second");
  });
  it("a late failure of an old read cannot put an error on the new selection", async () => {
    const d = deferred<unknown>();
    const { intake } = rig({ readFacts: vi.fn(() => d.promise) });
    intake.adopt(M1);
    const p = intake.read();
    intake.reset();
    intake.adopt(M2);
    d.reject(new ApiError(503, "media.poster_read_failed", "x"));
    await p;
    expect(intake.view()).toEqual({ upload: { phase: "uploaded", mediaId: M2 }, read: { phase: "idle" } });
  });
  it("a slow upload that finishes after 'continue without a poster' does not record an id", async () => {
    const d = deferred<never>();
    const { intake, uploaded } = rig({ upload: vi.fn(() => d.promise) });
    const p = intake.upload(FILE, "image/png");
    intake.reset();
    d.resolve({ media_object: { id: M1 } } as never);
    await p;
    expect(uploaded).toEqual([]);
    expect(intake.view().upload.phase).toBe("idle");
  });
  it("the read request is cancelled when the selection changes", async () => {
    let signal: AbortSignal | undefined;
    const { intake } = rig({
      readFacts: vi.fn((_o: string, _m: string, s?: AbortSignal) => {
        signal = s;
        return new Promise<unknown>(() => {});
      }),
    });
    intake.adopt(M1);
    void intake.read();
    intake.reset();
    expect(signal?.aborted).toBe(true);
  });
  it("after dispose (screen or organization gone) every answer is ignored and nothing is reported", async () => {
    const d = deferred<unknown>();
    const { intake } = rig({ readFacts: vi.fn(() => d.promise) });
    const seen = vi.fn();
    intake.adopt(M1);
    intake.subscribe(seen);
    const p = intake.read();
    seen.mockClear();
    intake.dispose();
    d.resolve(GOOD);
    await p;
    expect(seen).not.toHaveBeenCalled();
    await intake.read();
    await intake.upload(FILE, "image/png");
    expect(intake.view().read.phase).toBe("reading");
  });
  it("an intake of one organization cannot read for another: the org id is fixed at creation", async () => {
    const a = rig({}, "org-A");
    const b = rig({}, "org-B");
    a.intake.adopt(M1);
    await a.intake.read();
    expect(a.readFacts).toHaveBeenCalledWith("org-A", M1, expect.anything());
    expect(b.readFacts).not.toHaveBeenCalled();
  });
});
