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

export interface ManifestScene {
  id: string;
  startFrame: number;
  frames: number;
  startSec: number;
  durationSec: number;
  voiceSec: number;
  pauseAfterSec: number;
  narration: string;
  title?: string;
  /** file names inside the Remotion public dir */
  image: string;
  imageWidth: number;
  imageHeight: number;
  clip?: string;
  audio: string;
  zoom?: ZoomSpec;
  highlight?: HighlightSpec;
  cursor?: CursorPoint[];
  cues: Cue[];
}

export interface Manifest {
  scenario: string;
  language: string;
  provider: string;
  fps: number;
  totalFrames: number;
  totalSeconds: number;
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
