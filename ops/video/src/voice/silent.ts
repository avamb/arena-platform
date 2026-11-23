import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import type { VoiceProvider, VoiceResult } from "./index.ts";

const RATE = 22050;
const WORDS_PER_SEC = 2.5;
const MIN_SEC = 1.5;

export function estimateSeconds(text: string): number {
  const words = text.trim().split(/\s+/).filter(Boolean).length;
  return Math.max(MIN_SEC, words / WORDS_PER_SEC);
}

/** 16-bit mono PCM WAV of silence. */
export function silentWav(seconds: number): Buffer {
  const samples = Math.round(seconds * RATE);
  const dataBytes = samples * 2;
  const buf = Buffer.alloc(44 + dataBytes);
  buf.write("RIFF", 0);
  buf.writeUInt32LE(36 + dataBytes, 4);
  buf.write("WAVE", 8);
  buf.write("fmt ", 12);
  buf.writeUInt32LE(16, 16);
  buf.writeUInt16LE(1, 20);
  buf.writeUInt16LE(1, 22);
  buf.writeUInt32LE(RATE, 24);
  buf.writeUInt32LE(RATE * 2, 28);
  buf.writeUInt16LE(2, 32);
  buf.writeUInt16LE(16, 34);
  buf.write("data", 36);
  buf.writeUInt32LE(dataBytes, 40);
  return buf;
}

export class SilentProvider implements VoiceProvider {
  readonly name = "silent" as const;
  constructor(private readonly dir: string) {}

  async synthesize(text: string, _lang: string): Promise<VoiceResult> {
    const seconds = estimateSeconds(text);
    const hash = crypto.createHash("sha256").update(text).digest("hex").slice(0, 16);
    fs.mkdirSync(this.dir, { recursive: true });
    const file = path.join(this.dir, `${hash}.wav`);
    fs.writeFileSync(file, silentWav(seconds));
    return { file, durationSec: Math.round(seconds * RATE) / RATE, chars: 0, cached: false };
  }
}
