import React from "react";
import type { Box } from "./layout.ts";
import { phoneScreen } from "./layout.ts";

/** A phone body (dark edge, round corners); children are drawn on the screen and clipped by it. */
export const PhoneFrame: React.FC<{ body: Box; children: (screen: Box) => React.ReactNode }> = ({ body, children }) => {
  const screen = phoneScreen(body);
  return (
    <div
      style={{
        position: "absolute",
        left: body.x,
        top: body.y,
        width: body.w,
        height: body.h,
        borderRadius: body.w * 0.15,
        background: "linear-gradient(145deg, #2b2f3a, #0d0f14)",
        boxShadow: "0 30px 80px rgba(0,0,0,0.45), inset 0 0 0 2px rgba(255,255,255,0.10)",
      }}
    >
      <div
        style={{
          position: "absolute",
          left: screen.x,
          top: screen.y,
          width: screen.w,
          height: screen.h,
          borderRadius: body.w * 0.115,
          overflow: "hidden",
          background: "#111",
        }}
      >
        {children(screen)}
      </div>
      {/* camera island */}
      <div
        style={{
          position: "absolute",
          left: "50%",
          // A thin speaker slit in the body, above the screen: a camera island inside the
          // screen covered the header of the captured page.
          top: Math.max(2, screen.y - screen.w * 0.03),
          width: screen.w * 0.14,
          height: Math.max(3, screen.w * 0.012),
          marginLeft: -screen.w * 0.07,
          borderRadius: screen.w * 0.006,
          background: "#2a2f45",
        }}
      />
    </div>
  );
};
