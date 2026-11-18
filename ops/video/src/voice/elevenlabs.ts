import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { parseFile } from "music-metadata";
import type { VoiceProvider, VoiceResult } from "./index.ts";

export interface ElevenLabsOptions {
  apiKey: string;
  voiceId: string;
  modelId: string;
  cacheDir: string;
}

export class ElevenLabsProvider implements VoiceProvider {
  readonly name = "elevenlabs" as const;
  constructor(private readonly opts: ElevenLabsOptions) {}

  async synthesize(text: string, _lang: string): Promise<VoiceResult> {
    const { apiKey, voiceId, modelId, cacheDir } = this.opts;
    const hash = crypto.createHash("sha256").update(JSON.stringify([text, voiceId, modelId])).digest("hex").slice(0, 32);
    const file = path.join(cacheDir, `${hash}.mp3`);
    if (!fs.existsSync(file)) {
      fs.mkdirSync(cacheDir, { recursive: true });
      const url = `https://api.elevenlabs.io/v1/text-to-speech/${encodeURIComponent(voiceId)}?output_format=mp3_44100_128`;
      const res = await fetch(url, {
        method: "POST",
        headers: { "xi-api-key": apiKey, "Content-Type": "application/json", Accept: "audio/mpeg" },
        body: JSON.stringify({ text, model_id: modelId }),
      });
      if (!res.ok) {
        // The body of an error answer never contains the key, but keep it short anyway.
        const body = (await res.text()).slice(0, 300);
        throw new Error(`elevenlabs: HTTP ${res.status} ${body}`);
      }
      const tmp = `${file}.part`;
      fs.writeFileSync(tmp, Buffer.from(await res.arrayBuffer()));
      fs.renameSync(tmp, file);
    }
    const meta = await parseFile(file, { duration: true });
    const durationSec = meta.format.duration;
    if (!durationSec || durationSec <= 0) throw new Error(`elevenlabs: cannot read length of ${file}`);
    return { file, durationSec };
  }
}
