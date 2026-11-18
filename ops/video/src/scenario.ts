import fs from "node:fs";
import path from "node:path";
import YAML from "yaml";
import { ROOT } from "./env.ts";
import type { CursorPoint, HighlightSpec, ZoomSpec } from "./types.ts";

export type Step =
  | { goto: string }
  | { click: string }
  | { hover: string }
  | { fill: { selector: string; value?: string; value_env?: string } }
  | { press: string | { selector?: string; key: string } }
  | { wait_for: string }
  | { wait: number };

export interface SceneSpec {
  id: string;
  source: {
    image?: string;
    capture?: Step[];
    use?: "image" | "clip";
  };
  narration: Record<string, string>;
  title?: Record<string, string>;
  zoom?: ZoomSpec;
  highlight?: HighlightSpec;
  cursor?: CursorPoint[];
  pause_after?: number;
}

export interface Scenario {
  id: string;
  fps: number;
  languages: string[];
  title?: Record<string, string>;
  voice: {
    provider: "auto" | "elevenlabs" | "silent";
    elevenlabs: { voice_id_env: string; model_id: string };
  };
  capture?: {
    base_url_env: string;
    viewport: { width: number; height: number };
    setup?: Step[];
  };
  scenes: SceneSpec[];
  dir: string;
}

const STEP_KEYS = ["goto", "click", "hover", "fill", "press", "wait_for", "wait"];

function fail(msg: string): never {
  throw new Error(`scenario: ${msg}`);
}

function isObj(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function frac(v: unknown, what: string): number {
  if (typeof v !== "number" || v < 0 || v > 1) fail(`${what} must be a number from 0 to 1`);
  return v;
}

const PASSWORD_RE = /pass(word|wd)?|парол|secret/i;

export function validateSteps(steps: unknown, where: string): Step[] {
  if (!Array.isArray(steps)) fail(`${where} must be a list of steps`);
  return steps.map((s, i) => {
    const w = `${where}[${i}]`;
    if (!isObj(s)) fail(`${w} must be a mapping`);
    const keys = Object.keys(s).filter((k) => STEP_KEYS.includes(k));
    if (keys.length !== 1 || Object.keys(s).length !== 1) {
      fail(`${w} must have exactly one of: ${STEP_KEYS.join(", ")}`);
    }
    const k = keys[0]!;
    const v = s[k];
    if (k === "wait") {
      if (typeof v !== "number" || v < 0) fail(`${w}.wait must be milliseconds`);
    } else if (k === "fill") {
      if (!isObj(v) || typeof v.selector !== "string") fail(`${w}.fill needs a selector`);
      const hasV = typeof v.value === "string";
      const hasE = typeof v.value_env === "string";
      if (hasV === hasE) fail(`${w}.fill needs exactly one of value, value_env`);
      if (hasV && PASSWORD_RE.test(v.selector as string)) {
        fail(`${w}: a password field must use value_env, never value (secrets stay out of the repository)`);
      }
    } else if (k === "press") {
      if (typeof v !== "string" && !(isObj(v) && typeof v.key === "string")) fail(`${w}.press must be a key`);
    } else if (typeof v !== "string") {
      fail(`${w}.${k} must be a string`);
    }
    return s as Step;
  });
}

export function loadScenario(id: string): Scenario {
  if (!/^[a-z0-9][a-z0-9_-]*$/i.test(id)) fail(`bad scenario id "${id}"`);
  const dir = path.join(ROOT, "scenarios", id);
  const file = path.join(dir, "scenario.yaml");
  if (!fs.existsSync(file)) fail(`${file} not found`);
  const raw: unknown = YAML.parse(fs.readFileSync(file, "utf8"));
  if (!isObj(raw)) fail("file must be a mapping");
  if (raw.id !== id) fail(`id "${String(raw.id)}" differs from the folder name "${id}"`);
  const fps = raw.fps === undefined ? 30 : raw.fps;
  if (typeof fps !== "number" || fps < 1 || fps > 120) fail("fps must be a number from 1 to 120");
  const languages = raw.languages;
  if (!Array.isArray(languages) || languages.length === 0 || languages.some((l) => typeof l !== "string")) {
    fail("languages must be a non-empty list of strings");
  }
  const v = isObj(raw.voice) ? raw.voice : {};
  const provider = (v.provider ?? "auto") as string;
  if (!["auto", "elevenlabs", "silent"].includes(provider)) fail("voice.provider: auto | elevenlabs | silent");
  const el = isObj(v.elevenlabs) ? v.elevenlabs : {};

  let capture: Scenario["capture"];
  if (raw.capture !== undefined) {
    const c = raw.capture;
    if (!isObj(c) || typeof c.base_url_env !== "string") fail("capture.base_url_env is required");
    const vp = isObj(c.viewport) ? c.viewport : { width: 1440, height: 900 };
    if (typeof vp.width !== "number" || typeof vp.height !== "number") fail("capture.viewport needs width and height");
    capture = {
      base_url_env: c.base_url_env,
      viewport: { width: vp.width, height: vp.height },
      setup: c.setup === undefined ? undefined : validateSteps(c.setup, "capture.setup"),
    };
  }

  if (!Array.isArray(raw.scenes) || raw.scenes.length === 0) fail("scenes must be a non-empty list");
  const seen = new Set<string>();
  const scenes: SceneSpec[] = raw.scenes.map((s: unknown, i: number) => {
    const w = `scenes[${i}]`;
    if (!isObj(s) || typeof s.id !== "string") fail(`${w}.id is required`);
    if (seen.has(s.id)) fail(`${w}: duplicate scene id "${s.id}"`);
    seen.add(s.id);
    if (!isObj(s.source)) fail(`${w}.source is required`);
    const src: SceneSpec["source"] = {};
    if (s.source.image !== undefined) {
      if (typeof s.source.image !== "string") fail(`${w}.source.image must be a path`);
      src.image = s.source.image;
      if (!fs.existsSync(path.join(dir, src.image))) fail(`${w}: picture ${src.image} not found`);
    }
    if (s.source.capture !== undefined) {
      src.capture = validateSteps(s.source.capture, `${w}.source.capture`);
      if (!capture) fail(`${w} has capture steps but the scenario has no capture section`);
    }
    if (!src.image && !src.capture) fail(`${w}.source needs image or capture`);
    if (s.source.use !== undefined) {
      if (s.source.use !== "image" && s.source.use !== "clip") fail(`${w}.source.use: image | clip`);
      src.use = s.source.use;
    }
    if (!isObj(s.narration)) fail(`${w}.narration is required`);
    const narration: Record<string, string> = {};
    for (const lang of languages as string[]) {
      const t = s.narration[lang];
      if (typeof t !== "string" || !t.trim()) fail(`${w}.narration.${lang} is missing`);
      narration[lang] = t.trim();
    }
    const scene: SceneSpec = { id: s.id, source: src, narration };
    if (s.title !== undefined) {
      if (!isObj(s.title)) fail(`${w}.title must be a mapping of languages`);
      scene.title = s.title as Record<string, string>;
    }
    if (s.zoom !== undefined) {
      const z = s.zoom;
      if (!isObj(z)) fail(`${w}.zoom must be a mapping`);
      if (typeof z.from !== "number" || typeof z.to !== "number" || z.from <= 0 || z.to <= 0) {
        fail(`${w}.zoom from/to must be positive numbers`);
      }
      scene.zoom = { from: z.from, to: z.to, x: frac(z.x ?? 0.5, `${w}.zoom.x`), y: frac(z.y ?? 0.5, `${w}.zoom.y`) };
    }
    if (s.highlight !== undefined) {
      const h = s.highlight;
      if (!isObj(h)) fail(`${w}.highlight must be a mapping`);
      scene.highlight = {
        x: frac(h.x, `${w}.highlight.x`),
        y: frac(h.y, `${w}.highlight.y`),
        w: frac(h.w, `${w}.highlight.w`),
        h: frac(h.h, `${w}.highlight.h`),
      };
    }
    if (s.cursor !== undefined) {
      if (!Array.isArray(s.cursor)) fail(`${w}.cursor must be a list`);
      scene.cursor = s.cursor.map((p: unknown, j: number) => {
        if (!isObj(p)) fail(`${w}.cursor[${j}] must be a mapping`);
        return {
          at: frac(p.at, `${w}.cursor[${j}].at`),
          x: frac(p.x, `${w}.cursor[${j}].x`),
          y: frac(p.y, `${w}.cursor[${j}].y`),
          click: p.click === true,
        };
      });
      for (let j = 1; j < scene.cursor.length; j++) {
        if (scene.cursor[j]!.at < scene.cursor[j - 1]!.at) fail(`${w}.cursor must be sorted by "at"`);
      }
    }
    if (s.pause_after !== undefined) {
      if (typeof s.pause_after !== "number" || s.pause_after < 0) fail(`${w}.pause_after must be seconds`);
      scene.pause_after = s.pause_after;
    }
    return scene;
  });

  return {
    id,
    fps,
    languages: languages as string[],
    title: isObj(raw.title) ? (raw.title as Record<string, string>) : undefined,
    voice: {
      provider: provider as Scenario["voice"]["provider"],
      elevenlabs: {
        voice_id_env: typeof el.voice_id_env === "string" ? el.voice_id_env : "ELEVENLABS_VOICE_ID",
        model_id: typeof el.model_id === "string" ? el.model_id : "eleven_multilingual_v2",
      },
    },
    capture,
    scenes,
    dir,
  };
}
