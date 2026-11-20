import React from "react";
import { AbsoluteFill, Audio, Img, Sequence, interpolate, staticFile, useCurrentFrame, useVideoConfig } from "remotion";
import type { Cue, FrameFormat, ManifestScene, ManifestShot, VideoProps } from "../types.ts";
import { BrowserFrame } from "./BrowserFrame.tsx";
import { Graphic } from "./Graphics.tsx";
import { BROWSER_BAR, FONT, browserBox, phone169, phoneVertical, subtitleReserve, titleReserve, type Box } from "./layout.ts";
import { PhoneFrame } from "./PhoneFrame.tsx";
import { Picture, easeInOut, zoomScale } from "./Picture.tsx";

const FADE_FRAMES = 10;
const clamp01 = (v: number) => Math.min(1, Math.max(0, v));

/** Rectangle (in frame pixels) of a picture fitted whole into the frame. */
function containRect(frameW: number, frameH: number, imgW: number, imgH: number) {
  const k = Math.min(frameW / imgW, frameH / imgH);
  const w = imgW * k;
  const h = imgH * k;
  return { left: (frameW - w) / 2, top: (frameH - h) / 2, w, h };
}

const Title: React.FC<{ text: string; style: React.CSSProperties; fontSize: number; maxWidth: number; local: number; fadeOutAt?: number }> = ({
  text,
  style,
  fontSize,
  maxWidth,
  local,
  fadeOutAt,
}) => {
  const clampOpts = { extrapolateLeft: "clamp", extrapolateRight: "clamp" } as const;
  const opacity =
    fadeOutAt === undefined
      ? interpolate(local, [4, 16], [0, 1], clampOpts)
      : interpolate(local, [4, 16, fadeOutAt, fadeOutAt + 12], [0, 1, 1, 0], clampOpts);
  return (
    <div
      style={{
        position: "absolute",
        display: "flex",
        opacity,
        transform: `translateY(${interpolate(local, [4, 16], [-20, 0], clampOpts)}px)`,
        ...style,
      }}
    >
      <div
        style={{
          fontFamily: FONT,
          fontWeight: 700,
          fontSize,
          lineHeight: 1.2,
          color: "#fff",
          background: "rgba(20,30,70,0.82)",
          padding: "14px 36px",
          borderRadius: 18,
          maxWidth,
          textAlign: "center",
        }}
      >
        {text}
      </div>
    </div>
  );
};

const ShotView: React.FC<{
  shot: ManifestShot;
  format: FrameFormat;
  width: number;
  height: number;
  background: string;
  lead: number;
}> = ({ shot, format, width, height, background, lead }) => {
  const frame = useCurrentFrame(); // local to the Sequence, which starts `lead` frames early
  const local = frame - lead; // 0 at the real start of the shot
  const progress = clamp01(local / Math.max(1, shot.frames));
  const opacity = lead > 0 ? interpolate(frame, [0, lead], [0, 1], { extrapolateRight: "clamp" }) : 1;
  const vertical = format !== "16x9";
  const common = { zoom: shot.zoom, highlight: shot.highlight, cursor: shot.cursor, progress, local, shotFrames: shot.frames };
  const scrollAt = (hidden: number) => {
    const s = shot.scroll ?? { from: 0, to: 0 };
    return -hidden * (s.from + (s.to - s.from) * easeInOut(progress));
  };

  let bg: React.ReactNode = <AbsoluteFill style={{ backgroundColor: background }} />;
  let body: React.ReactNode = null;

  if (shot.graphic) {
    body = <Graphic shot={shot} format={format} width={width} height={height} local={local} />;
  } else if (shot.image && shot.imageWidth && shot.imageHeight) {
    const iw = shot.imageWidth;
    const ih = shot.imageHeight;
    const hasTitle = Boolean(shot.title);
    if (shot.frame === "phone") {
      const p169 = phone169(width, height);
      const pv = phoneVertical(format, width, height, hasTitle);
      const bodyBox = vertical ? pv.body : p169.body;
      body = (
        <>
          <PhoneFrame body={bodyBox}>
            {(s: Box) => {
              const pw = s.w;
              const ph = (pw * ih) / iw;
              const hidden = ph - s.h;
              const top = hidden > 0 ? scrollAt(hidden) : (s.h - ph) / 2;
              // vertical: the zoom point goes to the middle of the part of the screen that is above the subtitles
              const view = vertical
                ? { w: s.w, h: s.h, cx: s.w / 2, cy: (bodyBox.y + s.y + pv.visBottom) / 2 - bodyBox.y - s.y }
                : undefined;
              return <Picture image={shot.image!} clip={shot.clip} left={0} top={top} w={pw} h={ph} view={view} {...common} />;
            }}
          </PhoneFrame>
          {shot.title ? (
            vertical ? (
              <Title
                text={shot.title}
                local={local}
                fontSize={56}
                maxWidth={width * 0.88}
                style={{ top: height * 0.025, left: 0, right: 0, justifyContent: "center" }}
              />
            ) : (
              <Title
                text={shot.title}
                local={local}
                fontSize={60}
                maxWidth={p169.colW}
                style={{ left: p169.colX, top: height * 0.12, justifyContent: "flex-start" }}
              />
            )
          ) : null}
        </>
      );
    } else if (shot.frame === "browser") {
      let box: Box;
      let barH: number;
      if (!vertical) {
        // almost the whole frame; the title lies on the top right corner, the subtitles on the lower part
        const r = browserBox({ x: width * 0.03, y: height * 0.03, w: width * 0.94, h: height * 0.94 }, iw, ih);
        box = r.box;
        barH = r.barH;
      } else {
        const cw = width * 0.94;
        const ph0 = (cw * ih) / iw;
        barH = cw * BROWSER_BAR;
        const top0 = titleReserve(format, height, hasTitle);
        const areaH = height - top0 - subtitleReserve(format, height);
        let ch: number;
        let y: number;
        if (shot.zoom) {
          // the window grows with the zoom (up to about 60 % of the frame) and keeps its top edge
          const chMax = Math.min(height * 0.6, areaH) - barH;
          const s = Math.max(1, zoomScale(shot.zoom, progress));
          ch = Math.min(Math.max(ph0, ph0 * s), Math.max(chMax, Math.min(ph0, chMax)));
          y = top0 + (areaH - chMax - barH) / 2;
        } else {
          ch = Math.min(ph0, areaH - barH);
          y = top0 + (areaH - ch - barH) / 2;
        }
        box = { x: width * 0.03, y, w: cw, h: ch + barH };
      }
      body = (
        <>
          <BrowserFrame box={box} barH={barH} address={shot.address}>
            {(c: Box) => {
              const ph = vertical ? (c.w * ih) / iw : c.h;
              const view = vertical ? { w: c.w, h: c.h, cx: c.w / 2, cy: c.h / 2 } : undefined;
              return (
                <Picture image={shot.image!} clip={shot.clip} left={0} top={0} w={c.w} h={ph} view={view} {...common} />
              );
            }}
          </BrowserFrame>
          {shot.title ? (
            vertical ? (
              <Title
                text={shot.title}
                local={local}
                fontSize={56}
                maxWidth={width * 0.88}
                style={{ top: height * 0.025, left: 0, right: 0, justifyContent: "center" }}
              />
            ) : (
              <Title
                text={shot.title}
                local={local}
                fontSize={54}
                maxWidth={width * 0.45}
                style={{ top: height * 0.045, right: width * 0.045, justifyContent: "flex-end" }}
              />
            )
          ) : null}
        </>
      );
    } else {
      // no frame: the picture whole (or scrolled), on a blurred copy of itself
      const src = staticFile(shot.image);
      bg = (
        <AbsoluteFill style={{ backgroundColor: "#0b1020" }}>
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
        </AbsoluteFill>
      );
      let rect = containRect(width, height, iw, ih);
      if (shot.scroll) {
        const ph = (width * ih) / iw;
        rect = { left: 0, top: ph > height ? scrollAt(ph - height) : (height - ph) / 2, w: width, h: ph };
      }
      body = (
        <>
          <div style={{ position: "absolute", inset: 0, overflow: "hidden" }}>
            <Picture image={shot.image} clip={shot.clip} left={rect.left} top={rect.top} w={rect.w} h={rect.h} shadow {...common} />
          </div>
          {shot.title ? (
            <Title
              text={shot.title}
              local={local}
              fontSize={vertical ? 64 : 56}
              maxWidth={width * 0.88}
              // In 16:9 the title lies on the picture, so it goes away after about 2 seconds.
              fadeOutAt={vertical ? undefined : 64}
              style={{ top: vertical ? height * 0.06 : height * 0.05, left: 0, right: 0, justifyContent: "center" }}
            />
          ) : null}
        </>
      );
    }
  }

  return (
    <AbsoluteFill style={{ opacity, overflow: "hidden" }}>
      {bg}
      {body}
    </AbsoluteFill>
  );
};

const SceneView: React.FC<{
  scene: ManifestScene;
  format: FrameFormat;
  width: number;
  height: number;
  background: string;
  fadeIn: boolean;
}> = ({ scene, format, width, height, background, fadeIn }) => {
  const frame = useCurrentFrame();
  const lead = fadeIn ? FADE_FRAMES : 0;
  const opacity = fadeIn ? interpolate(frame, [0, FADE_FRAMES], [0, 1], { extrapolateRight: "clamp" }) : 1;
  return (
    <AbsoluteFill style={{ opacity, backgroundColor: "#0b1020", overflow: "hidden" }}>
      {scene.shots.map((shot, j) => {
        const shotLead = j > 0 ? FADE_FRAMES : 0;
        return (
          <Sequence key={j} from={lead + shot.startFrame - shotLead} durationInFrames={shot.frames + shotLead} layout="none">
            <ShotView shot={{ ...shot, ...(shot.perFormat?.[format] ?? {}) }} format={format} width={width} height={height} background={background} lead={shotLead} />
          </Sequence>
        );
      })}
    </AbsoluteFill>
  );
};

const Subtitles: React.FC<{
  cues: Cue[];
  format: FrameFormat;
  width: number;
  height: number;
  sec: number;
  column?: { x: number; w: number };
}> = ({ cues, format, width, height, sec, column }) => {
  const cue = cues.find((c) => sec >= c.start && sec < c.end);
  if (!cue) return null;
  const phone = format !== "16x9";
  return (
    <div
      style={{
        position: "absolute",
        left: column ? column.x : 0,
        ...(column ? { width: column.w } : { right: 0 }),
        bottom: phone ? height * 0.07 : height * 0.06,
        display: "flex",
        justifyContent: column ? "flex-start" : "center",
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
          maxWidth: column ? column.w : width * 0.92,
          textAlign: column ? "left" : "center",
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
  // In 16:9 the subtitles of a phone shot stand in the right column, beside the phone.
  const current = manifest.scenes
    .flatMap((sc) => sc.shots.map((sh) => ({ sh: { ...sh, ...(sh.perFormat?.[format] ?? {}) }, start: sc.startFrame + sh.startFrame })))
    .filter((x) => x.start <= frame)
    .pop();
  const p169 = phone169(width, height);
  const column =
    format === "16x9" && current && current.sh.frame === "phone" && !current.sh.graphic
      ? { x: p169.colX, w: p169.colW }
      : undefined;
  return (
    <AbsoluteFill style={{ backgroundColor: "#0b1020" }}>
      {manifest.scenes.map((scene, i) => {
        const fadeIn = i > 0;
        const from = scene.startFrame - (fadeIn ? FADE_FRAMES : 0);
        return (
          <Sequence key={scene.id} from={from} durationInFrames={scene.frames + (fadeIn ? FADE_FRAMES : 0)} layout="none">
            <SceneView
              scene={scene}
              format={format}
              width={width}
              height={height}
              background={manifest.background}
              fadeIn={fadeIn}
            />
          </Sequence>
        );
      })}
      {manifest.scenes.map((scene) => (
        <Sequence key={`a-${scene.id}`} from={scene.startFrame} durationInFrames={scene.frames} layout="none">
          <Audio src={staticFile(scene.audio)} />
        </Sequence>
      ))}
      <Subtitles cues={allCues} format={format} width={width} height={height} sec={frame / fps} column={column} />
    </AbsoluteFill>
  );
};
