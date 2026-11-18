import React from "react";
import { AbsoluteFill, Audio, Img, OffthreadVideo, Sequence, interpolate, staticFile, useCurrentFrame, useVideoConfig } from "remotion";
import type { Cue, FrameFormat, ManifestScene, VideoProps } from "../types.ts";
import { Cursor } from "./Cursor.tsx";

const FONT = '"Segoe UI", Arial, sans-serif';
const FADE_FRAMES = 10;

/** Rectangle (in frame pixels) of a picture fitted whole into the frame. */
function containRect(frameW: number, frameH: number, imgW: number, imgH: number) {
  const k = Math.min(frameW / imgW, frameH / imgH);
  const w = imgW * k;
  const h = imgH * k;
  return { left: (frameW - w) / 2, top: (frameH - h) / 2, w, h };
}

const easeInOut = (t: number) => (t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2);

const SceneView: React.FC<{
  scene: ManifestScene;
  format: FrameFormat;
  width: number;
  height: number;
  sceneFrames: number;
  fadeIn: boolean;
}> = ({ scene, format, width, height, sceneFrames, fadeIn }) => {
  const frame = useCurrentFrame(); // local to the Sequence, which starts FADE_FRAMES early when fadeIn
  const lead = fadeIn ? FADE_FRAMES : 0;
  const local = frame - lead; // 0 at the real start of the scene
  const progress = Math.min(1, Math.max(0, local / Math.max(1, sceneFrames)));
  const opacity = fadeIn ? interpolate(frame, [0, FADE_FRAMES], [0, 1], { extrapolateRight: "clamp" }) : 1;

  const rect = containRect(width, height, scene.imageWidth, scene.imageHeight);
  const z = scene.zoom;
  const scale = z ? z.from + (z.to - z.from) * easeInOut(progress) : 1;
  const origin = z ? `${z.x * 100}% ${z.y * 100}%` : "50% 50%";
  const src = staticFile(scene.image);
  const isPhone = format !== "16x9";

  return (
    <AbsoluteFill style={{ opacity, backgroundColor: "#0b1020", overflow: "hidden" }}>
      {/* blurred stretched copy fills the whole frame */}
      <Img
        src={src}
        style={{
          position: "absolute",
          inset: 0,
          width: "100%",
          height: "100%",
          objectFit: "cover",
          filter: "blur(36px) brightness(0.55)",
          transform: "scale(1.15)",
        }}
      />
      {/* the picture, whole, with zoom, highlight and cursor in its own coordinates */}
      <div
        style={{
          position: "absolute",
          left: rect.left,
          top: rect.top,
          width: rect.w,
          height: rect.h,
          transform: `scale(${scale})`,
          transformOrigin: origin,
          boxShadow: "0 12px 60px rgba(0,0,0,0.5)",
        }}
      >
        {scene.clip ? (
          <OffthreadVideo src={staticFile(scene.clip)} muted style={{ width: "100%", height: "100%", objectFit: "fill" }} />
        ) : (
          <Img src={src} style={{ width: "100%", height: "100%", display: "block" }} />
        )}
        {scene.highlight ? <Highlight spec={scene.highlight} local={local} scaleWidth={rect.w} /> : null}
        {scene.cursor && scene.cursor.length > 0 ? (
          <Cursor points={scene.cursor} progress={progress} local={local} rectW={rect.w} sceneFrames={sceneFrames} />
        ) : null}
      </div>
      {scene.title ? (
        <div
          style={{
            position: "absolute",
            top: isPhone ? height * 0.06 : height * 0.05,
            left: 0,
            right: 0,
            display: "flex",
            justifyContent: "center",
            // In 16:9 the title lies on the picture, so it goes away after about 2 seconds.
            opacity: interpolate(local, isPhone ? [4, 16] : [4, 16, 64, 76], isPhone ? [0, 1] : [0, 1, 1, 0], { extrapolateLeft: "clamp", extrapolateRight: "clamp" }),
            transform: `translateY(${interpolate(local, [4, 16], [-20, 0], { extrapolateLeft: "clamp", extrapolateRight: "clamp" })}px)`,
          }}
        >
          <div
            style={{
              fontFamily: FONT,
              fontWeight: 700,
              fontSize: isPhone ? 64 : 56,
              color: "#fff",
              background: "rgba(20,30,70,0.82)",
              padding: "14px 36px",
              borderRadius: 18,
              maxWidth: width * 0.88,
              textAlign: "center",
            }}
          >
            {scene.title}
          </div>
        </div>
      ) : null}
    </AbsoluteFill>
  );
};

const Highlight: React.FC<{ spec: NonNullable<ManifestScene["highlight"]>; local: number; scaleWidth: number }> = ({
  spec,
  local,
  scaleWidth,
}) => {
  const appear = interpolate(local, [6, 18], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  const pulse = 1 + 0.015 * Math.sin(local / 6);
  const border = Math.max(3, scaleWidth * 0.0035);
  return (
    <div
      style={{
        position: "absolute",
        left: `${spec.x * 100}%`,
        top: `${spec.y * 100}%`,
        width: `${spec.w * 100}%`,
        height: `${spec.h * 100}%`,
        border: `${border}px solid #ffb400`,
        borderRadius: 10,
        boxShadow: "0 0 0 9999px rgba(0,0,0,0.0), 0 0 24px rgba(255,180,0,0.7)",
        opacity: appear,
        transform: `scale(${pulse})`,
        pointerEvents: "none",
      }}
    />
  );
};

const Subtitles: React.FC<{ cues: Cue[]; format: FrameFormat; width: number; height: number; sec: number }> = ({
  cues,
  format,
  width,
  height,
  sec,
}) => {
  const cue = cues.find((c) => sec >= c.start && sec < c.end);
  if (!cue) return null;
  const phone = format !== "16x9";
  return (
    <div
      style={{
        position: "absolute",
        left: 0,
        right: 0,
        bottom: phone ? height * 0.07 : height * 0.06,
        display: "flex",
        justifyContent: "center",
      }}
    >
      <div
        style={{
          fontFamily: FONT,
          fontWeight: 600,
          fontSize: phone ? 46 : 44,
          lineHeight: 1.25,
          color: "#fff",
          background: "rgba(0,0,0,0.72)",
          padding: "10px 26px",
          borderRadius: 12,
          maxWidth: width * 0.92,
          textAlign: "center",
          whiteSpace: "pre-line",
        }}
      >
        {phone ? cue.text.replace(/\n/g, " ") : cue.text}
      </div>
    </div>
  );
};

export const Video: React.FC<VideoProps> = ({ manifest, format, width, height }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const allCues = React.useMemo(() => manifest.scenes.flatMap((s) => s.cues), [manifest]);
  return (
    <AbsoluteFill style={{ backgroundColor: "#0b1020" }}>
      {manifest.scenes.map((scene, i) => {
        const fadeIn = i > 0;
        const from = scene.startFrame - (fadeIn ? FADE_FRAMES : 0);
        return (
          <Sequence key={scene.id} from={from} durationInFrames={scene.frames + (fadeIn ? FADE_FRAMES : 0)} layout="none">
            <AbsoluteFill>
              <SceneView
                scene={scene}
                format={format}
                width={width}
                height={height}
                sceneFrames={scene.frames}
                fadeIn={fadeIn}
              />
            </AbsoluteFill>
          </Sequence>
        );
      })}
      {manifest.scenes.map((scene) => (
        <Sequence key={`a-${scene.id}`} from={scene.startFrame} durationInFrames={scene.frames} layout="none">
          <Audio src={staticFile(scene.audio)} />
        </Sequence>
      ))}
      <Subtitles cues={allCues} format={format} width={width} height={height} sec={frame / fps} />
    </AbsoluteFill>
  );
};
