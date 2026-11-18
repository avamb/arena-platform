import fs from "node:fs";
import path from "node:path";
import { bundle } from "@remotion/bundler";
import { renderMedia, selectComposition } from "@remotion/renderer";
import { loadEnv, ROOT } from "./env.ts";
import { loadScenario } from "./scenario.ts";
import { timeCues, toSrt, toVtt } from "./subtitles.ts";
import { FORMATS, type FrameFormat, type Manifest, type ManifestScene, type VideoProps } from "./types.ts";
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

async function main(): Promise<void> {
  loadEnv();
  const [id, lang] = process.argv.slice(2);
  if (!id || !lang) throw new Error("usage: npm run build:video -- <scenario> <language>");
  const sc = loadScenario(id);
  if (!sc.languages.includes(lang)) throw new Error(`language ${lang} is not in the scenario (${sc.languages.join(", ")})`);

  const outDir = path.join(ROOT, "out", id, lang);
  const workDir = path.join(outDir, ".work");
  const publicDir = path.join(workDir, "public");
  fs.rmSync(workDir, { recursive: true, force: true });
  fs.mkdirSync(publicDir, { recursive: true });

  const provider = createVoiceProvider(sc.voice, path.join(workDir, "silent"));
  console.log(`voice provider: ${provider.name}`);

  const scenes: ManifestScene[] = [];
  let startFrame = 0;
  for (const s of sc.scenes) {
    const text = s.narration[lang]!;
    const voice = await provider.synthesize(text, lang);
    const pause = s.pause_after ?? 0.3;
    const frames = Math.max(1, Math.round((voice.durationSec + pause) * sc.fps));

    const capDir = path.join(sc.dir, "captures");
    const imgPath = s.source.image ? path.join(sc.dir, s.source.image) : path.join(capDir, `${s.id}.png`);
    if (!fs.existsSync(imgPath)) throw new Error(`scene ${s.id}: picture ${imgPath} not found, run capture first`);
    const size = pngSize(imgPath);
    const image = copyInto(publicDir, `img/${s.id}.png`, imgPath);
    let clip: string | undefined;
    if (s.source.use === "clip") {
      const clipPath = path.join(capDir, `${s.id}.webm`);
      if (!fs.existsSync(clipPath)) throw new Error(`scene ${s.id}: clip ${clipPath} not found, run capture first`);
      clip = copyInto(publicDir, `clip/${s.id}.webm`, clipPath);
    }
    const audio = copyInto(publicDir, `audio/${s.id}${path.extname(voice.file)}`, voice.file);

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
      title: s.title?.[lang],
      image,
      imageWidth: size.w,
      imageHeight: size.h,
      clip,
      audio,
      zoom: s.zoom,
      highlight: s.highlight,
      cursor: s.cursor,
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
    scenes,
  };
  const base = `${id}_${lang}`;
  fs.writeFileSync(path.join(outDir, "manifest.json"), JSON.stringify(manifest, null, 2) + "\n");
  const cues = scenes.flatMap((s) => s.cues);
  fs.writeFileSync(path.join(outDir, `${base}.srt`), toSrt(cues));
  fs.writeFileSync(path.join(outDir, `${base}.vtt`), toVtt(cues));
  console.log(`manifest: ${scenes.length} scenes, ${manifest.totalSeconds.toFixed(2)} s`);

  console.log("bundling (once)");
  const serveUrl = await bundle({
    entryPoint: path.join(ROOT, "src", "remotion", "index.ts"),
    publicDir,
  });

  for (const format of Object.keys(FORMATS) as FrameFormat[]) {
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
  console.log(`done: ${path.relative(ROOT, outDir)} (voice: ${provider.name})`);
}

main().catch((e: unknown) => {
  console.error(e instanceof Error ? e.message : String(e));
  process.exit(1);
});
