import type { FrameFormat } from "../types.ts";

export const FONT = '"Segoe UI", Arial, sans-serif';

export interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}

/** Space kept free at the bottom for the burned-in subtitles (vertical formats: up to three lines of 46 px). */
export function subtitleReserve(format: FrameFormat, height: number): number {
  if (format === "16x9") return height * 0.06 + 150;
  return height * 0.07 + 215;
}

/** Space kept free at the top for a title (vertical formats: two lines of 56 px). */
export function titleReserve(format: FrameFormat, height: number, hasTitle: boolean): number {
  if (format === "16x9") return height * 0.04;
  return hasTitle ? height * 0.04 + 185 : height * 0.04;
}

/** The free rectangle for a phone body or a browser window: not under the title and not under the subtitles. */
export function freeArea(format: FrameFormat, width: number, height: number, hasTitle: boolean): Box {
  const top = titleReserve(format, height, hasTitle);
  const bottom = subtitleReserve(format, height);
  return { x: 0, y: top, w: width, h: height - top - bottom };
}

/** Phone proportions: screen 1 : 2.1, bezel 4.5 % of the screen width on each side. */
export const PHONE_SCREEN_RATIO = 2.1;
const BEZEL = 0.045;
export const PHONE_BODY_RATIO = (1 + 2 * BEZEL) / (PHONE_SCREEN_RATIO + 2 * BEZEL);

export function phoneBox(area: Box, maxW: number): Box {
  let w = Math.min(maxW, area.w, area.h * PHONE_BODY_RATIO);
  const h = w / PHONE_BODY_RATIO;
  w = h * PHONE_BODY_RATIO;
  return { x: area.x + (area.w - w) / 2, y: area.y + (area.h - h) / 2, w, h };
}

/** Screen rectangle inside the body (coordinates relative to the body). */
export function phoneScreen(body: Box): Box {
  const b = (body.w / (1 + 2 * BEZEL)) * BEZEL;
  return { x: b, y: b, w: body.w - 2 * b, h: body.h - 2 * b };
}

/** 16:9 phone: the body takes the whole height (3 % margins) and stands left of the centre; the title and subtitles use the right column. */
export function phone169(width: number, height: number): { body: Box; colX: number; colW: number } {
  const h = height * 0.94;
  const w = h * PHONE_BODY_RATIO;
  const x = width * 0.28 - w / 2;
  const body = { x, y: height * 0.03, w, h };
  const colX = x + w + width * 0.05;
  return { body, colX, colW: width - colX - width * 0.04 };
}

/** 9:16 and 4:5 phone: the body is almost as wide as the frame; its lower part may leave the frame or lie under the subtitles. */
export function phoneVertical(format: FrameFormat, width: number, height: number, hasTitle: boolean): { body: Box; visTop: number; visBottom: number } {
  const w = width * 0.9;
  const h = w / PHONE_BODY_RATIO;
  const y = hasTitle ? height * 0.025 + 150 : height * 0.03;
  const body = { x: (width - w) / 2, y, w, h };
  return { body, visTop: y, visBottom: height - subtitleReserve(format, height) };
}

export const BROWSER_BAR = 0.04; // bar height as a fraction of the content width

/** Largest browser window with the proportions of the picture that fits the area. */
export function browserBox(area: Box, imgW: number, imgH: number): { box: Box; barH: number } {
  const r = imgH / imgW;
  const cw = Math.min(area.w, area.h / (r + BROWSER_BAR));
  const barH = cw * BROWSER_BAR;
  const ch = cw * r;
  const box = { x: area.x + (area.w - cw) / 2, y: area.y + (area.h - ch - barH) / 2, w: cw, h: ch + barH };
  return { box, barH };
}
