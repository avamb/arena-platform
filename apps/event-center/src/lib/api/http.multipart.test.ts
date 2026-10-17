/**
 * Task 7-1: a call may carry a multipart form through the SAME transport as a
 * JSON call, and every JSON/download behaviour stays as it was.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { call, createSession, download, CLIENT_CHANNEL, useSessionForTests } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type { SessionStore, Stored } from "@/lib/api/tabSession";

function memoryStore(): SessionStore & { value: Stored | null } {
  const s = {
    value: null as Stored | null,
    get: () => s.value,
    set: (v: Stored | null) => {
      s.value = v;
    },
  };
  return s;
}

interface Seen {
  path: string;
  method: string;
  headers: Record<string, string>;
  body: unknown;
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

async function signedIn() {
  const store = memoryStore();
  const s = createSession({
    store,
    bus: null,
    locks: null,
    refresh: async () => ({ accessToken: "a-new", refreshToken: "r-new", expiresAt: new Date(Date.now() + 900_000).toISOString(), userId: "u1" }),
  });
  useSessionForTests(s);
  await s.signedIn({ accessToken: "a-good", refreshToken: "r-old", expiresAt: new Date(Date.now() + 600_000).toISOString(), userId: "u1" });
  return s;
}

function stubFetch(handler: (seen: Seen) => Response | Promise<Response>) {
  const seen: Seen[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      const s: Seen = {
        path: new URL(url).pathname,
        method: String(init.method),
        headers: init.headers as Record<string, string>,
        body: init.body,
      };
      seen.push(s);
      return handler(s);
    }),
  );
  return seen;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("multipart through the shared transport", () => {
  it("sends the FormData as is, with the token and channel, and NO Content-Type of its own", async () => {
    await signedIn();
    const seen = stubFetch(() => json(201, { media_object: { id: "m1" } }));
    const form = new FormData();
    form.append("owner_type", "event_poster");
    form.append("file", new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }), "poster.png");
    const res = await call<{ media_object: { id: string } }>({ route: API.mediaUpload, form });
    expect(res.media_object.id).toBe("m1");
    expect(seen).toHaveLength(1);
    expect(seen[0].path).toBe("/v1/media");
    expect(seen[0].method).toBe("POST");
    expect(seen[0].body).toBe(form);
    expect(seen[0].headers.Authorization).toBe("Bearer a-good");
    expect(seen[0].headers["X-Client-Channel"]).toBe(CLIENT_CHANNEL);
    expect(Object.keys(seen[0].headers).map((k) => k.toLowerCase())).not.toContain("content-type");
  });

  it("refreshes the token once on a 401 and repeats the SAME form", async () => {
    await signedIn();
    let n = 0;
    const seen = stubFetch(() =>
      ++n === 1 ? json(401, { error: { code: "auth.unauthenticated", message: "no" } }) : json(201, { media_object: { id: "m2" } }),
    );
    const form = new FormData();
    form.append("file", new Blob([new Uint8Array([9])]), "poster.png");
    await call({ route: API.mediaUpload, form });
    expect(seen).toHaveLength(2);
    expect(seen[1].body).toBe(form);
    expect(seen[1].headers.Authorization).toBe("Bearer a-new");
  });

  it("a caller cancel is reported as network.aborted and not as a timeout", async () => {
    await signedIn();
    const controller = new AbortController();
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string, init: RequestInit) =>
          new Promise((_res, rej) => init.signal?.addEventListener("abort", () => rej(new DOMException("x", "AbortError")))),
      ),
    );
    const pending = call({ route: API.mediaUpload, form: new FormData(), signal: controller.signal });
    controller.abort();
    await expect(pending).rejects.toMatchObject({ status: 0, code: "network.aborted" });
  });

  it("an already cancelled signal never produces a result", async () => {
    await signedIn();
    const controller = new AbortController();
    controller.abort();
    vi.stubGlobal(
      "fetch",
      vi.fn((_u: string, init: RequestInit) =>
        init.signal?.aborted ? Promise.reject(new DOMException("x", "AbortError")) : Promise.resolve(json(200, {})),
      ),
    );
    await expect(call({ route: API.mediaGet, params: { id: "m" }, signal: controller.signal })).rejects.toMatchObject({
      code: "network.aborted",
    });
  });

  it("refuses a call that carries both a body and a form", async () => {
    await signedIn();
    stubFetch(() => json(200, {}));
    await expect(call({ route: API.mediaUpload, body: { a: 1 }, form: new FormData() })).rejects.toThrow(/not both/);
  });
});

describe("JSON and download calls are unchanged", () => {
  it("a JSON call still sets the JSON Content-Type and stringifies the body", async () => {
    await signedIn();
    const seen = stubFetch(() => json(200, { event: { id: "e1" } }));
    await call({ route: API.eventUpdate, params: { org_id: "o", id: "e1" }, body: { poster_media_id: "m1" } });
    expect(seen[0].headers["Content-Type"]).toBe("application/json");
    expect(seen[0].body).toBe(JSON.stringify({ poster_media_id: "m1" }));
    expect(seen[0].method).toBe("PATCH");
    expect(seen[0].path).toBe("/v1/organizations/o/events/e1");
  });

  it("a GET sends no body and no Content-Type", async () => {
    await signedIn();
    const seen = stubFetch(() => json(200, {}));
    await call({ route: API.mediaGet, params: { id: "m1" } });
    expect(seen[0].body).toBeUndefined();
    expect(Object.keys(seen[0].headers)).not.toContain("Content-Type");
  });

  it("a download still returns the blob and the file name", async () => {
    await signedIn();
    stubFetch(
      () => new Response("a,b\n1,2\n", { status: 200, headers: { "Content-Disposition": "attachment; filename=x.csv" } }),
    );
    const file = await download({ route: API.me, fallbackName: "f.csv" });
    expect(file.filename).toBe("x.csv");
    expect(await file.blob.text()).toBe("a,b\n1,2\n");
  });
});
