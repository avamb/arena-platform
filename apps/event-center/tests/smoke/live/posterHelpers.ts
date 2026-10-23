/**
 * Helpers of the poster-first live specs: a real browser-encoded picture, the
 * REST reads that prove what the server holds, and the control of the scripted
 * poster reader of the test-only arena-api build (`-tags e2eposter`).
 *
 * The scripted reader replaces ONLY the vision model. The route
 * POST /v1/organizations/{org}/media/{media}/poster-facts (authentication,
 * organization access, permission, media lookup, quota, reading the stored bytes)
 * is the real one. No Anthropic key exists in this harness.
 */
import { createHash } from "node:crypto";
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { expect, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, person, type Role } from "./liveEnv";

export const env = liveEnv();
export const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });
export const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
export const sha256 = (b: Buffer) => createHash("sha256").update(b).digest("hex");

export interface EventRow {
  id: string;
  name: string;
  status: string;
  poster_media_id: string | null;
}

export interface Img {
  buffer: Buffer;
  width: number;
  height: number;
  mime: "image/jpeg" | "image/png";
}

/** A REAL picture, encoded by the browser itself (so the browser can also decode it). */
export async function makeImage(page: Page, mime: Img["mime"], width: number, height: number, hue: number): Promise<Img> {
  const b64 = await page.evaluate(
    async ({ mime, width, height, hue }) => {
      const canvas = document.createElement("canvas");
      canvas.width = width;
      canvas.height = height;
      const ctx = canvas.getContext("2d") as CanvasRenderingContext2D;
      const g = ctx.createLinearGradient(0, 0, width, height);
      g.addColorStop(0, `hsl(${hue} 80% 50%)`);
      g.addColorStop(1, `hsl(${(hue + 120) % 360} 80% 30%)`);
      ctx.fillStyle = g;
      ctx.fillRect(0, 0, width, height);
      ctx.fillStyle = "#fff";
      ctx.font = `${Math.round(height / 8)}px sans-serif`;
      ctx.fillText(`poster ${hue}`, width / 10, height / 2);
      const blob = await new Promise<Blob | null>((res) => canvas.toBlob(res, mime, 0.92));
      if (blob === null) throw new Error("the browser could not encode the picture");
      const bytes = new Uint8Array(await blob.arrayBuffer());
      let s = "";
      for (const b of bytes) s += String.fromCharCode(b);
      return btoa(s);
    },
    { mime, width, height, hue },
  );
  return { buffer: Buffer.from(b64, "base64"), width, height, mime };
}

export const file = (img: Img, name?: string) => ({
  name: name ?? (img.mime === "image/png" ? "poster.png" : "poster.jpg"),
  mimeType: img.mime,
  buffer: img.buffer,
});

export async function events(request: APIRequestContext): Promise<EventRow[]> {
  const res = await request.get(`${base}/events`, { headers: auth("org_admin") });
  expect(res.status()).toBe(200);
  return ((await res.json()) as { events: EventRow[] }).events;
}
export const named = async (request: APIRequestContext, name: string) => (await events(request)).filter((e) => e.name === name);

export async function mediaOf(request: APIRequestContext, id: string) {
  const res = await request.get(`${env.api_base_url}/v1/media/${id}`, { headers: auth("org_admin") });
  expect(res.status()).toBe(200);
  return ((await res.json()) as { media_object: { content_type: string; byte_size: number; checksum_sha256: string; signed_url: string } }).media_object;
}

/** The stored bytes, through the signed address, equal what was uploaded. */
export async function expectStored(request: APIRequestContext, mediaId: string, img: Img) {
  const m = await mediaOf(request, mediaId);
  expect(m.content_type).toBe(img.mime);
  expect(m.byte_size).toBe(img.buffer.length);
  expect(m.checksum_sha256).toBe(sha256(img.buffer));
  const bytes = await request.get(m.signed_url);
  expect(bytes.status()).toBe(200);
  expect(sha256(Buffer.from(await bytes.body()))).toBe(sha256(img.buffer));
}

export async function venueId(request: APIRequestContext): Promise<string> {
  const res = await request.get(`${base}/venues`, { headers: auth("org_admin") });
  const v = ((await res.json()) as { venues: { id: string; name: string }[] }).venues.find((x) => x.name === "E2E Hall");
  if (v === undefined) throw new Error("the fixture venue E2E Hall is missing");
  return v.id;
}

/** What the scripted reader answers (the poster facts of the API contract). */
export interface Facts {
  name: string;
  date: string;
  time: string;
  venue_name: string;
  city: string;
  age: string;
  description: string;
  categories: { name: string; price_minor: number; currency: string }[];
}
export const NO_FACTS: Facts = { name: "", date: "", time: "", venue_name: "", city: "", age: "", description: "", categories: [] };

export function scriptFacts(facts: Partial<Facts>, delayMs = 0) {
  writeFileSync(env.poster_script, JSON.stringify({ mode: "facts", facts: { ...NO_FACTS, ...facts }, delay_ms: delayMs }));
}
/** The scripted model fails: the real route answers 503 media.poster_read_failed. */
export function scriptFailure() {
  writeFileSync(env.poster_script, JSON.stringify({ mode: "fail" }));
}
/** No script: the reader answers "not configured" (the real route's 503 media.poster_reader_not_configured). */
export function scriptNotConfigured() {
  rmSync(env.poster_script, { force: true });
}

/** The reads the scripted reader has served so far (one line per call that reached the model boundary). */
export function reads(): { sha256: string; media_type: string; bytes: number }[] {
  if (!existsSync(env.poster_calls)) return [];
  return readFileSync(env.poster_calls, "utf8")
    .split("\n")
    .filter((l) => l.trim() !== "")
    .map((l) => JSON.parse(l) as { sha256: string; media_type: string; bytes: number });
}

/** Counts the writes of a page: uploads, attaches, creates and poster reads. */
export function trackWrites(page: Page) {
  const seen = { upload: 0, attach: 0, create: 0, read: 0 };
  page.on("request", (r) => {
    const url = r.url();
    if (r.method() === "POST" && /\/v1\/media$/.test(url)) seen.upload += 1;
    if (r.method() === "PATCH" && /\/events\/[0-9a-f-]{36}$/.test(url)) seen.attach += 1;
    if (r.method() === "POST" && url.includes("/imports/event-bundle") && !url.includes("dry_run=true")) seen.create += 1;
    if (r.method() === "POST" && url.endsWith("/poster-facts")) seen.read += 1;
  });
  return seen;
}
