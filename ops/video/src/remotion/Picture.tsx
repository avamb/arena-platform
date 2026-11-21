import React from "react";
import { Img, OffthreadVideo, interpolate, staticFile } from "remotion";
import type { CursorPoint, HighlightSpec, ZoomSpec } from "../types.ts";
import { Cursor } from "./Cursor.tsx";

export const easeInOut = (t: number) => (t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2);

export const zoomScale = (zoom: ZoomSpec | undefined, progress: number) =>
  zoom ? zoom.from + (zoom.to - zoom.from) * easeInOut(progress) : 1;

export const Highlight: React.FC<{ spec: HighlightSpec; local: number; scaleWidth: number }> = ({ spec, local, scaleWidth }) => {
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

/**
 * The picture at a pixel size, placed at (left, top) of its clipping parent,
 * with zoom, highlight and cursor in the coordinates of the picture.
 */
export const Picture: React.FC<{
  image: string;
  clip?: string;
  left: number;
  top: number;
  w: number;
  h: number;
  zoom?: ZoomSpec;
  highlight?: HighlightSpec;
  cursor?: CursorPoint[];
  progress: number;
  local: number;
  shotFrames: number;
  shadow?: boolean;
  /** Visible part (coordinates of the clipping parent) and the point where the zoom point should go while zooming. */
  view?: { w: number; h: number; cx: number; cy: number };
}> = ({ image, clip, left, top, w, h, zoom, highlight, cursor, progress, local, shotFrames, shadow, view }) => {
  const scale = zoomScale(zoom, progress);
  const origin = zoom ? `${zoom.x * 100}% ${zoom.y * 100}%` : "50% 50%";
  let dx = 0;
  let dy = 0;
  if (zoom && view) {
    const maxS = Math.max(zoom.from, zoom.to);
    const f = maxS > 1 ? Math.min(1, Math.max(0, (scale - 1) / (maxS - 1))) : 0;
    const px = zoom.x * w;
    const py = zoom.y * h;
    const fit = (d0: number, p: number, l: number, size: number, vis: number) => {
      const lo = vis - l - p - scale * (size - p); // the far edge stays covered
      const hi = scale * p - l - p; // the near edge stays covered
      const d = d0 * f;
      return lo <= hi ? Math.min(hi, Math.max(lo, d)) : d;
    };
    dx = fit(view.cx - (left + px), px, left, w, view.w);
    dy = fit(view.cy - (top + py), py, top, h, view.h);
  }
  return (
    <div
      style={{
        position: "absolute",
        left,
        top,
        width: w,
        height: h,
        transform: `translate(${dx}px, ${dy}px) scale(${scale})`,
        transformOrigin: origin,
        boxShadow: shadow ? "0 12px 60px rgba(0,0,0,0.5)" : undefined,
      }}
    >
      {clip ? (
        <OffthreadVideo src={staticFile(clip)} muted style={{ width: "100%", height: "100%", objectFit: "fill" }} />
      ) : (
        <Img src={staticFile(image)} style={{ width: "100%", height: "100%", display: "block" }} />
      )}
      {highlight ? <Highlight spec={highlight} local={local} scaleWidth={w} /> : null}
      {cursor && cursor.length > 0 ? (
        <Cursor points={cursor} progress={progress} local={local} rectW={w} sceneFrames={shotFrames} />
      ) : null}
    </div>
  );
};
