import fs from "node:fs";
import path from "node:path";
import YAML from "yaml";
import { ROOT } from "./env.ts";
import {
  FORMATS,
  type CursorPoint,
  type FrameFormat,
  type FrameKind,
  type GraphicKind,
  type HighlightSpec,
  type ScrollSpec,
  type ZoomSpec,
} from "./types.ts";

export type Step =
  | { goto: string }
  | { click: string }
  | { hover: string }
  | { fill: { selector: string; value?: string; value_env?: string } }
  | { press: string | { selector?: string; key: string } }
  | { wait_for: string }
  | { wait: number };

export interface ShotSpec {
  /** path from the scenario folder (a picture shot) */
  image?: string;
  /** path from the scenario folder, a recorded clip of an old-form scene */
  clip?: string;
  graphic?: GraphicKind;
  frame: FrameKind;
  weight: number;
  title?: Record<string, string>;
  zoom?: ZoomSpec;
  highlight?: HighlightSpec;
  cursor?: CursorPoint[];
  scroll?: ScrollSpec;
  /** per language: a field given here replaces the same field of the shot when that language is built */
  perLanguage?: Record<string, Pick<ShotSpec, "zoom" | "highlight" | "cursor" | "scroll">>;
  /** text in the address pill of a browser frame, per language */
  address?: Record<string, string>;
  /** per frame format: a field given here replaces the same field of the shot in that format */
  perFormat?: Partial<Record<FrameFormat, Partial<Pick<ShotSpec, "image" | "frame" | "zoom" | "highlight" | "cursor" | "scroll" | "address">>>>;
  steps?: Record<string, string[]>;
  caption?: Record<string, string>;
  heading?: Record<string, string>;
  lines?: string[];
  qr?: string;
  logo?: string;
}

export interface SceneSpec {
  id: string;
  /** the old one-picture form; capture.ts reads its capture steps */
  source?: {
    image?: string;
    capture?: Step[];
    use?: "image" | "clip";
  };
  /** always filled: the old form becomes one shot */
  shots: ShotSpec[];
  narration: Record<string, string>;
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
  formats: FrameFormat[];
  background: string;
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

function langMap(v: unknown, what: string): Record<string, string> | undefined {
  if (v === undefined) return undefined;
  if (!isObj(v)) fail(`${what} must be a mapping of languages`);
  for (const [k, t] of Object.entries(v)) if (typeof t !== "string") fail(`${what}.${k} must be a string`);
  return v as Record<string, string>;
}

type Look = Pick<ShotSpec, "title" | "zoom" | "highlight" | "cursor">;

/** title, zoom, highlight, cursor: the same keys on a scene (old form) and on a shot. */
function parseShotLook(s: Record<string, unknown>, w: string): Look {
  const out: Look = {};
  out.title = langMap(s.title, `${w}.title`);
  if (s.zoom !== undefined) {
    const z = s.zoom;
    if (!isObj(z)) fail(`${w}.zoom must be a mapping`);
    if (typeof z.from !== "number" || typeof z.to !== "number" || z.from <= 0 || z.to <= 0) {
      fail(`${w}.zoom from/to must be positive numbers`);
    }
    out.zoom = { from: z.from, to: z.to, x: frac(z.x ?? 0.5, `${w}.zoom.x`), y: frac(z.y ?? 0.5, `${w}.zoom.y`) };
  }
  if (s.highlight !== undefined) {
    const h = s.highlight;
    if (!isObj(h)) fail(`${w}.highlight must be a mapping`);
    out.highlight = {
      x: frac(h.x, `${w}.highlight.x`),
      y: frac(h.y, `${w}.highlight.y`),
      w: frac(h.w, `${w}.highlight.w`),
      h: frac(h.h, `${w}.highlight.h`),
    };
  }
  if (s.cursor !== undefined) {
    if (!Array.isArray(s.cursor)) fail(`${w}.cursor must be a list`);
    const pts = s.cursor.map((p: unknown, j: number) => {
      if (!isObj(p)) fail(`${w}.cursor[${j}] must be a mapping`);
      return {
        at: frac(p.at, `${w}.cursor[${j}].at`),
        x: frac(p.x, `${w}.cursor[${j}].x`),
        y: frac(p.y, `${w}.cursor[${j}].y`),
        click: p.click === true,
      };
    });
    for (let j = 1; j < pts.length; j++) {
      if (pts[j]!.at < pts[j - 1]!.at) fail(`${w}.cursor must be sorted by "at"`);
    }
    out.cursor = pts;
  }
  return out;
}

function parseShot(sh: unknown, w: string, languages: string[], dir: string): ShotSpec {
  if (!isObj(sh)) fail(`${w} must be a mapping`);
  const hasImage = sh.image !== undefined;
  const hasGraphic = sh.graphic !== undefined;
  if (hasImage === hasGraphic) fail(`${w} needs exactly one of image, graphic`);
  const weight = sh.weight === undefined ? 1 : sh.weight;
  if (typeof weight !== "number" || weight <= 0) fail(`${w}.weight must be a positive number`);
  const frame = (sh.frame ?? "none") as string;
  if (!["phone", "browser", "none"].includes(frame)) fail(`${w}.frame: phone | browser | none`);
  const shot: ShotSpec = { ...parseShotLook(sh, w), frame: frame as FrameKind, weight };
  if (sh.scroll !== undefined) {
    const sc = sh.scroll;
    if (!isObj(sc)) fail(`${w}.scroll must be a mapping`);
    shot.scroll = { from: frac(sc.from ?? 0, `${w}.scroll.from`), to: frac(sc.to ?? 0, `${w}.scroll.to`) };
  }
  if (sh.per_language !== undefined) {
    const pl = sh.per_language;
    if (!isObj(pl)) fail(`${w}.per_language must be a mapping of languages`);
    const perLang: Record<string, Pick<ShotSpec, "zoom" | "highlight" | "cursor" | "scroll">> = {};
    for (const [lang, v] of Object.entries(pl)) {
      const lw = `${w}.per_language.${lang}`;
      if (!languages.includes(lang)) fail(`${lw}: the language is not in languages`);
      if (!isObj(v)) fail(`${lw} must be a mapping`);
      const look = parseShotLook(v, lw);
      const over: Pick<ShotSpec, "zoom" | "highlight" | "cursor" | "scroll"> = {};
      if (look.zoom !== undefined) over.zoom = look.zoom;
      if (look.highlight !== undefined) over.highlight = look.highlight;
      if (look.cursor !== undefined) over.cursor = look.cursor;
      if (v.scroll !== undefined) {
        const sc = v.scroll;
        if (!isObj(sc)) fail(`${lw}.scroll must be a mapping`);
        over.scroll = { from: frac(sc.from ?? 0, `${lw}.scroll.from`), to: frac(sc.to ?? 0, `${lw}.scroll.to`) };
      }
      perLang[lang] = over;
    }
    shot.perLanguage = perLang;
  }
  const addr = (v: unknown, aw: string): Record<string, string> | undefined => {
    if (v === undefined) return undefined;
    if (typeof v === "string") return Object.fromEntries(languages.map((l) => [l, v]));
    return langMap(v, aw);
  };
  if (sh.address !== undefined) {
    if (hasGraphic) fail(`${w}.address is only for a picture shot`);
    shot.address = addr(sh.address, `${w}.address`);
  }
  if (sh.per_format !== undefined) {
    if (hasGraphic) fail(`${w}.per_format is only for a picture shot`);
    const pf = sh.per_format;
    if (!isObj(pf)) fail(`${w}.per_format must be a mapping of formats`);
    const perFmt: NonNullable<ShotSpec["perFormat"]> = {};
    const fields = ["image", "frame", "zoom", "highlight", "cursor", "scroll", "address"];
    for (const [fmt, v] of Object.entries(pf)) {
      const fw = `${w}.per_format.${fmt}`;
      if (!Object.keys(FORMATS).includes(fmt)) fail(`${fw}: unknown format, use ${Object.keys(FORMATS).join(" | ")}`);
      if (!isObj(v)) fail(`${fw} must be a mapping`);
      for (const k of Object.keys(v)) {
        if (!fields.includes(k)) fail(`${fw}.${k}: unknown field, use ${fields.join(" | ")}`);
      }
      const look = parseShotLook(v, fw);
      const over: Partial<Pick<ShotSpec, "image" | "frame" | "zoom" | "highlight" | "cursor" | "scroll" | "address">> = {};
      if (v.image !== undefined) {
        if (typeof v.image !== "string") fail(`${fw}.image must be a path`);
        over.image = v.image;
      }
      if (v.frame !== undefined) {
        if (typeof v.frame !== "string" || !["phone", "browser", "none"].includes(v.frame)) {
          fail(`${fw}.frame: phone | browser | none`);
        }
        over.frame = v.frame as FrameKind;
      }
      if (look.zoom !== undefined) over.zoom = look.zoom;
      if (look.highlight !== undefined) over.highlight = look.highlight;
      if (look.cursor !== undefined) over.cursor = look.cursor;
      if (v.scroll !== undefined) {
        const sc = v.scroll;
        if (!isObj(sc)) fail(`${fw}.scroll must be a mapping`);
        over.scroll = { from: frac(sc.from ?? 0, `${fw}.scroll.from`), to: frac(sc.to ?? 0, `${fw}.scroll.to`) };
      }
      const a = addr(v.address, `${fw}.address`);
      if (a !== undefined) over.address = a;
      perFmt[fmt as FrameFormat] = over;
    }
    shot.perFormat = perFmt;
  }
  if (hasImage) {
    if (typeof sh.image !== "string") fail(`${w}.image must be a path`);
    shot.image = sh.image;
    return shot;
  }
  if (!["flow", "cta", "placeholder"].includes(sh.graphic as string)) fail(`${w}.graphic: flow | cta | placeholder`);
  shot.graphic = sh.graphic as GraphicKind;
  shot.frame = "none";
  shot.caption = langMap(sh.caption, `${w}.caption`);
  if (shot.graphic === "flow") {
    const st = sh.steps;
    if (!isObj(st)) fail(`${w}.steps must be a mapping of languages to lists`);
    shot.steps = {};
    for (const lang of languages) {
      const list = st[lang];
      if (!Array.isArray(list) || list.length < 2 || list.length > 5 || list.some((x) => typeof x !== "string")) {
        fail(`${w}.steps.${lang} must be a list of 2 to 5 strings`);
      }
      shot.steps[lang] = list as string[];
    }
  } else if (shot.graphic === "cta") {
    shot.heading = langMap(sh.heading, `${w}.heading`);
    if (sh.lines !== undefined) {
      if (!Array.isArray(sh.lines) || sh.lines.some((x) => typeof x !== "string")) fail(`${w}.lines must be a list of strings`);
      shot.lines = sh.lines as string[];
    }
    if (sh.qr !== undefined) {
      if (typeof sh.qr !== "string" || !sh.qr) fail(`${w}.qr must be a text`);
      shot.qr = sh.qr;
    }
    if (sh.logo !== undefined) {
      if (typeof sh.logo !== "string") fail(`${w}.logo must be a path`);
      if (!fs.existsSync(path.join(dir, sh.logo))) fail(`${w}: logo ${sh.logo} not found`);
      shot.logo = sh.logo;
    }
  } else if (!shot.caption) fail(`${w}.caption is required for a placeholder`);
  return shot;
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
    if (!isObj(s.narration)) fail(`${w}.narration is required`);
    const narration: Record<string, string> = {};
    for (const lang of languages as string[]) {
      const t = s.narration[lang];
      if (typeof t !== "string" || !t.trim()) fail(`${w}.narration.${lang} is missing`);
      narration[lang] = t.trim();
    }
    if (s.pause_after !== undefined && (typeof s.pause_after !== "number" || s.pause_after < 0)) {
      fail(`${w}.pause_after must be seconds`);
    }
    if ((s.source === undefined) === (s.shots === undefined)) fail(`${w} needs exactly one of source, shots`);

    let source: SceneSpec["source"];
    let shots: ShotSpec[];
    if (s.source !== undefined) {
      if (!isObj(s.source)) fail(`${w}.source must be a mapping`);
      const src: NonNullable<SceneSpec["source"]> = {};
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
      source = src;
      shots = [
        {
          ...parseShotLook(s, w),
          frame: "none",
          weight: 1,
          image: src.image ?? `captures/${s.id}.png`,
          clip: src.use === "clip" ? `captures/${s.id}.webm` : undefined,
        },
      ];
    } else {
      for (const k of ["title", "zoom", "highlight", "cursor"]) {
        if (s[k] !== undefined) fail(`${w}.${k}: with shots put it inside the shot`);
      }
      if (!Array.isArray(s.shots) || s.shots.length === 0) fail(`${w}.shots must be a non-empty list`);
      shots = s.shots.map((sh: unknown, j: number) => parseShot(sh, `${w}.shots[${j}]`, languages as string[], dir));
    }
    const scene: SceneSpec = { id: s.id, source, shots, narration };
    if (s.pause_after !== undefined) scene.pause_after = s.pause_after as number;
    return scene;
  });

  let formats: FrameFormat[] = Object.keys(FORMATS) as FrameFormat[];
  if (raw.formats !== undefined) {
    if (!Array.isArray(raw.formats) || raw.formats.length === 0) fail("formats must be a non-empty list");
    for (const f of raw.formats) {
      if (typeof f !== "string" || !(f in FORMATS)) fail(`formats: unknown "${String(f)}" (16x9, 9x16, 4x5)`);
    }
    formats = [...new Set(raw.formats as FrameFormat[])];
  }
  let background = "#4f46e5";
  if (raw.background !== undefined) {
    if (typeof raw.background !== "string" || !/^#[0-9a-f]{3,8}$/i.test(raw.background)) {
      fail('background must be a colour like "#4f46e5"');
    }
    background = raw.background;
  }

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
    formats,
    background,
    scenes,
    dir,
  };
}
