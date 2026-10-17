import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { EventEnvelope, MediaObject } from "@/lib/api/types";
import { attachPoster, classifyFailure, createWithPoster, replacePoster, uploadPoster, type PosterApi } from "@/lib/posterFlow";
import type { SaveOutcome } from "@/lib/wizard/save";

const MEDIA = "0199aaaa-0000-7000-8000-000000000001";
const EVENT = "0199bbbb-0000-7000-8000-000000000002";
const file = new Blob([new Uint8Array([1, 2, 3])]);

const media = (id: string) => ({ media_object: { id } as MediaObject });
const envelope = (id: string, poster: string | null) => ({ event: { id, poster_media_id: poster } }) as unknown as EventEnvelope;
const apiError = (status: number, code = "x") => new ApiError(status, code, "m");

function api(over: Partial<PosterApi> = {}): PosterApi & { upload: ReturnType<typeof vi.fn>; attach: ReturnType<typeof vi.fn> } {
  return {
    upload: vi.fn(async () => media(MEDIA)),
    attach: vi.fn(async (eventId: string, mediaId: string) => envelope(eventId, mediaId)),
    readPosterId: vi.fn(async () => null),
    ...over,
  } as never;
}

describe("classifyFailure", () => {
  it("separates denied, rejected, unknown and cancelled", () => {
    expect(classifyFailure(apiError(403)).kind).toBe("denied");
    expect(classifyFailure(apiError(401)).kind).toBe("denied");
    expect(classifyFailure(apiError(422, "event.invalid_poster_media_id")).kind).toBe("rejected");
    expect(classifyFailure(apiError(413)).kind).toBe("rejected");
    expect(classifyFailure(apiError(500)).kind).toBe("unknown");
    expect(classifyFailure(apiError(429)).kind).toBe("unknown");
    expect(classifyFailure(apiError(0, "network.timeout")).kind).toBe("unknown");
    expect(classifyFailure(apiError(0, "network.aborted")).kind).toBe("aborted");
    expect(classifyFailure(new TypeError("boom")).kind).toBe("unknown");
  });
});

describe("uploadPoster", () => {
  it("reads the id from the NESTED media_object", async () => {
    expect(await uploadPoster(api(), file, "image/png")).toEqual({ kind: "uploaded", mediaId: MEDIA });
  });
  it("an answer without a usable id is a lost response, not a success", async () => {
    const flat = api({ upload: vi.fn(async () => ({ id: MEDIA }) as never) });
    expect(await uploadPoster(flat, file, "image/png")).toMatchObject({ kind: "failed", failure: { kind: "unknown" } });
    const bad = api({ upload: vi.fn(async () => media("not-a-uuid")) });
    expect(await uploadPoster(bad, file, "image/png")).toMatchObject({ kind: "failed" });
  });
  it("keeps the kind of the failure", async () => {
    const denied = api({ upload: vi.fn(async () => Promise.reject(apiError(403))) });
    expect(await uploadPoster(denied, file, "image/png")).toMatchObject({ kind: "failed", failure: { kind: "denied" } });
  });
});

describe("attachPoster", () => {
  it("is attached only when the answer names this event and this media id", async () => {
    expect(await attachPoster(api(), EVENT, MEDIA)).toEqual({ kind: "attached" });
    const wrongMedia = api({ attach: vi.fn(async (e: string) => envelope(e, "0199cccc-0000-7000-8000-000000000003")) });
    expect(await attachPoster(wrongMedia, EVENT, MEDIA)).toMatchObject({ kind: "failed" });
    const wrongEvent = api({ attach: vi.fn(async () => envelope("other", MEDIA)) });
    expect(await attachPoster(wrongEvent, EVENT, MEDIA)).toMatchObject({ kind: "failed" });
  });

  it("a lost response is checked by reading the event back", async () => {
    const lostButApplied = api({
      attach: vi.fn(async () => Promise.reject(apiError(0, "network.failure"))),
      readPosterId: vi.fn(async () => MEDIA),
    });
    expect(await attachPoster(lostButApplied, EVENT, MEDIA)).toEqual({ kind: "attached" });
    const lostNotApplied = api({
      attach: vi.fn(async () => Promise.reject(apiError(503))),
      readPosterId: vi.fn(async () => null),
    });
    expect(await attachPoster(lostNotApplied, EVENT, MEDIA)).toMatchObject({ kind: "failed", failure: { kind: "unknown" } });
    const readFails = api({
      attach: vi.fn(async () => Promise.reject(apiError(0, "network.timeout"))),
      readPosterId: vi.fn(async () => Promise.reject(new Error("offline"))),
    });
    expect(await attachPoster(readFails, EVENT, MEDIA)).toMatchObject({ kind: "failed", failure: { kind: "unknown" } });
  });

  it("a refusal is final and is not verified by a read", async () => {
    const refused = api({ attach: vi.fn(async () => Promise.reject(apiError(422, "event.invalid_poster_media_id"))) });
    expect(await attachPoster(refused, EVENT, MEDIA)).toMatchObject({ kind: "failed", failure: { kind: "rejected" } });
    expect(refused.readPosterId).not.toHaveBeenCalled();
  });
});

describe("replacePoster", () => {
  it("uploads, then attaches, and says done", async () => {
    const a = api();
    expect(await replacePoster(a, { eventId: EVENT, file, type: "image/png" })).toEqual({ kind: "done", mediaId: MEDIA });
    expect(a.upload).toHaveBeenCalledTimes(1);
    expect(a.attach).toHaveBeenCalledWith(EVENT, MEDIA, undefined);
  });

  it("a failed upload attaches nothing, so the old poster stays", async () => {
    const a = api({ upload: vi.fn(async () => Promise.reject(apiError(500))) });
    expect(await replacePoster(a, { eventId: EVENT, file, type: "image/png" })).toMatchObject({ kind: "upload_failed" });
    expect(a.attach).not.toHaveBeenCalled();
  });

  it("a failed attach keeps the media id; the retry never uploads again", async () => {
    let first = true;
    const a = api({
      attach: vi.fn(async (e: string, m: string) => {
        if (first) {
          first = false;
          throw apiError(0, "network.failure");
        }
        return envelope(e, m);
      }),
    });
    const r1 = await replacePoster(a, { eventId: EVENT, file, type: "image/png" });
    expect(r1).toMatchObject({ kind: "attach_failed", mediaId: MEDIA });
    const r2 = await replacePoster(a, { eventId: EVENT, file, type: "image/png", uploadedMediaId: MEDIA });
    expect(r2).toEqual({ kind: "done", mediaId: MEDIA });
    expect(a.upload).toHaveBeenCalledTimes(1);
  });
});

describe("createWithPoster (upload -> draft -> attach)", () => {
  const saved: SaveOutcome = { kind: "saved", eventId: EVENT, sessionId: "s1", created: true };
  const args = (over: Record<string, unknown> = {}) => {
    const progress: Array<{ mediaId?: string; eventId?: string }> = [];
    return {
      progress,
      a: {
        file,
        type: "image/png" as const,
        mediaId: "",
        eventId: "",
        save: vi.fn(async () => saved),
        onProgress: (p: { mediaId?: string; eventId?: string }) => progress.push(p),
        ...over,
      },
    };
  };

  it("happy path: one upload, one create, one attach, in that order", async () => {
    const order: string[] = [];
    const a = api({
      upload: vi.fn(async () => (order.push("upload"), media(MEDIA))),
      attach: vi.fn(async (e: string, m: string) => (order.push("attach"), envelope(e, m))),
    });
    const { a: x, progress } = args({
      save: vi.fn(async () => (order.push("create"), saved)),
    });
    expect(await createWithPoster(a, x)).toEqual({ kind: "done", eventId: EVENT, poster: "attached" });
    expect(order).toEqual(["upload", "create", "attach"]);
    expect(progress).toEqual([{ mediaId: MEDIA }, { eventId: EVENT }]);
  });

  it("no poster: only the create call, no media call at all", async () => {
    const a = api();
    const { a: x, progress } = args({ file: null, type: null });
    expect(await createWithPoster(a, x)).toEqual({ kind: "done", eventId: EVENT, poster: "none" });
    expect(a.upload).not.toHaveBeenCalled();
    expect(a.attach).not.toHaveBeenCalled();
    expect(progress).toEqual([]);
  });

  it("a failed upload creates NO event", async () => {
    const a = api({ upload: vi.fn(async () => Promise.reject(apiError(0, "network.failure"))) });
    const { a: x } = args();
    expect(await createWithPoster(a, x)).toMatchObject({ kind: "upload_failed", failure: { kind: "unknown" } });
    expect(x.save).not.toHaveBeenCalled();
  });

  it("a refused create keeps the media id for the retry and attaches nothing", async () => {
    const a = api();
    const { a: x, progress } = args({ save: vi.fn(async () => ({ kind: "rejected", status: 422, code: "c", message: "m" }) as SaveOutcome) });
    expect(await createWithPoster(a, x)).toMatchObject({ kind: "save_failed", mediaId: MEDIA });
    expect(a.attach).not.toHaveBeenCalled();
    expect(progress).toEqual([{ mediaId: MEDIA }]);
  });

  it("attach failure after a real creation keeps both ids and never claims the poster", async () => {
    const a = api({ attach: vi.fn(async () => Promise.reject(apiError(0, "network.failure"))) });
    const { a: x, progress } = args();
    const r = await createWithPoster(a, x);
    expect(r).toMatchObject({ kind: "attach_failed", eventId: EVENT, mediaId: MEDIA });
    expect(progress).toEqual([{ mediaId: MEDIA }, { eventId: EVENT }]);
  });

  it("the retry after that failure attaches only: no upload, no second create", async () => {
    const a = api();
    const { a: x } = args({ file: null, type: null, mediaId: MEDIA, eventId: EVENT });
    expect(await createWithPoster(a, x)).toEqual({ kind: "done", eventId: EVENT, poster: "attached" });
    expect(a.upload).not.toHaveBeenCalled();
    expect(x.save).not.toHaveBeenCalled();
    expect(a.attach).toHaveBeenCalledTimes(1);
  });

  it("an uploaded picture of a stored draft is reused after a reload (no file in memory)", async () => {
    const a = api();
    const { a: x } = args({ file: null, type: null, mediaId: MEDIA });
    expect(await createWithPoster(a, x)).toEqual({ kind: "done", eventId: EVENT, poster: "attached" });
    expect(a.upload).not.toHaveBeenCalled();
  });
});
