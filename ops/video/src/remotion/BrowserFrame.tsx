import React from "react";
import type { Box } from "./layout.ts";

/** A simple browser window: bar with three dots, the picture below. Children are drawn in the content area. */
export const BrowserFrame: React.FC<{ box: Box; barH: number; children: (content: Box) => React.ReactNode }> = ({
  box,
  barH,
  children,
}) => {
  const dot = barH * 0.3;
  return (
    <div
      style={{
        position: "absolute",
        left: box.x,
        top: box.y,
        width: box.w,
        height: box.h,
        borderRadius: barH * 0.45,
        overflow: "hidden",
        background: "#fff",
        boxShadow: "0 24px 70px rgba(0,0,0,0.42), 0 0 0 2px rgba(255,255,255,0.12)",
      }}
    >
      <div
        style={{
          position: "absolute",
          left: 0,
          top: 0,
          width: "100%",
          height: barH,
          background: "#e7e9ee",
          display: "flex",
          alignItems: "center",
          gap: dot * 0.8,
          paddingLeft: barH * 0.45,
          boxSizing: "border-box",
        }}
      >
        {["#ff5f57", "#febc2e", "#28c840"].map((c) => (
          <div key={c} style={{ width: dot, height: dot, borderRadius: "50%", background: c }} />
        ))}
        <div style={{ marginLeft: barH * 0.4, height: barH * 0.5, flex: 1, maxWidth: "55%", borderRadius: barH * 0.25, background: "#fff" }} />
      </div>
      <div style={{ position: "absolute", left: 0, top: barH, width: box.w, height: box.h - barH, overflow: "hidden" }}>
        {children({ x: 0, y: 0, w: box.w, h: box.h - barH })}
      </div>
    </div>
  );
};
