/**
 * The poster step of the wizard as a small state machine (poster-first creation).
 *
 * ONE picture per selection: it is uploaded once, as soon as it is chosen, and the
 * media id that comes back is what the later "Create event" attaches (the draft
 * keeps it) AND what the poster is read with. Reading is a SEPARATE, EXPLICIT,
 * paid step: nothing here reads a poster by itself, on a render, after a reload or
 * after a failure. A second press while a read is running does nothing, and a
 * failed or lost read is never repeated by itself (the person decides).
 *
 * Every answer belongs to the exact organization and selection it was asked for:
 * `reset()` (the picture was removed or changed, "continue without a poster" was
 * pressed) and `dispose()` (the screen or the organization went away) start a new
 * generation, so a late answer of an older generation is dropped and cannot change
 * the draft, the review or the notices of the new one.
 *
 * Free of React and of `fetch`: the calls are passed in, so every transition is
 * tested with fakes. It holds ids and the parsed hints only: never the file, a
 * blob address, a signed link or a token.
 */
import { ApiError } from "@/lib/api/http";
import { uploadPoster, type Failure, type PosterApi } from "@/lib/posterFlow";
import type { PosterType } from "@/lib/posters";
import { hasUsableFacts, parseFacts, type PosterFacts } from "@/lib/wizard/posterHints";

export type UploadView =
  | { phase: "idle" }
  | { phase: "uploading" }
  | { phase: "uploaded"; mediaId: string }
  /** The picture was NOT stored. The file is still in the picker: a retry uploads it again. */
  | { phase: "failed"; failure: Failure };

export type ReadFailure =
  /** The server has no reading service set up: not a thing to retry. */
  | "unconfigured"
  /** The organization used its readings for today. */
  | "quota"
  | "denied"
  /** The stored picture is gone or is not this organization's. */
  | "missing"
  /** The picture cannot be read (type or size). */
  | "unsupported"
  /** The service answered that it cannot read right now: the person may try again. */
  | "unavailable"
  /** No answer (timeout, lost connection): the reading MAY have been counted. */
  | "unknown";

export type ReadView =
  | { phase: "idle" }
  | { phase: "reading" }
  /** Suggestions for exactly this organization and this uploaded picture. */
  | { phase: "ready"; orgId: string; mediaId: string; facts: PosterFacts }
  /** The poster was read and nothing usable was found. */
  | { phase: "nothing" }
  | { phase: "failed"; reason: ReadFailure; canRetry: boolean }
  /** The person applied or dismissed the suggestions. */
  | { phase: "done"; applied: readonly string[] };

export interface IntakeView {
  upload: UploadView;
  read: ReadView;
}

export interface IntakeApi {
  upload: PosterApi["upload"];
  /** One paid read. The answer is untrusted JSON (`parseFacts` decides what is usable). */
  readFacts: (orgId: string, mediaId: string, signal?: AbortSignal) => Promise<unknown>;
}

export interface Intake {
  view(): IntakeView;
  subscribe(listener: (v: IntakeView) => void): () => void;
  /** A draft that already holds an uploaded picture (after a reload): no upload, no read. */
  adopt(mediaId: string): void;
  /** Uploads the chosen file (also the retry of a failed upload). Does nothing while an upload runs or one is stored. */
  upload(file: Blob, type: PosterType): Promise<void>;
  /** The explicit read. Does nothing unless a picture is uploaded and no read is running. */
  read(): Promise<void>;
  /** The person applied (with `applied` = what was filled) or dismissed the suggestions. */
  finishRead(applied: readonly string[]): void;
  /** Another picture, no picture, or "continue without": a new generation, everything pending is dropped. */
  reset(): void;
  /** The screen is gone: aborts what runs and ignores every answer. */
  dispose(): void;
}

export function classifyReadFailure(err: unknown): { reason: ReadFailure; canRetry: boolean } | "aborted" {
  if (err instanceof ApiError) {
    if (err.code === "network.aborted") return "aborted";
    if (err.status === 401 || err.status === 403) return { reason: "denied", canRetry: false };
    if (err.status === 404) return { reason: "missing", canRetry: false };
    if (err.status === 422) return { reason: "unsupported", canRetry: false };
    if (err.status === 429) return { reason: "quota", canRetry: false };
    if (err.status === 503 && err.code === "media.poster_reader_not_configured") return { reason: "unconfigured", canRetry: false };
    if (err.status >= 500) return { reason: "unavailable", canRetry: true };
    if (err.status === 0) return { reason: "unknown", canRetry: true };
    return { reason: "unavailable", canRetry: false };
  }
  return { reason: "unknown", canRetry: true };
}

export function createIntake(args: { orgId: string; api: IntakeApi; onUploaded?: (mediaId: string) => void }): Intake {
  let state: IntakeView = { upload: { phase: "idle" }, read: { phase: "idle" } };
  let gen = 0;
  let disposed = false;
  let control: AbortController | null = null;
  const listeners = new Set<(v: IntakeView) => void>();
  const set = (patch: Partial<IntakeView>) => {
    state = { ...state, ...patch };
    for (const l of [...listeners]) l(state);
  };
  const abort = () => {
    control?.abort();
    control = null;
  };
  return {
    view: () => state,
    subscribe(l) {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    adopt(mediaId) {
      if (disposed) return;
      gen += 1;
      abort();
      set({ upload: { phase: "uploaded", mediaId }, read: { phase: "idle" } });
    },
    async upload(file, type) {
      if (disposed) return;
      if (state.upload.phase === "uploading" || state.upload.phase === "uploaded") return;
      const mine = gen;
      const ctl = new AbortController();
      control = ctl;
      set({ upload: { phase: "uploading" } });
      const res = await uploadPoster({ upload: args.api.upload }, file, type, ctl.signal);
      if (disposed || mine !== gen) return;
      if (control === ctl) control = null;
      if (res.kind === "failed") {
        // A person who left or chose again is not an error to show.
        set({ upload: res.failure.kind === "aborted" ? { phase: "idle" } : { phase: "failed", failure: res.failure } });
        return;
      }
      set({ upload: { phase: "uploaded", mediaId: res.mediaId }, read: { phase: "idle" } });
      args.onUploaded?.(res.mediaId);
    },
    async read() {
      if (disposed) return;
      const up = state.upload;
      if (up.phase !== "uploaded" || state.read.phase === "reading") return;
      const mine = gen;
      const mediaId = up.mediaId;
      const ctl = new AbortController();
      control = ctl;
      set({ read: { phase: "reading" } });
      let raw: unknown;
      try {
        raw = await args.api.readFacts(args.orgId, mediaId, ctl.signal);
      } catch (err) {
        if (disposed || mine !== gen) return;
        if (control === ctl) control = null;
        const c = classifyReadFailure(err);
        set({ read: c === "aborted" ? { phase: "idle" } : { phase: "failed", reason: c.reason, canRetry: c.canRetry } });
        return;
      }
      if (disposed || mine !== gen) return;
      if (control === ctl) control = null;
      const facts = parseFacts(raw);
      if (facts === null) {
        set({ read: { phase: "failed", reason: "unavailable", canRetry: true } });
        return;
      }
      set({ read: hasUsableFacts(facts) ? { phase: "ready", orgId: args.orgId, mediaId, facts } : { phase: "nothing" } });
    },
    finishRead(applied) {
      if (disposed || state.read.phase === "reading") return;
      set({ read: { phase: "done", applied } });
    },
    reset() {
      if (disposed) return;
      gen += 1;
      abort();
      set({ upload: { phase: "idle" }, read: { phase: "idle" } });
    },
    dispose() {
      disposed = true;
      gen += 1;
      abort();
      listeners.clear();
    },
  };
}
