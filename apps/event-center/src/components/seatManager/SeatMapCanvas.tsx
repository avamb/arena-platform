/**
 * The map of the seat manager: the hall as SVG, with pan, zoom and the ways of
 * picking seats. Touch is the main input (the screen opens inside Telegram), so:
 *
 *  - no hover anywhere, every control is a finger-sized press;
 *  - two fingers always move and zoom the map, whatever the tool is;
 *  - one finger moves the map with the "Move" tool, and in the picking tools
 *    (seat, row, section) a drag moves the map while a tap picks;
 *  - one finger DRAWS with the rectangle and lasso tools;
 *  - the mouse wheel zooms on a desktop.
 *
 * The geometry of the gestures (pan, pinch, wheel, fit) is the widget's own
 * pan-zoom module, so the organizer's map behaves like the buyer's.
 */
import { forwardRef, useCallback, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef } from "react";
import type { Geometry } from "@seatmap/types";
import {
  IDENTITY,
  fitTransform,
  pan,
  pinchZoom,
  pointerDist,
  pointerMid,
  toCSSTransform,
  wheelZoom,
  type PanZoomState,
} from "@seatmap/lib/pan-zoom";
import {
  normalizeRect,
  rowSeatsOf,
  sectionSeatsOf,
  seatAt,
  seatsInPolygon,
  seatsInRect,
  type HallIndex,
  type Point,
} from "@/lib/seatManager/model";
import {
  buildHallSvg,
  collectSeatNodes,
  drawShape,
  drawStage,
  fitHandles,
  recolor,
  type SeatLook,
} from "@/lib/seatManager/render";

export type MapTool = "pan" | "seat" | "row" | "section" | "rect" | "lasso" | "stage";

export interface SeatMapHandle {
  fit: () => void;
  zoomBy: (factor: number) => void;
}

interface Props {
  geometry: Geometry;
  index: HallIndex;
  /** The accessible name of the map. */
  label: string;
  look: (seatKey: string) => SeatLook;
  tool: MapTool;
  stage: Point | null;
  /** Seats picked by a tap or a drawn shape. */
  onSeats: (keys: string[], tool: MapTool) => void;
  /** A row handle was pressed. */
  onRowHandle: (rowId: string, additive: boolean) => void;
  /** The stage was pointed at (tool "stage"). */
  onStage: (p: Point) => void;
}

/** A finger moves a little while it taps: further than this it is a drag. */
const TAP_SLOP = 8;
/** Extra reach around a seat, in screen pixels: a seat is small under a finger. */
const SEAT_SLACK_PX = 10;

type Mode = "none" | "tap" | "pan" | "pinch" | "draw";

export const SeatMapCanvas = forwardRef<SeatMapHandle, Props>(function SeatMapCanvas(props, ref) {
  const { geometry, index, label } = props;
  const viewport = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  const nodes = useRef<Map<string, SVGCircleElement>>(new Map());
  const view = useRef<PanZoomState>(IDENTITY);
  const moved = useRef(false);
  const latest = useRef(props);
  latest.current = props;

  const svg = useMemo(() => buildHallSvg(geometry, index, label), [geometry, index, label]);

  const apply = useCallback(() => {
    const el = inner.current;
    if (el === null) return;
    el.style.transform = toCSSTransform(view.current);
    fitHandles(el, latest.current.index.minHandleGap, view.current.scale);
    drawStage(el, latest.current.stage, view.current.scale);
    viewport.current?.setAttribute("data-scale", view.current.scale.toFixed(3));
  }, []);

  const fit = useCallback(() => {
    const vp = viewport.current;
    if (vp === null) return;
    const { width, height } = vp.getBoundingClientRect();
    view.current = fitTransform(latest.current.index.width, latest.current.index.height, width, height, 8);
    moved.current = false;
    apply();
  }, [apply]);

  useImperativeHandle(
    ref,
    () => ({
      fit,
      zoomBy: (factor: number) => {
        const vp = viewport.current;
        if (vp === null) return;
        const r = vp.getBoundingClientRect();
        view.current = wheelZoom(view.current, factor > 1 ? -1 : 1, r.width / 2, r.height / 2);
        moved.current = true;
        apply();
      },
    }),
    [fit, apply],
  );

  // A new hall: collect its seat nodes, paint them, fit the view.
  useLayoutEffect(() => {
    const el = inner.current;
    if (el === null) return;
    nodes.current = collectSeatNodes(el);
    recolor(nodes.current, latest.current.look);
    fit();
  }, [svg, fit]);

  // The tray, the preview or the server changed what a seat looks like: a new `look`.
  useEffect(() => {
    recolor(nodes.current, props.look);
  }, [props.look]);

  // The stage marker moved.
  useEffect(() => {
    if (inner.current !== null) drawStage(inner.current, props.stage, view.current.scale);
  }, [props.stage]);

  // Keep the whole hall in view while the person has not moved the map.
  useEffect(() => {
    const vp = viewport.current;
    if (vp === null || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      if (!moved.current) fit();
    });
    ro.observe(vp);
    return () => ro.disconnect();
  }, [fit]);

  // Wheel zoom needs a non-passive listener to stop the page from scrolling.
  useEffect(() => {
    const vp = viewport.current;
    if (vp === null) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const r = vp.getBoundingClientRect();
      view.current = wheelZoom(view.current, e.deltaY, e.clientX - r.left, e.clientY - r.top);
      moved.current = true;
      apply();
    };
    vp.addEventListener("wheel", onWheel, { passive: false });
    return () => vp.removeEventListener("wheel", onWheel);
  }, [apply]);

  // ── gestures ──────────────────────────────────────────────────────────
  const pointers = useRef(new Map<number, Point>());
  const g = useRef<{
    mode: Mode;
    start: Point;
    last: Point;
    row: string | null;
    lastDist: number;
    lastMid: Point;
    lasso: Point[];
    rectStart: Point;
    additive: boolean;
  }>({
    mode: "none",
    start: { x: 0, y: 0 },
    last: { x: 0, y: 0 },
    row: null,
    lastDist: 0,
    lastMid: { x: 0, y: 0 },
    lasso: [],
    rectStart: { x: 0, y: 0 },
    additive: false,
  });

  const local = (e: { clientX: number; clientY: number }): Point => {
    const r = viewport.current!.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  };
  const toCanvas = (p: Point): Point => ({
    x: (p.x - view.current.x) / view.current.scale,
    y: (p.y - view.current.y) / view.current.scale,
  });

  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    const vp = viewport.current;
    if (vp === null) return;
    if (e.pointerType === "mouse" && e.button !== 0) return;
    try {
      vp.setPointerCapture(e.pointerId);
    } catch {
      // A synthetic event has no active pointer: the gesture still works without capture.
    }
    const p = local(e);
    pointers.current.set(e.pointerId, p);
    const st = g.current;
    if (pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()] as [Point, Point];
      st.mode = "pinch";
      st.lastDist = pointerDist(a, b);
      st.lastMid = pointerMid(a, b);
      if (inner.current !== null) drawShape(inner.current, null, view.current.scale);
      return;
    }
    if (pointers.current.size > 2) return;
    st.start = p;
    st.last = p;
    st.additive = e.shiftKey || e.ctrlKey || e.metaKey;
    const handle = (e.target as Element).closest?.("[data-row]");
    st.row = handle?.getAttribute("data-row") ?? null;
    const t = latest.current.tool;
    if ((t === "rect" || t === "lasso") && st.row === null) {
      st.mode = "draw";
      st.rectStart = toCanvas(p);
      st.lasso = [st.rectStart];
    } else {
      st.mode = "tap";
    }
  };

  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!pointers.current.has(e.pointerId)) return;
    const p = local(e);
    pointers.current.set(e.pointerId, p);
    const st = g.current;
    const el = inner.current;
    if (st.mode === "pinch" && pointers.current.size >= 2) {
      const [a, b] = [...pointers.current.values()] as [Point, Point];
      const dist = pointerDist(a, b);
      const mid = pointerMid(a, b);
      let next = pinchZoom(view.current, st.lastDist, dist, mid.x, mid.y);
      next = pan(next, mid.x - st.lastMid.x, mid.y - st.lastMid.y);
      view.current = next;
      st.lastDist = dist;
      st.lastMid = mid;
      moved.current = true;
      apply();
      return;
    }
    if (st.mode === "tap") {
      if (Math.hypot(p.x - st.start.x, p.y - st.start.y) > TAP_SLOP) st.mode = "pan";
    }
    if (st.mode === "pan") {
      view.current = pan(view.current, p.x - st.last.x, p.y - st.last.y);
      st.last = p;
      moved.current = true;
      apply();
      return;
    }
    if (st.mode === "draw" && el !== null) {
      const c = toCanvas(p);
      if (latest.current.tool === "rect") {
        const r = normalizeRect(st.rectStart, c);
        drawShape(el, { kind: "rect", ...r }, view.current.scale);
      } else {
        const lastPoint = st.lasso[st.lasso.length - 1]!;
        if (Math.hypot(c.x - lastPoint.x, c.y - lastPoint.y) * view.current.scale > 3) st.lasso.push(c);
        drawShape(el, { kind: "lasso", points: st.lasso }, view.current.scale);
      }
    }
  };

  const finish = (e: React.PointerEvent<HTMLDivElement>, cancelled: boolean) => {
    const st = g.current;
    const had = pointers.current.has(e.pointerId);
    pointers.current.delete(e.pointerId);
    try {
      viewport.current?.releasePointerCapture(e.pointerId);
    } catch {
      // Already released.
    }
    if (!had) return;
    const el = inner.current;
    if (st.mode === "pinch") {
      if (pointers.current.size === 1) {
        // One finger is left: it moves the map, without a jump.
        st.mode = "pan";
        st.last = [...pointers.current.values()][0]!;
      } else if (pointers.current.size === 0) st.mode = "none";
      return;
    }
    const p = local(e);
    const { index: idx, tool, onSeats, onRowHandle, onStage } = latest.current;
    if (st.mode === "draw") {
      if (el !== null) drawShape(el, null, view.current.scale);
      st.mode = "none";
      if (cancelled) return;
      const c = toCanvas(p);
      let keys: string[] = [];
      if (tool === "rect") {
        const tiny = Math.hypot(p.x - st.start.x, p.y - st.start.y) < TAP_SLOP;
        if (tiny) {
          const s = seatAt(idx, c, SEAT_SLACK_PX / view.current.scale);
          keys = s === null ? [] : [s.key];
        } else keys = seatsInRect(idx, normalizeRect(st.rectStart, c));
      } else {
        keys = seatsInPolygon(idx, st.lasso);
      }
      if (keys.length > 0) onSeats(keys, tool);
      return;
    }
    if (st.mode === "tap" && !cancelled) {
      st.mode = "none";
      if (st.row !== null && tool !== "stage") {
        onRowHandle(st.row, st.additive);
        return;
      }
      const c = toCanvas(p);
      if (tool === "stage") {
        onStage(c);
        return;
      }
      if (tool === "pan" || tool === "rect" || tool === "lasso") return;
      const s = seatAt(idx, c, SEAT_SLACK_PX / view.current.scale);
      if (s === null) return;
      if (tool === "seat") onSeats([s.key], tool);
      else if (tool === "row") onSeats([...rowSeatsOf(idx, s.key)], tool);
      else if (tool === "section") onSeats([...sectionSeatsOf(idx, s.key)], tool);
      return;
    }
    st.mode = "none";
  };

  return (
    <div
      ref={viewport}
      className="sm-viewport"
      data-testid="seat-map"
      data-tool={props.tool}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={(e) => finish(e, false)}
      onPointerCancel={(e) => finish(e, true)}
    >
      <div ref={inner} className="sm-inner" dangerouslySetInnerHTML={{ __html: svg }} />
    </div>
  );
});
