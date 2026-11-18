import type { Cue } from "./types.ts";

const LINE_MAX = 42;
const LINES_MAX = 2;

/** Splits text into lines of at most LINE_MAX characters (a longer word stays whole). */
function wrapLines(words: string[]): string[] {
  const lines: string[] = [];
  let cur = "";
  for (const w of words) {
    if (!cur) cur = w;
    else if (cur.length + 1 + w.length <= LINE_MAX) cur += ` ${w}`;
    else {
      lines.push(cur);
      cur = w;
    }
  }
  if (cur) lines.push(cur);
  return lines;
}

/** Narrator text -> cue texts of at most two lines of 42 characters. Sentences start new cues. */
export function splitCues(text: string): string[] {
  const sentences = text.replace(/\s+/g, " ").trim().match(/[^.!?…]+[.!?…]*\s*/g) ?? [text];
  const cues: string[] = [];
  for (const s of sentences) {
    const lines = wrapLines(s.trim().split(" ").filter(Boolean));
    for (let i = 0; i < lines.length; i += LINES_MAX) {
      cues.push(lines.slice(i, i + LINES_MAX).join("\n"));
    }
  }
  return cues;
}

/** Divides [start, start+duration] between cues in proportion to their length. */
export function timeCues(text: string, start: number, duration: number): Cue[] {
  const parts = splitCues(text);
  const total = parts.reduce((n, p) => n + p.replace(/\n/g, " ").length, 0) || 1;
  const cues: Cue[] = [];
  let t = start;
  for (const p of parts) {
    const d = (duration * p.replace(/\n/g, " ").length) / total;
    cues.push({ start: t, end: t + d, text: p });
    t += d;
  }
  return cues;
}

function pad(n: number, w: number): string {
  return String(n).padStart(w, "0");
}

function stamp(sec: number, sep: "," | "."): string {
  const ms = Math.round(sec * 1000);
  const h = Math.floor(ms / 3600000);
  const m = Math.floor((ms % 3600000) / 60000);
  const s = Math.floor((ms % 60000) / 1000);
  return `${pad(h, 2)}:${pad(m, 2)}:${pad(s, 2)}${sep}${pad(ms % 1000, 3)}`;
}

export function toSrt(cues: Cue[]): string {
  return (
    cues.map((c, i) => `${i + 1}\n${stamp(c.start, ",")} --> ${stamp(c.end, ",")}\n${c.text}\n`).join("\n")
  );
}

export function toVtt(cues: Cue[]): string {
  return `WEBVTT\n\n${cues.map((c) => `${stamp(c.start, ".")} --> ${stamp(c.end, ".")}\n${c.text}\n`).join("\n")}`;
}
