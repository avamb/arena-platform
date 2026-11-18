import fs from "node:fs";
import path from "node:path";
import { bundle } from "@remotion/bundler";
import { renderMedia, selectComposition } from "@remotion/renderer";
import QRCode from "qrcode";
import { loadEnv, ROOT } from "./env.ts";
import { loadScenario, type ShotSpec } from "./scenario.ts";
import { timeCues, toSrt, toVtt } from "./subtitles.ts";
import {
  FORMATS,
  type FrameFormat,
  type Manifest,
  type ManifestScene,
  type ManifestShot,
  type VideoProps,
} from "./types.ts";
import { createVoiceProvider } from "./voice/index.ts";

function pngSize(file: string): { w: number; h: number } {
  const b = fs.readFileSync(file);
  if (b.length < 24 || b.toString("ascii", 1, 4) !== "PNG") throw new Error(`${file} is not a PNG file`);
  return { w: b.readUInt32BE(16), h: b.readUInt32BE(20) };
}

function copyInto(publicDir: string, rel: string, from: string): string {
  const to = path.join(publicDir, rel);
  fs.mkdirSync(path.dirname(to), { recursive: true });
  fs.copyFileSync(from, to);
  return rel.replace(/\\/g, "/");
}

/** Divides the frames of a scene between shots in proportion to their weight; every shot gets at least one frame. */
function splitFrames(total: number, weights: number[]): number[] {
  const sum = weights.reduce((a, b) => a + b, 0);
  const out: number[] = [];
  let used = 0;
  weights.forEach((w, i) => {
    const left = weights.length - 1 - i;
    const f = i === weights.length - 1 ? total - used : Math.min(total - used - left, Math.max(1, Math.round((total * w) / sum)));
    out.push(Math.max(1, f));
    used += out[i]!;
  });
  return out;
}

async function buildShot(
  spec: ShotSpec,
  o: { sceneId: string; index: number; lang: string; scenarioDir: string; publicDir: string; startFrame: number; frames: number },
): Promise<ManifestShot> {
  const { lang } = o;
  const shot: ManifestShot = {
    startFrame: o.startFrame,
    frames: o.frames,
    frame: spec.frame,
    title: spec.title?.[lang],
    zoom: spec.zoom,
    highlight: spec.highlight,
    cursor: spec.cursor,
    scroll: spec.scroll,
  };
  if (spec.image) {
    const imgPath = path.join(o.scenarioDir, spec.image);
    if (!fs.existsSync(imgPath)) throw new Error(`scene ${o.sceneId}: picture ${imgPath} not found, run capture first`);
    const size = pngSize(imgPath);
    shot.image = copyInto(o.publicDir, `img/${o.sceneId}_${o.index}.png`, imgPath);
    shot.imageWidth = size.w;
    shot.imageHeight = size.h;
    if (spec.clip) {
      const clipPath = path.join(o.scenarioDir, spec.clip);
      if (!fs.existsSync(clipPath)) throw new Error(`scene ${o.sceneId}: clip ${clipPath} not found, run capture first`);
      shot.clip = copyInto(o.publicDir, `clip/${o.sceneId}_${o.index}.webm`, clipPath);
    }
    return shot;
  }
  shot.graphic = spec.graphic;
  shot.caption = spec.caption?.[lang];
  if (spec.graphic === "flow") shot.steps = spec.steps?.[lang];
  if (spec.graphic === "cta") {
    shot.heading = spec.heading?.[lang];
    shot.lines = spec.lines;
    if (spec.qr) shot.qr = await QRCode.toDataURL(spec.qr, { margin: 1, width: 640, errorCorrectionLevel: "M" });
    if (spec.logo) {
      const logoPath = path.join(o.scenarioDir, spec.logo);
      const size = pngSize(logoPath);
      shot.logo = copyInto(o.publicDir, `logo/${o.sceneId}_${o.index}.png`, logoPath);
      shot.logoWidth = size.w;
      shot.logoHeight = size.h;
    }
  }
  return shot;
}

async function main(): Promise<void> {
  loadEnv();
  const args = process.argv.slice(2);
  // npm may swallow a flag after "--": VIDEO_VOICE=silent does the same as --voice silent
  let force: "silent" | undefined = process.env.VIDEO_VOICE === "silent" ? "silent" : undefined;
  const vi = args.indexOf("--voice");
  if (vi >= 0) {
    if (args[vi + 1] !== "silent") throw new Error("--voice accepts only: silent");
    force = "silent";
    args.splice(vi, 2);
  }
  const [id, lang] = args;
  if (!id || !lang) throw new Error("usage: npm run build:video -- <scenario> <language> [--voice silent]");
  const sc = loadScenario(id);
  if (!sc.languages.includes(lang)) throw new Error(`language ${lang} is not in the scenario (${sc.languages.join(", ")})`);

  const outDir = path.join(ROOT, "out", id, lang);
  const workDir = path.join(outDir, ".work");
  const publicDir = path.join(workDir, "public");
  fs.rmSync(workDir, { recursive: true, force: true });
  fs.mkdirSync(publicDir, { recursive: true });

  const provider = createVoiceProvider(sc.voice, path.join(workDir, "silent"), force);
  console.log(`voice provider: ${provider.name}`);

  const scenes: ManifestScene[] = [];
  let startFrame = 0;
  let newChars = 0;
  for (const s of sc.scenes) {
    const text = s.narration[lang]!;
    const voice = await provider.synthesize(text, lang);
    newChars += voice.chars;
    const pause = s.pause_after ?? 0.3;
    const frames = Math.max(s.shots.length, Math.round((voice.durationSec + pause) * sc.fps));

    const audio = copyInto(publicDir, `audio/${s.id}${path.extname(voice.file)}`, voice.file);
    const split = splitFrames(
      frames,
      s.shots.map((x) => x.weight),
    );
    const shots: ManifestShot[] = [];
    let off = 0;
    for (let j = 0; j < s.shots.length; j++) {
      shots.push(
        await buildShot(s.shots[j]!, {
          sceneId: s.id,
          index: j,
          lang,
          scenarioDir: sc.dir,
          publicDir,
          startFrame: off,
          frames: split[j]!,
        }),
      );
      off += split[j]!;
    }

    const startSec = startFrame / sc.fps;
    scenes.push({
      id: s.id,
      startFrame,
      frames,
      startSec,
      durationSec: frames / sc.fps,
      voiceSec: voice.durationSec,
      pauseAfterSec: pause,
      narration: text,
      chars: voice.chars,
      cached: voice.cached,
      audio,
      shots,
      cues: timeCues(text, startSec, Math.min(voice.durationSec, frames / sc.fps)),
    });
    startFrame += frames;
  }

  const manifest: Manifest = {
    scenario: id,
    language: lang,
    provider: provider.name,
    fps: sc.fps,
    totalFrames: startFrame,
    totalSeconds: startFrame / sc.fps,
    background: sc.background,
    formats: sc.formats,
    scenes,
  };
  const base = `${id}_${lang}`;
  // the QR data URL is big and useless to read: the file shows its size only
  const fileCopy = JSON.parse(JSON.stringify(manifest)) as Manifest;
  for (const sc2 of fileCopy.scenes) for (const sh of sc2.shots) if (sh.qr) sh.qr = `(png data url, ${sh.qr.length} chars)`;
  fs.writeFileSync(path.join(outDir, "manifest.json"), JSON.stringify(fileCopy, null, 2) + "\n");
  const cues = scenes.flatMap((s) => s.cues);
  fs.writeFileSync(path.join(outDir, `${base}.srt`), toSrt(cues));
  fs.writeFileSync(path.join(outDir, `${base}.vtt`), toVtt(cues));
  console.log(`manifest: ${scenes.length} scenes, ${manifest.totalSeconds.toFixed(2)} s`);
  console.log(`voice characters sent now (not from the cache): ${newChars}`);

  console.log("bundling (once)");
  const serveUrl = await bundle({
    entryPoint: path.join(ROOT, "src", "remotion", "index.ts"),
    publicDir,
  });

  for (const format of sc.formats as FrameFormat[]) {
    const { width, height } = FORMATS[format];
    const inputProps: VideoProps = {
      manifest,
      format,
      width,
      height,
      durationInFrames: manifest.totalFrames,
      fps: sc.fps,
    };
    const composition = await selectComposition({ serveUrl, id: "Video", inputProps });
    const out = path.join(outDir, `${base}_${format}.mp4`);
    let lastPct = -1;
    await renderMedia({
      composition,
      serveUrl,
      codec: "h264",
      audioCodec: "aac",
      outputLocation: out,
      inputProps,
      onProgress: ({ progress }) => {
        const pct = Math.floor(progress * 10) * 10;
        if (pct !== lastPct) {
          lastPct = pct;
          console.log(`  ${format} ${pct}%`);
        }
      },
    });
    console.log(`written ${path.relative(ROOT, out)}`);
  }

  fs.rmSync(workDir, { recursive: true, force: true });
  console.log(`done: ${path.relative(ROOT, outDir)} (voice: ${provider.name}, new characters: ${newChars})`);
}

main().catch((e: unknown) => {
  console.error(e instanceof Error ? e.message : String(e));
  process.exit(1);
});
