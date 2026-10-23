import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { MediaObject } from "@/lib/api/types";
import { DISPLAY_MAX_BYTES, loadPosterBlob, type LoadDeps } from "@/lib/posterImage";

const API = "http://127.0.0.1:18086";
const ID = "0199aaaa-0000-7000-8000-000000000001";

function deps(
  media: Partial<MediaObject> | Error,
  bytes: Response | Error = new Response(new Uint8Array([1, 2, 3]), { status: 200 }),
): LoadDeps & { fetchBytes: ReturnType<typeof vi.fn> } {
  return {
    apiBaseUrl: API,
    getMedia: vi.fn(async () => {
      if (media instanceof Error) throw media;
      return { media_object: media as MediaObject };
    }),
    fetchBytes: vi.fn(async () => {
      if (bytes instanceof Error) throw bytes;
      return bytes;
    }),
  } as never;
}

const signed = `${API}/v1/media-files/${ID}?expires=1&sig=secret`;
const png = (extra: Partial<MediaObject> = {}) => ({ content_type: "image/png", signed_url: signed, ...extra }) as MediaObject;

describe("loadPosterBlob", () => {
  it("fetches the bytes of a trusted signed address into a blob of the STORED type", async () => {
    const d = deps(png({ width: 30, height: 40 }));
    const res = await loadPosterBlob(d, ID);
    expect(res.kind).toBe("ok");
    if (res.kind === "ok") {
      expect(res.blob.type).toBe("image/png");
      expect(res.width).toBe(30);
    }
    expect(d.fetchBytes).toHaveBeenCalledWith(signed, undefined);
  });

  it("never fetches an address on another origin (no wildcard, no made-up host)", async () => {
    const d = deps(png({ signed_url: "https://bucket.s3.example/x?sig=1" }));
    expect(await loadPosterBlob(d, ID)).toEqual({ kind: "failed", why: "address" });
    expect(d.fetchBytes).not.toHaveBeenCalled();
  });

  it("never draws an SVG or any non-picture the server names", async () => {
    for (const type of ["image/svg+xml", "application/pdf", "text/html", ""]) {
      const d = deps(png({ content_type: type }));
      expect(await loadPosterBlob(d, ID)).toEqual({ kind: "failed", why: "type" });
      expect(d.fetchBytes).not.toHaveBeenCalled();
    }
  });

  it("tells a missing permission or a foreign object from a network problem", async () => {
    expect(await loadPosterBlob(deps(new ApiError(403, "iam.forbidden", "")), ID)).toEqual({ kind: "unavailable", why: "denied" });
    expect(await loadPosterBlob(deps(new ApiError(404, "media.not_found", "")), ID)).toEqual({ kind: "unavailable", why: "missing" });
    expect(await loadPosterBlob(deps(new ApiError(0, "network.failure", "")), ID)).toEqual({ kind: "failed", why: "network" });
  });

  it("an expired or refused signed address is a failure to retry, never a picture", async () => {
    const d = deps(png(), new Response("{}", { status: 401 }));
    expect(await loadPosterBlob(d, ID)).toEqual({ kind: "failed", why: "network" });
  });

  it("refuses an empty answer and one over the size cap", async () => {
    const empty = deps(png(), new Response(new Uint8Array(0), { status: 200 }));
    expect(await loadPosterBlob(empty, ID)).toEqual({ kind: "failed", why: "size" });
    const huge = { ok: true, blob: async () => ({ size: DISPLAY_MAX_BYTES + 1 }) } as unknown as Response;
    expect(await loadPosterBlob(deps(png(), huge), ID)).toEqual({ kind: "failed", why: "size" });
  });
});
