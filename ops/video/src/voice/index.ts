import path from "node:path";
import { ROOT } from "../env.ts";
import type { Scenario } from "../scenario.ts";
import { ElevenLabsProvider } from "./elevenlabs.ts";
import { SilentProvider } from "./silent.ts";

export interface VoiceResult {
  /** absolute path of the audio file */
  file: string;
  durationSec: number;
}

export interface VoiceProvider {
  readonly name: "elevenlabs" | "silent";
  synthesize(text: string, lang: string): Promise<VoiceResult>;
}

export function createVoiceProvider(voice: Scenario["voice"], silentDir: string): VoiceProvider {
  const hasKey = Boolean(process.env.ELEVENLABS_API_KEY);
  const wanted = voice.provider === "auto" ? (hasKey ? "elevenlabs" : "silent") : voice.provider;
  if (wanted === "silent") return new SilentProvider(silentDir);
  const apiKey = process.env.ELEVENLABS_API_KEY;
  const voiceId = process.env[voice.elevenlabs.voice_id_env];
  if (!apiKey) throw new Error("voice: provider elevenlabs needs ELEVENLABS_API_KEY");
  if (!voiceId) throw new Error(`voice: provider elevenlabs needs ${voice.elevenlabs.voice_id_env}`);
  return new ElevenLabsProvider({
    apiKey,
    voiceId,
    modelId: voice.elevenlabs.model_id,
    cacheDir: path.join(ROOT, ".cache", "voice"),
  });
}
