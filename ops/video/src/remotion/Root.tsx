import React from "react";
import { Composition } from "remotion";
import type { Manifest, VideoProps } from "../types.ts";
import { Video } from "./Video.tsx";

export const COMPOSITION_ID = "Video";

const emptyManifest: Manifest = {
  scenario: "",
  language: "",
  provider: "silent",
  fps: 30,
  totalFrames: 30,
  totalSeconds: 1,
  scenes: [],
};

const defaultProps: VideoProps = {
  manifest: emptyManifest,
  format: "16x9",
  width: 1920,
  height: 1080,
  durationInFrames: 30,
  fps: 30,
};

export const RemotionRoot: React.FC = () => (
  <Composition
    id={COMPOSITION_ID}
    component={Video}
    width={defaultProps.width}
    height={defaultProps.height}
    fps={defaultProps.fps}
    durationInFrames={defaultProps.durationInFrames}
    defaultProps={defaultProps}
    calculateMetadata={({ props }) => ({
      width: props.width,
      height: props.height,
      fps: props.fps,
      durationInFrames: props.durationInFrames,
    })}
  />
);
