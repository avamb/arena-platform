import React from "react";
import { interpolate } from "remotion";
import type { CursorPoint } from "../types.ts";

const ease = (t: number) => (t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2);

/** Drawn cursor that moves along points given in fractions of the scene time. */
export const Cursor: React.FC<{
  points: CursorPoint[];
  progress: number;
  local: number;
  rectW: number;
  sceneFrames: number;
}> = ({ points, progress, local, rectW, sceneFrames }) => {
  const first = points[0]!;
  const last = points[points.length - 1]!;
  let x = first.x;
  let y = first.y;
  if (progress >= last.at) {
    x = last.x;
    y = last.y;
  } else if (progress > first.at) {
    for (let i = 1; i < points.length; i++) {
      const a = points[i - 1]!;
      const b = points[i]!;
      if (progress <= b.at) {
        const t = b.at === a.at ? 1 : ease((progress - a.at) / (b.at - a.at));
        x = a.x + (b.x - a.x) * t;
        y = a.y + (b.y - a.y) * t;
        break;
      }
    }
  }
  const startFrame = first.at * sceneFrames;
  const opacity = interpolate(local, [startFrame - 6, startFrame + 4], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
  const size = Math.max(28, rectW * 0.028);

  // click ripples
  const ripples = points
    .filter((p) => p.click)
    .map((p, i) => {
      const f = (local - p.at * sceneFrames) / 14;
      if (f < 0 || f > 1) return null;
      return (
        <div
          key={i}
          style={{
            position: "absolute",
            left: `${p.x * 100}%`,
            top: `${p.y * 100}%`,
            width: size * 2.4 * f + size * 0.4,
            height: size * 2.4 * f + size * 0.4,
            marginLeft: -(size * 1.2 * f + size * 0.2),
            marginTop: -(size * 1.2 * f + size * 0.2),
            borderRadius: "50%",
            border: `${Math.max(3, size * 0.1)}px solid rgba(255,180,0,${1 - f})`,
          }}
        />
      );
    });

  return (
    <>
      {ripples}
      <svg
        width={size}
        height={size * 1.45}
        viewBox="0 0 20 29"
        style={{ position: "absolute", left: `${x * 100}%`, top: `${y * 100}%`, opacity, filter: "drop-shadow(2px 4px 4px rgba(0,0,0,0.5))" }}
      >
        <path d="M1 1 L1 22 L6.5 17 L10.5 26.5 L14 25 L10 15.5 L18 15.5 Z" fill="#fff" stroke="#111" strokeWidth="1.6" strokeLinejoin="round" />
      </svg>
    </>
  );
};
