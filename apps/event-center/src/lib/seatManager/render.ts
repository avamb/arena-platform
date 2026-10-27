/**
 * The hall as SVG for the seat manager.
 *
 * The markup is built ONCE per hall (a string, set as innerHTML, no inline
 * style: the page's CSP allows only style-src 'self'). After that only
 * presentation attributes change: recolor() paints every seat from a look
 * function, fitHandles() sizes the row handles to the zoom. A hall of 1 500
 * seats is therefore painted in a few milliseconds and a stroke of the brush
 * never rebuilds the DOM (the widget's own approach, WID-B).
 *
 * The decor layer is an organizer's upload and goes through the widget's
 * allowlist sanitizer before it is placed in the markup.
 */
import type { Geometry } from "@seatmap/types";
import { sanitizeDecorSvg } from "@seatmap/lib/svg-sanitize";
import type { HallIndex } from "@/lib/seatManager/model";

export function esc(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

/** How one seat is drawn. */
export interface SeatLook {
  readonly fill: string;
  readonly stroke: string;
  readonly strokeWidth: number;
  readonly dashed: boolean;
}

export const HATCH_FILL = "url(#sm-hatch)";

/** The row handles are at least this big on screen, at any zoom (a finger), when the rows leave room. */
export const HANDLE_SCREEN_RADIUS = 22;
/** Below this on-screen radius a handle is not worth drawing: use the "Row" tool instead. */
export const HANDLE_MIN_SCREEN_RADIUS = 11;

export function buildHallSvg(geometry: Geometry, index: HallIndex, label: string): string {
  const w = index.width;
  const h = index.height;
  const parts: string[] = [];
  parts.push(
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${w} ${h}" width="${w}" height="${h}" role="img" aria-label="${esc(label)}" data-hall="1">`,
  );
  parts.push(
    `<defs><pattern id="sm-hatch" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">` +
      `<rect width="6" height="6" fill="#ffffff"/><line x1="0" y1="0" x2="0" y2="6" stroke="#9ca3af" stroke-width="2"/></pattern></defs>`,
  );
  if (geometry.decor_svg) {
    const safe = sanitizeDecorSvg(geometry.decor_svg);
    if (safe) parts.push(`<g id="sm-decor" aria-hidden="true">${safe}</g>`);
  }
  // General-admission areas have no seats to paint: drawn faint, for orientation only.
  const gaAreas = geometry.categories.filter((c) => c.kind === "general_admission" && c.polygon && c.polygon.length >= 3);
  if (gaAreas.length > 0) {
    parts.push(`<g id="sm-ga" aria-hidden="true">`);
    for (const c of gaAreas) {
      const pts = c.polygon!.map((p) => `${p.x},${p.y}`).join(" ");
      parts.push(`<polygon points="${pts}" fill="#e5e7eb" fill-opacity="0.5" stroke="#9ca3af" stroke-width="1" stroke-dasharray="4 3"/>`);
    }
    parts.push(`</g>`);
  }
  parts.push(`<g id="sm-seats">`);
  for (const s of index.seats) {
    parts.push(`<circle data-k="${esc(s.key)}" cx="${s.x}" cy="${s.y}" r="${s.radius}" fill="#d1d5db" stroke="none"/>`);
  }
  parts.push(`</g>`);
  parts.push(`<g id="sm-handles">`);
  for (const r of index.rows) {
    parts.push(
      `<g data-row="${esc(r.id)}" data-hx="${r.handle.x}" data-hy="${r.handle.y}">` +
        `<circle cx="${r.handle.x}" cy="${r.handle.y}" r="${r.seatRadius * 1.6}" fill="#ffffff" fill-opacity="0.92" stroke="#4b5563" stroke-width="1.5"/>` +
        `<text x="${r.handle.x}" y="${r.handle.y}" text-anchor="middle" dominant-baseline="central" fill="#111827" font-size="${r.seatRadius * 1.5}" font-weight="700" pointer-events="none">${esc(r.name)}</text></g>`,
    );
  }
  parts.push(`</g>`);
  parts.push(`<g id="sm-overlay" pointer-events="none"></g>`);
  parts.push(`</svg>`);
  return parts.join("");
}

/** The seat circles of a mounted hall, by seat key. */
export function collectSeatNodes(root: ParentNode): Map<string, SVGCircleElement> {
  const out = new Map<string, SVGCircleElement>();
  root.querySelectorAll<SVGCircleElement>("#sm-seats circle[data-k]").forEach((c) => {
    const k = c.getAttribute("data-k");
    if (k !== null) out.set(k, c);
  });
  return out;
}

const painted = new WeakMap<Element, string>();

/** Paints every seat. A seat whose look did not change is not touched. */
export function recolor(nodes: ReadonlyMap<string, SVGCircleElement>, look: (key: string) => SeatLook): void {
  for (const [key, node] of nodes) {
    const l = look(key);
    const sig = `${l.fill}|${l.stroke}|${l.strokeWidth}|${l.dashed ? 1 : 0}`;
    if (painted.get(node) === sig) continue;
    painted.set(node, sig);
    node.setAttribute("fill", l.fill);
    node.setAttribute("stroke", l.stroke);
    node.setAttribute("stroke-width", String(l.strokeWidth));
    if (l.dashed) node.setAttribute("stroke-dasharray", "2 1.5");
    else node.removeAttribute("stroke-dasharray");
  }
}

export interface HandleFit {
  /** Radius in canvas units, or 0 when the handles are hidden. */
  readonly radius: number;
  readonly visible: boolean;
}

/**
 * The size of the row handles at a zoom. A handle is as big as a finger
 * (HANDLE_SCREEN_RADIUS) unless the rows are too close for it; when even the
 * smallest useful handle would overlap its neighbour the handles are hidden
 * and the "Row" tool and the list of rows do the job.
 */
export function handleFit(minGap: number, scale: number): HandleFit {
  if (!Number.isFinite(minGap) || scale <= 0) return { radius: 0, visible: false };
  const room = (minGap * scale) / 2 - 1;
  const screen = Math.min(HANDLE_SCREEN_RADIUS, room);
  if (screen < HANDLE_MIN_SCREEN_RADIUS) return { radius: 0, visible: false };
  return { radius: screen / scale, visible: true };
}

/** Sizes and shows or hides the row handles for the current zoom. */
export function fitHandles(root: ParentNode, minGap: number, scale: number): HandleFit {
  const fit = handleFit(minGap, scale);
  root.querySelectorAll<SVGGElement>("#sm-handles > g[data-row]").forEach((g) => {
    if (!fit.visible) {
      g.setAttribute("display", "none");
      return;
    }
    g.removeAttribute("display");
    const circle = g.querySelector("circle");
    const text = g.querySelector("text");
    circle?.setAttribute("r", String(fit.radius));
    text?.setAttribute("font-size", String(fit.radius * 1.1));
  });
  return fit;
}

const SVG_NS = "http://www.w3.org/2000/svg";

function overlayGroup(root: ParentNode): SVGGElement | null {
  return root.querySelector<SVGGElement>("#sm-overlay");
}

/** Draws (or clears, with null) the rectangle or lasso being dragged. */
export function drawShape(
  root: ParentNode,
  shape: { kind: "rect"; x0: number; y0: number; x1: number; y1: number } | { kind: "lasso"; points: readonly { x: number; y: number }[] } | null,
  scale: number,
): void {
  const g = overlayGroup(root);
  if (g === null) return;
  g.querySelectorAll("[data-shape]").forEach((n) => n.remove());
  if (shape === null) return;
  const sw = String(2 / Math.max(scale, 0.01));
  if (shape.kind === "rect") {
    const r = document.createElementNS(SVG_NS, "rect");
    r.setAttribute("data-shape", "rect");
    r.setAttribute("x", String(shape.x0));
    r.setAttribute("y", String(shape.y0));
    r.setAttribute("width", String(shape.x1 - shape.x0));
    r.setAttribute("height", String(shape.y1 - shape.y0));
    r.setAttribute("fill", "#4f46e5");
    r.setAttribute("fill-opacity", "0.12");
    r.setAttribute("stroke", "#4f46e5");
    r.setAttribute("stroke-width", sw);
    r.setAttribute("stroke-dasharray", `${6 / Math.max(scale, 0.01)} ${4 / Math.max(scale, 0.01)}`);
    g.appendChild(r);
    return;
  }
  const poly = document.createElementNS(SVG_NS, "polygon");
  poly.setAttribute("data-shape", "lasso");
  poly.setAttribute("points", shape.points.map((p) => `${p.x},${p.y}`).join(" "));
  poly.setAttribute("fill", "#4f46e5");
  poly.setAttribute("fill-opacity", "0.12");
  poly.setAttribute("stroke", "#4f46e5");
  poly.setAttribute("stroke-width", sw);
  g.appendChild(poly);
}

/** The stage marker (the focal point the allocator needs), or none. */
export function drawStage(root: ParentNode, point: { x: number; y: number } | null, scale: number): void {
  const g = overlayGroup(root);
  if (g === null) return;
  g.querySelectorAll("[data-stage]").forEach((n) => n.remove());
  if (point === null) return;
  const r = 14 / Math.max(scale, 0.01);
  const sw = String(3 / Math.max(scale, 0.01));
  const grp = document.createElementNS(SVG_NS, "g");
  grp.setAttribute("data-stage", "1");
  const ring = document.createElementNS(SVG_NS, "circle");
  ring.setAttribute("cx", String(point.x));
  ring.setAttribute("cy", String(point.y));
  ring.setAttribute("r", String(r));
  ring.setAttribute("fill", "#ffffff");
  ring.setAttribute("fill-opacity", "0.7");
  ring.setAttribute("stroke", "#be123c");
  ring.setAttribute("stroke-width", sw);
  grp.appendChild(ring);
  for (const [dx, dy] of [
    [1, 0],
    [0, 1],
  ] as const) {
    const l = document.createElementNS(SVG_NS, "line");
    l.setAttribute("x1", String(point.x - dx * r * 1.6));
    l.setAttribute("y1", String(point.y - dy * r * 1.6));
    l.setAttribute("x2", String(point.x + dx * r * 1.6));
    l.setAttribute("y2", String(point.y + dy * r * 1.6));
    l.setAttribute("stroke", "#be123c");
    l.setAttribute("stroke-width", sw);
    grp.appendChild(l);
  }
  g.appendChild(grp);
}
