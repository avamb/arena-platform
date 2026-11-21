// Types shared by the build (node) and the Remotion composition (browser).
// No node imports here.

export type FrameFormat = "16x9" | "9x16" | "4x5";

export const FORMATS: Record<FrameFormat, { width: number; height: number }> = {
  "16x9": { width: 1920, height: 1080 },
  "9x16": { width: 1080, height: 1920 },
  "4x5": { width: 1080, height: 1350 },
};

export interface ZoomSpec {
  from: number;
  to: number;
  x: number;
  y: number;
}

export interface HighlightSpec {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface CursorPoint {
  at: number;
  x: number;
  y: number;
  click?: boolean;
}

export interface Cue {
  /** seconds from the start of the whole video */
  start: number;
  end: number;
  text: string;
}

export type FrameKind = "phone" | "browser" | "none";
export type GraphicKind = "flow" | "cta" | "placeholder";

export interface ScrollSpec {
  from: number;
  to: number;
}

/** One picture or graphic inside a scene; times are frames inside the scene. */
export interface ManifestShot {
  startFrame: number;
  frames: number;
  frame: FrameKind;
  title?: string;
  /** file name inside the Remotion public dir (a picture shot) */
  image?: string;
  imageWidth?: number;
  imageHeight?: number;
  clip?: string;
  zoom?: ZoomSpec;
  highlight?: HighlightSpec;
  cursor?: CursorPoint[];
  scroll?: ScrollSpec;
  /** a graphic shot, drawn by the edit */
  graphic?: GraphicKind;
  steps?: string[];
  caption?: string;
  heading?: string;
  lines?: string[];
  /** PNG data URL made at build time */
  qr?: string;
  logo?: string;
  logoWidth?: number;
  logoHeight?: number;
}

export interface ManifestScene {
  id: string;
  startFrame: number;
  frames: number;
  startSec: number;
  durationSec: number;
  voiceSec: number;
  pauseAfterSec: number;
  narration: string;
  /** characters sent to the voice provider for this scene (0 for the silent stub) */
  chars: number;
  /** true if the sound came from the cache and nothing was sent */
  cached: boolean;
  /** file name inside the Remotion public dir */
  audio: string;
  shots: ManifestShot[];
  cues: Cue[];
}

export interface Manifest {
  scenario: string;
  language: string;
  provider: string;
  fps: number;
  totalFrames: number;
  totalSeconds: number;
  background: string;
  formats: FrameFormat[];
  scenes: ManifestScene[];
}

export interface VideoProps extends Record<string, unknown> {
  manifest: Manifest;
  format: FrameFormat;
  width: number;
  height: number;
  durationInFrames: number;
  fps: number;
}
