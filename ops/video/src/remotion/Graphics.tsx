import React from "react";
import { Img, interpolate, staticFile } from "remotion";
import type { FrameFormat, ManifestShot } from "../types.ts";
import { FONT, freeArea } from "./layout.ts";

interface GraphicProps {
  shot: ManifestShot;
  format: FrameFormat;
  width: number;
  height: number;
  local: number;
}

const appear = (local: number, start: number) =>
  interpolate(local, [start, start + 12], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });

/** Boxes joined by arrows: a row in 16:9, a column in the vertical formats. */
const Flow: React.FC<GraphicProps> = ({ shot, format, width, height, local }) => {
  const steps = shot.steps ?? [];
  const area = freeArea(format, width, height, false);
  const wide = format === "16x9";
  const capH = shot.caption ? (wide ? 110 : 150) : 0;
  const n = steps.length;
  const gap = wide ? 90 : 70;
  const boxW = wide ? Math.min(380, (area.w * 0.9 - gap * (n - 1)) / n) : Math.min(area.w * 0.7, 640);
  const boxH = wide ? 190 : Math.min(190, (area.h - capH - gap * (n - 1)) / n);
  const fs = wide ? 44 : 50;
  const items: React.ReactNode[] = [];
  steps.forEach((label, i) => {
    const a = appear(local, 6 + i * 12);
    items.push(
      <div
        key={`s${i}`}
        style={{
          width: boxW,
          height: boxH,
          borderRadius: 28,
          background: "#fff",
          color: "#1b1f3b",
          fontFamily: FONT,
          fontWeight: 700,
          fontSize: fs,
          lineHeight: 1.15,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          textAlign: "center",
          padding: "0 20px",
          boxSizing: "border-box",
          boxShadow: "0 14px 40px rgba(0,0,0,0.3)",
          opacity: a,
          transform: `scale(${0.85 + 0.15 * a})`,
          flex: "none",
        }}
      >
        {label}
      </div>,
    );
    if (i < n - 1) {
      const b = appear(local, 12 + i * 12);
      items.push(
        <svg
          key={`a${i}`}
          width={wide ? gap : 60}
          height={wide ? 60 : gap}
          viewBox="0 0 60 60"
          preserveAspectRatio="none"
          style={{ opacity: b, flex: "none" }}
        >
          {wide ? (
            <path d="M2 30 H46 M34 14 L52 30 L34 46" stroke="#fff" strokeWidth="6" fill="none" strokeLinecap="round" strokeLinejoin="round" />
          ) : (
            <path d="M30 2 V46 M14 34 L30 52 L46 34" stroke="#fff" strokeWidth="6" fill="none" strokeLinecap="round" strokeLinejoin="round" />
          )}
        </svg>,
      );
    }
  });
  return (
    <div
      style={{
        position: "absolute",
        left: area.x,
        top: area.y,
        width: area.w,
        height: area.h,
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        justifyContent: "center",
        gap: wide ? 70 : 40,
      }}
    >
      <div style={{ display: "flex", flexDirection: wide ? "row" : "column", alignItems: "center" }}>{items}</div>
      {shot.caption ? (
        <div
          style={{
            fontFamily: FONT,
            fontWeight: 600,
            fontSize: wide ? 48 : 52,
            color: "#fff",
            textAlign: "center",
            maxWidth: area.w * 0.86,
            opacity: appear(local, 6 + n * 12),
          }}
        >
          {shot.caption}
        </div>
      ) : null}
    </div>
  );
};

/** Heading, QR code, text lines, optional logo. */
const Cta: React.FC<GraphicProps> = ({ shot, format, width, height, local }) => {
  const area = freeArea(format, width, height, false);
  const wide = format === "16x9";
  const qrSize = wide ? Math.min(area.h * 0.8, 560) : Math.min(area.w * 0.62, area.h * 0.42, 560);
  const text = (
    <div style={{ display: "flex", flexDirection: "column", alignItems: wide ? "flex-start" : "center", gap: wide ? 26 : 22, maxWidth: wide ? area.w * 0.48 : area.w * 0.9 }}>
      {shot.logo ? (
        <Img src={staticFile(shot.logo)} style={{ height: wide ? 90 : 80, width: "auto", opacity: appear(local, 2) }} />
      ) : null}
      {shot.heading ? (
        <div
          style={{
            fontFamily: FONT,
            fontWeight: 800,
            fontSize: wide ? 78 : 70,
            lineHeight: 1.1,
            color: "#fff",
            textAlign: wide ? "left" : "center",
            opacity: appear(local, 4),
          }}
        >
          {shot.heading}
        </div>
      ) : null}
      {(shot.lines ?? []).map((l, i) => (
        <div
          key={i}
          style={{
            fontFamily: FONT,
            fontWeight: 600,
            fontSize: wide ? 50 : 48,
            color: "#fff",
            opacity: appear(local, 12 + i * 8) * 0.95,
          }}
        >
          {l}
        </div>
      ))}
    </div>
  );
  const qr = shot.qr ? (
    <div
      style={{
        width: qrSize,
        height: qrSize,
        background: "#fff",
        borderRadius: 28,
        padding: qrSize * 0.06,
        boxSizing: "border-box",
        boxShadow: "0 14px 40px rgba(0,0,0,0.3)",
        opacity: appear(local, 8),
        flex: "none",
      }}
    >
      <Img src={shot.qr} style={{ width: "100%", height: "100%", display: "block" }} />
    </div>
  ) : null;
  return (
    <div
      style={{
        position: "absolute",
        left: area.x,
        top: area.y,
        width: area.w,
        height: area.h,
        display: "flex",
        flexDirection: wide ? "row" : "column",
        alignItems: "center",
        justifyContent: "center",
        gap: wide ? 110 : 44,
      }}
    >
      {wide ? qr : text}
      {wide ? text : qr}
    </div>
  );
};

/** A scene that is not filmed yet: the frame says so. */
const Placeholder: React.FC<GraphicProps> = ({ shot, format, width, height, local }) => {
  const area = freeArea(format, width, height, false);
  const wide = format === "16x9";
  const m = wide ? 60 : 50;
  return (
    <div
      style={{
        position: "absolute",
        left: area.x + m,
        top: area.y + m,
        width: area.w - 2 * m,
        height: area.h - 2 * m,
        border: "6px dashed rgba(255,255,255,0.75)",
        borderRadius: 36,
        boxSizing: "border-box",
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        justifyContent: "center",
        gap: 36,
        opacity: appear(local, 2),
      }}
    >
      <svg width={wide ? 150 : 170} height={wide ? 150 : 170} viewBox="0 0 64 64" fill="none" stroke="#fff" strokeWidth="3.4" strokeLinejoin="round">
        <path d="M8 20 H18 L22 14 H42 L46 20 H56 V50 H8 Z" />
        <circle cx="32" cy="35" r="9" />
      </svg>
      <div
        style={{
          fontFamily: FONT,
          fontWeight: 700,
          fontSize: wide ? 56 : 54,
          color: "#fff",
          textAlign: "center",
          maxWidth: "85%",
          lineHeight: 1.2,
        }}
      >
        {shot.caption}
      </div>
    </div>
  );
};

export const Graphic: React.FC<GraphicProps> = (p) => {
  if (p.shot.graphic === "flow") return <Flow {...p} />;
  if (p.shot.graphic === "cta") return <Cta {...p} />;
  return <Placeholder {...p} />;
};
